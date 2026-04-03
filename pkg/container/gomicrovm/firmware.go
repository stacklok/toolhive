// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package gomicrovm

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/gofrs/flock"
)

const (
	// firmwareLibName is the shared library filename for libkrunfw on Linux.
	firmwareLibName = "libkrunfw.so.5"

	// maxFirmwareArchiveSize caps firmware downloads to 64 MiB.
	maxFirmwareArchiveSize = 64 << 20
	// maxFirmwareExtractSize caps extracted firmware to 128 MiB.
	maxFirmwareExtractSize = 128 << 20
	// maxFirmwareTarEntries caps the number of tar entries to prevent inode exhaustion.
	maxFirmwareTarEntries = 1000

	// firmwareHTTPTimeout is the timeout for GitHub API requests.
	firmwareHTTPTimeout = 30 * time.Second
	// firmwareDownloadTimeout is the timeout for downloading firmware archives.
	firmwareDownloadTimeout = 2 * time.Minute
)

// firmwareManifest records metadata about cached firmware for validation.
type firmwareManifest struct {
	Version     string    `json:"version"`
	Arch        string    `json:"arch"`
	LibraryHash string    `json:"library_hash"`
	Timestamp   time.Time `json:"timestamp"`
}

// ResolveFirmware returns the directory containing libkrunfw.so.5.
// It first attempts to download the firmware from go-microvm GitHub releases
// into a versioned cache directory. If the download fails, it falls back to
// well-known system library paths.
//
// The version parameter should be a go-microvm release tag (e.g. "v0.0.23").
// The cacheDir parameter is the root directory for firmware caching (e.g.
// "$XDG_STATE_HOME/toolhive/gomicrovm/cache").
func ResolveFirmware(ctx context.Context, version, cacheDir string) (string, error) {
	if version == "" {
		return "", errors.New("firmware version is required")
	}
	if cacheDir == "" {
		return "", errors.New("firmware cache directory is required")
	}

	dir, err := downloadFirmware(ctx, cacheDir, version)
	if err == nil {
		return dir, nil
	}

	slog.WarnContext(ctx, "firmware download failed, falling back to system paths", "error", err)

	dir, sysErr := findSystemFirmware()
	if sysErr == nil {
		return dir, nil
	}
	return "", fmt.Errorf("resolve firmware: download failed: %w; system lookup failed: %v", err, sysErr)
}

// downloadFirmware downloads and caches the firmware for the current architecture.
// Returns the directory containing libkrunfw.so.5.
func downloadFirmware(ctx context.Context, cacheRoot, version string) (string, error) {
	arch := runtime.GOARCH
	cacheDir := filepath.Join(cacheRoot, "firmware", version, "linux-"+arch)
	manifestPath := filepath.Join(cacheDir, "firmware.json")

	if err := ensureSecureFirmwareCacheRoot(cacheRoot); err != nil {
		return "", err
	}

	lockDir := filepath.Join(cacheRoot, "firmware")
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return "", fmt.Errorf("create firmware lock directory: %w", err)
	}
	lock := flock.New(filepath.Join(lockDir, ".firmware.lock"))
	if err := lock.Lock(); err != nil {
		return "", fmt.Errorf("acquire firmware lock: %w", err)
	}
	defer func() { _ = lock.Unlock() }()

	// Check for a valid cache hit.
	if dir, ok := checkFirmwareCache(ctx, cacheDir, manifestPath); ok {
		return dir, nil
	}

	// Cache miss or invalid -- clear and re-download.
	if err := os.RemoveAll(cacheDir); err != nil {
		return "", fmt.Errorf("clear firmware cache: %w", err)
	}

	// Fetch release asset metadata and checksums from GitHub.
	assets, err := fetchFirmwareReleaseAssets(ctx, version)
	if err != nil {
		return "", fmt.Errorf("fetch release assets: %w", err)
	}

	checksumURL, ok := assets["sha256sums.txt"]
	if !ok {
		return "", errors.New("sha256sums.txt not found in release")
	}
	checksums, err := downloadFirmwareChecksums(ctx, checksumURL)
	if err != nil {
		return "", fmt.Errorf("download firmware checksums: %w", err)
	}

	// Try architecture name candidates (e.g. amd64 and x86_64).
	candidates := firmwareArchCandidates(arch)
	var lastErr error
	for _, candidate := range candidates {
		dir, err := tryDownloadFirmwareArch(ctx, cacheRoot, cacheDir, manifestPath, version, candidate, assets, checksums)
		if err == nil {
			return dir, nil
		}
		lastErr = err
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no firmware archive found for arch %s", arch)
	}
	return "", lastErr
}

// checkFirmwareCache validates a cached firmware directory against its manifest.
// Returns the directory containing libkrunfw and true if the cache is valid.
func checkFirmwareCache(ctx context.Context, cacheDir, manifestPath string) (string, bool) {
	manifest, ok := readFirmwareManifest(manifestPath)
	if !ok || manifest.LibraryHash == "" {
		return "", false
	}

	fwPath, err := findFirmwareInDir(cacheDir)
	if err != nil {
		return "", false
	}

	fileHash, err := hashFile(fwPath)
	if err != nil {
		return "", false
	}

	if fileHash != manifest.LibraryHash {
		return "", false
	}

	fwDir := filepath.Dir(fwPath)
	slog.DebugContext(ctx, "firmware cache hit", "dir", fwDir, "version", manifest.Version)
	return fwDir, true
}

// tryDownloadFirmwareArch attempts to download, verify, and extract firmware
// for a specific architecture candidate name.
func tryDownloadFirmwareArch(
	ctx context.Context,
	cacheRoot, cacheDir, manifestPath, version, archCandidate string,
	assets, checksums map[string]string,
) (string, error) {
	archiveName := fmt.Sprintf("go-microvm-firmware-linux-%s.tar.gz", archCandidate)
	checksum, ok := checksums[archiveName]
	if !ok {
		return "", fmt.Errorf("no checksum for %s", archiveName)
	}
	archiveURL, ok := assets[archiveName]
	if !ok {
		return "", fmt.Errorf("no release asset for %s", archiveName)
	}

	// Download the archive to a temp file.
	tmpArchive, err := os.CreateTemp(cacheRoot, "firmware-*.tar.gz")
	if err != nil {
		return "", fmt.Errorf("create firmware temp archive: %w", err)
	}
	tmpArchivePath := tmpArchive.Name()
	cleanupArchive := func() {
		_ = tmpArchive.Close()
		_ = os.Remove(tmpArchivePath)
	}

	archiveHash, err := downloadToFile(ctx, archiveURL, tmpArchive, maxFirmwareArchiveSize)
	if err != nil {
		cleanupArchive()
		return "", err
	}
	if !strings.EqualFold(archiveHash, checksum) {
		cleanupArchive()
		return "", fmt.Errorf("firmware checksum mismatch: expected %s got %s", checksum, archiveHash)
	}
	if err := tmpArchive.Close(); err != nil {
		_ = os.Remove(tmpArchivePath)
		return "", fmt.Errorf("close firmware archive: %w", err)
	}

	// Extract to a temp directory, then atomically rename to cacheDir.
	tmpDir, err := os.MkdirTemp(cacheRoot, "firmware-extract-")
	if err != nil {
		_ = os.Remove(tmpArchivePath)
		return "", fmt.Errorf("create firmware temp dir: %w", err)
	}
	cleanupDir := func() { _ = os.RemoveAll(tmpDir) }

	if err := extractFirmwareTarGz(tmpArchivePath, tmpDir); err != nil {
		cleanupDir()
		_ = os.Remove(tmpArchivePath)
		return "", fmt.Errorf("extract firmware archive: %w", err)
	}

	// Verify the firmware file exists in the extracted content.
	if _, err := findFirmwareInDir(tmpDir); err != nil {
		cleanupDir()
		_ = os.Remove(tmpArchivePath)
		return "", errors.New("firmware archive missing " + firmwareLibName)
	}

	// Move extracted content to the final cache location.
	if err := os.MkdirAll(filepath.Dir(cacheDir), 0o700); err != nil {
		cleanupDir()
		_ = os.Remove(tmpArchivePath)
		return "", fmt.Errorf("create firmware parent: %w", err)
	}
	if err := os.Rename(tmpDir, cacheDir); err != nil {
		cleanupDir()
		_ = os.Remove(tmpArchivePath)
		return "", fmt.Errorf("finalize firmware cache: %w", err)
	}
	_ = os.Remove(tmpArchivePath)

	// Compute hash of the final firmware file and write the manifest.
	fwPath, err := findFirmwareInDir(cacheDir)
	if err != nil {
		return "", fmt.Errorf("find firmware in cache: %w", err)
	}
	fwHash, err := hashFile(fwPath)
	if err != nil {
		return "", fmt.Errorf("hash firmware library: %w", err)
	}

	manifest := firmwareManifest{
		Version:     version,
		Arch:        runtime.GOARCH,
		LibraryHash: fwHash,
		Timestamp:   time.Now().UTC(),
	}
	if err := writeFirmwareManifest(manifestPath, manifest); err != nil {
		return "", err
	}

	fwDir := filepath.Dir(fwPath)
	slog.InfoContext(ctx, "firmware downloaded", "dir", fwDir, "version", version, "arch", archCandidate)
	return fwDir, nil
}

// findSystemFirmware searches well-known system library directories for
// libkrunfw.so.5. Returns the directory containing the firmware.
func findSystemFirmware() (string, error) {
	dirs := []string{"/usr/lib", "/usr/local/lib", "/lib", "/lib64", "/usr/lib64"}
	for _, dir := range dirs {
		path := filepath.Join(dir, firmwareLibName)
		if _, err := os.Stat(path); err == nil {
			slog.Warn("using system-installed firmware", "path", path)
			return dir, nil
		}
	}
	return "", fmt.Errorf("%s not found in system library paths", firmwareLibName)
}

// findFirmwareInDir walks a directory to find the firmware library.
// Uses WalkDir because tar extraction may produce subdirectories.
func findFirmwareInDir(dir string) (string, error) {
	var found string
	errFound := errors.New("found")
	walkErr := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && entry.Name() == firmwareLibName {
			found = path
			return errFound
		}
		return nil
	})
	if errors.Is(walkErr, errFound) {
		return found, nil
	}
	if walkErr != nil {
		return "", fmt.Errorf("search firmware dir: %w", walkErr)
	}
	return "", fmt.Errorf("%s not found in %s", firmwareLibName, dir)
}

// --- GitHub release helpers ---

type firmwareReleaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type firmwareReleaseResponse struct {
	Assets []firmwareReleaseAsset `json:"assets"`
}

// fetchFirmwareReleaseAssets queries the GitHub API for release asset metadata.
// Returns a map of asset name to API download URL.
func fetchFirmwareReleaseAssets(ctx context.Context, version string) (map[string]string, error) {
	apiURL := fmt.Sprintf("https://api.github.com/repos/stacklok/go-microvm/releases/tags/%s", version)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create release request: %w", err)
	}
	setFirmwareGitHubAuth(req)
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{Timeout: firmwareHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch release: unexpected status %s", resp.Status)
	}

	var release firmwareReleaseResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}

	assets := make(map[string]string, len(release.Assets))
	for _, a := range release.Assets {
		assets[a.Name] = a.URL
	}
	return assets, nil
}

// downloadFirmwareChecksums downloads and parses the sha256sums.txt asset.
func downloadFirmwareChecksums(ctx context.Context, url string) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create checksums request: %w", err)
	}
	setFirmwareGitHubAuth(req)
	req.Header.Set("Accept", "application/octet-stream")

	client := &http.Client{Timeout: firmwareHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download checksums: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download checksums: unexpected status %s", resp.Status)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return nil, fmt.Errorf("read checksums: %w", err)
	}

	return parseFirmwareChecksumMap(string(data))
}

// parseFirmwareChecksumMap parses a sha256sums.txt file into a map of
// filename to hex-encoded hash.
func parseFirmwareChecksumMap(text string) (map[string]string, error) {
	result := make(map[string]string)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("invalid checksum line: %q", line)
		}
		hash, filename := fields[0], fields[1]
		if len(hash) != 64 {
			return nil, fmt.Errorf("invalid checksum length %d for %s", len(hash), filename)
		}
		if _, err := hex.DecodeString(hash); err != nil {
			return nil, fmt.Errorf("invalid checksum hex for %s: %w", filename, err)
		}
		result[filename] = hash
	}
	return result, nil
}

// --- Download and extraction helpers ---

// downloadToFile downloads url into dst, returning the SHA-256 hex digest.
// It enforces a maximum size of maxBytes.
func downloadToFile(ctx context.Context, url string, dst *os.File, maxBytes int64) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create download request: %w", err)
	}
	setFirmwareGitHubAuth(req)
	req.Header.Set("Accept", "application/octet-stream")

	client := &http.Client{Timeout: firmwareDownloadTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download firmware: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download firmware: unexpected status %s", resp.Status)
	}

	hasher := sha256.New()
	lr := io.LimitReader(resp.Body, maxBytes+1)
	n, err := io.Copy(io.MultiWriter(dst, hasher), lr)
	if err != nil {
		return "", fmt.Errorf("download firmware: %w", err)
	}
	if n > maxBytes {
		return "", fmt.Errorf("download firmware: exceeded %d bytes", maxBytes)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// extractFirmwareTarGz extracts a .tar.gz archive into destDir with security
// protections: path traversal prevention, size limits, and entry count limits.
func extractFirmwareTarGz(archivePath, destDir string) error {
	//nolint:gosec // G304: archivePath is a temp file we created in a secure cache directory
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open firmware archive: %w", err)
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	var extracted int64
	var entries int

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}

		entries++
		if entries > maxFirmwareTarEntries {
			return fmt.Errorf("extract firmware: exceeded %d entries", maxFirmwareTarEntries)
		}

		targetPath, err := safeTarEntryPath(destDir, hdr.Name)
		if err != nil {
			return err
		}
		if targetPath == "" {
			continue
		}

		written, err := extractTarEntry(tr, hdr, targetPath, maxFirmwareExtractSize-extracted)
		if err != nil {
			return err
		}
		extracted += written
	}
	return nil
}

// safeTarEntryPath validates a tar entry name against path traversal attacks
// and returns the safe absolute path within destDir. Returns an empty string
// for entries that should be skipped (empty names).
func safeTarEntryPath(destDir, name string) (string, error) {
	if name == "" {
		return "", nil
	}
	cleanName := filepath.Clean(name)
	if strings.HasPrefix(cleanName, "..") || filepath.IsAbs(cleanName) {
		return "", fmt.Errorf("invalid firmware entry: %s", name)
	}
	targetPath := filepath.Join(destDir, cleanName)
	if !strings.HasPrefix(targetPath, destDir+string(filepath.Separator)) && targetPath != destDir {
		return "", fmt.Errorf("invalid firmware entry path: %s", name)
	}
	return targetPath, nil
}

// extractTarEntry extracts a single tar entry (directory or regular file) to
// targetPath. Returns the number of bytes written for regular files. The
// remaining parameter caps total extracted bytes to prevent zip-bomb attacks.
func extractTarEntry(tr *tar.Reader, hdr *tar.Header, targetPath string, remaining int64) (int64, error) {
	switch hdr.Typeflag {
	case tar.TypeDir:
		//nolint:gosec // G301: firmware extraction directory, not user-facing
		if err := os.MkdirAll(targetPath, 0o755); err != nil {
			return 0, fmt.Errorf("create dir: %w", err)
		}
		return 0, nil
	case tar.TypeReg:
		return extractTarRegularFile(tr, hdr, targetPath, remaining)
	default:
		return 0, fmt.Errorf("unsupported firmware entry type: %d", hdr.Typeflag)
	}
}

// extractTarRegularFile extracts a regular file from a tar archive.
func extractTarRegularFile(tr *tar.Reader, hdr *tar.Header, targetPath string, remaining int64) (int64, error) {
	//nolint:gosec // G301: firmware extraction directory, not user-facing
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return 0, fmt.Errorf("create parent dir: %w", err)
	}
	if remaining <= 0 {
		return 0, fmt.Errorf("extract firmware: exceeded %d bytes", maxFirmwareExtractSize)
	}
	mode := safeFirmwareFileMode(hdr.Mode)
	//nolint:gosec // G304: targetPath is validated by safeTarEntryPath
	out, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return 0, fmt.Errorf("create file: %w", err)
	}
	lr := io.LimitReader(tr, remaining+1)
	written, err := io.Copy(out, lr)
	if err != nil {
		_ = out.Close()
		return written, fmt.Errorf("write file: %w", err)
	}
	if written > remaining {
		_ = out.Close()
		return written, fmt.Errorf("extract firmware: exceeded %d bytes", maxFirmwareExtractSize)
	}
	if err := out.Close(); err != nil {
		return written, fmt.Errorf("close file: %w", err)
	}
	return written, nil
}

// --- Utility functions ---

// setFirmwareGitHubAuth adds a Bearer token to the request if GITHUB_TOKEN
// or GH_TOKEN is set.
func setFirmwareGitHubAuth(req *http.Request) {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		token = os.Getenv("GH_TOKEN")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

// hashFile computes the SHA-256 hex digest of the file at path.
func hashFile(path string) (string, error) {
	//nolint:gosec // G304: path is from our cache directory or known system paths
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open file for hashing: %w", err)
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash file: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// readFirmwareManifest reads and parses a firmware.json manifest file.
func readFirmwareManifest(path string) (firmwareManifest, bool) {
	//nolint:gosec // G304: path is constructed from our cache directory
	data, err := os.ReadFile(path)
	if err != nil {
		return firmwareManifest{}, false
	}
	var manifest firmwareManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return firmwareManifest{}, false
	}
	return manifest, true
}

// writeFirmwareManifest writes a firmware.json manifest to disk.
func writeFirmwareManifest(path string, manifest firmwareManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal firmware manifest: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write firmware manifest: %w", err)
	}
	return nil
}

// firmwareArchCandidates returns architecture name variants to try when
// looking for firmware archives. Go uses "amd64"/"arm64", but release
// artifacts may use "x86_64"/"aarch64".
func firmwareArchCandidates(arch string) []string {
	switch arch {
	case "amd64":
		return []string{"amd64", "x86_64"}
	case "arm64":
		return []string{"arm64", "aarch64"}
	case "x86_64":
		return []string{"x86_64", "amd64"}
	case "aarch64":
		return []string{"aarch64", "arm64"}
	default:
		return []string{arch}
	}
}

// safeFirmwareFileMode returns a sanitized file permission mode.
// Executable files get 0o755, others get 0o644.
func safeFirmwareFileMode(mode int64) os.FileMode {
	if mode&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

// ensureSecureFirmwareCacheRoot verifies the cache root directory is safe:
// exists with 0o700 permissions, is not a symlink, and is owned by the
// current user.
func ensureSecureFirmwareCacheRoot(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create firmware cache root: %w", err)
	}

	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat firmware cache root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("firmware cache root must not be a symlink")
	}
	if !info.IsDir() {
		return errors.New("firmware cache root is not a directory")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("firmware cache root permissions too open: %v", info.Mode().Perm())
	}

	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		uid := os.Getuid()
		if uid < 0 {
			return errors.New("firmware cache root: negative UID from os.Getuid()")
		}
		if stat.Uid != uint32(uid) { //nolint:gosec // G115: uid verified non-negative above
			return errors.New("firmware cache root not owned by current user")
		}
	}
	return nil
}

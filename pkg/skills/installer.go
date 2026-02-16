// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	ociskills "github.com/stacklok/toolhive-core/oci/skills"
)

const (
	// MaxTotalExtractSize is the maximum total decompressed size (500MB).
	MaxTotalExtractSize int64 = 500 * 1024 * 1024
	// MaxFileExtractSize is the maximum size per file in the tar archive (100MB).
	// Matches the toolhive-core default.
	MaxFileExtractSize int64 = 100 * 1024 * 1024
	// MaxExtractFileCount is the maximum number of files allowed in an archive.
	MaxExtractFileCount = 1000

	// dirPermissions is the permission mode for created directories.
	dirPermissions os.FileMode = 0750
	// filePermissionMask strips setuid, setgid, sticky bits and caps at 0644.
	filePermissionMask os.FileMode = 0644
)

// ExtractResult contains the outcome of an Extract operation.
type ExtractResult struct {
	// SkillDir is the absolute path where the skill was extracted.
	SkillDir string
	// Files is the number of files written.
	Files int
}

// Extract decompresses a tar.gz OCI layer and writes files to targetDir.
// If targetDir exists and force is false, an error is returned.
// If force is true, the existing directory is removed before extraction.
func Extract(layerData []byte, targetDir string, force bool) (*ExtractResult, error) {
	// Decompress gzip with total size limit
	tarData, err := ociskills.DecompressWithLimit(layerData, MaxTotalExtractSize)
	if err != nil {
		return nil, fmt.Errorf("decompressing layer: %w", err)
	}

	// Extract tar with per-file size limit (rejects symlinks, hardlinks, path traversal)
	files, err := ociskills.ExtractTarWithLimit(tarData, MaxFileExtractSize)
	if err != nil {
		return nil, fmt.Errorf("extracting tar: %w", err)
	}

	if len(files) > MaxExtractFileCount {
		return nil, fmt.Errorf("archive contains %d files, exceeding limit of %d", len(files), MaxExtractFileCount)
	}

	// Handle existing directory
	if _, statErr := os.Stat(targetDir); statErr == nil {
		if !force {
			return nil, fmt.Errorf("target directory %q already exists; use force to overwrite", targetDir)
		}
		if err := Remove(targetDir); err != nil {
			return nil, fmt.Errorf("removing existing directory: %w", err)
		}
	}

	// Pre-extraction: validate that no existing path components are symlinks.
	// This prevents an attacker from placing a symlink at a parent directory
	// that would cause MkdirAll/writes to follow through to an unintended location.
	if err := validatePathNoSymlinks(targetDir); err != nil {
		return nil, fmt.Errorf("target path validation: %w", err)
	}

	if err := os.MkdirAll(targetDir, dirPermissions); err != nil {
		return nil, fmt.Errorf("creating target directory: %w", err)
	}

	if err := writeFiles(files, targetDir); err != nil {
		return nil, err
	}

	// Defense in depth: verify the extracted directory post-extraction
	if err := CheckFilesystem(targetDir); err != nil {
		_ = os.RemoveAll(targetDir) // clean up on verification failure
		return nil, fmt.Errorf("post-extraction verification failed: %w", err)
	}

	return &ExtractResult{
		SkillDir: targetDir,
		Files:    len(files),
	}, nil
}

// writeFiles writes extracted file entries to targetDir with containment checks
// and sanitized permissions.
func writeFiles(files []ociskills.FileEntry, targetDir string) error {
	cleanTarget := filepath.Clean(targetDir) + string(os.PathSeparator)

	for _, f := range files {
		destPath := filepath.Clean(filepath.Join(targetDir, filepath.FromSlash(f.Path)))

		// Pre-write containment check: ensure destPath is beneath targetDir.
		// This is defense-in-depth — toolhive-core already validates paths,
		// but an escaped file would NOT be cleaned up by CheckFilesystem.
		if !strings.HasPrefix(destPath, cleanTarget) {
			return fmt.Errorf("path traversal detected: file %q escapes target directory", f.Path)
		}

		parentDir := filepath.Dir(destPath)
		if err := os.MkdirAll(parentDir, dirPermissions); err != nil {
			return fmt.Errorf("creating directory %q: %w", parentDir, err)
		}

		// Sanitize file permissions: strip setuid/setgid/sticky, cap at 0644
		mode := os.FileMode(f.Mode&0o777) & filePermissionMask //nolint:gosec // mode is masked to 9 bits before conversion

		if err := os.WriteFile(destPath, f.Content, mode); err != nil {
			return fmt.Errorf("writing file %q: %w", f.Path, err)
		}
	}
	return nil
}

// Remove safely removes a skill directory. Returns nil if the directory does not exist.
func Remove(skillDir string) error {
	if skillDir == "" {
		return fmt.Errorf("skill directory path must not be empty")
	}

	// Resolve to absolute path for safety checks
	absPath, err := filepath.Abs(skillDir)
	if err != nil {
		return fmt.Errorf("resolving absolute path: %w", err)
	}

	// Guard against removing dangerous paths
	homeDir, homeErr := os.UserHomeDir()
	if absPath == "/" {
		return fmt.Errorf("refusing to remove dangerous path %q", absPath)
	}
	if homeErr == nil && absPath == homeDir {
		return fmt.Errorf("refusing to remove dangerous path %q", absPath)
	}
	// If we couldn't determine the home directory, refuse shallow paths as a safety net.
	// Count path depth by splitting on separator (e.g., "/var/home/user" → 4 components).
	if homeErr != nil && pathDepth(absPath) < 4 {
		return fmt.Errorf("refusing to remove shallow path %q (could not determine home directory)", absPath)
	}

	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		return nil
	}

	return os.RemoveAll(absPath)
}

// validatePathNoSymlinks walks up from the target path checking each existing
// path component for symlinks. This prevents symlink attacks where an attacker
// places a symlink at a parent directory before extraction.
func validatePathNoSymlinks(targetDir string) error {
	absTarget, err := filepath.Abs(targetDir)
	if err != nil {
		return fmt.Errorf("resolving absolute path: %w", err)
	}

	// Walk each component from the root down, checking existing segments.
	current := "/"
	for _, component := range strings.Split(absTarget, string(os.PathSeparator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)

		info, err := os.Lstat(current)
		if err != nil {
			// Path doesn't exist yet — remaining components will be created by MkdirAll.
			break
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink found at %q: refusing to extract through symlinks", current)
		}
	}
	return nil
}

// pathDepth counts the number of non-empty components in an absolute path.
// For example, "/var/home/user/skills" returns 4.
func pathDepth(absPath string) int {
	count := 0
	for _, part := range strings.Split(absPath, string(os.PathSeparator)) {
		if part != "" {
			count++
		}
	}
	return count
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package lockfile

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ContentFile is a single file used for contentDigest computation.
type ContentFile struct {
	// Path is the relative path within the skill directory, using either
	// native or slash separators.
	Path string
	// Content is the raw file bytes.
	Content []byte
}

// ContentDigest computes a deterministic SHA-256 dirhash over files.
//
// Algorithm (content-only; file modes and timestamps are ignored):
//  1. Normalize each path to slash-separated form and sort files by path.
//  2. For each file, feed path + "\x00" + sha256(content) + "\n" into a
//     running SHA-256.
//  3. Return "sha256:" + hex(aggregate).
//
// The algorithm is frozen: lock files in the wild pin its output, so any
// change would report every installed skill as drifted. Golden vectors in
// the package tests guard against accidental changes.
func ContentDigest(files []ContentFile) (string, error) {
	if len(files) == 0 {
		return "", fmt.Errorf("content digest requires at least one file")
	}

	normalized := make([]ContentFile, 0, len(files))
	for _, f := range files {
		path := strings.TrimPrefix(filepath.ToSlash(f.Path), "./")
		if path == "" || path == ".." || strings.HasPrefix(path, "../") || strings.Contains(path, "/../") ||
			strings.HasSuffix(path, "/..") || strings.HasPrefix(path, "/") {
			return "", fmt.Errorf("invalid content file path %q", f.Path)
		}
		normalized = append(normalized, ContentFile{Path: path, Content: f.Content})
	}
	slices.SortFunc(normalized, func(a, b ContentFile) int { return strings.Compare(a.Path, b.Path) })

	h := sha256.New()
	for _, f := range normalized {
		fileHash := sha256.Sum256(f.Content)
		// Writes to a hash.Hash never fail.
		_, _ = io.WriteString(h, f.Path)
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(fileHash[:])
		_, _ = h.Write([]byte{'\n'})
	}
	return ContentDigestPrefix + hex.EncodeToString(h.Sum(nil)), nil
}

// ContentDigestFromDir walks skillDir and computes the content digest from
// the on-disk file set. The walk is confined to skillDir via [os.Root], so
// symlinks cannot escape it.
func ContentDigestFromDir(skillDir string) (string, error) {
	root, err := os.OpenRoot(skillDir)
	if err != nil {
		return "", fmt.Errorf("opening skill directory: %w", err)
	}
	defer func() { _ = root.Close() }()

	var files []ContentFile
	err = fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		data, err := fs.ReadFile(root.FS(), path)
		if err != nil {
			return fmt.Errorf("reading %q: %w", path, err)
		}
		files = append(files, ContentFile{Path: path, Content: data})
		return nil
	})
	if err != nil {
		return "", err
	}
	return ContentDigest(files)
}

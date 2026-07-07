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
	"sort"
	"strings"
)

// ContentFile is a single file used for contentDigest computation.
type ContentFile struct {
	// Path is the relative path within the skill directory.
	Path string
	// Content is the raw file bytes.
	Content []byte
}

// ContentDigestPrefix is the prefix for contentDigest values in the lock file.
const ContentDigestPrefix = "sha256:"

// ContentDigest computes a deterministic SHA-256 dirhash over files.
//
// Algorithm (content-only, no modes/timestamps):
//  1. Sort files by relative path.
//  2. For each file, hash path + "\x00" + sha256(content) + "\n" into a running SHA-256.
//  3. Return "sha256:" + hex(aggregate).
func ContentDigest(files []ContentFile) (string, error) {
	if len(files) == 0 {
		return "", fmt.Errorf("content digest requires at least one file")
	}

	sorted := append([]ContentFile(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	h := sha256.New()
	for _, f := range sorted {
		path := strings.TrimPrefix(filepath.ToSlash(f.Path), "./")
		if path == "" || strings.Contains(path, "..") {
			return "", fmt.Errorf("invalid content file path %q", f.Path)
		}
		fileHash := sha256.Sum256(f.Content)
		if _, err := io.WriteString(h, path); err != nil {
			return "", err
		}
		if _, err := h.Write([]byte{0}); err != nil {
			return "", err
		}
		if _, err := h.Write(fileHash[:]); err != nil {
			return "", err
		}
		if _, err := h.Write([]byte{'\n'}); err != nil {
			return "", err
		}
	}
	return ContentDigestPrefix + hex.EncodeToString(h.Sum(nil)), nil
}

// ContentDigestFromDir walks skillDir and computes the content digest from on-disk files.
func ContentDigestFromDir(skillDir string) (string, error) {
	var files []ContentFile
	err := filepath.WalkDir(skillDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(skillDir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path) // #nosec G304 -- path is under validated skillDir
		if err != nil {
			return fmt.Errorf("reading %q: %w", rel, err)
		}
		files = append(files, ContentFile{Path: rel, Content: data})
		return nil
	})
	if err != nil {
		return "", err
	}
	return ContentDigest(files)
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package gitresolver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/stacklok/toolhive/pkg/skills"
)

// WriteFiles writes resolved skill files to the target directory.
// If force is true, any existing directory is removed before writing.
//
// Security: targetDir is produced by PathResolver.GetSkillPath (a trusted
// internal source that builds paths from known base directories). File paths
// within the archive are validated via containment check against targetDir.
func WriteFiles(files []FileEntry, targetDir string, force bool) error {
	// Sanitize targetDir early so all downstream os calls use the clean path.
	targetDir = filepath.Clean(targetDir)

	// Handle existing directory
	if _, statErr := os.Stat(targetDir); statErr == nil { // lgtm[go/path-injection] #nosec G304
		if !force {
			return fmt.Errorf("target directory %q already exists; use force to overwrite", targetDir)
		}
		if err := os.RemoveAll(targetDir); err != nil { // lgtm[go/path-injection] #nosec G304 -- targetDir is cleaned above
			return fmt.Errorf("removing existing directory: %w", err)
		}
	}

	// Pre-extraction: validate that no existing path components are symlinks.
	// Reuses the same check as the OCI installer (pkg/skills/installer.go).
	if err := skills.ValidatePathNoSymlinks(targetDir); err != nil {
		return fmt.Errorf("target path validation: %w", err)
	}

	if err := os.MkdirAll(targetDir, skills.DirPermissions); err != nil { // lgtm[go/path-injection] #nosec G304
		return fmt.Errorf("creating target directory: %w", err)
	}

	// Containment and write logic mirrors pkg/skills/installer.go:writeFiles.
	cleanTarget := targetDir + string(os.PathSeparator)

	for _, f := range files {
		destPath := filepath.Clean(filepath.Join(targetDir, filepath.FromSlash(f.Path)))

		// Containment check: ensure destPath is beneath targetDir.
		if !strings.HasPrefix(destPath, cleanTarget) {
			return fmt.Errorf("path traversal detected: file %q escapes target directory", f.Path)
		}

		parentDir := filepath.Dir(destPath)
		if err := os.MkdirAll(parentDir, skills.DirPermissions); err != nil {
			return fmt.Errorf("creating directory %q: %w", parentDir, err)
		}

		// Sanitize file permissions: strip setuid/setgid/sticky, cap at 0644
		mode := (f.Mode & 0o777) & skills.FilePermissionMask

		if err := os.WriteFile(destPath, f.Content, mode); err != nil {
			return fmt.Errorf("writing file %q: %w", f.Path, err)
		}
	}

	return nil
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package gitresolver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// dirPermissions is the permission mode for created directories.
	dirPermissions os.FileMode = 0750
	// filePermissionMask caps file permissions at 0644 (strips setuid/setgid/sticky).
	filePermissionMask os.FileMode = 0644
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
	if _, statErr := os.Stat(targetDir); statErr == nil { //#nosec G304 -- targetDir is cleaned and produced by PathResolver
		if !force {
			return fmt.Errorf("target directory %q already exists; use force to overwrite", targetDir)
		}
		if err := os.RemoveAll(targetDir); err != nil { //#nosec G304 -- targetDir is cleaned above
			return fmt.Errorf("removing existing directory: %w", err)
		}
	}

	// Pre-extraction: validate that no existing path components are symlinks.
	if err := validatePathNoSymlinks(targetDir); err != nil {
		return fmt.Errorf("target path validation: %w", err)
	}

	if err := os.MkdirAll(targetDir, dirPermissions); err != nil { //#nosec G304 -- targetDir is cleaned above
		return fmt.Errorf("creating target directory: %w", err)
	}

	cleanTarget := filepath.Clean(targetDir) + string(os.PathSeparator)

	for _, f := range files {
		destPath := filepath.Clean(filepath.Join(targetDir, filepath.FromSlash(f.Path)))

		// Containment check: ensure destPath is beneath targetDir.
		if !strings.HasPrefix(destPath, cleanTarget) {
			return fmt.Errorf("path traversal detected: file %q escapes target directory", f.Path)
		}

		parentDir := filepath.Dir(destPath)
		if err := os.MkdirAll(parentDir, dirPermissions); err != nil {
			return fmt.Errorf("creating directory %q: %w", parentDir, err)
		}

		// Sanitize file permissions: strip setuid/setgid/sticky, cap at 0644
		mode := (f.Mode & 0o777) & filePermissionMask

		if err := os.WriteFile(destPath, f.Content, mode); err != nil {
			return fmt.Errorf("writing file %q: %w", f.Path, err)
		}
	}

	return nil
}

// validatePathNoSymlinks walks up from the target path checking each existing
// path component for symlinks.
func validatePathNoSymlinks(targetDir string) error {
	absTarget, err := filepath.Abs(targetDir)
	if err != nil {
		return fmt.Errorf("resolving absolute path: %w", err)
	}

	current := func() string {
		if vol := filepath.VolumeName(absTarget); vol != "" {
			return vol + string(os.PathSeparator)
		}
		return string(os.PathSeparator)
	}()
	for _, component := range strings.Split(absTarget, string(os.PathSeparator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)

		info, err := os.Lstat(current) //#nosec G304 -- current is built from filepath.Abs of the cleaned targetDir
		if err != nil {
			// Path doesn't exist yet — remaining components will be created by MkdirAll.
			break
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink found at %q: refusing to write through symlinks", current)
		}
	}
	return nil
}

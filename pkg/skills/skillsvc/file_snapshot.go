// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// snapshotDirs copies each existing directory in dirs into a temporary
// location so a later failed force/upgrade install can restore prior content.
// Missing directories are skipped (nothing to restore). The returned map keys
// are the original directory paths; values are backup directory paths.
func snapshotDirs(dirs []string) (map[string]string, error) {
	backups := make(map[string]string, len(dirs))
	for _, dir := range dirs {
		dir = filepath.Clean(dir)
		info, err := os.Stat(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat %q before snapshot: %w", dir, err)
		}
		if !info.IsDir() {
			continue
		}
		backup, err := os.MkdirTemp("", "thv-skill-snap-*")
		if err != nil {
			_ = restoreDirs(backups)
			return nil, fmt.Errorf("creating snapshot dir: %w", err)
		}
		if err := copyDir(dir, backup); err != nil {
			_ = os.RemoveAll(backup)
			_ = restoreDirs(backups)
			return nil, fmt.Errorf("snapshotting %q: %w", dir, err)
		}
		backups[dir] = backup
	}
	return backups, nil
}

// restoreDirs replaces each original directory with its backup and removes
// the backup tree. Directories that were created by the failed install and
// had no prior snapshot are left untouched by this helper — callers that
// create fresh dirs should remove them separately.
func restoreDirs(backups map[string]string) error {
	var first error
	for original, backup := range backups {
		if err := os.RemoveAll(original); err != nil && first == nil {
			first = fmt.Errorf("removing overwritten %q: %w", original, err)
		}
		if err := os.Rename(backup, original); err != nil {
			// Rename across filesystems can fail; fall back to copy+remove.
			if copyErr := copyDir(backup, original); copyErr != nil {
				if first == nil {
					first = fmt.Errorf("restoring %q: %w", original, copyErr)
				}
				continue
			}
			_ = os.RemoveAll(backup)
			continue
		}
	}
	return first
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	in, err := os.Open(src) // #nosec G304 -- paths are skill install dirs under project root
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode) // #nosec G304
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

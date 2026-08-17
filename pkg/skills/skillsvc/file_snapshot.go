// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// fileSnapshot is one regular file captured by snapshotDir: contents plus a
// sanitized permission mode so executable bits survive restore.
type fileSnapshot struct {
	data []byte
	mode fs.FileMode
}

// snapshotFileModeMask strips setuid/setgid/sticky and caps at 0755.
const snapshotFileModeMask fs.FileMode = 0o755

func sanitizeFileMode(mode fs.FileMode) fs.FileMode {
	return mode.Perm() & snapshotFileModeMask
}

// snapshotDirs copies each existing directory in dirs into memory so a later
// failed force/upgrade install can restore prior content without temp
// directories. Missing directories are skipped. Map keys are the original
// cleaned directory paths.
func snapshotDirs(dirs []string) (map[string]map[string]fileSnapshot, error) {
	backups := make(map[string]map[string]fileSnapshot, len(dirs))
	for _, dir := range dirs {
		dir = filepath.Clean(dir)
		info, err := os.Stat(dir) // lgtm[go/path-injection] #nosec G304 -- skill install dirs from PathResolver
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat %q before snapshot: %w", dir, err)
		}
		if !info.IsDir() {
			continue
		}
		files, err := snapshotDir(dir)
		if err != nil {
			return nil, fmt.Errorf("snapshotting %q: %w", dir, err)
		}
		backups[dir] = files
	}
	return backups, nil
}

// restoreDirs replaces each original directory with its in-memory snapshot.
func restoreDirs(backups map[string]map[string]fileSnapshot) error {
	var errs []error
	for original, files := range backups {
		if err := restoreDir(original, files); err != nil {
			errs = append(errs, fmt.Errorf("restoring %q: %w", original, err))
		}
	}
	return errors.Join(errs...)
}

// snapshotDir reads every regular file under dir into a relative-path map.
func snapshotDir(dir string) (map[string]fileSnapshot, error) {
	files := make(map[string]fileSnapshot)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error { // lgtm[go/path-injection]
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		if !safeRelPath(rel) {
			return fmt.Errorf("refusing to snapshot path %q outside %q", path, dir)
		}
		data, readErr := os.ReadFile(path) // #nosec G304 -- path walked under validated skill install dir; lgtm[go/path-injection]
		if readErr != nil {
			return readErr
		}
		files[rel] = fileSnapshot{data: data, mode: sanitizeFileMode(info.Mode())}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// restoreDir replaces dir with the files captured by snapshotDir.
func restoreDir(dir string, files map[string]fileSnapshot) error {
	if err := os.RemoveAll(dir); err != nil { // lgtm[go/path-injection] #nosec G304 -- skill install dir
		return err
	}
	var errs []error
	for rel, snap := range files {
		if !safeRelPath(rel) {
			errs = append(errs, fmt.Errorf("refusing to restore unsafe relative path %q", rel))
			continue
		}
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil { // lgtm[go/path-injection]
			errs = append(errs, err)
			continue
		}
		if err := os.WriteFile(path, snap.data, snap.mode); err != nil { // lgtm[go/path-injection] #nosec G304
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// safeRelPath reports whether rel is a non-escaping relative path suitable
// for joining under a snapshot root.
func safeRelPath(rel string) bool {
	if rel == "" || rel == "." {
		return false
	}
	slash := filepath.ToSlash(rel)
	if !fs.ValidPath(slash) {
		return false
	}
	return !strings.HasPrefix(slash, "../") && slash != ".."
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// fileSnapshot is one regular file captured by snapshotTargets: contents plus
// a sanitized permission mode so executable bits survive restore.
type fileSnapshot struct {
	data []byte
	mode fs.FileMode
}

// snapshotFileModeMask strips setuid/setgid/sticky and caps at 0755.
const snapshotFileModeMask fs.FileMode = 0o755

func sanitizeFileMode(mode fs.FileMode) fs.FileMode {
	return mode.Perm() & snapshotFileModeMask
}

// dirBackup captures one target directory's pre-mutation state: whether it
// existed at all, and its regular files when it did. Restore reproduces that
// exact state — directories that did not exist are removed, pre-existing
// ones are rewritten from the snapshot.
type dirBackup struct {
	existed bool
	files   map[string]fileSnapshot
}

// snapshotTargets captures each target directory into memory so a later
// failed install can restore prior content and remove freshly created trees.
// All filesystem access goes through an os.Root anchored at the directory's
// parent, so no traversal can escape the validated skill install location.
func snapshotTargets(dirs []string) (map[string]dirBackup, error) {
	backups := make(map[string]dirBackup, len(dirs))
	for _, dir := range dirs {
		dir = filepath.Clean(dir)
		if _, seen := backups[dir]; seen {
			continue
		}
		backup, err := snapshotTarget(dir)
		if err != nil {
			return nil, fmt.Errorf("snapshotting %q: %w", dir, err)
		}
		backups[dir] = backup
	}
	return backups, nil
}

// snapshotTarget captures a single directory. A missing directory (or a
// missing parent) is recorded as existed=false so restore knows to remove
// whatever the failed install writes there.
func snapshotTarget(dir string) (dirBackup, error) {
	parent, err := os.OpenRoot(filepath.Dir(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return dirBackup{existed: false}, nil
		}
		return dirBackup{}, err
	}
	defer func() { _ = parent.Close() }()

	base := filepath.Base(dir)
	info, err := parent.Stat(base)
	if err != nil {
		if os.IsNotExist(err) {
			return dirBackup{existed: false}, nil
		}
		return dirBackup{}, err
	}
	if !info.IsDir() {
		return dirBackup{existed: false}, nil
	}

	sub, err := parent.OpenRoot(base)
	if err != nil {
		return dirBackup{}, err
	}
	defer func() { _ = sub.Close() }()

	files := make(map[string]fileSnapshot)
	fsys := sub.FS()
	walkErr := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
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
		data, readErr := fs.ReadFile(fsys, path)
		if readErr != nil {
			return readErr
		}
		files[path] = fileSnapshot{data: data, mode: sanitizeFileMode(info.Mode())}
		return nil
	})
	if walkErr != nil {
		return dirBackup{}, walkErr
	}
	return dirBackup{existed: true, files: files}, nil
}

// restoreTargets restores every snapshotted directory to its exact
// pre-mutation state: targets that did not exist are removed (they were
// created by the failed install), pre-existing targets are rewritten from
// their snapshots. All errors are joined so a partial restore still surfaces.
func restoreTargets(backups map[string]dirBackup) error {
	var errs []error
	for dir, backup := range backups {
		if err := restoreTarget(dir, backup); err != nil {
			errs = append(errs, fmt.Errorf("restoring %q: %w", dir, err))
		}
	}
	return errors.Join(errs...)
}

// restoreTarget restores one directory from its backup through an os.Root
// anchored at the parent directory.
func restoreTarget(dir string, backup dirBackup) error {
	parent, err := os.OpenRoot(filepath.Dir(dir))
	if err != nil {
		if os.IsNotExist(err) && !backup.existed {
			// Neither the parent nor the target existed before, and the
			// failed install did not create them either.
			return nil
		}
		return err
	}
	defer func() { _ = parent.Close() }()

	base := filepath.Base(dir)
	if err := parent.RemoveAll(base); err != nil {
		return err
	}
	if !backup.existed {
		return nil
	}
	if err := parent.Mkdir(base, 0o750); err != nil {
		return err
	}
	sub, err := parent.OpenRoot(base)
	if err != nil {
		return err
	}
	defer func() { _ = sub.Close() }()

	var errs []error
	for rel, snap := range backup.files {
		if parentDir := filepath.Dir(rel); parentDir != "." {
			if err := sub.MkdirAll(parentDir, 0o750); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if err := sub.WriteFile(rel, snap.data, snap.mode); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

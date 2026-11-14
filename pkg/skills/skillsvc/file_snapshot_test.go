// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSnapshotTargets(t *testing.T) {
	t.Parallel()

	t.Run("missing parent records existed=false", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "no-such-parent", "my-skill")
		backups, err := snapshotTargets([]string{dir})
		require.NoError(t, err)
		require.Contains(t, backups, filepath.Clean(dir))
		assert.False(t, backups[filepath.Clean(dir)].existed)
	})

	t.Run("missing target records existed=false", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "my-skill")
		backups, err := snapshotTargets([]string{dir})
		require.NoError(t, err)
		assert.False(t, backups[filepath.Clean(dir)].existed)
	})

	t.Run("non-directory target records existed=false", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "my-skill")
		require.NoError(t, os.WriteFile(dir, []byte("a file, not a dir"), 0o644))
		backups, err := snapshotTargets([]string{dir})
		require.NoError(t, err)
		assert.False(t, backups[filepath.Clean(dir)].existed)
	})

	t.Run("existing tree captures nested files and executable mode", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "my-skill")
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "hooks"), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# skill"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks", "run.sh"), []byte("#!/bin/sh"), 0o755))

		backups, err := snapshotTargets([]string{dir})
		require.NoError(t, err)
		backup := backups[filepath.Clean(dir)]
		require.True(t, backup.existed)
		require.Len(t, backup.files, 2)
		assert.Equal(t, fs.FileMode(0o755), backup.files[filepath.Join("hooks", "run.sh")].mode)
		assert.Equal(t, []byte("# skill"), backup.files["SKILL.md"].data)
	})

	t.Run("duplicate targets are deduplicated", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "my-skill")
		require.NoError(t, os.MkdirAll(dir, 0o750))
		backups, err := snapshotTargets([]string{dir, dir, filepath.Clean(dir)})
		require.NoError(t, err)
		assert.Len(t, backups, 1)
	})
}

func TestRestoreTargets(t *testing.T) {
	t.Parallel()

	t.Run("fresh target is removed", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "my-skill")
		backups, err := snapshotTargets([]string{dir})
		require.NoError(t, err)

		// The failed install created the tree after the snapshot.
		require.NoError(t, os.MkdirAll(dir, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("new"), 0o644))

		require.NoError(t, restoreTargets(backups))
		assert.NoDirExists(t, dir, "a target that did not exist at snapshot time must be removed")
	})

	t.Run("fresh target under missing parent is a no-op", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "never-created", "my-skill")
		backups, err := snapshotTargets([]string{dir})
		require.NoError(t, err)
		require.NoError(t, restoreTargets(backups),
			"restoring a fresh target whose parent never appeared must not fail")
	})

	t.Run("pre-existing tree is restored with content and mode", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "my-skill")
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "hooks"), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("old content"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks", "run.sh"), []byte("#!/bin/sh"), 0o755))

		backups, err := snapshotTargets([]string{dir})
		require.NoError(t, err)

		// The failed install rewrote the tree and dropped a file.
		require.NoError(t, os.RemoveAll(dir))
		require.NoError(t, os.MkdirAll(dir, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("overwritten"), 0o600))

		require.NoError(t, restoreTargets(backups))
		got, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
		require.NoError(t, err)
		assert.Equal(t, "old content", string(got))
		info, err := os.Stat(filepath.Join(dir, "hooks", "run.sh"))
		require.NoError(t, err)
		assert.Equal(t, fs.FileMode(0o755), info.Mode().Perm(),
			"executable bits must survive snapshot and restore")
	})
}

func TestDepState(t *testing.T) {
	t.Parallel()

	t.Run("nil receiver is inert", func(t *testing.T) {
		t.Parallel()
		var d *depState
		require.NoError(t, d.enter("a"))
		d.leave("a")
		assert.False(t, d.alreadyDone("a"))
	})

	t.Run("re-entering an active name reports a cycle", func(t *testing.T) {
		t.Parallel()
		d := newDepState()
		require.NoError(t, d.enter("a"))
		err := d.enter("a")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dependency cycle")
	})

	t.Run("leave marks a name completed", func(t *testing.T) {
		t.Parallel()
		d := newDepState()
		require.NoError(t, d.enter("a"))
		assert.False(t, d.alreadyDone("a"))
		d.leave("a")
		assert.True(t, d.alreadyDone("a"))
		require.NoError(t, d.enter("a"), "a completed name can be re-entered (shared dep merge path)")
	})
}

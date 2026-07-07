// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package lockfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return dir
}

func TestLoadMissingFileReturnsEmptyLockfile(t *testing.T) {
	t.Parallel()
	dir := resolvedTempDir(t)

	lf, err := Load(dir)
	require.NoError(t, err)
	assert.Equal(t, CurrentVersion, lf.Version)
	assert.Empty(t, lf.Skills)
}

func TestLoadRejectsMalformedYAML(t *testing.T) {
	t.Parallel()
	dir := resolvedTempDir(t)
	require.NoError(t, os.WriteFile(Path(dir), []byte("not: [valid: yaml"), 0o644))

	_, err := Load(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing lock file")
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	t.Parallel()
	dir := resolvedTempDir(t)

	lf := &Lockfile{Version: CurrentVersion}
	entry := Entry{
		Name:              "code-review",
		Version:           "1.0.0",
		Source:            "code-review",
		ResolvedReference: "ghcr.io/org/code-review:1.0.0",
		Digest:            "sha256:abcdef0123456789",
	}
	lf.Upsert(entry)
	require.NoError(t, lf.Save(dir))

	loaded, err := Load(dir)
	require.NoError(t, err)
	require.Len(t, loaded.Skills, 1)
	assert.Equal(t, entry, loaded.Skills[0])
}

func TestLockfileGetUpsertRemove(t *testing.T) {
	t.Parallel()

	lf := &Lockfile{Version: CurrentVersion}

	_, ok := lf.Get("missing")
	assert.False(t, ok)

	a := Entry{Name: "b-skill", Source: "b-skill", Digest: "sha256:abcdef0123456789"}
	c := Entry{Name: "a-skill", Source: "a-skill", Digest: "sha256:1234567890abcdef"}
	lf.Upsert(a)
	lf.Upsert(c)

	// Entries are kept sorted by name.
	require.Len(t, lf.Skills, 2)
	assert.Equal(t, "a-skill", lf.Skills[0].Name)
	assert.Equal(t, "b-skill", lf.Skills[1].Name)

	got, ok := lf.Get("b-skill")
	require.True(t, ok)
	assert.Equal(t, "sha256:abcdef0123456789", got.Digest)

	// Upsert replaces an existing entry rather than duplicating it.
	updated := Entry{Name: "b-skill", Source: "b-skill", Digest: "sha256:fedcba0987654321"}
	lf.Upsert(updated)
	require.Len(t, lf.Skills, 2)
	got, ok = lf.Get("b-skill")
	require.True(t, ok)
	assert.Equal(t, "sha256:fedcba0987654321", got.Digest)

	removed := lf.Remove("b-skill")
	assert.True(t, removed)
	require.Len(t, lf.Skills, 1)

	removedAgain := lf.Remove("b-skill")
	assert.False(t, removedAgain)
}

func TestUpsertEntryAndRemoveEntry(t *testing.T) {
	t.Parallel()
	dir := resolvedTempDir(t)

	entry := Entry{Name: "my-skill", Source: "my-skill", Digest: "sha256:abcdef0123456789"}
	require.NoError(t, UpsertEntry(dir, entry))

	lf, err := Load(dir)
	require.NoError(t, err)
	require.Len(t, lf.Skills, 1)
	assert.Equal(t, "my-skill", lf.Skills[0].Name)

	// Upserting a second entry preserves the first.
	require.NoError(t, UpsertEntry(dir, Entry{Name: "other-skill", Source: "other-skill", Digest: "sha256:1234567890abcdef"}))
	lf, err = Load(dir)
	require.NoError(t, err)
	assert.Len(t, lf.Skills, 2)

	require.NoError(t, RemoveEntry(dir, "my-skill"))
	lf, err = Load(dir)
	require.NoError(t, err)
	require.Len(t, lf.Skills, 1)
	assert.Equal(t, "other-skill", lf.Skills[0].Name)

	// Removing a name that isn't present is a no-op, not an error.
	require.NoError(t, RemoveEntry(dir, "does-not-exist"))
}

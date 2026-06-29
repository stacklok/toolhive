// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ociplugins "github.com/stacklok/toolhive-core/oci/plugins"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/storage"
)

func newPluginTestStore(t *testing.T) *PluginStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(t.Context(), dbPath)
	require.NoError(t, err)
	store := NewPluginStore(db)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testPlugin(name string) plugins.InstalledPlugin {
	return plugins.InstalledPlugin{
		Metadata: plugins.PluginMetadata{
			Name:        name,
			Version:     "1.0.0",
			Description: "Test plugin " + name,
			Author:      "test-author",
			License:     "MIT",
			Keywords:    []string{"test", "example"},
		},
		Scope:      plugins.ScopeUser,
		Reference:  "ghcr.io/test/" + name + ":v1.0.0",
		Tag:        "v1.0.0",
		Digest:     "sha256:abc123",
		Status:     plugins.InstallStatusInstalled,
		Clients:    []string{"claude-code", "cursor"},
		Components: ociplugins.ComponentInventory{"commands": 2, "skills": 1},
		Signature:  "sig-abc",
		Dependencies: []plugins.Dependency{
			{Name: "dep1", Reference: "ghcr.io/test/dep1:v1", Digest: "sha256:dep1"},
		},
	}
}

func TestPluginStore_Create(t *testing.T) {
	t.Parallel()
	store := newPluginTestStore(t)

	pl := testPlugin("create-test")
	require.NoError(t, store.Create(t.Context(), pl))

	got, err := store.Get(t.Context(), pl.Metadata.Name, pl.Scope, pl.ProjectRoot)
	require.NoError(t, err)

	assert.Equal(t, pl.Metadata.Name, got.Metadata.Name)
	assert.Equal(t, pl.Metadata.Version, got.Metadata.Version)
	assert.Equal(t, pl.Metadata.Description, got.Metadata.Description)
	assert.Equal(t, pl.Metadata.Author, got.Metadata.Author)
	assert.Equal(t, pl.Metadata.License, got.Metadata.License)
	assert.Equal(t, pl.Metadata.Keywords, got.Metadata.Keywords)
	assert.Equal(t, pl.Scope, got.Scope)
	assert.Equal(t, pl.ProjectRoot, got.ProjectRoot)
	assert.Equal(t, pl.Reference, got.Reference)
	assert.Equal(t, pl.Tag, got.Tag)
	assert.Equal(t, pl.Digest, got.Digest)
	assert.Equal(t, pl.Status, got.Status)
	assert.Equal(t, pl.Clients, got.Clients)
	assert.Equal(t, pl.Components, got.Components)
	assert.Equal(t, pl.Signature, got.Signature)
	assert.Equal(t, pl.Dependencies, got.Dependencies)

	assert.False(t, got.InstalledAt.IsZero(), "InstalledAt should not be zero")
}

func TestPluginStore_CreateDuplicate(t *testing.T) {
	t.Parallel()
	store := newPluginTestStore(t)

	pl := testPlugin("dup-test")
	require.NoError(t, store.Create(t.Context(), pl))

	err := store.Create(t.Context(), pl)
	require.ErrorIs(t, err, storage.ErrAlreadyExists)
}

func TestPluginStore_GetNotFound(t *testing.T) {
	t.Parallel()
	store := newPluginTestStore(t)

	_, err := store.Get(t.Context(), "nonexistent", plugins.ScopeUser, "")
	require.ErrorIs(t, err, storage.ErrNotFound)
}

func TestPluginStore_GetByScope(t *testing.T) {
	t.Parallel()
	store := newPluginTestStore(t)

	userPlugin := testPlugin("scoped-plugin")
	userPlugin.Metadata.Description = "user-scoped"
	require.NoError(t, store.Create(t.Context(), userPlugin))

	projPlugin := testPlugin("scoped-plugin")
	projPlugin.Scope = plugins.ScopeProject
	projPlugin.ProjectRoot = "/home/user/myproject"
	projPlugin.Metadata.Description = "project-scoped"
	require.NoError(t, store.Create(t.Context(), projPlugin))

	got, err := store.Get(t.Context(), "scoped-plugin", plugins.ScopeUser, "")
	require.NoError(t, err)
	assert.Equal(t, plugins.ScopeUser, got.Scope)
	assert.Equal(t, "user-scoped", got.Metadata.Description)

	got, err = store.Get(t.Context(), "scoped-plugin", plugins.ScopeProject, "/home/user/myproject")
	require.NoError(t, err)
	assert.Equal(t, plugins.ScopeProject, got.Scope)
	assert.Equal(t, "/home/user/myproject", got.ProjectRoot)
	assert.Equal(t, "project-scoped", got.Metadata.Description)
}

func TestPluginStore_List(t *testing.T) {
	t.Parallel()
	store := newPluginTestStore(t)

	for _, name := range []string{"alpha", "bravo", "charlie"} {
		pl := testPlugin(name)
		require.NoError(t, store.Create(t.Context(), pl))
	}

	list, err := store.List(t.Context(), storage.ListFilter{})
	require.NoError(t, err)
	assert.Len(t, list, 3)

	// Verify ORDER BY e.name: a regression dropping the clause would pass
	// without this assertion.
	names := make([]string, len(list))
	for i, p := range list {
		names[i] = p.Metadata.Name
	}
	assert.Equal(t, []string{"alpha", "bravo", "charlie"}, names)

	// Verify the two-phase pattern populates dependencies correctly.
	for _, p := range list {
		assert.Len(t, p.Dependencies, 1, "plugin %q should have its dependency", p.Metadata.Name)
	}
}

func TestPluginStore_ListFilterByScope(t *testing.T) {
	t.Parallel()
	store := newPluginTestStore(t)

	for _, name := range []string{"user-a", "user-b"} {
		pl := testPlugin(name)
		pl.Scope = plugins.ScopeUser
		require.NoError(t, store.Create(t.Context(), pl))
	}

	projPlugin := testPlugin("proj-a")
	projPlugin.Scope = plugins.ScopeProject
	projPlugin.ProjectRoot = "/projects/one"
	require.NoError(t, store.Create(t.Context(), projPlugin))

	userList, err := store.List(t.Context(), storage.ListFilter{Scope: plugins.ScopeUser})
	require.NoError(t, err)
	assert.Len(t, userList, 2)
	for _, p := range userList {
		assert.Equal(t, plugins.ScopeUser, p.Scope)
	}

	projList, err := store.List(t.Context(), storage.ListFilter{Scope: plugins.ScopeProject})
	require.NoError(t, err)
	assert.Len(t, projList, 1)
	assert.Equal(t, plugins.ScopeProject, projList[0].Scope)
}

func TestPluginStore_ListFilterByProjectRoot(t *testing.T) {
	t.Parallel()
	store := newPluginTestStore(t)

	roots := []string{"/projects/alpha", "/projects/bravo", "/projects/alpha"}
	for i, root := range roots {
		pl := testPlugin("proj-plugin-" + string(rune('a'+i)))
		pl.Scope = plugins.ScopeProject
		pl.ProjectRoot = root
		require.NoError(t, store.Create(t.Context(), pl))
	}

	list, err := store.List(t.Context(), storage.ListFilter{ProjectRoot: "/projects/alpha"})
	require.NoError(t, err)
	assert.Len(t, list, 2)
	for _, p := range list {
		assert.Equal(t, "/projects/alpha", p.ProjectRoot)
	}
}

func TestPluginStore_ListFilterByClientApp(t *testing.T) {
	t.Parallel()
	store := newPluginTestStore(t)

	pl1 := testPlugin("multi-client")
	pl1.Clients = []string{"claude-code", "cursor"}
	require.NoError(t, store.Create(t.Context(), pl1))

	pl2 := testPlugin("cursor-only")
	pl2.Clients = []string{"cursor"}
	require.NoError(t, store.Create(t.Context(), pl2))

	pl3 := testPlugin("claude-only")
	pl3.Clients = []string{"claude-code"}
	require.NoError(t, store.Create(t.Context(), pl3))

	list, err := store.List(t.Context(), storage.ListFilter{ClientApp: "claude-code"})
	require.NoError(t, err)
	assert.Len(t, list, 2, "expected multi-client and claude-only")

	names := make([]string, 0, len(list))
	for _, p := range list {
		names = append(names, p.Metadata.Name)
	}
	assert.Contains(t, names, "multi-client")
	assert.Contains(t, names, "claude-only")
}

func TestPluginStore_Update(t *testing.T) {
	t.Parallel()
	store := newPluginTestStore(t)

	pl := testPlugin("update-test")
	require.NoError(t, store.Create(t.Context(), pl))

	pl.Metadata.Version = "2.0.0"
	pl.Status = plugins.InstallStatusPending
	pl.Clients = []string{"vscode"}
	pl.Components = ociplugins.ComponentInventory{"agents": 5}
	pl.Signature = "new-sig"
	pl.Dependencies = []plugins.Dependency{
		{Name: "dep2", Reference: "ghcr.io/test/dep2:v2", Digest: "sha256:dep2"},
		{Name: "dep3", Reference: "ghcr.io/test/dep3:v3", Digest: "sha256:dep3"},
	}

	require.NoError(t, store.Update(t.Context(), pl))

	got, err := store.Get(t.Context(), pl.Metadata.Name, pl.Scope, pl.ProjectRoot)
	require.NoError(t, err)

	assert.Equal(t, "2.0.0", got.Metadata.Version)
	assert.Equal(t, plugins.InstallStatusPending, got.Status)
	assert.Equal(t, []string{"vscode"}, got.Clients)
	assert.Equal(t, ociplugins.ComponentInventory{"agents": 5}, got.Components)
	assert.Equal(t, "new-sig", got.Signature)
	assert.Len(t, got.Dependencies, 2)
	assert.Equal(t, "dep2", got.Dependencies[0].Name)
	assert.Equal(t, "dep3", got.Dependencies[1].Name)
}

func TestPluginStore_UpdateNotFound(t *testing.T) {
	t.Parallel()
	store := newPluginTestStore(t)

	pl := testPlugin("ghost")
	err := store.Update(t.Context(), pl)
	require.ErrorIs(t, err, storage.ErrNotFound)
}

func TestPluginStore_UpdateShrinksToZeroDependencies(t *testing.T) {
	t.Parallel()
	store := newPluginTestStore(t)

	pl := testPlugin("shrink-test")
	pl.Dependencies = []plugins.Dependency{
		{Name: "dep1", Reference: "ghcr.io/test/dep1:v1", Digest: "sha256:dep1"},
		{Name: "dep2", Reference: "ghcr.io/test/dep2:v2", Digest: "sha256:dep2"},
	}
	require.NoError(t, store.Create(t.Context(), pl))

	got, err := store.Get(t.Context(), pl.Metadata.Name, pl.Scope, pl.ProjectRoot)
	require.NoError(t, err)
	assert.Len(t, got.Dependencies, 2)

	// Update with an empty dependency slice: the DELETE-then-insert path
	// must remove all rows, not leave stale ones.
	pl.Dependencies = nil
	require.NoError(t, store.Update(t.Context(), pl))

	got, err = store.Get(t.Context(), pl.Metadata.Name, pl.Scope, pl.ProjectRoot)
	require.NoError(t, err)
	assert.Empty(t, got.Dependencies, "dependencies should be cleared on update to zero")
}

func TestPluginStore_Delete(t *testing.T) {
	t.Parallel()
	store := newPluginTestStore(t)

	pl := testPlugin("delete-me")
	require.NoError(t, store.Create(t.Context(), pl))

	require.NoError(t, store.Delete(t.Context(), pl.Metadata.Name, pl.Scope, pl.ProjectRoot))

	_, err := store.Get(t.Context(), pl.Metadata.Name, pl.Scope, pl.ProjectRoot)
	require.ErrorIs(t, err, storage.ErrNotFound)
}

func TestPluginStore_DeleteNotFound(t *testing.T) {
	t.Parallel()
	store := newPluginTestStore(t)

	err := store.Delete(t.Context(), "no-such-plugin", plugins.ScopeUser, "")
	require.ErrorIs(t, err, storage.ErrNotFound)
}

func TestPluginStore_DeleteCascade(t *testing.T) {
	t.Parallel()
	store := newPluginTestStore(t)

	pl := testPlugin("cascade-plugin")
	pl.Dependencies = []plugins.Dependency{
		{Name: "dep-a", Reference: "ghcr.io/test/dep-a:v1", Digest: "sha256:depa"},
		{Name: "dep-b", Reference: "ghcr.io/test/dep-b:v1", Digest: "sha256:depb"},
	}
	require.NoError(t, store.Create(t.Context(), pl))

	require.NoError(t, store.Delete(t.Context(), pl.Metadata.Name, pl.Scope, pl.ProjectRoot))

	// Verify no orphaned dependency rows remain.
	pl2 := testPlugin("survivor")
	pl2.Dependencies = []plugins.Dependency{
		{Name: "dep-c", Reference: "ghcr.io/test/dep-c:v1", Digest: "sha256:depc"},
	}
	require.NoError(t, store.Create(t.Context(), pl2))

	got, err := store.Get(t.Context(), "survivor", plugins.ScopeUser, "")
	require.NoError(t, err)
	assert.Len(t, got.Dependencies, 1, "survivor should have exactly 1 dependency")
	assert.Equal(t, "dep-c", got.Dependencies[0].Name)

	var depCount int
	err = store.db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM plugin_dependencies
		 WHERE dep_reference IN ('ghcr.io/test/dep-a:v1', 'ghcr.io/test/dep-b:v1')`,
	).Scan(&depCount)
	require.NoError(t, err)
	assert.Equal(t, 0, depCount, "cascaded dependencies should be deleted")
}

func TestPluginStore_NilSlicesRoundTrip(t *testing.T) {
	t.Parallel()
	store := newPluginTestStore(t)

	pl := testPlugin("nil-fields")
	pl.Metadata.Keywords = nil
	pl.Clients = nil
	pl.Components = nil
	pl.Dependencies = nil
	pl.Signature = ""
	require.NoError(t, store.Create(t.Context(), pl))

	got, err := store.Get(t.Context(), "nil-fields", plugins.ScopeUser, "")
	require.NoError(t, err)

	assert.Nil(t, got.Metadata.Keywords, "nil keywords should round-trip as nil")
	assert.Nil(t, got.Clients, "nil clients should round-trip as nil")
	assert.Nil(t, got.Components, "nil components should round-trip as nil")
	assert.Empty(t, got.Signature, "empty signature should round-trip as empty")
	assert.Empty(t, got.Dependencies, "nil dependencies should round-trip as empty")
}

func TestPluginStore_MultiConnectionAccess(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "multi-conn.db")

	store1, err := newPluginStoreFromPath(t.Context(), dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store1.(*PluginStore).Close() })

	store2, err := newPluginStoreFromPath(t.Context(), dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store2.(*PluginStore).Close() })

	pl := testPlugin("multi-conn-plugin")
	require.NoError(t, store1.Create(t.Context(), pl))

	got, err := store2.Get(t.Context(), pl.Metadata.Name, pl.Scope, pl.ProjectRoot)
	require.NoError(t, err)
	assert.Equal(t, pl.Metadata.Name, got.Metadata.Name)
	assert.Equal(t, pl.Reference, got.Reference)
	assert.Equal(t, pl.Clients, got.Clients)
}

// TestPluginStore_EntryReuseAcrossScopes asserts the entries-table reuse path
// works with EntryTypePlugin: the same plugin name at user AND project scope
// shares one entries row (entry_id reused). Proves plugin_store.Create looks
// up entries by (EntryTypePlugin, name), never name alone.
func TestPluginStore_EntryReuseAcrossScopes(t *testing.T) {
	t.Parallel()
	store := newPluginTestStore(t)

	userPlugin := testPlugin("shared-name")
	userPlugin.Scope = plugins.ScopeUser
	require.NoError(t, store.Create(t.Context(), userPlugin))

	projPlugin := testPlugin("shared-name")
	projPlugin.Scope = plugins.ScopeProject
	projPlugin.ProjectRoot = "/projects/x"
	require.NoError(t, store.Create(t.Context(), projPlugin))

	// Both must be retrievable independently.
	gotUser, err := store.Get(t.Context(), "shared-name", plugins.ScopeUser, "")
	require.NoError(t, err)
	assert.Equal(t, plugins.ScopeUser, gotUser.Scope)

	gotProj, err := store.Get(t.Context(), "shared-name", plugins.ScopeProject, "/projects/x")
	require.NoError(t, err)
	assert.Equal(t, plugins.ScopeProject, gotProj.Scope)

	// Exactly one entries row for (EntryTypePlugin, "shared-name").
	var entryCount int
	err = store.db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM entries WHERE entry_type = ? AND name = ?`,
		string(storage.EntryTypePlugin), "shared-name",
	).Scan(&entryCount)
	require.NoError(t, err)
	assert.Equal(t, 1, entryCount, "entry_id should be reused across scopes")

	// Two installed_plugins rows sharing that entry.
	var instCount int
	err = store.db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM installed_plugins ip_
		 JOIN entries e ON ip_.entry_id = e.id
		 WHERE e.entry_type = ? AND e.name = ?`,
		string(storage.EntryTypePlugin), "shared-name",
	).Scan(&instCount)
	require.NoError(t, err)
	assert.Equal(t, 2, instCount)
}

// TestPluginStore_NullableSignature asserts the signature column round-trips
// as empty when stored as SQL NULL (the Create path stores NULL for empty).
func TestPluginStore_NullableSignature(t *testing.T) {
	t.Parallel()
	store := newPluginTestStore(t)

	pl := testPlugin("null-sig")
	pl.Signature = ""
	require.NoError(t, store.Create(t.Context(), pl))

	// Verify the column is actually NULL in the DB.
	var nullCount int
	err := store.db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM installed_plugins ip_
		 JOIN entries e ON ip_.entry_id = e.id
		 WHERE e.name = ? AND ip_.signature IS NULL`,
		"null-sig",
	).Scan(&nullCount)
	require.NoError(t, err)
	assert.Equal(t, 1, nullCount, "empty signature should be stored as NULL")

	got, err := store.Get(t.Context(), "null-sig", plugins.ScopeUser, "")
	require.NoError(t, err)
	assert.Empty(t, got.Signature)
}

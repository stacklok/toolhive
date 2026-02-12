// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/storage"
)

func newTestStore(t *testing.T) *SkillStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(t.Context(), dbPath)
	require.NoError(t, err)
	store := NewSkillStore(db)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testSkill(name string) skills.InstalledSkill {
	return skills.InstalledSkill{
		Metadata: skills.SkillMetadata{
			Name:        name,
			Version:     "1.0.0",
			Description: "Test skill " + name,
			Author:      "test-author",
			Tags:        []string{"test", "example"},
		},
		Scope:     skills.ScopeUser,
		Reference: "ghcr.io/test/" + name + ":v1.0.0",
		Tag:       "v1.0.0",
		Digest:    "sha256:abc123",
		Status:    skills.InstallStatusInstalled,
		Clients:   []string{"claude-code", "cursor"},
		Dependencies: []skills.Dependency{
			{Name: "dep1", Reference: "ghcr.io/test/dep1:v1", Digest: "sha256:dep1"},
		},
	}
}

func TestSkillStore_Create(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)

	sk := testSkill("create-test")
	err := store.Create(t.Context(), sk)
	require.NoError(t, err)

	got, err := store.Get(t.Context(), sk.Metadata.Name, sk.Scope, sk.ProjectRoot)
	require.NoError(t, err)

	assert.Equal(t, sk.Metadata.Name, got.Metadata.Name)
	assert.Equal(t, sk.Metadata.Version, got.Metadata.Version)
	assert.Equal(t, sk.Metadata.Description, got.Metadata.Description)
	assert.Equal(t, sk.Metadata.Author, got.Metadata.Author)
	assert.Equal(t, sk.Metadata.Tags, got.Metadata.Tags)
	assert.Equal(t, sk.Scope, got.Scope)
	assert.Equal(t, sk.ProjectRoot, got.ProjectRoot)
	assert.Equal(t, sk.Reference, got.Reference)
	assert.Equal(t, sk.Tag, got.Tag)
	assert.Equal(t, sk.Digest, got.Digest)
	assert.Equal(t, sk.Status, got.Status)
	assert.Equal(t, sk.Clients, got.Clients)
	assert.Equal(t, sk.Dependencies, got.Dependencies)

	// InstalledAt is set by the DB DEFAULT, so just assert it is not zero.
	assert.False(t, got.InstalledAt.IsZero(), "InstalledAt should not be zero")
}

func TestSkillStore_CreateDuplicate(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)

	sk := testSkill("dup-test")
	require.NoError(t, store.Create(t.Context(), sk))

	err := store.Create(t.Context(), sk)
	require.ErrorIs(t, err, storage.ErrAlreadyExists)
}

func TestSkillStore_Get(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)

	_, err := store.Get(t.Context(), "nonexistent", skills.ScopeUser, "")
	require.ErrorIs(t, err, storage.ErrNotFound)
}

func TestSkillStore_GetByScope(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)

	// testSkill defaults to ScopeUser with empty ProjectRoot.
	userSkill := testSkill("scoped-skill")
	userSkill.Metadata.Description = "user-scoped"
	require.NoError(t, store.Create(t.Context(), userSkill))

	// Same name, different scope + project root.
	projSkill := testSkill("scoped-skill")
	projSkill.Scope = skills.ScopeProject
	projSkill.ProjectRoot = "/home/user/myproject"
	projSkill.Metadata.Description = "project-scoped"
	require.NoError(t, store.Create(t.Context(), projSkill))

	// Get user-scoped skill.
	got, err := store.Get(t.Context(), "scoped-skill", skills.ScopeUser, "")
	require.NoError(t, err)
	assert.Equal(t, skills.ScopeUser, got.Scope)
	assert.Equal(t, "user-scoped", got.Metadata.Description)

	// Get project-scoped skill with the correct project root.
	got, err = store.Get(t.Context(), "scoped-skill", skills.ScopeProject, "/home/user/myproject")
	require.NoError(t, err)
	assert.Equal(t, skills.ScopeProject, got.Scope)
	assert.Equal(t, "/home/user/myproject", got.ProjectRoot)
	assert.Equal(t, "project-scoped", got.Metadata.Description)
}

func TestSkillStore_List(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)

	for _, name := range []string{"alpha", "bravo", "charlie"} {
		sk := testSkill(name)
		require.NoError(t, store.Create(t.Context(), sk))
	}

	list, err := store.List(t.Context(), storage.ListFilter{})
	require.NoError(t, err)
	assert.Len(t, list, 3)

	// Verify the two-phase pattern populates dependencies correctly.
	for _, s := range list {
		assert.Len(t, s.Dependencies, 1, "skill %q should have its dependency", s.Metadata.Name)
	}
}

func TestSkillStore_ListFilterByScope(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)

	for _, name := range []string{"user-a", "user-b"} {
		sk := testSkill(name)
		sk.Scope = skills.ScopeUser
		require.NoError(t, store.Create(t.Context(), sk))
	}

	projSkill := testSkill("proj-a")
	projSkill.Scope = skills.ScopeProject
	projSkill.ProjectRoot = "/projects/one"
	require.NoError(t, store.Create(t.Context(), projSkill))

	userList, err := store.List(t.Context(), storage.ListFilter{Scope: skills.ScopeUser})
	require.NoError(t, err)
	assert.Len(t, userList, 2)
	for _, s := range userList {
		assert.Equal(t, skills.ScopeUser, s.Scope)
	}

	projList, err := store.List(t.Context(), storage.ListFilter{Scope: skills.ScopeProject})
	require.NoError(t, err)
	assert.Len(t, projList, 1)
	assert.Equal(t, skills.ScopeProject, projList[0].Scope)
}

func TestSkillStore_ListFilterByProjectRoot(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)

	roots := []string{"/projects/alpha", "/projects/bravo", "/projects/alpha"}
	for i, root := range roots {
		sk := testSkill("proj-skill-" + string(rune('a'+i)))
		sk.Scope = skills.ScopeProject
		sk.ProjectRoot = root
		require.NoError(t, store.Create(t.Context(), sk))
	}

	list, err := store.List(t.Context(), storage.ListFilter{ProjectRoot: "/projects/alpha"})
	require.NoError(t, err)
	assert.Len(t, list, 2)
	for _, s := range list {
		assert.Equal(t, "/projects/alpha", s.ProjectRoot)
	}
}

func TestSkillStore_ListFilterByClientApp(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)

	sk1 := testSkill("multi-client")
	sk1.Clients = []string{"claude-code", "cursor"}
	require.NoError(t, store.Create(t.Context(), sk1))

	sk2 := testSkill("cursor-only")
	sk2.Clients = []string{"cursor"}
	require.NoError(t, store.Create(t.Context(), sk2))

	sk3 := testSkill("claude-only")
	sk3.Clients = []string{"claude-code"}
	require.NoError(t, store.Create(t.Context(), sk3))

	list, err := store.List(t.Context(), storage.ListFilter{ClientApp: "claude-code"})
	require.NoError(t, err)
	assert.Len(t, list, 2, "expected multi-client and claude-only")

	names := make([]string, 0, len(list))
	for _, s := range list {
		names = append(names, s.Metadata.Name)
	}
	assert.Contains(t, names, "multi-client")
	assert.Contains(t, names, "claude-only")
}

func TestSkillStore_Update(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)

	sk := testSkill("update-test")
	require.NoError(t, store.Create(t.Context(), sk))

	sk.Metadata.Version = "2.0.0"
	sk.Status = skills.InstallStatusPending
	sk.Clients = []string{"vscode"}
	sk.Dependencies = []skills.Dependency{
		{Name: "dep2", Reference: "ghcr.io/test/dep2:v2", Digest: "sha256:dep2"},
		{Name: "dep3", Reference: "ghcr.io/test/dep3:v3", Digest: "sha256:dep3"},
	}

	err := store.Update(t.Context(), sk)
	require.NoError(t, err)

	got, err := store.Get(t.Context(), sk.Metadata.Name, sk.Scope, sk.ProjectRoot)
	require.NoError(t, err)

	assert.Equal(t, "2.0.0", got.Metadata.Version)
	assert.Equal(t, skills.InstallStatusPending, got.Status)
	assert.Equal(t, []string{"vscode"}, got.Clients)
	assert.Len(t, got.Dependencies, 2)
	assert.Equal(t, "dep2", got.Dependencies[0].Name)
	assert.Equal(t, "dep3", got.Dependencies[1].Name)
}

func TestSkillStore_UpdateNotFound(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)

	sk := testSkill("ghost")
	err := store.Update(t.Context(), sk)
	require.ErrorIs(t, err, storage.ErrNotFound)
}

func TestSkillStore_Delete(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)

	sk := testSkill("delete-me")
	require.NoError(t, store.Create(t.Context(), sk))

	err := store.Delete(t.Context(), sk.Metadata.Name, sk.Scope, sk.ProjectRoot)
	require.NoError(t, err)

	_, err = store.Get(t.Context(), sk.Metadata.Name, sk.Scope, sk.ProjectRoot)
	require.ErrorIs(t, err, storage.ErrNotFound)
}

func TestSkillStore_DeleteNotFound(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)

	err := store.Delete(t.Context(), "no-such-skill", skills.ScopeUser, "")
	require.ErrorIs(t, err, storage.ErrNotFound)
}

func TestSkillStore_DeleteCascade(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)

	sk := testSkill("cascade-skill")
	sk.Dependencies = []skills.Dependency{
		{Name: "dep-a", Reference: "ghcr.io/test/dep-a:v1", Digest: "sha256:depa"},
		{Name: "dep-b", Reference: "ghcr.io/test/dep-b:v1", Digest: "sha256:depb"},
	}
	require.NoError(t, store.Create(t.Context(), sk))

	require.NoError(t, store.Delete(t.Context(), sk.Metadata.Name, sk.Scope, sk.ProjectRoot))

	// Verify no orphaned dependency rows remain.
	sk2 := testSkill("survivor")
	sk2.Dependencies = []skills.Dependency{
		{Name: "dep-c", Reference: "ghcr.io/test/dep-c:v1", Digest: "sha256:depc"},
	}
	require.NoError(t, store.Create(t.Context(), sk2))

	got, err := store.Get(t.Context(), "survivor", skills.ScopeUser, "")
	require.NoError(t, err)
	assert.Len(t, got.Dependencies, 1, "survivor should have exactly 1 dependency")
	assert.Equal(t, "dep-c", got.Dependencies[0].Name)

	var depCount int
	err = store.db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM skill_dependencies
		 WHERE dep_reference IN ('ghcr.io/test/dep-a:v1', 'ghcr.io/test/dep-b:v1')`,
	).Scan(&depCount)
	require.NoError(t, err)
	assert.Equal(t, 0, depCount, "cascaded dependencies should be deleted")
}

func TestSkillStore_NilSlicesRoundTrip(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)

	sk := testSkill("nil-fields")
	sk.Metadata.Tags = nil
	sk.Clients = nil
	sk.Dependencies = nil
	require.NoError(t, store.Create(t.Context(), sk))

	got, err := store.Get(t.Context(), "nil-fields", skills.ScopeUser, "")
	require.NoError(t, err)

	assert.Nil(t, got.Metadata.Tags, "nil tags should round-trip as nil")
	assert.Nil(t, got.Clients, "nil clients should round-trip as nil")
	assert.Empty(t, got.Dependencies, "nil dependencies should round-trip as empty")
}

func TestSkillStore_MultiConnectionAccess(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "multi-conn.db")

	// Two independent connections to the same DB file.
	store1, err := newSkillStoreFromPath(t.Context(), dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store1.(*SkillStore).Close() })

	store2, err := newSkillStoreFromPath(t.Context(), dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store2.(*SkillStore).Close() })

	sk := testSkill("multi-conn-skill")
	require.NoError(t, store1.Create(t.Context(), sk))

	got, err := store2.Get(t.Context(), sk.Metadata.Name, sk.Scope, sk.ProjectRoot)
	require.NoError(t, err)
	assert.Equal(t, sk.Metadata.Name, got.Metadata.Name)
	assert.Equal(t, sk.Reference, got.Reference)
	assert.Equal(t, sk.Clients, got.Clients)
}

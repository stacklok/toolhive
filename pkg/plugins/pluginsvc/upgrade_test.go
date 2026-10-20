// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
	ociplugins "github.com/stacklok/toolhive-core/oci/plugins"
	ocimocks "github.com/stacklok/toolhive-core/oci/plugins/mocks"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
)

func addPluginRepoCommit(t *testing.T, repoDir, content string) {
	t.Helper()
	repo, err := gogit.PlainOpen(repoDir)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "commands", "hello.md"), []byte(content), 0o644))
	_, err = wt.Add(".")
	require.NoError(t, err)
	_, err = wt.Commit("update", &gogit.CommitOptions{Author: &object.Signature{Name: "T", Email: "t@e"}})
	require.NoError(t, err)
}

func installGitTestPlugin(t *testing.T, svc plugins.PluginService, projectRoot string) {
	t.Helper()
	_, err := svc.Install(t.Context(), plugins.InstallOptions{
		Name: gitPluginRef, Scope: plugins.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestUpgrade_ReportsUpToDateWhenSourceUnchanged(t *testing.T) {
	repoDir := createPluginTestRepo(t, "")
	svc, projectRoot := newGitLockTestService(t, repoDir)
	installGitTestPlugin(t, svc, projectRoot)

	result, err := svc.(*service).Upgrade(t.Context(), plugins.UpgradeOptions{ProjectRoot: projectRoot}) //nolint:forcetypeassert
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, plugins.UpgradeStatusUpToDate, result.Outcomes[0].Status)
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestUpgrade_InstallsNewerContent(t *testing.T) {
	repoDir := createPluginTestRepo(t, "")
	svc, projectRoot := newGitLockTestService(t, repoDir)
	installGitTestPlugin(t, svc, projectRoot)

	before := readLockfile(t, projectRoot)
	beforeEntry, ok := before.GetPlugin("my-plugin")
	require.True(t, ok)

	addPluginRepoCommit(t, repoDir, "# hello v2")

	result, err := svc.(*service).Upgrade(t.Context(), plugins.UpgradeOptions{ProjectRoot: projectRoot}) //nolint:forcetypeassert
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	outcome := result.Outcomes[0]
	assert.Equal(t, plugins.UpgradeStatusUpgraded, outcome.Status)
	assert.Equal(t, beforeEntry.Digest, outcome.OldDigest)
	assert.NotEqual(t, outcome.OldDigest, outcome.NewDigest)

	after := readLockfile(t, projectRoot)
	afterEntry, ok := after.GetPlugin("my-plugin")
	require.True(t, ok)
	assert.Equal(t, outcome.NewDigest, afterEntry.Digest)
	assert.Equal(t, beforeEntry.Source, afterEntry.Source, "Source must never be rewritten by upgrade")

	hello, err := os.ReadFile(filepath.Join(pluginOnDiskPath(projectRoot, "my-plugin"), "commands", "hello.md")) //nolint:gosec
	require.NoError(t, err)
	assert.Contains(t, string(hello), "# hello v2")
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestUpgrade_PreviewDoesNotInstall(t *testing.T) {
	repoDir := createPluginTestRepo(t, "")
	svc, projectRoot := newGitLockTestService(t, repoDir)
	installGitTestPlugin(t, svc, projectRoot)

	before := readLockfile(t, projectRoot)
	beforeEntry, _ := before.GetPlugin("my-plugin")

	addPluginRepoCommit(t, repoDir, "# hello preview")

	result, err := svc.(*service).Upgrade(t.Context(), plugins.UpgradeOptions{ //nolint:forcetypeassert
		ProjectRoot: projectRoot, Preview: true,
	})
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, plugins.UpgradeStatusUpgraded, result.Outcomes[0].Status, "preview still reports what would happen")

	after := readLockfile(t, projectRoot)
	afterEntry, ok := after.GetPlugin("my-plugin")
	require.True(t, ok)
	assert.Equal(t, beforeEntry.Digest, afterEntry.Digest, "preview must not rewrite the lock file")
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestUpgrade_PreservesExistingClients(t *testing.T) {
	repoDir := createPluginTestRepo(t, "")
	svc, projectRoot := newGitLockTestService(t, repoDir)
	installGitTestPlugin(t, svc, projectRoot)

	addPluginRepoCommit(t, repoDir, "# hello clients")

	result, err := svc.(*service).Upgrade(t.Context(), plugins.UpgradeOptions{ProjectRoot: projectRoot}) //nolint:forcetypeassert
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, plugins.UpgradeStatusUpgraded, result.Outcomes[0].Status)

	info, err := svc.Info(t.Context(), plugins.InfoOptions{Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot})
	require.NoError(t, err)
	require.NotNil(t, info.InstalledPlugin)
	assert.Equal(t, []string{"claude-code"}, info.InstalledPlugin.Clients,
		"upgrade must preserve the plugin's existing clients, not expand to every detected client")
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestUpgrade_NotUpgradableForImmutableSource(t *testing.T) {
	repoDir := createPluginTestRepo(t, "")
	svc, projectRoot := newGitLockTestService(t, repoDir)
	installGitTestPlugin(t, svc, projectRoot)

	lf := readLockfile(t, projectRoot)
	entry, ok := lf.GetPlugin("my-plugin")
	require.True(t, ok)
	entry.Source = gitPluginRef + "@" + entry.Digest
	lf.UpsertPlugin(entry)
	require.NoError(t, lf.Save(mustOpenRoot(t, projectRoot)))

	addPluginRepoCommit(t, repoDir, "# hello immutable")

	result, err := svc.(*service).Upgrade(t.Context(), plugins.UpgradeOptions{ProjectRoot: projectRoot}) //nolint:forcetypeassert
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, plugins.UpgradeStatusNotUpgradable, result.Outcomes[0].Status)
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestUpgrade_UnknownNameReturnsNotFound(t *testing.T) {
	repoDir := createPluginTestRepo(t, "")
	svc, projectRoot := newGitLockTestService(t, repoDir)

	_, err := svc.(*service).Upgrade(t.Context(), plugins.UpgradeOptions{ //nolint:forcetypeassert
		ProjectRoot: projectRoot, Names: []string{"does-not-exist"},
	})
	require.Error(t, err)
	assert.Equal(t, http.StatusNotFound, httperr.Code(err))
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestUpgrade_FailOnChangesReportsOutcomesWithoutError(t *testing.T) {
	repoDir := createPluginTestRepo(t, "")
	svc, projectRoot := newGitLockTestService(t, repoDir)
	installGitTestPlugin(t, svc, projectRoot)

	before := readLockfile(t, projectRoot)
	beforeEntry, _ := before.GetPlugin("my-plugin")

	addPluginRepoCommit(t, repoDir, "# hello fail-on-changes")

	result, err := svc.(*service).Upgrade(t.Context(), plugins.UpgradeOptions{ //nolint:forcetypeassert
		ProjectRoot: projectRoot, FailOnChanges: true,
	})
	require.NoError(t, err, "fail-on-changes reports outcomes, it does not error")
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, plugins.UpgradeStatusUpgraded, result.Outcomes[0].Status)

	after := readLockfile(t, projectRoot)
	afterEntry, ok := after.GetPlugin("my-plugin")
	require.True(t, ok)
	assert.Equal(t, beforeEntry.Digest, afterEntry.Digest, "fail-on-changes must not rewrite the lock file")
}

type countingLookup struct {
	hits []PluginSearchHit
	n    int
}

func (c *countingLookup) SearchPlugins(context.Context, string) ([]PluginSearchHit, error) {
	c.n++
	return c.hits, nil
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestUpgrade_PlainNameResolvesLocalStoreWithoutRegistry(t *testing.T) {
	svc, projectRoot := newLockTestService(t)
	ociStore, err := ociplugins.NewStore(tempDir(t))
	require.NoError(t, err)

	lookup := &countingLookup{}
	inner := svc.(*service) //nolint:forcetypeassert
	inner.ociStore = ociStore
	inner.pluginLookup = lookup

	_, err = svc.Install(t.Context(), plugins.InstallOptions{
		Name:          "my-plugin",
		LayerData:     makePluginLayerData(t, "my-plugin"),
		AllowUnsigned: true,
		Digest:        validLockDigest(),
		Scope:         plugins.ScopeProject,
		ProjectRoot:   projectRoot,
		Clients:       []string{"claude-code"},
	})
	require.NoError(t, err)
	lookup.n = 0

	d2 := buildTestPlugin(t, ociStore, "my-plugin", "2.0.0")
	require.NoError(t, ociStore.Tag(t.Context(), d2, "my-plugin"))

	result, err := inner.Upgrade(t.Context(), plugins.UpgradeOptions{ProjectRoot: projectRoot, Preview: true})
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, plugins.UpgradeStatusUpgraded, result.Outcomes[0].Status)
	assert.Equal(t, d2.String(), result.Outcomes[0].NewDigest)
	assert.Equal(t, 0, lookup.n, "a local-store hit must not fall through to registry lookup")
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestUpgrade_AppliesSameNameLocalTagWithoutRegistry(t *testing.T) {
	svc, projectRoot := newLockTestService(t)
	ociStore, err := ociplugins.NewStore(tempDir(t))
	require.NoError(t, err)

	lookup := &countingLookup{}
	inner := svc.(*service) //nolint:forcetypeassert
	inner.ociStore = ociStore
	inner.pluginLookup = lookup

	_, err = svc.Install(t.Context(), plugins.InstallOptions{
		Name:          "my-plugin",
		LayerData:     makePluginLayerData(t, "my-plugin"),
		AllowUnsigned: true,
		Digest:        validLockDigest(),
		Scope:         plugins.ScopeProject,
		ProjectRoot:   projectRoot,
		Clients:       []string{"claude-code"},
	})
	require.NoError(t, err)
	lookup.n = 0

	prevRef := "ghcr.io/org/my-plugin@" + validLockDigest()
	existing, ok := readLockfile(t, projectRoot).GetPlugin("my-plugin")
	require.True(t, ok)
	existing.ResolvedReference = prevRef
	require.NoError(t, lockfile.UpsertPluginEntry(mustOpenRoot(t, projectRoot), existing))

	d2 := buildTestPlugin(t, ociStore, "my-plugin", "2.0.0")
	require.NoError(t, ociStore.Tag(t.Context(), d2, "my-plugin"))

	result, err := inner.Upgrade(t.Context(), plugins.UpgradeOptions{ProjectRoot: projectRoot})
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, plugins.UpgradeStatusUpgraded, result.Outcomes[0].Status)
	assert.Equal(t, 0, lookup.n, "apply must not fall through to the registry")

	after, ok := readLockfile(t, projectRoot).GetPlugin("my-plugin")
	require.True(t, ok)
	assert.Equal(t, d2.String(), after.Digest)
	assert.Empty(t, after.ResolvedReference,
		"a local-store upgrade must clear any prior remote resolvedReference so sync can restore by digest")
	assert.Equal(t, "my-plugin", after.Source)

	info, err := svc.Info(t.Context(), plugins.InfoOptions{
		Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	})
	require.NoError(t, err)
	require.NotNil(t, info.InstalledPlugin)
	assert.Equal(t, "my-plugin", info.InstalledPlugin.Reference)

	manifest, err := os.ReadFile(filepath.Join(pluginOnDiskPath(projectRoot, "my-plugin"), ".claude-plugin", "plugin.json")) //nolint:gosec
	require.NoError(t, err)
	assert.Contains(t, string(manifest), "2.0.0")
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestUpgrade_AppliesDifferentlyNamedLocalTagWithoutRegistry(t *testing.T) {
	svc, projectRoot := newLockTestService(t)
	ociStore, err := ociplugins.NewStore(tempDir(t))
	require.NoError(t, err)

	lookup := &countingLookup{}
	inner := svc.(*service) //nolint:forcetypeassert
	inner.ociStore = ociStore
	inner.pluginLookup = lookup

	_, err = svc.Install(t.Context(), plugins.InstallOptions{
		Name:          "my-plugin",
		LayerData:     makePluginLayerData(t, "my-plugin"),
		AllowUnsigned: true,
		Digest:        validLockDigest(),
		Scope:         plugins.ScopeProject,
		ProjectRoot:   projectRoot,
		Clients:       []string{"claude-code"},
	})
	require.NoError(t, err)
	lookup.n = 0

	d2 := buildTestPlugin(t, ociStore, "my-plugin", "2.0.0")
	require.NoError(t, tagAsLocalBuild(t.Context(), ociStore, d2, "my-plugin-dev"))

	result, err := inner.Upgrade(t.Context(), plugins.UpgradeOptions{ProjectRoot: projectRoot})
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, plugins.UpgradeStatusUpgraded, result.Outcomes[0].Status)
	assert.Equal(t, 0, lookup.n, "a differently named local-build tag must not fall through to the registry")

	after, ok := readLockfile(t, projectRoot).GetPlugin("my-plugin")
	require.True(t, ok)
	assert.Equal(t, d2.String(), after.Digest)
	assert.Empty(t, after.ResolvedReference, "a bare local tag must not be written as resolvedReference")

	info, err := svc.Info(t.Context(), plugins.InfoOptions{
		Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	})
	require.NoError(t, err)
	require.NotNil(t, info.InstalledPlugin)
	assert.Equal(t, "my-plugin-dev", info.InstalledPlugin.Reference)
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestUpgrade_PlainNameFallsBackToRegistryWhenLocalMisses(t *testing.T) {
	svc, projectRoot := newLockTestService(t)
	ociStore, err := ociplugins.NewStore(tempDir(t))
	require.NoError(t, err)

	inner := svc.(*service) //nolint:forcetypeassert
	inner.ociStore = ociStore

	_, err = svc.Install(t.Context(), plugins.InstallOptions{
		Name:          "my-plugin",
		LayerData:     makePluginLayerData(t, "my-plugin"),
		AllowUnsigned: true,
		Digest:        validLockDigest(),
		Scope:         plugins.ScopeProject,
		ProjectRoot:   projectRoot,
		Clients:       []string{"claude-code"},
	})
	require.NoError(t, err)

	newer := buildTestPlugin(t, ociStore, "my-plugin", "2.0.0")

	lookup := &countingLookup{hits: []PluginSearchHit{{
		Name:     "my-plugin",
		Packages: []PluginPackage{{Reference: "ghcr.io/org/my-plugin:v2", Type: "oci"}},
	}}}
	ctrl := gomock.NewController(t)
	reg := ocimocks.NewMockRegistryClient(ctrl)
	reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/org/my-plugin:v2").Return(newer, nil)
	inner.pluginLookup = lookup
	inner.registry = reg

	result, err := inner.Upgrade(t.Context(), plugins.UpgradeOptions{ProjectRoot: projectRoot, Preview: true})
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, plugins.UpgradeStatusUpgraded, result.Outcomes[0].Status)
	assert.Equal(t, 1, lookup.n, "a local-store miss must fall through to registry lookup")
	assert.Equal(t, newer.String(), result.Outcomes[0].NewDigest)
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestUpgrade_DoesNotResurrectRemovedLockEntry(t *testing.T) {
	svc, projectRoot := newLockTestService(t)
	installTestPlugin(t, svc, projectRoot, validLockDigest())

	require.NoError(t, lockfile.RemovePluginEntry(mustOpenRoot(t, projectRoot), "my-plugin"))

	inner := svc.(*service) //nolint:forcetypeassert
	outcome := inner.upgradeOne(t.Context(), plugins.UpgradeOptions{ProjectRoot: projectRoot}, "my-plugin")
	assert.Equal(t, plugins.UpgradeStatusFailed, outcome.Status)
	assert.Contains(t, outcome.Error, "no longer in the lock file")

	_, ok := readLockfile(t, projectRoot).GetPlugin("my-plugin")
	assert.False(t, ok, "upgrade must not rewrite a lock entry that was removed")
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/git"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	"github.com/stacklok/toolhive/pkg/storage/sqlite"
)

const gitPluginRef = "git://github.com/org/my-plugin"

// redirectGitClient clones a local fixture repo regardless of the requested
// URL, so Install can use a github.com git:// reference (ParseGitReference
// rejects file:// and localhost) while still exercising the real clone path.
type redirectGitClient struct {
	dir   string
	inner git.Client
}

func (c *redirectGitClient) Clone(ctx context.Context, config *git.CloneConfig) (*git.RepositoryInfo, error) {
	cloned := *config
	cloned.URL = c.dir
	return c.inner.Clone(ctx, &cloned)
}

func (c *redirectGitClient) GetFileContent(repoInfo *git.RepositoryInfo, path string) ([]byte, error) {
	return c.inner.GetFileContent(repoInfo, path)
}

func (c *redirectGitClient) HeadCommit(repoInfo *git.RepositoryInfo) (git.HeadCommit, error) {
	return c.inner.HeadCommit(repoInfo)
}

func (c *redirectGitClient) Cleanup(ctx context.Context, repoInfo *git.RepositoryInfo) error {
	return c.inner.Cleanup(ctx, repoInfo)
}

func newGitLockTestService(t *testing.T, repoDir string) (plugins.PluginService, string) {
	t.Helper()
	t.Setenv(plugins.LockFileEnvVar, "true")

	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sqlite.Open(t.Context(), dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	projectRoot := makeProjectRoot(t)
	adapter := &extractingAdapter{
		base:      filepath.Join(projectRoot, ".claude", "plugins"),
		installer: skills.NewInstaller(),
	}
	svc := New(
		WithStore(sqlite.NewPluginStore(db)),
		WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": adapter}),
		WithClientManager(client.NewTestClientManagerWithHome(t.TempDir())),
		WithGitClient(&redirectGitClient{dir: repoDir, inner: git.NewDefaultGitClient()}),
	)
	return svc, projectRoot
}

func pluginOnDiskPath(projectRoot, name string) string {
	return filepath.Join(projectRoot, ".claude", "plugins", name)
}

func tamperPluginFile(t *testing.T, projectRoot, name string) string {
	t.Helper()
	path := filepath.Join(pluginOnDiskPath(projectRoot, name), "commands", "hello.md")
	require.NoError(t, os.WriteFile(path, []byte("tampered content"), 0o644))
	return path
}

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestSync_ReportsUpToDateWhenNothingChanged(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)
	installTestPlugin(t, svc, projectRoot, validLockDigest())

	result, err := svc.(*service).Sync(t.Context(), plugins.SyncOptions{ProjectRoot: projectRoot}) //nolint:forcetypeassert
	require.NoError(t, err)
	assert.Equal(t, []string{"my-plugin"}, result.AlreadyCurrent)
	assert.Empty(t, result.Installed)
	assert.Empty(t, result.Drifted)
	assert.Empty(t, result.Failed)
}

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestSync_CheckReportsDriftWithoutWriting(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)
	installTestPlugin(t, svc, projectRoot, validLockDigest())

	path := tamperPluginFile(t, projectRoot, "my-plugin")

	result, err := svc.(*service).Sync(t.Context(), plugins.SyncOptions{ProjectRoot: projectRoot, Check: true}) //nolint:forcetypeassert
	require.NoError(t, err)
	assert.Equal(t, []string{"my-plugin"}, result.Drifted)
	assert.Empty(t, result.Installed, "check must not install/write anything")

	stillTampered, err := os.ReadFile(path) //nolint:gosec // fixed test path
	require.NoError(t, err)
	assert.Equal(t, "tampered content", string(stillTampered))
}

//nolint:paralleltest // uses t.Setenv via newGitLockTestService
func TestSync_ReinstallsDriftedContent(t *testing.T) {
	repoDir := createPluginTestRepo(t, "")
	svc, projectRoot := newGitLockTestService(t, repoDir)

	_, err := svc.Install(t.Context(), plugins.InstallOptions{
		Name: gitPluginRef, Scope: plugins.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	path := tamperPluginFile(t, projectRoot, "my-plugin")

	result, err := svc.(*service).Sync(t.Context(), plugins.SyncOptions{ProjectRoot: projectRoot}) //nolint:forcetypeassert
	require.NoError(t, err)
	assert.Equal(t, []string{"my-plugin"}, result.Drifted)
	assert.Equal(t, []string{"my-plugin"}, result.Installed)

	restored, err := os.ReadFile(path) //nolint:gosec // fixed test path
	require.NoError(t, err)
	assert.Contains(t, string(restored), "# hello")
}

//nolint:paralleltest // uses t.Setenv via newGitLockTestService
func TestSync_ReinstallPreservesResolvedReference(t *testing.T) {
	repoDir := createPluginTestRepo(t, "")
	svc, projectRoot := newGitLockTestService(t, repoDir)

	_, err := svc.Install(t.Context(), plugins.InstallOptions{
		Name: gitPluginRef, Scope: plugins.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	before, ok := readLockfile(t, projectRoot).GetPlugin("my-plugin")
	require.True(t, ok)
	require.Equal(t, gitPluginRef, before.ResolvedReference)

	tamperPluginFile(t, projectRoot, "my-plugin")

	result, err := svc.(*service).Sync(t.Context(), plugins.SyncOptions{ProjectRoot: projectRoot}) //nolint:forcetypeassert
	require.NoError(t, err)
	require.Equal(t, []string{"my-plugin"}, result.Installed)

	after, ok := readLockfile(t, projectRoot).GetPlugin("my-plugin")
	require.True(t, ok)
	assert.Equal(t, before.ResolvedReference, after.ResolvedReference,
		"a drift-repair reinstall must preserve ResolvedReference, not overwrite it with the pinned restore form")
}

//nolint:paralleltest // uses t.Setenv via newGitLockTestService
func TestSync_MissingInstallIsRestored(t *testing.T) {
	repoDir := createPluginTestRepo(t, "")
	svc, projectRoot := newGitLockTestService(t, repoDir)

	_, err := svc.Install(t.Context(), plugins.InstallOptions{
		Name: gitPluginRef, Scope: plugins.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	require.NoError(t, os.RemoveAll(pluginOnDiskPath(projectRoot, "my-plugin")))
	require.NoError(t, svc.(*service).store.Delete(t.Context(), "my-plugin", plugins.ScopeProject, projectRoot)) //nolint:forcetypeassert

	result, err := svc.(*service).Sync(t.Context(), plugins.SyncOptions{ProjectRoot: projectRoot}) //nolint:forcetypeassert
	require.NoError(t, err)
	assert.Equal(t, []string{"my-plugin"}, result.Missing)
	assert.Equal(t, []string{"my-plugin"}, result.Installed)
	assert.Empty(t, result.Drifted)

	_, err = svc.Info(t.Context(), plugins.InfoOptions{Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot})
	require.NoError(t, err)
}

//nolint:paralleltest // uses t.Setenv via newGitLockTestService
func TestSync_CheckReportsMissingInstalls(t *testing.T) {
	repoDir := createPluginTestRepo(t, "")
	svc, projectRoot := newGitLockTestService(t, repoDir)

	_, err := svc.Install(t.Context(), plugins.InstallOptions{
		Name: gitPluginRef, Scope: plugins.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	require.NoError(t, os.RemoveAll(pluginOnDiskPath(projectRoot, "my-plugin")))
	require.NoError(t, svc.(*service).store.Delete(t.Context(), "my-plugin", plugins.ScopeProject, projectRoot)) //nolint:forcetypeassert

	result, err := svc.(*service).Sync(t.Context(), plugins.SyncOptions{ProjectRoot: projectRoot, Check: true}) //nolint:forcetypeassert
	require.NoError(t, err)
	assert.Equal(t, []string{"my-plugin"}, result.Missing)
	assert.Empty(t, result.Installed)

	_, err = svc.Info(t.Context(), plugins.InfoOptions{Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot})
	require.Error(t, err, "check must not have installed anything")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestSync_AdoptsUnmanagedInstall(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)
	installTestPlugin(t, svc, projectRoot, validLockDigest())

	require.NoError(t, lockfile.RemovePluginEntry(mustOpenRoot(t, projectRoot), "my-plugin"))
	syncSvc := svc.(*service) //nolint:forcetypeassert
	legacy, err := syncSvc.store.Get(t.Context(), "my-plugin", plugins.ScopeProject, projectRoot)
	require.NoError(t, err)
	legacy.Managed = false
	legacy.Reference = "ghcr.io/org/my-plugin:v1"
	require.NoError(t, syncSvc.store.Update(t.Context(), legacy))

	result, err := syncSvc.Sync(t.Context(), plugins.SyncOptions{ProjectRoot: projectRoot})
	require.NoError(t, err)
	assert.Equal(t, []string{"my-plugin"}, result.NeverManaged)

	result, err = syncSvc.Sync(t.Context(), plugins.SyncOptions{ProjectRoot: projectRoot, Adopt: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"my-plugin"}, result.NeverManaged)
	assert.Empty(t, result.Failed)

	lf := readLockfile(t, projectRoot)
	entry, ok := lf.GetPlugin("my-plugin")
	require.True(t, ok, "adopt must write a lock entry for the unmanaged install")
	assert.NotEmpty(t, entry.ContentDigest)
	assert.False(t, entry.Unsigned)
	assert.Nil(t, entry.Provenance)

	info, err := svc.Info(t.Context(), plugins.InfoOptions{Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot})
	require.NoError(t, err)
	require.NotNil(t, info.InstalledPlugin)
	assert.True(t, info.InstalledPlugin.Managed)
}

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestSync_PrunesRemovedFromLock(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)
	installTestPlugin(t, svc, projectRoot, validLockDigest())

	require.NoError(t, lockfile.RemovePluginEntry(mustOpenRoot(t, projectRoot), "my-plugin"))

	syncer := svc.(*service) //nolint:forcetypeassert
	result, err := syncer.Sync(t.Context(), plugins.SyncOptions{ProjectRoot: projectRoot})
	require.NoError(t, err)
	assert.Equal(t, []string{"my-plugin"}, result.RemovedFromLock)

	result, err = syncer.Sync(t.Context(), plugins.SyncOptions{ProjectRoot: projectRoot, Prune: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"my-plugin"}, result.Pruned)

	_, err = svc.Info(t.Context(), plugins.InfoOptions{Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot})
	require.Error(t, err, "prune must uninstall the plugin")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestSync_DisabledGateReturnsForbidden(t *testing.T) {
	svc, projectRoot := newLockTestService(t, false)

	_, err := svc.(*service).Sync(t.Context(), plugins.SyncOptions{ProjectRoot: projectRoot}) //nolint:forcetypeassert
	require.Error(t, err)
	assert.Equal(t, 403, httperr.Code(err))
}

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestSync_AdoptUpdateFailureRemovesLockEntry(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)
	installTestPlugin(t, svc, projectRoot, validLockDigest())

	require.NoError(t, lockfile.RemovePluginEntry(mustOpenRoot(t, projectRoot), "my-plugin"))
	syncSvc := svc.(*service) //nolint:forcetypeassert
	legacy, err := syncSvc.store.Get(t.Context(), "my-plugin", plugins.ScopeProject, projectRoot)
	require.NoError(t, err)
	legacy.Managed = false
	legacy.Reference = "ghcr.io/org/my-plugin:v1"
	require.NoError(t, syncSvc.store.Update(t.Context(), legacy))

	syncSvc.store = &hookPluginStore{
		PluginStore: syncSvc.store,
		beforeUpdate: func(_ int, p plugins.InstalledPlugin) error {
			if p.Managed {
				return errors.New("db locked")
			}
			return nil
		},
	}

	result, err := syncSvc.Sync(t.Context(), plugins.SyncOptions{ProjectRoot: projectRoot, Adopt: true})
	require.NoError(t, err)
	require.Len(t, result.Failed, 1)
	assert.Equal(t, "my-plugin", result.Failed[0].Name)

	_, ok := readLockfile(t, projectRoot).GetPlugin("my-plugin")
	assert.False(t, ok, "a failed adopt must not leave a lock entry without Managed=true")

	info, err := svc.Info(t.Context(), plugins.InfoOptions{
		Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	})
	require.NoError(t, err)
	require.NotNil(t, info.InstalledPlugin)
	assert.False(t, info.InstalledPlugin.Managed)

	check, err := syncSvc.Sync(t.Context(), plugins.SyncOptions{ProjectRoot: projectRoot, Check: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"my-plugin"}, check.NeverManaged)
	assert.Empty(t, check.AlreadyCurrent, "a split adopt must not look up-to-date")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestSync_AdoptRejectsUnrestorableLocalPin(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)
	installTestPlugin(t, svc, projectRoot, validLockDigest())

	require.NoError(t, lockfile.RemovePluginEntry(mustOpenRoot(t, projectRoot), "my-plugin"))
	syncSvc := svc.(*service) //nolint:forcetypeassert
	legacy, err := syncSvc.store.Get(t.Context(), "my-plugin", plugins.ScopeProject, projectRoot)
	require.NoError(t, err)
	legacy.Managed = false
	legacy.Reference = "my-plugin"
	require.NoError(t, syncSvc.store.Update(t.Context(), legacy))

	result, err := syncSvc.Sync(t.Context(), plugins.SyncOptions{ProjectRoot: projectRoot, Adopt: true})
	require.NoError(t, err)
	require.Len(t, result.Failed, 1)
	assert.Contains(t, result.Failed[0].Error, "not a restorable git or OCI pin")
	assert.Equal(t, plugins.FailureReasonValidationRejected, result.Failed[0].Reason)

	_, ok := readLockfile(t, projectRoot).GetPlugin("my-plugin")
	assert.False(t, ok, "adoption of a bare local tag must not write a lock entry")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestSync_RequestedClientIsNotAlreadyCurrent(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)
	installTestPlugin(t, svc, projectRoot, validLockDigest())

	result, err := svc.(*service).Sync(t.Context(), plugins.SyncOptions{ //nolint:forcetypeassert
		ProjectRoot: projectRoot, Check: true, Clients: []string{"codex"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"my-plugin"}, result.Drifted)
	assert.Empty(t, result.AlreadyCurrent, "a plugin current in one client must not skip a requested extra client")
}

type unhealthyAdapter struct {
	extractingAdapter
}

func (*unhealthyAdapter) Health(context.Context, plugins.DematerializeRequest) error {
	return errors.New("marketplace entry missing")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestSync_UnhealthyRegistrationIsNotAlreadyCurrent(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)
	installTestPlugin(t, svc, projectRoot, validLockDigest())

	inner := svc.(*service) //nolint:forcetypeassert
	inner.materializers["claude-code"] = &unhealthyAdapter{
		extractingAdapter: extractingAdapter{
			base:      filepath.Join(projectRoot, ".claude", "plugins"),
			installer: skills.NewInstaller(),
		},
	}

	result, err := inner.Sync(t.Context(), plugins.SyncOptions{ProjectRoot: projectRoot, Check: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"my-plugin"}, result.Drifted)
	assert.Empty(t, result.AlreadyCurrent, "missing marketplace/settings registration is drift, not current")
}

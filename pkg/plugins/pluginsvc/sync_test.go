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
	ociplugins "github.com/stacklok/toolhive-core/oci/plugins"
	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/git"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	"github.com/stacklok/toolhive/pkg/storage"
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

// newGitLockTestService builds a lock-enabled service whose git installs
// resolve to repoDir. Test repo commits are unsigned, so it defaults to a
// verifier that reports every commit as signed by a fixed test identity —
// tests about verification itself pass their own WithVerifier via extra
// (later options win).
func newGitLockTestService(t *testing.T, repoDir string, extra ...Option) (plugins.PluginService, string) {
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
	home := t.TempDir()
	// Claude Code RelPath is empty; IsClientInstalled checks ~/.claude.json.
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude.json"), []byte("{}"), 0o644))
	opts := append([]Option{
		WithStore(sqlite.NewPluginStore(db)),
		WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": adapter}),
		WithClientManager(client.NewTestClientManagerWithHome(home)),
		WithGitClient(&redirectGitClient{dir: repoDir, inner: git.NewDefaultGitClient()}),
		WithVerifier(alwaysSignedVerifier(t)),
	}, extra...)
	return New(opts...), projectRoot
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

// The documented `--clients all` mode must behave exactly like the empty
// default: expand to every detected plugin-supporting client, not fail
// validation.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestSync_ClientsAllSentinelMatchesDefault(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)
	installTestPlugin(t, svc, projectRoot, validLockDigest())

	result, err := svc.(*service).Sync(t.Context(), plugins.SyncOptions{ //nolint:forcetypeassert
		ProjectRoot: projectRoot, Clients: []string{"All"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"my-plugin"}, result.AlreadyCurrent)
	assert.Empty(t, result.Failed, "--clients all must not produce a validation failure")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestSync_ClientsAllSentinelRejectsCombination(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)
	installTestPlugin(t, svc, projectRoot, validLockDigest())

	result, err := svc.(*service).Sync(t.Context(), plugins.SyncOptions{ //nolint:forcetypeassert
		ProjectRoot: projectRoot, Clients: []string{"all", "claude-code"},
	})
	require.NoError(t, err)
	require.Len(t, result.Failed, 1)
	assert.Contains(t, result.Failed[0].Error, "cannot be combined")
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

	// Adoption of an install with no stored bundle is an unsigned trust
	// decision: without the explicit exception it fails...
	result, err = syncSvc.Sync(t.Context(), plugins.SyncOptions{ProjectRoot: projectRoot, Adopt: true})
	require.NoError(t, err)
	require.Len(t, result.Failed, 1)
	assert.Equal(t, plugins.FailureReasonUnsignedRejected, result.Failed[0].Reason)
	_, ok := readLockfile(t, projectRoot).GetPlugin("my-plugin")
	assert.False(t, ok, "adoption without the unsigned exception must not write a lock entry")

	// ...and with it, the entry records the unsigned state.
	result, err = syncSvc.Sync(t.Context(), plugins.SyncOptions{
		ProjectRoot: projectRoot, Adopt: true, AllowUnsigned: true,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"my-plugin"}, result.NeverManaged)
	assert.Empty(t, result.Failed)

	lf := readLockfile(t, projectRoot)
	entry, ok := lf.GetPlugin("my-plugin")
	require.True(t, ok, "adopt must write a lock entry for the unmanaged install")
	assert.NotEmpty(t, entry.ContentDigest)
	assert.True(t, entry.Unsigned, "an adoption without verifiable trust must record unsigned")
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

	result, err := syncSvc.Sync(t.Context(), plugins.SyncOptions{
		ProjectRoot: projectRoot, Adopt: true, AllowUnsigned: true,
	})
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
func TestSync_AdoptUpdateFailureRestoresExistingLockEntry(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)
	installTestPlugin(t, svc, projectRoot, validLockDigest())

	prev := lockfile.Entry{
		Name:              "my-plugin",
		Source:            "ghcr.io/org/other:v0",
		ResolvedReference: "ghcr.io/org/other@" + validLockDigestAlt(),
		Digest:            validLockDigestAlt(),
		ContentDigest:     "sha256:" + "1111111111111111111111111111111111111111111111111111111111111111",
		Explicit:          true,
	}
	require.NoError(t, lockfile.UpsertPluginEntry(mustOpenRoot(t, projectRoot), prev))

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

	result, err := syncSvc.Sync(t.Context(), plugins.SyncOptions{
		ProjectRoot: projectRoot, Adopt: true, AllowUnsigned: true,
	})
	require.NoError(t, err)
	require.Len(t, result.Failed, 1)

	got, ok := readLockfile(t, projectRoot).GetPlugin("my-plugin")
	require.True(t, ok, "a failed adopt must restore the pre-existing lock pin")
	assert.Equal(t, prev.Source, got.Source)
	assert.Equal(t, prev.Digest, got.Digest)
	assert.Equal(t, prev.ResolvedReference, got.ResolvedReference)
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

	inner := svc.(*service) //nolint:forcetypeassert
	inner.materializers["codex"] = &extractingAdapter{
		base:      filepath.Join(projectRoot, ".agents", "plugins", "toolhive"),
		installer: skills.NewInstaller(),
	}
	// Codex is supported but not installed (no ~/.codex). Explicit --clients
	// must still be allowed and must not report AlreadyCurrent.

	result, err := inner.Sync(t.Context(), plugins.SyncOptions{
		ProjectRoot: projectRoot, Check: true, Clients: []string{"codex"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"my-plugin"}, result.Drifted)
	assert.Empty(t, result.AlreadyCurrent, "a plugin current in one client must not skip a requested extra client")
	assert.Empty(t, result.Failed)
}

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestSync_DefaultExpandsToNewlyDetectedClients(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)
	installTestPlugin(t, svc, projectRoot, validLockDigest())

	inner := svc.(*service) //nolint:forcetypeassert
	inner.materializers["codex"] = &extractingAdapter{
		base:      filepath.Join(projectRoot, ".agents", "plugins", "toolhive"),
		installer: skills.NewInstaller(),
	}
	require.NoError(t, os.MkdirAll(filepath.Join(inner.clientManager.HomeDir(), ".codex"), 0o755))

	result, err := inner.Sync(t.Context(), plugins.SyncOptions{ProjectRoot: projectRoot, Check: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"my-plugin"}, result.Drifted)
	assert.Empty(t, result.AlreadyCurrent, "default sync must not treat a missing detected client as current")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestSync_DefaultIgnoresSupportedButAbsentClients(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)
	installTestPlugin(t, svc, projectRoot, validLockDigest())

	inner := svc.(*service) //nolint:forcetypeassert
	inner.materializers["codex"] = &extractingAdapter{
		base:      filepath.Join(projectRoot, ".agents", "plugins", "toolhive"),
		installer: skills.NewInstaller(),
	}
	// No ~/.codex — Codex supports plugins but is not installed, so default
	// sync must not require it.

	result, err := inner.Sync(t.Context(), plugins.SyncOptions{ProjectRoot: projectRoot, Check: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"my-plugin"}, result.AlreadyCurrent)
	assert.Empty(t, result.Drifted)
}

//nolint:paralleltest // uses t.Setenv via newGitLockTestService
func TestSync_CanonicalNameMismatchFailsBeforeMutate(t *testing.T) {
	repoDir := createPluginTestRepo(t, "")
	svc, projectRoot := newGitLockTestService(t, repoDir)

	_, err := svc.Install(t.Context(), plugins.InstallOptions{
		Name: gitPluginRef, Scope: plugins.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	before := readLockfile(t, projectRoot)
	entry, ok := before.GetPlugin("my-plugin")
	require.True(t, ok)
	require.NoError(t, lockfile.RemovePluginEntry(mustOpenRoot(t, projectRoot), "my-plugin"))

	mismatched := entry
	mismatched.Name = "other-plugin"
	require.NoError(t, lockfile.UpsertPluginEntry(mustOpenRoot(t, projectRoot), mismatched))

	syncSvc := svc.(*service) //nolint:forcetypeassert
	beforeDB, err := syncSvc.store.Get(t.Context(), "my-plugin", plugins.ScopeProject, projectRoot)
	require.NoError(t, err)
	beforeDigest := beforeDB.Digest
	path := filepath.Join(pluginOnDiskPath(projectRoot, "my-plugin"), "commands", "hello.md")
	beforeBytes, err := os.ReadFile(path) //nolint:gosec // fixed test path
	require.NoError(t, err)

	result, err := syncSvc.Sync(t.Context(), plugins.SyncOptions{ProjectRoot: projectRoot})
	require.NoError(t, err)
	require.Len(t, result.Failed, 1)
	assert.Equal(t, "other-plugin", result.Failed[0].Name)
	assert.Equal(t, plugins.FailureReasonValidationRejected, result.Failed[0].Reason)
	assert.Contains(t, result.Failed[0].Error, "does not match lock entry name")

	after, ok := readLockfile(t, projectRoot).GetPlugin("other-plugin")
	require.True(t, ok)
	assert.Equal(t, mismatched.Digest, after.Digest)
	assert.Equal(t, mismatched.ContentDigest, after.ContentDigest)

	_, err = syncSvc.store.Get(t.Context(), "other-plugin", plugins.ScopeProject, projectRoot)
	require.Error(t, err, "canonical mismatch must not create a DB row under the lock name")

	still, err := syncSvc.store.Get(t.Context(), "my-plugin", plugins.ScopeProject, projectRoot)
	require.NoError(t, err)
	assert.Equal(t, beforeDigest, still.Digest)

	afterBytes, err := os.ReadFile(path) //nolint:gosec // fixed test path
	require.NoError(t, err)
	assert.Equal(t, beforeBytes, afterBytes, "on-disk tree must be unchanged after rejected sync")
}

type staleListStore struct {
	storage.PluginStore
	listed []plugins.InstalledPlugin
}

func (s *staleListStore) List(context.Context, storage.ListFilter) ([]plugins.InstalledPlugin, error) {
	out := make([]plugins.InstalledPlugin, len(s.listed))
	copy(out, s.listed)
	return out, nil
}

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestSync_StaleListDoesNotResurrectUninstalledPlugin(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)
	installTestPlugin(t, svc, projectRoot, validLockDigest())

	inner := svc.(*service) //nolint:forcetypeassert
	stale, err := inner.store.List(t.Context(), storage.ListFilter{
		Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	})
	require.NoError(t, err)
	require.NotEmpty(t, stale)

	require.NoError(t, svc.Uninstall(t.Context(), plugins.UninstallOptions{
		Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	}))

	inner.store = &staleListStore{PluginStore: inner.store, listed: stale}

	result, err := inner.Sync(t.Context(), plugins.SyncOptions{ProjectRoot: projectRoot})
	require.NoError(t, err)
	assert.Empty(t, result.Installed)
	assert.Empty(t, result.NeverManaged)

	_, err = svc.Info(t.Context(), plugins.InfoOptions{
		Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	})
	require.Error(t, err, "a stale List snapshot must not resurrect an uninstalled plugin")
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

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestSync_LocalUpgradeRoundTripRestoresExactDigest(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)
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

	d2 := buildTestPlugin(t, ociStore, "my-plugin", "2.0.0")
	require.NoError(t, ociStore.Tag(t.Context(), d2, "my-plugin"))

	upgradeResult, err := inner.Upgrade(t.Context(), plugins.UpgradeOptions{ProjectRoot: projectRoot})
	require.NoError(t, err)
	require.Len(t, upgradeResult.Outcomes, 1)
	assert.Equal(t, plugins.UpgradeStatusUpgraded, upgradeResult.Outcomes[0].Status)
	assert.Equal(t, d2.String(), upgradeResult.Outcomes[0].NewDigest)

	lockEntry, ok := readLockfile(t, projectRoot).GetPlugin("my-plugin")
	require.True(t, ok)
	assert.Equal(t, d2.String(), lockEntry.Digest)
	assert.Empty(t, lockEntry.ResolvedReference)
	assert.Equal(t, "my-plugin", lockEntry.Source)

	require.NoError(t, os.RemoveAll(pluginOnDiskPath(projectRoot, "my-plugin")))
	require.NoError(t, inner.store.Delete(t.Context(), "my-plugin", plugins.ScopeProject, projectRoot))

	syncResult, err := inner.Sync(t.Context(), plugins.SyncOptions{ProjectRoot: projectRoot})
	require.NoError(t, err)
	assert.Equal(t, []string{"my-plugin"}, syncResult.Missing)
	assert.Equal(t, []string{"my-plugin"}, syncResult.Installed)
	assert.Empty(t, syncResult.Failed)

	info, err := svc.Info(t.Context(), plugins.InfoOptions{
		Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	})
	require.NoError(t, err)
	require.NotNil(t, info.InstalledPlugin)
	assert.Equal(t, d2.String(), info.InstalledPlugin.Digest)

	manifest, err := os.ReadFile(filepath.Join(pluginOnDiskPath(projectRoot, "my-plugin"), ".claude-plugin", "plugin.json")) //nolint:gosec
	require.NoError(t, err)
	assert.Contains(t, string(manifest), "2.0.0")

	afterLock, ok := readLockfile(t, projectRoot).GetPlugin("my-plugin")
	require.True(t, ok)
	assert.Equal(t, lockEntry.Digest, afterLock.Digest)
	assert.Empty(t, afterLock.ResolvedReference)
}

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestSync_LocalStorePinDigestMissing(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)
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

	d2 := buildTestPlugin(t, ociStore, "my-plugin", "2.0.0")
	require.NoError(t, ociStore.Tag(t.Context(), d2, "my-plugin"))

	upgradeResult, err := inner.Upgrade(t.Context(), plugins.UpgradeOptions{ProjectRoot: projectRoot})
	require.NoError(t, err)
	require.Len(t, upgradeResult.Outcomes, 1)
	require.Equal(t, plugins.UpgradeStatusUpgraded, upgradeResult.Outcomes[0].Status)

	lockBefore := readLockfile(t, projectRoot)
	entryBefore, ok := lockBefore.GetPlugin("my-plugin")
	require.True(t, ok)
	require.Equal(t, d2.String(), entryBefore.Digest)
	require.Empty(t, entryBefore.ResolvedReference)

	require.NoError(t, os.RemoveAll(pluginOnDiskPath(projectRoot, "my-plugin")))
	require.NoError(t, inner.store.Delete(t.Context(), "my-plugin", plugins.ScopeProject, projectRoot))

	emptyStore, err := ociplugins.NewStore(tempDir(t))
	require.NoError(t, err)
	inner.ociStore = emptyStore

	syncResult, err := inner.Sync(t.Context(), plugins.SyncOptions{ProjectRoot: projectRoot})
	require.NoError(t, err)
	assert.Equal(t, []string{"my-plugin"}, syncResult.Missing)
	assert.Empty(t, syncResult.Installed)
	require.Len(t, syncResult.Failed, 1)
	assert.Equal(t, "my-plugin", syncResult.Failed[0].Name)
	assert.Equal(t, plugins.FailureReasonDigestMissing, syncResult.Failed[0].Reason)

	_, err = inner.store.Get(t.Context(), "my-plugin", plugins.ScopeProject, projectRoot)
	assert.ErrorIs(t, err, storage.ErrNotFound, "digest-missing must not recreate the DB row")
	_, err = os.Stat(pluginOnDiskPath(projectRoot, "my-plugin"))
	assert.True(t, os.IsNotExist(err), "digest-missing must not write plugin files")

	entryAfter, ok := readLockfile(t, projectRoot).GetPlugin("my-plugin")
	require.True(t, ok)
	assert.Equal(t, entryBefore, entryAfter, "digest-missing must leave the lock entry untouched")
}

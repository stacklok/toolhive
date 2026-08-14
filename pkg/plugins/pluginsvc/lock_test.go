// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	"github.com/stacklok/toolhive/pkg/storage"
	"github.com/stacklok/toolhive/pkg/storage/sqlite"
)

// extractingAdapter materializes by ExtractPlugin into <base>/<name>, matching
// the canonical plugin tree contentDigest hashes (not marketplace.json).
type extractingAdapter struct {
	base      string
	installer skills.Installer
}

func (a *extractingAdapter) Materialize(_ context.Context, req plugins.MaterializeRequest) (*plugins.MaterializeResult, error) {
	dir := filepath.Join(a.base, req.Name)
	if _, err := a.installer.ExtractPlugin(req.LayerData, dir, true); err != nil {
		return nil, err
	}
	return &plugins.MaterializeResult{
		InstallPath:         dir,
		InstalledComponents: []plugins.ComponentType{plugins.ComponentCommands},
	}, nil
}

func (a *extractingAdapter) Dematerialize(_ context.Context, req plugins.DematerializeRequest) error {
	return a.installer.Remove(filepath.Join(a.base, req.Name))
}

func (*extractingAdapter) SupportedComponents() []plugins.ComponentType {
	return []plugins.ComponentType{plugins.ComponentCommands}
}

func (*extractingAdapter) ScopeSupport() plugins.ScopeSupport {
	return plugins.ScopeSupport{}
}

func newLockTestService(t *testing.T, enableGate bool) (plugins.PluginService, string) {
	t.Helper()
	if enableGate {
		t.Setenv(plugins.LockFileEnvVar, "true")
	} else {
		t.Setenv(plugins.LockFileEnvVar, "")
	}

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
	)
	return svc, projectRoot
}

func mustOpenRoot(t *testing.T, projectRoot string) lockfile.Root {
	t.Helper()
	root, err := lockfile.OpenRoot(projectRoot)
	require.NoError(t, err)
	return root
}

func readLockfile(t *testing.T, projectRoot string) *lockfile.Lockfile {
	t.Helper()
	lf, err := lockfile.Load(mustOpenRoot(t, projectRoot))
	require.NoError(t, err)
	return lf
}

func validLockDigest() string {
	return "sha256:" + "abababababababababababababababababababababababababababababababab"
}

func validLockDigestAlt() string {
	return "sha256:" + "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"
}

func installTestPlugin(t *testing.T, svc plugins.PluginService, projectRoot, digest string) *plugins.InstallResult {
	t.Helper()
	const name = "my-plugin"
	result, err := svc.Install(t.Context(), plugins.InstallOptions{
		Name:        name,
		LayerData:   makePluginLayerData(t, name),
		Digest:      digest,
		Scope:       plugins.ScopeProject,
		ProjectRoot: projectRoot,
		Clients:     []string{"claude-code"},
	})
	require.NoError(t, err)
	return result
}

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestInstallProjectScope_RecordsExplicitEntry(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)

	result := installTestPlugin(t, svc, projectRoot, validLockDigest())
	assert.True(t, result.Plugin.Managed, "project-scope install must be marked lock-managed")
	assert.NotEmpty(t, result.ContentDigest)

	lf := readLockfile(t, projectRoot)
	entry, ok := lf.GetPlugin("my-plugin")
	require.True(t, ok, "expected a plugins: lock entry for my-plugin")
	assert.Equal(t, "my-plugin", entry.Source, "source must be exactly what the caller requested")
	assert.Equal(t, validLockDigest(), entry.Digest)
	assert.Equal(t, result.ContentDigest, entry.ContentDigest)
	assert.True(t, entry.Explicit)
	assert.Empty(t, entry.RequiredBy)
	assert.Empty(t, lf.Skills, "plugin install must not write a skills: entry")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestInstallProjectScope_DisabledGateDoesNotWriteLock(t *testing.T) {
	svc, projectRoot := newLockTestService(t, false)

	result := installTestPlugin(t, svc, projectRoot, validLockDigest())
	assert.False(t, result.Plugin.Managed)

	_, err := os.Stat(filepath.Join(projectRoot, lockfile.FileName))
	assert.True(t, os.IsNotExist(err), "lock file must not be written when the feature is disabled")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestInstallUserScope_DoesNotWriteLock(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)

	result, err := svc.Install(t.Context(), plugins.InstallOptions{
		Name:      "my-plugin",
		LayerData: makePluginLayerData(t, "my-plugin"),
		Digest:    validLockDigest(),
		Scope:     plugins.ScopeUser,
		Clients:   []string{"claude-code"},
	})
	require.NoError(t, err)
	assert.False(t, result.Plugin.Managed)

	_, err = os.Stat(filepath.Join(projectRoot, lockfile.FileName))
	assert.True(t, os.IsNotExist(err), "user-scope install must not write a lock file")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestInstallProjectScope_PreservesExistingSkillsKey(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)

	require.NoError(t, lockfile.UpsertEntry(mustOpenRoot(t, projectRoot), lockfile.Entry{
		Name:   "code-review",
		Source: "code-review",
		Digest: validLockDigest(),
	}))

	installTestPlugin(t, svc, projectRoot, validLockDigest())

	lf := readLockfile(t, projectRoot)
	_, ok := lf.Get("code-review")
	assert.True(t, ok, "a plugin install must not drop existing skills: entries")
	_, ok = lf.GetPlugin("my-plugin")
	assert.True(t, ok)
}

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestInstallProjectScope_LockWriteFailureRollsBackInstall(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)

	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, lockfile.FileName), 0o755))

	_, err := svc.Install(t.Context(), plugins.InstallOptions{
		Name:        "my-plugin",
		LayerData:   makePluginLayerData(t, "my-plugin"),
		Digest:      validLockDigest(),
		Scope:       plugins.ScopeProject,
		ProjectRoot: projectRoot,
		Clients:     []string{"claude-code"},
	})
	require.Error(t, err, "install must fail when the lock file cannot be written")
	assert.Equal(t, http.StatusInternalServerError, httperr.Code(err))

	_, err = svc.Info(t.Context(), plugins.InfoOptions{
		Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	})
	require.Error(t, err, "the DB record must be rolled back so a retry starts fresh")

	_, err = os.Stat(filepath.Join(projectRoot, ".claude", "plugins", "my-plugin"))
	assert.True(t, os.IsNotExist(err), "a failed fresh install must dematerialize on rollback")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestInstallProjectScope_RollbackRestoresPreExistingState(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)

	first := installTestPlugin(t, svc, projectRoot, validLockDigest())
	before, ok := readLockfile(t, projectRoot).GetPlugin("my-plugin")
	require.True(t, ok)
	helloPath := filepath.Join(projectRoot, ".claude", "plugins", "my-plugin", "commands", "hello.md")
	beforeHello, err := os.ReadFile(helloPath) //nolint:gosec // test fixture path
	require.NoError(t, err)

	require.NoError(t, os.Chmod(projectRoot, 0o555))
	t.Cleanup(func() { _ = os.Chmod(projectRoot, 0o755) })

	_, err = svc.Install(t.Context(), plugins.InstallOptions{
		Name:        "my-plugin",
		LayerData:   makePluginLayerDataWithBody(t, "my-plugin", "# hello v2"),
		Digest:      validLockDigestAlt(),
		Scope:       plugins.ScopeProject,
		ProjectRoot: projectRoot,
		Clients:     []string{"claude-code"},
	})
	require.Error(t, err, "reinstall must fail when the lock file cannot be written")

	require.NoError(t, os.Chmod(projectRoot, 0o755))
	after, ok := readLockfile(t, projectRoot).GetPlugin("my-plugin")
	require.True(t, ok, "a transient failure must not destroy the pre-existing lock entry")
	assert.Equal(t, before.Digest, after.Digest, "the previous pin must be restored")

	info, err := svc.Info(t.Context(), plugins.InfoOptions{
		Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	})
	require.NoError(t, err, "the pre-existing DB record must survive")
	assert.Equal(t, first.Plugin.Digest, info.InstalledPlugin.Digest)

	afterHello, err := os.ReadFile(helloPath) //nolint:gosec // test fixture path
	require.NoError(t, err, "the previous materialization must be restored")
	assert.Equal(t, beforeHello, afterHello)
}

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestUninstall_RemovesPluginLockEntry(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)
	installTestPlugin(t, svc, projectRoot, validLockDigest())

	err := svc.Uninstall(t.Context(), plugins.UninstallOptions{
		Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	})
	require.NoError(t, err)

	_, ok := readLockfile(t, projectRoot).GetPlugin("my-plugin")
	assert.False(t, ok, "uninstall must remove the plugins: entry")

	_, err = svc.Info(t.Context(), plugins.InfoOptions{
		Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	})
	require.Error(t, err)
}

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestUninstall_LockWriteFailureAbortsBeforeDestruction(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)
	installTestPlugin(t, svc, projectRoot, validLockDigest())

	require.NoError(t, os.Chmod(projectRoot, 0o555))
	t.Cleanup(func() { _ = os.Chmod(projectRoot, 0o755) })

	err := svc.Uninstall(t.Context(), plugins.UninstallOptions{
		Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	})
	require.Error(t, err, "uninstall must fail when the lock entry cannot be removed")

	require.NoError(t, os.Chmod(projectRoot, 0o755))
	info, err := svc.Info(t.Context(), plugins.InfoOptions{
		Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	})
	require.NoError(t, err, "the plugin must remain fully installed")
	assert.NotNil(t, info.InstalledPlugin)
	_, ok := readLockfile(t, projectRoot).GetPlugin("my-plugin")
	assert.True(t, ok, "the lock entry must be untouched")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestUninstall_DoesNotTouchSkillsKey(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)
	require.NoError(t, lockfile.UpsertEntry(mustOpenRoot(t, projectRoot), lockfile.Entry{
		Name:   "code-review",
		Source: "code-review",
		Digest: validLockDigest(),
	}))
	installTestPlugin(t, svc, projectRoot, validLockDigest())

	require.NoError(t, svc.Uninstall(t.Context(), plugins.UninstallOptions{
		Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	}))

	lf := readLockfile(t, projectRoot)
	_, ok := lf.Get("code-review")
	assert.True(t, ok, "uninstalling a plugin must not drop skills: entries")
	_, ok = lf.GetPlugin("my-plugin")
	assert.False(t, ok)
}

type hookPluginStore struct {
	storage.PluginStore
	beforeDelete func() error
}

func (s *hookPluginStore) Delete(ctx context.Context, name string, scope plugins.Scope, projectRoot string) error {
	if s.beforeDelete != nil {
		if err := s.beforeDelete(); err != nil {
			return err
		}
	}
	return s.PluginStore.Delete(ctx, name, scope, projectRoot)
}

type failingDematerializeAdapter struct {
	plugins.MaterializationAdapter
	err error
}

func (a *failingDematerializeAdapter) Dematerialize(context.Context, plugins.DematerializeRequest) error {
	return a.err
}

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestUninstall_DematerializeFailureRestoresLockEntry(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)
	installTestPlugin(t, svc, projectRoot, validLockDigest())

	inner := svc.(*service) //nolint:forcetypeassert
	inner.materializers["claude-code"] = &failingDematerializeAdapter{
		MaterializationAdapter: inner.materializers["claude-code"],
		err:                    errors.New("permission denied"),
	}

	err := svc.Uninstall(t.Context(), plugins.UninstallOptions{
		Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")

	_, ok := readLockfile(t, projectRoot).GetPlugin("my-plugin")
	assert.True(t, ok, "a failed dematerialize must restore the lock entry")

	info, err := svc.Info(t.Context(), plugins.InfoOptions{
		Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	})
	require.NoError(t, err, "the DB record must survive so uninstall can be retried")
	assert.NotNil(t, info.InstalledPlugin)
}

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestUninstall_StoreDeleteFailureRestoresLockEntry(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)
	installTestPlugin(t, svc, projectRoot, validLockDigest())

	inner := svc.(*service) //nolint:forcetypeassert
	inner.store = &hookPluginStore{
		PluginStore:  inner.store,
		beforeDelete: func() error { return errors.New("db locked") },
	}

	err := svc.Uninstall(t.Context(), plugins.UninstallOptions{
		Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "db locked")

	_, ok := readLockfile(t, projectRoot).GetPlugin("my-plugin")
	assert.True(t, ok, "a failed DB delete must restore the lock entry")

	info, err := svc.Info(t.Context(), plugins.InfoOptions{
		Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	})
	require.NoError(t, err, "the plugin must remain installed")
	assert.NotNil(t, info.InstalledPlugin)
}

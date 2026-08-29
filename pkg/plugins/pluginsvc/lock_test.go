// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/groups"
	groupmocks "github.com/stacklok/toolhive/pkg/groups/mocks"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/plugins/adapters"
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

func (*extractingAdapter) EnsureRegistered(context.Context, plugins.DematerializeRequest) error {
	return nil
}

func (a *extractingAdapter) Health(_ context.Context, req plugins.DematerializeRequest) error {
	if _, err := os.Stat(filepath.Join(a.base, req.Name)); err != nil {
		return fmt.Errorf("plugin directory missing: %w", err)
	}
	return nil
}

func (*extractingAdapter) SupportedComponents() []plugins.ComponentType {
	return []plugins.ComponentType{plugins.ComponentCommands}
}

func (*extractingAdapter) ScopeSupport() plugins.ScopeSupport {
	return plugins.ScopeSupport{}
}

func newLockTestService(t *testing.T, enableGate bool, extra ...Option) (plugins.PluginService, string) {
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
	home := t.TempDir()
	// Claude Code RelPath is empty; IsClientInstalled checks ~/.claude.json.
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude.json"), []byte("{}"), 0o644))
	opts := append([]Option{
		WithStore(sqlite.NewPluginStore(db)),
		WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": adapter}),
		WithClientManager(client.NewTestClientManagerWithHome(home)),
	}, extra...)
	return New(opts...), projectRoot
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
		Name:          name,
		LayerData:     makePluginLayerData(t, name),
		AllowUnsigned: true,
		Digest:        digest,
		Scope:         plugins.ScopeProject,
		ProjectRoot:   projectRoot,
		Clients:       []string{"claude-code"},
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
		Name:          "my-plugin",
		LayerData:     makePluginLayerData(t, "my-plugin"),
		AllowUnsigned: true,
		Digest:        validLockDigest(),
		Scope:         plugins.ScopeUser,
		Clients:       []string{"claude-code"},
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
		Name:          "my-plugin",
		LayerData:     makePluginLayerData(t, "my-plugin"),
		AllowUnsigned: true,
		Digest:        validLockDigest(),
		Scope:         plugins.ScopeProject,
		ProjectRoot:   projectRoot,
		Clients:       []string{"claude-code"},
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

// TestInstallProjectScope_RollbackRestoresPreExistingState exercises the real
// prevEntry restore branch: the failure is injected AFTER the lock entry was
// overwritten (recordLockState's managed-flag Update fails), so rollback must
// reinstate the previous pin, DB record, and files — not merely observe that
// nothing was written.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestInstallProjectScope_RollbackRestoresPreExistingState(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)

	first := installTestPlugin(t, svc, projectRoot, validLockDigest())
	before, ok := readLockfile(t, projectRoot).GetPlugin("my-plugin")
	require.True(t, ok)
	helloPath := filepath.Join(projectRoot, ".claude", "plugins", "my-plugin", "commands", "hello.md")
	beforeHello, err := os.ReadFile(helloPath) //nolint:gosec // test fixture path
	require.NoError(t, err)

	// Drift state: the lock entry exists but the DB record is unmanaged, so
	// recordLockState writes the lock entry and THEN updates the DB record —
	// giving rollback a genuinely overwritten entry to restore.
	inner := svc.(*service) //nolint:forcetypeassert
	drifted, err := inner.store.Get(t.Context(), "my-plugin", plugins.ScopeProject, projectRoot)
	require.NoError(t, err)
	drifted.Managed = false
	require.NoError(t, inner.store.Update(t.Context(), drifted))

	var digestAtFailure string
	inner.store = &hookPluginStore{
		PluginStore: inner.store,
		beforeUpdate: func(call int, _ plugins.InstalledPlugin) error {
			// Call 1 persists the new digest (materializeAndPersist); call 2
			// is recordLockState marking the record managed — after the lock
			// entry was already rewritten. Fail there.
			if call == 2 {
				if e, ok := readLockfile(t, projectRoot).GetPlugin("my-plugin"); ok {
					digestAtFailure = e.Digest
				}
				return errors.New("db update unavailable")
			}
			return nil
		},
	}

	_, err = svc.Install(t.Context(), plugins.InstallOptions{
		Name:          "my-plugin",
		LayerData:     makePluginLayerDataWithBody(t, "my-plugin", "# hello v2"),
		AllowUnsigned: true,
		Digest:        validLockDigestAlt(),
		Scope:         plugins.ScopeProject,
		ProjectRoot:   projectRoot,
		Clients:       []string{"claude-code"},
	})
	require.Error(t, err, "reinstall must fail when marking the record managed fails")
	assert.Contains(t, err.Error(), "db update unavailable")
	assert.Equal(t, validLockDigestAlt(), digestAtFailure,
		"precondition: the lock entry must have been overwritten before the injected failure")

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

// A rollback whose own DB compensation fails must join that error with the
// trigger instead of reporting only the original failure.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestInstallProjectScope_RollbackCompensationErrorIsJoined(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)
	installTestPlugin(t, svc, projectRoot, validLockDigest())

	inner := svc.(*service) //nolint:forcetypeassert
	drifted, err := inner.store.Get(t.Context(), "my-plugin", plugins.ScopeProject, projectRoot)
	require.NoError(t, err)
	drifted.Managed = false
	require.NoError(t, inner.store.Update(t.Context(), drifted))

	// Fail recordLockState's managed-flag Update AND the rollback's
	// pre-existing record restore that follows it.
	inner.store = &hookPluginStore{
		PluginStore: inner.store,
		beforeUpdate: func(call int, _ plugins.InstalledPlugin) error {
			if call >= 2 {
				return errors.New("db update unavailable")
			}
			return nil
		},
	}

	_, err = svc.Install(t.Context(), plugins.InstallOptions{
		Name:          "my-plugin",
		LayerData:     makePluginLayerDataWithBody(t, "my-plugin", "# hello v2"),
		AllowUnsigned: true,
		Digest:        validLockDigestAlt(),
		Scope:         plugins.ScopeProject,
		ProjectRoot:   projectRoot,
		Clients:       []string{"claude-code"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "recording plugin in project lock file",
		"the trigger failure must be reported")
	assert.Contains(t, err.Error(), "restoring pre-existing DB record",
		"the failed compensation must be joined into the returned error")
}

// A rollback must not remove a group membership this install did not add.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestInstallProjectScope_RollbackKeepsPreExistingGroupMembership(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)
	ctrl := gomock.NewController(t)
	gm := groupmocks.NewMockManager(ctrl)

	// The plugin is already a member, so AddPluginToGroup reports added=false
	// and rollback must leave the membership alone (no Update calls at all).
	gm.EXPECT().Get(gomock.Any(), groups.DefaultGroup).
		Return(&groups.Group{Name: groups.DefaultGroup, Plugins: []string{"my-plugin"}}, nil)

	inner := svc.(*service) //nolint:forcetypeassert
	inner.groupManager = gm

	// Lock writes fail after group registration: the project root is
	// read-only, so Load succeeds but recordLockEntry's write fails.
	// Extraction still works because the plugin tree lives in a
	// pre-existing writable subdirectory.
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, ".claude", "plugins"), 0o755))
	require.NoError(t, os.Chmod(projectRoot, 0o555))
	t.Cleanup(func() { _ = os.Chmod(projectRoot, 0o755) })

	_, err := svc.Install(t.Context(), plugins.InstallOptions{
		Name:          "my-plugin",
		LayerData:     makePluginLayerData(t, "my-plugin"),
		AllowUnsigned: true,
		Digest:        validLockDigest(),
		Scope:         plugins.ScopeProject,
		ProjectRoot:   projectRoot,
		Clients:       []string{"claude-code"},
	})
	require.Error(t, err, "install must fail when the lock entry cannot be written")
	// gomock verifies no gm.Update ran: rollback did not touch the
	// pre-existing membership it did not create.
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
	// beforeUpdate runs before each Update with the 1-based call count and
	// the record being written; returning an error fails that Update.
	beforeUpdate func(call int, pl plugins.InstalledPlugin) error
	updateCalls  int
}

func (s *hookPluginStore) Delete(ctx context.Context, name string, scope plugins.Scope, projectRoot string) error {
	if s.beforeDelete != nil {
		if err := s.beforeDelete(); err != nil {
			return err
		}
	}
	return s.PluginStore.Delete(ctx, name, scope, projectRoot)
}

func (s *hookPluginStore) Update(ctx context.Context, pl plugins.InstalledPlugin) error {
	s.updateCalls++
	if s.beforeUpdate != nil {
		if err := s.beforeUpdate(s.updateCalls, pl); err != nil {
			return err
		}
	}
	return s.PluginStore.Update(ctx, pl)
}

type failingDematerializeAdapter struct {
	plugins.MaterializationAdapter
	err   error
	after func()
}

func (a *failingDematerializeAdapter) Dematerialize(_ context.Context, _ plugins.DematerializeRequest) error {
	if a.after != nil {
		a.after()
	}
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

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestUninstall_LockRestoreErrorIsJoined(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)
	installTestPlugin(t, svc, projectRoot, validLockDigest())

	inner := svc.(*service) //nolint:forcetypeassert
	inner.materializers["claude-code"] = &failingDematerializeAdapter{
		MaterializationAdapter: inner.materializers["claude-code"],
		err:                    errors.New("permission denied"),
		after: func() {
			lockPath := filepath.Join(projectRoot, lockfile.FileName)
			require.NoError(t, os.Remove(lockPath))
			require.NoError(t, os.Mkdir(lockPath, 0o755))
		},
	}

	err := svc.Uninstall(t.Context(), plugins.UninstallOptions{
		Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
	assert.Contains(t, err.Error(), "restoring lock entry")
}

func TestSnapshotRestore_PreservesExecutableMode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	hook := filepath.Join(dir, "hooks", "preinstall.sh")
	require.NoError(t, os.MkdirAll(filepath.Dir(hook), 0o750))
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\necho hi\n"), 0o600))
	require.NoError(t, os.Chmod(hook, 0o755))
	md := filepath.Join(dir, "commands", "hello.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(md), 0o750))
	require.NoError(t, os.WriteFile(md, []byte("# hello"), 0o644))

	// An empty directory must survive the snapshot/restore round trip too.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills", "empty"), 0o750))

	tree, err := snapshotDir(dir)
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0o755), tree.files[filepath.Join("hooks", "preinstall.sh")].mode)
	assert.Equal(t, fs.FileMode(0o644), tree.files[filepath.Join("commands", "hello.md")].mode)

	dest := t.TempDir()
	require.NoError(t, restoreDir(dest, tree))

	info, err := os.Stat(filepath.Join(dest, "hooks", "preinstall.sh"))
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0o755), info.Mode().Perm())
	mdInfo, err := os.Stat(filepath.Join(dest, "commands", "hello.md"))
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0o644), mdInfo.Mode().Perm())
	assert.DirExists(t, filepath.Join(dest, "skills", "empty"),
		"empty directories must be recreated on restore")
}

type failingMaterializeAdapter struct {
	err error
}

func (a *failingMaterializeAdapter) Materialize(context.Context, plugins.MaterializeRequest) (*plugins.MaterializeResult, error) {
	return nil, a.err
}

func (*failingMaterializeAdapter) Dematerialize(context.Context, plugins.DematerializeRequest) error {
	return nil
}

func (*failingMaterializeAdapter) EnsureRegistered(context.Context, plugins.DematerializeRequest) error {
	return nil
}

func (*failingMaterializeAdapter) Health(context.Context, plugins.DematerializeRequest) error {
	return nil
}

func (*failingMaterializeAdapter) SupportedComponents() []plugins.ComponentType {
	return nil
}

func (*failingMaterializeAdapter) ScopeSupport() plugins.ScopeSupport {
	return plugins.ScopeSupport{}
}

// extractThenFailAdapter extracts the plugin tree then fails, matching Claude
// Code's Materialize: ExtractPlugin succeeds, marketplace/settings write fails.
type extractThenFailAdapter struct {
	extractingAdapter
	err error
}

func (a *extractThenFailAdapter) Materialize(ctx context.Context, req plugins.MaterializeRequest) (*plugins.MaterializeResult, error) {
	if _, err := a.extractingAdapter.Materialize(ctx, req); err != nil {
		return nil, err
	}
	return nil, a.err
}

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestInstall_MaterializeFailureAfterExtractRemovesTree(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)
	inner := svc.(*service) //nolint:forcetypeassert
	base := filepath.Join(projectRoot, ".claude", "plugins")
	inner.materializers["claude-code"] = &extractThenFailAdapter{
		extractingAdapter: extractingAdapter{base: base, installer: skills.NewInstaller()},
		err:               errors.New("marketplace write failed"),
	}

	_, err := svc.Install(t.Context(), plugins.InstallOptions{
		Name:          "my-plugin",
		LayerData:     makePluginLayerData(t, "my-plugin"),
		AllowUnsigned: true,
		Digest:        validLockDigest(),
		Scope:         plugins.ScopeProject,
		ProjectRoot:   projectRoot,
		Clients:       []string{"claude-code"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "marketplace write failed")

	_, statErr := os.Stat(filepath.Join(base, "my-plugin"))
	assert.ErrorIs(t, statErr, os.ErrNotExist, "the extracted tree must be dematerialized")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestInstallProjectScope_LockWriteFailureRemovesGroupMembership(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)
	ctrl := gomock.NewController(t)
	gm := groupmocks.NewMockManager(ctrl)

	var members []string
	gm.EXPECT().Get(gomock.Any(), groups.DefaultGroup).DoAndReturn(
		func(context.Context, string) (*groups.Group, error) {
			cp := make([]string, len(members))
			copy(cp, members)
			return &groups.Group{Name: groups.DefaultGroup, Plugins: cp}, nil
		},
	).AnyTimes()
	gm.EXPECT().Update(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, g *groups.Group) error {
			members = append([]string(nil), g.Plugins...)
			if len(g.Plugins) > 0 {
				// After group registration (and the prior-lock snapshot), make
				// the lock path unwritable so recordLockState fails.
				lockPath := filepath.Join(projectRoot, lockfile.FileName)
				_ = os.Remove(lockPath)
				require.NoError(t, os.MkdirAll(lockPath, 0o755))
			}
			return nil
		},
	).Times(2)

	inner := svc.(*service) //nolint:forcetypeassert
	inner.groupManager = gm

	_, err := svc.Install(t.Context(), plugins.InstallOptions{
		Name:          "my-plugin",
		LayerData:     makePluginLayerData(t, "my-plugin"),
		AllowUnsigned: true,
		Digest:        validLockDigest(),
		Scope:         plugins.ScopeProject,
		ProjectRoot:   projectRoot,
		Clients:       []string{"claude-code"},
	})
	require.Error(t, err)
	assert.Empty(t, members, "a failed fresh install must not leave the plugin in the group")
}

//nolint:paralleltest // uses t.Setenv
func TestInstallUpgrade_SecondClientFailureRestoresRegistration(t *testing.T) {
	t.Setenv(plugins.LockFileEnvVar, "true")

	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sqlite.Open(t.Context(), dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	projectRoot := makeProjectRoot(t)
	cm := client.NewTestClientManagerWithHome(t.TempDir())
	svc := New(
		WithStore(sqlite.NewPluginStore(db)),
		WithMaterializers(map[string]plugins.MaterializationAdapter{
			"claude-code": adapters.NewClaudeCodeAdapter(cm),
			"codex":       &failingMaterializeAdapter{err: errors.New("disk full")},
		}),
		WithClientManager(cm),
	)

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

	settingsPath := filepath.Join(projectRoot, ".claude", "settings.json")
	before, err := os.ReadFile(settingsPath) //nolint:gosec // test fixture
	require.NoError(t, err)
	assert.Contains(t, string(before), "my-plugin@toolhive")

	_, err = svc.Install(t.Context(), plugins.InstallOptions{
		Name:          "my-plugin",
		LayerData:     makePluginLayerDataWithBody(t, "my-plugin", "# hello v2"),
		AllowUnsigned: true,
		Digest:        validLockDigestAlt(),
		Scope:         plugins.ScopeProject,
		ProjectRoot:   projectRoot,
		Clients:       []string{"codex"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disk full")

	after, err := os.ReadFile(settingsPath) //nolint:gosec // test fixture
	require.NoError(t, err)
	assert.Contains(t, string(after), "my-plugin@toolhive",
		"a failed upgrade must restore Claude Code settings registration")

	hello, err := os.ReadFile(filepath.Join(projectRoot, ".claude", "plugins", "my-plugin", "commands", "hello.md")) //nolint:gosec
	require.NoError(t, err)
	assert.Equal(t, "# hello", string(hello), "the previous plugin tree must be restored")
}

//nolint:paralleltest // uses t.Setenv
func TestUninstall_PartialDematerializeRestoresAllClients(t *testing.T) {
	t.Setenv(plugins.LockFileEnvVar, "true")

	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sqlite.Open(t.Context(), dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	projectRoot := makeProjectRoot(t)
	claude := &extractingAdapter{
		base:      filepath.Join(projectRoot, ".claude", "plugins"),
		installer: skills.NewInstaller(),
	}
	codex := &extractingAdapter{
		base:      filepath.Join(projectRoot, ".agents", "plugins", "toolhive"),
		installer: skills.NewInstaller(),
	}
	svc := New(
		WithStore(sqlite.NewPluginStore(db)),
		WithMaterializers(map[string]plugins.MaterializationAdapter{
			"claude-code": claude,
			"codex":       codex,
		}),
		WithClientManager(client.NewTestClientManagerWithHome(t.TempDir())),
	)

	_, err = svc.Install(t.Context(), plugins.InstallOptions{
		Name:          "my-plugin",
		LayerData:     makePluginLayerData(t, "my-plugin"),
		AllowUnsigned: true,
		Digest:        validLockDigest(),
		Scope:         plugins.ScopeProject,
		ProjectRoot:   projectRoot,
		Clients:       []string{"claude-code", "codex"},
	})
	require.NoError(t, err)

	inner := svc.(*service) //nolint:forcetypeassert
	inner.materializers["codex"] = &failingDematerializeAdapter{
		MaterializationAdapter: codex,
		err:                    errors.New("permission denied"),
	}

	err = svc.Uninstall(t.Context(), plugins.UninstallOptions{
		Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	})
	require.Error(t, err)

	_, err = os.Stat(filepath.Join(projectRoot, ".claude", "plugins", "my-plugin", "commands", "hello.md"))
	require.NoError(t, err, "the successfully dematerialized client must be restored")

	info, err := svc.Info(t.Context(), plugins.InfoOptions{
		Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	})
	require.NoError(t, err)
	require.NotNil(t, info.InstalledPlugin)
	_, ok := readLockfile(t, projectRoot).GetPlugin("my-plugin")
	assert.True(t, ok, "the lock entry must be restored")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestInstallFresh_LockWriteFailureRestoresPreexistingTree(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)
	cm := client.NewTestClientManagerWithHome(t.TempDir())
	inner := svc.(*service) //nolint:forcetypeassert
	inner.clientManager = cm

	pluginDir, err := cm.GetPluginPath(client.ClaudeCode, "my-plugin", plugins.ScopeProject, projectRoot)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(pluginDir, "commands"), 0o750))
	prior := []byte("# prior unmanaged")
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "commands", "hello.md"), prior, 0o644))

	// Make lock writes fail after extraction/DB create.
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, lockfile.FileName), 0o755))

	_, err = svc.Install(t.Context(), plugins.InstallOptions{
		Name:          "my-plugin",
		LayerData:     makePluginLayerDataWithBody(t, "my-plugin", "# installed"),
		AllowUnsigned: true,
		Digest:        validLockDigest(),
		Scope:         plugins.ScopeProject,
		ProjectRoot:   projectRoot,
		Clients:       []string{"claude-code"},
		Force:         true,
	})
	require.Error(t, err)

	got, readErr := os.ReadFile(filepath.Join(pluginDir, "commands", "hello.md")) //nolint:gosec
	require.NoError(t, readErr)
	assert.Equal(t, string(prior), string(got),
		"a failed Force install must restore the pre-existing unmanaged tree")

	_, infoErr := svc.Info(t.Context(), plugins.InfoOptions{
		Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	})
	require.ErrorIs(t, infoErr, storage.ErrNotFound)
}

// registrationTrackingAdapter mimics Claude Code: Materialize extracts and
// registers, Dematerialize removes and deregisters, EnsureRegistered
// re-registers, Health fails unless registered.
type registrationTrackingAdapter struct {
	extractingAdapter
	registered bool
}

func (a *registrationTrackingAdapter) Materialize(
	ctx context.Context, req plugins.MaterializeRequest,
) (*plugins.MaterializeResult, error) {
	res, err := a.extractingAdapter.Materialize(ctx, req)
	if err != nil {
		return nil, err
	}
	a.registered = true
	return res, nil
}

func (a *registrationTrackingAdapter) Dematerialize(ctx context.Context, req plugins.DematerializeRequest) error {
	a.registered = false
	return a.extractingAdapter.Dematerialize(ctx, req)
}

func (a *registrationTrackingAdapter) EnsureRegistered(context.Context, plugins.DematerializeRequest) error {
	a.registered = true
	return nil
}

func (a *registrationTrackingAdapter) Health(ctx context.Context, req plugins.DematerializeRequest) error {
	if !a.registered {
		return errors.New("plugin is not registered")
	}
	return a.extractingAdapter.Health(ctx, req)
}

// A forced install over a pre-existing unmanaged tree that was NOT registered
// must not leave the plugin registered after rollback: restore reproduces the
// exact snapshot state (files present, registration absent).
//
//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestInstallFresh_RollbackDoesNotRegisterUnmanagedTree(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)
	cm := client.NewTestClientManagerWithHome(t.TempDir())
	inner := svc.(*service) //nolint:forcetypeassert
	inner.clientManager = cm
	tracking := &registrationTrackingAdapter{
		extractingAdapter: extractingAdapter{
			base:      filepath.Join(projectRoot, ".claude", "plugins"),
			installer: skills.NewInstaller(),
		},
	}
	inner.materializers["claude-code"] = tracking

	pluginDir, err := cm.GetPluginPath(client.ClaudeCode, "my-plugin", plugins.ScopeProject, projectRoot)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(pluginDir, "commands"), 0o750))
	prior := []byte("# prior unregistered unmanaged")
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "commands", "hello.md"), prior, 0o644))
	require.False(t, tracking.registered, "precondition: the unmanaged tree is not registered")

	// Make lock writes fail after extraction/DB create.
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, lockfile.FileName), 0o755))

	_, err = svc.Install(t.Context(), plugins.InstallOptions{
		Name:          "my-plugin",
		LayerData:     makePluginLayerDataWithBody(t, "my-plugin", "# installed"),
		AllowUnsigned: true,
		Digest:        validLockDigest(),
		Scope:         plugins.ScopeProject,
		ProjectRoot:   projectRoot,
		Clients:       []string{"claude-code"},
		Force:         true,
	})
	require.Error(t, err)

	got, readErr := os.ReadFile(filepath.Join(pluginDir, "commands", "hello.md")) //nolint:gosec
	require.NoError(t, readErr)
	assert.Equal(t, string(prior), string(got),
		"a failed Force install must restore the pre-existing unmanaged tree")
	assert.False(t, tracking.registered,
		"rollback must not register a tree that was unregistered at snapshot time")
}

// TestInstall_UnreadableLockFileAbortsBeforeMutating covers an unreadable
// lock file on a project-scope install. Install-time verification reads the
// entry's trust state before anything is extracted, so an unloadable lock
// file now fails there — earlier than installAndRegister's own snapshot,
// which keeps its Load guard only against a rewrite racing that window.
// Failing closed is the point: no DB record, no files, no lock entry.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestInstall_UnreadableLockFileAbortsBeforeMutating(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)

	// A lock path that is a directory makes Load fail.
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, lockfile.FileName), 0o755))

	_, err := svc.Install(t.Context(), plugins.InstallOptions{
		Name:          "my-plugin",
		LayerData:     makePluginLayerData(t, "my-plugin"),
		AllowUnsigned: true,
		Digest:        validLockDigest(),
		Scope:         plugins.ScopeProject,
		ProjectRoot:   projectRoot,
		Clients:       []string{"claude-code"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading lock trust state")

	_, statErr := os.Stat(filepath.Join(projectRoot, ".claude", "plugins", "my-plugin"))
	assert.True(t, os.IsNotExist(statErr), "nothing may be extracted before the lock file can be read")

	info, infoErr := svc.Info(t.Context(), plugins.InfoOptions{
		Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	})
	require.ErrorIs(t, infoErr, storage.ErrNotFound)
	assert.Nil(t, info)
}

//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestUninstall_ManagedMissingMaterializerAborts(t *testing.T) {
	svc, projectRoot := newLockTestService(t, true)
	installTestPlugin(t, svc, projectRoot, validLockDigest())

	inner := svc.(*service) //nolint:forcetypeassert
	delete(inner.materializers, "claude-code")

	err := svc.Uninstall(t.Context(), plugins.UninstallOptions{
		Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no materializer configured")

	_, ok := readLockfile(t, projectRoot).GetPlugin("my-plugin")
	assert.True(t, ok, "the lock pin must remain when uninstall is refused")
	info, err := svc.Info(t.Context(), plugins.InfoOptions{
		Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	})
	require.NoError(t, err)
	require.NotNil(t, info.InstalledPlugin)
}

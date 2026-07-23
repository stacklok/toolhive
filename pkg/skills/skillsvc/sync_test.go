// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	skillsmocks "github.com/stacklok/toolhive/pkg/skills/mocks"
	"github.com/stacklok/toolhive/pkg/storage/sqlite"
)

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestSync_FeatureDisabledReturnsForbidden(t *testing.T) {
	gr, _ := newGitResolverMock(t)
	svc, projectRoot := newLockTestService(t, gr)
	t.Setenv(skills.LockFileEnvVar, "false")

	_, err := svc.(*service).Sync(t.Context(), skills.SyncOptions{ProjectRoot: projectRoot}) //nolint:forcetypeassert
	require.Error(t, err)
	assert.Equal(t, http.StatusForbidden, httperr.Code(err))
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestSync_ReportsUpToDateWhenNothingChanged(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	fx.register("my-skill", gitSkill("my-skill"))
	svc, projectRoot := newLockTestService(t, gr)

	ref, _ := gitRef("my-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	syncer := svc.(*service) //nolint:forcetypeassert
	result, err := syncer.Sync(t.Context(), skills.SyncOptions{ProjectRoot: projectRoot})
	require.NoError(t, err)
	assert.Equal(t, []string{"my-skill"}, result.AlreadyCurrent)
	assert.Empty(t, result.Installed)
	assert.Empty(t, result.Drifted)
	assert.Empty(t, result.Failed)
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestSync_ReinstallsDriftedContent(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	fx.register("my-skill", gitSkill("my-skill"))
	svc, projectRoot := newLockTestService(t, gr)

	ref, _ := gitRef("my-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	// Tamper with the installed file.
	skillMD := filepath.Join(projectRoot, ".claude", "skills", "my-skill", "SKILL.md")
	require.NoError(t, os.WriteFile(skillMD, []byte("tampered content"), 0o644))

	syncer := svc.(*service) //nolint:forcetypeassert
	result, err := syncer.Sync(t.Context(), skills.SyncOptions{ProjectRoot: projectRoot})
	require.NoError(t, err)
	assert.Equal(t, []string{"my-skill"}, result.Drifted)
	assert.Equal(t, []string{"my-skill"}, result.Installed)

	restored, err := os.ReadFile(skillMD) //nolint:gosec // fixed test path
	require.NoError(t, err)
	assert.Contains(t, string(restored), "name: my-skill")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestSync_ReinstallPreservesResolvedReference(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	fx.register("my-skill", gitSkill("my-skill"))
	svc, projectRoot := newLockTestService(t, gr)

	ref, _ := gitRef("my-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	before, ok := readLockfile(t, projectRoot).Get("my-skill")
	require.True(t, ok)
	require.Equal(t, ref, before.ResolvedReference)

	// Tamper with the installed file so sync has to reinstall at the pinned
	// reference — the drift-repair path that must not overwrite
	// ResolvedReference with that internal pinned form.
	skillMD := filepath.Join(projectRoot, ".claude", "skills", "my-skill", "SKILL.md")
	require.NoError(t, os.WriteFile(skillMD, []byte("tampered content"), 0o644))

	syncer := svc.(*service) //nolint:forcetypeassert
	result, err := syncer.Sync(t.Context(), skills.SyncOptions{ProjectRoot: projectRoot})
	require.NoError(t, err)
	require.Equal(t, []string{"my-skill"}, result.Installed)

	after, ok := readLockfile(t, projectRoot).Get("my-skill")
	require.True(t, ok)
	assert.Equal(t, before.ResolvedReference, after.ResolvedReference,
		"a drift-repair reinstall must preserve ResolvedReference, not overwrite it with the pinned restore form")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestSync_CheckReportsDriftWithoutWriting(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	fx.register("my-skill", gitSkill("my-skill"))
	svc, projectRoot := newLockTestService(t, gr)

	ref, _ := gitRef("my-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	skillMD := filepath.Join(projectRoot, ".claude", "skills", "my-skill", "SKILL.md")
	require.NoError(t, os.WriteFile(skillMD, []byte("tampered content"), 0o644))

	syncer := svc.(*service) //nolint:forcetypeassert
	result, err := syncer.Sync(t.Context(), skills.SyncOptions{ProjectRoot: projectRoot, Check: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"my-skill"}, result.Drifted)
	assert.Empty(t, result.Installed, "check must not install/write anything")

	stillTampered, err := os.ReadFile(skillMD) //nolint:gosec // fixed test path
	require.NoError(t, err)
	assert.Equal(t, "tampered content", string(stillTampered))
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestSync_MissingInstallIsRestored(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	fx.register("my-skill", gitSkill("my-skill"))
	svc, projectRoot := newLockTestService(t, gr)

	ref, _ := gitRef("my-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	require.NoError(t, os.RemoveAll(filepath.Join(projectRoot, ".claude", "skills", "my-skill")))
	require.NoError(t, svc.(*service).store.Delete(t.Context(), "my-skill", skills.ScopeProject, projectRoot)) //nolint:forcetypeassert

	syncer := svc.(*service) //nolint:forcetypeassert
	result, err := syncer.Sync(t.Context(), skills.SyncOptions{ProjectRoot: projectRoot})
	require.NoError(t, err)
	assert.Equal(t, []string{"my-skill"}, result.Missing, "an entry with no install record is Missing")
	assert.Equal(t, []string{"my-skill"}, result.Installed)
	assert.Empty(t, result.Drifted, "a missing (not previously-DB-tracked) install is Missing, not Drifted")

	_, err = svc.Info(t.Context(), skills.InfoOptions{Name: "my-skill", Scope: skills.ScopeProject, ProjectRoot: projectRoot})
	require.NoError(t, err)
}

// TestSync_CheckReportsMissingInstalls guards the fresh-clone CI gate: a lock
// entry with no install record at all (the state of every entry on a fresh
// checkout or CI runner, where SQLite state is per-machine) must be reported
// by --check, not silently skipped. Before the fix, such entries landed in
// no result bucket and the gate exited 0 with nothing installed.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestSync_CheckReportsMissingInstalls(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	fx.register("my-skill", gitSkill("my-skill"))
	svc, projectRoot := newLockTestService(t, gr)

	ref, _ := gitRef("my-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	// Simulate the fresh clone: lock file committed, no local install state.
	require.NoError(t, os.RemoveAll(filepath.Join(projectRoot, ".claude", "skills", "my-skill")))
	require.NoError(t, svc.(*service).store.Delete(t.Context(), "my-skill", skills.ScopeProject, projectRoot)) //nolint:forcetypeassert

	syncer := svc.(*service) //nolint:forcetypeassert
	result, err := syncer.Sync(t.Context(), skills.SyncOptions{ProjectRoot: projectRoot, Check: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"my-skill"}, result.Missing, "--check must report the missing install")
	assert.Empty(t, result.Installed, "check must not install anything")

	_, err = svc.Info(t.Context(), skills.InfoOptions{Name: "my-skill", Scope: skills.ScopeProject, ProjectRoot: projectRoot})
	require.Error(t, err, "check must not have installed anything")
}

// TestSync_RestoreDoesNotReResolveDependencies covers the deterministic-
// restore guarantee: restoring a parent whose SKILL.md declares
// toolhive.requires must NOT re-materialize those dependencies from their
// mutable source strings. Each dependency has its own pinned lock entry the
// sync loop restores directly; re-resolving would silently upgrade and
// re-pin a dependency whose upstream tag moved.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestSync_RestoreDoesNotReResolveDependencies(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	// The dependency name sorts BEFORE the parent's: the sync loop restores
	// it first (correctly, at its pin), and the parent's restore afterwards
	// is what would re-resolve and re-pin it if requires recursion ran —
	// with nothing after it in the loop to repair the damage.
	depRef, _ := gitRef("a-pinned-dep")
	fx.register("a-pinned-dep", gitSkill("a-pinned-dep"))
	fx.register("z-parent-skill", gitSkill("z-parent-skill", depRef))
	svc, projectRoot := newLockTestService(t, gr)

	parentRef, _ := gitRef("z-parent-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: parentRef, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	depBefore, ok := readLockfile(t, projectRoot).Get("a-pinned-dep")
	require.True(t, ok)

	// Move the dependency's upstream: same source string, newer content.
	fx.register("a-pinned-dep", gitSkillVersion("a-pinned-dep"))

	// Wipe local state so sync must restore both entries from the lock file.
	svcImpl := svc.(*service) //nolint:forcetypeassert
	require.NoError(t, os.RemoveAll(filepath.Join(projectRoot, ".claude", "skills")))
	require.NoError(t, svcImpl.store.Delete(t.Context(), "z-parent-skill", skills.ScopeProject, projectRoot))
	require.NoError(t, svcImpl.store.Delete(t.Context(), "a-pinned-dep", skills.ScopeProject, projectRoot))

	result, err := svcImpl.Sync(t.Context(), skills.SyncOptions{ProjectRoot: projectRoot})
	require.NoError(t, err)
	assert.Empty(t, result.Failed)

	depAfter, ok := readLockfile(t, projectRoot).Get("a-pinned-dep")
	require.True(t, ok)
	assert.Equal(t, depBefore.Digest, depAfter.Digest,
		"restore must reinstall the dependency at its pinned digest, not re-resolve the moved source")
	assert.Equal(t, depBefore.ResolvedReference, depAfter.ResolvedReference)
}

// TestSync_RestorePreservesExplicitFlag: a restore is not a user install,
// so it must not promote a non-explicit dependency's lock entry to
// Explicit: true — that would permanently exempt it from cascade removal
// when its last requiring parent is uninstalled.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestSync_RestorePreservesExplicitFlag(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	depRef, _ := gitRef("dep-skill")
	fx.register("dep-skill", gitSkill("dep-skill"))
	fx.register("parent-skill", gitSkill("parent-skill", depRef))
	svc, projectRoot := newLockTestService(t, gr)

	parentRef, _ := gitRef("parent-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: parentRef, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	before, ok := readLockfile(t, projectRoot).Get("dep-skill")
	require.True(t, ok)
	require.False(t, before.Explicit, "a materialized dependency starts non-explicit")

	// Tamper with the dependency so sync restores it.
	depMD := filepath.Join(projectRoot, ".claude", "skills", "dep-skill", "SKILL.md")
	require.NoError(t, os.WriteFile(depMD, []byte("tampered"), 0o644))

	syncer := svc.(*service) //nolint:forcetypeassert
	result, err := syncer.Sync(t.Context(), skills.SyncOptions{ProjectRoot: projectRoot})
	require.NoError(t, err)
	assert.Contains(t, result.Installed, "dep-skill")

	after, ok := readLockfile(t, projectRoot).Get("dep-skill")
	require.True(t, ok)
	assert.False(t, after.Explicit, "a restore must not promote a dependency to explicit")
	assert.Equal(t, before.RequiredBy, after.RequiredBy)
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestSync_AdoptsUnmanagedInstall(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	fx.register("unmanaged-skill", gitSkill("unmanaged-skill"))
	svc, projectRoot := newLockTestService(t, gr)

	// Disable the feature for the initial install so it lands unmanaged
	// (no lock entry, Managed=false) — simulating a pre-existing install
	// from before the lock feature was ever enabled.
	t.Setenv(skills.LockFileEnvVar, "false")
	ref, _ := gitRef("unmanaged-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)
	t.Setenv(skills.LockFileEnvVar, "true")

	syncer := svc.(*service) //nolint:forcetypeassert
	result, err := syncer.Sync(t.Context(), skills.SyncOptions{ProjectRoot: projectRoot})
	require.NoError(t, err)
	assert.Equal(t, []string{"unmanaged-skill"}, result.NeverManaged)

	result, err = syncer.Sync(t.Context(), skills.SyncOptions{ProjectRoot: projectRoot, Adopt: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"unmanaged-skill"}, result.NeverManaged)

	lf := readLockfile(t, projectRoot)
	entry, ok := lf.Get("unmanaged-skill")
	require.True(t, ok, "adopt must write a lock entry for the unmanaged install")
	assert.NotEmpty(t, entry.ContentDigest)

	sk, err := svc.Info(t.Context(), skills.InfoOptions{Name: "unmanaged-skill", Scope: skills.ScopeProject, ProjectRoot: projectRoot})
	require.NoError(t, err)
	require.NotNil(t, sk.InstalledSkill)
	assert.True(t, sk.InstalledSkill.Managed, "adopt must mark the DB record as managed")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestSync_PrunesRemovedFromLock(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	fx.register("my-skill", gitSkill("my-skill"))
	svc, projectRoot := newLockTestService(t, gr)

	ref, _ := gitRef("my-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	// Simulate the entry being deleted from the lock file by hand (or by a
	// teammate's commit) while the DB record and files remain.
	require.NoError(t, lockfile.RemoveEntry(mustOpenRoot(t, projectRoot), "my-skill"))

	syncer := svc.(*service) //nolint:forcetypeassert
	result, err := syncer.Sync(t.Context(), skills.SyncOptions{ProjectRoot: projectRoot})
	require.NoError(t, err)
	assert.Equal(t, []string{"my-skill"}, result.RemovedFromLock)

	result, err = syncer.Sync(t.Context(), skills.SyncOptions{ProjectRoot: projectRoot, Prune: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"my-skill"}, result.Pruned)

	_, err = svc.Info(t.Context(), skills.InfoOptions{Name: "my-skill", Scope: skills.ScopeProject, ProjectRoot: projectRoot})
	require.Error(t, err, "prune must uninstall the skill")
}

// TestSync_CheckDetectsTamperInAnyClientDir: verification must cover every
// client directory a skill is installed into. Checking only the first
// client's copy would leave tampering with any other client's materialized
// files invisible to --check — and which directory got checked would depend
// on install order.
//
//nolint:paralleltest // uses t.Setenv, incompatible with t.Parallel
func TestSync_CheckDetectsTamperInAnyClientDir(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	fx.register("multi-skill", gitSkill("multi-skill"))

	t.Setenv(skills.LockFileEnvVar, "true")
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sqlite.Open(t.Context(), dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store := sqlite.NewSkillStore(db)

	projectRoot := makeProjectRoot(t)
	// Unlike newLockTestService's resolver, this one gives every client its
	// own directory so the per-client verification actually has two copies
	// to distinguish.
	ctrl := gomock.NewController(t)
	pr := skillsmocks.NewMockPathResolver(ctrl)
	pr.EXPECT().GetSkillPath(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().
		DoAndReturn(func(client, skillName string, _ skills.Scope, _ string) (string, error) {
			return filepath.Join(projectRoot, "."+client, "skills", skillName), nil
		})
	pr.EXPECT().ListSkillSupportingClients().AnyTimes().Return([]string{"claude-code", "cursor"})
	svc := New(store, WithPathResolver(pr), WithGitResolver(gr))

	ref, _ := gitRef("multi-skill")
	_, err = svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot,
		Clients: []string{"claude-code", "cursor"},
	})
	require.NoError(t, err)

	syncer := svc.(*service) //nolint:forcetypeassert
	result, err := syncer.Sync(t.Context(), skills.SyncOptions{ProjectRoot: projectRoot, Check: true})
	require.NoError(t, err)
	require.Equal(t, []string{"multi-skill"}, result.AlreadyCurrent, "untampered install must verify clean")

	// Tamper with the SECOND client's copy only.
	cursorMD := filepath.Join(projectRoot, ".cursor", "skills", "multi-skill", "SKILL.md")
	require.NoError(t, os.WriteFile(cursorMD, []byte("tampered"), 0o644))

	result, err = syncer.Sync(t.Context(), skills.SyncOptions{ProjectRoot: projectRoot, Check: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"multi-skill"}, result.Drifted,
		"tampering with a non-first client's copy must be reported as drift")
}

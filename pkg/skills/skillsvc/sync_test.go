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

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
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
	assert.Equal(t, []string{"my-skill"}, result.Installed)
	assert.Empty(t, result.Drifted, "a missing (not previously-DB-tracked) install is Installed, not Drifted")

	_, err = svc.Info(t.Context(), skills.InfoOptions{Name: "my-skill", Scope: skills.ScopeProject, ProjectRoot: projectRoot})
	require.NoError(t, err)
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

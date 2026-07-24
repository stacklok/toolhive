// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/skills"
)

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUninstall_CascadesToOrphanedDependency(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	depRef, _ := gitRef("shared-dep")
	fx.register("parent-skill", gitSkill("parent-skill", depRef))
	fx.register("shared-dep", gitSkill("shared-dep"))
	svc, projectRoot := newLockTestService(t, gr)

	parentRef, _ := gitRef("parent-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: parentRef, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)
	_, ok := readLockfile(t, projectRoot).Get("shared-dep")
	require.True(t, ok, "expected shared-dep to be materialized before uninstalling parent-skill")

	require.NoError(t, svc.Uninstall(t.Context(), skills.UninstallOptions{
		Name: "parent-skill", Scope: skills.ScopeProject, ProjectRoot: projectRoot,
	}))

	lf := readLockfile(t, projectRoot)
	_, ok = lf.Get("parent-skill")
	assert.False(t, ok, "parent's own lock entry must be removed")
	_, ok = lf.Get("shared-dep")
	assert.False(t, ok, "an orphaned, non-explicit dependency must cascade-uninstall")

	_, err = svc.Info(t.Context(), skills.InfoOptions{Name: "shared-dep", Scope: skills.ScopeProject, ProjectRoot: projectRoot})
	require.Error(t, err, "cascade must remove the dependency's DB record and files too")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUninstall_SharedDependencySurvivesOneParentRemoval(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	depRef, _ := gitRef("shared-dep")
	fx.register("parent-a", gitSkill("parent-a", depRef))
	fx.register("parent-b", gitSkill("parent-b", depRef))
	fx.register("shared-dep", gitSkill("shared-dep"))
	svc, projectRoot := newLockTestService(t, gr)

	refA, _ := gitRef("parent-a")
	refB, _ := gitRef("parent-b")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: refA, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)
	_, err = svc.Install(t.Context(), skills.InstallOptions{
		Name: refB, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	require.NoError(t, svc.Uninstall(t.Context(), skills.UninstallOptions{
		Name: "parent-a", Scope: skills.ScopeProject, ProjectRoot: projectRoot,
	}))

	lf := readLockfile(t, projectRoot)
	dep, ok := lf.Get("shared-dep")
	require.True(t, ok, "dependency must survive while parent-b still requires it")
	assert.Equal(t, []string{"parent-b"}, dep.RequiredBy)

	_, err = svc.Info(t.Context(), skills.InfoOptions{Name: "shared-dep", Scope: skills.ScopeProject, ProjectRoot: projectRoot})
	require.NoError(t, err, "the dependency's DB record and files must remain")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUninstall_ExplicitDependencyIsNeverCascaded(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	depRef, _ := gitRef("also-explicit")
	fx.register("parent-skill", gitSkill("parent-skill", depRef))
	fx.register("also-explicit", gitSkill("also-explicit"))
	svc, projectRoot := newLockTestService(t, gr)

	// Install the dependency explicitly first, then install a parent that
	// also requires it.
	explicitRef, _ := gitRef("also-explicit")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: explicitRef, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)
	parentRef, _ := gitRef("parent-skill")
	_, err = svc.Install(t.Context(), skills.InstallOptions{
		Name: parentRef, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	lf := readLockfile(t, projectRoot)
	dep, ok := lf.Get("also-explicit")
	require.True(t, ok)
	assert.True(t, dep.Explicit, "an entry installed explicitly stays explicit even once also required by a parent")
	assert.Equal(t, []string{"parent-skill"}, dep.RequiredBy)

	require.NoError(t, svc.Uninstall(t.Context(), skills.UninstallOptions{
		Name: "parent-skill", Scope: skills.ScopeProject, ProjectRoot: projectRoot,
	}))

	lf = readLockfile(t, projectRoot)
	dep, ok = lf.Get("also-explicit")
	require.True(t, ok, "an explicit entry must never be cascade-removed")
	assert.Empty(t, dep.RequiredBy)

	_, err = svc.Info(t.Context(), skills.InfoOptions{Name: "also-explicit", Scope: skills.ScopeProject, ProjectRoot: projectRoot})
	require.NoError(t, err)
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUninstall_CascadeCycleTerminates(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	refA, _ := gitRef("skill-a")
	refB, _ := gitRef("skill-b")
	fx.register("skill-a", gitSkill("skill-a", refB))
	fx.register("skill-b", gitSkill("skill-b", refA))
	svc, projectRoot := newLockTestService(t, gr)

	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: refA, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		done <- svc.Uninstall(t.Context(), skills.UninstallOptions{
			Name: "skill-a", Scope: skills.ScopeProject, ProjectRoot: projectRoot,
		})
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("timeout: cascade uninstall did not terminate on a requiredBy cycle")
	}

	lf := readLockfile(t, projectRoot)
	_, ok := lf.Get("skill-a")
	assert.False(t, ok)
	_, ok = lf.Get("skill-b")
	assert.False(t, ok, "skill-b lost its only (cyclic) parent and must cascade too")
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/skills"
)

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_FeatureDisabledReturnsForbidden(t *testing.T) {
	gr, _ := newGitResolverMock(t)
	svc, projectRoot := newLockTestService(t, gr)
	t.Setenv(skills.LockFileEnvVar, "false")

	_, err := svc.(*service).Upgrade(t.Context(), skills.UpgradeOptions{ProjectRoot: projectRoot}) //nolint:forcetypeassert
	require.Error(t, err)
	assert.Equal(t, http.StatusForbidden, httperr.Code(err))
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_ReportsUpToDateWhenSourceUnchanged(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	fx.register("my-skill", gitSkill("my-skill"))
	svc, projectRoot := newLockTestService(t, gr)

	ref, _ := gitRef("my-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	result, err := svc.(*service).Upgrade(t.Context(), skills.UpgradeOptions{ProjectRoot: projectRoot}) //nolint:forcetypeassert
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, skills.UpgradeStatusUpToDate, result.Outcomes[0].Status)
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_InstallsNewerContent(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	fx.register("my-skill", gitSkill("my-skill"))
	svc, projectRoot := newLockTestService(t, gr)

	ref, _ := gitRef("my-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	before := readLockfile(t, projectRoot)
	beforeEntry, _ := before.Get("my-skill")

	// Republish newer content at the same fixture URL — same source, new commit.
	fx.register("my-skill", gitSkillVersion("my-skill", "2.0.0"))

	result, err := svc.(*service).Upgrade(t.Context(), skills.UpgradeOptions{ProjectRoot: projectRoot}) //nolint:forcetypeassert
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	outcome := result.Outcomes[0]
	assert.Equal(t, skills.UpgradeStatusUpgraded, outcome.Status)
	assert.Equal(t, beforeEntry.Digest, outcome.OldDigest)
	assert.NotEqual(t, outcome.OldDigest, outcome.NewDigest)

	after := readLockfile(t, projectRoot)
	afterEntry, ok := after.Get("my-skill")
	require.True(t, ok)
	assert.Equal(t, outcome.NewDigest, afterEntry.Digest)
	assert.Equal(t, beforeEntry.Source, afterEntry.Source, "Source must never be rewritten by upgrade")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_PreviewDoesNotInstall(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	fx.register("my-skill", gitSkill("my-skill"))
	svc, projectRoot := newLockTestService(t, gr)

	ref, _ := gitRef("my-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	before := readLockfile(t, projectRoot)
	beforeEntry, _ := before.Get("my-skill")

	fx.register("my-skill", gitSkillVersion("my-skill", "2.0.0"))

	result, err := svc.(*service).Upgrade(t.Context(), skills.UpgradeOptions{ //nolint:forcetypeassert
		ProjectRoot: projectRoot, Preview: true,
	})
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, skills.UpgradeStatusUpgraded, result.Outcomes[0].Status, "preview still reports what would happen")

	after := readLockfile(t, projectRoot)
	afterEntry, ok := after.Get("my-skill")
	require.True(t, ok)
	assert.Equal(t, beforeEntry.Digest, afterEntry.Digest, "preview must not rewrite the lock file")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_NotUpgradableForImmutableSource(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	fx.register("my-skill", gitSkill("my-skill"))
	svc, projectRoot := newLockTestService(t, gr)

	ref, _ := gitRef("my-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	// Hand-edit the lock entry to pin an immutable (full commit hash) source,
	// as if the user had originally installed with an explicit @<sha>.
	lf := readLockfile(t, projectRoot)
	entry, ok := lf.Get("my-skill")
	require.True(t, ok)
	entry.Source = ref + "@" + entry.Digest
	lf.Upsert(entry)
	require.NoError(t, lf.Save(mustOpenRoot(t, projectRoot)))

	result, err := svc.(*service).Upgrade(t.Context(), skills.UpgradeOptions{ProjectRoot: projectRoot}) //nolint:forcetypeassert
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, skills.UpgradeStatusNotUpgradable, result.Outcomes[0].Status)
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_UnknownNameReturnsNotFound(t *testing.T) {
	gr, _ := newGitResolverMock(t)
	svc, projectRoot := newLockTestService(t, gr)

	_, err := svc.(*service).Upgrade(t.Context(), skills.UpgradeOptions{ //nolint:forcetypeassert
		ProjectRoot: projectRoot, Names: []string{"does-not-exist"},
	})
	require.Error(t, err)
	assert.Equal(t, http.StatusNotFound, httperr.Code(err))
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_FailOnChangesStopsAtFirstChange(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	fx.register("my-skill", gitSkill("my-skill"))
	svc, projectRoot := newLockTestService(t, gr)

	ref, _ := gitRef("my-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	fx.register("my-skill", gitSkillVersion("my-skill", "2.0.0"))

	_, err = svc.(*service).Upgrade(t.Context(), skills.UpgradeOptions{ //nolint:forcetypeassert
		ProjectRoot: projectRoot, Preview: true, FailOnChanges: true,
	})
	require.Error(t, err)
	assert.Equal(t, http.StatusConflict, httperr.Code(err))
}

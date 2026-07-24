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
	fx.register("my-skill", gitSkillVersion("my-skill"))

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

	fx.register("my-skill", gitSkillVersion("my-skill"))

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

// TestUpgrade_PreservesExistingClients guards against Upgrade silently
// expanding a skill to every detected client. Install without an explicit
// InstallOptions.Clients falls back to whatever the caller passed (here,
// a single client); Upgrade must default to that skill's already-installed
// Clients when opts.Clients is empty, matching Sync's reinstallPinned — not
// fall through to Install's own "no clients means every detected client"
// default.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_PreservesExistingClients(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	fx.register("my-skill", gitSkill("my-skill"))
	svc, projectRoot := newLockTestService(t, gr)

	ref, _ := gitRef("my-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	fx.register("my-skill", gitSkillVersion("my-skill"))

	result, err := svc.(*service).Upgrade(t.Context(), skills.UpgradeOptions{ProjectRoot: projectRoot}) //nolint:forcetypeassert
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, skills.UpgradeStatusUpgraded, result.Outcomes[0].Status)

	info, err := svc.Info(t.Context(), skills.InfoOptions{Name: "my-skill", Scope: skills.ScopeProject, ProjectRoot: projectRoot})
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-code"}, info.InstalledSkill.Clients,
		"upgrade must preserve the skill's existing clients, not expand to every detected client")
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

// TestUpgrade_FailOnChangesReportsOutcomesWithoutError: the gate returns
// the full outcome set with a 200 rather than an error — callers (and the
// CLI's exit-code mapping) derive the freshness verdict from the outcomes,
// which also distinguishes "would change" from a genuine resolution
// failure and names the stale skills.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_FailOnChangesReportsOutcomesWithoutError(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	fx.register("my-skill", gitSkill("my-skill"))
	svc, projectRoot := newLockTestService(t, gr)

	ref, _ := gitRef("my-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	fx.register("my-skill", gitSkillVersion("my-skill"))

	result, err := svc.(*service).Upgrade(t.Context(), skills.UpgradeOptions{ //nolint:forcetypeassert
		ProjectRoot: projectRoot, FailOnChanges: true,
	})
	require.NoError(t, err, "fail-on-changes reports outcomes, it does not error")
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, skills.UpgradeStatusUpgraded, result.Outcomes[0].Status,
		"the outcome reports what would change so callers can name the stale skill")
}

// TestUpgrade_FailOnChangesWithoutPreviewDoesNotMutateAnyEntry covers the
// core fix: --fail-on-changes must behave as a read-only CI gate even
// without --preview. Before the fix, Upgrade installed each target in
// sequence and only checked FailOnChanges after each one, so a multi-entry
// lock file with one changed skill would install that skill for real before
// reporting the conflict — a partial mutation the gate is supposed to
// prevent entirely.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_FailOnChangesWithoutPreviewDoesNotMutateAnyEntry(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	fx.register("changed-skill", gitSkill("changed-skill"))
	fx.register("stable-skill", gitSkill("stable-skill"))
	svc, projectRoot := newLockTestService(t, gr)

	changedRef, _ := gitRef("changed-skill")
	stableRef, _ := gitRef("stable-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: changedRef, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)
	_, err = svc.Install(t.Context(), skills.InstallOptions{
		Name: stableRef, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	before := readLockfile(t, projectRoot)
	changedBefore, _ := before.Get("changed-skill")
	stableBefore, _ := before.Get("stable-skill")

	// Republish newer content for only one of the two skills.
	fx.register("changed-skill", gitSkillVersion("changed-skill"))

	result, err := svc.(*service).Upgrade(t.Context(), skills.UpgradeOptions{ //nolint:forcetypeassert
		ProjectRoot: projectRoot, FailOnChanges: true,
	})
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 2)

	after := readLockfile(t, projectRoot)
	changedAfter, ok := after.Get("changed-skill")
	require.True(t, ok)
	assert.Equal(t, changedBefore.Digest, changedAfter.Digest,
		"the changed skill must not be installed for real before --fail-on-changes reports the conflict")
	stableAfter, ok := after.Get("stable-skill")
	require.True(t, ok)
	assert.Equal(t, stableBefore.Digest, stableAfter.Digest)
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	"github.com/stacklok/toolhive/pkg/skills/verifier"
	verifiermocks "github.com/stacklok/toolhive/pkg/skills/verifier/mocks"
)

// otherSignerResult is a verification result from a different identity than
// signedResult's.
func otherSignerResult() *verifier.Result {
	r := signedResult()
	r.SignerIdentity = "/.github/workflows/other.yml"
	return r
}

// signerChangeFixture installs a signed skill, then republishes newer
// content at the same source and returns a service whose verifier reports
// the candidate as signed by candidate() (or unsigned when candidate
// returns nil, err).
func signerChangeFixture(
	t *testing.T,
	candidate func() (*verifier.Result, error),
) (skills.SkillService, string) {
	t.Helper()
	gr, fx := newGitResolverMock(t)
	fx.register("guarded-skill", gitSkill("guarded-skill"))

	installs := 0
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().
		DoAndReturn(func(_ any, _, _ []byte, expected *verifier.ProvenanceExpectation) (*verifier.Result, error) {
			installs++
			if installs == 1 {
				return signedResult(), nil // initial install (TOFU)
			}
			result, err := candidate()
			if err != nil {
				return nil, err
			}
			if expected != nil && result.SignerIdentity != testSignerIdentity {
				return nil, verifier.ErrSignerMismatch
			}
			return result, nil
		})
	mv.EXPECT().VerifyBundleOffline(gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().Return(nil)

	svc, projectRoot := newLockTestService(t, gr, WithVerifier(mv))
	ref, _ := gitRef("guarded-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	// Republish newer content at the same source so an upgrade is planned.
	fx.register("guarded-skill", gitSkillVersion("guarded-skill"))
	return svc, projectRoot
}

// TestUpgrade_RefChangeRequiresAllowSignerChange covers the ref-pinning
// guard's current shape: ANY ref change — including a plausible-looking
// tag-to-tag release rotation — blocks the upgrade exactly like a genuine
// signer-identity change, and the existing --allow-signer-change override
// is what re-pins it.
//
// An earlier version of this guard let a recorded tag ref rotate to any
// other tag ref automatically, on the theory that a release workflow signs
// each version on its own tag. Panel review on stacklok/toolhive#6315 found
// that this let a candidate signed from an attacker's OWN tag on the same
// repository (e.g. "refs/tags/attacker-release") replace a pinned tag,
// since nothing tied the candidate's tag to the specific version actually
// being upgraded to — binding it correctly would need the resolved release
// source's own tag, which the git resolver never surfaces (only the
// resolved commit hash), so a fix scoped to OCI would have left git-sourced
// skills with the identical hole. The automatic allowance was removed
// rather than patched per-format.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_RefChangeRequiresAllowSignerChange(t *testing.T) {
	const (
		installedRef = "refs/tags/v0.1.0"
		releaseRef   = "refs/tags/v0.2.0"
	)
	gr, fx := newGitResolverMock(t)
	fx.register("repin-skill", gitSkill("repin-skill"))

	calls := 0
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().
		DoAndReturn(func(_ any, _, _ []byte, expected *verifier.ProvenanceExpectation) (*verifier.Result, error) {
			calls++
			if calls == 1 {
				return refSignedResult(installedRef), nil // initial install (TOFU)
			}
			candidate := refSignedResult(releaseRef)
			if expected == nil {
				return candidate, nil // the upgrade's plan-time signer probe
			}
			return nil, verifier.ErrSignerMismatch
		})
	mv.EXPECT().VerifyBundleOffline(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().Return(nil)

	svc, projectRoot := newLockTestService(t, gr, WithVerifier(mv))
	ref, _ := gitRef("repin-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)
	entry, ok := readLockfile(t, projectRoot).Get("repin-skill")
	require.True(t, ok)
	require.NotNil(t, entry.Provenance)
	require.Equal(t, installedRef, entry.Provenance.RepositoryRef, "the install must pin the observed ref")

	fx.register("repin-skill", gitSkillVersion("repin-skill"))

	// Without the override, even a tag-shaped rotation is blocked.
	result, err := svc.(*service).Upgrade(t.Context(), skills.UpgradeOptions{ProjectRoot: projectRoot}) //nolint:forcetypeassert
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, skills.UpgradeStatusSignerChangeBlocked, result.Outcomes[0].Status,
		"a ref change has no automatic allowance, even a plausible release-tag rotation")

	entry, ok = readLockfile(t, projectRoot).Get("repin-skill")
	require.True(t, ok)
	require.NotNil(t, entry.Provenance)
	assert.Equal(t, installedRef, entry.Provenance.RepositoryRef, "a blocked upgrade must not touch the lock")

	// With the explicit override, it proceeds and re-pins the new ref —
	// the same mechanism a genuine signer-identity change already uses.
	result, err = svc.(*service).Upgrade(t.Context(), //nolint:forcetypeassert
		skills.UpgradeOptions{ProjectRoot: projectRoot, AllowSignerChange: true})
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, skills.UpgradeStatusUpgraded, result.Outcomes[0].Status)

	entry, ok = readLockfile(t, projectRoot).Get("repin-skill")
	require.True(t, ok)
	require.NotNil(t, entry.Provenance)
	assert.Equal(t, releaseRef, entry.Provenance.RepositoryRef,
		"the override must re-record the new ref, so the next install enforces it")
}

func TestRepositoryRefChanged(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		probe    string
		recorded string
		want     bool
	}{
		{name: "same ref", probe: "refs/tags/v0.1.0", recorded: "refs/tags/v0.1.0"},
		{name: "entry recorded none is unconstrained", probe: "refs/heads/attacker"},
		{name: "tag rotation blocked", probe: "refs/tags/v0.2.0", recorded: "refs/tags/v0.1.0", want: true},
		{name: "branch change blocked", probe: "refs/heads/attacker", recorded: "refs/heads/main", want: true},
		{name: "candidate carrying none blocked", recorded: "refs/tags/v0.1.0", want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, repositoryRefChanged(
				&verifier.Result{RepositoryRef: tc.probe},
				&lockfile.Provenance{RepositoryRef: tc.recorded}))
		})
	}
}

// TestUpgrade_RefTransitionBlocked is the regression test for the ref-pin
// guard: guardSignerChange must reject ANY ref change without an explicit
// --allow-signer-change, and critically, the transition must never reach
// applyUpgrade's install call at all, so the lock stays untouched. Before
// the original fix, every upgrade unconditionally cleared the expected ref
// with no prior check, so a candidate signed by the same identity, issuer,
// and runner from a different branch would pass and silently replace the
// locked ref — the exact substitution this PR's ref pinning exists to
// catch. See TestUpgrade_RefChangeRequiresAllowSignerChange for why even a
// plausible tag-to-tag rotation is included, not just an obviously
// suspicious branch change.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_RefTransitionBlocked(t *testing.T) {
	tests := []struct {
		name        string
		lockedRef   string
		candidate   string
		description string
	}{
		{
			name: "attacker branch", lockedRef: "refs/heads/main", candidate: "refs/heads/attacker",
			description: "same identity, issuer, and runner, signed from a different branch",
		},
		{
			name: "candidate lost its ref extension", lockedRef: "refs/tags/v0.1.0", candidate: "",
			description: "a certificate that stopped carrying a ref extension must not silently unpin one",
		},
		{
			name: "plausible tag rotation", lockedRef: "refs/tags/v0.1.0", candidate: "refs/tags/v0.2.0",
			description: "a tag-to-tag rotation has no automatic allowance either — see the test above for why",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gr, fx := newGitResolverMock(t)
			fx.register("ref-guarded-skill", gitSkill("ref-guarded-skill"))

			calls := 0
			mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
			mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
				AnyTimes().
				DoAndReturn(func(_ any, _, _ []byte, expected *verifier.ProvenanceExpectation) (*verifier.Result, error) {
					calls++
					if calls == 1 {
						return refSignedResult(tc.lockedRef), nil // initial install (TOFU)
					}
					candidate := refSignedResult(tc.candidate)
					if expected == nil {
						return candidate, nil // the upgrade's plan-time signer probe
					}
					// A blocked transition must never reach here: applyUpgrade
					// is only called when guardSignerChange did not block.
					t.Fatalf("install-time verification must not run for a blocked ref transition: %s", tc.description)
					return nil, nil
				})
			mv.EXPECT().VerifyBundleOffline(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().Return(nil)

			svc, projectRoot := newLockTestService(t, gr, WithVerifier(mv))
			ref, _ := gitRef("ref-guarded-skill")
			_, err := svc.Install(t.Context(), skills.InstallOptions{
				Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
			})
			require.NoError(t, err)

			fx.register("ref-guarded-skill", gitSkillVersion("ref-guarded-skill"))
			result, err := svc.(*service).Upgrade(t.Context(), skills.UpgradeOptions{ProjectRoot: projectRoot}) //nolint:forcetypeassert
			require.NoError(t, err)
			require.Len(t, result.Outcomes, 1)
			assert.Equal(t, skills.UpgradeStatusSignerChangeBlocked, result.Outcomes[0].Status, tc.description)

			entry, ok := readLockfile(t, projectRoot).Get("ref-guarded-skill")
			require.True(t, ok)
			require.NotNil(t, entry.Provenance)
			assert.Equal(t, tc.lockedRef, entry.Provenance.RepositoryRef,
				"a blocked transition must leave the locked ref untouched")
		})
	}
}

func TestRunnerEnvironmentChanged(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		probe    string
		recorded string
		want     bool
	}{
		{name: "same runner class", probe: testRunnerEnvironment, recorded: testRunnerEnvironment},
		{name: "entry recorded none is unconstrained", probe: "self-hosted"},
		{name: "runner class change blocked", probe: "self-hosted", recorded: testRunnerEnvironment, want: true},
		{name: "candidate carrying none blocked", recorded: testRunnerEnvironment, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, runnerEnvironmentChanged(
				&verifier.Result{RunnerEnvironment: tc.probe},
				&lockfile.Provenance{RunnerEnvironment: tc.recorded}))
		})
	}
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_SignerChangeBlocked(t *testing.T) {
	svc, projectRoot := signerChangeFixture(t, func() (*verifier.Result, error) {
		return otherSignerResult(), nil
	})

	result, err := svc.(*service).Upgrade(t.Context(), skills.UpgradeOptions{ProjectRoot: projectRoot}) //nolint:forcetypeassert
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, skills.UpgradeStatusSignerChangeBlocked, result.Outcomes[0].Status)
	assert.Equal(t, "/.github/workflows/other.yml", result.Outcomes[0].NewSignerIdentity)

	// Nothing installed, lock unchanged.
	lf := readLockfile(t, projectRoot)
	entry, ok := lf.Get("guarded-skill")
	require.True(t, ok)
	require.NotNil(t, entry.Provenance)
	assert.Equal(t, testSignerIdentity, entry.Provenance.SignerIdentity)
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_UnsignedCandidateBlockedAgainstSignedEntry(t *testing.T) {
	svc, projectRoot := signerChangeFixture(t, func() (*verifier.Result, error) {
		return nil, verifier.ErrUnsigned
	})

	result, err := svc.(*service).Upgrade(t.Context(), skills.UpgradeOptions{ProjectRoot: projectRoot}) //nolint:forcetypeassert
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, skills.UpgradeStatusSignerChangeBlocked, result.Outcomes[0].Status)
	assert.Empty(t, result.Outcomes[0].NewSignerIdentity, "an unsigned candidate has no identity to report")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_AllowSignerChangeRecordsNewIdentity(t *testing.T) {
	svc, projectRoot := signerChangeFixture(t, func() (*verifier.Result, error) {
		return otherSignerResult(), nil
	})

	result, err := svc.(*service).Upgrade(t.Context(), //nolint:forcetypeassert
		skills.UpgradeOptions{ProjectRoot: projectRoot, AllowSignerChange: true})
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, skills.UpgradeStatusUpgraded, result.Outcomes[0].Status)

	lf := readLockfile(t, projectRoot)
	entry, ok := lf.Get("guarded-skill")
	require.True(t, ok)
	require.NotNil(t, entry.Provenance)
	assert.Equal(t, "/.github/workflows/other.yml", entry.Provenance.SignerIdentity,
		"the explicit override must re-record the new identity")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_SignerChangePreviewParity(t *testing.T) {
	svc, projectRoot := signerChangeFixture(t, func() (*verifier.Result, error) {
		return otherSignerResult(), nil
	})

	result, err := svc.(*service).Upgrade(t.Context(), //nolint:forcetypeassert
		skills.UpgradeOptions{ProjectRoot: projectRoot, Preview: true})
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, skills.UpgradeStatusSignerChangeBlocked, result.Outcomes[0].Status,
		"preview must report the same signer-change block as apply")

	// Preview installed nothing and rewrote nothing.
	lf := readLockfile(t, projectRoot)
	entry, ok := lf.Get("guarded-skill")
	require.True(t, ok)
	assert.Equal(t, testSignerIdentity, entry.Provenance.SignerIdentity)
}

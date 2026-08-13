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
		DoAndReturn(func(_ any, _, _ []byte, expected *lockfile.Provenance) (*verifier.Result, error) {
			installs++
			if installs == 1 {
				return signedResult(), nil // initial install (TOFU)
			}
			result, err := candidate()
			if err != nil {
				return nil, err
			}
			if expected != nil && expected.SignerIdentity != result.SignerIdentity {
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

// TestUpgrade_RepinsRepositoryRef proves the release-workflow case: a new
// version signed on a new ref upgrades and the lock entry re-records the new
// ref, while every other pinned field — the runner class included — is still
// enforced at install-time verification.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_RepinsRepositoryRef(t *testing.T) {
	const (
		installedRef = "refs/tags/v0.1.0"
		releaseRef   = "refs/tags/v0.2.0"
	)
	gr, fx := newGitResolverMock(t)
	fx.register("repin-skill", gitSkill("repin-skill"))

	// installExpected captures the provenance the upgrade's install-time
	// verification enforces — the value refRelaxedExpectation produced.
	var installExpected *lockfile.Provenance
	calls := 0
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().
		DoAndReturn(func(_ any, _, _ []byte, expected *lockfile.Provenance) (*verifier.Result, error) {
			calls++
			if calls == 1 {
				return refSignedResult(installedRef), nil // initial install (TOFU)
			}
			candidate := refSignedResult(releaseRef)
			if expected == nil {
				return candidate, nil // the upgrade's plan-time signer probe
			}
			installExpected = expected
			if expected.RepositoryRef != "" && expected.RepositoryRef != candidate.RepositoryRef {
				return nil, verifier.ErrSignerMismatch
			}
			return candidate, nil
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
	result, err := svc.(*service).Upgrade(t.Context(), skills.UpgradeOptions{ProjectRoot: projectRoot}) //nolint:forcetypeassert
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, skills.UpgradeStatusUpgraded, result.Outcomes[0].Status,
		"a release signed on a new ref must not need --allow-signer-change")

	require.NotNil(t, installExpected)
	assert.Empty(t, installExpected.RepositoryRef,
		"upgrade must relax the pinned ref rather than reject the new release ref")
	assert.Equal(t, testRunnerEnvironment, installExpected.RunnerEnvironment,
		"the runner class has no release-workflow carve-out and stays enforced")
	assert.Equal(t, testSignerIdentity, installExpected.SignerIdentity)

	entry, ok = readLockfile(t, projectRoot).Get("repin-skill")
	require.True(t, ok)
	require.NotNil(t, entry.Provenance)
	assert.Equal(t, releaseRef, entry.Provenance.RepositoryRef,
		"the upgrade must re-record the new ref, so the next install enforces it")
}

func TestRefRelaxedExpectation(t *testing.T) {
	t.Parallel()

	locked := &lockfile.Provenance{
		SignerIdentity:    testSignerIdentity,
		RepositoryRef:     "refs/tags/v0.1.0",
		RunnerEnvironment: testRunnerEnvironment,
	}

	tests := []struct {
		name    string
		opts    skills.InstallOptions
		relaxed bool
	}{
		{name: "plain install enforces the pinned ref", opts: skills.InstallOptions{}},
		{
			name: "sync restore enforces the pinned ref",
			opts: skills.InstallOptions{SyncRestore: true, LockResolvedReference: "https://github.com/org/repo"},
		},
		{name: "upgrade re-pin relaxes it", opts: skills.InstallOptions{AllowRefRepin: true}, relaxed: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := refRelaxedExpectation(locked, tc.opts)
			require.NotNil(t, got)
			if !tc.relaxed {
				assert.Equal(t, locked, got)
				return
			}
			assert.Empty(t, got.RepositoryRef)
			assert.Equal(t, testRunnerEnvironment, got.RunnerEnvironment, "only the ref is relaxed")
			assert.Equal(t, "refs/tags/v0.1.0", locked.RepositoryRef, "the caller's lock entry must not be mutated")
		})
	}

	assert.Nil(t, refRelaxedExpectation(nil, skills.InstallOptions{AllowRefRepin: true}),
		"trust on first use has nothing to relax")
}

func TestRefTransitionAllowed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		recordedRef string
		candidate   string
		want        bool
	}{
		{name: "unpinned entry allows anything", recordedRef: "", candidate: "refs/heads/attacker", want: true},
		{name: "tag rotates to another tag", recordedRef: "refs/tags/v0.1.0", candidate: "refs/tags/v0.2.0", want: true},
		{name: "tag to a branch is blocked", recordedRef: "refs/tags/v0.1.0", candidate: "refs/heads/main", want: false},
		{name: "tag to no ref at all is blocked", recordedRef: "refs/tags/v0.1.0", candidate: "", want: false},
		{name: "identical branch stays allowed", recordedRef: "refs/heads/main", candidate: "refs/heads/main", want: true},
		{
			name: "branch to a different branch is blocked", recordedRef: "refs/heads/main",
			candidate: "refs/heads/attacker", want: false,
		},
		{name: "branch to a tag is blocked", recordedRef: "refs/heads/main", candidate: "refs/tags/v0.1.0", want: false},
		{name: "branch to no ref at all is blocked", recordedRef: "refs/heads/main", candidate: "", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, refTransitionAllowed(tc.recordedRef, tc.candidate))
		})
	}
}

// TestUpgrade_RefTransitionBlocked is the regression test for the guard the
// P1 fix restores: guardSignerChange must reject a ref transition
// refTransitionAllowed disallows, and — critically — the transition must
// never reach applyUpgrade's refRelaxedExpectation at all, so the lock stays
// untouched. Before the fix, every upgrade unconditionally cleared the
// expected ref (AllowRefRepin: true) with no prior check, so a candidate
// signed by the same identity/issuer/runner from a different branch would
// pass and silently replace the locked ref — the exact substitution this
// PR's ref pinning exists to catch.
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
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gr, fx := newGitResolverMock(t)
			fx.register("ref-guarded-skill", gitSkill("ref-guarded-skill"))

			calls := 0
			mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
			mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
				AnyTimes().
				DoAndReturn(func(_ any, _, _ []byte, expected *lockfile.Provenance) (*verifier.Result, error) {
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

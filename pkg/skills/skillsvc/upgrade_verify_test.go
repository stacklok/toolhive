// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"fmt"
	"strings"
	"testing"

	godigest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	ociskills "github.com/stacklok/toolhive-core/oci/skills"
	ocimocks "github.com/stacklok/toolhive-core/oci/skills/mocks"
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

// TestJudgeKeyedCandidate covers the measurement of a candidate against a
// key-pinned entry, and what each mode does with the result. The keyless
// guard probes for a certificate identity to compare, which a key-pair bundle
// does not have — so before the keyed path existed a key-pinned entry could
// never be upgraded at all: the probe returned "key-signed" and the plan
// failed with a signature error that named none of the real situation.
//
// The verdicts are split by what --allow-signer-change can actually do about
// them. It drops the recorded key and re-verifies keylessly, which is a real
// fix for exactly one state — a candidate that conclusively moved to keyless
// signing — and a dead end for everything else, so nothing else is reported
// as a signer change.
func TestJudgeKeyedCandidate(t *testing.T) {
	t.Parallel()

	keyPEM, err := verifier.DecodePublicKey(testPublicKeyB64)
	require.NoError(t, err)
	newRef, newDigest := "example.com/org/keyed-skill:v2", "sha256:"+strings.Repeat("c", 64)

	tests := []struct {
		name      string
		verifyErr error
		probe     *verifier.Result
		probeErr  error
		noProbe   bool

		wantKind     keyedVerdictKind
		wantIdentity string
		wantReason   skills.FailureReason
		wantErrText  string
		wantNoText   string

		// what each mode does with the verdict
		wantBlocked         bool
		wantBlockedOverride bool
		wantStatus          skills.UpgradeStatus
	}{
		{
			name:        "verifying against the pinned key is the evidence the signer is unchanged",
			noProbe:     true,
			wantKind:    keyedPinHolds,
			wantBlocked: false, wantBlockedOverride: false,
		},
		{
			name:         "a candidate that moved to keyless signing is a signer change",
			verifyErr:    verifier.ErrKeylessSigned,
			probe:        &verifier.Result{Signed: true, SignerIdentity: "ci@example.com"},
			wantKind:     keyedMovedToKeyless,
			wantIdentity: "ci@example.com",
			// Blocked without the override, permitted with it: this is the
			// one transition --allow-signer-change exists to authorize.
			wantBlocked: true, wantBlockedOverride: false,
			wantStatus: skills.UpgradeStatusSignerChangeBlocked,
		},
		{
			// VerifyOCIWithKey reports ErrKeylessSigned only when EVERY
			// bundle is keyless, so an artifact mid-migration — valid
			// keyless bundle beside a stale key-pair one — arrives as
			// ErrSignatureInvalid. It is still the supported transition.
			name:         "a mixed keyless and stale-key artifact is the supported transition",
			verifyErr:    verifier.ErrSignatureInvalid,
			probe:        &verifier.Result{Signed: true, SignerIdentity: "ci@example.com"},
			wantKind:     keyedMovedToKeyless,
			wantIdentity: "ci@example.com",
			wantBlocked:  true, wantBlockedOverride: false,
			wantStatus: skills.UpgradeStatusSignerChangeBlocked,
		},
		{
			// --allow-signer-change bypasses the guard but not verification,
			// and upgrade has no --allow-unsigned to pair with it.
			name:        "a candidate that lost its signature is an unsigned rejection",
			verifyErr:   verifier.ErrUnsigned,
			noProbe:     true,
			wantKind:    keyedUndecided,
			wantReason:  skills.FailureReasonUnsignedRejected,
			wantErrText: "--scope project --allow-unsigned",
			wantBlocked: true, wantBlockedOverride: true,
			wantStatus: skills.UpgradeStatusFailed,
		},
		{
			name:        "a different key is a failure, since allow_signer_change cannot re-anchor",
			verifyErr:   verifier.ErrSignatureInvalid,
			probeErr:    verifier.ErrKeySigned,
			wantKind:    keyedUndecided,
			wantReason:  skills.FailureReasonSignatureInvalid,
			wantErrText: "--scope project --public-key",
			wantBlocked: true, wantBlockedOverride: true,
			wantStatus: skills.UpgradeStatusFailed,
		},
		{
			name:        "an all-keyless candidate whose signature is broken is a failure",
			verifyErr:   verifier.ErrKeylessSigned,
			probeErr:    verifier.ErrSignatureInvalid,
			wantKind:    keyedUndecided,
			wantReason:  skills.FailureReasonSignatureInvalid,
			wantErrText: "dropped key-pair signing for keyless",
			wantBlocked: true, wantBlockedOverride: true,
			wantStatus: skills.UpgradeStatusFailed,
		},
		{
			// SECURITY: an operational failure is not evidence about which
			// key signed the artifact, so it must not read as "the pin no
			// longer applies" — under the override that would be enough to
			// drop the pin. The probe is given a result that WOULD succeed
			// and asserted never called: an artifact can carry a valid
			// keyless signature beside a still-valid pinned-key one, so
			// consulting it here would re-anchor on a transient fault.
			name:        "an operational verifier failure is not a signature verdict",
			verifyErr:   fmt.Errorf("fetching signatures: %w", context.DeadlineExceeded),
			probe:       &verifier.Result{Signed: true, SignerIdentity: "ci@example.com"},
			noProbe:     true,
			wantKind:    keyedUndecided,
			wantReason:  skills.FailureReasonUnknown,
			wantErrText: "context deadline exceeded",
			wantNoText:  "uninstall",
			wantBlocked: true, wantBlockedOverride: true,
			wantStatus: skills.UpgradeStatusFailed,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
			mv.EXPECT().VerifyOCIWithKey(gomock.Any(), newRef, newDigest, keyPEM).
				Return(nil, tc.verifyErr)
			// A keyed success and a bare unsigned artifact decide without a
			// probe; every other arm must establish what the candidate
			// actually carries before naming a diagnosis.
			probeCalls := 1
			if tc.noProbe {
				probeCalls = 0
			}
			mv.EXPECT().VerifyOCI(gomock.Any(), newRef, newDigest, gomock.Nil()).
				Times(probeCalls).Return(tc.probe, tc.probeErr)

			svc := &service{sigVerifier: mv}
			verdict := svc.judgeKeyedCandidate(t.Context(), keyedLockEntry(), newRef, newDigest)

			assert.Equal(t, tc.wantKind, verdict.kind)
			assert.Equal(t, tc.wantIdentity, verdict.identity)
			assert.Equal(t, tc.wantReason, verdict.reason)
			if tc.wantErrText != "" {
				assert.Contains(t, verdict.err, tc.wantErrText)
			}
			if tc.wantNoText != "" {
				assert.NotContains(t, verdict.err, tc.wantNoText,
					"an operational failure must not carry a destructive remedy")
			}

			for _, override := range []bool{false, true} {
				outcome := skills.UpgradeOutcome{Name: "keyed-skill"}
				blocked := recordKeyedVerdict(verdict, override, &outcome)
				want := tc.wantBlocked
				if override {
					want = tc.wantBlockedOverride
				}
				assert.Equal(t, want, blocked, "allow_signer_change=%v", override)
				if !blocked {
					continue
				}
				assert.Equal(t, tc.wantStatus, outcome.Status)
				// The CLI renders a blocked outcome carrying no identity as
				// "unsigned", so a keyless candidate that arrives unnamed is
				// reported as the one thing it demonstrably is not.
				assert.Equal(t, tc.wantIdentity, outcome.NewSignerIdentity)
			}
		})
	}
}

// TestJudgeKeyedCandidate_UndecodablePinnedKey fails the plan rather than
// treating a corrupt anchor as a signer change: the lock file is
// hand-editable and the entry cannot be evaluated at all, which is not the
// same claim. It must not become permission to drop the pin either.
func TestJudgeKeyedCandidate_UndecodablePinnedKey(t *testing.T) {
	t.Parallel()

	entry := keyedLockEntry()
	entry.Provenance = &lockfile.Provenance{PublicKey: "not-base64!!"}
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyOCIWithKey(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
	mv.EXPECT().VerifyOCI(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

	svc := &service{sigVerifier: mv}
	verdict := svc.judgeKeyedCandidate(t.Context(), entry,
		"example.com/org/keyed-skill:v2", "sha256:"+strings.Repeat("c", 64))
	assert.Equal(t, keyedUndecided, verdict.kind)
	assert.Equal(t, skills.FailureReasonUnknown, verdict.reason)

	for _, override := range []bool{false, true} {
		outcome := skills.UpgradeOutcome{Name: entry.Name}
		require.True(t, recordKeyedVerdict(verdict, override, &outcome),
			"allow_signer_change must not rescue an anchor that cannot be read")
		assert.Equal(t, skills.UpgradeStatusFailed, outcome.Status)
	}
}

// keyPinnedUpgradeFixture installs an OCI skill against testPublicKeyB64 —
// so the lock entry is genuinely key-pinned by the install path rather than
// hand-written — and publishes a second version so an upgrade is planned. The
// returned function switches which digest the tag resolves to; callers move it
// before upgrading.
//
// The registry client is mocked but the OCI store is real: Pull only reports
// which digest a reference names, and the content is read back out of the
// store, so both the guard and the install exercise their real code paths.
func keyPinnedUpgradeFixture(
	t *testing.T, mv verifier.Verifier,
) (*service, string, func()) {
	t.Helper()

	ociStore, err := ociskills.NewStore(tempDir(t))
	require.NoError(t, err)

	d1 := buildTestArtifact(t, ociStore, "my-skill", "1.0.0")
	d2 := buildTestArtifact(t, ociStore, "my-skill", "2.0.0")
	tagged := d1

	reg := ocimocks.NewMockRegistryClient(gomock.NewController(t))
	reg.EXPECT().Pull(gomock.Any(), ociStore, gomock.Any()).AnyTimes().
		DoAndReturn(func(_ context.Context, _ *ociskills.Store, ref string) (godigest.Digest, error) {
			// A digest-pinned reference names its own artifact; the tag
			// resolves to whatever is currently published under it.
			if _, after, found := strings.Cut(ref, "@"); found {
				return godigest.Digest(after), nil
			}
			return tagged, nil
		})

	gr, _ := newGitResolverMock(t)
	svc, projectRoot := newLockTestService(t, gr,
		WithVerifier(mv), WithRegistryClient(reg), WithOCIStore(ociStore))

	_, err = svc.Install(t.Context(), skills.InstallOptions{
		Name:        "ghcr.io/org/my-skill:v1",
		PublicKey:   testPublicKeyB64,
		Scope:       skills.ScopeProject,
		ProjectRoot: projectRoot,
		Clients:     []string{"claude-code"},
	})
	require.NoError(t, err)

	entry, ok := loadLockEntry(t, projectRoot, "my-skill")
	require.True(t, ok)
	require.NotNil(t, entry.Provenance)
	require.Equal(t, testPublicKeyB64, entry.Provenance.PublicKey,
		"precondition: the install must have pinned the key")

	return svc.(*service), projectRoot, func() { tagged = d2 } //nolint:forcetypeassert // white-box test
}

// upgradeKeyPinned runs an upgrade over the single fixture entry and returns
// its outcome.
func upgradeKeyPinned(t *testing.T, svc *service, opts skills.UpgradeOptions) skills.UpgradeOutcome {
	t.Helper()
	result, err := svc.Upgrade(t.Context(), opts)
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	return result.Outcomes[0]
}

// TestUpgrade_KeyPinnedEntryVerifiesAgainstPinnedKey proves the whole keyed
// upgrade, not just the guard: the candidate is checked against the pinned
// key, and the install applyUpgrade then performs is checked against it too.
// That install carries no key of its own — resolveKeyAnchor reads the anchor
// back out of the lock — so a regression that dropped the pin would surface
// here as a keyless verification call rather than as a wrong result.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_KeyPinnedEntryVerifiesAgainstPinnedKey(t *testing.T) {
	keyPEM, err := verifier.DecodePublicKey(testPublicKeyB64)
	require.NoError(t, err)

	keyCalls := 0
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyOCIWithKey(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Eq(keyPEM)).
		AnyTimes().
		DoAndReturn(func(_ context.Context, _, _ string, _ []byte) (*verifier.Result, error) {
			keyCalls++
			return &verifier.Result{Signed: true, Bundle: []byte(`{"bundle":true}`)}, nil
		})
	// Neither the guard nor the install may fall back to the keyless path.
	mv.EXPECT().VerifyOCI(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

	svc, projectRoot, publishV2 := keyPinnedUpgradeFixture(t, mv)
	installCalls := keyCalls
	publishV2()

	outcome := upgradeKeyPinned(t, svc, skills.UpgradeOptions{ProjectRoot: projectRoot})
	assert.Equal(t, skills.UpgradeStatusUpgraded, outcome.Status,
		"a candidate that verifies against the pinned key must not be blocked; error: %s", outcome.Error)

	assert.Equal(t, 2, keyCalls-installCalls,
		"the signer guard and the install applyUpgrade performs must each verify against the key")

	entry, ok := loadLockEntry(t, projectRoot, "my-skill")
	require.True(t, ok)
	require.NotNil(t, entry.Provenance)
	assert.Equal(t, testPublicKeyB64, entry.Provenance.PublicKey,
		"the upgrade must leave the entry pinned to the same key")
	assert.Empty(t, entry.Provenance.SignerIdentity,
		"a key-pair bundle carries no certificate identity to record")
	assert.Equal(t, outcome.NewDigest, entry.Digest)
}

// TestUpgrade_AllowSignerChangeMovesKeyPinnedEntryToKeyless covers the one
// supported way off a key: --allow-signer-change drops the recorded key and
// re-verifies keylessly, so the entry ends up anchored to the identity that
// was actually observed. This is why a keyless candidate blocks as a signer
// change rather than failing — the remedy the CLI prints genuinely works.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_AllowSignerChangeMovesKeyPinnedEntryToKeyless(t *testing.T) {
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	// The install pins the key; the candidate has since moved to keyless
	// signing, which is what makes the override applicable at all.
	installed := false
	mv.EXPECT().VerifyOCIWithKey(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().
		DoAndReturn(func(_ any, _, _ string, _ []byte) (*verifier.Result, error) {
			if !installed {
				installed = true
				return &verifier.Result{Signed: true, Bundle: []byte(`{"bundle":true}`)}, nil
			}
			return nil, verifier.ErrKeylessSigned
		})
	mv.EXPECT().VerifyOCI(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().Return(signedResult(), nil)

	svc, projectRoot, publishV2 := keyPinnedUpgradeFixture(t, mv)
	publishV2()

	outcome := upgradeKeyPinned(t, svc, skills.UpgradeOptions{
		ProjectRoot: projectRoot, AllowSignerChange: true,
	})
	assert.Equal(t, skills.UpgradeStatusUpgraded, outcome.Status, "error: %s", outcome.Error)

	entry, ok := loadLockEntry(t, projectRoot, "my-skill")
	require.True(t, ok)
	require.NotNil(t, entry.Provenance)
	assert.Empty(t, entry.Provenance.PublicKey,
		"the override drops the recorded key rather than keeping a pin it did not enforce")
	assert.Equal(t, testSignerIdentity, entry.Provenance.SignerIdentity,
		"the entry must be re-anchored to the identity actually observed")
	assert.Equal(t, testCertIssuer, entry.Provenance.CertIssuer)
}

// TestUpgrade_AllowSignerChangeKeepsSameKeyPin is the multi-skill case:
// --allow-signer-change is a project-wide flag, so needing it for one skill
// must not silently unpin another. The override authorizes dropping a
// recorded key only when the candidate actually moved off it; a candidate
// still signed by the pinned key keeps the pin, because dropping it sends a
// key-signed artifact through keyless verification, which refuses it.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_AllowSignerChangeKeepsSameKeyPin(t *testing.T) {
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyOCIWithKey(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().Return(&verifier.Result{Signed: true, Bundle: []byte(`{"bundle":true}`)}, nil)
	// What a key-signed artifact really answers when verified keylessly.
	mv.EXPECT().VerifyOCI(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().Return(nil, verifier.ErrKeySigned)

	svc, projectRoot, publishV2 := keyPinnedUpgradeFixture(t, mv)
	publishV2()

	outcome := upgradeKeyPinned(t, svc, skills.UpgradeOptions{
		ProjectRoot: projectRoot, AllowSignerChange: true,
	})
	assert.Equal(t, skills.UpgradeStatusUpgraded, outcome.Status, "error: %s", outcome.Error)

	entry, ok := loadLockEntry(t, projectRoot, "my-skill")
	require.True(t, ok)
	require.NotNil(t, entry.Provenance)
	assert.Equal(t, testPublicKeyB64, entry.Provenance.PublicKey,
		"a candidate that still verifies against the pinned key stays pinned to it")
	assert.Empty(t, entry.Provenance.SignerIdentity)
}

// TestUpgrade_OperationalKeyedErrorDoesNotDropPin is the fail-closed case for
// a project-wide --allow-signer-change. A registry or transport fault during
// planning says nothing about which key signed the candidate, so it must not
// stand in as evidence that the skill moved off its pinned key. If it did, a
// transient fault would be enough to drop the pin and re-anchor an artifact
// that still carries a valid signature by that very key.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_OperationalKeyedErrorDoesNotDropPin(t *testing.T) {
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	installed := false
	mv.EXPECT().VerifyOCIWithKey(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().
		DoAndReturn(func(_ any, _, _ string, _ []byte) (*verifier.Result, error) {
			if !installed {
				installed = true
				return &verifier.Result{Signed: true, Bundle: []byte(`{"bundle":true}`)}, nil
			}
			return nil, fmt.Errorf("fetching signatures: %w", context.DeadlineExceeded)
		})
	// The candidate also carries a perfectly good keyless signature, so a
	// probe would succeed. That is the trap: the artifact still carries a
	// valid pinned-key signature too, and only the transient keyed fault
	// makes it look like it moved. The probe must never be reached.
	mv.EXPECT().VerifyOCI(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().Return(signedResult(), nil)

	svc, projectRoot, publishV2 := keyPinnedUpgradeFixture(t, mv)
	before, ok := loadLockEntry(t, projectRoot, "my-skill")
	require.True(t, ok)
	publishV2()

	outcome := upgradeKeyPinned(t, svc, skills.UpgradeOptions{
		ProjectRoot: projectRoot, AllowSignerChange: true,
	})
	assert.Equal(t, skills.UpgradeStatusFailed, outcome.Status)
	assert.Equal(t, skills.FailureReasonUnknown, outcome.Reason)
	assert.NotContains(t, outcome.Error, "uninstall",
		"a transport fault must not advise uninstalling the skill")

	after, ok := loadLockEntry(t, projectRoot, "my-skill")
	require.True(t, ok)
	require.NotNil(t, after.Provenance)
	assert.Equal(t, testPublicKeyB64, after.Provenance.PublicKey, "the pin must survive a transport fault")
	assert.Empty(t, after.Provenance.SignerIdentity, "nothing may be re-anchored to keyless")
	assert.Equal(t, before.Digest, after.Digest, "a failed plan must not re-pin the entry")
}

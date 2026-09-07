// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"context"
	"net/http"
	"strings"
	"testing"

	godigest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
	ociplugins "github.com/stacklok/toolhive-core/oci/plugins"
	ocimocks "github.com/stacklok/toolhive-core/oci/plugins/mocks"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	"github.com/stacklok/toolhive/pkg/skills/verifier"
	verifiermocks "github.com/stacklok/toolhive/pkg/skills/verifier/mocks"
)

const testRunnerEnvironment = "github-hosted"

// otherSignerResult is a verification result from a different identity than
// signedResult's.
func otherSignerResult() *verifier.Result {
	r := signedResult()
	r.SignerIdentity = "/.github/workflows/other.yml"
	return r
}

// provenanceSignedResult is signedResult with the pinned certificate fields
// the guard also enforces (repository ref and runner class) populated.
func provenanceSignedResult(ref, runner string) *verifier.Result {
	r := signedResult()
	r.RepositoryRef = ref
	r.RunnerEnvironment = runner
	return r
}

// signerChangeFixture installs a signed git plugin, then adds a commit at the
// same source so an upgrade is planned, and returns a service whose verifier
// reports the candidate as candidate() (or unsigned when candidate returns
// nil, err). The initial install always sees signedResult, so the lock entry
// is anchored to the fixed test identity.
func signerChangeFixture(
	t *testing.T,
	candidate func() (*verifier.Result, error),
) (plugins.PluginService, string) {
	t.Helper()
	repoDir := createPluginTestRepo(t, "")

	calls := 0
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().
		DoAndReturn(func(_ any, _, _ []byte, expected *verifier.ProvenanceExpectation) (*verifier.Result, error) {
			calls++
			if calls == 1 {
				return signedResult(), nil // initial install (TOFU)
			}
			result, err := candidate()
			if err != nil {
				return nil, err
			}
			// Stand in for the real verifier's lock-pin enforcement: a
			// non-nil expectation that does not describe the candidate is a
			// signer mismatch. The probe always passes nil, so this only
			// fires on the install applyUpgrade performs.
			if expected != nil && !assert.ObjectsAreEqual(verifier.NewLockExpectation(result.ToLockProvenance()), expected) {
				return nil, verifier.ErrSignerMismatch
			}
			return result, nil
		})
	mv.EXPECT().VerifyBundleOffline(gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().Return(nil)

	svc, projectRoot := newGitLockTestService(t, repoDir, WithVerifier(mv))
	installGitTestPlugin(t, svc, projectRoot)

	// Add a commit at the same source so an upgrade is planned.
	addPluginRepoCommit(t, repoDir, "# hello guarded")
	return svc, projectRoot
}

// upgradePlugins runs Upgrade against every entry in projectRoot's lock file.
func upgradePlugins(t *testing.T, svc plugins.PluginService, opts plugins.UpgradeOptions) plugins.UpgradeOutcome {
	t.Helper()
	result, err := svc.(*service).Upgrade(t.Context(), opts) //nolint:forcetypeassert
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	return result.Outcomes[0]
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestUpgrade_SameSignerProceeds(t *testing.T) {
	svc, projectRoot := signerChangeFixture(t, func() (*verifier.Result, error) {
		return signedResult(), nil
	})

	outcome := upgradePlugins(t, svc, plugins.UpgradeOptions{ProjectRoot: projectRoot})
	assert.Equal(t, plugins.UpgradeStatusUpgraded, outcome.Status,
		"a candidate signed by the recorded identity must not be blocked")

	entry, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)
	require.NotNil(t, entry.Provenance)
	assert.Equal(t, testSignerIdentity, entry.Provenance.SignerIdentity)
	assert.Equal(t, outcome.NewDigest, entry.Digest)
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestUpgrade_SignerChangeBlocked(t *testing.T) {
	svc, projectRoot := signerChangeFixture(t, func() (*verifier.Result, error) {
		return otherSignerResult(), nil
	})

	outcome := upgradePlugins(t, svc, plugins.UpgradeOptions{ProjectRoot: projectRoot})
	assert.Equal(t, plugins.UpgradeStatusSignerChangeBlocked, outcome.Status)
	assert.Equal(t, "/.github/workflows/other.yml", outcome.NewSignerIdentity)

	// Nothing installed, lock unchanged.
	entry, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)
	require.NotNil(t, entry.Provenance)
	assert.Equal(t, testSignerIdentity, entry.Provenance.SignerIdentity)
	assert.Equal(t, outcome.OldDigest, entry.Digest, "a blocked upgrade must not re-pin the entry")
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestUpgrade_UnsignedCandidateBlockedAgainstSignedEntry(t *testing.T) {
	svc, projectRoot := signerChangeFixture(t, func() (*verifier.Result, error) {
		return nil, verifier.ErrUnsigned
	})

	outcome := upgradePlugins(t, svc, plugins.UpgradeOptions{ProjectRoot: projectRoot})
	assert.Equal(t, plugins.UpgradeStatusSignerChangeBlocked, outcome.Status)
	assert.Empty(t, outcome.NewSignerIdentity, "an unsigned candidate has no identity to report")
}

// TestUpgrade_ProvenanceFieldDivergenceBlocked covers the fields beyond the
// signer identity that the guard pins: a certificate's repository ref and its
// runner class. A candidate signed by the SAME identity and issuer from a
// different branch, a different tag, or a different runner class is the exact
// substitution the pinning exists to catch, so each blocks like a genuine
// signer change — and must never reach applyUpgrade's install call, leaving
// the lock untouched.
//
//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestUpgrade_ProvenanceFieldDivergenceBlocked(t *testing.T) {
	tests := []struct {
		name            string
		lockedRef       string
		lockedRunner    string
		candidateRef    string
		candidateRunner string
		description     string
	}{
		{
			name:      "attacker branch",
			lockedRef: "refs/heads/main", candidateRef: "refs/heads/attacker",
			lockedRunner: testRunnerEnvironment, candidateRunner: testRunnerEnvironment,
			description: "same identity, issuer, and runner, signed from a different branch",
		},
		{
			name:      "plausible tag rotation",
			lockedRef: "refs/tags/v0.1.0", candidateRef: "refs/tags/v0.2.0",
			lockedRunner: testRunnerEnvironment, candidateRunner: testRunnerEnvironment,
			description: "a tag-to-tag rotation has no automatic allowance either",
		},
		{
			name:      "candidate lost its ref extension",
			lockedRef: "refs/tags/v0.1.0", candidateRef: "",
			lockedRunner: testRunnerEnvironment, candidateRunner: testRunnerEnvironment,
			description: "a certificate that stopped carrying a ref extension must not silently unpin one",
		},
		{
			name:      "runner class change",
			lockedRef: "refs/heads/main", candidateRef: "refs/heads/main",
			lockedRunner: testRunnerEnvironment, candidateRunner: "self-hosted",
			description: "a move from a hosted runner to a self-hosted one is a provenance change",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repoDir := createPluginTestRepo(t, "")

			calls := 0
			mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
			mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
				AnyTimes().
				DoAndReturn(func(_ any, _, _ []byte, expected *verifier.ProvenanceExpectation) (*verifier.Result, error) {
					calls++
					if calls == 1 {
						return provenanceSignedResult(tc.lockedRef, tc.lockedRunner), nil // initial install (TOFU)
					}
					if expected == nil {
						// The upgrade's plan-time signer probe.
						return provenanceSignedResult(tc.candidateRef, tc.candidateRunner), nil
					}
					// A blocked divergence must never reach here: applyUpgrade
					// only runs when guardSignerChange did not block.
					t.Fatalf("install-time verification must not run for a blocked upgrade: %s", tc.description)
					return nil, nil
				})
			mv.EXPECT().VerifyBundleOffline(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().Return(nil)

			svc, projectRoot := newGitLockTestService(t, repoDir, WithVerifier(mv))
			installGitTestPlugin(t, svc, projectRoot)

			entry, ok := loadPluginLockEntry(t, projectRoot)
			require.True(t, ok)
			require.NotNil(t, entry.Provenance)
			require.Equal(t, tc.lockedRef, entry.Provenance.RepositoryRef, "the install must pin the observed ref")

			addPluginRepoCommit(t, repoDir, "# hello divergence")
			outcome := upgradePlugins(t, svc, plugins.UpgradeOptions{ProjectRoot: projectRoot})
			assert.Equal(t, plugins.UpgradeStatusSignerChangeBlocked, outcome.Status, tc.description)

			entry, ok = loadPluginLockEntry(t, projectRoot)
			require.True(t, ok)
			require.NotNil(t, entry.Provenance)
			assert.Equal(t, tc.lockedRef, entry.Provenance.RepositoryRef,
				"a blocked upgrade must leave the locked ref untouched")
			assert.Equal(t, tc.lockedRunner, entry.Provenance.RunnerEnvironment,
				"a blocked upgrade must leave the locked runner class untouched")
		})
	}
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestUpgrade_AllowSignerChangeRecordsNewIdentity(t *testing.T) {
	svc, projectRoot := signerChangeFixture(t, func() (*verifier.Result, error) {
		return otherSignerResult(), nil
	})

	outcome := upgradePlugins(t, svc, plugins.UpgradeOptions{
		ProjectRoot: projectRoot, AllowSignerChange: true,
	})
	assert.Equal(t, plugins.UpgradeStatusUpgraded, outcome.Status)

	entry, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)
	require.NotNil(t, entry.Provenance)
	assert.Equal(t, "/.github/workflows/other.yml", entry.Provenance.SignerIdentity,
		"the explicit override must re-record the new identity")
	assert.False(t, entry.Unsigned)
}

// TestUpgrade_AllowSignerChangeRepinsProvenanceFields is the override's other
// half: a ref/runner divergence is re-pinned to what the candidate actually
// carries, so the next install enforces the new values rather than the stale
// ones.
//
//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestUpgrade_AllowSignerChangeRepinsProvenanceFields(t *testing.T) {
	svc, projectRoot := signerChangeFixture(t, func() (*verifier.Result, error) {
		return provenanceSignedResult("refs/tags/v0.2.0", "self-hosted"), nil
	})

	outcome := upgradePlugins(t, svc, plugins.UpgradeOptions{
		ProjectRoot: projectRoot, AllowSignerChange: true,
	})
	assert.Equal(t, plugins.UpgradeStatusUpgraded, outcome.Status)

	entry, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)
	require.NotNil(t, entry.Provenance)
	assert.Equal(t, "refs/tags/v0.2.0", entry.Provenance.RepositoryRef)
	assert.Equal(t, "self-hosted", entry.Provenance.RunnerEnvironment)
}

// TestUpgrade_SignerChangePreviewAndGateParity pins the two plan-only modes
// against the blocked outcome: --preview reports the same block without
// installing, and --fail-on-changes counts it as a change. Neither carries a
// pinned reference, so neither can install anything.
//
//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestUpgrade_SignerChangePreviewAndGateParity(t *testing.T) {
	svc, projectRoot := signerChangeFixture(t, func() (*verifier.Result, error) {
		return otherSignerResult(), nil
	})

	before, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)

	for _, opts := range []plugins.UpgradeOptions{
		{ProjectRoot: projectRoot, Preview: true},
		{ProjectRoot: projectRoot, FailOnChanges: true},
	} {
		outcome := upgradePlugins(t, svc, opts)
		assert.Equal(t, plugins.UpgradeStatusSignerChangeBlocked, outcome.Status,
			"plan-only modes must report the same signer-change block as apply")

		after, found := loadPluginLockEntry(t, projectRoot)
		require.True(t, found)
		assert.Equal(t, before.Digest, after.Digest, "a plan-only run must not rewrite the lock file")
		assert.Equal(t, testSignerIdentity, after.Provenance.SignerIdentity)
	}
}

// TestUpgrade_BlockedOutcomeCarriesNoPinnedReference proves the block happens
// during planning rather than at install: the plan carries no pinnedRef, which
// is what makes preview, the CI gate, and apply all agree.
//
//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestUpgrade_BlockedOutcomeCarriesNoPinnedReference(t *testing.T) {
	svc, projectRoot := signerChangeFixture(t, func() (*verifier.Result, error) {
		return otherSignerResult(), nil
	})

	entry, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)

	plan := svc.(*service).planUpgrade(t.Context(), //nolint:forcetypeassert
		plugins.UpgradeOptions{ProjectRoot: projectRoot}, entry)
	assert.Equal(t, plugins.UpgradeStatusSignerChangeBlocked, plan.outcome.Status)
	assert.Empty(t, plan.pinnedRef, "a blocked plan must carry no pinned reference to install")
	assert.Empty(t, plan.layerData)
}

// TestUpgrade_UnaffectedEntrySkipsSignerProbe covers the entries the guard
// leaves alone: one recorded `unsigned: true` has no identity to compare
// against, so no probe runs at all — an extra verification round-trip there
// would be both wasted work and a new failure mode for a plugin the lock file
// already accepts as unsigned.
//
//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestUpgrade_UnaffectedEntrySkipsSignerProbe(t *testing.T) {
	repoDir := createPluginTestRepo(t, "")

	calls := 0
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().
		DoAndReturn(func(_ any, _, _ []byte, _ *verifier.ProvenanceExpectation) (*verifier.Result, error) {
			calls++
			return nil, verifier.ErrUnsigned
		})
	mv.EXPECT().VerifyBundleOffline(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().Return(nil)

	svc, projectRoot := newGitLockTestService(t, repoDir, WithVerifier(mv))
	require.NoError(t, gitInstall(t, svc, projectRoot, func(o *plugins.InstallOptions) { o.AllowUnsigned = true }))

	entry, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)
	require.True(t, entry.Unsigned)
	require.Nil(t, entry.Provenance)
	require.Equal(t, 1, calls, "only the install itself consulted the verifier")

	addPluginRepoCommit(t, repoDir, "# hello unsigned")
	outcome := upgradePlugins(t, svc, plugins.UpgradeOptions{ProjectRoot: projectRoot})
	assert.Equal(t, plugins.UpgradeStatusUpgraded, outcome.Status,
		"an entry with no recorded signer identity is unaffected by the guard")
	assert.Equal(t, 1, calls,
		"the guard must not probe an entry that records no signer identity")
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

// TestVerifyInstallHonorsAllowSignerChange pins the install-side half of the
// override on the OCI path, which the git-sourced upgrade tests above never
// reach: with AllowSignerChange the recorded identity is not handed to the
// verifier as the expected one, so the chain of trust is checked and whatever
// identity is observed gets recorded in its place.
func TestVerifyInstallHonorsAllowSignerChange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		allowSignerChange bool
		wantExpected      bool
	}{
		{name: "default enforces the recorded identity", wantExpected: true},
		{name: "override verifies chain of trust only", allowSignerChange: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			projectRoot := writeSignedPluginLockEntry(t)

			mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
			mv.EXPECT().VerifyOCI(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
				DoAndReturn(func(_ any, _, _ string, expected *verifier.ProvenanceExpectation) (*verifier.Result, error) {
					assert.Equal(t, tc.wantExpected, expected != nil,
						"AllowSignerChange decides whether the recorded identity is enforced")
					return otherSignerResult(), nil
				})

			svc := &service{sigVerifier: mv}
			decision, err := svc.verifyOCIInstall(t.Context(), plugins.InstallOptions{
				ProjectRoot:       projectRoot,
				AllowSignerChange: tc.allowSignerChange,
			}, "my-plugin", "ghcr.io/org/my-plugin:v2", validLockDigest())
			require.NoError(t, err)
			require.NotNil(t, decision.provenance)
			assert.Equal(t, "/.github/workflows/other.yml", decision.provenance.SignerIdentity,
				"the observed identity is what gets recorded")
		})
	}
}

// writeSignedPluginLockEntry creates a project root whose lock file records
// the fixture plugin as signed by the fixed test identity.
func writeSignedPluginLockEntry(t *testing.T) string {
	t.Helper()
	projectRoot := makeProjectRoot(t)
	root := mustOpenRoot(t, projectRoot)
	lf, err := lockfile.Load(root)
	require.NoError(t, err)
	lf.UpsertPlugin(lockfile.Entry{
		Name:   "my-plugin",
		Source: "ghcr.io/org/my-plugin",
		Digest: validLockDigest(),
		Provenance: &lockfile.Provenance{
			SignerIdentity: testSignerIdentity,
			CertIssuer:     testCertIssuer,
		},
	})
	require.NoError(t, lf.Save(root))
	return projectRoot
}

// TestProbeCandidateSigner_RejectsOversizedCommitMaterial extends the size
// ceiling verifyGitInstall enforces (#6396 review) to the plan-time probe,
// which the install path never covers: the probe runs on every guarded
// upgrade, including --preview and --fail-on-changes, so an unbounded probe
// would let a hostile repository spend our CPU without ever reaching an
// install. Rejection happens before the verifier is consulted at all.
func TestProbeCandidateSigner_RejectsOversizedCommitMaterial(t *testing.T) {
	t.Parallel()

	oversized := make([]byte, maxSignatureBlobSize+1)

	tests := []struct {
		name    string
		latest  resolvedLatest
		wantErr bool
	}{
		{
			name:   "material within the limit is verified",
			latest: resolvedLatest{commitPayload: []byte("commit"), commitSignature: "sig"},
		},
		{
			name:    "oversized payload rejected",
			latest:  resolvedLatest{commitPayload: oversized, commitSignature: "sig"},
			wantErr: true,
		},
		{
			name:    "oversized signature rejected",
			latest:  resolvedLatest{commitPayload: []byte("commit"), commitSignature: string(oversized)},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
			if !tc.wantErr {
				mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Nil()).
					Return(signedResult(), nil)
			}

			svc := &service{sigVerifier: mv}
			result, err := svc.probeCandidateSigner(t.Context(), "my-plugin", tc.latest)
			if tc.wantErr {
				require.Error(t, err)
				assert.Equal(t, http.StatusUnprocessableEntity, httperr.Code(err),
					"oversized material is unprocessable, not a signature failure")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, testSignerIdentity, result.SignerIdentity)
		})
	}
}

// TestUpgrade_OversizedCommitMaterialFailsRatherThanBlocks pins how the guard
// reports a rejected probe: an over-limit candidate is a failure with a
// message, not a silent signer-change block, so the operator sees why.
func TestUpgrade_OversizedCommitMaterialFailsRatherThanBlocks(t *testing.T) {
	t.Parallel()

	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	svc := &service{sigVerifier: mv}
	outcome := plugins.UpgradeOutcome{Name: "my-plugin"}

	blocked := svc.guardSignerChange(t.Context(),
		lockfile.Entry{
			Name:       "my-plugin",
			Provenance: &lockfile.Provenance{SignerIdentity: testSignerIdentity},
		},
		resolvedLatest{commitPayload: make([]byte, maxSignatureBlobSize+1), commitSignature: "sig"},
		&outcome)

	assert.True(t, blocked, "an unusable probe must stop the upgrade")
	assert.Equal(t, plugins.UpgradeStatusFailed, outcome.Status)
	assert.Equal(t, plugins.FailureReasonUnknown, outcome.Reason)
	assert.Contains(t, outcome.Error, "over the")
}

// TestGuardKeyedSignerChange covers the guard for a key-pinned entry. The
// keyless guard probes for a certificate identity to compare, which a
// key-pair bundle does not have — so before this branch a key-pinned entry
// could never be upgraded at all: probeCandidateSigner returned ErrKeySigned
// and the plan failed with a signature error, where a policy decision was
// intended.
//
// The blocked arms are split by whether the remedy the CLI prints actually
// works. "signer change blocked" tells the user to pass --allow-signer-change,
// which drops the recorded key and re-verifies keylessly — a real fix when the
// candidate moved to keyless signing or lost its signature, and a dead end
// when it is simply signed by a different key.
func TestGuardKeyedSignerChange(t *testing.T) {
	t.Parallel()

	keyPEM, err := verifier.DecodePublicKey(testPublicKeyB64)
	require.NoError(t, err)
	latest := resolvedLatest{
		ref:    "ghcr.io/org/keyed-plugin:v2",
		digest: "sha256:" + strings.Repeat("c", 64),
	}

	tests := []struct {
		name        string
		verifyErr   error
		wantBlocked bool
		wantStatus  plugins.UpgradeStatus
		wantReason  plugins.FailureReason
		wantErrText string
	}{
		{
			name:        "verifying against the pinned key is the evidence the signer is unchanged",
			wantBlocked: false,
		},
		{
			name:        "a candidate that moved to keyless signing is a signer change",
			verifyErr:   verifier.ErrKeylessSigned,
			wantBlocked: true,
			wantStatus:  plugins.UpgradeStatusSignerChangeBlocked,
		},
		{
			name:        "a candidate that lost its signature is a signer change",
			verifyErr:   verifier.ErrUnsigned,
			wantBlocked: true,
			wantStatus:  plugins.UpgradeStatusSignerChangeBlocked,
		},
		{
			name:        "a different key is a failure, since allow_signer_change cannot re-anchor",
			verifyErr:   verifier.ErrSignatureInvalid,
			wantBlocked: true,
			wantStatus:  plugins.UpgradeStatusFailed,
			wantReason:  plugins.FailureReasonSignatureInvalid,
			wantErrText: "reinstall it with `thv ai-plugin install --public-key`",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
			mv.EXPECT().VerifyOCIWithKey(gomock.Any(), latest.ref, latest.digest, keyPEM).
				Return(nil, tc.verifyErr)
			// The keyless probe must not run: it is what produced the
			// unhelpful diagnosis this branch exists to replace.
			mv.EXPECT().VerifyOCI(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

			svc := &service{sigVerifier: mv}
			outcome := plugins.UpgradeOutcome{Name: "keyed-plugin"}
			blocked := svc.guardSignerChange(
				t.Context(), keyedLockEntry("keyed-plugin"), latest, &outcome)

			assert.Equal(t, tc.wantBlocked, blocked)
			if !tc.wantBlocked {
				return
			}
			assert.Equal(t, tc.wantStatus, outcome.Status)
			assert.Equal(t, tc.wantReason, outcome.Reason)
			if tc.wantErrText != "" {
				assert.Contains(t, outcome.Error, tc.wantErrText)
			}
			if tc.wantStatus == plugins.UpgradeStatusSignerChangeBlocked {
				assert.Empty(t, outcome.NewSignerIdentity,
					"a key-signed candidate has no identity to report, and inventing one would"+
						" print a signer the artifact never claimed")
			}
		})
	}
}

// TestGuardKeyedSignerChange_UndecodablePinnedKey fails the plan rather than
// treating a corrupt anchor as a signer change: the lock file is hand-editable
// and the entry cannot be evaluated at all, which is not the same claim.
func TestGuardKeyedSignerChange_UndecodablePinnedKey(t *testing.T) {
	t.Parallel()

	entry := keyedLockEntry("keyed-plugin")
	entry.Provenance = &lockfile.Provenance{PublicKey: "not-base64!!"}
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyOCIWithKey(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
	mv.EXPECT().VerifyOCI(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

	svc := &service{sigVerifier: mv}
	outcome := plugins.UpgradeOutcome{Name: entry.Name}
	require.True(t, svc.guardSignerChange(t.Context(), entry, resolvedLatest{
		ref:    "ghcr.io/org/keyed-plugin:v2",
		digest: "sha256:" + strings.Repeat("c", 64),
	}, &outcome))
	assert.Equal(t, plugins.UpgradeStatusFailed, outcome.Status)
	assert.Equal(t, plugins.FailureReasonUnknown, outcome.Reason)
	assert.NotEqual(t, plugins.UpgradeStatusSignerChangeBlocked, outcome.Status)
}

// keyPinnedUpgradeFixture installs an OCI plugin against testPublicKeyB64 —
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

	svc, projectRoot := newLockTestService(t, WithVerifier(mv))
	ociStore, err := ociplugins.NewStore(tempDir(t))
	require.NoError(t, err)

	d1 := buildTestPlugin(t, ociStore, "my-plugin", "1.0.0")
	d2 := buildTestPlugin(t, ociStore, "my-plugin", "2.0.0")
	tagged := d1

	reg := ocimocks.NewMockRegistryClient(gomock.NewController(t))
	reg.EXPECT().Pull(gomock.Any(), ociStore, gomock.Any()).AnyTimes().
		DoAndReturn(func(_ context.Context, _ *ociplugins.Store, ref string) (godigest.Digest, error) {
			// A digest-pinned reference names its own artifact; the tag
			// resolves to whatever is currently published under it.
			if _, after, found := strings.Cut(ref, "@"); found {
				return godigest.Digest(after), nil
			}
			return tagged, nil
		})

	inner := svc.(*service) //nolint:forcetypeassert
	inner.ociStore = ociStore
	inner.registry = reg

	_, err = svc.Install(t.Context(), plugins.InstallOptions{
		Name:        "ghcr.io/org/my-plugin:v1",
		PublicKey:   testPublicKeyB64,
		Scope:       plugins.ScopeProject,
		ProjectRoot: projectRoot,
		Clients:     []string{"claude-code"},
	})
	require.NoError(t, err)

	entry, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)
	require.NotNil(t, entry.Provenance)
	require.Equal(t, testPublicKeyB64, entry.Provenance.PublicKey,
		"precondition: the install must have pinned the key")

	return inner, projectRoot, func() { tagged = d2 }
}

// TestUpgrade_KeyPinnedEntryVerifiesAgainstPinnedKey proves the whole keyed
// upgrade, not just the guard: the candidate is checked against the pinned
// key, and the install applyUpgrade then performs is checked against it too.
// That install carries no key of its own — resolveKeyAnchor reads the anchor
// back out of the lock — so a regression that dropped the pin would surface
// here as a keyless verification call rather than as a wrong result.
//
//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
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
	mv.EXPECT().VerifyBundleOfflineWithKey(gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().Return(nil)
	// Neither the guard nor the install may fall back to the keyless path.
	mv.EXPECT().VerifyOCI(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

	inner, projectRoot, publishV2 := keyPinnedUpgradeFixture(t, mv)
	installCalls := keyCalls
	publishV2()

	outcome := upgradePlugins(t, inner, plugins.UpgradeOptions{ProjectRoot: projectRoot})
	assert.Equal(t, plugins.UpgradeStatusUpgraded, outcome.Status,
		"a candidate that verifies against the pinned key must not be blocked")

	assert.Equal(t, 2, keyCalls-installCalls,
		"the signer guard and the install applyUpgrade performs must each verify against the key")

	entry, ok := loadPluginLockEntry(t, projectRoot)
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
//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestUpgrade_AllowSignerChangeMovesKeyPinnedEntryToKeyless(t *testing.T) {
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyOCIWithKey(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().Return(&verifier.Result{Signed: true, Bundle: []byte(`{"bundle":true}`)}, nil)
	mv.EXPECT().VerifyOCI(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().Return(signedResult(), nil)
	mv.EXPECT().VerifyBundleOfflineWithKey(gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().Return(nil)
	mv.EXPECT().VerifyBundleOffline(gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().Return(nil)

	inner, projectRoot, publishV2 := keyPinnedUpgradeFixture(t, mv)
	publishV2()

	outcome := upgradePlugins(t, inner, plugins.UpgradeOptions{
		ProjectRoot: projectRoot, AllowSignerChange: true,
	})
	assert.Equal(t, plugins.UpgradeStatusUpgraded, outcome.Status)

	entry, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)
	require.NotNil(t, entry.Provenance)
	assert.Empty(t, entry.Provenance.PublicKey,
		"the override drops the recorded key rather than keeping a pin it did not enforce")
	assert.Equal(t, testSignerIdentity, entry.Provenance.SignerIdentity,
		"the entry must be re-anchored to the identity actually observed")
	assert.Equal(t, testCertIssuer, entry.Provenance.CertIssuer)
}

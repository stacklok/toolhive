// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
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

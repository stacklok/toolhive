// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

const (
	testSignerIdentity = "/.github/workflows/release.yml"
	testCertIssuer     = "https://token.actions.githubusercontent.com"
)

func signedResult() *verifier.Result {
	return &verifier.Result{
		Signed:         true,
		SignerIdentity: testSignerIdentity,
		CertIssuer:     testCertIssuer,
		RepositoryURI:  "https://github.com/org/repo",
		SigstoreURL:    "https://rekor.sigstore.dev",
		Bundle:         []byte(`{"bundle":true}`),
	}
}

// alwaysSignedVerifier reports every artifact as signed by the fixed test
// identity — including offline re-verification of stored bundles — so tests
// exercising lock/sync/upgrade mechanics don't trip verification.
func alwaysSignedVerifier(t *testing.T) verifier.Verifier {
	t.Helper()
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().Return(signedResult(), nil)
	mv.EXPECT().VerifyOCI(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().Return(signedResult(), nil)
	mv.EXPECT().VerifyBundleOffline(gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().Return(nil)
	mv.EXPECT().ResultFromBundle(gomock.Any(), gomock.Any()).
		AnyTimes().Return(signedResult(), nil)
	return mv
}

// loadPluginLockEntry reads the fixture plugin's lock entry from projectRoot.
func loadPluginLockEntry(t *testing.T, projectRoot string) (lockfile.Entry, bool) {
	t.Helper()
	return readLockfile(t, projectRoot).GetPlugin("my-plugin")
}

// gitInstall installs the fixture git plugin project-scoped.
func gitInstall(t *testing.T, svc plugins.PluginService, projectRoot string, mutate func(*plugins.InstallOptions)) error {
	t.Helper()
	opts := plugins.InstallOptions{
		Name:        gitPluginRef,
		Scope:       plugins.ScopeProject,
		ProjectRoot: projectRoot,
		Clients:     []string{"claude-code"},
	}
	if mutate != nil {
		mutate(&opts)
	}
	_, err := svc.Install(t.Context(), opts)
	return err
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestInstallVerification_TOFURecordsProvenance(t *testing.T) {
	repoDir := createPluginTestRepo(t, "")
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	// First install: no lock entry yet — trust on first use, nil expected.
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Nil()).
		Return(signedResult(), nil)

	svc, projectRoot := newGitLockTestService(t, repoDir, WithVerifier(mv))
	require.NoError(t, gitInstall(t, svc, projectRoot, nil))

	entry, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)
	require.NotNil(t, entry.Provenance, "TOFU must record the observed identity")
	assert.Equal(t, testSignerIdentity, entry.Provenance.SignerIdentity)
	assert.Equal(t, testCertIssuer, entry.Provenance.CertIssuer)
	assert.False(t, entry.Unsigned)

	stored, err := svc.Info(t.Context(), plugins.InfoOptions{
		Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	})
	require.NoError(t, err)
	assert.Equal(t, []byte(`{"bundle":true}`), stored.InstalledPlugin.SigstoreBundle,
		"the bundle must be persisted with the install record for offline re-verification")

	// Second install: the recorded identity must flow into the verifier as
	// the expected identity.
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ any, _, _ []byte, expected *verifier.ProvenanceExpectation) (*verifier.Result, error) {
			require.NotNil(t, expected, "the second install must enforce the recorded identity")
			assert.Equal(t, verifier.NewLockExpectation(entry.Provenance), expected)
			return signedResult(), nil
		})
	require.NoError(t, gitInstall(t, svc, projectRoot, func(o *plugins.InstallOptions) { o.Force = true }))
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestInstallVerification_UnsignedRejectedWithoutFlag(t *testing.T) {
	repoDir := createPluginTestRepo(t, "")
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Nil()).
		Return(nil, verifier.ErrUnsigned)

	svc, projectRoot := newGitLockTestService(t, repoDir, WithVerifier(mv))
	err := gitInstall(t, svc, projectRoot, nil)
	require.Error(t, err)
	assert.Equal(t, http.StatusForbidden, httperr.Code(err))

	_, ok := loadPluginLockEntry(t, projectRoot)
	assert.False(t, ok, "a rejected install must not write a lock entry")
	_, err = svc.Info(t.Context(), plugins.InfoOptions{
		Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	})
	require.Error(t, err, "a rejected install must not create a DB record")
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestInstallVerification_UnsignedAcceptedWithFlag(t *testing.T) {
	repoDir := createPluginTestRepo(t, "")
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Nil()).
		Return(nil, verifier.ErrUnsigned)

	svc, projectRoot := newGitLockTestService(t, repoDir, WithVerifier(mv))
	require.NoError(t, gitInstall(t, svc, projectRoot,
		func(o *plugins.InstallOptions) { o.AllowUnsigned = true }))

	entry, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)
	assert.True(t, entry.Unsigned, "the unsigned exception must be recorded")
	assert.Nil(t, entry.Provenance)

	stored, err := svc.Info(t.Context(), plugins.InfoOptions{
		Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	})
	require.NoError(t, err)
	assert.Nil(t, stored.InstalledPlugin.SigstoreBundle, "an unsigned install stores no bundle")
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestInstallVerification_SignerMismatchRejectedAndLockIntact(t *testing.T) {
	repoDir := createPluginTestRepo(t, "")
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Nil()).
		Return(signedResult(), nil)

	svc, projectRoot := newGitLockTestService(t, repoDir, WithVerifier(mv))
	require.NoError(t, gitInstall(t, svc, projectRoot, nil))

	// The re-install is signed by someone else: the verifier reports a
	// mismatch (the expected identity was bound into its policy).
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, verifier.ErrSignerMismatch)
	err := gitInstall(t, svc, projectRoot, func(o *plugins.InstallOptions) { o.Force = true })
	require.Error(t, err)
	assert.Equal(t, http.StatusForbidden, httperr.Code(err))

	// The prior trusted state is untouched.
	entry, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)
	require.NotNil(t, entry.Provenance)
	assert.Equal(t, testSignerIdentity, entry.Provenance.SignerIdentity)
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestInstallVerification_LockedUnsignedRequiresFlagAgain(t *testing.T) {
	repoDir := createPluginTestRepo(t, "")
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Nil()).
		Return(nil, verifier.ErrUnsigned)

	svc, projectRoot := newGitLockTestService(t, repoDir, WithVerifier(mv))
	require.NoError(t, gitInstall(t, svc, projectRoot,
		func(o *plugins.InstallOptions) { o.AllowUnsigned = true }))

	// Reinstall without the flag: the locked unsigned exception does not
	// silently renew — the verifier is not even consulted (the mock has no
	// second expectation, so a call would fail the test).
	err := gitInstall(t, svc, projectRoot, func(o *plugins.InstallOptions) { o.Force = true })
	require.Error(t, err)
	assert.Equal(t, http.StatusForbidden, httperr.Code(err))
}

// TestInstallVerification_LockDrivenInstallHonorsRecordedTrust proves a sync
// restore of an unsigned-locked entry does not demand the flag again: the
// lock file already records the decision the restore is materializing.
//
//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestInstallVerification_LockDrivenInstallHonorsRecordedTrust(t *testing.T) {
	repoDir := createPluginTestRepo(t, "")
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Nil()).
		Return(nil, verifier.ErrUnsigned)

	svc, projectRoot := newGitLockTestService(t, repoDir, WithVerifier(mv))
	require.NoError(t, gitInstall(t, svc, projectRoot,
		func(o *plugins.InstallOptions) { o.AllowUnsigned = true }))

	// Drop the install so sync has to restore it from the lock entry.
	inner := svc.(*service) //nolint:forcetypeassert
	require.NoError(t, os.RemoveAll(pluginOnDiskPath(projectRoot, "my-plugin")))
	require.NoError(t, inner.store.Delete(t.Context(), "my-plugin", plugins.ScopeProject, projectRoot))

	result, err := inner.Sync(t.Context(), plugins.SyncOptions{ProjectRoot: projectRoot})
	require.NoError(t, err)
	assert.Equal(t, []string{"my-plugin"}, result.Installed, "the restore must not demand allow_unsigned")
	assert.Empty(t, result.Failed)

	entry, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)
	assert.True(t, entry.Unsigned, "the restore keeps the recorded trust state")
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestInstallVerification_UserScopeSkipsVerification(t *testing.T) {
	repoDir := createPluginTestRepo(t, "")
	// The mock has no expectations: any verifier call fails the test.
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))

	svc, _ := newGitLockTestService(t, repoDir, WithVerifier(mv))
	_, err := svc.Install(t.Context(), plugins.InstallOptions{
		Name:    gitPluginRef,
		Scope:   plugins.ScopeUser,
		Clients: []string{"claude-code"},
	})
	require.NoError(t, err)
}

func TestVerifyLocalInstall(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		opts     plugins.InstallOptions
		entry    *lockfile.Entry
		wantErr  bool
		unsigned bool
	}{
		{
			name:    "no flag rejected",
			opts:    plugins.InstallOptions{},
			wantErr: true,
		},
		{
			name:     "flag records unsigned",
			opts:     plugins.InstallOptions{AllowUnsigned: true},
			unsigned: true,
		},
		{
			name: "locked identity refuses local replacement even with flag",
			opts: plugins.InstallOptions{AllowUnsigned: true},
			entry: &lockfile.Entry{
				Name:              "local-plugin",
				Source:            "example.com/org/local-plugin",
				ResolvedReference: "example.com/org/local-plugin:v1",
				Digest:            "sha256:" + strings.Repeat("a", 64),
				Provenance: &lockfile.Provenance{
					SignerIdentity: testSignerIdentity,
					CertIssuer:     testCertIssuer,
				},
			},
			wantErr: true,
		},
		{
			name: "locked unsigned honored with flag",
			opts: plugins.InstallOptions{AllowUnsigned: true},
			entry: &lockfile.Entry{
				Name:              "local-plugin",
				Source:            "example.com/org/local-plugin",
				ResolvedReference: "example.com/org/local-plugin:v1",
				Digest:            "sha256:" + strings.Repeat("a", 64),
				Unsigned:          true,
			},
			unsigned: true,
		},
		{
			name:     "lock-driven upgrade of a local build needs no flag",
			opts:     plugins.InstallOptions{ExpectedCanonicalName: "local-plugin"},
			unsigned: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			projectRoot := makeProjectRoot(t)
			if tc.entry != nil {
				require.NoError(t, lockfile.UpsertPluginEntry(mustOpenRoot(t, projectRoot), *tc.entry))
			}
			opts := tc.opts
			opts.ProjectRoot = projectRoot

			decision, err := verifyLocalInstall(opts, "local-plugin")
			if tc.wantErr {
				require.Error(t, err)
				assert.Equal(t, http.StatusForbidden, httperr.Code(err))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.unsigned, decision.unsigned)
			assert.Nil(t, decision.provenance)
		})
	}
}

func TestClassifySignatureError(t *testing.T) {
	t.Parallel()
	assert.Equal(t, plugins.FailureReasonSignerMismatch, classifySignatureError(verifier.ErrSignerMismatch))
	assert.Equal(t, plugins.FailureReasonUnsignedRejected, classifySignatureError(verifier.ErrUnsigned))
	assert.Equal(t, plugins.FailureReasonSignatureInvalid, classifySignatureError(verifier.ErrSignatureInvalid))
	assert.Equal(t, plugins.FailureReason(""), classifySignatureError(assert.AnError))

	// A pinned ref/runner mismatch satisfies errors.Is against BOTH
	// ErrSignerMismatch and ErrProvenanceFieldMismatch (see
	// verifier.pinnedFieldMismatch) — the more specific reason must win, or
	// every version bump on a ref-pinned plugin would misreport as a
	// publisher change rather than a provenance-field change.
	fieldMismatch := fmt.Errorf("%w: %w: locked to repository ref, but the artifact carries a different one",
		verifier.ErrSignerMismatch, verifier.ErrProvenanceFieldMismatch)
	assert.Equal(t, plugins.FailureReasonProvenanceFieldMismatch, classifySignatureError(fieldMismatch))
}

// TestClassifyInstallVerifyErrorDistinguishesProvenanceField covers the
// install-time (403) classification alongside TestClassifySignatureError's
// sync/upgrade coverage: a pinned ref/runner mismatch must not be reported
// to the operator as a signer-identity change.
func TestClassifyInstallVerifyErrorDistinguishesProvenanceField(t *testing.T) {
	t.Parallel()

	fieldMismatch := fmt.Errorf("%w: %w: locked to repository ref, but the artifact carries a different one",
		verifier.ErrSignerMismatch, verifier.ErrProvenanceFieldMismatch)
	err := classifyInstallVerifyError(fieldMismatch, "some-plugin", &lockfile.Provenance{SignerIdentity: testSignerIdentity})
	assert.Contains(t, err.Error(), "no longer matches its pinned provenance",
		"a provenance-field mismatch must lead with the field-specific wording, not the identity one")
	assert.NotContains(t, err.Error(), "signer identity mismatch for",
		"the identity-specific phrasing (distinct from ErrSignerMismatch's own wrapped message text) must not appear")

	identityMismatch := classifyInstallVerifyError(
		verifier.ErrSignerMismatch, "some-plugin", &lockfile.Provenance{SignerIdentity: testSignerIdentity})
	assert.Contains(t, identityMismatch.Error(), "signer identity mismatch for",
		"a genuine signer-identity mismatch keeps its existing wording")
}

// TestVerify_OversizedSignatureMaterialRejected covers the size ceiling
// deferred from #6396's review: neither the bundle a registry serves nor the
// commit payload/signature a repo serves is bounded at its source, so each is
// refused at capture time rather than written to the store. The git payload
// and signature are checked before the verifier is consulted, which the
// mock's missing VerifyGit expectation enforces.
func TestVerify_OversizedSignatureMaterialRejected(t *testing.T) {
	t.Parallel()

	oversized := make([]byte, maxSignatureBlobSize+1)

	tests := []struct {
		name    string
		arrange func(*verifiermocks.MockVerifier)
		call    func(*service) error
	}{
		{
			name: "oversized OCI bundle",
			arrange: func(mv *verifiermocks.MockVerifier) {
				result := signedResult()
				result.Bundle = oversized
				mv.EXPECT().VerifyOCI(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					Return(result, nil)
			},
			call: func(svc *service) error {
				_, err := svc.verifyOCIInstall(t.Context(), plugins.InstallOptions{},
					"my-plugin", "ghcr.io/org/my-plugin:v1", validLockDigest())
				return err
			},
		},
		{
			name: "oversized git bundle",
			arrange: func(mv *verifiermocks.MockVerifier) {
				result := signedResult()
				result.Bundle = oversized
				mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					Return(result, nil)
			},
			call: func(svc *service) error {
				_, err := svc.verifyGitInstall(t.Context(), plugins.InstallOptions{},
					"my-plugin", []byte("commit payload"), "signature")
				return err
			},
		},
		{
			name:    "oversized commit payload is refused before verification",
			arrange: func(*verifiermocks.MockVerifier) {},
			call: func(svc *service) error {
				_, err := svc.verifyGitInstall(t.Context(), plugins.InstallOptions{},
					"my-plugin", oversized, "signature")
				return err
			},
		},
		{
			name:    "oversized commit signature is refused before verification",
			arrange: func(*verifiermocks.MockVerifier) {},
			call: func(svc *service) error {
				_, err := svc.verifyGitInstall(t.Context(), plugins.InstallOptions{},
					"my-plugin", []byte("commit payload"), string(oversized))
				return err
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
			tc.arrange(mv)

			err := tc.call(&service{sigVerifier: mv})
			require.Error(t, err)
			assert.Equal(t, http.StatusUnprocessableEntity, httperr.Code(err))
			assert.Equal(t, plugins.FailureReasonValidationRejected, classifySyncFailure(err))
		})
	}
}

// TestInstallVerification_OversizedBundleIsNotStored proves the ceiling holds
// end to end: an over-limit bundle fails the install rather than reaching the
// DB or the lock file.
//
//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestInstallVerification_OversizedBundleIsNotStored(t *testing.T) {
	repoDir := createPluginTestRepo(t, "")
	result := signedResult()
	result.Bundle = make([]byte, maxSignatureBlobSize+1)

	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Nil()).
		Return(result, nil)

	svc, projectRoot := newGitLockTestService(t, repoDir, WithVerifier(mv))
	err := gitInstall(t, svc, projectRoot, nil)
	require.Error(t, err)
	assert.Equal(t, http.StatusUnprocessableEntity, httperr.Code(err))

	_, ok := loadPluginLockEntry(t, projectRoot)
	assert.False(t, ok, "an oversized bundle must not produce a lock entry")
	_, err = svc.Info(t.Context(), plugins.InfoOptions{
		Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	})
	require.Error(t, err, "an oversized bundle must not produce a DB record")
}

// TestClassifyInstallVerifyErrorNamesKeySigned pins the user-facing half of
// #6442: a key-signed artifact must be diagnosed as key-signed, must not be
// reported as a verification failure, and must say plainly that allow_unsigned
// is no remedy — the artifact is signed, so recording an unsigned exception
// would file a false trust decision in the lock.
func TestClassifyInstallVerifyErrorNamesKeySigned(t *testing.T) {
	t.Parallel()

	err := classifyInstallVerifyError(verifier.ErrKeySigned, "some-plugin", nil)
	assert.Contains(t, err.Error(), "cosign key pair")
	assert.Contains(t, err.Error(), "re-publish it with keyless signing",
		"the message must state the remedy, not merely the refusal")
	assert.Contains(t, err.Error(), "allow_unsigned does not apply")
	assert.NotContains(t, err.Error(), "signature verification failed for",
		"the generic invalid-signature wording is the misdiagnosis this replaces")
}

// TestClassifySignatureErrorNamesKeySigned keeps the sync/upgrade failure
// reason distinct from signature-invalid for the same reason.
func TestClassifySignatureErrorNamesKeySigned(t *testing.T) {
	t.Parallel()

	assert.Equal(t, plugins.FailureReasonKeySigned, classifySignatureError(verifier.ErrKeySigned))
	assert.Equal(t, plugins.FailureReasonSignatureInvalid,
		classifySignatureError(verifier.ErrSignatureInvalid),
		"the pre-existing mapping must be unaffected")
}

// TestIsAllowedUnsignedRejectsKeySigned is the guard that closes #6442's
// actual escape-hatch gap: --allow-unsigned must not rescue a key-signed
// artifact even on true first use with the flag explicitly set.
func TestIsAllowedUnsignedRejectsKeySigned(t *testing.T) {
	t.Parallel()

	assert.False(t, isAllowedUnsigned(verifier.ErrKeySigned,
		plugins.InstallOptions{AllowUnsigned: true}, nil),
		"a signed artifact must never be recordable as an unsigned exception")
	assert.True(t, isAllowedUnsigned(verifier.ErrUnsigned,
		plugins.InstallOptions{AllowUnsigned: true}, nil),
		"the genuine unsigned case must still be allowed through")
}

// TestInfoReportsLockTrustState covers the four states `thv ai-plugin info`
// renders: a verified signer, a provisional one, an explicit unsigned
// exception, and a plugin with no lock entry at all.
//
//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestInfoReportsLockTrustState(t *testing.T) {
	tests := []struct {
		name            string
		mutateEntry     func(*lockfile.Entry)
		removeEntry     bool
		wantProvenance  bool
		wantProvisional bool
		wantUnsigned    bool
	}{
		{name: "signed records the observed identity", wantProvenance: true},
		{
			name:            "provisional signature is marked",
			mutateEntry:     func(e *lockfile.Entry) { e.Provenance.Provisional = true },
			wantProvenance:  true,
			wantProvisional: true,
		},
		{
			name: "unsigned exception",
			mutateEntry: func(e *lockfile.Entry) {
				e.Provenance = nil
				e.Unsigned = true
			},
			wantUnsigned: true,
		},
		{name: "no lock entry reports nothing", removeEntry: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repoDir := createPluginTestRepo(t, "")
			svc, projectRoot := newGitLockTestService(t, repoDir)
			require.NoError(t, gitInstall(t, svc, projectRoot, nil))

			entry, ok := loadPluginLockEntry(t, projectRoot)
			require.True(t, ok)
			switch {
			case tc.removeEntry:
				require.NoError(t, lockfile.RemovePluginEntry(mustOpenRoot(t, projectRoot), "my-plugin"))
			case tc.mutateEntry != nil:
				tc.mutateEntry(&entry)
				lf := readLockfile(t, projectRoot)
				lf.UpsertPlugin(entry)
				require.NoError(t, lf.Save(mustOpenRoot(t, projectRoot)))
			}

			info, err := svc.Info(t.Context(), plugins.InfoOptions{
				Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
			})
			require.NoError(t, err)

			assert.Equal(t, tc.wantUnsigned, info.Unsigned)
			if !tc.wantProvenance {
				assert.Nil(t, info.Provenance)
				return
			}
			require.NotNil(t, info.Provenance)
			assert.Equal(t, testSignerIdentity, info.Provenance.SignerIdentity)
			assert.Equal(t, testCertIssuer, info.Provenance.CertIssuer)
			assert.Equal(t, tc.wantProvisional, info.Provenance.Provisional)
		})
	}
}

// TestInstallResultCarriesTrustDecision proves the decision install recorded
// is surfaced on the result, which is what the CLI prints after installing.
//
//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestInstallResultCarriesTrustDecision(t *testing.T) {
	t.Run("signed", func(t *testing.T) {
		repoDir := createPluginTestRepo(t, "")
		svc, projectRoot := newGitLockTestService(t, repoDir)

		result, err := svc.Install(t.Context(), plugins.InstallOptions{
			Name: gitPluginRef, Scope: plugins.ScopeProject,
			ProjectRoot: projectRoot, Clients: []string{"claude-code"},
		})
		require.NoError(t, err)
		require.NotNil(t, result.Provenance)
		assert.Equal(t, testSignerIdentity, result.Provenance.SignerIdentity)
		assert.False(t, result.Unsigned)
	})

	t.Run("unsigned exception", func(t *testing.T) {
		repoDir := createPluginTestRepo(t, "")
		mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
		mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Nil()).
			Return(nil, verifier.ErrUnsigned)
		svc, projectRoot := newGitLockTestService(t, repoDir, WithVerifier(mv))

		result, err := svc.Install(t.Context(), plugins.InstallOptions{
			Name: gitPluginRef, Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
			Clients: []string{"claude-code"}, AllowUnsigned: true,
		})
		require.NoError(t, err)
		assert.Nil(t, result.Provenance)
		assert.True(t, result.Unsigned)
	})
}

// TestDispatchExtractionPersistsNewlyVerifiedBundle covers the migration path
// that removing the lock feature gate opened up. A project install recorded
// while verification was gated off carries no Sigstore bundle, so a
// same-digest reinstall that now verifies must not short-circuit: the no-op
// and same-digest-new-clients paths both return the stored record verbatim
// without persisting, which would leave the lock entry naming a signer that
// verifyStoredSignature cannot re-verify offline (it fails closed on
// provenance with no stored bundle) and leave on-disk drift unrepaired.
//
//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestDispatchExtractionPersistsNewlyVerifiedBundle(t *testing.T) {
	const name = "my-plugin"

	tests := []struct {
		name string
		// storedBundle seeds the pre-existing record's bundle. Empty stands
		// in for a record written before lock tracking; non-empty for one
		// whose stored material has since gone stale or corrupt.
		storedBundle []byte
		bundle       []byte
		wantBundle   []byte
	}{
		{
			name:       "bundle to persist is not a no-op",
			bundle:     []byte(`{"bundle":true}`),
			wantBundle: []byte(`{"bundle":true}`),
		},
		{
			name:   "no bundle still short-circuits",
			bundle: nil,
		},
		{
			name:         "stale stored bundle is replaced",
			storedBundle: []byte(`{"bundle":"stale"}`),
			bundle:       []byte(`{"bundle":"fresh"}`),
			wantBundle:   []byte(`{"bundle":"fresh"}`),
		},
		{
			name:         "identical stored bundle needs no rewrite",
			storedBundle: []byte(`{"bundle":true}`),
			bundle:       []byte(`{"bundle":true}`),
			wantBundle:   []byte(`{"bundle":true}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, projectRoot := newLockTestService(t)
			inner := svc.(*service) //nolint:forcetypeassert
			digest := validLockDigest()

			// Stand in for a record written before lock tracking: right
			// digest and clients, no stored bundle unless seeded.
			installTestPlugin(t, svc, projectRoot, digest)
			existing, err := inner.store.Get(t.Context(), name, plugins.ScopeProject, projectRoot)
			require.NoError(t, err)
			require.Empty(t, existing.SigstoreBundle, "precondition: the pre-gate record stores no bundle")

			if len(tt.storedBundle) > 0 {
				existing.SigstoreBundle = tt.storedBundle
				require.NoError(t, inner.store.Update(t.Context(), existing))
			}

			result, err := inner.dispatchExtraction(t.Context(), plugins.InstallOptions{
				Name:           name,
				LayerData:      makePluginLayerData(t, name),
				Digest:         digest,
				Scope:          plugins.ScopeProject,
				ProjectRoot:    projectRoot,
				Clients:        []string{"claude-code"},
				SigstoreBundle: tt.bundle,
			}, plugins.ScopeProject, existing, nil, []string{"claude-code"})
			require.NoError(t, err)

			if len(tt.wantBundle) == 0 {
				assert.Empty(t, result.Plugin.SigstoreBundle)
				return
			}
			assert.Equal(t, tt.wantBundle, result.Plugin.SigstoreBundle,
				"the freshly verified bundle must reach the record, or offline sync fails closed")

			stored, err := inner.store.Get(t.Context(), name, plugins.ScopeProject, projectRoot)
			require.NoError(t, err)
			assert.Equal(t, tt.wantBundle, stored.SigstoreBundle,
				"the bundle must be persisted, not just returned")
		})
	}
}

// TestInfoPropagatesUnreadableLockTrustState pins that a lock file which
// exists but cannot be trusted surfaces as an error rather than as absent
// trust state: "no provenance and not unsigned" is exactly how an untracked
// install renders, so swallowing the read failure would leave CLI and JSON
// callers unable to tell a malformed toolhive.lock.yaml from a plugin nothing
// is pinning. A missing lock file stays non-fatal (lockfile.Load returns an
// empty lockfile), which the "no lock entry" case above covers.
//
//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestInfoPropagatesUnreadableLockTrustState(t *testing.T) {
	repoDir := createPluginTestRepo(t, "")
	svc, projectRoot := newGitLockTestService(t, repoDir)
	require.NoError(t, gitInstall(t, svc, projectRoot, nil))

	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, lockfile.FileName), []byte("plugins: [unterminated"), 0o644))

	_, err := svc.Info(t.Context(), plugins.InfoOptions{
		Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
	})
	require.Error(t, err, "an unreadable lock file must not render as an untracked install")
	assert.Contains(t, err.Error(), "reading lock trust state")
}

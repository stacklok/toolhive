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

// testPublicKeyB64 is a real P-256 public key in the base64 DER SPKI form the
// lock file stores, so validation and decoding exercise the real parser rather
// than a placeholder string.
const testPublicKeyB64 = "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAExlVDpbnOEv2fH3gS8n7UCHS9Gs0wKxIPR5EAcl8F1jSxlxAV/pll0NsSiuAK95Ws4Fpkn+5QkdVKNXy7LHgb2A=="

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
			// A lock-driven repair of an entry recording no trust decision
			// must NOT invent one. Converting "no decision" to
			// "unsigned: true" automatically is the implicit trust decision
			// the lock file exists to prevent, so this fails closed even
			// though no user flag reached the operation.
			name:    "lock-driven repair of an unrecorded entry fails closed",
			opts:    plugins.InstallOptions{ExpectedCanonicalName: "local-plugin"},
			wantErr: true,
		},
		{
			// The remedy: sync forwards --allow-unsigned, so the exception
			// can still be recorded — deliberately, by the user.
			name: "lock-driven repair records the exception with the flag",
			opts: plugins.InstallOptions{
				ExpectedCanonicalName: "local-plugin",
				SyncRestore:           true,
				AllowUnsigned:         true,
			},
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
	err := classifyInstallVerifyError(fieldMismatch, "some-plugin",
		&lockfile.Provenance{SignerIdentity: testSignerIdentity}, plugins.InstallOptions{})
	assert.Contains(t, err.Error(), "no longer matches its pinned provenance",
		"a provenance-field mismatch must lead with the field-specific wording, not the identity one")
	assert.NotContains(t, err.Error(), "signer identity mismatch for",
		"the identity-specific phrasing (distinct from ErrSignerMismatch's own wrapped message text) must not appear")

	identityMismatch := classifyInstallVerifyError(
		verifier.ErrSignerMismatch, "some-plugin",
		&lockfile.Provenance{SignerIdentity: testSignerIdentity}, plugins.InstallOptions{})
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

	err := classifyInstallVerifyError(verifier.ErrKeySigned, "some-plugin", nil, plugins.InstallOptions{})
	assert.Contains(t, err.Error(), "cosign key pair")
	assert.Contains(t, err.Error(), "--public-key",
		"a first install of a key-signed artifact is exactly what --public-key is for")
	assert.Contains(t, err.Error(), "allow_unsigned does not apply")
	assert.NotContains(t, err.Error(), "re-publish it with keyless signing",
		"republishing is no longer the remedy — the artifact is verifiable as signed")
	assert.NotContains(t, err.Error(), "signature verification failed for",
		"the generic invalid-signature wording is the misdiagnosis this replaces")
}

// TestClassifyInstallVerifyErrorKeySignedAgainstKeylessPin covers the arm
// where the obvious advice is wrong: the entry pins a certificate identity, so
// resolveKeyAnchor refuses a supplied key rather than letting it displace the
// pin. Telling this caller to pass --public-key would walk them into that
// conflict one step later, so the message has to name the pin and the fact
// that re-anchoring means removing the entry.
func TestClassifyInstallVerifyErrorKeySignedAgainstKeylessPin(t *testing.T) {
	t.Parallel()

	err := classifyInstallVerifyError(
		verifier.ErrKeySigned, "some-plugin",
		&lockfile.Provenance{SignerIdentity: testSignerIdentity}, plugins.InstallOptions{})
	require.Error(t, err)
	assert.Equal(t, http.StatusForbidden, httperr.Code(err))
	assert.Contains(t, err.Error(), testSignerIdentity,
		"the pinned identity is the reason the key is refused; naming it explains the refusal")
	assert.Contains(t, err.Error(), "Remove the lock entry and reinstall",
		"re-anchoring is deliberately not offered in place — say what does work")
	assert.Contains(t, err.Error(), "allow_unsigned does not apply")

	// The supplied key really is refused against a certificate pin, so the
	// message is not merely cautious wording.
	_, anchorErr := resolveKeyAnchor(
		plugins.InstallOptions{PublicKey: testPublicKeyB64}, "some-plugin",
		&lockfile.Provenance{SignerIdentity: testSignerIdentity}, false)
	require.Error(t, anchorErr)
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

// TestInfoReportsLockTrustState covers the five states `thv ai-plugin info`
// renders: a verified signer, a provisional one, an explicit unsigned
// exception, a lock-managed entry recording no trust decision at all, and a
// plugin with no lock entry. The last two must stay distinguishable — both
// leave Provenance nil and Unsigned false, but one is drift sync can repair
// and the other is simply untracked.
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
		wantUnrecorded  bool
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
		{
			// A pre-verification entry: neither field set. sync calls this
			// drift, so info must not render it as untracked.
			name: "entry with no trust decision is reported as unrecorded",
			mutateEntry: func(e *lockfile.Entry) {
				e.Provenance = nil
				e.Unsigned = false
			},
			wantUnrecorded: true,
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
			assert.Equal(t, tc.wantUnrecorded, info.TrustUnrecorded)
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

// gitSignedVerifier mirrors what the real VerifyGit returns: a verified
// identity with a NIL bundle. gitsign material is not a Sigstore bundle and
// the transparency-log proof that would let us build one is a tracked
// follow-up, so git verification has nothing to hand back.
//
// That nil matters for the test below rather than being incidental detail:
// alwaysSignedVerifier returns a bundle, which would make dispatchExtraction's
// mustPersistTrust fire and route the reinstall to the rematerializing path
// for a reason no git install ever has. A test built on it would pass while
// exercising nothing.
func gitSignedVerifier(t *testing.T) verifier.Verifier {
	t.Helper()
	result := signedResult()
	result.Bundle = nil
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().Return(result, nil)
	mv.EXPECT().VerifyBundleOffline(gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().Return(nil)
	return mv
}

// TestInstallVerification_SameCommitGitMigrationRematerializes pins the
// invariant that a lock entry only ever describes content the install that
// wrote it actually materialized.
//
// The shape that broke it: a project install predating lock tracking, so the
// store record exists, is unmanaged, and carries no bundle — and whose on-disk
// tree has been modified since. Reinstalling at the same commit verifies the
// fresh gitsign signature and writes an entry naming that signer plus a
// contentDigest hashed from the freshly downloaded source, never from disk.
// With no bundle to persist there was nothing to pull the install off the
// no-op path, so the modified files stayed active behind an entry that reads
// as fully verified, and only a later `sync --check` noticed the mismatch.
//
//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestInstallVerification_SameCommitGitMigrationRematerializes(t *testing.T) {
	const name = "my-plugin"

	repoDir := createPluginTestRepo(t, "")
	svc, projectRoot := newGitLockTestService(t, repoDir, WithVerifier(gitSignedVerifier(t)))
	require.NoError(t, gitInstall(t, svc, projectRoot, nil))
	inner := svc.(*service) //nolint:forcetypeassert

	// Reduce the install to the pre-verification shape: no lock entry and an
	// unmanaged record. Nothing writes this today; it is what a project
	// install made while lock tracking was gated off looks like.
	require.NoError(t, lockfile.RemovePluginEntry(mustOpenRoot(t, projectRoot), name))
	legacy, err := inner.store.Get(t.Context(), name, plugins.ScopeProject, projectRoot)
	require.NoError(t, err)
	legacy.Managed = false
	legacy.SigstoreBundle = nil
	require.NoError(t, inner.store.Update(t.Context(), legacy))

	// Drift: the tree on disk no longer matches the commit it came from.
	tampered := filepath.Join(pluginOnDiskPath(projectRoot, name), "commands", "hello.md")
	require.NoError(t, os.WriteFile(tampered, []byte("tampered content"), 0o644))

	// Reinstall at the same commit, every client already present — the no-op
	// path, and with a nil bundle nothing else would divert it.
	require.NoError(t, gitInstall(t, svc, projectRoot, nil))

	entry, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)
	require.NotNil(t, entry.Provenance, "precondition: the reinstall recorded a verified signer")
	assert.Equal(t, testSignerIdentity, entry.Provenance.SignerIdentity)

	restored, err := os.ReadFile(tampered) //nolint:gosec // test-controlled path
	require.NoError(t, err)
	assert.Equal(t, "# hello", string(restored),
		"an install recording verified provenance must rematerialize the content it vouches for")

	// The contentDigest now describes what is on disk, so the entry is
	// self-consistent at the moment it is written rather than only after a
	// repair pass.
	checked, err := inner.Sync(t.Context(), plugins.SyncOptions{ProjectRoot: projectRoot, Check: true})
	require.NoError(t, err)
	assert.Equal(t, []string{name}, checked.AlreadyCurrent)
	assert.Empty(t, checked.Drifted)
}

// keyedLockEntry is a lock entry pinned to testPublicKeyB64 — the shape a
// previous key-verified install leaves behind.
func keyedLockEntry(name string) lockfile.Entry {
	return lockfile.Entry{
		Name:              name,
		Source:            "example.com/org/" + name,
		ResolvedReference: "example.com/org/" + name + ":v1",
		Digest:            "sha256:" + strings.Repeat("b", 64),
		Provenance:        &lockfile.Provenance{PublicKey: testPublicKeyB64},
	}
}

// TestValidateInstallPublicKey covers the entry guard: a key that this install
// could never use is bad input, reported before any resolve or fetch rather
// than as a verification failure afterwards.
func TestValidateInstallPublicKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		opts    plugins.InstallOptions
		scope   plugins.Scope
		wantMsg string
	}{
		{
			name:  "no key is always fine",
			opts:  plugins.InstallOptions{},
			scope: plugins.ScopeUser,
		},
		{
			name:  "project scope with a valid key",
			opts:  plugins.InstallOptions{PublicKey: testPublicKeyB64, ProjectRoot: "/tmp/project"},
			scope: plugins.ScopeProject,
		},
		{
			// User-scope installs are not lock-managed, so verification never
			// runs and the key would be accepted and then dropped.
			name:    "user scope rejects a key it would never use",
			opts:    plugins.InstallOptions{PublicKey: testPublicKeyB64},
			scope:   plugins.ScopeUser,
			wantMsg: "applies to project-scoped installs",
		},
		{
			name:    "project scope without a root rejects a key",
			opts:    plugins.InstallOptions{PublicKey: testPublicKeyB64},
			scope:   plugins.ScopeProject,
			wantMsg: "applies to project-scoped installs",
		},
		{
			name:    "malformed base64 rejected",
			opts:    plugins.InstallOptions{PublicKey: "not!base64", ProjectRoot: "/tmp/project"},
			scope:   plugins.ScopeProject,
			wantMsg: "not valid base64",
		},
		{
			// Well-encoded is not well-formed. This value decodes cleanly and
			// is not a key, which is exactly the input that would otherwise
			// fail deep inside verification with the lock file as the suspect.
			name:    "valid base64 that is not a public key rejected",
			opts:    plugins.InstallOptions{PublicKey: "aGVsbG8gd29ybGQ=", ProjectRoot: "/tmp/project"},
			scope:   plugins.ScopeProject,
			wantMsg: "not a DER SPKI public key",
		},
		{
			name: "oversized key rejected before decoding",
			opts: plugins.InstallOptions{
				PublicKey:   strings.Repeat("A", lockfile.MaxEncodedPublicKeyLength+1),
				ProjectRoot: "/tmp/project",
			},
			scope:   plugins.ScopeProject,
			wantMsg: "exceeding the",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateInstallPublicKey(tc.opts, tc.scope)
			if tc.wantMsg == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, http.StatusBadRequest, httperr.Code(err),
				"a key this install cannot use is bad input, not a policy refusal")
			assert.Contains(t, err.Error(), tc.wantMsg)
		})
	}
}

// TestInstallRejectsMalformedPublicKeyBeforeFetching pins where the guard sits.
// The verifier mock carries no expectations, so any verification call fails the
// test, and a 400 (rather than a clone or pull failure) shows the key was
// judged as input before the artifact was touched at all.
//
//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestInstallRejectsMalformedPublicKeyBeforeFetching(t *testing.T) {
	repoDir := createPluginTestRepo(t, "")
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))

	svc, projectRoot := newGitLockTestService(t, repoDir, WithVerifier(mv))
	err := gitInstall(t, svc, projectRoot, func(o *plugins.InstallOptions) { o.PublicKey = "not!base64" })

	require.Error(t, err)
	assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
	assert.Contains(t, err.Error(), "public_key")
	_, ok := loadPluginLockEntry(t, projectRoot)
	assert.False(t, ok, "a rejected install must not write a lock entry")
}

// TestVerifyOCIInstall_KeyPathDispatch covers the two ways the key path is
// reached and the fact that reaching it excludes the keyless one. Dispatch is
// lock-first by design: were the artifact allowed to select the policy, a
// republished key-signed artifact could walk an entry out of the certificate
// identity it is pinned to.
func TestVerifyOCIInstall_KeyPathDispatch(t *testing.T) {
	t.Parallel()

	keyPEM, err := verifier.DecodePublicKey(testPublicKeyB64)
	require.NoError(t, err)

	tests := []struct {
		name   string
		locked bool
		opts   plugins.InstallOptions
	}{
		{
			name: "first use verifies against the supplied key",
			opts: plugins.InstallOptions{PublicKey: testPublicKeyB64},
		},
		{
			name:   "pinned entry verifies against the locked key with no flag",
			locked: true,
		},
		{
			name:   "supplied key that agrees with the pin is accepted",
			locked: true,
			opts:   plugins.InstallOptions{PublicKey: testPublicKeyB64},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			projectRoot := makeProjectRoot(t)
			if tc.locked {
				require.NoError(t, lockfile.UpsertPluginEntry(
					mustOpenRoot(t, projectRoot), keyedLockEntry("keyed-plugin")))
			}
			mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
			mv.EXPECT().VerifyOCIWithKey(
				gomock.Any(), gomock.Any(), gomock.Any(), gomock.Eq(keyPEM)).
				Return(&verifier.Result{Signed: true, Bundle: []byte(`{"bundle":true}`)}, nil)

			opts := tc.opts
			opts.ProjectRoot = projectRoot
			svc := newTestService(WithVerifier(mv))
			decision, err := svc.verifyOCIInstall(
				t.Context(), opts, "keyed-plugin", "example.com/org/keyed-plugin:v1",
				"sha256:"+strings.Repeat("b", 64))

			require.NoError(t, err)
			// The key is recorded, and nothing else is: a key-pair bundle
			// carries no certificate, so there is no identity to observe and
			// inventing one would file provenance the artifact never asserted.
			assert.Equal(t, &lockfile.Provenance{PublicKey: testPublicKeyB64}, decision.provenance)
			assert.Equal(t, []byte(`{"bundle":true}`), decision.bundle,
				"the bundle must be captured for offline re-verification")
			assert.False(t, decision.unsigned)
		})
	}
}

// TestVerifyOCIInstall_SuppliedKeyConflictsAreRefused runs the conflicts
// through the real dispatch rather than through resolveKeyAnchor alone. The
// verifier mock has no expectations, so a conflict that was silently ignored
// instead of refused — the failure mode a mistyped --public-key produces —
// would show up as an unexpected keyless verification call.
func TestVerifyOCIInstall_SuppliedKeyConflictsAreRefused(t *testing.T) {
	t.Parallel()

	identityEntry := keyedLockEntry("conflicted-plugin")
	identityEntry.Provenance = &lockfile.Provenance{
		SignerIdentity: testSignerIdentity,
		CertIssuer:     testCertIssuer,
	}
	unsignedEntry := keyedLockEntry("conflicted-plugin")
	unsignedEntry.Provenance = nil
	unsignedEntry.Unsigned = true

	tests := []struct {
		name    string
		entry   lockfile.Entry
		wantMsg string
	}{
		{
			name:    "keyless-pinned entry refuses a supplied key",
			entry:   identityEntry,
			wantMsg: "carries no certificate identity",
		},
		{
			name:    "unsigned exception cannot be upgraded by a key",
			entry:   unsignedEntry,
			wantMsg: "unsigned exception",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			projectRoot := makeProjectRoot(t)
			require.NoError(t, lockfile.UpsertPluginEntry(mustOpenRoot(t, projectRoot), tc.entry))
			svc := newTestService(WithVerifier(verifiermocks.NewMockVerifier(gomock.NewController(t))))

			_, err := svc.verifyOCIInstall(
				t.Context(),
				plugins.InstallOptions{ProjectRoot: projectRoot, PublicKey: testPublicKeyB64},
				"conflicted-plugin", "example.com/org/conflicted-plugin:v1",
				"sha256:"+strings.Repeat("b", 64))

			require.Error(t, err)
			assert.Equal(t, http.StatusForbidden, httperr.Code(err))
			assert.Contains(t, err.Error(), tc.wantMsg)
		})
	}
}

// TestResolveKeyAnchor pins the conflict rules. Every disagreement between a
// supplied key and the recorded trust state is an error rather than a
// precedence rule, because silently preferring either one is how a mistyped
// --public-key installs as though it had been honored.
func TestResolveKeyAnchor(t *testing.T) {
	t.Parallel()

	const otherKeyB64 = "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEZ7Bd5Kk7GAOI1PoQFvY6Sw+9zL3fVX" +
		"Bqz0mAo0hVW1nQz4Vv9pQmT2yqXqL7NqRk5FvPQZ8DdcW0xTn3Yg6ZBw=="
	identityPin := &lockfile.Provenance{SignerIdentity: testSignerIdentity, CertIssuer: testCertIssuer}
	keyPin := &lockfile.Provenance{PublicKey: testPublicKeyB64}

	tests := []struct {
		name           string
		opts           plugins.InstallOptions
		expected       *lockfile.Provenance
		expectUnsigned bool
		want           string
		wantCode       int
		wantMsg        string
	}{
		{name: "nothing supplied, nothing pinned: keyless"},
		{name: "identity pin, no key: keyless", expected: identityPin},
		{name: "key pin selects the locked key", expected: keyPin, want: testPublicKeyB64},
		{
			name:     "supplied key confirms the locked key",
			opts:     plugins.InstallOptions{PublicKey: testPublicKeyB64},
			expected: keyPin,
			want:     testPublicKeyB64,
		},
		{
			name:     "supplied key that differs from the pin is refused",
			opts:     plugins.InstallOptions{PublicKey: otherKeyB64},
			expected: keyPin,
			wantCode: http.StatusForbidden,
			wantMsg:  "pinned to a different cosign public key",
		},
		{
			name:     "supplied key against an identity pin is refused",
			opts:     plugins.InstallOptions{PublicKey: testPublicKeyB64},
			expected: identityPin,
			wantCode: http.StatusForbidden,
			wantMsg:  "carries no certificate identity",
		},
		{
			name:           "supplied key cannot upgrade a recorded unsigned exception",
			opts:           plugins.InstallOptions{PublicKey: testPublicKeyB64},
			expectUnsigned: true,
			wantCode:       http.StatusForbidden,
			wantMsg:        "unsigned exception",
		},
		{
			name: "first use adopts the supplied key",
			opts: plugins.InstallOptions{PublicKey: testPublicKeyB64},
			want: testPublicKeyB64,
		},
		{
			// The override re-records whatever it observes, and a key-pair
			// bundle offers nothing to observe — so honoring a key here would
			// re-anchor on the caller's say-so alone. v1 has no such path.
			name:     "allow_signer_change with a key is refused",
			opts:     plugins.InstallOptions{PublicKey: testPublicKeyB64, AllowSignerChange: true},
			expected: keyPin,
			wantCode: http.StatusBadRequest,
			wantMsg:  "cannot be combined with allow_signer_change",
		},
		{
			// key -> keyless is the one supported transition: the candidate is
			// chain-verifiable, so the override drops the pinned key and lets
			// the keyless path record what it observes.
			name:     "allow_signer_change without a key drops the pinned key",
			opts:     plugins.InstallOptions{AllowSignerChange: true},
			expected: keyPin,
			want:     "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := resolveKeyAnchor(tc.opts, "some-plugin", tc.expected, tc.expectUnsigned)
			if tc.wantCode != 0 {
				require.Error(t, err)
				assert.Equal(t, tc.wantCode, httperr.Code(err))
				assert.Contains(t, err.Error(), tc.wantMsg)
				assert.Empty(t, got, "a refused anchor must not also be returned")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestClassifyKeyVerifyError pins the diagnoses the key path reports. None of
// them mention allow_unsigned: an install that named a public key asked for
// that key to be enforced, and the unsigned exception answers a different
// question entirely.
func TestClassifyKeyVerifyError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		err     error
		wantMsg string
	}{
		{
			name:    "unsigned artifact",
			err:     verifier.ErrUnsigned,
			wantMsg: "carries no signature material at all",
		},
		{
			// The likeliest mistake: a key aimed at an artifact that was
			// signed keylessly. The remedy is to drop the key, which a bare
			// "verification failed" would never suggest.
			name:    "keyless artifact names the right remedy",
			err:     verifier.ErrKeylessSigned,
			wantMsg: "install it without a public key",
		},
		{
			// Wrong key and damaged signature are genuinely indistinguishable:
			// the bundle records no key of its own to compare against.
			name:    "verification failure names both possible causes",
			err:     verifier.ErrSignatureInvalid,
			wantMsg: "either the key is not the one that signed it, or the signature is damaged",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := classifyKeyVerifyError(tc.err, "keyed-plugin")
			require.Error(t, err)
			assert.Equal(t, http.StatusForbidden, httperr.Code(err))
			assert.Contains(t, err.Error(), tc.wantMsg)
			assert.Contains(t, err.Error(), "plugin")
			assert.NotContains(t, err.Error(), "allow_unsigned")
		})
	}
}

// TestVerifyGitInstall_RefusesPublicKey guards the git side of the same rule
// the lock file enforces on key-pinned git entries: a commit signature is made
// with a Fulcio certificate, so a public key has no operation to take part in.
func TestVerifyGitInstall_RefusesPublicKey(t *testing.T) {
	t.Parallel()

	svc := newTestService(WithVerifier(verifiermocks.NewMockVerifier(gomock.NewController(t))))
	_, err := svc.verifyGitInstall(
		t.Context(),
		plugins.InstallOptions{ProjectRoot: makeProjectRoot(t), PublicKey: testPublicKeyB64},
		"git-plugin", []byte("payload"), "signature")

	require.Error(t, err)
	assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
	assert.Contains(t, err.Error(), "a cosign public key cannot verify it")
}

// TestVerifyLocalInstall_RefusesPublicKey covers the local-build side: there is
// no registry signature material for a key to check, so the key is refused
// rather than accepted and then recorded as an unsigned install anyway.
func TestVerifyLocalInstall_RefusesPublicKey(t *testing.T) {
	t.Parallel()

	_, err := verifyLocalInstall(plugins.InstallOptions{
		ProjectRoot: makeProjectRoot(t),
		PublicKey:   testPublicKeyB64,
		// Set so the refusal cannot be mistaken for the ordinary
		// unsigned-needs-a-flag rejection.
		AllowUnsigned: true,
	}, "local-plugin")

	require.Error(t, err)
	assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
	assert.Contains(t, err.Error(), "carries no registry signature")
}

// TestVerifyLocalInstall_KeyPinnedEntryRendersAnchor pins the message a
// key-pinned entry produces when a local build tries to replace it. Naming the
// (empty) signer identity there would print `locked to signer ""` and read as a
// corrupt lock file rather than as a refusal.
func TestVerifyLocalInstall_KeyPinnedEntryRendersAnchor(t *testing.T) {
	t.Parallel()

	projectRoot := makeProjectRoot(t)
	require.NoError(t, lockfile.UpsertPluginEntry(
		mustOpenRoot(t, projectRoot), keyedLockEntry("local-plugin")))

	_, err := verifyLocalInstall(plugins.InstallOptions{
		ProjectRoot:   projectRoot,
		AllowUnsigned: true,
	}, "local-plugin")

	require.Error(t, err)
	assert.Equal(t, http.StatusForbidden, httperr.Code(err))
	assert.Contains(t, err.Error(), "locked to a cosign public key")
	assert.NotContains(t, err.Error(), `signer ""`)
}

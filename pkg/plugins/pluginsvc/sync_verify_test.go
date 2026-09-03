// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	"github.com/stacklok/toolhive/pkg/skills/verifier"
	verifiermocks "github.com/stacklok/toolhive/pkg/skills/verifier/mocks"
)

// TestSync_StoredSignatureFailureIsDriftThenHeals proves the offline
// re-verification path: a stored bundle that no longer verifies reports as
// drift in check mode, and an apply reinstalls from the pinned reference —
// where install-time verification enforces the locked identity and heals the
// stored state.
//
//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestSync_StoredSignatureFailureIsDriftThenHeals(t *testing.T) {
	repoDir := createPluginTestRepo(t, "")

	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().Return(signedResult(), nil)
	// Every offline re-verification of the stored bundle fails.
	mv.EXPECT().VerifyBundleOffline(gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().Return(verifier.ErrSignatureInvalid)

	svc, projectRoot := newGitLockTestService(t, repoDir, WithVerifier(mv))
	require.NoError(t, gitInstall(t, svc, projectRoot, nil))

	syncer := svc.(*service) //nolint:forcetypeassert

	result, err := syncer.Sync(t.Context(), plugins.SyncOptions{ProjectRoot: projectRoot, Check: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"my-plugin"}, result.Drifted,
		"a failed offline re-verification must report as drift in check mode")
	assert.Empty(t, result.AlreadyCurrent)

	result, err = syncer.Sync(t.Context(), plugins.SyncOptions{ProjectRoot: projectRoot})
	require.NoError(t, err)
	assert.Equal(t, []string{"my-plugin"}, result.Installed,
		"apply mode must reinstall from the pinned reference, re-verifying the artifact")
	assert.Empty(t, result.Failed)
}

func TestVerifyStoredSignature(t *testing.T) {
	t.Parallel()

	provenance := &lockfile.Provenance{
		SignerIdentity: testSignerIdentity,
		CertIssuer:     testCertIssuer,
	}
	ociDigest := "sha256:" + strings.Repeat("a", 64)
	gitDigest := strings.Repeat("b", 40)

	tests := []struct {
		name       string
		entry      lockfile.Entry
		bundle     []byte
		offlineErr error
		expectCall bool
		wantErr    bool
	}{
		{
			name:  "unsigned entry has nothing to verify",
			entry: lockfile.Entry{Unsigned: true, Digest: ociDigest},
		},
		{
			// A pre-verification entry: written while verification was gated
			// off, so it carries no trust decision at all. Reported as drift
			// so sync reinstalls and records one, instead of passing as
			// AlreadyCurrent with nothing ever verified.
			name:    "entry with no trust decision is drift",
			entry:   lockfile.Entry{Digest: ociDigest},
			wantErr: true,
		},
		{
			// Same shape on a git pin: still no decision, still drift.
			name:    "entry with no trust decision is drift for git",
			entry:   lockfile.Entry{Digest: gitDigest},
			wantErr: true,
		},
		{
			name:    "provenance without stored bundle fails closed for OCI",
			entry:   lockfile.Entry{Provenance: provenance, Digest: ociDigest},
			wantErr: true,
		},
		{
			name:  "provenance without stored bundle is fine for git",
			entry: lockfile.Entry{Provenance: provenance, Digest: gitDigest},
		},
		{
			name:       "stored bundle delegates to offline verification",
			entry:      lockfile.Entry{Provenance: provenance, Digest: ociDigest},
			bundle:     []byte(`{"bundle":true}`),
			expectCall: true,
		},
		{
			name:       "offline verification failure propagates",
			entry:      lockfile.Entry{Provenance: provenance, Digest: ociDigest},
			bundle:     []byte(`{"bundle":true}`),
			offlineErr: verifier.ErrSignatureInvalid,
			expectCall: true,
			wantErr:    true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
			if tc.expectCall {
				mv.EXPECT().VerifyBundleOffline(tc.bundle, tc.entry.Digest, tc.entry.Provenance).
					Return(tc.offlineErr)
			}
			svc := &service{sigVerifier: mv}
			err := svc.verifyStoredSignature(tc.entry, plugins.InstalledPlugin{SigstoreBundle: tc.bundle})
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestVerifyStoredSignature_MissingBundleClassifiesAsSignatureInvalid pins the
// typed reason a fail-closed missing bundle produces: sync reports it through
// the same FailureReasonSignatureInvalid channel as a tampered one.
func TestVerifyStoredSignature_MissingBundleClassifiesAsSignatureInvalid(t *testing.T) {
	t.Parallel()

	svc := &service{sigVerifier: verifiermocks.NewMockVerifier(gomock.NewController(t))}
	err := svc.verifyStoredSignature(lockfile.Entry{
		Provenance: &lockfile.Provenance{SignerIdentity: testSignerIdentity},
		Digest:     "sha256:" + strings.Repeat("a", 64),
	}, plugins.InstalledPlugin{})
	require.Error(t, err)
	assert.Equal(t, plugins.FailureReasonSignatureInvalid, classifySyncFailure(err))
}

// TestSync_AdoptBackFillsProvenanceFromStoredBundle proves adoption is a trust
// decision made from evidence: a stored bundle back-fills the identity into
// the lock entry, so the adopted entry is signed rather than an unsigned
// exception.
//
//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestSync_AdoptBackFillsProvenanceFromStoredBundle(t *testing.T) {
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().ResultFromBundle([]byte(`{"bundle":true}`), gomock.Any()).
		Return(signedResult(), nil)
	mv.EXPECT().VerifyBundleOffline(gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().Return(nil)

	svc, projectRoot := newLockTestService(t, WithVerifier(mv))
	installTestPlugin(t, svc, projectRoot, validLockDigest())

	require.NoError(t, lockfile.RemovePluginEntry(mustOpenRoot(t, projectRoot), "my-plugin"))
	syncSvc := svc.(*service) //nolint:forcetypeassert
	legacy, err := syncSvc.store.Get(t.Context(), "my-plugin", plugins.ScopeProject, projectRoot)
	require.NoError(t, err)
	legacy.Managed = false
	legacy.Reference = "ghcr.io/org/my-plugin:v1"
	legacy.SigstoreBundle = []byte(`{"bundle":true}`)
	require.NoError(t, syncSvc.store.Update(t.Context(), legacy))

	// No AllowUnsigned: the stored bundle is the evidence adoption needs.
	result, err := syncSvc.Sync(t.Context(), plugins.SyncOptions{ProjectRoot: projectRoot, Adopt: true})
	require.NoError(t, err)
	assert.Empty(t, result.Failed)

	entry, ok := readLockfile(t, projectRoot).GetPlugin("my-plugin")
	require.True(t, ok)
	require.NotNil(t, entry.Provenance, "adoption must back-fill provenance from the stored bundle")
	assert.Equal(t, testSignerIdentity, entry.Provenance.SignerIdentity)
	assert.Equal(t, testCertIssuer, entry.Provenance.CertIssuer)
	assert.False(t, entry.Unsigned)
}

// TestSync_AdoptRejectsUnverifiableStoredBundle covers the third adoption
// outcome: a bundle exists but does not verify, which is a failure rather
// than a silent fall-through to the unsigned path.
//
//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestSync_AdoptRejectsUnverifiableStoredBundle(t *testing.T) {
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().ResultFromBundle(gomock.Any(), gomock.Any()).
		Return(nil, verifier.ErrSignatureInvalid)
	mv.EXPECT().VerifyBundleOffline(gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().Return(nil)

	svc, projectRoot := newLockTestService(t, WithVerifier(mv))
	installTestPlugin(t, svc, projectRoot, validLockDigest())

	require.NoError(t, lockfile.RemovePluginEntry(mustOpenRoot(t, projectRoot), "my-plugin"))
	syncSvc := svc.(*service) //nolint:forcetypeassert
	legacy, err := syncSvc.store.Get(t.Context(), "my-plugin", plugins.ScopeProject, projectRoot)
	require.NoError(t, err)
	legacy.Managed = false
	legacy.Reference = "ghcr.io/org/my-plugin:v1"
	legacy.SigstoreBundle = []byte(`{"bundle":true}`)
	require.NoError(t, syncSvc.store.Update(t.Context(), legacy))

	result, err := syncSvc.Sync(t.Context(), plugins.SyncOptions{
		ProjectRoot: projectRoot, Adopt: true, AllowUnsigned: true,
	})
	require.NoError(t, err)
	require.Len(t, result.Failed, 1)
	assert.Equal(t, plugins.FailureReasonSignatureInvalid, result.Failed[0].Reason)

	_, ok := readLockfile(t, projectRoot).GetPlugin("my-plugin")
	assert.False(t, ok, "an unverifiable stored bundle must not produce a lock entry")
}

// alwaysUnsignedVerifier reports every artifact as carrying no signature
// material, so a reinstall of real fixture content exercises the unsigned
// decision path rather than failing earlier for want of content.
func alwaysUnsignedVerifier(t *testing.T) verifier.Verifier {
	t.Helper()
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().Return(nil, verifier.ErrUnsigned)
	mv.EXPECT().VerifyOCI(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().Return(nil, verifier.ErrUnsigned)
	mv.EXPECT().VerifyBundleOffline(gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().Return(nil)
	return mv
}

// TestSyncMigratesUnrecordedTrustEntry walks the whole migration this PR
// introduces, because the interesting property is not any single decision but
// the sequence: an entry recording no trust decision must be visible as drift,
// must NOT be repaired into an unsigned exception on its own, and must be
// repairable once the user asks for it.
//
// Step 2 is the one worth pinning. Reporting the entry as drift makes sync
// reinstall it, and if that reinstall accepted unsigned content it would
// rewrite "no decision" into "unsigned: true" with nobody having chosen it —
// trading a visibly ambiguous entry for a silently fabricated exception. The
// git fixture is used deliberately: its content is genuinely reinstallable, so
// a failure here is the trust refusal and not a missing-content error standing
// in for one.
//
//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestSyncMigratesUnrecordedTrustEntry(t *testing.T) {
	const name = "my-plugin"

	repoDir := createPluginTestRepo(t, "")
	svc, projectRoot := newGitLockTestService(t, repoDir, WithVerifier(alwaysUnsignedVerifier(t)))
	require.NoError(t, gitInstall(t, svc, projectRoot, func(o *plugins.InstallOptions) {
		o.AllowUnsigned = true
	}))

	// Rewrite the entry into the pre-verification shape: no provenance and no
	// unsigned exception. Nothing writes this today; it is what entries
	// created while verification was gated off look like.
	entry, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)
	require.True(t, entry.Unsigned, "precondition: the install recorded an exception")
	entry.Provenance = nil
	entry.Unsigned = false
	lf := readLockfile(t, projectRoot)
	lf.UpsertPlugin(entry)
	require.NoError(t, lf.Save(mustOpenRoot(t, projectRoot)))

	inner := svc.(*service) //nolint:forcetypeassert

	// 1. Visible as drift, and --check records nothing.
	checked, err := inner.Sync(t.Context(), plugins.SyncOptions{ProjectRoot: projectRoot, Check: true})
	require.NoError(t, err)
	assert.Equal(t, []string{name}, checked.Drifted, "an entry with no trust decision must not pass as current")
	assert.Empty(t, checked.Installed)

	after, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)
	assert.False(t, after.Unsigned, "--check must not record a decision")

	// 2. Repair without consent fails closed rather than inventing one.
	repaired, err := inner.Sync(t.Context(), plugins.SyncOptions{ProjectRoot: projectRoot})
	require.NoError(t, err)
	assert.Empty(t, repaired.Installed, "unsigned content must not be silently adopted")
	require.Len(t, repaired.Failed, 1)
	assert.Equal(t, name, repaired.Failed[0].Name)
	assert.Equal(t, plugins.FailureReasonUnsignedRejected, repaired.Failed[0].Reason,
		"the refusal must be the unsigned trust decision, not an incidental failure")

	stillUnrecorded, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)
	assert.Nil(t, stillUnrecorded.Provenance)
	assert.False(t, stillUnrecorded.Unsigned,
		"a failed repair must leave the entry unrecorded, not convert it to an exception")

	// 3. With the explicit exception, the migration completes.
	accepted, err := inner.Sync(t.Context(), plugins.SyncOptions{
		ProjectRoot: projectRoot, AllowUnsigned: true,
	})
	require.NoError(t, err)
	assert.Empty(t, accepted.Failed)
	assert.Equal(t, []string{name}, accepted.Installed)

	migrated, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)
	assert.True(t, migrated.Unsigned, "the exception the user asked for must be recorded")
	assert.Nil(t, migrated.Provenance)
}

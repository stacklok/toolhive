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
//nolint:paralleltest // uses t.Setenv via newGitLockTestService
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
			name:  "no provenance has nothing to verify",
			entry: lockfile.Entry{Digest: ociDigest},
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
//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestSync_AdoptBackFillsProvenanceFromStoredBundle(t *testing.T) {
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().ResultFromBundle([]byte(`{"bundle":true}`), gomock.Any()).
		Return(signedResult(), nil)
	mv.EXPECT().VerifyBundleOffline(gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().Return(nil)

	svc, projectRoot := newLockTestService(t, true, WithVerifier(mv))
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
//nolint:paralleltest // uses t.Setenv via newLockTestService
func TestSync_AdoptRejectsUnverifiableStoredBundle(t *testing.T) {
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().ResultFromBundle(gomock.Any(), gomock.Any()).
		Return(nil, verifier.ErrSignatureInvalid)
	mv.EXPECT().VerifyBundleOffline(gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().Return(nil)

	svc, projectRoot := newLockTestService(t, true, WithVerifier(mv))
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

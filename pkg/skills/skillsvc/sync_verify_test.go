// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	"github.com/stacklok/toolhive/pkg/skills/verifier"
	verifiermocks "github.com/stacklok/toolhive/pkg/skills/verifier/mocks"
)

// TestSync_StoredSignatureFailureIsDriftThenHeals proves the offline
// re-verification path: a stored bundle that no longer verifies reports as
// drift in check mode, and an apply reinstalls from the pinned reference —
// where install-time verification enforces the locked identity and heals
// the stored state.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestSync_StoredSignatureFailureIsDriftThenHeals(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	fx.register("sig-drift-skill", gitSkill("sig-drift-skill"))

	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().Return(signedResult(), nil)
	// Every offline re-verification of the stored bundle fails.
	mv.EXPECT().VerifyBundleOffline(gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().Return(verifier.ErrSignatureInvalid)

	svc, projectRoot := newLockTestService(t, gr, WithVerifier(mv))
	ref, _ := gitRef("sig-drift-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	syncer := svc.(*service) //nolint:forcetypeassert

	result, err := syncer.Sync(t.Context(), skills.SyncOptions{ProjectRoot: projectRoot, Check: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"sig-drift-skill"}, result.Drifted,
		"a failed offline re-verification must report as drift in check mode")
	assert.Empty(t, result.AlreadyCurrent)

	result, err = syncer.Sync(t.Context(), skills.SyncOptions{ProjectRoot: projectRoot})
	require.NoError(t, err)
	assert.Equal(t, []string{"sig-drift-skill"}, result.Installed,
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
			err := svc.verifyStoredSignature(tc.entry, skills.InstalledSkill{SigstoreBundle: tc.bundle})
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestVerifyStoredSignature_KeyPinnedEntry covers the branch that keeps a
// key-pinned project from reporting drift forever. The keyless path refuses a
// key-pinned entry outright, and sync reads a refusal as drift it can heal by
// reinstalling — so before this branch existed the skill was reported modified
// on every run and --check failed permanently on an intact project.
func TestVerifyStoredSignature_KeyPinnedEntry(t *testing.T) {
	t.Parallel()

	keyPEM, err := verifier.DecodePublicKey(testPublicKeyB64)
	require.NoError(t, err)
	bundle := []byte(`{"bundle":true}`)

	t.Run("verifies against the pinned key, not the keyless path", func(t *testing.T) {
		t.Parallel()
		entry := keyedLockEntry()
		mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
		mv.EXPECT().VerifyBundleOfflineWithKey(bundle, entry.Digest, keyPEM).
			Return(nil)
		// Not merely "the key path was taken": reaching the keyless path at
		// all is the bug, and it fails closed in a way that looks like drift.
		mv.EXPECT().VerifyBundleOffline(gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

		svc := &service{sigVerifier: mv}
		require.NoError(t, svc.verifyStoredSignature(entry, skills.InstalledSkill{SigstoreBundle: bundle}))
	})

	t.Run("a real verification failure still propagates", func(t *testing.T) {
		t.Parallel()
		entry := keyedLockEntry()
		mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
		mv.EXPECT().VerifyBundleOfflineWithKey(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(verifier.ErrSignatureInvalid)

		svc := &service{sigVerifier: mv}
		require.ErrorIs(t,
			svc.verifyStoredSignature(entry, skills.InstalledSkill{SigstoreBundle: bundle}),
			verifier.ErrSignatureInvalid)
	})

	// The signature is checked against the digest the LOCK pins, and no
	// reference reaches the verifier at all. Both halves matter: the entry
	// is the authority on what the project is pinned to, and a payload
	// rebuilt from a reference would verify against whatever that reference
	// claimed — the check a signature lifted onto another artifact passes.
	// So a differing install record must not change what is verified.
	t.Run("checks the digest the lock pins, ignoring the install record", func(t *testing.T) {
		t.Parallel()
		entry := keyedLockEntry()
		entry.ResolvedReference = ""
		mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
		mv.EXPECT().VerifyBundleOfflineWithKey(bundle, entry.Digest, keyPEM).Return(nil)

		svc := &service{sigVerifier: mv}
		require.NoError(t, svc.verifyStoredSignature(entry, skills.InstalledSkill{
			SigstoreBundle: bundle,
			Reference:      "example.com/org/from-install",
		}))
	})

	t.Run("a missing bundle names the key anchor, not an empty signer", func(t *testing.T) {
		t.Parallel()
		entry := keyedLockEntry()
		svc := &service{sigVerifier: verifiermocks.NewMockVerifier(gomock.NewController(t))}
		err := svc.verifyStoredSignature(entry, skills.InstalledSkill{})
		require.ErrorIs(t, err, verifier.ErrSignatureInvalid)
		assert.Contains(t, err.Error(), "a cosign public key")
		assert.NotContains(t, err.Error(), `signer ""`,
			"a key entry records no signer identity; the keyless phrasing named an empty string")
	})
}

// TestAdoptSkill_RefusesKeySignedInstall covers the one place a key-signed
// artifact has no path through: adoption back-fills trust from what the stored
// bundle reveals, and a key-pair bundle reveals no identity and does not carry
// the key. Recording it as unsigned instead would file a false trust decision
// about an artifact that IS signed, so the refusal has to name the route that
// can anchor it.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestAdoptSkill_RefusesKeySignedInstall(t *testing.T) {
	gr, fx := newGitResolverMock(t)
	fx.register("adopt-keyed", gitSkill("adopt-keyed"))

	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().Return(signedResult(), nil)
	mv.EXPECT().VerifyBundleOffline(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().Return(nil)
	// The stored bundle is a key-pair one: nothing to observe.
	mv.EXPECT().ResultFromBundle(gomock.Any(), gomock.Any()).
		AnyTimes().Return(nil, verifier.ErrKeySigned)

	svc, projectRoot := newLockTestService(t, gr, WithVerifier(mv))
	ref, _ := gitRef("adopt-keyed")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	// Strip it back to the unmanaged state a pre-lock-tracking install is in,
	// but leave a stored bundle behind so adoption has something to read.
	syncer := svc.(*service) //nolint:forcetypeassert
	root := mustOpenRoot(t, projectRoot)
	require.NoError(t, lockfile.Update(root, func(lf *lockfile.Lockfile) error {
		lf.Remove("adopt-keyed")
		return nil
	}))
	legacy, err := syncer.store.Get(t.Context(), "adopt-keyed", skills.ScopeProject, projectRoot)
	require.NoError(t, err)
	legacy.Managed = false
	legacy.SigstoreBundle = []byte(`{"bundle":true}`)
	require.NoError(t, syncer.store.Update(t.Context(), legacy))

	adoptErr := syncer.adoptSkill(t.Context(),
		skills.SyncOptions{ProjectRoot: projectRoot}, legacy)

	require.Error(t, adoptErr)
	assert.Equal(t, http.StatusForbidden, httperr.Code(adoptErr))
	require.ErrorIs(t, adoptErr, verifier.ErrKeySigned)
	assert.Contains(t, adoptErr.Error(), "--public-key",
		"the refusal must name the path that can anchor it, not merely refuse")
	assert.Contains(t, adoptErr.Error(), "--allow-unsigned is not a substitute",
		"the artifact is signed; recording an unsigned exception would be a false trust decision")

	// The refusal must not leave a lock entry behind.
	lf := readLockfile(t, projectRoot)
	_, ok := lf.Get("adopt-keyed")
	assert.False(t, ok, "a refused adoption must write nothing")
}

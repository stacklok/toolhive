// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

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

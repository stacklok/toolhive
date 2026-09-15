// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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

func TestResolveOCITrustPolicy_ReanchorMatrixUsesOneSnapshot(t *testing.T) {
	t.Parallel()

	oldKeyPEM, err := verifier.DecodePublicKey(testPublicKeyB64)
	require.NoError(t, err)
	newKeyPEM, err := verifier.DecodePublicKey(otherKeyB64)
	require.NoError(t, err)

	keylessEntry := keyedLockEntry()
	keylessEntry.Provenance = provenanceInfoToLock(provenanceInfoFromResult(signedResult()))
	unsignedEntry := keyedLockEntry()
	unsignedEntry.Provenance = nil
	unsignedEntry.Unsigned = true
	unrecordedEntry := keyedLockEntry()
	unrecordedEntry.Provenance = nil
	unrecordedEntry.Unsigned = false

	tests := []struct {
		name         string
		entry        lockfile.Entry
		retrieveErr  error
		configure    func(*verifiermocks.MockOCISnapshot)
		wantBlocked  bool
		wantKey      string
		wantIdentity string
		wantUnsigned bool
		wantReason   skills.FailureReason
	}{
		{
			name:  "old key wins before a replacement is considered",
			entry: keyedLockEntry(),
			configure: func(snapshot *verifiermocks.MockOCISnapshot) {
				snapshot.EXPECT().VerifyWithKey(oldKeyPEM).
					Return(&verifier.Result{Signed: true, Bundle: []byte("old-key")}, nil)
				// This replacement would also verify if evaluated. The old
				// anchor must nevertheless win.
				snapshot.EXPECT().VerifyWithKey(newKeyPEM).AnyTimes().
					Return(&verifier.Result{Signed: true, Bundle: []byte("new-key")}, nil)
			},
			wantKey: testPublicKeyB64,
		},
		{
			name:  "replacement key wins after old key mismatch",
			entry: keyedLockEntry(),
			configure: func(snapshot *verifiermocks.MockOCISnapshot) {
				gomock.InOrder(
					snapshot.EXPECT().VerifyWithKey(oldKeyPEM).Return(nil, verifier.ErrSignatureInvalid),
					snapshot.EXPECT().VerifyWithKey(newKeyPEM).
						Return(&verifier.Result{Signed: true, Bundle: []byte("new-key")}, nil),
				)
			},
			wantKey: otherKeyB64,
		},
		{
			name:  "replacement key mismatch fails without keyless fallback",
			entry: keyedLockEntry(),
			configure: func(snapshot *verifiermocks.MockOCISnapshot) {
				gomock.InOrder(
					snapshot.EXPECT().VerifyWithKey(oldKeyPEM).Return(nil, verifier.ErrKeylessSigned),
					snapshot.EXPECT().VerifyWithKey(newKeyPEM).Return(nil, verifier.ErrKeylessSigned),
				)
				snapshot.EXPECT().VerifyKeyless(gomock.Any()).Times(0)
			},
			wantBlocked: true,
			wantReason:  skills.FailureReasonSignatureInvalid,
		},
		{
			name:        "retrieval error leaves trust undecided",
			entry:       keyedLockEntry(),
			retrieveErr: context.DeadlineExceeded,
			wantBlocked: true,
			wantReason:  skills.FailureReasonUnknown,
		},
		{
			name:        "unsigned candidate cannot manufacture an exception for unrecorded trust",
			entry:       unrecordedEntry,
			retrieveErr: verifier.ErrUnsigned,
			wantBlocked: true,
			wantReason:  skills.FailureReasonUnsignedRejected,
		},
		{
			name:  "keyless entry moves to supplied key",
			entry: keylessEntry,
			configure: func(snapshot *verifiermocks.MockOCISnapshot) {
				snapshot.EXPECT().VerifyWithKey(newKeyPEM).
					Return(&verifier.Result{Signed: true, Bundle: []byte("new-key")}, nil)
			},
			wantKey: otherKeyB64,
		},
		{
			name:  "unsigned entry moves to supplied key",
			entry: unsignedEntry,
			configure: func(snapshot *verifiermocks.MockOCISnapshot) {
				snapshot.EXPECT().VerifyWithKey(newKeyPEM).
					Return(&verifier.Result{Signed: true, Bundle: []byte("new-key")}, nil)
			},
			wantKey: otherKeyB64,
		},
		{
			name:  "inapplicable key retains matching keyless anchor",
			entry: keylessEntry,
			configure: func(snapshot *verifiermocks.MockOCISnapshot) {
				gomock.InOrder(
					snapshot.EXPECT().VerifyWithKey(newKeyPEM).Return(nil, verifier.ErrKeylessSigned),
					snapshot.EXPECT().VerifyKeyless(gomock.Any()).Return(signedResult(), nil),
				)
			},
			wantIdentity: testSignerIdentity,
		},
		{
			name:  "wrong key retains unsigned exception",
			entry: unsignedEntry,
			configure: func(snapshot *verifiermocks.MockOCISnapshot) {
				snapshot.EXPECT().VerifyWithKey(newKeyPEM).Return(nil, verifier.ErrSignatureInvalid)
			},
			wantUnsigned: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			mv := verifiermocks.NewMockOCISnapshotVerifier(ctrl)
			snapshot := verifiermocks.NewMockOCISnapshot(ctrl)
			mv.EXPECT().RetrieveOCISnapshot(gomock.Any(), "ghcr.io/org/skill:v2", tc.entry.Digest).
				Times(1).Return(snapshot, tc.retrieveErr)
			if tc.configure != nil {
				tc.configure(snapshot)
			}

			outcome := skills.UpgradeOutcome{Name: tc.entry.Name}
			decision, blocked := (&service{sigVerifier: mv}).resolveOCITrustPolicy(
				t.Context(),
				skills.UpgradeOptions{AllowSignerChange: true, PublicKey: otherKeyB64},
				tc.entry,
				"ghcr.io/org/skill:v2",
				tc.entry.Digest,
				&outcome,
			)

			assert.Equal(t, tc.wantBlocked, blocked)
			if tc.wantBlocked {
				assert.Nil(t, decision)
				assert.Equal(t, skills.UpgradeStatusFailed, outcome.Status)
				assert.Equal(t, tc.wantReason, outcome.Reason)
				return
			}
			require.NotNil(t, decision)
			assert.Equal(t, tc.wantUnsigned, decision.unsigned)
			if tc.wantKey != "" {
				require.NotNil(t, decision.provenance)
				assert.Equal(t, tc.wantKey, decision.provenance.PublicKey)
			}
			if tc.wantIdentity != "" {
				require.NotNil(t, decision.provenance)
				assert.Equal(t, tc.wantIdentity, decision.provenance.SignerIdentity)
				assert.Empty(t, decision.provenance.PublicKey)
			}
		})
	}
}

func TestResolvePlannedTrust_LegacyVerifierSupportsOrdinaryUpgrade(t *testing.T) {
	t.Parallel()

	keylessEntry := keyedLockEntry()
	keylessEntry.Provenance = provenanceInfoToLock(provenanceInfoFromResult(signedResult()))
	oldKeyPEM, err := verifier.DecodePublicKey(testPublicKeyB64)
	require.NoError(t, err)

	tests := []struct {
		name      string
		entry     lockfile.Entry
		configure func(*verifiermocks.MockVerifier)
	}{
		{
			name:  "keyless anchor",
			entry: keylessEntry,
			configure: func(mv *verifiermocks.MockVerifier) {
				mv.EXPECT().VerifyOCI(
					gomock.Any(), "ghcr.io/org/skill:v2", keylessEntry.Digest,
					nil,
				).Return(signedResult(), nil)
			},
		},
		{
			name:  "public key anchor",
			entry: keyedLockEntry(),
			configure: func(mv *verifiermocks.MockVerifier) {
				mv.EXPECT().VerifyOCIWithKey(
					gomock.Any(), "ghcr.io/org/skill:v2", keyedLockEntry().Digest, oldKeyPEM,
				).Return(&verifier.Result{Signed: true, Bundle: []byte("keyed-bundle")}, nil)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			legacyVerifier := verifiermocks.NewMockVerifier(gomock.NewController(t))
			tc.configure(legacyVerifier)
			outcome := skills.UpgradeOutcome{Name: tc.entry.Name}

			decision, allowSignerChange, blocked := (&service{sigVerifier: legacyVerifier}).resolvePlannedTrust(
				t.Context(), skills.UpgradeOptions{}, tc.entry,
				"ghcr.io/org/skill:v2", tc.entry.Digest, true, true, &outcome,
			)

			assert.False(t, blocked)
			assert.False(t, allowSignerChange)
			assert.Nil(t, decision,
				"ordinary upgrades must use the legacy verification path instead of requiring a snapshot")
		})
	}
}

func TestConsumePreverifiedTrustRejectsInvalidDecision(t *testing.T) {
	t.Parallel()

	digest := "sha256:planned"
	validSigned := &provenanceDecision{
		provenance: &skills.ProvenanceInfo{PublicKey: testPublicKeyB64},
		bundle:     []byte("bundle"),
	}
	tests := []struct {
		name        string
		preverified *preverifiedOCITrust
	}{
		{name: "missing handoff"},
		{name: "digest mismatch", preverified: &preverifiedOCITrust{decision: validSigned, digest: "sha256:other"}},
		{name: "missing decision", preverified: &preverifiedOCITrust{digest: digest}},
		{
			name: "signed decision missing bundle",
			preverified: &preverifiedOCITrust{
				decision: &provenanceDecision{provenance: &skills.ProvenanceInfo{PublicKey: testPublicKeyB64}},
				digest:   digest,
			},
		},
		{
			name: "unsigned decision carries bundle",
			preverified: &preverifiedOCITrust{
				decision: &provenanceDecision{unsigned: true, bundle: []byte("bundle")},
				digest:   digest,
			},
		},
		{
			name: "decision mixes signed and unsigned trust",
			preverified: &preverifiedOCITrust{
				decision: &provenanceDecision{
					provenance: &skills.ProvenanceInfo{PublicKey: testPublicKeyB64},
					unsigned:   true,
					bundle:     []byte("bundle"),
				},
				digest: digest,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			decision, err := consumePreverifiedTrust(tc.preverified, digest)
			require.Error(t, err)
			assert.Nil(t, decision)
		})
	}
}

func TestPlanUpgrade_ImmutableOCINeedsSignerChangeConsent(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mv := verifiermocks.NewMockVerifier(ctrl)
	entry := keyedLockEntry()
	entry.Source = "ghcr.io/org/skill@" + entry.Digest
	entry.ResolvedReference = entry.Source

	plan := (&service{sigVerifier: mv}).planUpgrade(t.Context(), skills.UpgradeOptions{}, entry)
	assert.Equal(t, skills.UpgradeStatusNotUpgradable, plan.outcome.Status)
	assert.Nil(t, plan.trustDecision)
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_PublicKeyDoesNotChangeGitTrust(t *testing.T) {
	svc, projectRoot := signerChangeFixture(t, func() (*verifier.Result, error) {
		return signedResult(), nil
	})
	result, err := svc.(*service).Upgrade(t.Context(), skills.UpgradeOptions{ //nolint:forcetypeassert
		ProjectRoot: projectRoot, AllowSignerChange: true, PublicKey: otherKeyB64,
	})
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, skills.UpgradeStatusUpgraded, result.Outcomes[0].Status)

	entry, ok := readLockfile(t, projectRoot).Get("guarded-skill")
	require.True(t, ok)
	require.NotNil(t, entry.Provenance)
	assert.Equal(t, testSignerIdentity, entry.Provenance.SignerIdentity)
	assert.Empty(t, entry.Provenance.PublicKey)
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_TrustOnlyReanchorPreservesInstalledState(t *testing.T) {
	for _, immutable := range []bool{false, true} {
		name := "mutable tag at same digest"
		if immutable {
			name = "immutable OCI digest with explicit consent"
		}
		t.Run(name, func(t *testing.T) {
			svc, projectRoot, _, newBundle := newKeyReanchorFixture(t)
			if immutable {
				root := mustOpenRoot(t, projectRoot)
				require.NoError(t, lockfile.Update(root, func(lf *lockfile.Lockfile) error {
					entry, ok := lf.Get("my-skill")
					require.True(t, ok)
					entry.Source = "ghcr.io/org/my-skill@" + entry.Digest
					entry.ResolvedReference = entry.Source
					lf.Upsert(entry)
					return nil
				}))
			}

			root := mustOpenRoot(t, projectRoot)
			require.NoError(t, lockfile.Update(root, func(lf *lockfile.Lockfile) error {
				entry, ok := lf.Get("my-skill")
				require.True(t, ok)
				parent := entry
				parent.Name = "parent-skill"
				parent.Source = "ghcr.io/org/parent-skill:v1"
				parent.ResolvedReference = parent.Source
				parent.RequiredBy = nil
				lf.Upsert(parent)
				entry.RequiredBy = []string{"parent-skill"}
				entry.Explicit = true
				lf.Upsert(entry)
				return nil
			}))

			installed, err := svc.store.Get(t.Context(), "my-skill", skills.ScopeProject, projectRoot)
			require.NoError(t, err)
			installed.Dependencies = []skills.Dependency{{
				Name: "dependency", Reference: "ghcr.io/org/dependency:v1", Digest: "sha256:dependency",
			}}
			require.NoError(t, svc.store.Update(t.Context(), installed))
			beforeInstalled, err := svc.store.Get(t.Context(), "my-skill", skills.ScopeProject, projectRoot)
			require.NoError(t, err)
			beforeEntry, ok := readLockfile(t, projectRoot).Get("my-skill")
			require.True(t, ok)

			sentinel := filepath.Join(projectRoot, ".claude", "skills", "my-skill", "trust-only-sentinel")
			require.NoError(t, os.WriteFile(sentinel, []byte("preserve me"), 0o600))

			outcome := upgradeKeyPinned(t, svc, skills.UpgradeOptions{
				ProjectRoot: projectRoot, Names: []string{"my-skill"},
				AllowSignerChange: true, PublicKey: otherKeyB64,
			})
			assert.Equal(t, skills.UpgradeStatusTrustUpdated, outcome.Status, "error: %s", outcome.Error)
			assert.True(t, outcome.TrustAnchorChanged)

			afterInstalled, err := svc.store.Get(t.Context(), "my-skill", skills.ScopeProject, projectRoot)
			require.NoError(t, err)
			expectedInstalled := beforeInstalled
			expectedInstalled.SigstoreBundle = newBundle
			assert.Equal(t, expectedInstalled, afterInstalled,
				"trust-only persistence may change only the stored bundle")

			afterEntry, ok := readLockfile(t, projectRoot).Get("my-skill")
			require.True(t, ok)
			expectedEntry := beforeEntry
			expectedEntry.Provenance = &lockfile.Provenance{PublicKey: otherKeyB64}
			expectedEntry.Unsigned = false
			assert.Equal(t, expectedEntry, afterEntry,
				"trust-only persistence must preserve source, digests, dependencies, and explicit state")

			sentinelContents, err := os.ReadFile(sentinel)
			require.NoError(t, err, "trust-only updates must not replace the extracted directory")
			assert.Equal(t, []byte("preserve me"), sentinelContents)
		})
	}
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_ChangedContentUsesPreverifiedReanchor(t *testing.T) {
	svc, projectRoot, publishV2, newBundle := newKeyReanchorFixture(t)
	publishV2()
	before, ok := readLockfile(t, projectRoot).Get("my-skill")
	require.True(t, ok)

	outcome := upgradeKeyPinned(t, svc, skills.UpgradeOptions{
		ProjectRoot: projectRoot, AllowSignerChange: true, PublicKey: otherKeyB64,
	})
	assert.Equal(t, skills.UpgradeStatusUpgraded, outcome.Status, "error: %s", outcome.Error)
	assert.True(t, outcome.TrustAnchorChanged)
	assert.NotEqual(t, before.Digest, outcome.NewDigest)

	after, ok := readLockfile(t, projectRoot).Get("my-skill")
	require.True(t, ok)
	require.NotNil(t, after.Provenance)
	assert.Equal(t, otherKeyB64, after.Provenance.PublicKey)
	assert.Equal(t, outcome.NewDigest, after.Digest)
	installed, err := svc.store.Get(t.Context(), "my-skill", skills.ScopeProject, projectRoot)
	require.NoError(t, err)
	assert.Equal(t, newBundle, installed.SigstoreBundle)
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_ContentApplyDetectsConflictAfterDatabaseWrite(t *testing.T) {
	svc, projectRoot, publishV2, _ := newKeyReanchorFixture(t)
	publishV2()
	beforeInstalled, err := svc.store.Get(t.Context(), "my-skill", skills.ScopeProject, projectRoot)
	require.NoError(t, err)

	concurrentDigest := ociTestDigest(9)
	updateCalls := 0
	var hookErr error
	svc.store = &hookSkillStore{
		SkillStore: svc.store,
		afterUpdate: func() {
			updateCalls++
			if updateCalls != 1 {
				return
			}
			root := mustOpenRoot(t, projectRoot)
			hookErr = lockfile.Update(root, func(lf *lockfile.Lockfile) error {
				entry, exists := lf.Get("my-skill")
				if !exists {
					return fmt.Errorf("planned lock entry disappeared")
				}
				entry.Digest = concurrentDigest
				lf.Upsert(entry)
				return nil
			})
		},
	}

	result, err := svc.Upgrade(t.Context(), skills.UpgradeOptions{
		ProjectRoot: projectRoot, AllowSignerChange: true, PublicKey: otherKeyB64,
	})
	require.NoError(t, err)
	require.NoError(t, hookErr)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, skills.UpgradeStatusFailed, result.Outcomes[0].Status)
	assert.Contains(t, result.Outcomes[0].Error, lockfile.ErrEntryChanged.Error())
	assert.Equal(t, 2, updateCalls, "the database update must be compensated after the lock CAS conflict")

	afterInstalled, getErr := svc.store.Get(t.Context(), "my-skill", skills.ScopeProject, projectRoot)
	require.NoError(t, getErr)
	assert.Equal(t, beforeInstalled, afterInstalled,
		"the failed content apply must restore the database record")
	afterEntry, ok := readLockfile(t, projectRoot).Get("my-skill")
	require.True(t, ok)
	assert.Equal(t, concurrentDigest, afterEntry.Digest,
		"rollback must not overwrite the competing lock update with its stale snapshot")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_SameDigestCrossRepositoryMoveIsBlocked(t *testing.T) {
	svc, projectRoot := newInstalledKeyFixture(t)
	newRef := "ghcr.io/new-org/my-skill:v2"
	root := mustOpenRoot(t, projectRoot)
	require.NoError(t, lockfile.Update(root, func(lf *lockfile.Lockfile) error {
		entry, ok := lf.Get("my-skill")
		require.True(t, ok)
		entry.Source = newRef
		lf.Upsert(entry)
		return nil
	}))
	before, ok := readLockfile(t, projectRoot).Get("my-skill")
	require.True(t, ok)

	result, err := svc.Upgrade(t.Context(), skills.UpgradeOptions{
		ProjectRoot: projectRoot, AllowSignerChange: true, PublicKey: otherKeyB64,
	})
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	outcome := result.Outcomes[0]
	assert.Equal(t, skills.UpgradeStatusRefChangeBlocked, outcome.Status)
	assert.Equal(t, before.Digest, outcome.NewDigest)
	assert.Equal(t, newRef, outcome.NewResolvedReference)

	after, ok := readLockfile(t, projectRoot).Get("my-skill")
	require.True(t, ok)
	assert.Equal(t, before, after, "a blocked same-digest repository move must not rewrite the lock entry")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_SameDigestCrossRepositoryMovePersistsAuthorizedReference(t *testing.T) {
	svc, projectRoot, _, newBundle := newKeyReanchorFixture(t)
	newRef := "ghcr.io/new-org/my-skill:v2"
	root := mustOpenRoot(t, projectRoot)
	require.NoError(t, lockfile.Update(root, func(lf *lockfile.Lockfile) error {
		entry, ok := lf.Get("my-skill")
		require.True(t, ok)
		entry.Source = newRef
		lf.Upsert(entry)
		return nil
	}))
	before, ok := readLockfile(t, projectRoot).Get("my-skill")
	require.True(t, ok)
	sentinel := filepath.Join(projectRoot, ".claude", "skills", "my-skill", "same-digest-sentinel")
	require.NoError(t, os.WriteFile(sentinel, []byte("preserve me"), 0o600))

	result, err := svc.Upgrade(t.Context(), skills.UpgradeOptions{
		ProjectRoot:       projectRoot,
		AllowRefChange:    true,
		AllowSignerChange: true,
		PublicKey:         otherKeyB64,
	})
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	outcome := result.Outcomes[0]
	assert.Equal(t, skills.UpgradeStatusUpgraded, outcome.Status, "error: %s", outcome.Error)
	assert.True(t, outcome.TrustAnchorChanged)
	assert.Equal(t, before.Digest, outcome.NewDigest)
	assert.Equal(t, newRef, outcome.NewResolvedReference)

	after, ok := readLockfile(t, projectRoot).Get("my-skill")
	require.True(t, ok)
	assert.Equal(t, newRef, after.ResolvedReference)
	require.NotNil(t, after.Provenance)
	assert.Equal(t, otherKeyB64, after.Provenance.PublicKey)
	assert.Equal(t, before.Digest, after.Digest)

	installed, err := svc.store.Get(t.Context(), "my-skill", skills.ScopeProject, projectRoot)
	require.NoError(t, err)
	expectedPinnedRef, err := buildPinnedReference(lockfile.Entry{
		ResolvedReference: newRef,
		Digest:            before.Digest,
	})
	require.NoError(t, err)
	assert.Equal(t, expectedPinnedRef, installed.Reference)
	assert.Equal(t, newBundle, installed.SigstoreBundle)
	sentinelContents, err := os.ReadFile(sentinel)
	require.NoError(t, err, "a same-digest reference move must not rematerialize extracted content")
	assert.Equal(t, []byte("preserve me"), sentinelContents)
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_TrustOnlyRollbackRestoresDatabase(t *testing.T) {
	svc, projectRoot, _, _ := newKeyReanchorFixture(t)
	beforeInstalled, err := svc.store.Get(t.Context(), "my-skill", skills.ScopeProject, projectRoot)
	require.NoError(t, err)
	beforeEntry, ok := readLockfile(t, projectRoot).Get("my-skill")
	require.True(t, ok)

	lockPath := filepath.Join(projectRoot, lockfile.FileName)
	backupPath := filepath.Join(projectRoot, "lock-before-failure.yaml")
	updateCalls := 0
	var hookErr error
	svc.store = &hookSkillStore{
		SkillStore: svc.store,
		afterUpdate: func() {
			updateCalls++
			if updateCalls != 1 {
				return
			}
			if err := os.Rename(lockPath, backupPath); err != nil {
				hookErr = err
				return
			}
			hookErr = os.Mkdir(lockPath, 0o700)
		},
	}
	restoreLock := func() {
		if _, err := os.Stat(backupPath); err != nil {
			return
		}
		_ = os.Remove(lockPath)
		_ = os.Rename(backupPath, lockPath)
	}
	t.Cleanup(restoreLock)

	outcome := upgradeKeyPinned(t, svc, skills.UpgradeOptions{
		ProjectRoot: projectRoot, AllowSignerChange: true, PublicKey: otherKeyB64,
	})
	require.NoError(t, hookErr)
	assert.Equal(t, skills.UpgradeStatusFailed, outcome.Status)
	assert.Contains(t, outcome.Error, "updating lock trust anchor")
	assert.Equal(t, 2, updateCalls, "the second database update must compensate the failed lock write")

	restoreLock()
	afterInstalled, err := svc.store.Get(t.Context(), "my-skill", skills.ScopeProject, projectRoot)
	require.NoError(t, err)
	assert.Equal(t, beforeInstalled, afterInstalled)
	afterEntry, ok := readLockfile(t, projectRoot).Get("my-skill")
	require.True(t, ok)
	assert.Equal(t, beforeEntry, afterEntry)
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_TrustOnlyRollbackSurvivesCallerCancellation(t *testing.T) {
	svc, projectRoot, _, _ := newKeyReanchorFixture(t)
	beforeInstalled, err := svc.store.Get(t.Context(), "my-skill", skills.ScopeProject, projectRoot)
	require.NoError(t, err)
	beforeEntry, ok := readLockfile(t, projectRoot).Get("my-skill")
	require.True(t, ok)

	callerCtx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	lockPath := filepath.Join(projectRoot, lockfile.FileName)
	backupPath := filepath.Join(projectRoot, "lock-before-canceled-failure.yaml")
	var updateContextErrors []error
	var hookErr error
	svc.store = &hookSkillStore{
		SkillStore: svc.store,
		afterUpdateContext: func(updateCtx context.Context) {
			updateContextErrors = append(updateContextErrors, updateCtx.Err())
			if len(updateContextErrors) != 1 {
				return
			}
			if err := os.Rename(lockPath, backupPath); err != nil {
				hookErr = err
				return
			}
			hookErr = os.Mkdir(lockPath, 0o700)
			cancel()
		},
	}
	restoreLock := func() {
		if _, err := os.Stat(backupPath); err != nil {
			return
		}
		_ = os.Remove(lockPath)
		_ = os.Rename(backupPath, lockPath)
	}
	t.Cleanup(restoreLock)

	result, err := svc.Upgrade(callerCtx, skills.UpgradeOptions{
		ProjectRoot: projectRoot, AllowSignerChange: true, PublicKey: otherKeyB64,
	})
	require.NoError(t, err)
	require.NoError(t, hookErr)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, skills.UpgradeStatusFailed, result.Outcomes[0].Status)
	assert.Contains(t, result.Outcomes[0].Error, "updating lock trust anchor")
	require.Len(t, updateContextErrors, 2,
		"the durable update must be followed by a compensating database update")
	assert.NoError(t, updateContextErrors[0])
	assert.NoError(t, updateContextErrors[1],
		"compensation must detach from the canceled request context")

	restoreLock()
	afterInstalled, err := svc.store.Get(t.Context(), "my-skill", skills.ScopeProject, projectRoot)
	require.NoError(t, err)
	assert.Equal(t, beforeInstalled, afterInstalled)
	afterEntry, ok := readLockfile(t, projectRoot).Get("my-skill")
	require.True(t, ok)
	assert.Equal(t, beforeEntry, afterEntry)
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestPersistTrustOnlyUpgradeRejectsStalePlan(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *service, string, lockfile.Entry)
	}{
		{
			name: "lock digest changed",
			mutate: func(t *testing.T, _ *service, projectRoot string, _ lockfile.Entry) {
				t.Helper()
				root := mustOpenRoot(t, projectRoot)
				require.NoError(t, lockfile.Update(root, func(lf *lockfile.Lockfile) error {
					entry, ok := lf.Get("my-skill")
					require.True(t, ok)
					entry.Digest = ociTestDigest(9)
					lf.Upsert(entry)
					return nil
				}))
			},
		},
		{
			name: "database digest changed",
			mutate: func(t *testing.T, svc *service, projectRoot string, _ lockfile.Entry) {
				t.Helper()
				installed, err := svc.store.Get(t.Context(), "my-skill", skills.ScopeProject, projectRoot)
				require.NoError(t, err)
				installed.Digest = ociTestDigest(9)
				require.NoError(t, svc.store.Update(t.Context(), installed))
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, projectRoot := newInstalledKeyFixture(t)
			planned, ok := readLockfile(t, projectRoot).Get("my-skill")
			require.True(t, ok)
			tc.mutate(t, svc, projectRoot, planned)

			plan := upgradePlan{
				entry: planned,
				outcome: skills.UpgradeOutcome{
					Name: planned.Name, OldDigest: planned.Digest, NewDigest: planned.Digest,
					Status: skills.UpgradeStatusTrustUpdated,
				},
				trustDecision: &provenanceDecision{
					provenance: &skills.ProvenanceInfo{PublicKey: otherKeyB64},
					bundle:     []byte("replacement-bundle"),
				},
			}
			err := svc.persistTrustOnlyUpgrade(t.Context(), projectRoot, plan)
			require.Error(t, err)
			assert.Equal(t, http.StatusConflict, httperr.Code(err))

			current, getErr := svc.store.Get(t.Context(), "my-skill", skills.ScopeProject, projectRoot)
			require.NoError(t, getErr)
			assert.NotEqual(t, []byte("replacement-bundle"), current.SigstoreBundle)
		})
	}
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestPersistTrustOnlyUpgradeDetectsConflictAfterDatabaseWrite(t *testing.T) {
	svc, projectRoot := newInstalledKeyFixture(t)
	planned, ok := readLockfile(t, projectRoot).Get("my-skill")
	require.True(t, ok)
	beforeInstalled, err := svc.store.Get(t.Context(), "my-skill", skills.ScopeProject, projectRoot)
	require.NoError(t, err)

	concurrentDigest := ociTestDigest(9)
	updateCalls := 0
	var hookErr error
	svc.store = &hookSkillStore{
		SkillStore: svc.store,
		afterUpdate: func() {
			updateCalls++
			if updateCalls != 1 {
				return
			}
			root := mustOpenRoot(t, projectRoot)
			hookErr = lockfile.Update(root, func(lf *lockfile.Lockfile) error {
				entry, exists := lf.Get(planned.Name)
				if !exists {
					return fmt.Errorf("planned lock entry disappeared")
				}
				entry.Digest = concurrentDigest
				lf.Upsert(entry)
				return nil
			})
		},
	}

	plan := upgradePlan{
		entry: planned,
		outcome: skills.UpgradeOutcome{
			Name: planned.Name, OldDigest: planned.Digest, NewDigest: planned.Digest,
			Status: skills.UpgradeStatusTrustUpdated,
		},
		trustDecision: &provenanceDecision{
			provenance: &skills.ProvenanceInfo{PublicKey: otherKeyB64},
			bundle:     []byte("replacement-bundle"),
		},
	}
	err = svc.persistTrustOnlyUpgrade(t.Context(), projectRoot, plan)
	require.Error(t, err)
	require.NoError(t, hookErr)
	assert.Equal(t, http.StatusConflict, httperr.Code(err))
	assert.Equal(t, 2, updateCalls, "the database update must be compensated after the lock CAS conflict")

	afterInstalled, getErr := svc.store.Get(t.Context(), "my-skill", skills.ScopeProject, projectRoot)
	require.NoError(t, getErr)
	assert.Equal(t, beforeInstalled, afterInstalled)
	afterEntry, ok := readLockfile(t, projectRoot).Get(planned.Name)
	require.True(t, ok)
	assert.Equal(t, concurrentDigest, afterEntry.Digest,
		"the competing lock update must survive the failed trust-only persistence")
}

func newInstalledKeyFixture(t *testing.T) (*service, string) {
	t.Helper()
	oldKeyPEM, err := verifier.DecodePublicKey(testPublicKeyB64)
	require.NoError(t, err)
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyOCIWithKey(gomock.Any(), gomock.Any(), gomock.Any(), oldKeyPEM).
		Return(&verifier.Result{Signed: true, Bundle: []byte("installed-bundle")}, nil)
	svc, projectRoot, _ := keyPinnedUpgradeFixture(t, mv)
	return svc, projectRoot
}

func newKeyReanchorFixture(t *testing.T) (*service, string, func(), []byte) {
	t.Helper()
	ctrl := gomock.NewController(t)
	oldKeyPEM, err := verifier.DecodePublicKey(testPublicKeyB64)
	require.NoError(t, err)
	newKeyPEM, err := verifier.DecodePublicKey(otherKeyB64)
	require.NoError(t, err)

	mv := verifiermocks.NewMockOCISnapshotVerifier(ctrl)
	mv.EXPECT().VerifyOCIWithKey(gomock.Any(), gomock.Any(), gomock.Any(), oldKeyPEM).
		Return(&verifier.Result{Signed: true, Bundle: []byte("installed-bundle")}, nil)
	svc, projectRoot, publishV2 := keyPinnedUpgradeFixture(t, mv)

	newBundle := []byte("replacement-bundle")
	snapshot := verifiermocks.NewMockOCISnapshot(ctrl)
	mv.EXPECT().RetrieveOCISnapshot(gomock.Any(), gomock.Any(), gomock.Any()).
		Times(1).Return(snapshot, nil)
	gomock.InOrder(
		snapshot.EXPECT().VerifyWithKey(oldKeyPEM).Return(nil, verifier.ErrSignatureInvalid),
		snapshot.EXPECT().VerifyWithKey(newKeyPEM).
			Return(&verifier.Result{Signed: true, Bundle: newBundle}, nil),
	)
	return svc, projectRoot, publishV2, newBundle
}

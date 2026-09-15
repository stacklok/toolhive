// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

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
	ociplugins "github.com/stacklok/toolhive-core/oci/plugins"
	"github.com/stacklok/toolhive/pkg/plugins"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	"github.com/stacklok/toolhive/pkg/skills/verifier"
	verifiermocks "github.com/stacklok/toolhive/pkg/skills/verifier/mocks"
)

const reanchorPublicKeyB64 = "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEPl5po5mEdKFsHEdt11SCm95YB50Jeyha" +
	"N7o00oGAZy+W+3bTntiNoo/j4AsPbBrKRoZFAbDRUX5SsOvkE7vYzA=="

func TestValidateUpgradePublicKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		opts     plugins.UpgradeOptions
		wantCode int
	}{
		{name: "no key"},
		{
			name:     "key requires signer-change consent",
			opts:     plugins.UpgradeOptions{PublicKey: reanchorPublicKeyB64},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "valid re-anchor",
			opts: plugins.UpgradeOptions{AllowSignerChange: true, PublicKey: reanchorPublicKeyB64},
		},
		{
			name:     "malformed key",
			opts:     plugins.UpgradeOptions{AllowSignerChange: true, PublicKey: "not-a-key"},
			wantCode: http.StatusBadRequest,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateUpgradePublicKey(tc.opts)
			if tc.wantCode == 0 {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tc.wantCode, httperr.Code(err))
		})
	}
}

func TestResolveOCITrustPolicy_ReanchorMatrixUsesOneSnapshot(t *testing.T) {
	t.Parallel()

	oldKeyPEM, err := verifier.DecodePublicKey(testPublicKeyB64)
	require.NoError(t, err)
	newKeyPEM, err := verifier.DecodePublicKey(reanchorPublicKeyB64)
	require.NoError(t, err)

	keylessEntry := keyedLockEntry("my-plugin")
	keylessEntry.Provenance = signedResult().ToLockProvenance()
	unsignedEntry := keyedLockEntry("my-plugin")
	unsignedEntry.Provenance = nil
	unsignedEntry.Unsigned = true
	unrecordedEntry := keyedLockEntry("my-plugin")
	unrecordedEntry.Provenance = nil
	unrecordedEntry.Unsigned = false
	latest := resolvedLatest{ref: "ghcr.io/org/my-plugin:v2", digest: validLockDigestAlt()}

	tests := []struct {
		name         string
		entry        lockfile.Entry
		retrieveErr  error
		configure    func(*verifiermocks.MockOCISnapshot)
		wantBlocked  bool
		wantKey      string
		wantIdentity string
		wantUnsigned bool
		wantReason   plugins.FailureReason
	}{
		{
			name:  "old key wins before replacement",
			entry: keyedLockEntry("my-plugin"),
			configure: func(snapshot *verifiermocks.MockOCISnapshot) {
				snapshot.EXPECT().VerifyWithKey(oldKeyPEM).
					Return(&verifier.Result{Signed: true, Bundle: []byte("old-key")}, nil)
			},
			wantKey: testPublicKeyB64,
		},
		{
			name:  "replacement key wins after old key mismatch",
			entry: keyedLockEntry("my-plugin"),
			configure: func(snapshot *verifiermocks.MockOCISnapshot) {
				gomock.InOrder(
					snapshot.EXPECT().VerifyWithKey(oldKeyPEM).Return(nil, verifier.ErrSignatureInvalid),
					snapshot.EXPECT().VerifyWithKey(newKeyPEM).
						Return(&verifier.Result{Signed: true, Bundle: []byte("new-key")}, nil),
				)
			},
			wantKey: reanchorPublicKeyB64,
		},
		{
			name:  "keyless succeeds after both keys mismatch",
			entry: keyedLockEntry("my-plugin"),
			configure: func(snapshot *verifiermocks.MockOCISnapshot) {
				gomock.InOrder(
					snapshot.EXPECT().VerifyWithKey(oldKeyPEM).Return(nil, verifier.ErrSignatureInvalid),
					snapshot.EXPECT().VerifyWithKey(newKeyPEM).Return(nil, verifier.ErrSignatureInvalid),
					snapshot.EXPECT().VerifyKeyless(nil).Return(signedResult(), nil),
				)
			},
			wantIdentity: testSignerIdentity,
		},
		{
			name:        "retrieval error leaves trust undecided",
			entry:       keyedLockEntry("my-plugin"),
			retrieveErr: context.DeadlineExceeded,
			wantBlocked: true,
			wantReason:  plugins.FailureReasonUnknown,
		},
		{
			name:        "unsigned candidate cannot manufacture an exception for unrecorded trust",
			entry:       unrecordedEntry,
			retrieveErr: verifier.ErrUnsigned,
			wantBlocked: true,
			wantReason:  plugins.FailureReasonUnsignedRejected,
		},
		{
			name:  "keyless entry moves to supplied key",
			entry: keylessEntry,
			configure: func(snapshot *verifiermocks.MockOCISnapshot) {
				snapshot.EXPECT().VerifyWithKey(newKeyPEM).
					Return(&verifier.Result{Signed: true, Bundle: []byte("new-key")}, nil)
			},
			wantKey: reanchorPublicKeyB64,
		},
		{
			name:  "unsigned entry moves to supplied key",
			entry: unsignedEntry,
			configure: func(snapshot *verifiermocks.MockOCISnapshot) {
				snapshot.EXPECT().VerifyWithKey(newKeyPEM).
					Return(&verifier.Result{Signed: true, Bundle: []byte("new-key")}, nil)
			},
			wantKey: reanchorPublicKeyB64,
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
			mv.EXPECT().RetrieveOCISnapshot(gomock.Any(), latest.ref, latest.digest).
				Times(1).Return(snapshot, tc.retrieveErr)
			if tc.configure != nil {
				tc.configure(snapshot)
			}

			outcome := plugins.UpgradeOutcome{Name: tc.entry.Name}
			decision, blocked := (&service{sigVerifier: mv}).resolveOCITrustPolicy(
				t.Context(),
				plugins.UpgradeOptions{AllowSignerChange: true, PublicKey: reanchorPublicKeyB64},
				tc.entry,
				latest,
				&outcome,
			)

			assert.Equal(t, tc.wantBlocked, blocked)
			if tc.wantBlocked {
				assert.Nil(t, decision)
				assert.Equal(t, plugins.UpgradeStatusFailed, outcome.Status)
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

	keylessEntry := keyedLockEntry("my-plugin")
	keylessEntry.Provenance = signedResult().ToLockProvenance()
	oldKeyPEM, err := verifier.DecodePublicKey(testPublicKeyB64)
	require.NoError(t, err)
	latest := resolvedLatest{ref: "ghcr.io/org/my-plugin:v2", digest: validLockDigestAlt()}

	tests := []struct {
		name      string
		entry     lockfile.Entry
		configure func(*verifiermocks.MockVerifier)
	}{
		{
			name:  "keyless anchor",
			entry: keylessEntry,
			configure: func(mv *verifiermocks.MockVerifier) {
				mv.EXPECT().VerifyOCI(gomock.Any(), latest.ref, latest.digest, nil).
					Return(signedResult(), nil)
			},
		},
		{
			name:  "public key anchor",
			entry: keyedLockEntry("my-plugin"),
			configure: func(mv *verifiermocks.MockVerifier) {
				mv.EXPECT().VerifyOCIWithKey(gomock.Any(), latest.ref, latest.digest, oldKeyPEM).
					Return(&verifier.Result{Signed: true, Bundle: []byte("keyed-bundle")}, nil)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			legacyVerifier := verifiermocks.NewMockVerifier(gomock.NewController(t))
			tc.configure(legacyVerifier)
			outcome := plugins.UpgradeOutcome{Name: tc.entry.Name}

			decision, allowSignerChange, blocked := (&service{sigVerifier: legacyVerifier}).resolvePlannedTrust(
				t.Context(), plugins.UpgradeOptions{}, tc.entry, latest, true, true, &outcome,
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
		provenance: &lockfile.Provenance{PublicKey: testPublicKeyB64},
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
				decision: &provenanceDecision{provenance: &lockfile.Provenance{PublicKey: testPublicKeyB64}},
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
					provenance: &lockfile.Provenance{PublicKey: testPublicKeyB64},
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

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestUpgrade_TrustOnlyPlanModesDoNotPersist(t *testing.T) {
	tests := []struct {
		name string
		opts plugins.UpgradeOptions
	}{
		{name: "preview", opts: plugins.UpgradeOptions{Preview: true}},
		{name: "fail on changes", opts: plugins.UpgradeOptions{FailOnChanges: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inner, projectRoot, _, _ := newPluginKeyReanchorFixture(t)
			beforeEntry, ok := readLockfile(t, projectRoot).GetPlugin("my-plugin")
			require.True(t, ok)
			beforeInstalled, err := inner.store.Get(
				t.Context(), "my-plugin", plugins.ScopeProject, projectRoot,
			)
			require.NoError(t, err)

			tc.opts.ProjectRoot = projectRoot
			tc.opts.AllowSignerChange = true
			tc.opts.PublicKey = reanchorPublicKeyB64
			outcome := upgradePlugins(t, inner, tc.opts)
			assert.Equal(t, plugins.UpgradeStatusTrustUpdated, outcome.Status, "error: %s", outcome.Error)
			assert.True(t, outcome.TrustAnchorChanged)

			afterEntry, ok := readLockfile(t, projectRoot).GetPlugin("my-plugin")
			require.True(t, ok)
			assert.Equal(t, beforeEntry, afterEntry)
			afterInstalled, err := inner.store.Get(
				t.Context(), "my-plugin", plugins.ScopeProject, projectRoot,
			)
			require.NoError(t, err)
			assert.Equal(t, beforeInstalled, afterInstalled)
		})
	}
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestUpgrade_PublicKeyRejectsNonRegistrySources(t *testing.T) {
	t.Run("git", func(t *testing.T) {
		svc, projectRoot := signerChangeFixture(t, func() (*verifier.Result, error) {
			return signedResult(), nil
		})
		before, ok := loadPluginLockEntry(t, projectRoot)
		require.True(t, ok)

		outcome := upgradePlugins(t, svc, plugins.UpgradeOptions{
			ProjectRoot: projectRoot, AllowSignerChange: true, PublicKey: reanchorPublicKeyB64,
		})
		assert.Equal(t, plugins.UpgradeStatusFailed, outcome.Status)
		assert.Equal(t, plugins.FailureReasonValidationRejected, outcome.Reason)
		assert.Contains(t, outcome.Error, "applies only to OCI registry artifacts")

		after, ok := loadPluginLockEntry(t, projectRoot)
		require.True(t, ok)
		assert.Equal(t, before, after)
	})

	t.Run("local store", func(t *testing.T) {
		svc, projectRoot := newLockTestService(t)
		ociStore, err := ociplugins.NewStore(tempDir(t))
		require.NoError(t, err)
		inner := svc.(*service) //nolint:forcetypeassert
		inner.ociStore = ociStore

		_, err = svc.Install(t.Context(), plugins.InstallOptions{
			Name:          "my-plugin",
			LayerData:     makePluginLayerData(t, "my-plugin"),
			AllowUnsigned: true,
			Digest:        validLockDigest(),
			Scope:         plugins.ScopeProject,
			ProjectRoot:   projectRoot,
			Clients:       []string{"claude-code"},
		})
		require.NoError(t, err)
		before, ok := loadPluginLockEntry(t, projectRoot)
		require.True(t, ok)

		d2 := buildTestPlugin(t, ociStore, "my-plugin", "2.0.0")
		require.NoError(t, ociStore.Tag(t.Context(), d2, "my-plugin"))
		outcome := upgradePlugins(t, inner, plugins.UpgradeOptions{
			ProjectRoot: projectRoot, AllowSignerChange: true, PublicKey: reanchorPublicKeyB64,
		})
		assert.Equal(t, plugins.UpgradeStatusFailed, outcome.Status)
		assert.Equal(t, plugins.FailureReasonValidationRejected, outcome.Reason)
		assert.Contains(t, outcome.Error, "applies only to OCI registry artifacts")

		after, ok := loadPluginLockEntry(t, projectRoot)
		require.True(t, ok)
		assert.Equal(t, before, after)
	})
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestUpgrade_TrustOnlyReanchorPreservesInstalledState(t *testing.T) {
	for _, immutable := range []bool{false, true} {
		name := "mutable tag at same digest"
		if immutable {
			name = "immutable OCI digest with explicit consent"
		}
		t.Run(name, func(t *testing.T) {
			inner, projectRoot, _, newBundle := newPluginKeyReanchorFixture(t)
			root := mustOpenRoot(t, projectRoot)
			require.NoError(t, lockfile.Update(root, func(lf *lockfile.Lockfile) error {
				entry, ok := lf.GetPlugin("my-plugin")
				require.True(t, ok)
				if immutable {
					entry.Source = "ghcr.io/org/my-plugin@" + entry.Digest
					entry.ResolvedReference = entry.Source
				}
				parent := entry
				parent.Name = "parent-plugin"
				parent.Source = "ghcr.io/org/parent-plugin:v1"
				parent.ResolvedReference = parent.Source
				parent.RequiredBy = nil
				lf.UpsertPlugin(parent)
				entry.RequiredBy = []string{"parent-plugin"}
				entry.Explicit = true
				lf.UpsertPlugin(entry)
				return nil
			}))

			installed, err := inner.store.Get(t.Context(), "my-plugin", plugins.ScopeProject, projectRoot)
			require.NoError(t, err)
			installed.Dependencies = []plugins.Dependency{{
				Name: "dependency", Reference: "ghcr.io/org/dependency:v1", Digest: validLockDigestAlt(),
			}}
			require.NoError(t, inner.store.Update(t.Context(), installed))
			beforeInstalled, err := inner.store.Get(t.Context(), "my-plugin", plugins.ScopeProject, projectRoot)
			require.NoError(t, err)
			beforeEntry, ok := readLockfile(t, projectRoot).GetPlugin("my-plugin")
			require.True(t, ok)

			sentinel := filepath.Join(pluginOnDiskPath(projectRoot, "my-plugin"), "trust-only-sentinel")
			require.NoError(t, os.WriteFile(sentinel, []byte("preserve me"), 0o600))

			outcome := upgradePlugins(t, inner, plugins.UpgradeOptions{
				ProjectRoot: projectRoot, Names: []string{"my-plugin"},
				AllowSignerChange: true, PublicKey: reanchorPublicKeyB64,
			})
			assert.Equal(t, plugins.UpgradeStatusTrustUpdated, outcome.Status, "error: %s", outcome.Error)
			assert.True(t, outcome.TrustAnchorChanged)

			afterInstalled, err := inner.store.Get(t.Context(), "my-plugin", plugins.ScopeProject, projectRoot)
			require.NoError(t, err)
			expectedInstalled := beforeInstalled
			expectedInstalled.SigstoreBundle = newBundle
			assert.Equal(t, expectedInstalled, afterInstalled,
				"trust-only persistence may change only the stored bundle")

			afterEntry, ok := readLockfile(t, projectRoot).GetPlugin("my-plugin")
			require.True(t, ok)
			expectedEntry := beforeEntry
			expectedEntry.Provenance = &lockfile.Provenance{PublicKey: reanchorPublicKeyB64}
			expectedEntry.Unsigned = false
			assert.Equal(t, expectedEntry, afterEntry,
				"trust-only persistence must preserve source, digests, dependencies, and explicit state")

			sentinelContents, err := os.ReadFile(sentinel)
			require.NoError(t, err, "trust-only updates must not replace the extracted directory")
			assert.Equal(t, []byte("preserve me"), sentinelContents)
		})
	}
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestUpgrade_ChangedContentUsesPreverifiedReanchor(t *testing.T) {
	inner, projectRoot, publishV2, newBundle := newPluginKeyReanchorFixture(t)
	publishV2()
	before, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)

	outcome := upgradePlugins(t, inner, plugins.UpgradeOptions{
		ProjectRoot: projectRoot, AllowSignerChange: true, PublicKey: reanchorPublicKeyB64,
	})
	assert.Equal(t, plugins.UpgradeStatusUpgraded, outcome.Status, "error: %s", outcome.Error)
	assert.True(t, outcome.TrustAnchorChanged)
	assert.NotEqual(t, before.Digest, outcome.NewDigest)

	after, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)
	require.NotNil(t, after.Provenance)
	assert.Equal(t, reanchorPublicKeyB64, after.Provenance.PublicKey)
	assert.Equal(t, outcome.NewDigest, after.Digest)
	installed, err := inner.store.Get(t.Context(), "my-plugin", plugins.ScopeProject, projectRoot)
	require.NoError(t, err)
	assert.Equal(t, newBundle, installed.SigstoreBundle)
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestUpgrade_ContentApplyDetectsConflictAfterDatabaseWrite(t *testing.T) {
	inner, projectRoot, publishV2, _ := newPluginKeyReanchorFixture(t)
	publishV2()
	beforeInstalled, err := inner.store.Get(t.Context(), "my-plugin", plugins.ScopeProject, projectRoot)
	require.NoError(t, err)

	concurrentDigest := validLockDigestAlt()
	var hookErr error
	hookedStore := &hookPluginStore{
		PluginStore: inner.store,
		afterUpdate: func(_ context.Context, call int) {
			if call != 1 {
				return
			}
			root := mustOpenRoot(t, projectRoot)
			hookErr = lockfile.Update(root, func(lf *lockfile.Lockfile) error {
				entry, exists := lf.GetPlugin("my-plugin")
				if !exists {
					return fmt.Errorf("planned lock entry disappeared")
				}
				entry.Digest = concurrentDigest
				lf.UpsertPlugin(entry)
				return nil
			})
		},
	}
	inner.store = hookedStore

	result, err := inner.Upgrade(t.Context(), plugins.UpgradeOptions{
		ProjectRoot: projectRoot, AllowSignerChange: true, PublicKey: reanchorPublicKeyB64,
	})
	require.NoError(t, err)
	require.NoError(t, hookErr)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, plugins.UpgradeStatusFailed, result.Outcomes[0].Status)
	assert.Contains(t, result.Outcomes[0].Error, lockfile.ErrEntryChanged.Error())
	assert.Equal(t, 2, hookedStore.updateCalls,
		"the database update must be compensated after the lock CAS conflict")

	afterInstalled, getErr := inner.store.Get(t.Context(), "my-plugin", plugins.ScopeProject, projectRoot)
	require.NoError(t, getErr)
	assert.Equal(t, beforeInstalled, afterInstalled,
		"the failed content apply must restore the database record")
	afterEntry, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)
	assert.Equal(t, concurrentDigest, afterEntry.Digest,
		"rollback must not overwrite the competing lock update with its stale snapshot")
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestPersistTrustOnlyUpgradeDetectsConflictAfterDatabaseWrite(t *testing.T) {
	inner, projectRoot := newInstalledPluginKeyFixture(t)
	planned, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)
	beforeInstalled, err := inner.store.Get(t.Context(), "my-plugin", plugins.ScopeProject, projectRoot)
	require.NoError(t, err)

	concurrentDigest := validLockDigestAlt()
	var hookErr error
	hookedStore := &hookPluginStore{
		PluginStore: inner.store,
		afterUpdate: func(_ context.Context, call int) {
			if call != 1 {
				return
			}
			root := mustOpenRoot(t, projectRoot)
			hookErr = lockfile.Update(root, func(lf *lockfile.Lockfile) error {
				entry, exists := lf.GetPlugin(planned.Name)
				if !exists {
					return fmt.Errorf("planned lock entry disappeared")
				}
				entry.Digest = concurrentDigest
				lf.UpsertPlugin(entry)
				return nil
			})
		},
	}
	inner.store = hookedStore

	plan := trustOnlyReanchorPlan(planned)
	err = inner.persistTrustOnlyUpgrade(t.Context(), projectRoot, plan)
	require.Error(t, err)
	require.NoError(t, hookErr)
	assert.Equal(t, http.StatusConflict, httperr.Code(err))
	assert.Equal(t, 2, hookedStore.updateCalls,
		"the database update must be compensated after the lock CAS conflict")

	afterInstalled, getErr := inner.store.Get(t.Context(), "my-plugin", plugins.ScopeProject, projectRoot)
	require.NoError(t, getErr)
	assert.Equal(t, beforeInstalled, afterInstalled)
	afterEntry, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)
	assert.Equal(t, concurrentDigest, afterEntry.Digest,
		"the competing lock update must survive the failed trust-only persistence")
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestPersistTrustOnlyUpgradeRollbackSurvivesCallerCancellation(t *testing.T) {
	inner, projectRoot := newInstalledPluginKeyFixture(t)
	planned, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)
	beforeInstalled, err := inner.store.Get(t.Context(), "my-plugin", plugins.ScopeProject, projectRoot)
	require.NoError(t, err)
	beforeEntry, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)

	callerCtx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	lockPath := filepath.Join(projectRoot, lockfile.FileName)
	backupPath := filepath.Join(projectRoot, "lock-before-canceled-failure.yaml")
	var updateContextErrors []error
	var hookErr error
	hookedStore := &hookPluginStore{
		PluginStore: inner.store,
		afterUpdate: func(updateCtx context.Context, call int) {
			updateContextErrors = append(updateContextErrors, updateCtx.Err())
			if call != 1 {
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
	inner.store = hookedStore
	restoreLock := func() {
		if _, statErr := os.Stat(backupPath); statErr != nil {
			return
		}
		_ = os.Remove(lockPath)
		_ = os.Rename(backupPath, lockPath)
	}
	t.Cleanup(restoreLock)

	err = inner.persistTrustOnlyUpgrade(callerCtx, projectRoot, trustOnlyReanchorPlan(planned))
	require.Error(t, err)
	require.NoError(t, hookErr)
	assert.Contains(t, err.Error(), "updating lock trust anchor")
	require.Len(t, updateContextErrors, 2,
		"the durable update must be followed by a compensating database update")
	assert.NoError(t, updateContextErrors[0])
	assert.NoError(t, updateContextErrors[1],
		"compensation must detach from the canceled request context")

	restoreLock()
	afterInstalled, getErr := inner.store.Get(t.Context(), "my-plugin", plugins.ScopeProject, projectRoot)
	require.NoError(t, getErr)
	assert.Equal(t, beforeInstalled, afterInstalled)
	afterEntry, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)
	assert.Equal(t, beforeEntry, afterEntry)
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestUpgrade_SameDigestCrossRepositoryMoveIsBlocked(t *testing.T) {
	inner, projectRoot := newInstalledPluginKeyFixture(t)
	newRef := "ghcr.io/new-org/my-plugin:v2"
	require.NoError(t, lockfile.Update(mustOpenRoot(t, projectRoot), func(lf *lockfile.Lockfile) error {
		entry, ok := lf.GetPlugin("my-plugin")
		require.True(t, ok)
		entry.Source = newRef
		lf.UpsertPlugin(entry)
		return nil
	}))
	before, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)

	result, err := inner.Upgrade(t.Context(), plugins.UpgradeOptions{
		ProjectRoot: projectRoot, AllowSignerChange: true, PublicKey: reanchorPublicKeyB64,
	})
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	outcome := result.Outcomes[0]
	assert.Equal(t, plugins.UpgradeStatusRefChangeBlocked, outcome.Status)
	assert.Equal(t, before.Digest, outcome.NewDigest)
	assert.Equal(t, newRef, outcome.NewResolvedReference)

	after, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)
	assert.Equal(t, before, after, "a blocked same-digest repository move must not rewrite the lock entry")
}

//nolint:paralleltest // serial: real sqlite + on-disk client materialization per test
func TestUpgrade_SameDigestCrossRepositoryMovePersistsAuthorizedReference(t *testing.T) {
	inner, projectRoot, _, newBundle := newPluginKeyReanchorFixture(t)
	newRef := "ghcr.io/new-org/my-plugin:v2"
	require.NoError(t, lockfile.Update(mustOpenRoot(t, projectRoot), func(lf *lockfile.Lockfile) error {
		entry, ok := lf.GetPlugin("my-plugin")
		require.True(t, ok)
		entry.Source = newRef
		lf.UpsertPlugin(entry)
		return nil
	}))
	before, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)
	sentinel := filepath.Join(pluginOnDiskPath(projectRoot, "my-plugin"), "same-digest-sentinel")
	require.NoError(t, os.WriteFile(sentinel, []byte("preserve me"), 0o600))

	result, err := inner.Upgrade(t.Context(), plugins.UpgradeOptions{
		ProjectRoot:       projectRoot,
		AllowRefChange:    true,
		AllowSignerChange: true,
		PublicKey:         reanchorPublicKeyB64,
	})
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	outcome := result.Outcomes[0]
	assert.Equal(t, plugins.UpgradeStatusUpgraded, outcome.Status, "error: %s", outcome.Error)
	assert.True(t, outcome.TrustAnchorChanged)
	assert.Equal(t, before.Digest, outcome.NewDigest)
	assert.Equal(t, newRef, outcome.NewResolvedReference)

	after, ok := loadPluginLockEntry(t, projectRoot)
	require.True(t, ok)
	assert.Equal(t, newRef, after.ResolvedReference)
	require.NotNil(t, after.Provenance)
	assert.Equal(t, reanchorPublicKeyB64, after.Provenance.PublicKey)
	assert.Equal(t, before.Digest, after.Digest)

	installed, err := inner.store.Get(t.Context(), "my-plugin", plugins.ScopeProject, projectRoot)
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

func trustOnlyReanchorPlan(entry lockfile.Entry) upgradePlan {
	return upgradePlan{
		entry: entry,
		outcome: plugins.UpgradeOutcome{
			Name: entry.Name, OldDigest: entry.Digest, NewDigest: entry.Digest,
			Status: plugins.UpgradeStatusTrustUpdated,
		},
		trustDecision: &provenanceDecision{
			provenance: &lockfile.Provenance{PublicKey: reanchorPublicKeyB64},
			bundle:     []byte("replacement-bundle"),
		},
	}
}

func newPluginKeyReanchorFixture(t *testing.T) (*service, string, func(), []byte) {
	t.Helper()

	ctrl := gomock.NewController(t)
	oldKeyPEM, err := verifier.DecodePublicKey(testPublicKeyB64)
	require.NoError(t, err)
	newKeyPEM, err := verifier.DecodePublicKey(reanchorPublicKeyB64)
	require.NoError(t, err)

	mv := verifiermocks.NewMockOCISnapshotVerifier(ctrl)
	mv.EXPECT().VerifyOCIWithKey(gomock.Any(), gomock.Any(), gomock.Any(), oldKeyPEM).
		Return(&verifier.Result{Signed: true, Bundle: []byte("installed-bundle")}, nil)
	inner, projectRoot, publishV2 := keyPinnedUpgradeFixture(t, mv)

	newBundle := []byte("replacement-bundle")
	snapshot := verifiermocks.NewMockOCISnapshot(ctrl)
	mv.EXPECT().RetrieveOCISnapshot(gomock.Any(), gomock.Any(), gomock.Any()).
		Times(1).Return(snapshot, nil)
	gomock.InOrder(
		snapshot.EXPECT().VerifyWithKey(oldKeyPEM).Return(nil, verifier.ErrSignatureInvalid),
		snapshot.EXPECT().VerifyWithKey(newKeyPEM).
			Return(&verifier.Result{Signed: true, Bundle: newBundle}, nil),
	)
	return inner, projectRoot, publishV2, newBundle
}

func newInstalledPluginKeyFixture(t *testing.T) (*service, string) {
	t.Helper()

	oldKeyPEM, err := verifier.DecodePublicKey(testPublicKeyB64)
	require.NoError(t, err)
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyOCIWithKey(gomock.Any(), gomock.Any(), gomock.Any(), oldKeyPEM).
		Return(&verifier.Result{Signed: true, Bundle: []byte("installed-bundle")}, nil)
	inner, projectRoot, _ := keyPinnedUpgradeFixture(t, mv)
	return inner, projectRoot
}

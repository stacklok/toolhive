// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"fmt"
	"net/http"
	"reflect"
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

const (
	testSignerIdentity    = "/.github/workflows/release.yml"
	testCertIssuer        = "https://token.actions.githubusercontent.com"
	testRunnerEnvironment = "github-hosted"
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

// refSignedResult is a verification result whose certificate pins ref and the
// standard runner class, as a GitHub Actions certificate does.
func refSignedResult(ref string) *verifier.Result {
	r := signedResult()
	r.RepositoryRef = ref
	r.RunnerEnvironment = testRunnerEnvironment
	return r
}

// loadLockEntry reads the lock entry for name from projectRoot.
func loadLockEntry(t *testing.T, projectRoot, name string) (lockfile.Entry, bool) {
	t.Helper()
	root, err := lockfile.OpenRoot(projectRoot)
	require.NoError(t, err)
	lf, err := lockfile.Load(root)
	require.NoError(t, err)
	return lf.Get(name)
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestInstallVerification_TOFURecordsProvenance(t *testing.T) {
	gr, fixtures := newGitResolverMock(t)
	ref, _ := gitRef("signed-skill")
	fixtures.register("signed-skill", gitSkill("signed-skill"))

	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	// First install: no lock entry yet — trust on first use, nil expected.
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Nil()).
		Return(signedResult(), nil)

	svc, projectRoot := newLockTestService(t, gr, WithVerifier(mv))
	result, err := svc.Install(t.Context(), skills.InstallOptions{
		Name:        ref,
		Scope:       skills.ScopeProject,
		ProjectRoot: projectRoot,
		Clients:     []string{"claude-code"},
	})
	require.NoError(t, err)

	entry, ok := loadLockEntry(t, projectRoot, "signed-skill")
	require.True(t, ok)
	require.NotNil(t, entry.Provenance, "TOFU must record the observed identity")
	assert.Equal(t, testSignerIdentity, entry.Provenance.SignerIdentity)
	assert.Equal(t, testCertIssuer, entry.Provenance.CertIssuer)
	assert.False(t, entry.Unsigned)
	assert.Equal(t, []byte(`{"bundle":true}`), result.Skill.SigstoreBundle,
		"the bundle must be persisted with the install record")

	// Second install: the recorded identity must flow into the verifier as
	// the expected identity.
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ any, _, _ []byte, expected *lockfile.Provenance) (*verifier.Result, error) {
			require.NotNil(t, expected, "the second install must enforce the recorded identity")
			assert.Equal(t, testSignerIdentity, expected.SignerIdentity)
			return signedResult(), nil
		})
	_, err = svc.Install(t.Context(), skills.InstallOptions{
		Name:        ref,
		Scope:       skills.ScopeProject,
		ProjectRoot: projectRoot,
		Clients:     []string{"claude-code"},
		Force:       true,
	})
	require.NoError(t, err)
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestInstallVerification_UnsignedRejectedWithoutFlag(t *testing.T) {
	gr, fixtures := newGitResolverMock(t)
	ref, _ := gitRef("unsigned-skill")
	fixtures.register("unsigned-skill", gitSkill("unsigned-skill"))

	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Nil()).
		Return(nil, verifier.ErrUnsigned)

	svc, projectRoot := newLockTestService(t, gr, WithVerifier(mv))
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name:        ref,
		Scope:       skills.ScopeProject,
		ProjectRoot: projectRoot,
		Clients:     []string{"claude-code"},
	})
	require.Error(t, err)
	assert.Equal(t, http.StatusForbidden, httperr.Code(err))

	_, ok := loadLockEntry(t, projectRoot, "unsigned-skill")
	assert.False(t, ok, "a rejected install must not write a lock entry")
	_, err = svc.Info(t.Context(), skills.InfoOptions{
		Name: "unsigned-skill", Scope: skills.ScopeProject, ProjectRoot: projectRoot,
	})
	require.Error(t, err, "a rejected install must not create a DB record")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestInstallVerification_UnsignedAcceptedWithFlag(t *testing.T) {
	gr, fixtures := newGitResolverMock(t)
	ref, _ := gitRef("unsigned-ok")
	fixtures.register("unsigned-ok", gitSkill("unsigned-ok"))

	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Nil()).
		Return(nil, verifier.ErrUnsigned)

	svc, projectRoot := newLockTestService(t, gr, WithVerifier(mv))
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name:          ref,
		Scope:         skills.ScopeProject,
		ProjectRoot:   projectRoot,
		Clients:       []string{"claude-code"},
		AllowUnsigned: true,
	})
	require.NoError(t, err)

	entry, ok := loadLockEntry(t, projectRoot, "unsigned-ok")
	require.True(t, ok)
	assert.True(t, entry.Unsigned, "the unsigned exception must be recorded")
	assert.Nil(t, entry.Provenance)
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestInstallVerification_SignerMismatchRejectedAndLockIntact(t *testing.T) {
	gr, fixtures := newGitResolverMock(t)
	ref, _ := gitRef("pinned-skill")
	fixtures.register("pinned-skill", gitSkill("pinned-skill"))

	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Nil()).
		Return(signedResult(), nil)

	svc, projectRoot := newLockTestService(t, gr, WithVerifier(mv))
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name:        ref,
		Scope:       skills.ScopeProject,
		ProjectRoot: projectRoot,
		Clients:     []string{"claude-code"},
	})
	require.NoError(t, err)

	// The re-install is signed by someone else: the verifier reports a
	// mismatch (the expected identity was bound into its policy).
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, verifier.ErrSignerMismatch)
	_, err = svc.Install(t.Context(), skills.InstallOptions{
		Name:        ref,
		Scope:       skills.ScopeProject,
		ProjectRoot: projectRoot,
		Clients:     []string{"claude-code"},
		Force:       true,
	})
	require.Error(t, err)
	assert.Equal(t, http.StatusForbidden, httperr.Code(err))

	// The prior trusted state is untouched.
	entry, ok := loadLockEntry(t, projectRoot, "pinned-skill")
	require.True(t, ok)
	require.NotNil(t, entry.Provenance)
	assert.Equal(t, testSignerIdentity, entry.Provenance.SignerIdentity)
}

// TestInstallVerification_EnforcesPinnedRef proves install gets no re-pin
// relaxation: unlike upgrade, a reinstall that resolves to a certificate
// signed on a different ref is a substitution, and the recorded ref must
// reach the verifier as the expected one.
//
//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestInstallVerification_EnforcesPinnedRef(t *testing.T) {
	gr, fixtures := newGitResolverMock(t)
	ref, _ := gitRef("ref-pinned-skill")
	fixtures.register("ref-pinned-skill", gitSkill("ref-pinned-skill"))

	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Nil()).
		Return(refSignedResult("refs/tags/v0.1.0"), nil)

	svc, projectRoot := newLockTestService(t, gr, WithVerifier(mv))
	installOpts := skills.InstallOptions{
		Name:        ref,
		Scope:       skills.ScopeProject,
		ProjectRoot: projectRoot,
		Clients:     []string{"claude-code"},
	}
	_, err := svc.Install(t.Context(), installOpts)
	require.NoError(t, err)

	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ any, _, _ []byte, expected *lockfile.Provenance) (*verifier.Result, error) {
			require.NotNil(t, expected)
			assert.Equal(t, "refs/tags/v0.1.0", expected.RepositoryRef,
				"install must enforce the recorded ref, not relax it like upgrade")
			return nil, verifier.ErrSignerMismatch
		})
	installOpts.Force = true
	_, err = svc.Install(t.Context(), installOpts)
	require.Error(t, err)
	assert.Equal(t, http.StatusForbidden, httperr.Code(err))

	entry, ok := loadLockEntry(t, projectRoot, "ref-pinned-skill")
	require.True(t, ok)
	require.NotNil(t, entry.Provenance)
	assert.Equal(t, "refs/tags/v0.1.0", entry.Provenance.RepositoryRef, "the rejected install must not re-pin")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestInstallVerification_LockedUnsignedRequiresFlagAgain(t *testing.T) {
	gr, fixtures := newGitResolverMock(t)
	ref, _ := gitRef("unsigned-locked")
	fixtures.register("unsigned-locked", gitSkill("unsigned-locked"))

	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Nil()).
		Return(nil, verifier.ErrUnsigned)

	svc, projectRoot := newLockTestService(t, gr, WithVerifier(mv))
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name:          ref,
		Scope:         skills.ScopeProject,
		ProjectRoot:   projectRoot,
		Clients:       []string{"claude-code"},
		AllowUnsigned: true,
	})
	require.NoError(t, err)

	// Reinstall without the flag: the locked unsigned exception does not
	// silently renew — the verifier is not even consulted.
	_, err = svc.Install(t.Context(), skills.InstallOptions{
		Name:        ref,
		Scope:       skills.ScopeProject,
		ProjectRoot: projectRoot,
		Clients:     []string{"claude-code"},
		Force:       true,
	})
	require.Error(t, err)
	assert.Equal(t, http.StatusForbidden, httperr.Code(err))
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestInstallVerification_UserScopeSkipsVerification(t *testing.T) {
	gr, fixtures := newGitResolverMock(t)
	ref, _ := gitRef("user-skill")
	fixtures.register("user-skill", gitSkill("user-skill"))

	// The mock has no expectations: any verifier call fails the test.
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))

	svc, _ := newLockTestService(t, gr, WithVerifier(mv))
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name:    ref,
		Scope:   skills.ScopeUser,
		Clients: []string{"claude-code"},
	})
	require.NoError(t, err)
}

func TestVerifyLocalInstall(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		opts     skills.InstallOptions
		entry    *lockfile.Entry
		wantErr  bool
		unsigned bool
	}{
		{
			name:    "no flag rejected",
			opts:    skills.InstallOptions{},
			wantErr: true,
		},
		{
			name:     "flag records unsigned",
			opts:     skills.InstallOptions{AllowUnsigned: true},
			unsigned: true,
		},
		{
			name: "locked identity refuses local replacement even with flag",
			opts: skills.InstallOptions{AllowUnsigned: true},
			entry: &lockfile.Entry{
				Name:              "local-skill",
				Source:            "example.com/org/local-skill",
				ResolvedReference: "example.com/org/local-skill:v1",
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
			opts: skills.InstallOptions{AllowUnsigned: true},
			entry: &lockfile.Entry{
				Name:              "local-skill",
				Source:            "example.com/org/local-skill",
				ResolvedReference: "example.com/org/local-skill:v1",
				Digest:            "sha256:" + strings.Repeat("a", 64),
				Unsigned:          true,
			},
			unsigned: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			projectRoot := makeProjectRoot(t)
			if tc.entry != nil {
				root, err := lockfile.OpenRoot(projectRoot)
				require.NoError(t, err)
				require.NoError(t, lockfile.Update(root, func(lf *lockfile.Lockfile) error {
					lf.Upsert(*tc.entry)
					return nil
				}))
			}
			opts := tc.opts
			opts.ProjectRoot = projectRoot

			decision, err := verifyLocalInstall(opts, "local-skill")
			if tc.wantErr {
				require.Error(t, err)
				assert.Equal(t, http.StatusForbidden, httperr.Code(err))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.unsigned, decision.unsigned)
		})
	}
}

// TestProvenanceConversionsPreserveEveryField guards the lock <-> API
// plumbing. A field added to one provenance shape but forgotten in a
// conversion silently drops recorded trust data in transit, and no
// printer-level test would notice.
func TestProvenanceConversionsPreserveEveryField(t *testing.T) {
	t.Parallel()

	locked := &lockfile.Provenance{
		SignerIdentity:    testSignerIdentity,
		CertIssuer:        testCertIssuer,
		RepositoryURI:     "https://github.com/org/repo",
		RepositoryRef:     "refs/heads/main",
		RunnerEnvironment: "github-hosted",
		SigstoreURL:       "https://rekor.sigstore.dev",
		Provisional:       true,
	}
	requireAllFieldsSet(t, locked)

	info := provenanceInfoFromLock(locked)
	requireAllFieldsSet(t, info)
	assert.Equal(t, locked, provenanceInfoToLock(info))

	assert.Nil(t, provenanceInfoFromLock(nil))
	assert.Nil(t, provenanceInfoToLock(nil))
}

// requireAllFieldsSet fails when any field of the struct pointed to by v holds
// its zero value, so a field added to one provenance shape without a matching
// line in the conversions is caught here rather than in production.
func requireAllFieldsSet(t *testing.T, v any) {
	t.Helper()
	rv := reflect.ValueOf(v).Elem()
	for i := range rv.NumField() {
		assert.False(t, rv.Field(i).IsZero(),
			"%s.%s is zero: wire it through the provenance conversions and this fixture",
			rv.Type().Name(), rv.Type().Field(i).Name)
	}
}

func TestClassifySignatureError(t *testing.T) {
	t.Parallel()
	assert.Equal(t, skills.FailureReasonSignerMismatch, classifySignatureError(verifier.ErrSignerMismatch))
	assert.Equal(t, skills.FailureReasonUnsignedRejected, classifySignatureError(verifier.ErrUnsigned))
	assert.Equal(t, skills.FailureReasonSignatureInvalid, classifySignatureError(verifier.ErrSignatureInvalid))
	assert.Equal(t, skills.FailureReason(""), classifySignatureError(assert.AnError))

	// A pinned ref/runner mismatch satisfies errors.Is against BOTH
	// ErrSignerMismatch and ErrProvenanceFieldMismatch (see
	// verifier.pinnedFieldMismatch) — the more specific reason must win, or
	// every version bump on a ref-pinned skill would misreport as a
	// publisher change rather than a provenance-field change.
	fieldMismatch := fmt.Errorf("%w: %w: locked to repository ref, but the artifact carries a different one",
		verifier.ErrSignerMismatch, verifier.ErrProvenanceFieldMismatch)
	assert.Equal(t, skills.FailureReasonProvenanceFieldMismatch, classifySignatureError(fieldMismatch))
}

// TestClassifyInstallVerifyErrorDistinguishesProvenanceField covers the
// install-time (403) classification alongside TestClassifySignatureError's
// sync/upgrade coverage: a pinned ref/runner mismatch must not be reported
// to the operator as a signer-identity change.
func TestClassifyInstallVerifyErrorDistinguishesProvenanceField(t *testing.T) {
	t.Parallel()

	fieldMismatch := fmt.Errorf("%w: %w: locked to repository ref, but the artifact carries a different one",
		verifier.ErrSignerMismatch, verifier.ErrProvenanceFieldMismatch)
	err := classifyInstallVerifyError(fieldMismatch, "some-skill", &lockfile.Provenance{SignerIdentity: testSignerIdentity})
	assert.Contains(t, err.Error(), "no longer matches its pinned provenance",
		"a provenance-field mismatch must lead with the field-specific wording, not the identity one")
	assert.NotContains(t, err.Error(), "signer identity mismatch for",
		"the identity-specific phrasing (distinct from ErrSignerMismatch's own wrapped message text) must not appear")

	identityMismatch := classifyInstallVerifyError(
		verifier.ErrSignerMismatch, "some-skill", &lockfile.Provenance{SignerIdentity: testSignerIdentity})
	assert.Contains(t, identityMismatch.Error(), "signer identity mismatch for",
		"a genuine signer-identity mismatch keeps its existing wording")
}

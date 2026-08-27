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
	regtypes "github.com/stacklok/toolhive-core/registry/types"
	regmocks "github.com/stacklok/toolhive/pkg/registry/mocks"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
	gitmocks "github.com/stacklok/toolhive/pkg/skills/gitresolver/mocks"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	"github.com/stacklok/toolhive/pkg/skills/verifier"
	verifiermocks "github.com/stacklok/toolhive/pkg/skills/verifier/mocks"
)

const (
	testSignerIdentity    = "/.github/workflows/release.yml"
	testCertIssuer        = "https://token.actions.githubusercontent.com"
	testRunnerEnvironment = "github-hosted"
)

// testPublicKeyB64 is a real P-256 public key in the base64 DER SPKI
// form a key-pinned lock entry stores. It must genuinely parse: validation
// rejects a value that merely decodes as base64.
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

// writeLockEntry seeds project trust state without first passing through the
// install path under test.
func writeLockEntry(t *testing.T, projectRoot string, entry lockfile.Entry) {
	t.Helper()
	root, err := lockfile.OpenRoot(projectRoot)
	require.NoError(t, err)
	require.NoError(t, lockfile.UpsertEntry(root, entry))
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
		DoAndReturn(func(_ any, _, _ []byte, expected *verifier.ProvenanceExpectation) (*verifier.Result, error) {
			require.NotNil(t, expected, "the second install must enforce the recorded identity")
			assert.Equal(t, verifier.NewLockExpectation(entry.Provenance), expected)
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

// catalogSkill returns a registry/catalog entry for "catalog-skill" resolving
// to a git package, declaring the given provenance (RFC THV-0080 follow-up
// #6310).
func catalogSkill(provenance *regtypes.Provenance) regtypes.Skill {
	return regtypes.Skill{
		Namespace: "io.github.test",
		Name:      "catalog-skill",
		Packages: []regtypes.SkillPackage{
			{RegistryType: "git", URL: "https://github.com/test/catalog-skill"},
		},
		Provenance: provenance,
	}
}

// catalogSkillFiles is the git-resolved content installFromGit expects for
// the catalogSkill fixture.
func catalogSkillFiles() *gitresolver.ResolveResult {
	return &gitresolver.ResolveResult{
		SkillConfig: &skills.ParseResult{Name: "catalog-skill", Version: "1.0.0"},
		Files: []gitresolver.FileEntry{
			{Path: "SKILL.md", Content: gitSkill("catalog-skill"), Mode: 0o644},
		},
		CommitHash: testCommitHash,
	}
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestInstallVerification_CatalogProvenanceUsedOnFirstUse(t *testing.T) {
	gr := gitmocks.NewMockResolver(gomock.NewController(t))
	gr.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(catalogSkillFiles(), nil)

	lookup := regmocks.NewMockProvider(gomock.NewController(t))
	lookup.EXPECT().SearchSkills("catalog-skill").Return([]regtypes.Skill{
		catalogSkill(&regtypes.Provenance{
			SignerIdentity: testSignerIdentity,
			CertIssuer:     testCertIssuer,
		}),
	}, nil)

	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	// No lock entry exists yet, so the catalog expectation and its wildcard
	// semantics must reach the verifier distinctly from strict lock trust.
	catalogExpected := &regtypes.Provenance{SignerIdentity: testSignerIdentity, CertIssuer: testCertIssuer}
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ any, _, _ []byte, expected *verifier.ProvenanceExpectation) (*verifier.Result, error) {
			require.NotNil(t, expected, "a catalog-declared provenance must be checked on first install")
			assert.Equal(t, verifier.NewCatalogExpectation(catalogExpected), expected)
			return signedResult(), nil
		})

	svc, projectRoot := newLockTestService(t, gr, WithVerifier(mv), WithSkillLookup(lookup))
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name:        "catalog-skill",
		Scope:       skills.ScopeProject,
		ProjectRoot: projectRoot,
		Clients:     []string{"claude-code"},
	})
	require.NoError(t, err)

	entry, ok := loadLockEntry(t, projectRoot, "catalog-skill")
	require.True(t, ok)
	assert.Equal(t, testSignerIdentity, entry.Provenance.SignerIdentity)
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestInstallVerification_LockEntryTakesPrecedenceOverCatalog(t *testing.T) {
	gr := gitmocks.NewMockResolver(gomock.NewController(t))
	gr.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(catalogSkillFiles(), nil)

	lookup := regmocks.NewMockProvider(gomock.NewController(t))
	// The catalog declares a different identity from the pre-existing lock.
	lookup.EXPECT().SearchSkills("catalog-skill").Return([]regtypes.Skill{
		catalogSkill(&regtypes.Provenance{SignerIdentity: "/.github/workflows/other.yml", CertIssuer: testCertIssuer}),
	}, nil)

	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	svc, projectRoot := newLockTestService(t, gr, WithVerifier(mv), WithSkillLookup(lookup))
	writeLockEntry(t, projectRoot, lockfile.Entry{
		Name:              "catalog-skill",
		Source:            "catalog-skill",
		ResolvedReference: "git://github.com/test/catalog-skill",
		Digest:            testCommitHash,
		Explicit:          true,
		Provenance: &lockfile.Provenance{
			SignerIdentity: testSignerIdentity,
			CertIssuer:     testCertIssuer,
		},
	})

	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ any, _, _ []byte, expected *verifier.ProvenanceExpectation) (*verifier.Result, error) {
			require.NotNil(t, expected)
			assert.Equal(t, verifier.NewLockExpectation(&lockfile.Provenance{
				SignerIdentity: testSignerIdentity,
				CertIssuer:     testCertIssuer,
			}), expected,
				"the lock entry, not the catalog, must be enforced once one exists")
			return signedResult(), nil
		})
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name:        "catalog-skill",
		Scope:       skills.ScopeProject,
		ProjectRoot: projectRoot,
		Clients:     []string{"claude-code"},
	})
	require.NoError(t, err)
}

func TestVerifyInstall_CatalogPartialConstraints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		provenance *regtypes.Provenance
		observed   *verifier.Result
	}{
		{name: "empty provenance constrains nothing", provenance: &regtypes.Provenance{}, observed: signedResult()},
		{name: "signer only", provenance: &regtypes.Provenance{SignerIdentity: testSignerIdentity}, observed: signedResult()},
		{name: "issuer only", provenance: &regtypes.Provenance{CertIssuer: testCertIssuer}, observed: signedResult()},
		{name: "repository only", provenance: &regtypes.Provenance{RepositoryURI: "https://github.com/org/repo"}, observed: signedResult()},
		{name: "ref only", provenance: &regtypes.Provenance{RepositoryRef: "refs/tags/v1.0.0"}, observed: refSignedResult("refs/tags/v1.0.0")},
		{name: "runner only", provenance: &regtypes.Provenance{RunnerEnvironment: testRunnerEnvironment}, observed: refSignedResult("refs/tags/v1.0.0")},
	}
	for _, backend := range []string{"git", "oci"} {
		for _, tc := range tests {
			t.Run(backend+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				projectRoot := makeProjectRoot(t)
				mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
				svc := &service{sigVerifier: mv}
				opts := skills.InstallOptions{
					ProjectRoot:       projectRoot,
					CatalogProvenance: tc.provenance,
				}

				var (
					decision *provenanceDecision
					err      error
				)
				wantExpected := verifier.NewCatalogExpectation(tc.provenance)
				if tc.provenance.SignerIdentity == "" && tc.provenance.CertIssuer == "" &&
					tc.provenance.RepositoryURI == "" && tc.provenance.RepositoryRef == "" &&
					tc.provenance.RunnerEnvironment == "" {
					wantExpected = nil
				}
				if backend == "git" {
					mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Eq(wantExpected)).
						Return(tc.observed, nil)
					decision, err = svc.verifyGitInstall(t.Context(), opts, "catalog-skill", []byte("payload"), "signature")
				} else {
					mv.EXPECT().VerifyOCI(
						gomock.Any(), "ghcr.io/test/catalog-skill:v1", "sha256:digest", gomock.Eq(wantExpected)).
						Return(tc.observed, nil)
					decision, err = svc.verifyOCIInstall(
						t.Context(), opts, "catalog-skill", "ghcr.io/test/catalog-skill:v1", "sha256:digest")
				}
				require.NoError(t, err)
				require.NotNil(t, decision.provenance)
				assert.Equal(t, tc.observed.SignerIdentity, decision.provenance.SignerIdentity)
			})
		}
	}
}

func TestVerifyInstall_CatalogPartialConstraintMismatchRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		provenance *regtypes.Provenance
	}{
		{name: "signer", provenance: &regtypes.Provenance{SignerIdentity: "attacker@example.com"}},
		{name: "issuer", provenance: &regtypes.Provenance{CertIssuer: "https://issuer.example.com"}},
		{name: "repository", provenance: &regtypes.Provenance{RepositoryURI: "https://github.com/attacker/repo"}},
		{name: "ref", provenance: &regtypes.Provenance{RepositoryRef: "refs/heads/attacker"}},
		{name: "runner", provenance: &regtypes.Provenance{RunnerEnvironment: "self-hosted"}},
	}
	for _, backend := range []string{"git", "oci"} {
		for _, tc := range tests {
			t.Run(backend+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				projectRoot := makeProjectRoot(t)
				mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
				svc := &service{sigVerifier: mv}
				opts := skills.InstallOptions{ProjectRoot: projectRoot, CatalogProvenance: tc.provenance}
				wantExpected := verifier.NewCatalogExpectation(tc.provenance)

				var err error
				if backend == "git" {
					mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Eq(wantExpected)).
						Return(nil, verifier.ErrSignerMismatch)
					_, err = svc.verifyGitInstall(t.Context(), opts, "catalog-skill", []byte("payload"), "signature")
				} else {
					mv.EXPECT().VerifyOCI(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Eq(wantExpected)).
						Return(nil, verifier.ErrSignerMismatch)
					_, err = svc.verifyOCIInstall(
						t.Context(), opts, "catalog-skill", "ghcr.io/test/catalog-skill:v1", "sha256:digest")
				}
				require.Error(t, err)
				assert.Equal(t, http.StatusForbidden, httperr.Code(err))
			})
		}
	}
}

func TestVerifyInstall_CatalogIdentityPairPreservesCatalogSemantics(t *testing.T) {
	t.Parallel()

	for _, backend := range []string{"git", "oci"} {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			projectRoot := makeProjectRoot(t)
			mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
			svc := &service{sigVerifier: mv}
			opts := skills.InstallOptions{
				ProjectRoot: projectRoot,
				CatalogProvenance: &regtypes.Provenance{
					SignerIdentity: testSignerIdentity,
					CertIssuer:     testCertIssuer,
				},
			}
			checkExpected := func(expected *verifier.ProvenanceExpectation) {
				require.NotNil(t, expected)
				assert.Equal(t, verifier.NewCatalogExpectation(opts.CatalogProvenance), expected)
			}

			var err error
			if backend == "git" {
				mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					Do(func(_ any, _, _ []byte, expected *verifier.ProvenanceExpectation) {
						checkExpected(expected)
					}).Return(signedResult(), nil)
				_, err = svc.verifyGitInstall(t.Context(), opts, "catalog-skill", []byte("payload"), "signature")
			} else {
				mv.EXPECT().VerifyOCI(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					Do(func(_ any, _, _ string, expected *verifier.ProvenanceExpectation) {
						checkExpected(expected)
					}).Return(signedResult(), nil)
				_, err = svc.verifyOCIInstall(
					t.Context(), opts, "catalog-skill", "ghcr.io/test/catalog-skill:v1", "sha256:digest")
			}
			require.NoError(t, err)
		})
	}
}

func TestVerifyInstall_CatalogConstraintCannotAllowUnsigned(t *testing.T) {
	t.Parallel()

	for _, backend := range []string{"git", "oci"} {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			projectRoot := makeProjectRoot(t)
			mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
			svc := &service{sigVerifier: mv}
			opts := skills.InstallOptions{
				ProjectRoot:       projectRoot,
				AllowUnsigned:     true,
				CatalogProvenance: &regtypes.Provenance{SignerIdentity: testSignerIdentity},
			}

			var err error
			if backend == "git" {
				mv.EXPECT().VerifyGit(
					gomock.Any(), gomock.Any(), gomock.Any(), gomock.Eq(verifier.NewCatalogExpectation(opts.CatalogProvenance))).
					Return(nil, verifier.ErrUnsigned)
				_, err = svc.verifyGitInstall(t.Context(), opts, "catalog-skill", []byte("payload"), "")
			} else {
				mv.EXPECT().VerifyOCI(
					gomock.Any(), gomock.Any(), gomock.Any(), gomock.Eq(verifier.NewCatalogExpectation(opts.CatalogProvenance))).
					Return(nil, verifier.ErrUnsigned)
				_, err = svc.verifyOCIInstall(
					t.Context(), opts, "catalog-skill", "ghcr.io/test/catalog-skill:v1", "sha256:digest")
			}
			require.Error(t, err)
			assert.Equal(t, http.StatusForbidden, httperr.Code(err))
		})
	}
}

func TestVerifyInstall_EmptyCatalogProvenanceAllowsUnsigned(t *testing.T) {
	t.Parallel()

	for _, backend := range []string{"git", "oci"} {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			projectRoot := makeProjectRoot(t)
			mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
			svc := &service{sigVerifier: mv}
			opts := skills.InstallOptions{
				ProjectRoot:       projectRoot,
				AllowUnsigned:     true,
				CatalogProvenance: &regtypes.Provenance{},
			}

			var (
				decision *provenanceDecision
				err      error
			)
			if backend == "git" {
				mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Nil()).
					Return(nil, verifier.ErrUnsigned)
				decision, err = svc.verifyGitInstall(t.Context(), opts, "catalog-skill", []byte("payload"), "")
			} else {
				mv.EXPECT().VerifyOCI(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Nil()).
					Return(nil, verifier.ErrUnsigned)
				decision, err = svc.verifyOCIInstall(
					t.Context(), opts, "catalog-skill", "ghcr.io/test/catalog-skill:v1", "sha256:digest")
			}
			require.NoError(t, err)
			assert.True(t, decision.unsigned)
		})
	}
}

func TestVerifyInstall_UnsupportedCatalogConstraintsOnlyFailOnFirstUse(t *testing.T) {
	t.Parallel()

	constraints := []struct {
		name       string
		provenance *regtypes.Provenance
	}{
		{
			name: "attestation",
			provenance: &regtypes.Provenance{
				Attestation: &regtypes.VerifiedAttestation{PredicateType: "https://slsa.dev/provenance/v1"},
			},
		},
		{
			name:       "sigstore URL",
			provenance: &regtypes.Provenance{SigstoreURL: "https://sigstore.example.com/root.json"},
		},
	}
	states := []struct {
		name         string
		entry        *lockfile.Entry
		wantErr      bool
		wantExpected bool
	}{
		{name: "first use", wantErr: true},
		{
			name: "signed lock",
			entry: &lockfile.Entry{
				Name:   "catalog-skill",
				Source: "catalog-skill",
				Digest: testCommitHash,
				Provenance: &lockfile.Provenance{
					SignerIdentity: testSignerIdentity,
					CertIssuer:     testCertIssuer,
				},
			},
			wantExpected: true,
		},
		{
			name: "legacy lock without trust state",
			entry: &lockfile.Entry{
				Name:   "catalog-skill",
				Source: "catalog-skill",
				Digest: testCommitHash,
			},
		},
	}
	for _, backend := range []string{"git", "oci"} {
		for _, constraint := range constraints {
			for _, state := range states {
				t.Run(backend+"/"+constraint.name+"/"+state.name, func(t *testing.T) {
					t.Parallel()
					projectRoot := makeProjectRoot(t)
					if state.entry != nil {
						writeLockEntry(t, projectRoot, *state.entry)
					}
					mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
					svc := &service{sigVerifier: mv}
					opts := skills.InstallOptions{
						ProjectRoot:       projectRoot,
						CatalogProvenance: constraint.provenance,
					}

					if !state.wantErr {
						checkExpected := func(expected *verifier.ProvenanceExpectation) {
							if state.wantExpected {
								require.NotNil(t, expected)
								assert.Equal(t, verifier.NewLockExpectation(state.entry.Provenance), expected)
							} else {
								assert.Nil(t, expected,
									"a legacy lock must suppress catalog policy without inventing lock trust")
							}
						}
						if backend == "git" {
							mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
								Do(func(_ any, _, _ []byte, expected *verifier.ProvenanceExpectation) {
									checkExpected(expected)
								}).Return(signedResult(), nil)
						} else {
							mv.EXPECT().VerifyOCI(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
								Do(func(_ any, _, _ string, expected *verifier.ProvenanceExpectation) {
									checkExpected(expected)
								}).Return(signedResult(), nil)
						}
					}

					var err error
					if backend == "git" {
						_, err = svc.verifyGitInstall(t.Context(), opts, "catalog-skill", []byte("payload"), "signature")
					} else {
						_, err = svc.verifyOCIInstall(
							t.Context(), opts, "catalog-skill", "ghcr.io/test/catalog-skill:v1", "sha256:digest")
					}
					if state.wantErr {
						require.Error(t, err)
						assert.Equal(t, http.StatusUnprocessableEntity, httperr.Code(err))
						return
					}
					require.NoError(t, err)
				})
			}
		}
	}
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestInstallVerification_CatalogRejectionHasNoSideEffects(t *testing.T) {
	tests := []struct {
		name       string
		provenance *regtypes.Provenance
		verifyErr  error
		wantCode   int
	}{
		{
			name:       "provenance mismatch",
			provenance: &regtypes.Provenance{SignerIdentity: "attacker@example.com"},
			verifyErr:  verifier.ErrSignerMismatch,
			wantCode:   http.StatusForbidden,
		},
		{
			name:       "unsupported sigstore URL",
			provenance: &regtypes.Provenance{SigstoreURL: "https://sigstore.example.com/root.json"},
			wantCode:   http.StatusUnprocessableEntity,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gr := gitmocks.NewMockResolver(gomock.NewController(t))
			gr.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(catalogSkillFiles(), nil)

			lookup := regmocks.NewMockProvider(gomock.NewController(t))
			lookup.EXPECT().SearchSkills("catalog-skill").Return([]regtypes.Skill{
				catalogSkill(tc.provenance),
			}, nil)

			mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
			if tc.verifyErr != nil {
				mv.EXPECT().VerifyGit(
					gomock.Any(), gomock.Any(), gomock.Any(), gomock.Eq(verifier.NewCatalogExpectation(tc.provenance))).
					Return(nil, tc.verifyErr)
			}
			svc, projectRoot := newLockTestService(t, gr, WithVerifier(mv), WithSkillLookup(lookup))

			_, err := svc.Install(t.Context(), skills.InstallOptions{
				Name:        "catalog-skill",
				Scope:       skills.ScopeProject,
				ProjectRoot: projectRoot,
				Clients:     []string{"claude-code"},
			})
			require.Error(t, err)
			assert.Equal(t, tc.wantCode, httperr.Code(err))

			_, ok := loadLockEntry(t, projectRoot, "catalog-skill")
			assert.False(t, ok, "a rejected catalog policy must not write a lock entry")
			_, err = svc.Info(t.Context(), skills.InfoOptions{
				Name: "catalog-skill", Scope: skills.ScopeProject, ProjectRoot: projectRoot,
			})
			require.Error(t, err, "a rejected catalog policy must not create a database record")
			assert.NoDirExists(t, projectRoot+"/.claude/skills/catalog-skill",
				"verification must fail before skill files are extracted")
		})
	}
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
	entry, ok := loadLockEntry(t, projectRoot, "ref-pinned-skill")
	require.True(t, ok)
	require.NotNil(t, entry.Provenance)

	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ any, _, _ []byte, expected *verifier.ProvenanceExpectation) (*verifier.Result, error) {
			require.NotNil(t, expected)
			assert.Equal(t, verifier.NewLockExpectation(entry.Provenance), expected,
				"install must enforce the recorded ref, not relax it like upgrade")
			return nil, verifier.ErrSignerMismatch
		})
	installOpts.Force = true
	_, err = svc.Install(t.Context(), installOpts)
	require.Error(t, err)
	assert.Equal(t, http.StatusForbidden, httperr.Code(err))

	entry, ok = loadLockEntry(t, projectRoot, "ref-pinned-skill")
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

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestInstallVerification_UserScopeIgnoresUnsupportedCatalogConstraints(t *testing.T) {
	tests := []struct {
		name       string
		provenance *regtypes.Provenance
	}{
		{
			name: "attestation",
			provenance: &regtypes.Provenance{
				Attestation: &regtypes.VerifiedAttestation{PredicateType: "https://slsa.dev/provenance/v1"},
			},
		},
		{
			name:       "sigstore URL",
			provenance: &regtypes.Provenance{SigstoreURL: "https://sigstore.example.com/root.json"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gr := gitmocks.NewMockResolver(gomock.NewController(t))
			gr.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(catalogSkillFiles(), nil)

			lookup := regmocks.NewMockProvider(gomock.NewController(t))
			lookup.EXPECT().SearchSkills("catalog-skill").Return([]regtypes.Skill{
				catalogSkill(tc.provenance),
			}, nil)

			// Unsupported project verification policy is irrelevant at user
			// scope, where no verifier or lock trust decision is used.
			mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
			svc, _ := newLockTestService(t, gr, WithVerifier(mv), WithSkillLookup(lookup))
			_, err := svc.Install(t.Context(), skills.InstallOptions{
				Name:    "catalog-skill",
				Scope:   skills.ScopeUser,
				Clients: []string{"claude-code"},
			})
			require.NoError(t, err)
		})
	}
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

	// Two fixtures rather than one, because the anchors are mutually
	// exclusive (see validateProvenanceAnchor): a single struct with every
	// field populated would encode a lock state that validation rejects, and
	// make it the canonical example. Coverage is asserted across the pair, so
	// a newly added field still has to appear in one of them.
	keyless := &lockfile.Provenance{
		SignerIdentity:    testSignerIdentity,
		CertIssuer:        testCertIssuer,
		RepositoryURI:     "https://github.com/org/repo",
		RepositoryRef:     "refs/heads/main",
		RunnerEnvironment: "github-hosted",
		SigstoreURL:       "https://rekor.sigstore.dev",
		Provisional:       true,
	}
	keyed := &lockfile.Provenance{PublicKey: testPublicKeyB64}
	requireEveryFieldCovered(t, keyless, keyed)

	keylessInfo := provenanceInfoFromLock(keyless)
	keyedInfo := provenanceInfoFromLock(keyed)
	requireEveryFieldCovered(t, keylessInfo, keyedInfo)
	assert.Equal(t, keyless, provenanceInfoToLock(keylessInfo))
	assert.Equal(t, keyed, provenanceInfoToLock(keyedInfo),
		"a key-pinned entry must survive the round trip as the only anchor it has")

	assert.Nil(t, provenanceInfoFromLock(nil))
	assert.Nil(t, provenanceInfoToLock(nil))
}

func TestNormalizeCatalogProvenance(t *testing.T) {
	t.Parallel()

	assert.Nil(t, normalizeCatalogProvenance(nil))
	assert.Nil(t, normalizeCatalogProvenance(&regtypes.Provenance{}),
		"an empty catalog block must preserve unconstrained TOFU behavior")

	partial := &regtypes.Provenance{RepositoryRef: "refs/tags/v1.0.0"}
	assert.Same(t, partial, normalizeCatalogProvenance(partial),
		"a single supported constraint must not be discarded")
}

// requireEveryFieldCovered fails when a field of the struct type is zero in
// every one of the given values, so a field added to one provenance shape
// without a matching line in the conversions is caught here rather than in
// production. Values are checked as a set because mutually exclusive anchors
// cannot be represented in a single legal fixture.
func requireEveryFieldCovered(t *testing.T, values ...any) {
	t.Helper()
	require.NotEmpty(t, values)
	first := reflect.ValueOf(values[0]).Elem()
	for i := range first.NumField() {
		covered := false
		for _, v := range values {
			if !reflect.ValueOf(v).Elem().Field(i).IsZero() {
				covered = true
				break
			}
		}
		assert.True(t, covered,
			"%s.%s is zero in every fixture: wire it through the provenance conversions and a fixture",
			first.Type().Name(), first.Type().Field(i).Name)
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

// TestClassifyInstallVerifyErrorNamesKeySigned pins the user-facing half of
// #6442: a key-signed artifact must be diagnosed as key-signed, must not be
// reported as a verification failure, and must say plainly that allow_unsigned
// is no remedy — the artifact is signed, so recording an unsigned exception
// would file a false trust decision in the lock.
func TestClassifyInstallVerifyErrorNamesKeySigned(t *testing.T) {
	t.Parallel()

	err := classifyInstallVerifyError(verifier.ErrKeySigned, "some-skill", nil)
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

	assert.Equal(t, skills.FailureReasonKeySigned, classifySignatureError(verifier.ErrKeySigned))
	assert.Equal(t, skills.FailureReasonSignatureInvalid,
		classifySignatureError(verifier.ErrSignatureInvalid),
		"the pre-existing mapping must be unaffected")
}

// TestIsAllowedUnsignedRejectsKeySigned is the guard that closes #6442's
// actual escape-hatch gap: --allow-unsigned must not rescue a key-signed
// artifact even on true first use with the flag explicitly set.
func TestIsAllowedUnsignedRejectsKeySigned(t *testing.T) {
	t.Parallel()

	assert.False(t, isAllowedUnsigned(verifier.ErrKeySigned,
		skills.InstallOptions{AllowUnsigned: true}, nil),
		"a signed artifact must never be recordable as an unsigned exception")
	assert.True(t, isAllowedUnsigned(verifier.ErrUnsigned,
		skills.InstallOptions{AllowUnsigned: true}, nil),
		"the genuine unsigned case must still be allowed through")
}

// TestCatalogInstallNamesKeySignedArtifact covers the second route to
// classification. A first install resolved from a catalog entry that declares
// provenance is classified by classifyCatalogVerifyError, not
// classifyInstallVerifyError, so a key-signed artifact arriving that way would
// otherwise be reported as failing to match its catalog-declared provenance —
// which is doubly wrong: nothing was compared, because the keyless policy
// cannot check a key-pair signature at all, and the report would carry neither
// the re-publish remedy nor the note that allow_unsigned cannot help.
func TestCatalogInstallNamesKeySignedArtifact(t *testing.T) {
	t.Parallel()

	projectRoot := makeProjectRoot(t)
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	svc := &service{sigVerifier: mv}
	opts := skills.InstallOptions{
		ProjectRoot:       projectRoot,
		CatalogProvenance: &regtypes.Provenance{SignerIdentity: testSignerIdentity},
	}
	mv.EXPECT().VerifyOCI(
		gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Eq(verifier.NewCatalogExpectation(opts.CatalogProvenance))).
		Return(nil, verifier.ErrKeySigned)

	_, err := svc.verifyOCIInstall(
		t.Context(), opts, "catalog-skill", "ghcr.io/test/catalog-skill:v1", "sha256:digest")

	require.Error(t, err)
	assert.Equal(t, http.StatusForbidden, httperr.Code(err))
	assert.Contains(t, err.Error(), "re-publish it with keyless signing")
	assert.Contains(t, err.Error(), "allow_unsigned does not apply")
	assert.NotContains(t, err.Error(), "does not match its catalog-declared provenance",
		"a key-signed artifact was never compared against the catalog constraint")
}

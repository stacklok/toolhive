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

	godigest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
	ociskills "github.com/stacklok/toolhive-core/oci/skills"
	ocimocks "github.com/stacklok/toolhive-core/oci/skills/mocks"
	regtypes "github.com/stacklok/toolhive-core/registry/types"
	regmocks "github.com/stacklok/toolhive/pkg/registry/mocks"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
	gitmocks "github.com/stacklok/toolhive/pkg/skills/gitresolver/mocks"
	skillsmocks "github.com/stacklok/toolhive/pkg/skills/mocks"
	"github.com/stacklok/toolhive/pkg/storage"
	storemocks "github.com/stacklok/toolhive/pkg/storage/mocks"
)

func TestInstallFromOCI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		opts         skills.InstallOptions
		setup        func(t *testing.T, ctrl *gomock.Controller) (ociskills.RegistryClient, *ociskills.Store, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver)
		wantCode     int
		wantErr      string
		wantName     string
		wantVersion  string
		wantDigest   bool
		wantRefSaved string
	}{
		{
			name: "registry not configured",
			opts: skills.InstallOptions{Name: "ghcr.io/org/my-skill:v1"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (ociskills.RegistryClient, *ociskills.Store, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				return nil, nil, storemocks.NewMockSkillStore(ctrl), skillsmocks.NewMockPathResolver(ctrl)
			},
			wantCode: http.StatusInternalServerError,
		},
		{
			name: "ociStore not configured",
			opts: skills.InstallOptions{Name: "ghcr.io/org/my-skill:v1"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (ociskills.RegistryClient, *ociskills.Store, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				return ocimocks.NewMockRegistryClient(ctrl), nil, storemocks.NewMockSkillStore(ctrl), skillsmocks.NewMockPathResolver(ctrl)
			},
			wantCode: http.StatusInternalServerError,
		},
		{
			name: "pathResolver not configured",
			opts: skills.InstallOptions{Name: "ghcr.io/org/my-skill:v1"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (ociskills.RegistryClient, *ociskills.Store, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				ociStore, err := ociskills.NewStore(tempDir(t))
				require.NoError(t, err)
				return ocimocks.NewMockRegistryClient(ctrl), ociStore, storemocks.NewMockSkillStore(ctrl), nil
			},
			wantCode: http.StatusInternalServerError,
		},
		{
			name: "pull error propagates",
			opts: skills.InstallOptions{Name: "ghcr.io/org/my-skill:v1"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (ociskills.RegistryClient, *ociskills.Store, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				ociStore, err := ociskills.NewStore(tempDir(t))
				require.NoError(t, err)
				reg := ocimocks.NewMockRegistryClient(ctrl)
				reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/org/my-skill:v1").
					Return(godigest.Digest(""), fmt.Errorf("auth required"))
				pr := skillsmocks.NewMockPathResolver(ctrl)
				return reg, ociStore, storemocks.NewMockSkillStore(ctrl), pr
			},
			wantErr: "auth required",
		},
		{
			name: "invalid skill name in artifact",
			opts: skills.InstallOptions{Name: "ghcr.io/org/bad-artifact:v1"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (ociskills.RegistryClient, *ociskills.Store, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				ociStore, err := ociskills.NewStore(tempDir(t))
				require.NoError(t, err)

				// Build an artifact with an invalid skill name (uppercase).
				skillDir := filepath.Join(tempDir(t), "INVALID")
				require.NoError(t, os.MkdirAll(skillDir, 0o750))
				require.NoError(t, os.WriteFile(
					filepath.Join(skillDir, "SKILL.md"),
					[]byte("---\nname: INVALID\ndescription: test\n---\n# Bad"),
					0o600,
				))
				packager := ociskills.NewPackager(ociStore)
				result, pkgErr := packager.Package(t.Context(), skillDir, ociskills.DefaultPackageOptions())
				require.NoError(t, pkgErr)

				reg := ocimocks.NewMockRegistryClient(ctrl)
				reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/org/bad-artifact:v1").
					Return(result.IndexDigest, nil)
				pr := skillsmocks.NewMockPathResolver(ctrl)
				return reg, ociStore, storemocks.NewMockSkillStore(ctrl), pr
			},
			wantCode: http.StatusUnprocessableEntity,
		},
		{
			name: "oversized layer returns 422",
			opts: skills.InstallOptions{Name: "ghcr.io/org/oversize-skill:v1"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (ociskills.RegistryClient, *ociskills.Store, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				ociStore, err := ociskills.NewStore(tempDir(t))
				require.NoError(t, err)
				manifestDigest := buildManifestWithLayerSize(t, ociStore, "oversize-skill", maxCompressedLayerSize+1)

				reg := ocimocks.NewMockRegistryClient(ctrl)
				reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/org/oversize-skill:v1").
					Return(manifestDigest, nil)
				pr := skillsmocks.NewMockPathResolver(ctrl)
				return reg, ociStore, storemocks.NewMockSkillStore(ctrl), pr
			},
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "compressed layer size",
		},
		{
			name: "successful pull and install",
			opts: skills.InstallOptions{Name: "ghcr.io/org/my-skill:v1", Clients: []string{"claude-code"}},
			setup: func(t *testing.T, ctrl *gomock.Controller) (ociskills.RegistryClient, *ociskills.Store, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				ociStore, err := ociskills.NewStore(tempDir(t))
				require.NoError(t, err)
				indexDigest := buildTestArtifact(t, ociStore, "my-skill", "1.0.0")

				reg := ocimocks.NewMockRegistryClient(ctrl)
				reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/org/my-skill:v1").
					Return(indexDigest, nil)

				store := storemocks.NewMockSkillStore(ctrl)
				pr := skillsmocks.NewMockPathResolver(ctrl)
				targetDir := filepath.Join(tempDir(t), "installed", "my-skill")
				pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(targetDir, nil)
				store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(skills.InstalledSkill{}, storage.ErrNotFound)
				store.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ context.Context, sk skills.InstalledSkill) error {
						assert.Equal(t, "my-skill", sk.Metadata.Name)
						assert.Equal(t, "1.0.0", sk.Metadata.Version)
						assert.Equal(t, "ghcr.io/org/my-skill:v1", sk.Reference)
						assert.Contains(t, sk.Digest, "sha256:")
						assert.Equal(t, skills.InstallStatusInstalled, sk.Status)
						return nil
					})
				return reg, ociStore, store, pr
			},
			wantName:     "my-skill",
			wantVersion:  "1.0.0",
			wantDigest:   true,
			wantRefSaved: "ghcr.io/org/my-skill:v1",
		},
		{
			name: "name mismatch between artifact and reference is rejected",
			opts: skills.InstallOptions{Name: "ghcr.io/org/some-repo:v1", Clients: []string{"claude-code"}},
			setup: func(t *testing.T, ctrl *gomock.Controller) (ociskills.RegistryClient, *ociskills.Store, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				ociStore, err := ociskills.NewStore(tempDir(t))
				require.NoError(t, err)
				// The artifact declares itself as "actual-skill", not "some-repo".
				indexDigest := buildTestArtifact(t, ociStore, "actual-skill", "2.0.0")

				reg := ocimocks.NewMockRegistryClient(ctrl)
				reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/org/some-repo:v1").
					Return(indexDigest, nil)

				pr := skillsmocks.NewMockPathResolver(ctrl)
				return reg, ociStore, storemocks.NewMockSkillStore(ctrl), pr
			},
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "does not match OCI reference repository",
		},
		{
			name: "preserves caller version over config version",
			opts: skills.InstallOptions{Name: "ghcr.io/org/my-skill:v1", Version: "override-version", Clients: []string{"claude-code"}},
			setup: func(t *testing.T, ctrl *gomock.Controller) (ociskills.RegistryClient, *ociskills.Store, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				ociStore, err := ociskills.NewStore(tempDir(t))
				require.NoError(t, err)
				indexDigest := buildTestArtifact(t, ociStore, "my-skill", "1.0.0")

				reg := ocimocks.NewMockRegistryClient(ctrl)
				reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/org/my-skill:v1").
					Return(indexDigest, nil)

				store := storemocks.NewMockSkillStore(ctrl)
				pr := skillsmocks.NewMockPathResolver(ctrl)
				targetDir := filepath.Join(tempDir(t), "installed", "my-skill")
				pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(targetDir, nil)
				store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(skills.InstalledSkill{}, storage.ErrNotFound)
				store.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ context.Context, sk skills.InstalledSkill) error {
						assert.Equal(t, "override-version", sk.Metadata.Version)
						return nil
					})
				return reg, ociStore, store, pr
			},
			wantName:    "my-skill",
			wantVersion: "override-version",
		},
		{
			name: "hydrates version from config when caller omits it",
			opts: skills.InstallOptions{Name: "ghcr.io/org/my-skill:v1", Clients: []string{"claude-code"}},
			setup: func(t *testing.T, ctrl *gomock.Controller) (ociskills.RegistryClient, *ociskills.Store, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				ociStore, err := ociskills.NewStore(tempDir(t))
				require.NoError(t, err)
				indexDigest := buildTestArtifact(t, ociStore, "my-skill", "3.0.0")

				reg := ocimocks.NewMockRegistryClient(ctrl)
				reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/org/my-skill:v1").
					Return(indexDigest, nil)

				store := storemocks.NewMockSkillStore(ctrl)
				pr := skillsmocks.NewMockPathResolver(ctrl)
				targetDir := filepath.Join(tempDir(t), "installed", "my-skill")
				pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(targetDir, nil)
				store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(skills.InstalledSkill{}, storage.ErrNotFound)
				store.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ context.Context, sk skills.InstalledSkill) error {
						assert.Equal(t, "3.0.0", sk.Metadata.Version)
						return nil
					})
				return reg, ociStore, store, pr
			},
			wantName:    "my-skill",
			wantVersion: "3.0.0",
		},
		{
			name: "invalid OCI reference returns 400",
			opts: skills.InstallOptions{Name: "not://valid:ref:extra"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (ociskills.RegistryClient, *ociskills.Store, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				return nil, nil, storemocks.NewMockSkillStore(ctrl), nil
			},
			wantCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			registry, ociStore, store, pr := tt.setup(t, ctrl)

			var opts []Option
			if registry != nil {
				opts = append(opts, WithRegistryClient(registry))
			}
			if ociStore != nil {
				opts = append(opts, WithOCIStore(ociStore))
			}
			if pr != nil {
				opts = append(opts, WithPathResolver(pr))
			}

			svc := New(store, opts...)
			result, err := svc.Install(t.Context(), tt.opts)

			if tt.wantCode != 0 {
				require.Error(t, err)
				assert.Equal(t, tt.wantCode, httperr.Code(err))
				return
			}
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			if tt.wantName != "" {
				assert.Equal(t, tt.wantName, result.Skill.Metadata.Name)
			}
			if tt.wantVersion != "" {
				assert.Equal(t, tt.wantVersion, result.Skill.Metadata.Version)
			}
			if tt.wantDigest {
				assert.Contains(t, result.Skill.Digest, "sha256:")
			}
			if tt.wantRefSaved != "" {
				assert.Equal(t, tt.wantRefSaved, result.Skill.Reference)
			}
		})
	}
}

func TestInstallFromLocalStore(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		opts        skills.InstallOptions
		setup       func(t *testing.T, ctrl *gomock.Controller) (*ociskills.Store, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver)
		wantCode    int
		wantErr     string
		wantStatus  string
		wantVersion string
		wantDigest  bool
	}{
		{
			name: "happy path: build then install",
			opts: skills.InstallOptions{Name: "my-skill", Clients: []string{"claude-code"}},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*ociskills.Store, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				ociStore, err := ociskills.NewStore(tempDir(t))
				require.NoError(t, err)

				// Build an artifact and tag it with the skill name.
				indexDigest := buildTestArtifact(t, ociStore, "my-skill", "1.0.0")
				require.NoError(t, ociStore.Tag(t.Context(), indexDigest, "my-skill"))

				store := storemocks.NewMockSkillStore(ctrl)
				pr := skillsmocks.NewMockPathResolver(ctrl)
				targetDir := filepath.Join(tempDir(t), "installed", "my-skill")
				pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(targetDir, nil)
				store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(skills.InstalledSkill{}, storage.ErrNotFound)
				store.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ context.Context, sk skills.InstalledSkill) error {
						assert.Equal(t, "my-skill", sk.Metadata.Name)
						assert.Equal(t, "1.0.0", sk.Metadata.Version)
						assert.Contains(t, sk.Digest, "sha256:")
						assert.Equal(t, skills.InstallStatusInstalled, sk.Status)
						return nil
					})
				return ociStore, store, pr
			},
			wantStatus:  string(skills.InstallStatusInstalled),
			wantVersion: "1.0.0",
			wantDigest:  true,
		},
		{
			name: "name mismatch in local artifact",
			opts: skills.InstallOptions{Name: "evil-skill"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*ociskills.Store, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				ociStore, err := ociskills.NewStore(tempDir(t))
				require.NoError(t, err)

				// Build "real-skill" but tag it as "evil-skill".
				indexDigest := buildTestArtifact(t, ociStore, "real-skill", "1.0.0")
				require.NoError(t, ociStore.Tag(t.Context(), indexDigest, "evil-skill"))

				store := storemocks.NewMockSkillStore(ctrl)
				pr := skillsmocks.NewMockPathResolver(ctrl)
				return ociStore, store, pr
			},
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "does not match install name",
		},
		{
			name: "tag not found returns not found error",
			opts: skills.InstallOptions{Name: "no-such-skill"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*ociskills.Store, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				// Empty store — no tags.
				ociStore, err := ociskills.NewStore(tempDir(t))
				require.NoError(t, err)

				store := storemocks.NewMockSkillStore(ctrl)
				pr := skillsmocks.NewMockPathResolver(ctrl)
				return ociStore, store, pr
			},
			wantCode: http.StatusNotFound,
			wantErr:  "not found in local store or registry",
		},
		{
			name: "nil ociStore returns not found error",
			opts: skills.InstallOptions{Name: "some-skill"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*ociskills.Store, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				store := storemocks.NewMockSkillStore(ctrl)
				return nil, store, nil
			},
			wantCode: http.StatusNotFound,
			wantErr:  "not found in local store or registry",
		},
		{
			name: "corrupt manifest propagates error",
			opts: skills.InstallOptions{Name: "corrupt-skill"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*ociskills.Store, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				ociStore, err := ociskills.NewStore(tempDir(t))
				require.NoError(t, err)

				// Store raw bytes as a "manifest" and tag it — this will
				// fail during extractOCIContent because it's not valid JSON.
				badManifest := []byte(`not valid json`)
				d, putErr := ociStore.PutManifest(t.Context(), badManifest)
				require.NoError(t, putErr)
				require.NoError(t, ociStore.Tag(t.Context(), d, "corrupt-skill"))

				store := storemocks.NewMockSkillStore(ctrl)
				pr := skillsmocks.NewMockPathResolver(ctrl)
				return ociStore, store, pr
			},
			wantErr: "checking OCI content type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			ociStore, store, pr := tt.setup(t, ctrl)

			var opts []Option
			if ociStore != nil {
				opts = append(opts, WithOCIStore(ociStore))
			}
			if pr != nil {
				opts = append(opts, WithPathResolver(pr))
			}

			svc := New(store, opts...)
			result, err := svc.Install(t.Context(), tt.opts)

			if tt.wantCode != 0 {
				require.Error(t, err)
				assert.Equal(t, tt.wantCode, httperr.Code(err))
				return
			}
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			if tt.wantStatus != "" {
				assert.Equal(t, tt.wantStatus, string(result.Skill.Status))
			}
			if tt.wantVersion != "" {
				assert.Equal(t, tt.wantVersion, result.Skill.Metadata.Version)
			}
			if tt.wantDigest {
				assert.Contains(t, result.Skill.Digest, "sha256:")
			}
		})
	}
}

func TestInstallQualifiedNameOCIFallback(t *testing.T) {
	t.Parallel()

	commitHash := testCommitHash

	tests := []struct {
		name     string
		opts     skills.InstallOptions
		setup    func(t *testing.T, ctrl *gomock.Controller) (*regmocks.MockProvider, *ocimocks.MockRegistryClient, *ociskills.Store, *gitmocks.MockResolver, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver)
		wantCode int
		wantErr  string
		wantName string
	}{
		{
			name: "qualified namespace/name falls back to registry OCI package",
			opts: skills.InstallOptions{Name: "io.github.stacklok/my-skill"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*regmocks.MockProvider, *ocimocks.MockRegistryClient, *ociskills.Store, *gitmocks.MockResolver, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				ociStore, err := ociskills.NewStore(tempDir(t))
				require.NoError(t, err)

				indexDigest := buildTestArtifact(t, ociStore, "my-skill", "1.0.0")

				reg := ocimocks.NewMockRegistryClient(ctrl)
				// First Pull is for the raw "io.github.stacklok/my-skill:latest" — fails.
				reg.EXPECT().Pull(gomock.Any(), gomock.Any(), "io.github.stacklok/my-skill:latest").
					Return(godigest.Digest(""), fmt.Errorf("no such host")).
					Times(1)
				// Second Pull is after registry lookup resolves the real OCI ref.
				reg.EXPECT().Pull(gomock.Any(), gomock.Any(), "ghcr.io/stacklok/my-skill:v1.0.0").
					Return(indexDigest, nil)

				lookup := regmocks.NewMockProvider(ctrl)
				lookup.EXPECT().SearchSkills("my-skill").Return([]regtypes.Skill{
					{
						Namespace: "io.github.stacklok",
						Name:      "my-skill",
						Packages: []regtypes.SkillPackage{
							{RegistryType: "oci", Identifier: "ghcr.io/stacklok/my-skill:v1.0.0"},
						},
					},
				}, nil)

				installBase := filepath.Join(tempDir(t), "installed")
				require.NoError(t, os.MkdirAll(installBase, 0o755))

				store := storemocks.NewMockSkillStore(ctrl)
				store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(skills.InstalledSkill{}, storage.ErrNotFound)
				store.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)

				pr := skillsmocks.NewMockPathResolver(ctrl)
				pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(filepath.Join(installBase, "my-skill"), nil)
				pr.EXPECT().ListSkillSupportingClients().Return([]string{"claude-code"})

				return lookup, reg, ociStore, nil, store, pr
			},
			wantName: "my-skill",
		},
		{
			name: "qualified namespace/name falls back to registry git package",
			opts: skills.InstallOptions{Name: "io.github.stacklok/my-skill"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*regmocks.MockProvider, *ocimocks.MockRegistryClient, *ociskills.Store, *gitmocks.MockResolver, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				ociStore, err := ociskills.NewStore(tempDir(t))
				require.NoError(t, err)

				reg := ocimocks.NewMockRegistryClient(ctrl)
				reg.EXPECT().Pull(gomock.Any(), gomock.Any(), "io.github.stacklok/my-skill:latest").
					Return(godigest.Digest(""), fmt.Errorf("no such host"))

				lookup := regmocks.NewMockProvider(ctrl)
				lookup.EXPECT().SearchSkills("my-skill").Return([]regtypes.Skill{
					{
						Namespace: "io.github.stacklok",
						Name:      "my-skill",
						Packages: []regtypes.SkillPackage{
							{RegistryType: "git", URL: "https://github.com/stacklok/my-skill"},
						},
					},
				}, nil)

				gr := gitmocks.NewMockResolver(ctrl)
				gr.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(&gitresolver.ResolveResult{
					SkillConfig: &skills.ParseResult{Name: "my-skill", Version: "1.0.0"},
					Files:       []gitresolver.FileEntry{{Path: "SKILL.md", Content: []byte("---\nname: my-skill\n---\n"), Mode: 0644}},
					CommitHash:  commitHash,
				}, nil)

				installBase := filepath.Join(tempDir(t), "installed")
				require.NoError(t, os.MkdirAll(installBase, 0o755))

				store := storemocks.NewMockSkillStore(ctrl)
				store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(skills.InstalledSkill{}, storage.ErrNotFound)
				store.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)

				pr := skillsmocks.NewMockPathResolver(ctrl)
				pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(filepath.Join(installBase, "my-skill"), nil)
				pr.EXPECT().ListSkillSupportingClients().Return([]string{"claude-code"})

				return lookup, reg, ociStore, gr, store, pr
			},
			wantName: "my-skill",
		},
		{
			name: "explicit OCI tag does not fall back to registry on pull failure",
			opts: skills.InstallOptions{Name: "ghcr.io/org/my-skill:v1"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*regmocks.MockProvider, *ocimocks.MockRegistryClient, *ociskills.Store, *gitmocks.MockResolver, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				ociStore, err := ociskills.NewStore(tempDir(t))
				require.NoError(t, err)

				reg := ocimocks.NewMockRegistryClient(ctrl)
				reg.EXPECT().Pull(gomock.Any(), gomock.Any(), "ghcr.io/org/my-skill:v1").
					Return(godigest.Digest(""), fmt.Errorf("auth required"))

				// pathResolver must be non-nil so installFromOCI proceeds past its
				// nil guard and reaches the Pull call.
				pr := skillsmocks.NewMockPathResolver(ctrl)

				store := storemocks.NewMockSkillStore(ctrl)

				// No lookup mock — gomock will fail the test if SearchSkills is called.
				return nil, reg, ociStore, nil, store, pr
			},
			wantCode: http.StatusBadGateway,
			wantErr:  "auth required",
		},
		{
			name: "qualified name with no registry match returns original OCI error",
			opts: skills.InstallOptions{Name: "io.github.stacklok/my-skill"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*regmocks.MockProvider, *ocimocks.MockRegistryClient, *ociskills.Store, *gitmocks.MockResolver, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				ociStore, err := ociskills.NewStore(tempDir(t))
				require.NoError(t, err)

				reg := ocimocks.NewMockRegistryClient(ctrl)
				reg.EXPECT().Pull(gomock.Any(), gomock.Any(), "io.github.stacklok/my-skill:latest").
					Return(godigest.Digest(""), fmt.Errorf("no such host"))

				// pathResolver must be non-nil so installFromOCI proceeds past its
				// nil guard and reaches the Pull call.
				pr := skillsmocks.NewMockPathResolver(ctrl)

				lookup := regmocks.NewMockProvider(ctrl)
				lookup.EXPECT().SearchSkills("my-skill").Return(nil, nil)

				store := storemocks.NewMockSkillStore(ctrl)
				return lookup, reg, ociStore, nil, store, pr
			},
			wantCode: http.StatusBadGateway,
			wantErr:  "no such host",
		},
		{
			name: "digest ref does not fall back to registry on pull failure",
			// A full 64-char SHA256 hex digest — required for nameref.ParseReference to accept it.
			opts: skills.InstallOptions{Name: "ghcr.io/org/my-skill@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*regmocks.MockProvider, *ocimocks.MockRegistryClient, *ociskills.Store, *gitmocks.MockResolver, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				ociStore, err := ociskills.NewStore(tempDir(t))
				require.NoError(t, err)

				reg := ocimocks.NewMockRegistryClient(ctrl)
				reg.EXPECT().Pull(gomock.Any(), gomock.Any(), "ghcr.io/org/my-skill@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa").
					Return(godigest.Digest(""), fmt.Errorf("manifest unknown"))

				pr := skillsmocks.NewMockPathResolver(ctrl)
				store := storemocks.NewMockSkillStore(ctrl)
				// No lookup mock — gomock will fail the test if SearchSkills is called.
				return nil, reg, ociStore, nil, store, pr
			},
			wantCode: http.StatusBadGateway,
			wantErr:  "manifest unknown",
		},
		{
			name: "multi-segment OCI ref does not fall back to registry on pull failure",
			opts: skills.InstallOptions{Name: "ghcr.io/org/my-skill"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*regmocks.MockProvider, *ocimocks.MockRegistryClient, *ociskills.Store, *gitmocks.MockResolver, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				ociStore, err := ociskills.NewStore(tempDir(t))
				require.NoError(t, err)

				reg := ocimocks.NewMockRegistryClient(ctrl)
				reg.EXPECT().Pull(gomock.Any(), gomock.Any(), "ghcr.io/org/my-skill:latest").
					Return(godigest.Digest(""), fmt.Errorf("auth required"))

				pr := skillsmocks.NewMockPathResolver(ctrl)
				store := storemocks.NewMockSkillStore(ctrl)
				// No lookup mock — gomock will fail if SearchSkills is called.
				return nil, reg, ociStore, nil, store, pr
			},
			wantCode: http.StatusBadGateway,
			wantErr:  "auth required",
		},
		{
			name: "registry ambiguity error surfaced to caller",
			// resolveFromRegistry returns a conflict error when multiple registry
			// entries match the same name — the Install method must propagate it.
			opts: skills.InstallOptions{Name: "io.github.stacklok/my-skill"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*regmocks.MockProvider, *ocimocks.MockRegistryClient, *ociskills.Store, *gitmocks.MockResolver, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				ociStore, err := ociskills.NewStore(tempDir(t))
				require.NoError(t, err)

				reg := ocimocks.NewMockRegistryClient(ctrl)
				reg.EXPECT().Pull(gomock.Any(), gomock.Any(), "io.github.stacklok/my-skill:latest").
					Return(godigest.Digest(""), fmt.Errorf("no such host"))

				pr := skillsmocks.NewMockPathResolver(ctrl)

				// Return two results with the same namespace/name so that
				// resolveFromRegistry treats this as an ambiguous match and
				// returns a conflict error rather than nil.
				lookup := regmocks.NewMockProvider(ctrl)
				lookup.EXPECT().SearchSkills("my-skill").Return([]regtypes.Skill{
					{Namespace: "io.github.stacklok", Name: "my-skill", Packages: []regtypes.SkillPackage{{RegistryType: "git", URL: "https://github.com/a/my-skill"}}},
					{Namespace: "io.github.stacklok", Name: "my-skill", Packages: []regtypes.SkillPackage{{RegistryType: "git", URL: "https://github.com/b/my-skill"}}},
				}, nil)

				store := storemocks.NewMockSkillStore(ctrl)
				return lookup, reg, ociStore, nil, store, pr
			},
			wantCode: http.StatusConflict,
			wantErr:  "ambiguous",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			lookup, reg, ociStore, gr, store, pr := tt.setup(t, ctrl)

			var opts []Option
			if lookup != nil {
				opts = append(opts, WithSkillLookup(lookup))
			}
			if reg != nil {
				opts = append(opts, WithRegistryClient(reg))
			}
			if ociStore != nil {
				opts = append(opts, WithOCIStore(ociStore))
			}
			if gr != nil {
				opts = append(opts, WithGitResolver(gr))
			}
			if pr != nil {
				opts = append(opts, WithPathResolver(pr))
			}

			svc := New(store, opts...)
			result, err := svc.Install(t.Context(), tt.opts)

			if tt.wantCode != 0 {
				require.Error(t, err)
				assert.Equal(t, tt.wantCode, httperr.Code(err))
				if tt.wantErr != "" {
					assert.Contains(t, err.Error(), tt.wantErr)
				}
				return
			}
			require.NoError(t, err)
			if tt.wantName != "" {
				assert.Equal(t, tt.wantName, result.Skill.Metadata.Name)
			}
		})
	}
}

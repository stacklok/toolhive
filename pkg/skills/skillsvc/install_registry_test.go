// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"fmt"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
	ociskills "github.com/stacklok/toolhive-core/oci/skills"
	ocimocks "github.com/stacklok/toolhive-core/oci/skills/mocks"
	regtypes "github.com/stacklok/toolhive-core/registry/types"
	regmocks "github.com/stacklok/toolhive/pkg/registry/mocks"
	"github.com/stacklok/toolhive/pkg/skills"
	skillsmocks "github.com/stacklok/toolhive/pkg/skills/mocks"
	"github.com/stacklok/toolhive/pkg/storage"
	storemocks "github.com/stacklok/toolhive/pkg/storage/mocks"
)

func TestInstallFromRegistry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		opts        skills.InstallOptions
		setup       func(t *testing.T, ctrl *gomock.Controller) (*regmocks.MockProvider, *ocimocks.MockRegistryClient, *ociskills.Store, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver)
		wantCode    int
		wantErr     string
		wantName    string
		wantDigest  bool
		wantVersion string
	}{
		{
			name: "registry resolves skill with OCI package",
			opts: skills.InstallOptions{Name: "my-skill"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*regmocks.MockProvider, *ocimocks.MockRegistryClient, *ociskills.Store, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				lookup := regmocks.NewMockProvider(ctrl)
				lookup.EXPECT().SearchSkills("my-skill").Return([]regtypes.Skill{
					{
						Namespace: "io.github.test",
						Name:      "my-skill",
						Packages: []regtypes.SkillPackage{
							{RegistryType: "oci", Identifier: "ghcr.io/test/my-skill:v1.0.0"},
						},
					},
				}, nil)

				ociStore, err := ociskills.NewStore(tempDir(t))
				require.NoError(t, err)

				// Build and tag an artifact with the skill name so the
				// registry client can return it when Pull is called.
				indexDigest := buildTestArtifact(t, ociStore, "my-skill", "1.0.0")

				reg := ocimocks.NewMockRegistryClient(ctrl)
				reg.EXPECT().Pull(gomock.Any(), gomock.Any(), "ghcr.io/test/my-skill:v1.0.0").Return(indexDigest, nil)

				store := storemocks.NewMockSkillStore(ctrl)
				store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(skills.InstalledSkill{}, storage.ErrNotFound)
				store.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)

				pr := skillsmocks.NewMockPathResolver(ctrl)
				pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(filepath.Join(tempDir(t), "installed", "my-skill"), nil)
				pr.EXPECT().ListSkillSupportingClients().Return([]string{"claude-code"})

				return lookup, reg, ociStore, store, pr
			},
			wantName:    "my-skill",
			wantDigest:  true,
			wantVersion: "1.0.0",
		},
		{
			name: "multiple exact name matches returns conflict",
			opts: skills.InstallOptions{Name: "my-skill"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*regmocks.MockProvider, *ocimocks.MockRegistryClient, *ociskills.Store, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				lookup := regmocks.NewMockProvider(ctrl)
				lookup.EXPECT().SearchSkills("my-skill").Return([]regtypes.Skill{
					{Namespace: "io.github.alice", Name: "my-skill"},
					{Namespace: "io.github.bob", Name: "my-skill"},
				}, nil)
				store := storemocks.NewMockSkillStore(ctrl)
				return lookup, nil, nil, store, nil
			},
			wantCode: http.StatusConflict,
			wantErr:  "ambiguous skill name",
		},
		{
			name: "skill with no installable packages returns unprocessable",
			opts: skills.InstallOptions{Name: "my-skill"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*regmocks.MockProvider, *ocimocks.MockRegistryClient, *ociskills.Store, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				lookup := regmocks.NewMockProvider(ctrl)
				lookup.EXPECT().SearchSkills("my-skill").Return([]regtypes.Skill{
					{
						Namespace: "io.github.test",
						Name:      "my-skill",
						Packages: []regtypes.SkillPackage{
							{RegistryType: "npm", URL: "https://npmjs.com/test/my-skill"},
						},
					},
				}, nil)
				store := storemocks.NewMockSkillStore(ctrl)
				return lookup, nil, nil, store, nil
			},
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "no installable package",
		},
		{
			name: "registry lookup error degrades to not found",
			opts: skills.InstallOptions{Name: "my-skill"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*regmocks.MockProvider, *ocimocks.MockRegistryClient, *ociskills.Store, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				lookup := regmocks.NewMockProvider(ctrl)
				lookup.EXPECT().SearchSkills("my-skill").Return(nil, fmt.Errorf("network error"))
				store := storemocks.NewMockSkillStore(ctrl)
				return lookup, nil, nil, store, nil
			},
			wantCode: http.StatusNotFound,
			wantErr:  "not found in local store or registry",
		},
		{
			name: "nil skill lookup returns not found",
			opts: skills.InstallOptions{Name: "my-skill"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*regmocks.MockProvider, *ocimocks.MockRegistryClient, *ociskills.Store, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				store := storemocks.NewMockSkillStore(ctrl)
				return nil, nil, nil, store, nil
			},
			wantCode: http.StatusNotFound,
			wantErr:  "not found in local store or registry",
		},
		{
			name: "partial name match only returns not found",
			opts: skills.InstallOptions{Name: "my-skill"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*regmocks.MockProvider, *ocimocks.MockRegistryClient, *ociskills.Store, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				lookup := regmocks.NewMockProvider(ctrl)
				// Search returns a skill with a different name (partial match only).
				lookup.EXPECT().SearchSkills("my-skill").Return([]regtypes.Skill{
					{Namespace: "io.github.test", Name: "my-skill-extended"},
				}, nil)
				store := storemocks.NewMockSkillStore(ctrl)
				return lookup, nil, nil, store, nil
			},
			wantCode: http.StatusNotFound,
			wantErr:  "not found in local store or registry",
		},
		{
			name: "invalid OCI identifier in registry result returns unprocessable",
			opts: skills.InstallOptions{Name: "my-skill"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*regmocks.MockProvider, *ocimocks.MockRegistryClient, *ociskills.Store, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				lookup := regmocks.NewMockProvider(ctrl)
				lookup.EXPECT().SearchSkills("my-skill").Return([]regtypes.Skill{
					{
						Namespace: "io.github.test",
						Name:      "my-skill",
						Packages: []regtypes.SkillPackage{
							{RegistryType: "oci", Identifier: "!!!invalid-ref!!!"},
						},
					},
				}, nil)
				store := storemocks.NewMockSkillStore(ctrl)
				return lookup, nil, nil, store, nil
			},
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "invalid OCI identifier",
		},
		{
			name: "case-insensitive name match resolves correctly",
			opts: skills.InstallOptions{Name: "my-skill"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*regmocks.MockProvider, *ocimocks.MockRegistryClient, *ociskills.Store, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				lookup := regmocks.NewMockProvider(ctrl)
				// Registry returns mixed-case name; should still match.
				lookup.EXPECT().SearchSkills("my-skill").Return([]regtypes.Skill{
					{
						Namespace: "io.github.test",
						Name:      "My-Skill",
						Packages: []regtypes.SkillPackage{
							{RegistryType: "oci", Identifier: "ghcr.io/test/my-skill:v1.0.0"},
						},
					},
				}, nil)

				ociStore, err := ociskills.NewStore(tempDir(t))
				require.NoError(t, err)
				indexDigest := buildTestArtifact(t, ociStore, "my-skill", "1.0.0")

				reg := ocimocks.NewMockRegistryClient(ctrl)
				reg.EXPECT().Pull(gomock.Any(), gomock.Any(), "ghcr.io/test/my-skill:v1.0.0").Return(indexDigest, nil)

				store := storemocks.NewMockSkillStore(ctrl)
				store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(skills.InstalledSkill{}, storage.ErrNotFound)
				store.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)

				pr := skillsmocks.NewMockPathResolver(ctrl)
				pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(filepath.Join(tempDir(t), "installed", "my-skill"), nil)
				pr.EXPECT().ListSkillSupportingClients().Return([]string{"claude-code"})

				return lookup, reg, ociStore, store, pr
			},
			wantName: "my-skill",
		},
		{
			name: "supply chain: registry reference with wrong repo name is rejected",
			opts: skills.InstallOptions{Name: "my-skill"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*regmocks.MockProvider, *ocimocks.MockRegistryClient, *ociskills.Store, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				lookup := regmocks.NewMockProvider(ctrl)
				// Registry points to wrong-name repo, but artifact declares my-skill.
				lookup.EXPECT().SearchSkills("my-skill").Return([]regtypes.Skill{
					{
						Namespace: "io.github.test",
						Name:      "my-skill",
						Packages: []regtypes.SkillPackage{
							{RegistryType: "oci", Identifier: "ghcr.io/test/wrong-name:v1.0.0"},
						},
					},
				}, nil)

				ociStore, err := ociskills.NewStore(tempDir(t))
				require.NoError(t, err)
				// Build artifact with name "my-skill" but the ref says "wrong-name".
				indexDigest := buildTestArtifact(t, ociStore, "my-skill", "1.0.0")

				reg := ocimocks.NewMockRegistryClient(ctrl)
				reg.EXPECT().Pull(gomock.Any(), gomock.Any(), "ghcr.io/test/wrong-name:v1.0.0").Return(indexDigest, nil)

				store := storemocks.NewMockSkillStore(ctrl)
				pr := skillsmocks.NewMockPathResolver(ctrl)
				pr.EXPECT().ListSkillSupportingClients().Return([]string{"claude-code"}).AnyTimes()

				return lookup, reg, ociStore, store, pr
			},
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "does not match OCI reference repository",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			lookup, reg, ociStore, store, pr := tt.setup(t, ctrl)

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
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			if tt.wantName != "" {
				assert.Equal(t, tt.wantName, result.Skill.Metadata.Name)
			}
			if tt.wantDigest {
				assert.Contains(t, result.Skill.Digest, "sha256:")
			}
			if tt.wantVersion != "" {
				assert.Equal(t, tt.wantVersion, result.Skill.Metadata.Version)
			}
		})
	}
}

func TestBuildGitReferenceFromRegistryURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rawURL  string
		want    string
		wantErr string
	}{
		{
			name:   "https URL converts to git scheme",
			rawURL: "https://github.com/org/repo",
			want:   "git://github.com/org/repo",
		},
		{
			name:   "http URL silently promoted to git scheme",
			rawURL: "http://github.com/org/repo",
			want:   "git://github.com/org/repo",
		},
		{
			name:   "git URL passes through unchanged",
			rawURL: "git://github.com/org/repo",
			want:   "git://github.com/org/repo",
		},
		{
			name:   "https URL with nested path",
			rawURL: "https://github.com/org/repo@v1.0#skills/foo",
			want:   "git://github.com/org/repo@v1.0#skills/foo",
		},
		{
			name:    "empty git reference",
			rawURL:  "git://",
			wantErr: "invalid git reference",
		},
		{
			name:    "unsupported ftp scheme",
			rawURL:  "ftp://github.com/org/repo",
			wantErr: "unsupported URL scheme",
		},
		{
			name:    "bare string no scheme",
			rawURL:  "noscheme/org/repo",
			wantErr: "unsupported URL scheme",
		},
		{
			name:    "https URL missing repo path",
			rawURL:  "https://github.com",
			wantErr: "no repository path after host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := buildGitReferenceFromRegistryURL(tt.rawURL)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSplitQualifiedName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		wantNS   string
		wantName string
	}{
		{"skill-creator", "", "skill-creator"},
		{"io.github.stacklok/skill-creator", "io.github.stacklok", "skill-creator"},
		{"deep/nested/name", "deep/nested", "name"},
		{"", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			ns, name := splitQualifiedName(tt.input)
			assert.Equal(t, tt.wantNS, ns)
			assert.Equal(t, tt.wantName, name)
		})
	}
}

func TestResolveRegistryPackagesSubfolder(t *testing.T) {
	t.Parallel()

	result, err := resolveRegistryPackages("my-skill", []regtypes.SkillPackage{
		{
			RegistryType: "git",
			URL:          "https://github.com/org/repo",
			Subfolder:    "skills/my-skill",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, result.GitURL, "#skills/my-skill")
}

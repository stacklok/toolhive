// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
	regtypes "github.com/stacklok/toolhive-core/registry/types"
	groupmocks "github.com/stacklok/toolhive/pkg/groups/mocks"
	regmocks "github.com/stacklok/toolhive/pkg/registry/mocks"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
	gitmocks "github.com/stacklok/toolhive/pkg/skills/gitresolver/mocks"
	skillsmocks "github.com/stacklok/toolhive/pkg/skills/mocks"
	"github.com/stacklok/toolhive/pkg/storage"
	storemocks "github.com/stacklok/toolhive/pkg/storage/mocks"
)

func TestInstallFromGit(t *testing.T) {
	t.Parallel()

	commitHash := testCommitHash

	tests := []struct {
		name        string
		opts        skills.InstallOptions
		setup       func(t *testing.T, ctrl *gomock.Controller) (*gitmocks.MockResolver, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver)
		wantCode    int
		wantErr     string
		wantName    string
		wantDigest  string
		wantVersion string
	}{
		{
			name: "git reference installs via git resolver",
			opts: skills.InstallOptions{Name: "git://github.com/test/my-skill@v1.0.0"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*gitmocks.MockResolver, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				gr := gitmocks.NewMockResolver(ctrl)
				gr.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(&gitresolver.ResolveResult{
					SkillConfig: &skills.ParseResult{Name: "my-skill", Version: "1.0.0"},
					Files: []gitresolver.FileEntry{
						{Path: "SKILL.md", Content: []byte("---\nname: my-skill\n---\n# Skill"), Mode: 0644},
					},
					CommitHash: commitHash,
				}, nil)

				installBase := filepath.Join(tempDir(t), "installed")
				require.NoError(t, os.MkdirAll(installBase, 0o755))

				store := storemocks.NewMockSkillStore(ctrl)
				store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(skills.InstalledSkill{}, storage.ErrNotFound)
				store.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)

				pr := skillsmocks.NewMockPathResolver(ctrl)
				pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(filepath.Join(installBase, "my-skill"), nil)
				pr.EXPECT().ListSkillSupportingClients().Return([]string{"claude-code"})

				return gr, store, pr
			},
			wantName:    "my-skill",
			wantDigest:  commitHash,
			wantVersion: "1.0.0",
		},
		{
			name: "git reference with nil resolver returns 500",
			opts: skills.InstallOptions{Name: "git://github.com/test/my-skill"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*gitmocks.MockResolver, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				store := storemocks.NewMockSkillStore(ctrl)
				return nil, store, nil
			},
			wantCode: http.StatusInternalServerError,
			wantErr:  "git resolver is not configured",
		},
		{
			name: "malformed git reference returns 400",
			opts: skills.InstallOptions{Name: "git://"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*gitmocks.MockResolver, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				gr := gitmocks.NewMockResolver(ctrl)
				store := storemocks.NewMockSkillStore(ctrl)
				pr := skillsmocks.NewMockPathResolver(ctrl)
				return gr, store, pr
			},
			wantCode: http.StatusBadRequest,
			wantErr:  "invalid git reference",
		},
		{
			name: "git resolve failure returns 502",
			opts: skills.InstallOptions{Name: "git://github.com/test/my-skill"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*gitmocks.MockResolver, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				gr := gitmocks.NewMockResolver(ctrl)
				gr.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("clone failed"))

				store := storemocks.NewMockSkillStore(ctrl)
				pr := skillsmocks.NewMockPathResolver(ctrl)
				return gr, store, pr
			},
			wantCode: http.StatusBadGateway,
			wantErr:  "resolving git skill",
		},
		{
			name: "same commit hash is no-op",
			opts: skills.InstallOptions{Name: "git://github.com/test/my-skill"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*gitmocks.MockResolver, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				gr := gitmocks.NewMockResolver(ctrl)
				gr.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(&gitresolver.ResolveResult{
					SkillConfig: &skills.ParseResult{Name: "my-skill", Version: "1.0.0"},
					Files:       []gitresolver.FileEntry{{Path: "SKILL.md", Content: []byte("test"), Mode: 0644}},
					CommitHash:  commitHash,
				}, nil)

				store := storemocks.NewMockSkillStore(ctrl)
				store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(skills.InstalledSkill{
					Metadata: skills.SkillMetadata{Name: "my-skill", Version: "1.0.0"},
					Digest:   commitHash,
					Status:   skills.InstallStatusInstalled,
				}, nil)

				pr := skillsmocks.NewMockPathResolver(ctrl)
				pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(filepath.Join(tempDir(t), "installed", "my-skill"), nil)
				pr.EXPECT().ListSkillSupportingClients().Return([]string{"claude-code"})

				return gr, store, pr
			},
			wantName:   "my-skill",
			wantDigest: commitHash,
		},
		{
			name: "unmanaged directory without force returns conflict",
			opts: skills.InstallOptions{Name: "git://github.com/test/my-skill"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*gitmocks.MockResolver, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				gr := gitmocks.NewMockResolver(ctrl)
				gr.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(&gitresolver.ResolveResult{
					SkillConfig: &skills.ParseResult{Name: "my-skill", Version: "1.0.0"},
					Files:       []gitresolver.FileEntry{{Path: "SKILL.md", Content: []byte("test"), Mode: 0644}},
					CommitHash:  commitHash,
				}, nil)

				// Create the target dir to simulate an unmanaged directory.
				installBase := filepath.Join(tempDir(t), "installed")
				installDir := filepath.Join(installBase, "my-skill")
				require.NoError(t, os.MkdirAll(installDir, 0o755))

				store := storemocks.NewMockSkillStore(ctrl)
				store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(skills.InstalledSkill{}, storage.ErrNotFound)

				pr := skillsmocks.NewMockPathResolver(ctrl)
				pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(installDir, nil)
				pr.EXPECT().ListSkillSupportingClients().Return([]string{"claude-code"})

				return gr, store, pr
			},
			wantCode: http.StatusConflict,
			wantErr:  "not managed by ToolHive",
		},
		{
			name: "different commit hash triggers upgrade",
			opts: skills.InstallOptions{Name: "git://github.com/test/my-skill"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*gitmocks.MockResolver, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				newCommit := "1111111111111111111111111111111111111111"

				gr := gitmocks.NewMockResolver(ctrl)
				gr.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(&gitresolver.ResolveResult{
					SkillConfig: &skills.ParseResult{Name: "my-skill", Version: "2.0.0"},
					Files:       []gitresolver.FileEntry{{Path: "SKILL.md", Content: []byte("---\nname: my-skill\n---\n# v2"), Mode: 0644}},
					CommitHash:  newCommit,
				}, nil)

				// Create the parent directory so the file lock can be created.
				installBase := filepath.Join(tempDir(t), "installed")
				require.NoError(t, os.MkdirAll(installBase, 0o755))
				installDir := filepath.Join(installBase, "my-skill")

				store := storemocks.NewMockSkillStore(ctrl)
				store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(skills.InstalledSkill{
					Metadata: skills.SkillMetadata{Name: "my-skill", Version: "1.0.0"},
					Digest:   commitHash,
					Clients:  []string{"claude-code"},
				}, nil)
				store.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil)

				pr := skillsmocks.NewMockPathResolver(ctrl)
				pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(installDir, nil)
				pr.EXPECT().ListSkillSupportingClients().Return([]string{"claude-code"})

				return gr, store, pr
			},
			wantName:    "my-skill",
			wantDigest:  "1111111111111111111111111111111111111111",
			wantVersion: "2.0.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			gr, store, pr := tt.setup(t, ctrl)

			var opts []Option
			if gr != nil {
				opts = append(opts, WithGitResolver(gr))
			}
			if pr != nil {
				opts = append(opts, WithPathResolver(pr))
			}

			svc := New(store, opts...)

			// Override the default git resolver when nil is expected.
			if gr == nil {
				svc.(*service).gitResolver = nil
			}

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
			if tt.wantDigest != "" {
				assert.Equal(t, tt.wantDigest, result.Skill.Digest)
			}
			if tt.wantVersion != "" {
				assert.Equal(t, tt.wantVersion, result.Skill.Metadata.Version)
			}
		})
	}
}

func TestInstallFromGitGroupRegistrationRollback(t *testing.T) {
	t.Parallel()

	commitHash := testCommitHash

	ctrl := gomock.NewController(t)

	gr := gitmocks.NewMockResolver(ctrl)
	gr.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(&gitresolver.ResolveResult{
		SkillConfig: &skills.ParseResult{Name: "my-skill", Version: "1.0.0"},
		Files: []gitresolver.FileEntry{
			{Path: "SKILL.md", Content: []byte("---\nname: my-skill\n---\n# Skill"), Mode: 0644},
		},
		CommitHash: commitHash,
	}, nil)

	installBase := filepath.Join(tempDir(t), "installed")
	require.NoError(t, os.MkdirAll(installBase, 0o755))

	store := storemocks.NewMockSkillStore(ctrl)
	store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(skills.InstalledSkill{}, storage.ErrNotFound)
	store.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
	// Rollback: DB record is removed when group registration fails.
	store.EXPECT().Delete(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(nil)

	pr := skillsmocks.NewMockPathResolver(ctrl)
	pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(filepath.Join(installBase, "my-skill"), nil)
	pr.EXPECT().ListSkillSupportingClients().Return([]string{"claude-code"})

	gm := groupmocks.NewMockManager(ctrl)
	gm.EXPECT().Get(gomock.Any(), "badgroup").Return(nil, fmt.Errorf("group not found"))

	svc := New(store, WithGitResolver(gr), WithPathResolver(pr), WithGroupManager(gm))

	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name:  "git://github.com/test/my-skill@v1.0.0",
		Group: "badgroup",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "registering skill in group")
	assert.Contains(t, err.Error(), "group not found")
}

func TestInstallFromRegistryGitFallback(t *testing.T) {
	t.Parallel()

	commitHash := testCommitHash

	tests := []struct {
		name     string
		opts     skills.InstallOptions
		setup    func(t *testing.T, ctrl *gomock.Controller) (*regmocks.MockProvider, *gitmocks.MockResolver, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver)
		wantCode int
		wantErr  string
		wantName string
	}{
		{
			name: "registry git package triggers git install",
			opts: skills.InstallOptions{Name: "my-skill"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*regmocks.MockProvider, *gitmocks.MockResolver, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				lookup := regmocks.NewMockProvider(ctrl)
				lookup.EXPECT().SearchSkills("my-skill").Return([]regtypes.Skill{
					{
						Namespace: "io.github.test",
						Name:      "my-skill",
						Packages: []regtypes.SkillPackage{
							{RegistryType: "git", URL: "https://github.com/test/my-skill"},
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

				return lookup, gr, store, pr
			},
			wantName: "my-skill",
		},
		{
			name: "registry git package with invalid URL returns unprocessable",
			opts: skills.InstallOptions{Name: "my-skill"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (*regmocks.MockProvider, *gitmocks.MockResolver, *storemocks.MockSkillStore, *skillsmocks.MockPathResolver) {
				t.Helper()
				lookup := regmocks.NewMockProvider(ctrl)
				lookup.EXPECT().SearchSkills("my-skill").Return([]regtypes.Skill{
					{
						Namespace: "io.github.test",
						Name:      "my-skill",
						Packages: []regtypes.SkillPackage{
							{RegistryType: "git", URL: "ftp://invalid/no-owner"},
						},
					},
				}, nil)

				store := storemocks.NewMockSkillStore(ctrl)
				return lookup, nil, store, nil
			},
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "invalid git URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			lookup, gr, store, pr := tt.setup(t, ctrl)

			var opts []Option
			if lookup != nil {
				opts = append(opts, WithSkillLookup(lookup))
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

// TestInstallQualifiedNameOCIFallback covers the scenario where Install
// receives a "namespace/name" string, which is parsed as an OCI reference but
// fails to pull because the namespace looks like a registry host. The service
// must fall back to a registry catalogue lookup and complete the install from
// the resolved package (OCI or git). Names that carry an explicit tag or digest
// (unambiguously OCI) must NOT trigger a fallback.

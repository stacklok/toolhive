// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	godigest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
	ociskills "github.com/stacklok/toolhive-core/oci/skills"
	ocimocks "github.com/stacklok/toolhive-core/oci/skills/mocks"
	"github.com/stacklok/toolhive/pkg/skills"
	skillsmocks "github.com/stacklok/toolhive/pkg/skills/mocks"
	"github.com/stacklok/toolhive/pkg/storage"
	storemocks "github.com/stacklok/toolhive/pkg/storage/mocks"
)

func makeLayerData(t *testing.T) []byte {
	t.Helper()
	files := []ociskills.FileEntry{
		{Path: "SKILL.md", Content: []byte("---\nname: my-skill\ndescription: test\n---\n# Skill"), Mode: 0644},
	}
	data, err := ociskills.CompressTar(files, ociskills.DefaultTarOptions(), ociskills.DefaultGzipOptions())
	require.NoError(t, err)
	return data
}

func TestList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		opts      skills.ListOptions
		setupMock func(*storemocks.MockSkillStore)
		wantErr   string
		wantCount int
	}{
		{
			name: "delegates to store with scope",
			opts: skills.ListOptions{Scope: skills.ScopeUser},
			setupMock: func(s *storemocks.MockSkillStore) {
				s.EXPECT().List(gomock.Any(), storage.ListFilter{Scope: skills.ScopeUser}).
					Return([]skills.InstalledSkill{{Metadata: skills.SkillMetadata{Name: "my-skill"}}}, nil)
			},
			wantCount: 1,
		},
		{
			name: "empty scope returns all",
			opts: skills.ListOptions{},
			setupMock: func(s *storemocks.MockSkillStore) {
				s.EXPECT().List(gomock.Any(), storage.ListFilter{}).
					Return([]skills.InstalledSkill{}, nil)
			},
			wantCount: 0,
		},
		{
			name: "propagates store errors",
			opts: skills.ListOptions{},
			setupMock: func(s *storemocks.MockSkillStore) {
				s.EXPECT().List(gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("db error"))
			},
			wantErr: "db error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			store := storemocks.NewMockSkillStore(ctrl)
			tt.setupMock(store)

			result, err := New(store).List(t.Context(), tt.opts)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Len(t, result, tt.wantCount)
		})
	}
}

func TestInstallPending(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		opts      skills.InstallOptions
		setupMock func(*storemocks.MockSkillStore)
		wantCode  int
		wantName  string
		wantScope skills.Scope
	}{
		{
			name: "creates pending record with defaults",
			opts: skills.InstallOptions{Name: "my-skill"},
			setupMock: func(s *storemocks.MockSkillStore) {
				s.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ context.Context, sk skills.InstalledSkill) error {
						assert.Equal(t, "my-skill", sk.Metadata.Name)
						assert.Equal(t, skills.ScopeUser, sk.Scope)
						assert.Equal(t, skills.InstallStatusPending, sk.Status)
						assert.False(t, sk.InstalledAt.IsZero())
						return nil
					})
			},
			wantName:  "my-skill",
			wantScope: skills.ScopeUser,
		},
		{
			name: "propagates version",
			opts: skills.InstallOptions{Name: "my-skill", Version: "2.1.0"},
			setupMock: func(s *storemocks.MockSkillStore) {
				s.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ context.Context, sk skills.InstalledSkill) error {
						assert.Equal(t, "2.1.0", sk.Metadata.Version)
						return nil
					})
			},
			wantName: "my-skill",
		},
		{
			name: "respects explicit scope",
			opts: skills.InstallOptions{Name: "my-skill", Scope: skills.ScopeProject},
			setupMock: func(s *storemocks.MockSkillStore) {
				s.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ context.Context, sk skills.InstalledSkill) error {
						assert.Equal(t, skills.ScopeProject, sk.Scope)
						return nil
					})
			},
			wantName:  "my-skill",
			wantScope: skills.ScopeProject,
		},
		{
			name:      "rejects invalid name",
			opts:      skills.InstallOptions{Name: "A"},
			setupMock: func(_ *storemocks.MockSkillStore) {},
			wantCode:  http.StatusBadRequest,
		},
		{
			name:      "rejects empty name",
			opts:      skills.InstallOptions{Name: ""},
			setupMock: func(_ *storemocks.MockSkillStore) {},
			wantCode:  http.StatusBadRequest,
		},
		{
			name: "returns conflict on duplicate",
			opts: skills.InstallOptions{Name: "my-skill"},
			setupMock: func(s *storemocks.MockSkillStore) {
				s.EXPECT().Create(gomock.Any(), gomock.Any()).Return(storage.ErrAlreadyExists)
			},
			wantCode: http.StatusConflict,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			store := storemocks.NewMockSkillStore(ctrl)
			tt.setupMock(store)

			result, err := New(store).Install(t.Context(), tt.opts)
			if tt.wantCode != 0 {
				require.Error(t, err)
				assert.Equal(t, tt.wantCode, httperr.Code(err))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantName, result.Skill.Metadata.Name)
			assert.Equal(t, skills.InstallStatusPending, result.Skill.Status)
		})
	}
}

func TestInstallWithExtraction(t *testing.T) {
	t.Parallel()

	layerData := makeLayerData(t)

	t.Run("fresh install creates record and extracts files", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)
		pr := skillsmocks.NewMockPathResolver(ctrl)

		targetDir := filepath.Join(t.TempDir(), "my-skill")
		pr.EXPECT().ListSkillSupportingClients().Return([]string{"claude-code"})
		pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(targetDir, nil)
		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(skills.InstalledSkill{}, storage.ErrNotFound)
		store.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, sk skills.InstalledSkill) error {
				assert.Equal(t, skills.InstallStatusInstalled, sk.Status)
				assert.Equal(t, "sha256:abc", sk.Digest)
				assert.Equal(t, []string{"claude-code"}, sk.Clients)
				return nil
			})

		svc := New(store, WithPathResolver(pr))
		result, err := svc.Install(t.Context(), skills.InstallOptions{
			Name:      "my-skill",
			LayerData: layerData,
			Digest:    "sha256:abc",
		})
		require.NoError(t, err)
		assert.Equal(t, skills.InstallStatusInstalled, result.Skill.Status)

		// Verify files were extracted
		_, statErr := os.Stat(filepath.Join(targetDir, "SKILL.md"))
		assert.NoError(t, statErr)
	})

	t.Run("same digest is no-op", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)
		pr := skillsmocks.NewMockPathResolver(ctrl)

		targetDir := filepath.Join(t.TempDir(), "my-skill")
		existing := skills.InstalledSkill{
			Metadata: skills.SkillMetadata{Name: "my-skill"},
			Digest:   "sha256:abc",
			Status:   skills.InstallStatusInstalled,
		}

		pr.EXPECT().ListSkillSupportingClients().Return([]string{"claude-code"})
		pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(targetDir, nil)
		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(existing, nil)
		// No Create or Update should be called

		svc := New(store, WithPathResolver(pr))
		result, err := svc.Install(t.Context(), skills.InstallOptions{
			Name:      "my-skill",
			LayerData: layerData,
			Digest:    "sha256:abc",
		})
		require.NoError(t, err)
		assert.Equal(t, "my-skill", result.Skill.Metadata.Name)
	})

	t.Run("different digest triggers upgrade", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)
		pr := skillsmocks.NewMockPathResolver(ctrl)

		targetDir := filepath.Join(t.TempDir(), "my-skill")
		existing := skills.InstalledSkill{
			Metadata: skills.SkillMetadata{Name: "my-skill"},
			Digest:   "sha256:old",
			Status:   skills.InstallStatusInstalled,
			Clients:  []string{"claude-code"},
		}

		pr.EXPECT().ListSkillSupportingClients().Return([]string{"claude-code"})
		pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(targetDir, nil)
		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(existing, nil)
		store.EXPECT().Update(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, sk skills.InstalledSkill) error {
				assert.Equal(t, "sha256:new", sk.Digest)
				assert.Equal(t, skills.InstallStatusInstalled, sk.Status)
				return nil
			})

		svc := New(store, WithPathResolver(pr))
		result, err := svc.Install(t.Context(), skills.InstallOptions{
			Name:      "my-skill",
			LayerData: layerData,
			Digest:    "sha256:new",
		})
		require.NoError(t, err)
		assert.Equal(t, "sha256:new", result.Skill.Digest)
	})

	t.Run("unmanaged directory refused without force", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)
		pr := skillsmocks.NewMockPathResolver(ctrl)

		// Create an existing unmanaged directory
		targetDir := filepath.Join(t.TempDir(), "my-skill")
		require.NoError(t, os.MkdirAll(targetDir, 0750))

		pr.EXPECT().ListSkillSupportingClients().Return([]string{"claude-code"})
		pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(targetDir, nil)
		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(skills.InstalledSkill{}, storage.ErrNotFound)

		svc := New(store, WithPathResolver(pr))
		_, err := svc.Install(t.Context(), skills.InstallOptions{
			Name:      "my-skill",
			LayerData: layerData,
			Digest:    "sha256:abc",
		})
		require.Error(t, err)
		assert.Equal(t, http.StatusConflict, httperr.Code(err))
		assert.Contains(t, err.Error(), "not managed by ToolHive")
	})

	t.Run("unmanaged directory overwritten with force", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)
		pr := skillsmocks.NewMockPathResolver(ctrl)

		// Create an existing unmanaged directory
		targetDir := filepath.Join(t.TempDir(), "my-skill")
		require.NoError(t, os.MkdirAll(targetDir, 0750))

		pr.EXPECT().ListSkillSupportingClients().Return([]string{"claude-code"})
		pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(targetDir, nil)
		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(skills.InstalledSkill{}, storage.ErrNotFound)
		store.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)

		svc := New(store, WithPathResolver(pr))
		result, err := svc.Install(t.Context(), skills.InstallOptions{
			Name:      "my-skill",
			LayerData: layerData,
			Digest:    "sha256:abc",
			Force:     true,
		})
		require.NoError(t, err)
		assert.Equal(t, skills.InstallStatusInstalled, result.Skill.Status)
	})

	t.Run("explicit client used over default", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)
		pr := skillsmocks.NewMockPathResolver(ctrl)

		targetDir := filepath.Join(t.TempDir(), "my-skill")
		pr.EXPECT().GetSkillPath("custom-client", "my-skill", skills.ScopeUser, "").Return(targetDir, nil)
		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(skills.InstalledSkill{}, storage.ErrNotFound)
		store.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, sk skills.InstalledSkill) error {
				assert.Equal(t, []string{"custom-client"}, sk.Clients)
				return nil
			})

		svc := New(store, WithPathResolver(pr))
		_, err := svc.Install(t.Context(), skills.InstallOptions{
			Name:      "my-skill",
			LayerData: layerData,
			Client:    "custom-client",
			Digest:    "sha256:abc",
		})
		require.NoError(t, err)
	})

	t.Run("fresh install rolls back extraction on store.Create failure", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)
		pr := skillsmocks.NewMockPathResolver(ctrl)
		inst := skillsmocks.NewMockInstaller(ctrl)

		targetDir := filepath.Join(t.TempDir(), "my-skill")
		pr.EXPECT().ListSkillSupportingClients().Return([]string{"claude-code"})
		pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(targetDir, nil)
		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(skills.InstalledSkill{}, storage.ErrNotFound)
		inst.EXPECT().Extract(layerData, targetDir, false).Return(&skills.ExtractResult{SkillDir: targetDir, Files: 1}, nil)
		store.EXPECT().Create(gomock.Any(), gomock.Any()).Return(fmt.Errorf("db write error"))
		inst.EXPECT().Remove(targetDir).Return(nil)

		svc := New(store, WithPathResolver(pr), WithInstaller(inst))
		_, err := svc.Install(t.Context(), skills.InstallOptions{
			Name:      "my-skill",
			LayerData: layerData,
			Digest:    "sha256:abc",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "db write error")
	})

	t.Run("upgrade rolls back extraction on store.Update failure", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)
		pr := skillsmocks.NewMockPathResolver(ctrl)
		inst := skillsmocks.NewMockInstaller(ctrl)

		targetDir := filepath.Join(t.TempDir(), "my-skill")
		existing := skills.InstalledSkill{
			Metadata: skills.SkillMetadata{Name: "my-skill"},
			Digest:   "sha256:old",
			Status:   skills.InstallStatusInstalled,
			Clients:  []string{"claude-code"},
		}

		pr.EXPECT().ListSkillSupportingClients().Return([]string{"claude-code"})
		pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(targetDir, nil)
		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(existing, nil)
		inst.EXPECT().Extract(layerData, targetDir, true).Return(&skills.ExtractResult{SkillDir: targetDir, Files: 1}, nil)
		store.EXPECT().Update(gomock.Any(), gomock.Any()).Return(fmt.Errorf("db update error"))
		inst.EXPECT().Remove(targetDir).Return(nil)

		svc := New(store, WithPathResolver(pr), WithInstaller(inst))
		_, err := svc.Install(t.Context(), skills.InstallOptions{
			Name:      "my-skill",
			LayerData: layerData,
			Digest:    "sha256:new",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "db update error")
	})
}

func TestUninstall(t *testing.T) {
	t.Parallel()

	t.Run("success with file cleanup", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)
		pr := skillsmocks.NewMockPathResolver(ctrl)

		// Create a skill directory to be cleaned up
		skillDir := filepath.Join(t.TempDir(), "my-skill")
		require.NoError(t, os.MkdirAll(skillDir, 0750))
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("test"), 0600))

		existing := skills.InstalledSkill{
			Metadata: skills.SkillMetadata{Name: "my-skill"},
			Scope:    skills.ScopeUser,
			Clients:  []string{"claude-code"},
		}

		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(existing, nil)
		pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(skillDir, nil)
		store.EXPECT().Delete(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(nil)

		svc := New(store, WithPathResolver(pr))
		err := svc.Uninstall(t.Context(), skills.UninstallOptions{Name: "my-skill"})
		require.NoError(t, err)

		// Verify directory was removed
		_, statErr := os.Stat(skillDir)
		assert.True(t, os.IsNotExist(statErr))
	})

	t.Run("success without path resolver", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)

		existing := skills.InstalledSkill{
			Metadata: skills.SkillMetadata{Name: "my-skill"},
			Scope:    skills.ScopeUser,
		}

		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(existing, nil)
		store.EXPECT().Delete(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(nil)

		svc := New(store)
		err := svc.Uninstall(t.Context(), skills.UninstallOptions{Name: "my-skill"})
		require.NoError(t, err)
	})

	t.Run("respects explicit scope", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)

		existing := skills.InstalledSkill{
			Metadata: skills.SkillMetadata{Name: "my-skill"},
			Scope:    skills.ScopeProject,
		}

		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeProject, "").Return(existing, nil)
		store.EXPECT().Delete(gomock.Any(), "my-skill", skills.ScopeProject, "").Return(nil)

		svc := New(store)
		err := svc.Uninstall(t.Context(), skills.UninstallOptions{Name: "my-skill", Scope: skills.ScopeProject})
		require.NoError(t, err)
	})

	t.Run("returns 404 when not found", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)

		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(skills.InstalledSkill{}, storage.ErrNotFound)

		svc := New(store)
		err := svc.Uninstall(t.Context(), skills.UninstallOptions{Name: "my-skill"})
		require.Error(t, err)
		assert.Equal(t, http.StatusNotFound, httperr.Code(err))
	})

	t.Run("rejects invalid name", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)

		svc := New(store)
		err := svc.Uninstall(t.Context(), skills.UninstallOptions{Name: "X"})
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
	})

	t.Run("cleans up all clients", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)
		pr := skillsmocks.NewMockPathResolver(ctrl)

		dir1 := filepath.Join(t.TempDir(), "client1", "my-skill")
		dir2 := filepath.Join(t.TempDir(), "client2", "my-skill")
		require.NoError(t, os.MkdirAll(dir1, 0750))
		require.NoError(t, os.MkdirAll(dir2, 0750))

		existing := skills.InstalledSkill{
			Metadata: skills.SkillMetadata{Name: "my-skill"},
			Scope:    skills.ScopeUser,
			Clients:  []string{"client-a", "client-b"},
		}

		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(existing, nil)
		pr.EXPECT().GetSkillPath("client-a", "my-skill", skills.ScopeUser, "").Return(dir1, nil)
		pr.EXPECT().GetSkillPath("client-b", "my-skill", skills.ScopeUser, "").Return(dir2, nil)
		store.EXPECT().Delete(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(nil)

		svc := New(store, WithPathResolver(pr))
		err := svc.Uninstall(t.Context(), skills.UninstallOptions{Name: "my-skill"})
		require.NoError(t, err)

		_, statErr1 := os.Stat(dir1)
		assert.True(t, os.IsNotExist(statErr1))
		_, statErr2 := os.Stat(dir2)
		assert.True(t, os.IsNotExist(statErr2))
	})

	t.Run("best-effort cleanup continues on remove error", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)
		pr := skillsmocks.NewMockPathResolver(ctrl)
		inst := skillsmocks.NewMockInstaller(ctrl)

		existing := skills.InstalledSkill{
			Metadata: skills.SkillMetadata{Name: "my-skill"},
			Scope:    skills.ScopeUser,
			Clients:  []string{"client-a", "client-b"},
		}

		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(existing, nil)
		pr.EXPECT().GetSkillPath("client-a", "my-skill", skills.ScopeUser, "").Return("/some/dir-a", nil)
		pr.EXPECT().GetSkillPath("client-b", "my-skill", skills.ScopeUser, "").Return("/some/dir-b", nil)
		// First remove fails, but second should still be attempted
		inst.EXPECT().Remove("/some/dir-a").Return(fmt.Errorf("permission denied"))
		inst.EXPECT().Remove("/some/dir-b").Return(nil)
		store.EXPECT().Delete(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(nil)

		svc := New(store, WithPathResolver(pr), WithInstaller(inst))
		err := svc.Uninstall(t.Context(), skills.UninstallOptions{Name: "my-skill"})
		// Store deletion succeeds, but cleanup errors are returned
		require.Error(t, err)
		assert.Contains(t, err.Error(), "permission denied")
	})
}

func TestInfo(t *testing.T) {
	t.Parallel()

	installed := skills.InstalledSkill{
		Metadata: skills.SkillMetadata{Name: "my-skill", Version: "1.0.0"},
		Scope:    skills.ScopeUser,
		Status:   skills.InstallStatusInstalled,
	}

	tests := []struct {
		name      string
		opts      skills.InfoOptions
		setupMock func(*storemocks.MockSkillStore)
		wantCode  int
		wantErr   string
	}{
		{
			name: "found skill",
			opts: skills.InfoOptions{Name: "my-skill"},
			setupMock: func(s *storemocks.MockSkillStore) {
				s.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(installed, nil)
			},
		},
		{
			name: "not found returns 404",
			opts: skills.InfoOptions{Name: "unknown"},
			setupMock: func(s *storemocks.MockSkillStore) {
				s.EXPECT().Get(gomock.Any(), "unknown", skills.ScopeUser, "").
					Return(skills.InstalledSkill{}, storage.ErrNotFound)
			},
			wantCode: http.StatusNotFound,
		},
		{
			name: "propagates store errors",
			opts: skills.InfoOptions{Name: "my-skill"},
			setupMock: func(s *storemocks.MockSkillStore) {
				s.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").
					Return(skills.InstalledSkill{}, fmt.Errorf("db error"))
			},
			wantErr: "db error",
		},
		{
			name:      "rejects invalid name",
			opts:      skills.InfoOptions{Name: "X"},
			setupMock: func(_ *storemocks.MockSkillStore) {},
			wantCode:  http.StatusBadRequest,
		},
		{
			name:      "rejects empty name",
			opts:      skills.InfoOptions{Name: ""},
			setupMock: func(_ *storemocks.MockSkillStore) {},
			wantCode:  http.StatusBadRequest,
		},
		{
			name: "respects project scope",
			opts: skills.InfoOptions{Name: "my-skill", Scope: skills.ScopeProject},
			setupMock: func(s *storemocks.MockSkillStore) {
				s.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeProject, "").Return(installed, nil)
			},
		},
		{
			name: "defaults to user scope when empty",
			opts: skills.InfoOptions{Name: "my-skill", Scope: ""},
			setupMock: func(s *storemocks.MockSkillStore) {
				s.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(installed, nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			store := storemocks.NewMockSkillStore(ctrl)
			tt.setupMock(store)

			info, err := New(store).Info(t.Context(), tt.opts)
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
			require.NotNil(t, info.InstalledSkill)
			assert.Equal(t, "my-skill", info.InstalledSkill.Metadata.Name)
		})
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	t.Run("valid skill directory", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		skillDir := filepath.Join(dir, "test-skill")
		require.NoError(t, os.MkdirAll(skillDir, 0o750))
		require.NoError(t, os.WriteFile(
			filepath.Join(skillDir, "SKILL.md"),
			[]byte("---\nname: test-skill\ndescription: A test skill\n---\n# Test Skill\n"),
			0o600,
		))

		svc := New(&storage.NoopSkillStore{})
		result, err := svc.Validate(t.Context(), skillDir)
		require.NoError(t, err)
		assert.True(t, result.Valid)
	})

	t.Run("missing SKILL.md", func(t *testing.T) {
		t.Parallel()
		svc := New(&storage.NoopSkillStore{})
		result, err := svc.Validate(t.Context(), t.TempDir())
		require.NoError(t, err)
		assert.False(t, result.Valid)
		assert.Contains(t, result.Errors, "SKILL.md not found in skill directory")
	})

	t.Run("empty path returns 400", func(t *testing.T) {
		t.Parallel()
		svc := New(&storage.NoopSkillStore{})
		_, err := svc.Validate(t.Context(), "")
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
	})

	t.Run("relative path returns 400", func(t *testing.T) {
		t.Parallel()
		svc := New(&storage.NoopSkillStore{})
		_, err := svc.Validate(t.Context(), "relative/path")
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
	})

	t.Run("path traversal returns 400", func(t *testing.T) {
		t.Parallel()
		svc := New(&storage.NoopSkillStore{})
		_, err := svc.Validate(t.Context(), "/foo/../../../etc")
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
	})
}

// putTestManifest stores a minimal manifest in the OCI store and returns its digest.
func putTestManifest(t *testing.T, store *ociskills.Store) godigest.Digest {
	t.Helper()
	d, err := store.PutManifest(t.Context(), []byte(`{"schemaVersion":2}`))
	require.NoError(t, err)
	return d
}

func TestBuild(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		opts     skills.BuildOptions
		setup    func(*gomock.Controller) (ociskills.SkillPackager, *ociskills.Store)
		wantCode int
		wantRef  string
		wantErr  string
	}{
		{
			name: "nil packager returns 500",
			opts: skills.BuildOptions{Path: "/some/dir"},
			setup: func(_ *gomock.Controller) (ociskills.SkillPackager, *ociskills.Store) {
				return nil, nil
			},
			wantCode: http.StatusInternalServerError,
		},
		{
			name: "empty path returns 400",
			opts: skills.BuildOptions{Path: ""},
			setup: func(ctrl *gomock.Controller) (ociskills.SkillPackager, *ociskills.Store) {
				ociStore, err := ociskills.NewStore(t.TempDir())
				require.NoError(t, err)
				return ocimocks.NewMockSkillPackager(ctrl), ociStore
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "relative path returns 400",
			opts: skills.BuildOptions{Path: "relative/path"},
			setup: func(ctrl *gomock.Controller) (ociskills.SkillPackager, *ociskills.Store) {
				ociStore, err := ociskills.NewStore(t.TempDir())
				require.NoError(t, err)
				return ocimocks.NewMockSkillPackager(ctrl), ociStore
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "path traversal returns 400",
			opts: skills.BuildOptions{Path: "/some/dir/../../../etc"},
			setup: func(ctrl *gomock.Controller) (ociskills.SkillPackager, *ociskills.Store) {
				ociStore, err := ociskills.NewStore(t.TempDir())
				require.NoError(t, err)
				return ocimocks.NewMockSkillPackager(ctrl), ociStore
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "invalid tag returns 400",
			opts: skills.BuildOptions{Path: "/some/dir", Tag: "invalid tag!@#"},
			setup: func(ctrl *gomock.Controller) (ociskills.SkillPackager, *ociskills.Store) {
				ociStore, err := ociskills.NewStore(t.TempDir())
				require.NoError(t, err)
				return ocimocks.NewMockSkillPackager(ctrl), ociStore
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "packager error propagates",
			opts: skills.BuildOptions{Path: "/some/dir"},
			setup: func(ctrl *gomock.Controller) (ociskills.SkillPackager, *ociskills.Store) {
				ociStore, err := ociskills.NewStore(t.TempDir())
				require.NoError(t, err)
				p := ocimocks.NewMockSkillPackager(ctrl)
				p.EXPECT().Package(gomock.Any(), "/some/dir", gomock.Any()).
					Return(nil, fmt.Errorf("packaging failed"))
				return p, ociStore
			},
			wantErr: "packaging skill",
		},
		{
			name: "successful build with explicit tag",
			opts: skills.BuildOptions{Path: "/some/dir", Tag: "v1.0.0"},
			setup: func(ctrl *gomock.Controller) (ociskills.SkillPackager, *ociskills.Store) {
				ociStore, err := ociskills.NewStore(t.TempDir())
				require.NoError(t, err)
				d := putTestManifest(t, ociStore)
				p := ocimocks.NewMockSkillPackager(ctrl)
				p.EXPECT().Package(gomock.Any(), "/some/dir", gomock.Any()).
					Return(&ociskills.PackageResult{
						IndexDigest: d,
						Config:      &ociskills.SkillConfig{Name: "my-skill"},
					}, nil)
				return p, ociStore
			},
			wantRef: "v1.0.0",
		},
		{
			name: "build without tag uses config name",
			opts: skills.BuildOptions{Path: "/some/dir"},
			setup: func(ctrl *gomock.Controller) (ociskills.SkillPackager, *ociskills.Store) {
				ociStore, err := ociskills.NewStore(t.TempDir())
				require.NoError(t, err)
				d := putTestManifest(t, ociStore)
				p := ocimocks.NewMockSkillPackager(ctrl)
				p.EXPECT().Package(gomock.Any(), "/some/dir", gomock.Any()).
					Return(&ociskills.PackageResult{
						IndexDigest: d,
						Config:      &ociskills.SkillConfig{Name: "my-skill"},
					}, nil)
				return p, ociStore
			},
			wantRef: "my-skill",
		},
		{
			name: "build without tag or config name returns digest",
			opts: skills.BuildOptions{Path: "/some/dir"},
			setup: func(ctrl *gomock.Controller) (ociskills.SkillPackager, *ociskills.Store) {
				ociStore, err := ociskills.NewStore(t.TempDir())
				require.NoError(t, err)
				d := putTestManifest(t, ociStore)
				p := ocimocks.NewMockSkillPackager(ctrl)
				p.EXPECT().Package(gomock.Any(), "/some/dir", gomock.Any()).
					Return(&ociskills.PackageResult{
						IndexDigest: d,
						Config:      &ociskills.SkillConfig{},
					}, nil)
				return p, ociStore
			},
			// wantRef is set dynamically below since the digest depends on store content
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			packager, ociStore := tt.setup(ctrl)

			svc := New(&storage.NoopSkillStore{},
				WithPackager(packager),
				WithOCIStore(ociStore),
			)

			result, err := svc.Build(t.Context(), tt.opts)
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
			if tt.wantRef != "" {
				assert.Equal(t, tt.wantRef, result.Reference)
			} else {
				// Fallback case returns a digest string
				assert.Contains(t, result.Reference, "sha256:")
			}
		})
	}
}

func TestPush(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		opts     skills.PushOptions
		setup    func(*gomock.Controller) (ociskills.RegistryClient, *ociskills.Store)
		wantCode int
		wantErr  string
	}{
		{
			name: "nil registry returns 500",
			opts: skills.PushOptions{Reference: "ghcr.io/test/skill:v1"},
			setup: func(_ *gomock.Controller) (ociskills.RegistryClient, *ociskills.Store) {
				return nil, nil
			},
			wantCode: http.StatusInternalServerError,
		},
		{
			name: "empty reference returns 400",
			opts: skills.PushOptions{Reference: ""},
			setup: func(ctrl *gomock.Controller) (ociskills.RegistryClient, *ociskills.Store) {
				ociStore, err := ociskills.NewStore(t.TempDir())
				require.NoError(t, err)
				return ocimocks.NewMockRegistryClient(ctrl), ociStore
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "resolve not found returns 404",
			opts: skills.PushOptions{Reference: "nonexistent"},
			setup: func(ctrl *gomock.Controller) (ociskills.RegistryClient, *ociskills.Store) {
				ociStore, err := ociskills.NewStore(t.TempDir())
				require.NoError(t, err)
				return ocimocks.NewMockRegistryClient(ctrl), ociStore
			},
			wantCode: http.StatusNotFound,
		},
		{
			name: "registry push error propagates",
			opts: skills.PushOptions{Reference: "my-tag"},
			setup: func(ctrl *gomock.Controller) (ociskills.RegistryClient, *ociskills.Store) {
				ociStore, err := ociskills.NewStore(t.TempDir())
				require.NoError(t, err)
				// Create a manifest so Resolve succeeds.
				d, tagErr := ociStore.PutManifest(t.Context(), []byte(`{"schemaVersion":2}`))
				require.NoError(t, tagErr)
				require.NoError(t, ociStore.Tag(t.Context(), d, "my-tag"))

				reg := ocimocks.NewMockRegistryClient(ctrl)
				reg.EXPECT().Push(gomock.Any(), ociStore, d, "my-tag").
					Return(fmt.Errorf("auth failed"))
				return reg, ociStore
			},
			wantErr: "pushing to registry",
		},
		{
			name: "successful push",
			opts: skills.PushOptions{Reference: "my-tag"},
			setup: func(ctrl *gomock.Controller) (ociskills.RegistryClient, *ociskills.Store) {
				ociStore, err := ociskills.NewStore(t.TempDir())
				require.NoError(t, err)
				d, tagErr := ociStore.PutManifest(t.Context(), []byte(`{"schemaVersion":2}`))
				require.NoError(t, tagErr)
				require.NoError(t, ociStore.Tag(t.Context(), d, "my-tag"))

				reg := ocimocks.NewMockRegistryClient(ctrl)
				reg.EXPECT().Push(gomock.Any(), ociStore, d, "my-tag").Return(nil)
				return reg, ociStore
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			registry, ociStore := tt.setup(ctrl)

			svc := New(&storage.NoopSkillStore{},
				WithRegistryClient(registry),
				WithOCIStore(ociStore),
			)

			err := svc.Push(t.Context(), tt.opts)
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
		})
	}
}

func TestNewWithZeroOptions(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	store := storemocks.NewMockSkillStore(ctrl)

	// New(store) without options should work
	svc := New(store)
	require.NotNil(t, svc)
}

func TestConcurrentInstallAndUninstall(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := storemocks.NewMockSkillStore(ctrl)

	// Per-skill atomic counters verify that at most one goroutine is inside
	// a critical section for a given skill at any time.
	var inFlight sync.Map // skill name -> *int32

	assertExclusive := func(name string) {
		counter, _ := inFlight.LoadOrStore(name, new(int32))
		cnt := counter.(*int32)
		cur := atomic.AddInt32(cnt, 1)
		assert.Equal(t, int32(1), cur, "concurrent access detected for %s", name)
		// Sleep briefly to widen the window for detecting overlap.
		time.Sleep(time.Millisecond)
		atomic.AddInt32(cnt, -1)
	}

	store.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, sk skills.InstalledSkill) error {
			assertExclusive(sk.Metadata.Name)
			return nil
		}).AnyTimes()
	store.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, name string, _ skills.Scope, _ string) (skills.InstalledSkill, error) {
			assertExclusive(name)
			return skills.InstalledSkill{
				Metadata: skills.SkillMetadata{Name: name},
				Scope:    skills.ScopeUser,
			}, nil
		}).AnyTimes()
	store.EXPECT().Delete(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, name string, _ skills.Scope, _ string) error {
			assertExclusive(name)
			return nil
		}).AnyTimes()

	svc := New(store)

	// Run concurrent install/uninstall pairs across multiple skill names.
	// Different skills proceed independently; the same skill name is
	// serialized by the per-skill lock. The atomic counters above detect
	// any overlap within a skill's critical section.
	skillNames := []string{"skill-a", "skill-b", "skill-c"}
	const goroutinesPerSkill = 5

	var wg sync.WaitGroup
	wg.Add(len(skillNames) * goroutinesPerSkill)

	for _, name := range skillNames {
		for range goroutinesPerSkill {
			go func() {
				defer wg.Done()
				_, _ = svc.Install(t.Context(), skills.InstallOptions{Name: name})
				_ = svc.Uninstall(t.Context(), skills.UninstallOptions{Name: name})
			}()
		}
	}

	wg.Wait()
}

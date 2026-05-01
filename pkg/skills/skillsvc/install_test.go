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
	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/groups"
	groupmocks "github.com/stacklok/toolhive/pkg/groups/mocks"
	"github.com/stacklok/toolhive/pkg/skills"
	skillsmocks "github.com/stacklok/toolhive/pkg/skills/mocks"
	"github.com/stacklok/toolhive/pkg/storage"
	storemocks "github.com/stacklok/toolhive/pkg/storage/mocks"
)

func TestInstallPlainNameNotFound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		opts     skills.InstallOptions
		wantCode int
		wantErr  string
	}{
		{
			name:     "plain name without store or lookup returns not found",
			opts:     skills.InstallOptions{Name: "my-skill"},
			wantCode: http.StatusNotFound,
			wantErr:  "not found in local store or registry",
		},
		{
			name:     "rejects project scope without root",
			opts:     skills.InstallOptions{Name: "my-skill", Scope: skills.ScopeProject},
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "rejects invalid name",
			opts:     skills.InstallOptions{Name: "A"},
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "rejects empty name",
			opts:     skills.InstallOptions{Name: ""},
			wantCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			store := storemocks.NewMockSkillStore(ctrl)

			_, err := New(store).Install(t.Context(), tt.opts)
			require.Error(t, err)
			assert.Equal(t, tt.wantCode, httperr.Code(err))
			if tt.wantErr != "" {
				assert.Contains(t, err.Error(), tt.wantErr)
			}
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

		targetDir := filepath.Join(tempDir(t), "my-skill")
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

		targetDir := filepath.Join(tempDir(t), "my-skill")
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

		targetDir := filepath.Join(tempDir(t), "my-skill")
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
		targetDir := filepath.Join(tempDir(t), "my-skill")
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
		targetDir := filepath.Join(tempDir(t), "my-skill")
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

		targetDir := filepath.Join(tempDir(t), "my-skill")
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
			Clients:   []string{"custom-client"},
			Digest:    "sha256:abc",
		})
		require.NoError(t, err)
	})

	t.Run("multiple clients fresh install", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)
		pr := skillsmocks.NewMockPathResolver(ctrl)
		inst := skillsmocks.NewMockInstaller(ctrl)

		dirA := filepath.Join(t.TempDir(), "a", "my-skill")
		dirB := filepath.Join(t.TempDir(), "b", "my-skill")
		pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(dirA, nil)
		pr.EXPECT().GetSkillPath("opencode", "my-skill", skills.ScopeUser, "").Return(dirB, nil)
		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(skills.InstalledSkill{}, storage.ErrNotFound)
		inst.EXPECT().Extract(layerData, dirA, false).Return(&skills.ExtractResult{SkillDir: dirA, Files: 1}, nil)
		inst.EXPECT().Extract(layerData, dirB, false).Return(&skills.ExtractResult{SkillDir: dirB, Files: 1}, nil)
		store.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, sk skills.InstalledSkill) error {
				assert.ElementsMatch(t, []string{"claude-code", "opencode"}, sk.Clients)
				return nil
			})

		svc := New(store, WithPathResolver(pr), WithInstaller(inst))
		_, err := svc.Install(t.Context(), skills.InstallOptions{
			Name:      "my-skill",
			LayerData: layerData,
			Digest:    "sha256:abc",
			Clients:   []string{"claude-code", "opencode"},
		})
		require.NoError(t, err)
	})

	t.Run("same digest adds second client without re-extracting first", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)
		pr := skillsmocks.NewMockPathResolver(ctrl)
		inst := skillsmocks.NewMockInstaller(ctrl)

		dirA := filepath.Join(t.TempDir(), "a")
		dirB := filepath.Join(t.TempDir(), "b")
		existing := skills.InstalledSkill{
			Metadata: skills.SkillMetadata{Name: "my-skill"},
			Digest:   "sha256:abc",
			Clients:  []string{"claude-code"},
		}
		pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(dirA, nil)
		pr.EXPECT().GetSkillPath("opencode", "my-skill", skills.ScopeUser, "").Return(dirB, nil)
		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(existing, nil)
		inst.EXPECT().Extract(layerData, dirB, false).Return(&skills.ExtractResult{SkillDir: dirB, Files: 1}, nil)
		store.EXPECT().Update(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, sk skills.InstalledSkill) error {
				assert.ElementsMatch(t, []string{"claude-code", "opencode"}, sk.Clients)
				return nil
			})

		svc := New(store, WithPathResolver(pr), WithInstaller(inst))
		_, err := svc.Install(t.Context(), skills.InstallOptions{
			Name:      "my-skill",
			LayerData: layerData,
			Digest:    "sha256:abc",
			Clients:   []string{"claude-code", "opencode"},
		})
		require.NoError(t, err)
	})

	t.Run("invalid client returns bad request", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)
		pr := skillsmocks.NewMockPathResolver(ctrl)

		pr.EXPECT().GetSkillPath("not-a-real-client", "my-skill", skills.ScopeUser, "").Return("", fmt.Errorf("%w: not-a-real-client", client.ErrUnsupportedClientType))

		svc := New(store, WithPathResolver(pr))
		_, err := svc.Install(t.Context(), skills.InstallOptions{
			Name:      "my-skill",
			LayerData: layerData,
			Digest:    "sha256:abc",
			Clients:   []string{"not-a-real-client"},
		})
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
	})

	t.Run("empty string in clients list returns bad request", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)
		pr := skillsmocks.NewMockPathResolver(ctrl)

		svc := New(store, WithPathResolver(pr))
		_, err := svc.Install(t.Context(), skills.InstallOptions{
			Name:      "my-skill",
			LayerData: layerData,
			Digest:    "sha256:abc",
			Clients:   []string{"claude-code", ""},
		})
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
	})

	t.Run("clients all sentinel expands to every skill-supporting client", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)
		pr := skillsmocks.NewMockPathResolver(ctrl)
		inst := skillsmocks.NewMockInstaller(ctrl)

		dirA := filepath.Join(t.TempDir(), "a", "my-skill")
		dirB := filepath.Join(t.TempDir(), "b", "my-skill")
		pr.EXPECT().ListSkillSupportingClients().Return([]string{"claude-code", "opencode"})
		pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(dirA, nil)
		pr.EXPECT().GetSkillPath("opencode", "my-skill", skills.ScopeUser, "").Return(dirB, nil)
		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(skills.InstalledSkill{}, storage.ErrNotFound)
		inst.EXPECT().Extract(layerData, dirA, false).Return(&skills.ExtractResult{SkillDir: dirA, Files: 1}, nil)
		inst.EXPECT().Extract(layerData, dirB, false).Return(&skills.ExtractResult{SkillDir: dirB, Files: 1}, nil)
		store.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, sk skills.InstalledSkill) error {
				assert.ElementsMatch(t, []string{"claude-code", "opencode"}, sk.Clients)
				return nil
			})

		svc := New(store, WithPathResolver(pr), WithInstaller(inst))
		_, err := svc.Install(t.Context(), skills.InstallOptions{
			Name:      "my-skill",
			LayerData: layerData,
			Digest:    "sha256:abc",
			Clients:   []string{"all"},
		})
		require.NoError(t, err)
	})

	t.Run("all sentinel mixed with named client returns bad request", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)
		pr := skillsmocks.NewMockPathResolver(ctrl)

		svc := New(store, WithPathResolver(pr))
		_, err := svc.Install(t.Context(), skills.InstallOptions{
			Name:      "my-skill",
			LayerData: layerData,
			Digest:    "sha256:abc",
			Clients:   []string{"all", "opencode"},
		})
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
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

	t.Run("multi-client fresh install rolls back first client on second extract failure", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)
		pr := skillsmocks.NewMockPathResolver(ctrl)
		inst := skillsmocks.NewMockInstaller(ctrl)

		dirA := filepath.Join(t.TempDir(), "a", "my-skill")
		dirB := filepath.Join(t.TempDir(), "b", "my-skill")
		pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(dirA, nil)
		pr.EXPECT().GetSkillPath("opencode", "my-skill", skills.ScopeUser, "").Return(dirB, nil)
		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").
			Return(skills.InstalledSkill{}, storage.ErrNotFound)
		inst.EXPECT().Extract(layerData, dirA, false).
			Return(&skills.ExtractResult{SkillDir: dirA, Files: 1}, nil)
		inst.EXPECT().Extract(layerData, dirB, false).
			Return(nil, fmt.Errorf("disk full"))
		inst.EXPECT().Remove(dirA).Return(nil)

		svc := New(store, WithPathResolver(pr), WithInstaller(inst))
		_, err := svc.Install(t.Context(), skills.InstallOptions{
			Name:      "my-skill",
			LayerData: layerData,
			Digest:    "sha256:abc",
			Clients:   []string{"claude-code", "opencode"},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "disk full")
	})

	t.Run("upgrade digest rolls back written clients on second extract failure", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)
		pr := skillsmocks.NewMockPathResolver(ctrl)
		inst := skillsmocks.NewMockInstaller(ctrl)

		dirA := filepath.Join(t.TempDir(), "a", "my-skill")
		dirB := filepath.Join(t.TempDir(), "b", "my-skill")
		existing := skills.InstalledSkill{
			Metadata: skills.SkillMetadata{Name: "my-skill"},
			Digest:   "sha256:old",
			Clients:  []string{"claude-code"},
		}
		pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(dirA, nil)
		pr.EXPECT().GetSkillPath("opencode", "my-skill", skills.ScopeUser, "").Return(dirB, nil)
		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(existing, nil)
		inst.EXPECT().Extract(layerData, dirA, true).
			Return(&skills.ExtractResult{SkillDir: dirA, Files: 1}, nil)
		inst.EXPECT().Extract(layerData, dirB, true).
			Return(nil, fmt.Errorf("write error"))
		inst.EXPECT().Remove(dirA).Return(nil)

		svc := New(store, WithPathResolver(pr), WithInstaller(inst))
		_, err := svc.Install(t.Context(), skills.InstallOptions{
			Name:      "my-skill",
			LayerData: layerData,
			Digest:    "sha256:new",
			Clients:   []string{"claude-code", "opencode"},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "write error")
	})

	t.Run("upgrade digest rolls back all clients on store.Update failure", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)
		pr := skillsmocks.NewMockPathResolver(ctrl)
		inst := skillsmocks.NewMockInstaller(ctrl)

		dirA := filepath.Join(t.TempDir(), "a", "my-skill")
		dirB := filepath.Join(t.TempDir(), "b", "my-skill")
		existing := skills.InstalledSkill{
			Metadata: skills.SkillMetadata{Name: "my-skill"},
			Digest:   "sha256:old",
			Clients:  []string{"claude-code"},
		}
		pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(dirA, nil)
		pr.EXPECT().GetSkillPath("opencode", "my-skill", skills.ScopeUser, "").Return(dirB, nil)
		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(existing, nil)
		inst.EXPECT().Extract(layerData, dirA, true).
			Return(&skills.ExtractResult{SkillDir: dirA, Files: 1}, nil)
		inst.EXPECT().Extract(layerData, dirB, true).
			Return(&skills.ExtractResult{SkillDir: dirB, Files: 1}, nil)
		store.EXPECT().Update(gomock.Any(), gomock.Any()).Return(fmt.Errorf("db failure"))
		inst.EXPECT().Remove(dirA).Return(nil)
		inst.EXPECT().Remove(dirB).Return(nil)

		svc := New(store, WithPathResolver(pr), WithInstaller(inst))
		_, err := svc.Install(t.Context(), skills.InstallOptions{
			Name:      "my-skill",
			LayerData: layerData,
			Digest:    "sha256:new",
			Clients:   []string{"claude-code", "opencode"},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "db failure")
	})

	t.Run("same digest new client rolls back on extract failure", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)
		pr := skillsmocks.NewMockPathResolver(ctrl)
		inst := skillsmocks.NewMockInstaller(ctrl)

		dirA := filepath.Join(t.TempDir(), "a", "my-skill")
		dirB := filepath.Join(t.TempDir(), "b", "my-skill")
		existing := skills.InstalledSkill{
			Metadata: skills.SkillMetadata{Name: "my-skill"},
			Digest:   "sha256:abc",
			Clients:  []string{"claude-code"},
		}
		pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(dirA, nil)
		pr.EXPECT().GetSkillPath("opencode", "my-skill", skills.ScopeUser, "").Return(dirB, nil)
		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(existing, nil)
		inst.EXPECT().Extract(layerData, dirB, false).Return(nil, fmt.Errorf("extract boom"))

		svc := New(store, WithPathResolver(pr), WithInstaller(inst))
		_, err := svc.Install(t.Context(), skills.InstallOptions{
			Name:      "my-skill",
			LayerData: layerData,
			Digest:    "sha256:abc",
			Clients:   []string{"claude-code", "opencode"},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "extract boom")
	})

	t.Run("same digest new client rolls back on store.Update failure", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)
		pr := skillsmocks.NewMockPathResolver(ctrl)
		inst := skillsmocks.NewMockInstaller(ctrl)

		dirA := filepath.Join(t.TempDir(), "a", "my-skill")
		dirB := filepath.Join(t.TempDir(), "b", "my-skill")
		existing := skills.InstalledSkill{
			Metadata: skills.SkillMetadata{Name: "my-skill"},
			Digest:   "sha256:abc",
			Clients:  []string{"claude-code"},
		}
		pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(dirA, nil)
		pr.EXPECT().GetSkillPath("opencode", "my-skill", skills.ScopeUser, "").Return(dirB, nil)
		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(existing, nil)
		inst.EXPECT().Extract(layerData, dirB, false).
			Return(&skills.ExtractResult{SkillDir: dirB, Files: 1}, nil)
		store.EXPECT().Update(gomock.Any(), gomock.Any()).Return(fmt.Errorf("db update boom"))
		inst.EXPECT().Remove(dirB).Return(nil)

		svc := New(store, WithPathResolver(pr), WithInstaller(inst))
		_, err := svc.Install(t.Context(), skills.InstallOptions{
			Name:      "my-skill",
			LayerData: layerData,
			Digest:    "sha256:abc",
			Clients:   []string{"claude-code", "opencode"},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "db update boom")
	})

	t.Run("same digest new client unmanaged dir conflict", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)
		pr := skillsmocks.NewMockPathResolver(ctrl)
		inst := skillsmocks.NewMockInstaller(ctrl)

		dirA := filepath.Join(t.TempDir(), "a", "my-skill")
		dirB := filepath.Join(t.TempDir(), "b", "my-skill")
		require.NoError(t, os.MkdirAll(dirB, 0o750))

		existing := skills.InstalledSkill{
			Metadata: skills.SkillMetadata{Name: "my-skill"},
			Digest:   "sha256:abc",
			Clients:  []string{"claude-code"},
		}
		pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(dirA, nil)
		pr.EXPECT().GetSkillPath("opencode", "my-skill", skills.ScopeUser, "").Return(dirB, nil)
		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(existing, nil)

		svc := New(store, WithPathResolver(pr), WithInstaller(inst))
		_, err := svc.Install(t.Context(), skills.InstallOptions{
			Name:      "my-skill",
			LayerData: layerData,
			Digest:    "sha256:abc",
			Clients:   []string{"claude-code", "opencode"},
		})
		require.Error(t, err)
		assert.Equal(t, http.StatusConflict, httperr.Code(err))
		assert.Contains(t, err.Error(), "not managed by ToolHive")
	})

	t.Run("legacy row with explicit client is not a no-op", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)
		pr := skillsmocks.NewMockPathResolver(ctrl)
		inst := skillsmocks.NewMockInstaller(ctrl)

		dirB := filepath.Join(t.TempDir(), "b", "my-skill")
		existing := skills.InstalledSkill{
			Metadata: skills.SkillMetadata{Name: "my-skill"},
			Digest:   "sha256:abc",
			Clients:  []string{},
		}
		pr.EXPECT().GetSkillPath("opencode", "my-skill", skills.ScopeUser, "").Return(dirB, nil)
		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(existing, nil)
		inst.EXPECT().Extract(layerData, dirB, false).
			Return(&skills.ExtractResult{SkillDir: dirB, Files: 1}, nil)
		store.EXPECT().Update(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, sk skills.InstalledSkill) error {
				assert.Contains(t, sk.Clients, "opencode")
				return nil
			})

		svc := New(store, WithPathResolver(pr), WithInstaller(inst))
		result, err := svc.Install(t.Context(), skills.InstallOptions{
			Name:      "my-skill",
			LayerData: layerData,
			Digest:    "sha256:abc",
			Clients:   []string{"opencode"},
		})
		require.NoError(t, err)
		assert.Equal(t, "my-skill", result.Skill.Metadata.Name)
	})

	t.Run("upgrade extracts to all existing clients not just requested", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockSkillStore(ctrl)
		pr := skillsmocks.NewMockPathResolver(ctrl)
		inst := skillsmocks.NewMockInstaller(ctrl)

		dirA := filepath.Join(t.TempDir(), "a", "my-skill")
		dirB := filepath.Join(t.TempDir(), "b", "my-skill")
		existing := skills.InstalledSkill{
			Metadata: skills.SkillMetadata{Name: "my-skill"},
			Digest:   "sha256:old",
			Clients:  []string{"claude-code"},
		}
		pr.EXPECT().GetSkillPath("opencode", "my-skill", skills.ScopeUser, "").Return(dirB, nil)
		pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(dirA, nil)
		store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(existing, nil)
		inst.EXPECT().Extract(layerData, dirB, true).
			Return(&skills.ExtractResult{SkillDir: dirB, Files: 1}, nil)
		inst.EXPECT().Extract(layerData, dirA, true).
			Return(&skills.ExtractResult{SkillDir: dirA, Files: 1}, nil)
		store.EXPECT().Update(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, sk skills.InstalledSkill) error {
				assert.ElementsMatch(t, []string{"opencode", "claude-code"}, sk.Clients)
				assert.Equal(t, "sha256:new", sk.Digest)
				return nil
			})

		svc := New(store, WithPathResolver(pr), WithInstaller(inst))
		result, err := svc.Install(t.Context(), skills.InstallOptions{
			Name:      "my-skill",
			LayerData: layerData,
			Digest:    "sha256:new",
			Clients:   []string{"opencode"},
		})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"opencode", "claude-code"}, result.Skill.Clients)
	})
}
func TestInstallAddsSkillToGroup(t *testing.T) {
	t.Parallel()

	layerData := makeLayerData(t)

	// These tests provide LayerData so Install goes through installWithExtraction,
	// which exercises group registration without needing OCI resolution.
	tests := []struct {
		name           string
		opts           skills.InstallOptions
		setupStoreMock func(*storemocks.MockSkillStore)
		setupPR        func(*skillsmocks.MockPathResolver)
		setupGroupMock func(*groupmocks.MockManager)
		wantErr        string
	}{
		{
			name: "install with group registers skill",
			opts: skills.InstallOptions{Name: "my-skill", Group: "mygroup", LayerData: layerData, Digest: "sha256:abc"},
			setupStoreMock: func(s *storemocks.MockSkillStore) {
				s.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(skills.InstalledSkill{}, storage.ErrNotFound)
				s.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
			},
			setupPR: func(pr *skillsmocks.MockPathResolver) {
				pr.EXPECT().ListSkillSupportingClients().Return([]string{"claude-code"})
				pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(filepath.Join(tempDir(t), "my-skill"), nil)
			},
			setupGroupMock: func(gm *groupmocks.MockManager) {
				gm.EXPECT().Get(gomock.Any(), "mygroup").
					Return(&groups.Group{Name: "mygroup", Skills: []string{}}, nil)
				gm.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil)
			},
		},
		{
			name: "install without group defaults to default group",
			opts: skills.InstallOptions{Name: "my-skill", LayerData: layerData, Digest: "sha256:abc"},
			setupStoreMock: func(s *storemocks.MockSkillStore) {
				s.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(skills.InstalledSkill{}, storage.ErrNotFound)
				s.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
			},
			setupPR: func(pr *skillsmocks.MockPathResolver) {
				pr.EXPECT().ListSkillSupportingClients().Return([]string{"claude-code"})
				pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(filepath.Join(tempDir(t), "my-skill"), nil)
			},
			setupGroupMock: func(gm *groupmocks.MockManager) {
				gm.EXPECT().Get(gomock.Any(), groups.DefaultGroup).
					Return(&groups.Group{Name: groups.DefaultGroup, Skills: []string{}}, nil)
				gm.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil)
			},
		},
		{
			name: "group registration error rolls back DB record",
			opts: skills.InstallOptions{Name: "my-skill", Group: "badgroup", LayerData: layerData, Digest: "sha256:abc"},
			setupStoreMock: func(s *storemocks.MockSkillStore) {
				s.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(skills.InstalledSkill{}, storage.ErrNotFound)
				s.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
				// Rollback: installAndRegister removes the DB record on group failure.
				s.EXPECT().Delete(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(nil)
			},
			setupPR: func(pr *skillsmocks.MockPathResolver) {
				pr.EXPECT().ListSkillSupportingClients().Return([]string{"claude-code"})
				pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(filepath.Join(tempDir(t), "my-skill"), nil)
			},
			setupGroupMock: func(gm *groupmocks.MockManager) {
				gm.EXPECT().Get(gomock.Any(), "badgroup").
					Return(nil, fmt.Errorf("group not found"))
			},
			wantErr: "registering skill in group",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			store := storemocks.NewMockSkillStore(ctrl)
			gm := groupmocks.NewMockManager(ctrl)
			pr := skillsmocks.NewMockPathResolver(ctrl)

			tt.setupStoreMock(store)
			tt.setupGroupMock(gm)
			tt.setupPR(pr)

			svc := New(store, WithGroupManager(gm), WithPathResolver(pr))

			_, err := svc.Install(t.Context(), tt.opts)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

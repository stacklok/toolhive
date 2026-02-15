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
	"github.com/stacklok/toolhive/pkg/storage"
	storemocks "github.com/stacklok/toolhive/pkg/storage/mocks"
)

func TestList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		opts       skills.ListOptions
		setupMock  func(*storemocks.MockSkillStore)
		wantErr    string
		wantCount  int
		wantScoped bool
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

func TestInstall(t *testing.T) {
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

func TestUninstall(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		opts      skills.UninstallOptions
		setupMock func(*storemocks.MockSkillStore)
		wantCode  int
	}{
		{
			name: "success defaults scope to user",
			opts: skills.UninstallOptions{Name: "my-skill"},
			setupMock: func(s *storemocks.MockSkillStore) {
				s.EXPECT().Delete(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(nil)
			},
		},
		{
			name: "respects explicit scope",
			opts: skills.UninstallOptions{Name: "my-skill", Scope: skills.ScopeProject},
			setupMock: func(s *storemocks.MockSkillStore) {
				s.EXPECT().Delete(gomock.Any(), "my-skill", skills.ScopeProject, "").Return(nil)
			},
		},
		{
			name: "returns 404 when not found",
			opts: skills.UninstallOptions{Name: "my-skill"},
			setupMock: func(s *storemocks.MockSkillStore) {
				s.EXPECT().Delete(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(storage.ErrNotFound)
			},
			wantCode: http.StatusNotFound,
		},
		{
			name:      "rejects invalid name",
			opts:      skills.UninstallOptions{Name: "X"},
			setupMock: func(_ *storemocks.MockSkillStore) {},
			wantCode:  http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			store := storemocks.NewMockSkillStore(ctrl)
			tt.setupMock(store)

			err := New(store).Uninstall(t.Context(), tt.opts)
			if tt.wantCode != 0 {
				require.Error(t, err)
				assert.Equal(t, tt.wantCode, httperr.Code(err))
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestInfo(t *testing.T) {
	t.Parallel()

	installed := skills.InstalledSkill{
		Metadata: skills.SkillMetadata{Name: "my-skill", Version: "1.0.0"},
		Scope:    skills.ScopeUser,
		Status:   skills.InstallStatusInstalled,
	}

	tests := []struct {
		name          string
		opts          skills.InfoOptions
		setupMock     func(*storemocks.MockSkillStore)
		wantCode      int
		wantInstalled bool
		wantErr       string
	}{
		{
			name: "found skill",
			opts: skills.InfoOptions{Name: "my-skill"},
			setupMock: func(s *storemocks.MockSkillStore) {
				s.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(installed, nil)
			},
			wantInstalled: true,
		},
		{
			name: "not found returns installed=false",
			opts: skills.InfoOptions{Name: "unknown"},
			setupMock: func(s *storemocks.MockSkillStore) {
				s.EXPECT().Get(gomock.Any(), "unknown", skills.ScopeUser, "").
					Return(skills.InstalledSkill{}, storage.ErrNotFound)
			},
			wantInstalled: false,
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
			assert.Equal(t, tt.wantInstalled, info.Installed)
			if tt.wantInstalled {
				require.NotNil(t, info.InstalledSkill)
				assert.Equal(t, "my-skill", info.InstalledSkill.Metadata.Name)
			} else {
				assert.Nil(t, info.InstalledSkill)
			}
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
}

func TestBuildAndPush_NotImplemented(t *testing.T) {
	t.Parallel()
	svc := New(&storage.NoopSkillStore{})

	t.Run("build", func(t *testing.T) {
		t.Parallel()
		_, err := svc.Build(t.Context(), skills.BuildOptions{})
		require.Error(t, err)
		assert.Equal(t, http.StatusNotImplemented, httperr.Code(err))
	})

	t.Run("push", func(t *testing.T) {
		t.Parallel()
		err := svc.Push(t.Context(), skills.PushOptions{})
		require.Error(t, err)
		assert.Equal(t, http.StatusNotImplemented, httperr.Code(err))
	})
}

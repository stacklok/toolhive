// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/storage"
	storemocks "github.com/stacklok/toolhive/pkg/storage/mocks"
)

func TestInfo(t *testing.T) {
	t.Parallel()

	projectRoot := makeProjectRoot(t)

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
			opts: skills.InfoOptions{Name: "my-skill", Scope: skills.ScopeProject, ProjectRoot: projectRoot},
			setupMock: func(s *storemocks.MockSkillStore) {
				s.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeProject, projectRoot).Return(installed, nil)
			},
		},
		{
			name:      "project scope missing project root",
			opts:      skills.InfoOptions{Name: "my-skill", Scope: skills.ScopeProject},
			setupMock: func(_ *storemocks.MockSkillStore) {},
			wantCode:  http.StatusBadRequest,
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

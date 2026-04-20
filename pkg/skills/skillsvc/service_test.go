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
	"github.com/stacklok/toolhive/pkg/groups"
	groupmocks "github.com/stacklok/toolhive/pkg/groups/mocks"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/storage"
	storemocks "github.com/stacklok/toolhive/pkg/storage/mocks"
)

func TestList(t *testing.T) {
	t.Parallel()

	projectRoot := makeProjectRoot(t)

	tests := []struct {
		name      string
		opts      skills.ListOptions
		setupMock func(*storemocks.MockSkillStore)
		wantCode  int
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
			name: "delegates to store with project root",
			opts: skills.ListOptions{Scope: skills.ScopeProject, ProjectRoot: projectRoot},
			setupMock: func(s *storemocks.MockSkillStore) {
				s.EXPECT().List(gomock.Any(), storage.ListFilter{
					Scope:       skills.ScopeProject,
					ProjectRoot: projectRoot,
				}).Return([]skills.InstalledSkill{}, nil)
			},
			wantCount: 0,
		},
		{
			name:      "project scope requires project root",
			opts:      skills.ListOptions{Scope: skills.ScopeProject},
			setupMock: func(_ *storemocks.MockSkillStore) {},
			wantCode:  http.StatusBadRequest,
			wantErr:   "project_root is required",
		},
		{
			name: "delegates to store with client app",
			opts: skills.ListOptions{ClientApp: "claude-code"},
			setupMock: func(s *storemocks.MockSkillStore) {
				s.EXPECT().List(gomock.Any(), storage.ListFilter{ClientApp: "claude-code"}).
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
			if tt.wantCode != 0 {
				require.Error(t, err)
				assert.Equal(t, tt.wantCode, httperr.Code(err))
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
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
func TestNewWithZeroOptions(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	store := storemocks.NewMockSkillStore(ctrl)

	// New(store) without options should work
	svc := New(store)
	require.NotNil(t, svc)
}
func TestListFiltersByGroup(t *testing.T) {
	t.Parallel()

	allSkills := []skills.InstalledSkill{
		{Metadata: skills.SkillMetadata{Name: "skill-a"}},
		{Metadata: skills.SkillMetadata{Name: "skill-b"}},
		{Metadata: skills.SkillMetadata{Name: "skill-c"}},
	}

	tests := []struct {
		name      string
		opts      skills.ListOptions
		setupMock func(*storemocks.MockSkillStore, *groupmocks.MockManager)
		wantNames []string
		wantCode  int
		wantErr   string
	}{
		{
			name: "no group filter returns all skills",
			opts: skills.ListOptions{},
			setupMock: func(s *storemocks.MockSkillStore, _ *groupmocks.MockManager) {
				s.EXPECT().List(gomock.Any(), storage.ListFilter{}).Return(allSkills, nil)
			},
			wantNames: []string{"skill-a", "skill-b", "skill-c"},
		},
		{
			name: "group filter returns only matching skills",
			opts: skills.ListOptions{Group: "mygroup"},
			setupMock: func(s *storemocks.MockSkillStore, gm *groupmocks.MockManager) {
				s.EXPECT().List(gomock.Any(), storage.ListFilter{}).Return(allSkills, nil)
				gm.EXPECT().Get(gomock.Any(), "mygroup").Return(&groups.Group{
					Name:   "mygroup",
					Skills: []string{"skill-a", "skill-c"},
				}, nil)
			},
			wantNames: []string{"skill-a", "skill-c"},
		},
		{
			name: "group filter with empty group skills returns no skills",
			opts: skills.ListOptions{Group: "emptygroup"},
			setupMock: func(s *storemocks.MockSkillStore, gm *groupmocks.MockManager) {
				s.EXPECT().List(gomock.Any(), storage.ListFilter{}).Return(allSkills, nil)
				gm.EXPECT().Get(gomock.Any(), "emptygroup").Return(&groups.Group{
					Name:   "emptygroup",
					Skills: []string{},
				}, nil)
			},
			wantNames: []string{},
		},
		{
			name: "group filter without group manager returns error",
			opts: skills.ListOptions{Group: "mygroup"},
			setupMock: func(s *storemocks.MockSkillStore, _ *groupmocks.MockManager) {
				s.EXPECT().List(gomock.Any(), storage.ListFilter{}).Return(allSkills, nil)
			},
			wantCode: http.StatusInternalServerError,
			wantErr:  "group manager is not configured",
		},
		{
			name: "group manager Get error propagates",
			opts: skills.ListOptions{Group: "badgroup"},
			setupMock: func(s *storemocks.MockSkillStore, gm *groupmocks.MockManager) {
				s.EXPECT().List(gomock.Any(), storage.ListFilter{}).Return(allSkills, nil)
				gm.EXPECT().Get(gomock.Any(), "badgroup").Return(nil, fmt.Errorf("group not found"))
			},
			wantErr: "getting group",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			store := storemocks.NewMockSkillStore(ctrl)
			gm := groupmocks.NewMockManager(ctrl)
			tt.setupMock(store, gm)

			opts := []Option{}
			// Only wire the group manager for tests that don't test the nil-manager case.
			if tt.wantCode != http.StatusInternalServerError {
				opts = append(opts, WithGroupManager(gm))
			}
			svc := New(store, opts...)

			result, err := svc.List(t.Context(), tt.opts)
			if tt.wantCode != 0 {
				require.Error(t, err)
				assert.Equal(t, tt.wantCode, httperr.Code(err))
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			var names []string
			for _, sk := range result {
				names = append(names, sk.Metadata.Name)
			}
			if tt.wantNames == nil {
				tt.wantNames = []string{}
			}
			if names == nil {
				names = []string{}
			}
			assert.ElementsMatch(t, tt.wantNames, names)
		})
	}
}

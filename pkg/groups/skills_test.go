// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package groups_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	. "github.com/stacklok/toolhive/pkg/groups"
	groupmocks "github.com/stacklok/toolhive/pkg/groups/mocks"
)

func TestAddSkillToGroups(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		groupName string
		skillName string
		setupMock func(*groupmocks.MockManager)
		wantErr   string
	}{
		{
			name:      "adds skill to one group",
			groupName: "mygroup",
			skillName: "my-skill",
			setupMock: func(m *groupmocks.MockManager) {
				m.EXPECT().Get(gomock.Any(), "mygroup").
					Return(&Group{Name: "mygroup", Skills: []string{}}, nil)
				m.EXPECT().Update(gomock.Any(), &Group{Name: "mygroup", Skills: []string{"my-skill"}}).
					Return(nil)
			},
		},
		{
			name:      "skips duplicate skill",
			groupName: "mygroup",
			skillName: "my-skill",
			setupMock: func(m *groupmocks.MockManager) {
				// Already has the skill — no Update call expected.
				m.EXPECT().Get(gomock.Any(), "mygroup").
					Return(&Group{Name: "mygroup", Skills: []string{"my-skill"}}, nil)
			},
		},
		{
			name:      "no-op when group names is empty",
			groupName: "",
			skillName: "my-skill",
			setupMock: func(_ *groupmocks.MockManager) {},
		},
		{
			name:      "returns error when group not found",
			groupName: "nonexistent",
			skillName: "my-skill",
			setupMock: func(m *groupmocks.MockManager) {
				m.EXPECT().Get(gomock.Any(), "nonexistent").
					Return(nil, errors.New("group not found"))
			},
			wantErr: "getting group",
		},
		{
			name:      "returns error when Update fails",
			groupName: "mygroup",
			skillName: "my-skill",
			setupMock: func(m *groupmocks.MockManager) {
				m.EXPECT().Get(gomock.Any(), "mygroup").
					Return(&Group{Name: "mygroup", Skills: []string{}}, nil)
				m.EXPECT().Update(gomock.Any(), gomock.Any()).
					Return(errors.New("disk full"))
			},
			wantErr: "updating group",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mgr := groupmocks.NewMockManager(ctrl)
			tt.setupMock(mgr)

			err := AddSkillToGroup(context.Background(), mgr, tt.groupName, tt.skillName)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRemoveSkillFromAllGroups(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		skillName string
		setupMock func(*groupmocks.MockManager)
		wantErr   string
	}{
		{
			name:      "removes skill from matching group",
			skillName: "my-skill",
			setupMock: func(m *groupmocks.MockManager) {
				m.EXPECT().List(gomock.Any()).Return([]*Group{
					{Name: "mygroup", Skills: []string{"my-skill", "other"}},
				}, nil)
				m.EXPECT().Update(gomock.Any(), &Group{Name: "mygroup", Skills: []string{"other"}}).
					Return(nil)
			},
		},
		{
			name:      "no-op when skill is not in any group",
			skillName: "absent-skill",
			setupMock: func(m *groupmocks.MockManager) {
				m.EXPECT().List(gomock.Any()).Return([]*Group{
					{Name: "mygroup", Skills: []string{"some-other-skill"}},
				}, nil)
				// No Update call expected.
			},
		},
		{
			name:      "no-op when no groups exist",
			skillName: "my-skill",
			setupMock: func(m *groupmocks.MockManager) {
				m.EXPECT().List(gomock.Any()).Return([]*Group{}, nil)
			},
		},
		{
			name:      "removes skill from multiple groups",
			skillName: "shared",
			setupMock: func(m *groupmocks.MockManager) {
				m.EXPECT().List(gomock.Any()).Return([]*Group{
					{Name: "group-a", Skills: []string{"shared"}},
					{Name: "group-b", Skills: []string{"shared", "other"}},
				}, nil)
				m.EXPECT().Update(gomock.Any(), &Group{Name: "group-a", Skills: []string{}}).
					Return(nil)
				m.EXPECT().Update(gomock.Any(), &Group{Name: "group-b", Skills: []string{"other"}}).
					Return(nil)
			},
		},
		{
			name:      "returns error when List fails",
			skillName: "my-skill",
			setupMock: func(m *groupmocks.MockManager) {
				m.EXPECT().List(gomock.Any()).Return(nil, errors.New("store error"))
			},
			wantErr: "listing groups",
		},
		{
			name:      "returns error when Update fails",
			skillName: "my-skill",
			setupMock: func(m *groupmocks.MockManager) {
				m.EXPECT().List(gomock.Any()).Return([]*Group{
					{Name: "mygroup", Skills: []string{"my-skill"}},
				}, nil)
				m.EXPECT().Update(gomock.Any(), gomock.Any()).Return(errors.New("write error"))
			},
			wantErr: "updating group",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mgr := groupmocks.NewMockManager(ctrl)
			tt.setupMock(mgr)

			err := RemoveSkillFromAllGroups(context.Background(), mgr, tt.skillName)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

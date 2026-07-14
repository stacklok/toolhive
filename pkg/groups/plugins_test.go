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

func TestAddPluginToGroups(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		groupName  string
		pluginName string
		setupMock  func(*groupmocks.MockManager)
		wantErr    string
	}{
		{
			name:       "adds plugin to one group",
			groupName:  "mygroup",
			pluginName: "my-plugin",
			setupMock: func(m *groupmocks.MockManager) {
				m.EXPECT().Get(gomock.Any(), "mygroup").
					Return(&Group{Name: "mygroup", Plugins: []string{}}, nil)
				m.EXPECT().Update(gomock.Any(), &Group{Name: "mygroup", Plugins: []string{"my-plugin"}}).
					Return(nil)
			},
		},
		{
			name:       "skips duplicate plugin",
			groupName:  "mygroup",
			pluginName: "my-plugin",
			setupMock: func(m *groupmocks.MockManager) {
				// Already has the plugin — no Update call expected.
				m.EXPECT().Get(gomock.Any(), "mygroup").
					Return(&Group{Name: "mygroup", Plugins: []string{"my-plugin"}}, nil)
			},
		},
		{
			name:       "no-op when group names is empty",
			groupName:  "",
			pluginName: "my-plugin",
			setupMock:  func(_ *groupmocks.MockManager) {},
		},
		{
			name:       "returns error when group not found",
			groupName:  "nonexistent",
			pluginName: "my-plugin",
			setupMock: func(m *groupmocks.MockManager) {
				m.EXPECT().Get(gomock.Any(), "nonexistent").
					Return(nil, errors.New("group not found"))
			},
			wantErr: "getting group",
		},
		{
			name:       "returns error when Update fails",
			groupName:  "mygroup",
			pluginName: "my-plugin",
			setupMock: func(m *groupmocks.MockManager) {
				m.EXPECT().Get(gomock.Any(), "mygroup").
					Return(&Group{Name: "mygroup", Plugins: []string{}}, nil)
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

			err := AddPluginToGroup(context.Background(), mgr, tt.groupName, tt.pluginName)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRemovePluginFromAllGroups(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		pluginName string
		setupMock  func(*groupmocks.MockManager)
		wantErr    string
	}{
		{
			name:       "removes plugin from matching group",
			pluginName: "my-plugin",
			setupMock: func(m *groupmocks.MockManager) {
				m.EXPECT().List(gomock.Any()).Return([]*Group{
					{Name: "mygroup", Plugins: []string{"my-plugin", "other"}},
				}, nil)
				m.EXPECT().Update(gomock.Any(), &Group{Name: "mygroup", Plugins: []string{"other"}}).
					Return(nil)
			},
		},
		{
			name:       "no-op when plugin is not in any group",
			pluginName: "absent-plugin",
			setupMock: func(m *groupmocks.MockManager) {
				m.EXPECT().List(gomock.Any()).Return([]*Group{
					{Name: "mygroup", Plugins: []string{"some-other-plugin"}},
				}, nil)
				// No Update call expected.
			},
		},
		{
			name:       "no-op when no groups exist",
			pluginName: "my-plugin",
			setupMock: func(m *groupmocks.MockManager) {
				m.EXPECT().List(gomock.Any()).Return([]*Group{}, nil)
			},
		},
		{
			name:       "removes plugin from multiple groups",
			pluginName: "shared",
			setupMock: func(m *groupmocks.MockManager) {
				m.EXPECT().List(gomock.Any()).Return([]*Group{
					{Name: "group-a", Plugins: []string{"shared"}},
					{Name: "group-b", Plugins: []string{"shared", "other"}},
				}, nil)
				m.EXPECT().Update(gomock.Any(), &Group{Name: "group-a", Plugins: []string{}}).
					Return(nil)
				m.EXPECT().Update(gomock.Any(), &Group{Name: "group-b", Plugins: []string{"other"}}).
					Return(nil)
			},
		},
		{
			name:       "returns error when List fails",
			pluginName: "my-plugin",
			setupMock: func(m *groupmocks.MockManager) {
				m.EXPECT().List(gomock.Any()).Return(nil, errors.New("store error"))
			},
			wantErr: "listing groups",
		},
		{
			name:       "returns error when Update fails",
			pluginName: "my-plugin",
			setupMock: func(m *groupmocks.MockManager) {
				m.EXPECT().List(gomock.Any()).Return([]*Group{
					{Name: "mygroup", Plugins: []string{"my-plugin"}},
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

			err := RemovePluginFromAllGroups(context.Background(), mgr, tt.pluginName)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

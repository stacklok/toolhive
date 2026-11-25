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
		wantAdded  bool
		wantErr    string
	}{
		{
			name:       "adds plugin to one group",
			groupName:  "mygroup",
			pluginName: "my-plugin",
			wantAdded:  true,
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

			added, err := AddPluginToGroup(context.Background(), mgr, tt.groupName, tt.pluginName)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.False(t, added)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantAdded, added)
			}
		})
	}
}

func TestRemovePluginFromGroup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		groupName  string
		pluginName string
		setupMock  func(*groupmocks.MockManager)
		wantErr    string
	}{
		{
			name:       "removes plugin from group",
			groupName:  "mygroup",
			pluginName: "my-plugin",
			setupMock: func(m *groupmocks.MockManager) {
				m.EXPECT().Get(gomock.Any(), "mygroup").
					Return(&Group{Name: "mygroup", Plugins: []string{"my-plugin", "other"}}, nil)
				m.EXPECT().Update(gomock.Any(), &Group{Name: "mygroup", Plugins: []string{"other"}}).
					Return(nil)
			},
		},
		{
			name:       "no-op when plugin is not a member",
			groupName:  "mygroup",
			pluginName: "absent",
			setupMock: func(m *groupmocks.MockManager) {
				m.EXPECT().Get(gomock.Any(), "mygroup").
					Return(&Group{Name: "mygroup", Plugins: []string{"other"}}, nil)
			},
		},
		{
			name:       "no-op when group name is empty",
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mgr := groupmocks.NewMockManager(ctrl)
			tt.setupMock(mgr)

			err := RemovePluginFromGroup(context.Background(), mgr, tt.groupName, tt.pluginName)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

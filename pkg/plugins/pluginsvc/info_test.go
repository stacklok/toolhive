// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/groups"
	groupmocks "github.com/stacklok/toolhive/pkg/groups/mocks"
	"github.com/stacklok/toolhive/pkg/plugins"
	plugmocks "github.com/stacklok/toolhive/pkg/plugins/mocks"
	"github.com/stacklok/toolhive/pkg/storage"
	storemocks "github.com/stacklok/toolhive/pkg/storage/mocks"
)

func TestInfo(t *testing.T) {
	t.Parallel()

	t.Run("returns metadata and unmaterialized components", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		adapter := plugmocks.NewMockMaterializationAdapter(ctrl)

		stored := plugins.InstalledPlugin{
			Metadata:   plugins.PluginMetadata{Name: "my-plugin"},
			Clients:    []string{"claude-code"},
			Components: plugins.ComponentInventory{"commands": 2, "agents": 1, "skills": 3},
		}
		store.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(stored, nil)
		// claude-code supports commands+agents but not skills → skills dropped.
		adapter.EXPECT().SupportedComponents().Return([]plugins.ComponentType{
			plugins.ComponentCommands, plugins.ComponentAgents,
		})

		svc := newTestService(WithStore(store),
			WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": adapter}))
		info, err := svc.Info(t.Context(), plugins.InfoOptions{Name: "my-plugin"})
		require.NoError(t, err)
		assert.Equal(t, "my-plugin", info.Metadata.Name)
		require.NotNil(t, info.InstalledPlugin)
		require.Contains(t, info.UnmaterializedComponents, "claude-code")
		assert.Equal(t, []plugins.ComponentType{plugins.ComponentSkills}, info.UnmaterializedComponents["claude-code"])
	})

	t.Run("no unmaterialized when adapter supports all", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		adapter := plugmocks.NewMockMaterializationAdapter(ctrl)

		stored := plugins.InstalledPlugin{
			Metadata:   plugins.PluginMetadata{Name: "my-plugin"},
			Clients:    []string{"claude-code"},
			Components: plugins.ComponentInventory{"commands": 1},
		}
		store.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(stored, nil)
		adapter.EXPECT().SupportedComponents().Return([]plugins.ComponentType{plugins.ComponentCommands})

		svc := newTestService(WithStore(store),
			WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": adapter}))
		info, err := svc.Info(t.Context(), plugins.InfoOptions{Name: "my-plugin"})
		require.NoError(t, err)
		assert.Nil(t, info.UnmaterializedComponents)
	})

	t.Run("not found returns error", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		store.EXPECT().Get(gomock.Any(), "missing", plugins.ScopeUser, "").Return(plugins.InstalledPlugin{}, storage.ErrNotFound)

		svc := newTestService(WithStore(store),
			WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": plugmocks.NewMockMaterializationAdapter(ctrl)}))
		_, err := svc.Info(t.Context(), plugins.InfoOptions{Name: "missing"})
		require.Error(t, err)
		assert.ErrorIs(t, err, storage.ErrNotFound)
	})

	t.Run("invalid name returns bad request", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		svc := newTestService(WithStore(store))
		_, err := svc.Info(t.Context(), plugins.InfoOptions{Name: "A"})
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
	})

	t.Run("project-scope degradation surfaced for degrading client", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		// codex degrades on project scope; claude-code does not.
		codex := plugmocks.NewMockMaterializationAdapter(ctrl)
		claude := plugmocks.NewMockMaterializationAdapter(ctrl)

		projectRoot := makeProjectRoot(t)
		stored := plugins.InstalledPlugin{
			Metadata:   plugins.PluginMetadata{Name: "my-plugin"},
			Scope:      plugins.ScopeProject,
			Clients:    []string{"codex", "claude-code"},
			Components: plugins.ComponentInventory{"skills": 1},
		}
		store.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeProject, projectRoot).Return(stored, nil)
		// SupportedComponents is called for the UnmaterializedComponents diff.
		codex.EXPECT().SupportedComponents().Return([]plugins.ComponentType{plugins.ComponentSkills})
		claude.EXPECT().SupportedComponents().Return([]plugins.ComponentType{plugins.ComponentSkills})
		codex.EXPECT().ScopeSupport().Return(plugins.ScopeSupport{DegradesOnProjectScope: true})
		claude.EXPECT().ScopeSupport().Return(plugins.ScopeSupport{})

		svc := newTestService(WithStore(store),
			WithMaterializers(map[string]plugins.MaterializationAdapter{
				"codex":       codex,
				"claude-code": claude,
			}))
		info, err := svc.Info(t.Context(), plugins.InfoOptions{Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot})
		require.NoError(t, err)
		// Only codex degrades.
		assert.Equal(t, []string{"codex"}, info.ProjectScopeDegradedClients)
	})

	t.Run("user-scope install has no degraded clients", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		codex := plugmocks.NewMockMaterializationAdapter(ctrl)

		stored := plugins.InstalledPlugin{
			Metadata:   plugins.PluginMetadata{Name: "my-plugin"},
			Scope:      plugins.ScopeUser,
			Clients:    []string{"codex"},
			Components: plugins.ComponentInventory{"skills": 1},
		}
		store.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(stored, nil)
		codex.EXPECT().SupportedComponents().Return([]plugins.ComponentType{plugins.ComponentSkills})

		svc := newTestService(WithStore(store),
			WithMaterializers(map[string]plugins.MaterializationAdapter{"codex": codex}))
		info, err := svc.Info(t.Context(), plugins.InfoOptions{Name: "my-plugin"})
		require.NoError(t, err)
		assert.Empty(t, info.ProjectScopeDegradedClients)
	})
}

func TestList(t *testing.T) {
	t.Parallel()

	t.Run("group filter uses group.Plugins", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		gm := groupmocks.NewMockManager(ctrl)
		gm.EXPECT().Get(gomock.Any(), "mygroup").Return(&groups.Group{
			Name:    "mygroup",
			Plugins: []string{"in-group"},
		}, nil)

		store.EXPECT().List(gomock.Any(), gomock.Any()).Return([]plugins.InstalledPlugin{
			{Metadata: plugins.PluginMetadata{Name: "in-group"}},
			{Metadata: plugins.PluginMetadata{Name: "out-group"}},
		}, nil)

		svc := newTestService(WithStore(store), WithGroupManager(gm))
		listed, err := svc.List(t.Context(), plugins.ListOptions{Group: "mygroup"})
		require.NoError(t, err)
		require.Len(t, listed, 1)
		assert.Equal(t, "in-group", listed[0].Metadata.Name)
	})

	t.Run("no group filter returns all", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		store.EXPECT().List(gomock.Any(), gomock.Any()).Return([]plugins.InstalledPlugin{
			{Metadata: plugins.PluginMetadata{Name: "a"}},
			{Metadata: plugins.PluginMetadata{Name: "b"}},
		}, nil)

		svc := newTestService(WithStore(store))
		listed, err := svc.List(t.Context(), plugins.ListOptions{})
		require.NoError(t, err)
		assert.Len(t, listed, 2)
	})

	t.Run("group filter with nil group manager returns 500", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		// No WithGroupManager — group manager is nil.
		store.EXPECT().List(gomock.Any(), gomock.Any()).Return([]plugins.InstalledPlugin{
			{Metadata: plugins.PluginMetadata{Name: "a"}},
		}, nil)

		svc := newTestService(WithStore(store))
		_, err := svc.List(t.Context(), plugins.ListOptions{Group: "mygroup"})
		require.Error(t, err)
		assert.Equal(t, http.StatusInternalServerError, httperr.Code(err))
	})

	t.Run("store List error propagates", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		store.EXPECT().List(gomock.Any(), gomock.Any()).Return(nil, errors.New("db down"))

		svc := newTestService(WithStore(store))
		_, err := svc.List(t.Context(), plugins.ListOptions{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "db down")
	})

	t.Run("group manager Get error propagates", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		gm := groupmocks.NewMockManager(ctrl)
		store.EXPECT().List(gomock.Any(), gomock.Any()).Return([]plugins.InstalledPlugin{
			{Metadata: plugins.PluginMetadata{Name: "a"}},
		}, nil)
		gm.EXPECT().Get(gomock.Any(), "mygroup").Return(nil, errors.New("etcd unavailable"))

		svc := newTestService(WithStore(store), WithGroupManager(gm))
		_, err := svc.List(t.Context(), plugins.ListOptions{Group: "mygroup"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "getting group")
	})
}

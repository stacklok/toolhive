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
	storemocks "github.com/stacklok/toolhive/pkg/storage/mocks"
)

func TestUninstall(t *testing.T) {
	t.Parallel()

	t.Run("rejects invalid name with bad request", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		svc := newTestService(WithStore(store))
		err := svc.Uninstall(t.Context(), plugins.UninstallOptions{Name: "INVALID"})
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
	})

	t.Run("rejects project scope without root", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		svc := newTestService(WithStore(store))
		err := svc.Uninstall(t.Context(), plugins.UninstallOptions{
			Name:  "my-plugin",
			Scope: plugins.ScopeProject,
		})
		require.Error(t, err)
	})

	// Dematerialize returns an error but store.Delete still runs and succeeds;
	// the dematerialize error is surfaced via errors.Join.
	t.Run("dematerialize error still deletes record and returns joined error", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		adapter := plugmocks.NewMockMaterializationAdapter(ctrl)

		existing := plugins.InstalledPlugin{
			Metadata: plugins.PluginMetadata{Name: "my-plugin"},
			Clients:  []string{"claude-code"},
		}
		store.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(existing, nil)
		adapter.EXPECT().Dematerialize(gomock.Any(), gomock.Any()).
			Return(errors.New("permission denied"))
		store.EXPECT().Delete(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(nil)

		svc := newTestService(WithStore(store),
			WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": adapter}))
		err := svc.Uninstall(t.Context(), plugins.UninstallOptions{Name: "my-plugin"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dematerializing plugin for client")
		assert.Contains(t, err.Error(), "permission denied")
	})

	// store.Delete fails: the dematerialize errors collected so far are dropped
	// because Delete aborts the flow (its error is returned directly). This
	// documents the current contract per the code: the collected cleanupErrs
	// are only returned after a successful Delete.
	t.Run("store delete failure aborts and returns the delete error", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		adapter := plugmocks.NewMockMaterializationAdapter(ctrl)

		existing := plugins.InstalledPlugin{
			Metadata: plugins.PluginMetadata{Name: "my-plugin"},
			Clients:  []string{"claude-code"},
		}
		store.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(existing, nil)
		adapter.EXPECT().Dematerialize(gomock.Any(), gomock.Any()).Return(nil)
		store.EXPECT().Delete(gomock.Any(), "my-plugin", plugins.ScopeUser, "").
			Return(errors.New("db locked"))

		svc := newTestService(WithStore(store),
			WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": adapter}))
		err := svc.Uninstall(t.Context(), plugins.UninstallOptions{Name: "my-plugin"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "db locked")
	})

	// RemovePluginFromAllGroups fails: the error is joined into the final
	// result (store.Delete already succeeded).
	t.Run("group removal failure joins into result", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		adapter := plugmocks.NewMockMaterializationAdapter(ctrl)
		gm := groupmocks.NewMockManager(ctrl)

		existing := plugins.InstalledPlugin{
			Metadata: plugins.PluginMetadata{Name: "my-plugin"},
			Clients:  []string{"claude-code"},
		}
		store.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(existing, nil)
		adapter.EXPECT().Dematerialize(gomock.Any(), gomock.Any()).Return(nil)
		store.EXPECT().Delete(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(nil)
		// RemovePluginFromAllGroups calls List then Update for each matching group.
		gm.EXPECT().List(gomock.Any()).Return(nil, errors.New("etcd unavailable"))

		svc := newTestService(WithStore(store), WithGroupManager(gm),
			WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": adapter}))
		err := svc.Uninstall(t.Context(), plugins.UninstallOptions{Name: "my-plugin"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "removing plugin from groups")
		assert.Contains(t, err.Error(), "etcd unavailable")
	})

	// A missing materializer for a stored client type is skipped (not an error);
	// the remaining clients dematerialize and the record is deleted.
	t.Run("missing materializer for stored client is skipped", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		adapter := plugmocks.NewMockMaterializationAdapter(ctrl)

		existing := plugins.InstalledPlugin{
			Metadata: plugins.PluginMetadata{Name: "my-plugin"},
			Clients:  []string{"claude-code", "ghost-client"},
		}
		store.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(existing, nil)
		// Only claude-code has a materializer; ghost-client is silently skipped.
		adapter.EXPECT().Dematerialize(gomock.Any(), gomock.Any()).Return(nil)
		store.EXPECT().Delete(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(nil)

		svc := newTestService(WithStore(store),
			WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": adapter}))
		err := svc.Uninstall(t.Context(), plugins.UninstallOptions{Name: "my-plugin"})
		require.NoError(t, err)
	})

	// Multi-client: one dematerialize fails, the other succeeds; the record is
	// still deleted and the failure is joined into the result.
	t.Run("multi-client partial dematerialize failure joins errors", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		adapterA := plugmocks.NewMockMaterializationAdapter(ctrl)
		adapterB := plugmocks.NewMockMaterializationAdapter(ctrl)

		existing := plugins.InstalledPlugin{
			Metadata: plugins.PluginMetadata{Name: "my-plugin"},
			Clients:  []string{"claude-code", "codex"},
		}
		store.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(existing, nil)
		adapterA.EXPECT().Dematerialize(gomock.Any(), gomock.Any()).Return(nil)
		adapterB.EXPECT().Dematerialize(gomock.Any(), gomock.Any()).
			Return(errors.New("codex config busy"))
		store.EXPECT().Delete(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(nil)

		svc := newTestService(WithStore(store),
			WithMaterializers(map[string]plugins.MaterializationAdapter{
				"claude-code": adapterA,
				"codex":       adapterB,
			}))
		err := svc.Uninstall(t.Context(), plugins.UninstallOptions{Name: "my-plugin"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "codex config busy")
	})

	// End-to-end happy path with group membership cleanup.
	t.Run("full uninstall removes from groups", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		adapter := plugmocks.NewMockMaterializationAdapter(ctrl)
		gm := groupmocks.NewMockManager(ctrl)

		existing := plugins.InstalledPlugin{
			Metadata: plugins.PluginMetadata{Name: "my-plugin"},
			Clients:  []string{"claude-code"},
		}
		store.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(existing, nil)
		adapter.EXPECT().Dematerialize(gomock.Any(), gomock.Any()).Return(nil)
		store.EXPECT().Delete(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(nil)
		// RemovePluginFromAllGroups lists groups, finds one containing the plugin, updates it.
		gm.EXPECT().List(gomock.Any()).Return([]*groups.Group{
			{Name: "mygroup", Plugins: []string{"my-plugin", "other"}},
		}, nil)
		gm.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil)

		svc := newTestService(WithStore(store), WithGroupManager(gm),
			WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": adapter}))
		err := svc.Uninstall(t.Context(), plugins.UninstallOptions{Name: "my-plugin"})
		require.NoError(t, err)
	})
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"context"
	"errors"
	"net/http"
	"slices"
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

	// RemovePluginFromAllGroups fails before the DB delete so the record
	// remains and uninstall can be retried; dematerialize may already have
	// run (best-effort) and its errors are joined when present.
	t.Run("group removal failure aborts before store delete", func(t *testing.T) {
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
		// Delete must not run — the DB row is what makes retry possible.
		gm.EXPECT().List(gomock.Any()).Return(nil, errors.New("etcd unavailable"))

		svc := newTestService(WithStore(store), WithGroupManager(gm),
			WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": adapter}))
		err := svc.Uninstall(t.Context(), plugins.UninstallOptions{Name: "my-plugin"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "removing plugin from groups")
		assert.Contains(t, err.Error(), "etcd unavailable")
	})

	// A failure on the second group update restores the membership already
	// removed from the first group before returning.
	t.Run("second group update failure restores first membership", func(t *testing.T) {
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

		groupA := map[string][]string{"alpha": {"my-plugin", "other"}, "beta": {"my-plugin"}}
		gm.EXPECT().List(gomock.Any()).Return([]*groups.Group{
			{Name: "alpha", Plugins: append([]string(nil), groupA["alpha"]...)},
			{Name: "beta", Plugins: append([]string(nil), groupA["beta"]...)},
		}, nil)
		gm.EXPECT().Get(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, name string) (*groups.Group, error) {
				return &groups.Group{Name: name, Plugins: append([]string(nil), groupA[name]...)}, nil
			},
		).AnyTimes()
		gm.EXPECT().Update(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, g *groups.Group) error {
				if g.Name == "beta" && !slices.Contains(g.Plugins, "my-plugin") {
					return errors.New("beta update failed")
				}
				groupA[g.Name] = append([]string(nil), g.Plugins...)
				return nil
			},
		).AnyTimes()

		svc := newTestService(WithStore(store), WithGroupManager(gm),
			WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": adapter}))
		err := svc.Uninstall(t.Context(), plugins.UninstallOptions{Name: "my-plugin"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "beta update failed")
		assert.Contains(t, groupA["alpha"], "my-plugin",
			"the membership removed from alpha must be restored after beta fails")
	})

	// A DB-delete failure after successful group removal restores every
	// removed membership so the still-installed plugin stays attached.
	t.Run("store delete failure restores group memberships", func(t *testing.T) {
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
		store.EXPECT().Delete(gomock.Any(), "my-plugin", plugins.ScopeUser, "").
			Return(errors.New("db locked"))

		memberships := map[string][]string{"alpha": {"my-plugin"}}
		gm.EXPECT().List(gomock.Any()).Return([]*groups.Group{
			{Name: "alpha", Plugins: append([]string(nil), memberships["alpha"]...)},
		}, nil)
		gm.EXPECT().Get(gomock.Any(), "alpha").DoAndReturn(
			func(context.Context, string) (*groups.Group, error) {
				return &groups.Group{Name: "alpha", Plugins: append([]string(nil), memberships["alpha"]...)}, nil
			},
		).AnyTimes()
		gm.EXPECT().Update(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, g *groups.Group) error {
				memberships[g.Name] = append([]string(nil), g.Plugins...)
				return nil
			},
		).AnyTimes()

		svc := newTestService(WithStore(store), WithGroupManager(gm),
			WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": adapter}))
		err := svc.Uninstall(t.Context(), plugins.UninstallOptions{Name: "my-plugin"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "db locked")
		assert.Contains(t, memberships["alpha"], "my-plugin",
			"a failed DB delete must restore the removed group membership")
	})

	// A missing materializer for a stored client type is skipped (not an error)
	// on unmanaged uninstall; the remaining clients dematerialize and the
	// record is deleted.
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
		// Group removal lists memberships, then removes per group (Get+Update).
		gm.EXPECT().List(gomock.Any()).Return([]*groups.Group{
			{Name: "mygroup", Plugins: []string{"my-plugin", "other"}},
		}, nil)
		gm.EXPECT().Get(gomock.Any(), "mygroup").
			Return(&groups.Group{Name: "mygroup", Plugins: []string{"my-plugin", "other"}}, nil)
		gm.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil)

		svc := newTestService(WithStore(store), WithGroupManager(gm),
			WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": adapter}))
		err := svc.Uninstall(t.Context(), plugins.UninstallOptions{Name: "my-plugin"})
		require.NoError(t, err)
	})

	// Unmanaged uninstall must not require a ClientManager just to take an
	// unused tree snapshot (regression: client manager is not configured).
	t.Run("unmanaged uninstall without client manager dematerializes and deletes", func(t *testing.T) {
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
		store.EXPECT().Delete(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(nil)

		svc := newTestService(WithStore(store),
			WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": adapter}))
		err := svc.Uninstall(t.Context(), plugins.UninstallOptions{Name: "my-plugin"})
		require.NoError(t, err)
	})

	// Managed uninstall refuses to delete the pin/DB when a recorded client
	// has no materializer, so executable trees are not left orphaned.
	t.Run("managed uninstall refuses missing materializer", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)

		projectRoot := makeProjectRoot(t)
		existing := plugins.InstalledPlugin{
			Metadata:    plugins.PluginMetadata{Name: "my-plugin"},
			Clients:     []string{"claude-code", "ghost-client"},
			Managed:     true,
			Scope:       plugins.ScopeProject,
			ProjectRoot: projectRoot,
		}
		store.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeProject, projectRoot).
			Return(existing, nil)

		svc := newTestService(WithStore(store),
			WithMaterializers(map[string]plugins.MaterializationAdapter{
				"claude-code": plugmocks.NewMockMaterializationAdapter(ctrl),
			}))
		err := svc.Uninstall(t.Context(), plugins.UninstallOptions{
			Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no materializer configured for client \"ghost-client\"")
		assert.Equal(t, http.StatusInternalServerError, httperr.Code(err))
	})
}

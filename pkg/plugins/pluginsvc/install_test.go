// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

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
	ociartifact "github.com/stacklok/toolhive-core/oci/artifact"
	"github.com/stacklok/toolhive/pkg/groups"
	groupmocks "github.com/stacklok/toolhive/pkg/groups/mocks"
	"github.com/stacklok/toolhive/pkg/plugins"
	plugmocks "github.com/stacklok/toolhive/pkg/plugins/mocks"
	"github.com/stacklok/toolhive/pkg/storage"
	storemocks "github.com/stacklok/toolhive/pkg/storage/mocks"
)

// makePluginLayerData builds a tar.gz layer containing a minimal plugin tree
// (manifest + a command file). Used by install round-trip tests.
func makePluginLayerData(t *testing.T, name string) []byte {
	t.Helper()
	files := []ociartifact.FileEntry{
		{Path: ".claude-plugin/plugin.json", Content: []byte(fmt.Sprintf(`{"name":%q,"version":"1.0.0"}`, name)), Mode: 0644},
		{Path: "commands/hello.md", Content: []byte("# hello"), Mode: 0644},
	}
	data, err := ociartifact.CompressTar(files, ociartifact.DefaultTarOptions(), ociartifact.DefaultGzipOptions())
	require.NoError(t, err)
	return data
}

func TestInstallPlainNameNotFound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		opts     plugins.InstallOptions
		wantCode int
		wantErr  string
	}{
		{
			name:     "plain name without store or lookup returns not found",
			opts:     plugins.InstallOptions{Name: "my-plugin"},
			wantCode: http.StatusNotFound,
			wantErr:  "not found in local store or registry",
		},
		{
			name:     "rejects project scope without root",
			opts:     plugins.InstallOptions{Name: "my-plugin", Scope: plugins.ScopeProject},
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "rejects invalid name",
			opts:     plugins.InstallOptions{Name: "A"},
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "rejects empty name",
			opts:     plugins.InstallOptions{Name: ""},
			wantCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			store := storemocks.NewMockPluginStore(ctrl)
			svc := newTestService(WithStore(store))
			_, err := svc.Install(t.Context(), tt.opts)
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

	layerData := makePluginLayerData(t, "my-plugin")

	t.Run("fresh install materializes and creates record", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		adapter := plugmocks.NewMockMaterializationAdapter(ctrl)

		store.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(plugins.InstalledPlugin{}, storage.ErrNotFound)
		adapter.EXPECT().Materialize(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, req plugins.MaterializeRequest) (*plugins.MaterializeResult, error) {
				assert.Equal(t, "my-plugin", req.Name)
				assert.Equal(t, layerData, req.LayerData)
				return &plugins.MaterializeResult{InstalledComponents: []plugins.ComponentType{plugins.ComponentCommands}}, nil
			})
		store.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, p plugins.InstalledPlugin) error {
				assert.Equal(t, plugins.InstallStatusInstalled, p.Status)
				assert.Equal(t, "sha256:abc", p.Digest)
				assert.Equal(t, []string{"claude-code"}, p.Clients)
				return nil
			})

		svc := newTestService(WithStore(store), WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": adapter}))
		result, err := svc.Install(t.Context(), plugins.InstallOptions{
			Name:      "my-plugin",
			LayerData: layerData,
			Digest:    "sha256:abc",
		})
		require.NoError(t, err)
		assert.Equal(t, plugins.InstallStatusInstalled, result.Plugin.Status)
	})

	t.Run("same digest is no-op", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		adapter := plugmocks.NewMockMaterializationAdapter(ctrl)

		existing := plugins.InstalledPlugin{
			Metadata: plugins.PluginMetadata{Name: "my-plugin"},
			Digest:   "sha256:abc",
			Status:   plugins.InstallStatusInstalled,
			Clients:  []string{"claude-code"},
		}
		store.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(existing, nil)
		// No Materialize, no Create, no Update.

		svc := newTestService(WithStore(store), WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": adapter}))
		result, err := svc.Install(t.Context(), plugins.InstallOptions{
			Name:      "my-plugin",
			LayerData: layerData,
			Digest:    "sha256:abc",
		})
		require.NoError(t, err)
		assert.Equal(t, "my-plugin", result.Plugin.Metadata.Name)
	})

	t.Run("different digest triggers upgrade", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		adapter := plugmocks.NewMockMaterializationAdapter(ctrl)

		existing := plugins.InstalledPlugin{
			Metadata: plugins.PluginMetadata{Name: "my-plugin"},
			Digest:   "sha256:old",
			Status:   plugins.InstallStatusInstalled,
			Clients:  []string{"claude-code"},
		}
		store.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(existing, nil)
		adapter.EXPECT().Materialize(gomock.Any(), gomock.Any()).Return(&plugins.MaterializeResult{}, nil)
		store.EXPECT().Update(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, p plugins.InstalledPlugin) error {
				assert.Equal(t, "sha256:new", p.Digest)
				return nil
			})

		svc := newTestService(WithStore(store), WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": adapter}))
		result, err := svc.Install(t.Context(), plugins.InstallOptions{
			Name:      "my-plugin",
			LayerData: layerData,
			Digest:    "sha256:new",
		})
		require.NoError(t, err)
		assert.Equal(t, "sha256:new", result.Plugin.Digest)
	})

	t.Run("explicit client used over default", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		adapter := plugmocks.NewMockMaterializationAdapter(ctrl)

		store.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(plugins.InstalledPlugin{}, storage.ErrNotFound)
		adapter.EXPECT().Materialize(gomock.Any(), gomock.Any()).Return(&plugins.MaterializeResult{}, nil)
		store.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, p plugins.InstalledPlugin) error {
				assert.Equal(t, []string{"custom-client"}, p.Clients)
				return nil
			})

		svc := newTestService(WithStore(store), WithMaterializers(map[string]plugins.MaterializationAdapter{"custom-client": adapter}))
		_, err := svc.Install(t.Context(), plugins.InstallOptions{
			Name:      "my-plugin",
			LayerData: layerData,
			Clients:   []string{"custom-client"},
			Digest:    "sha256:abc",
		})
		require.NoError(t, err)
	})

	t.Run("multiple clients fresh install", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		adapterA := plugmocks.NewMockMaterializationAdapter(ctrl)
		adapterB := plugmocks.NewMockMaterializationAdapter(ctrl)

		store.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(plugins.InstalledPlugin{}, storage.ErrNotFound)
		adapterA.EXPECT().Materialize(gomock.Any(), gomock.Any()).Return(&plugins.MaterializeResult{}, nil)
		adapterB.EXPECT().Materialize(gomock.Any(), gomock.Any()).Return(&plugins.MaterializeResult{}, nil)
		store.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, p plugins.InstalledPlugin) error {
				assert.ElementsMatch(t, []string{"claude-code", "codex"}, p.Clients)
				return nil
			})

		svc := newTestService(WithStore(store), WithMaterializers(map[string]plugins.MaterializationAdapter{
			"claude-code": adapterA,
			"codex":       adapterB,
		}))
		_, err := svc.Install(t.Context(), plugins.InstallOptions{
			Name:      "my-plugin",
			LayerData: layerData,
			Digest:    "sha256:abc",
			Clients:   []string{"claude-code", "codex"},
		})
		require.NoError(t, err)
	})

	t.Run("fresh install rolls back on store.Create failure", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		adapter := plugmocks.NewMockMaterializationAdapter(ctrl)

		store.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(plugins.InstalledPlugin{}, storage.ErrNotFound)
		adapter.EXPECT().Materialize(gomock.Any(), gomock.Any()).Return(&plugins.MaterializeResult{}, nil)
		store.EXPECT().Create(gomock.Any(), gomock.Any()).Return(fmt.Errorf("db write error"))
		adapter.EXPECT().Dematerialize(gomock.Any(), plugins.DematerializeRequest{Name: "my-plugin", Scope: plugins.ScopeUser}).Return(nil)

		svc := newTestService(WithStore(store), WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": adapter}))
		_, err := svc.Install(t.Context(), plugins.InstallOptions{
			Name:      "my-plugin",
			LayerData: layerData,
			Digest:    "sha256:abc",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "db write error")
	})

	t.Run("multi-client fresh install rolls back first client on second materialize failure", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		adapterA := plugmocks.NewMockMaterializationAdapter(ctrl)
		adapterB := plugmocks.NewMockMaterializationAdapter(ctrl)

		store.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(plugins.InstalledPlugin{}, storage.ErrNotFound)
		adapterA.EXPECT().Materialize(gomock.Any(), gomock.Any()).Return(&plugins.MaterializeResult{}, nil)
		adapterB.EXPECT().Materialize(gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("disk full"))
		adapterA.EXPECT().Dematerialize(gomock.Any(), plugins.DematerializeRequest{Name: "my-plugin", Scope: plugins.ScopeUser}).Return(nil)

		svc := newTestService(WithStore(store), WithMaterializers(map[string]plugins.MaterializationAdapter{
			"claude-code": adapterA,
			"codex":       adapterB,
		}))
		_, err := svc.Install(t.Context(), plugins.InstallOptions{
			Name:      "my-plugin",
			LayerData: layerData,
			Digest:    "sha256:abc",
			Clients:   []string{"claude-code", "codex"},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "disk full")
	})

	t.Run("invalid client returns bad request", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)

		svc := newTestService(WithStore(store), WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": plugmocks.NewMockMaterializationAdapter(ctrl)}))
		_, err := svc.Install(t.Context(), plugins.InstallOptions{
			Name:      "my-plugin",
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
		store := storemocks.NewMockPluginStore(ctrl)

		svc := newTestService(WithStore(store), WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": plugmocks.NewMockMaterializationAdapter(ctrl)}))
		_, err := svc.Install(t.Context(), plugins.InstallOptions{
			Name:      "my-plugin",
			LayerData: layerData,
			Digest:    "sha256:abc",
			Clients:   []string{"claude-code", ""},
		})
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
	})

	t.Run("all sentinel expands to every materializer", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		adapterA := plugmocks.NewMockMaterializationAdapter(ctrl)
		adapterB := plugmocks.NewMockMaterializationAdapter(ctrl)

		store.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(plugins.InstalledPlugin{}, storage.ErrNotFound)
		adapterA.EXPECT().Materialize(gomock.Any(), gomock.Any()).Return(&plugins.MaterializeResult{}, nil)
		adapterB.EXPECT().Materialize(gomock.Any(), gomock.Any()).Return(&plugins.MaterializeResult{}, nil)
		store.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, p plugins.InstalledPlugin) error {
				assert.ElementsMatch(t, []string{"claude-code", "codex"}, p.Clients)
				return nil
			})

		svc := newTestService(WithStore(store), WithMaterializers(map[string]plugins.MaterializationAdapter{
			"claude-code": adapterA,
			"codex":       adapterB,
		}))
		_, err := svc.Install(t.Context(), plugins.InstallOptions{
			Name:      "my-plugin",
			LayerData: layerData,
			Digest:    "sha256:abc",
			Clients:   []string{"all"},
		})
		require.NoError(t, err)
	})

	t.Run("all combined with explicit client returns bad request", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)

		svc := newTestService(WithStore(store), WithMaterializers(map[string]plugins.MaterializationAdapter{
			"claude-code": plugmocks.NewMockMaterializationAdapter(ctrl),
		}))
		_, err := svc.Install(t.Context(), plugins.InstallOptions{
			Name:      "my-plugin",
			LayerData: layerData,
			Digest:    "sha256:abc",
			Clients:   []string{"all", "claude-code"},
		})
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
		assert.Contains(t, err.Error(), "cannot be combined")
	})

	t.Run("same digest adds second client without re-materializing first", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		adapterA := plugmocks.NewMockMaterializationAdapter(ctrl)
		adapterB := plugmocks.NewMockMaterializationAdapter(ctrl)

		existing := plugins.InstalledPlugin{
			Metadata: plugins.PluginMetadata{Name: "my-plugin"},
			Digest:   "sha256:abc",
			Clients:  []string{"claude-code"},
		}
		store.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(existing, nil)
		// Only codex (the missing client) is materialized.
		adapterB.EXPECT().Materialize(gomock.Any(), gomock.Any()).Return(&plugins.MaterializeResult{}, nil)
		store.EXPECT().Update(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, p plugins.InstalledPlugin) error {
				assert.ElementsMatch(t, []string{"claude-code", "codex"}, p.Clients)
				return nil
			})

		svc := newTestService(WithStore(store), WithMaterializers(map[string]plugins.MaterializationAdapter{
			"claude-code": adapterA,
			"codex":       adapterB,
		}))
		_, err := svc.Install(t.Context(), plugins.InstallOptions{
			Name:      "my-plugin",
			LayerData: layerData,
			Digest:    "sha256:abc",
			Clients:   []string{"claude-code", "codex"},
		})
		require.NoError(t, err)
	})

	t.Run("upgrade materializes for all existing clients", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		adapterA := plugmocks.NewMockMaterializationAdapter(ctrl)
		adapterB := plugmocks.NewMockMaterializationAdapter(ctrl)

		existing := plugins.InstalledPlugin{
			Metadata: plugins.PluginMetadata{Name: "my-plugin"},
			Digest:   "sha256:old",
			Clients:  []string{"claude-code"},
		}
		store.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(existing, nil)
		// Upgrade re-materializes the union of existing + requested clients.
		adapterA.EXPECT().Materialize(gomock.Any(), gomock.Any()).Return(&plugins.MaterializeResult{}, nil)
		adapterB.EXPECT().Materialize(gomock.Any(), gomock.Any()).Return(&plugins.MaterializeResult{}, nil)
		store.EXPECT().Update(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, p plugins.InstalledPlugin) error {
				assert.ElementsMatch(t, []string{"claude-code", "codex"}, p.Clients)
				assert.Equal(t, "sha256:new", p.Digest)
				return nil
			})

		svc := newTestService(WithStore(store), WithMaterializers(map[string]plugins.MaterializationAdapter{
			"claude-code": adapterA,
			"codex":       adapterB,
		}))
		result, err := svc.Install(t.Context(), plugins.InstallOptions{
			Name:      "my-plugin",
			LayerData: layerData,
			Digest:    "sha256:new",
			Clients:   []string{"codex"},
		})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"claude-code", "codex"}, result.Plugin.Clients)
	})
}

func TestInstallRoundTrip(t *testing.T) {
	t.Parallel()

	layerData := makePluginLayerData(t, "my-plugin")

	t.Run("install then list then uninstall", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		adapter := plugmocks.NewMockMaterializationAdapter(ctrl)

		// Install.
		store.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(plugins.InstalledPlugin{}, storage.ErrNotFound)
		adapter.EXPECT().Materialize(gomock.Any(), gomock.Any()).Return(&plugins.MaterializeResult{}, nil)
		store.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)

		svc := newTestService(WithStore(store), WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": adapter}))
		_, err := svc.Install(t.Context(), plugins.InstallOptions{
			Name:      "my-plugin",
			LayerData: layerData,
			Digest:    "sha256:abc",
		})
		require.NoError(t, err)

		// List.
		store.EXPECT().List(gomock.Any(), gomock.Any()).Return([]plugins.InstalledPlugin{
			{Metadata: plugins.PluginMetadata{Name: "my-plugin"}, Clients: []string{"claude-code"}},
		}, nil)
		listed, err := svc.List(t.Context(), plugins.ListOptions{})
		require.NoError(t, err)
		require.Len(t, listed, 1)
		assert.Equal(t, "my-plugin", listed[0].Metadata.Name)

		// Uninstall.
		store.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(plugins.InstalledPlugin{
			Metadata: plugins.PluginMetadata{Name: "my-plugin"},
			Clients:  []string{"claude-code"},
		}, nil)
		adapter.EXPECT().Dematerialize(gomock.Any(), plugins.DematerializeRequest{Name: "my-plugin", Scope: plugins.ScopeUser}).Return(nil)
		store.EXPECT().Delete(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(nil)

		err = svc.Uninstall(t.Context(), plugins.UninstallOptions{Name: "my-plugin"})
		require.NoError(t, err)
	})

	t.Run("uninstall is idempotent for missing record", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		store.EXPECT().Get(gomock.Any(), "nope", plugins.ScopeUser, "").Return(plugins.InstalledPlugin{}, storage.ErrNotFound)

		svc := newTestService(WithStore(store), WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": plugmocks.NewMockMaterializationAdapter(ctrl)}))
		err := svc.Uninstall(t.Context(), plugins.UninstallOptions{Name: "nope"})
		require.NoError(t, err)
	})
}

func TestInstallAddsPluginToGroup(t *testing.T) {
	t.Parallel()

	layerData := makePluginLayerData(t, "my-plugin")

	tests := []struct {
		name           string
		opts           plugins.InstallOptions
		setupStoreMock func(*storemocks.MockPluginStore)
		setupGroupMock func(*groupmocks.MockManager)
		wantErr        string
	}{
		{
			name: "install with group registers plugin",
			opts: plugins.InstallOptions{Name: "my-plugin", Group: "mygroup", LayerData: layerData, Digest: "sha256:abc"},
			setupStoreMock: func(s *storemocks.MockPluginStore) {
				s.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(plugins.InstalledPlugin{}, storage.ErrNotFound)
				s.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
			},
			setupGroupMock: func(gm *groupmocks.MockManager) {
				gm.EXPECT().Get(gomock.Any(), "mygroup").
					Return(&groups.Group{Name: "mygroup", Plugins: []string{}}, nil)
				gm.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil)
			},
		},
		{
			name: "install without group defaults to default group",
			opts: plugins.InstallOptions{Name: "my-plugin", LayerData: layerData, Digest: "sha256:abc"},
			setupStoreMock: func(s *storemocks.MockPluginStore) {
				s.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(plugins.InstalledPlugin{}, storage.ErrNotFound)
				s.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
			},
			setupGroupMock: func(gm *groupmocks.MockManager) {
				gm.EXPECT().Get(gomock.Any(), groups.DefaultGroup).
					Return(&groups.Group{Name: groups.DefaultGroup, Plugins: []string{}}, nil)
				gm.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil)
			},
		},
		{
			name: "group registration error rolls back DB record",
			opts: plugins.InstallOptions{Name: "my-plugin", Group: "badgroup", LayerData: layerData, Digest: "sha256:abc"},
			setupStoreMock: func(s *storemocks.MockPluginStore) {
				s.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(plugins.InstalledPlugin{}, storage.ErrNotFound)
				s.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
				s.EXPECT().Delete(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(nil)
			},
			setupGroupMock: func(gm *groupmocks.MockManager) {
				gm.EXPECT().Get(gomock.Any(), "badgroup").Return(nil, fmt.Errorf("group not found"))
			},
			wantErr: "registering plugin in group",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			store := storemocks.NewMockPluginStore(ctrl)
			gm := groupmocks.NewMockManager(ctrl)
			adapter := plugmocks.NewMockMaterializationAdapter(ctrl)
			adapter.EXPECT().Materialize(gomock.Any(), gomock.Any()).Return(&plugins.MaterializeResult{}, nil).AnyTimes()

			tt.setupStoreMock(store)
			tt.setupGroupMock(gm)

			svc := newTestService(WithStore(store), WithGroupManager(gm),
				WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": adapter}))

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

// tempDir returns an eval-symlink-resolved temp directory. Mirror of
// skillsvc.tempDir.
func tempDir(t *testing.T) string {
	t.Helper()
	realTmpDir, _ := filepath.EvalSymlinks(t.TempDir())
	return realTmpDir
}

// Suppress unused-import in case a future trim removes the os/filepath use.
var _ = os.Stat

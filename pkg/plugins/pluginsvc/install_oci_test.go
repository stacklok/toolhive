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

	godigest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
	ociplugins "github.com/stacklok/toolhive-core/oci/plugins"
	ocimocks "github.com/stacklok/toolhive-core/oci/plugins/mocks"
	"github.com/stacklok/toolhive/pkg/plugins"
	plugmocks "github.com/stacklok/toolhive/pkg/plugins/mocks"
	"github.com/stacklok/toolhive/pkg/storage"
	storemocks "github.com/stacklok/toolhive/pkg/storage/mocks"
)

func TestInstallFromOCI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		opts        plugins.InstallOptions
		setup       func(t *testing.T, ctrl *gomock.Controller) (ociplugins.RegistryClient, *ociplugins.Store, *storemocks.MockPluginStore, *plugmocks.MockMaterializationAdapter)
		wantCode    int
		wantErr     string
		wantName    string
		wantVersion string
		wantDigest  bool
		wantRef     string
	}{
		{
			name: "registry not configured",
			opts: plugins.InstallOptions{Name: "ghcr.io/org/my-plugin:v1"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (ociplugins.RegistryClient, *ociplugins.Store, *storemocks.MockPluginStore, *plugmocks.MockMaterializationAdapter) {
				t.Helper()
				return nil, nil, storemocks.NewMockPluginStore(ctrl), plugmocks.NewMockMaterializationAdapter(ctrl)
			},
			wantCode: http.StatusInternalServerError,
		},
		{
			name: "ociStore not configured",
			opts: plugins.InstallOptions{Name: "ghcr.io/org/my-plugin:v1"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (ociplugins.RegistryClient, *ociplugins.Store, *storemocks.MockPluginStore, *plugmocks.MockMaterializationAdapter) {
				t.Helper()
				return ocimocks.NewMockRegistryClient(ctrl), nil, storemocks.NewMockPluginStore(ctrl), plugmocks.NewMockMaterializationAdapter(ctrl)
			},
			wantCode: http.StatusInternalServerError,
		},
		{
			name: "no materializers configured",
			opts: plugins.InstallOptions{Name: "ghcr.io/org/my-plugin:v1"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (ociplugins.RegistryClient, *ociplugins.Store, *storemocks.MockPluginStore, *plugmocks.MockMaterializationAdapter) {
				t.Helper()
				ociStore, err := ociplugins.NewStore(tempDir(t))
				require.NoError(t, err)
				return ocimocks.NewMockRegistryClient(ctrl), ociStore, storemocks.NewMockPluginStore(ctrl), nil
			},
			wantCode: http.StatusInternalServerError,
		},
		{
			name: "pull error propagates",
			opts: plugins.InstallOptions{Name: "ghcr.io/org/my-plugin:v1"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (ociplugins.RegistryClient, *ociplugins.Store, *storemocks.MockPluginStore, *plugmocks.MockMaterializationAdapter) {
				t.Helper()
				ociStore, err := ociplugins.NewStore(tempDir(t))
				require.NoError(t, err)
				reg := ocimocks.NewMockRegistryClient(ctrl)
				reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/org/my-plugin:v1").
					Return(godigest.Digest(""), fmt.Errorf("auth required"))
				return reg, ociStore, storemocks.NewMockPluginStore(ctrl), plugmocks.NewMockMaterializationAdapter(ctrl)
			},
			wantErr: "auth required",
		},
		{
			name: "name mismatch between artifact and reference is rejected",
			opts: plugins.InstallOptions{Name: "ghcr.io/org/some-repo:v1", Clients: []string{"claude-code"}},
			setup: func(t *testing.T, ctrl *gomock.Controller) (ociplugins.RegistryClient, *ociplugins.Store, *storemocks.MockPluginStore, *plugmocks.MockMaterializationAdapter) {
				t.Helper()
				ociStore, err := ociplugins.NewStore(tempDir(t))
				require.NoError(t, err)
				indexDigest := buildTestPlugin(t, ociStore, "actual-plugin", "2.0.0")

				reg := ocimocks.NewMockRegistryClient(ctrl)
				reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/org/some-repo:v1").
					Return(indexDigest, nil)

				return reg, ociStore, storemocks.NewMockPluginStore(ctrl), plugmocks.NewMockMaterializationAdapter(ctrl)
			},
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "does not match OCI reference repository",
		},
		{
			name: "successful pull and install",
			opts: plugins.InstallOptions{Name: "ghcr.io/org/my-plugin:v1", Clients: []string{"claude-code"}},
			setup: func(t *testing.T, ctrl *gomock.Controller) (ociplugins.RegistryClient, *ociplugins.Store, *storemocks.MockPluginStore, *plugmocks.MockMaterializationAdapter) {
				t.Helper()
				ociStore, err := ociplugins.NewStore(tempDir(t))
				require.NoError(t, err)
				indexDigest := buildTestPlugin(t, ociStore, "my-plugin", "1.0.0")

				reg := ocimocks.NewMockRegistryClient(ctrl)
				reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/org/my-plugin:v1").
					Return(indexDigest, nil)

				store := storemocks.NewMockPluginStore(ctrl)
				adapter := plugmocks.NewMockMaterializationAdapter(ctrl)
				store.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(plugins.InstalledPlugin{}, storage.ErrNotFound)
				adapter.EXPECT().Materialize(gomock.Any(), gomock.Any()).Return(&plugins.MaterializeResult{}, nil)
				store.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ context.Context, p plugins.InstalledPlugin) error {
						assert.Equal(t, "my-plugin", p.Metadata.Name)
						assert.Equal(t, "1.0.0", p.Metadata.Version)
						assert.Equal(t, "ghcr.io/org/my-plugin:v1", p.Reference)
						assert.Equal(t, "v1", p.Tag)
						assert.Contains(t, p.Digest, "sha256:")
						return nil
					})
				return reg, ociStore, store, adapter
			},
			wantName:    "my-plugin",
			wantVersion: "1.0.0",
			wantDigest:  true,
			wantRef:     "ghcr.io/org/my-plugin:v1",
		},
		{
			name: "preserves caller version over config version",
			opts: plugins.InstallOptions{Name: "ghcr.io/org/my-plugin:v1", Version: "override-version", Clients: []string{"claude-code"}},
			setup: func(t *testing.T, ctrl *gomock.Controller) (ociplugins.RegistryClient, *ociplugins.Store, *storemocks.MockPluginStore, *plugmocks.MockMaterializationAdapter) {
				t.Helper()
				ociStore, err := ociplugins.NewStore(tempDir(t))
				require.NoError(t, err)
				indexDigest := buildTestPlugin(t, ociStore, "my-plugin", "1.0.0")

				reg := ocimocks.NewMockRegistryClient(ctrl)
				reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/org/my-plugin:v1").
					Return(indexDigest, nil)

				store := storemocks.NewMockPluginStore(ctrl)
				adapter := plugmocks.NewMockMaterializationAdapter(ctrl)
				store.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(plugins.InstalledPlugin{}, storage.ErrNotFound)
				adapter.EXPECT().Materialize(gomock.Any(), gomock.Any()).Return(&plugins.MaterializeResult{}, nil)
				store.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ context.Context, p plugins.InstalledPlugin) error {
						assert.Equal(t, "override-version", p.Metadata.Version)
						return nil
					})
				return reg, ociStore, store, adapter
			},
			wantName:    "my-plugin",
			wantVersion: "override-version",
		},
		{
			name: "invalid OCI reference returns 400",
			opts: plugins.InstallOptions{Name: "not://valid:ref:extra"},
			setup: func(t *testing.T, ctrl *gomock.Controller) (ociplugins.RegistryClient, *ociplugins.Store, *storemocks.MockPluginStore, *plugmocks.MockMaterializationAdapter) {
				t.Helper()
				return nil, nil, storemocks.NewMockPluginStore(ctrl), plugmocks.NewMockMaterializationAdapter(ctrl)
			},
			wantCode: http.StatusBadRequest,
		},
		{
			name: "oversized layer returns 422",
			opts: plugins.InstallOptions{Name: "ghcr.io/org/oversize-plugin:v1", Clients: []string{"claude-code"}},
			setup: func(t *testing.T, ctrl *gomock.Controller) (ociplugins.RegistryClient, *ociplugins.Store, *storemocks.MockPluginStore, *plugmocks.MockMaterializationAdapter) {
				t.Helper()
				ociStore, err := ociplugins.NewStore(tempDir(t))
				require.NoError(t, err)
				manifestDigest := buildPluginManifestWithLayerSize(t, ociStore, "oversize-plugin", maxCompressedLayerSize+1)

				reg := ocimocks.NewMockRegistryClient(ctrl)
				reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/org/oversize-plugin:v1").
					Return(manifestDigest, nil)
				return reg, ociStore, storemocks.NewMockPluginStore(ctrl), plugmocks.NewMockMaterializationAdapter(ctrl)
			},
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "compressed layer size",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			registry, ociStore, store, adapter := tt.setup(t, ctrl)

			var opts []Option
			if registry != nil {
				opts = append(opts, WithRegistryClient(registry))
			}
			if ociStore != nil {
				opts = append(opts, WithOCIStore(ociStore))
			}
			if adapter != nil {
				opts = append(opts, WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": adapter}))
			}
			opts = append(opts, WithStore(store))

			svc := newTestService(opts...)
			result, err := svc.Install(t.Context(), tt.opts)

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
			if tt.wantName != "" {
				assert.Equal(t, tt.wantName, result.Plugin.Metadata.Name)
			}
			if tt.wantVersion != "" {
				assert.Equal(t, tt.wantVersion, result.Plugin.Metadata.Version)
			}
			if tt.wantDigest {
				assert.Contains(t, result.Plugin.Digest, "sha256:")
			}
			if tt.wantRef != "" {
				assert.Equal(t, tt.wantRef, result.Plugin.Reference)
			}
		})
	}
}

func TestInstallFromLocalStore(t *testing.T) {
	t.Parallel()

	t.Run("happy path: build then install", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		ociStore, err := ociplugins.NewStore(tempDir(t))
		require.NoError(t, err)

		indexDigest := buildTestPlugin(t, ociStore, "my-plugin", "1.0.0")
		require.NoError(t, ociStore.Tag(t.Context(), indexDigest, "my-plugin"))

		store := storemocks.NewMockPluginStore(ctrl)
		adapter := plugmocks.NewMockMaterializationAdapter(ctrl)
		store.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(plugins.InstalledPlugin{}, storage.ErrNotFound)
		adapter.EXPECT().Materialize(gomock.Any(), gomock.Any()).Return(&plugins.MaterializeResult{}, nil)
		store.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, p plugins.InstalledPlugin) error {
				assert.Equal(t, "my-plugin", p.Metadata.Name)
				assert.Equal(t, "1.0.0", p.Metadata.Version)
				assert.Contains(t, p.Digest, "sha256:")
				return nil
			})

		svc := newTestService(WithStore(store), WithOCIStore(ociStore),
			WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": adapter}))
		result, err := svc.Install(t.Context(), plugins.InstallOptions{
			Name:    "my-plugin",
			Clients: []string{"claude-code"},
		})
		require.NoError(t, err)
		assert.Equal(t, "my-plugin", result.Plugin.Metadata.Name)
	})

	t.Run("name mismatch in local artifact", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		ociStore, err := ociplugins.NewStore(tempDir(t))
		require.NoError(t, err)

		indexDigest := buildTestPlugin(t, ociStore, "real-plugin", "1.0.0")
		require.NoError(t, ociStore.Tag(t.Context(), indexDigest, "evil-plugin"))

		store := storemocks.NewMockPluginStore(ctrl)
		svc := newTestService(WithStore(store), WithOCIStore(ociStore),
			WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": plugmocks.NewMockMaterializationAdapter(ctrl)}))
		_, err = svc.Install(t.Context(), plugins.InstallOptions{Name: "evil-plugin"})
		require.Error(t, err)
		assert.Equal(t, http.StatusUnprocessableEntity, httperr.Code(err))
		assert.Contains(t, err.Error(), "does not match install name")
	})

	t.Run("tag not found returns not found error", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		ociStore, err := ociplugins.NewStore(tempDir(t))
		require.NoError(t, err)

		store := storemocks.NewMockPluginStore(ctrl)
		svc := newTestService(WithStore(store), WithOCIStore(ociStore),
			WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": plugmocks.NewMockMaterializationAdapter(ctrl)}))
		_, err = svc.Install(t.Context(), plugins.InstallOptions{Name: "no-such-plugin"})
		require.Error(t, err)
		assert.Equal(t, http.StatusNotFound, httperr.Code(err))
		assert.Contains(t, err.Error(), "not found in local store or registry")
	})
}

// suppress unused import when builds trim helper usage.
var _ = os.Stat
var _ = filepath.Join

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"context"
	"fmt"
	"net/http"
	"testing"

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

// stubLookup is a test helper implementing PluginLookup with canned results.
type stubLookup struct {
	hits []PluginSearchHit
	err  error
}

func (s *stubLookup) SearchPlugins(_ context.Context, _ string) ([]PluginSearchHit, error) {
	return s.hits, s.err
}

func TestInstallRegistryResolution(t *testing.T) {
	t.Parallel()

	t.Run("resolves plain name via lookup and installs from OCI", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)

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
				return nil
			})

		lookup := &stubLookup{hits: []PluginSearchHit{
			{
				Name:        "my-plugin",
				Description: "test plugin",
				Packages:    []PluginPackage{{Reference: "ghcr.io/org/my-plugin:v1", Type: "oci"}},
			},
		}}

		svc := newTestService(
			WithStore(store),
			WithOCIStore(ociStore),
			WithRegistryClient(reg),
			WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": adapter}),
			WithPluginLookup(lookup),
		)
		result, err := svc.Install(t.Context(), plugins.InstallOptions{
			Name:    "my-plugin",
			Clients: []string{"claude-code"},
		})
		require.NoError(t, err)
		assert.Equal(t, "my-plugin", result.Plugin.Metadata.Name)
		assert.Equal(t, "1.0.0", result.Plugin.Metadata.Version)
		assert.Equal(t, "ghcr.io/org/my-plugin:v1", result.Plugin.Reference)
	})

	t.Run("lookup returns no hits returns 404", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)

		store := storemocks.NewMockPluginStore(ctrl)

		lookup := &stubLookup{hits: nil}

		svc := newTestService(
			WithStore(store),
			WithPluginLookup(lookup),
		)
		_, err := svc.Install(t.Context(), plugins.InstallOptions{Name: "nonexistent"})
		require.Error(t, err)
		assert.Equal(t, http.StatusNotFound, httperr.Code(err))
		assert.Contains(t, err.Error(), "not found in local store or registry")
	})

	t.Run("lookup search error propagates", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)

		store := storemocks.NewMockPluginStore(ctrl)

		lookup := &stubLookup{err: fmt.Errorf("registry timeout")}

		svc := newTestService(
			WithStore(store),
			WithPluginLookup(lookup),
		)
		_, err := svc.Install(t.Context(), plugins.InstallOptions{Name: "some-plugin"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "registry timeout")
	})
}

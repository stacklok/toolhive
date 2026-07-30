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

	t.Run("exact name wins over substring matches", func(t *testing.T) {
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
				return nil
			})

		// Search returns several substring hits, only one with an exact Name
		// match. The exact match must be selected even though it is not first.
		lookup := &stubLookup{hits: []PluginSearchHit{
			{Name: "my-plugin-extra", Packages: []PluginPackage{{Reference: "ghcr.io/org/other:v1", Type: "oci"}}},
			{Name: "my-plugin", Packages: []PluginPackage{{Reference: "ghcr.io/org/my-plugin:v1", Type: "oci"}}},
			{Name: "my-plugin-tool", Packages: []PluginPackage{{Reference: "ghcr.io/org/other2:v1", Type: "oci"}}},
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
		assert.Equal(t, "ghcr.io/org/my-plugin:v1", result.Plugin.Reference)
	})

	t.Run("ambiguous exact matches return 409", func(t *testing.T) {
		t.Parallel()

		// Two hits with the exact same Name across namespaces — ambiguous.
		lookup := &stubLookup{hits: []PluginSearchHit{
			{Name: "my-plugin", Packages: []PluginPackage{{Reference: "ghcr.io/org1/my-plugin:v1", Type: "oci"}}},
			{Name: "my-plugin", Packages: []PluginPackage{{Reference: "ghcr.io/org2/my-plugin:v1", Type: "oci"}}},
		}}

		svc := newTestService(
			WithPluginLookup(lookup),
		)
		_, err := svc.Install(t.Context(), plugins.InstallOptions{Name: "my-plugin"})
		require.Error(t, err)
		assert.Equal(t, http.StatusConflict, httperr.Code(err))
		assert.Contains(t, err.Error(), "ambiguous plugin name")
		assert.Contains(t, err.Error(), "my-plugin")
	})

	t.Run("selects oci package over positional git package", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)

		ociStore, err := ociplugins.NewStore(tempDir(t))
		require.NoError(t, err)
		indexDigest := buildTestPlugin(t, ociStore, "my-plugin", "1.0.0")

		reg := ocimocks.NewMockRegistryClient(ctrl)
		// The OCI package is second in the list; the first is a git package.
		// Selection must pick the OCI package, not Packages[0].
		reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/org/my-plugin:v1").
			Return(indexDigest, nil)

		store := storemocks.NewMockPluginStore(ctrl)
		adapter := plugmocks.NewMockMaterializationAdapter(ctrl)
		store.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(plugins.InstalledPlugin{}, storage.ErrNotFound)
		adapter.EXPECT().Materialize(gomock.Any(), gomock.Any()).Return(&plugins.MaterializeResult{}, nil)
		store.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, p plugins.InstalledPlugin) error {
				assert.Equal(t, "ghcr.io/org/my-plugin:v1", p.Reference)
				return nil
			})

		lookup := &stubLookup{hits: []PluginSearchHit{
			{
				Name: "my-plugin",
				Packages: []PluginPackage{
					{Reference: "https://github.com/org/repo", Type: "git"},
					{Reference: "ghcr.io/org/my-plugin:v1", Type: "oci"},
				},
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
		assert.Equal(t, "ghcr.io/org/my-plugin:v1", result.Plugin.Reference)
	})

	t.Run("no oci package returns 422", func(t *testing.T) {
		t.Parallel()

		// Exact match but only a git package (plugins have no git install flow).
		lookup := &stubLookup{hits: []PluginSearchHit{
			{Name: "my-plugin", Packages: []PluginPackage{{Reference: "https://github.com/org/repo", Type: "git"}}},
		}}

		svc := newTestService(
			WithPluginLookup(lookup),
		)
		_, err := svc.Install(t.Context(), plugins.InstallOptions{Name: "my-plugin"})
		require.Error(t, err)
		assert.Equal(t, http.StatusUnprocessableEntity, httperr.Code(err))
		assert.Contains(t, err.Error(), "no installable OCI package")
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

	t.Run("lookup search error falls back to 404 not-found", func(t *testing.T) {
		t.Parallel()

		lookup := &stubLookup{err: fmt.Errorf("registry timeout")}

		svc := newTestService(
			WithPluginLookup(lookup),
		)
		_, err := svc.Install(t.Context(), plugins.InstallOptions{Name: "some-plugin"})
		require.Error(t, err)
		// A lookup error is logged and treated as not-found (mirroring
		// skillsvc.resolveFromRegistry), not propagated.
		assert.Equal(t, http.StatusNotFound, httperr.Code(err))
		assert.Contains(t, err.Error(), "not found in local store or registry")
	})

	// Regression: a malformed catalog package typed "oci" but whose Reference
	// has no '/', ':', or '@' must not reach installFromOCI with a nil ref.
	// parseOCIReference returns (nil, false, nil) for such input; previously
	// the isOCI bool was discarded and the nil ref caused a panic in
	// validateOCIRegistryHost via ref.Context(). Must return 422 instead.
	t.Run("malformed oci package reference returns 422 no panic", func(t *testing.T) {
		t.Parallel()

		lookup := &stubLookup{hits: []PluginSearchHit{
			{
				Name:    "my-plugin",
				Version: "1.0.0",
				Packages: []PluginPackage{
					{Reference: "foo", Type: "oci"},
				},
			},
		}}

		svc := newTestService(
			WithPluginLookup(lookup),
		)
		_, err := svc.Install(t.Context(), plugins.InstallOptions{Name: "my-plugin"})
		require.Error(t, err)
		assert.Equal(t, http.StatusUnprocessableEntity, httperr.Code(err))
		assert.Contains(t, err.Error(), "invalid OCI reference")
		assert.Contains(t, err.Error(), "foo")
	})

	// Regression: when opts.Version is set, only an exact-name hit whose
	// Version matches is installed.
	t.Run("version requested with matching hit version installs", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)

		ociStore, err := ociplugins.NewStore(tempDir(t))
		require.NoError(t, err)
		indexDigest := buildTestPlugin(t, ociStore, "my-plugin", "1.0.0")

		reg := ocimocks.NewMockRegistryClient(ctrl)
		reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/org/my-plugin:1.0.0").
			Return(indexDigest, nil)

		store := storemocks.NewMockPluginStore(ctrl)
		adapter := plugmocks.NewMockMaterializationAdapter(ctrl)
		store.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeUser, "").Return(plugins.InstalledPlugin{}, storage.ErrNotFound)
		adapter.EXPECT().Materialize(gomock.Any(), gomock.Any()).Return(&plugins.MaterializeResult{}, nil)
		store.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, p plugins.InstalledPlugin) error {
				assert.Equal(t, "my-plugin", p.Metadata.Name)
				assert.Equal(t, "ghcr.io/org/my-plugin:1.0.0", p.Reference)
				return nil
			})

		lookup := &stubLookup{hits: []PluginSearchHit{
			{
				Name:    "my-plugin",
				Version: "1.0.0",
				Packages: []PluginPackage{
					{Reference: "ghcr.io/org/my-plugin:1.0.0", Type: "oci"},
				},
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
			Version: "1.0.0",
			Clients: []string{"claude-code"},
		})
		require.NoError(t, err)
		assert.Equal(t, "my-plugin", result.Plugin.Metadata.Name)
		assert.Equal(t, "ghcr.io/org/my-plugin:1.0.0", result.Plugin.Reference)
	})

	// Regression: a version request with only non-matching hit versions must
	// fall through to the 404 path, and the message must mention the version.
	t.Run("version requested with non-matching hit version returns 404 mentioning version", func(t *testing.T) {
		t.Parallel()

		lookup := &stubLookup{hits: []PluginSearchHit{
			{
				Name:    "my-plugin",
				Version: "2.0.0",
				Packages: []PluginPackage{
					{Reference: "ghcr.io/org/my-plugin:2.0.0", Type: "oci"},
				},
			},
		}}

		svc := newTestService(
			WithPluginLookup(lookup),
		)
		_, err := svc.Install(t.Context(), plugins.InstallOptions{
			Name:    "my-plugin",
			Version: "1.0.0",
		})
		require.Error(t, err)
		assert.Equal(t, http.StatusNotFound, httperr.Code(err))
		assert.Contains(t, err.Error(), "not found in local store or registry")
		assert.Contains(t, err.Error(), "1.0.0", "404 message should mention the requested version")
	})

	// Regression: the install hint text uses the renamed command
	// "thv ai-plugin install" (was "thv plugin install").
	t.Run("not found hint text references thv ai-plugin install", func(t *testing.T) {
		t.Parallel()

		lookup := &stubLookup{hits: nil}

		svc := newTestService(
			WithPluginLookup(lookup),
		)
		_, err := svc.Install(t.Context(), plugins.InstallOptions{Name: "nonexistent"})
		require.Error(t, err)
		assert.Equal(t, http.StatusNotFound, httperr.Code(err))
		assert.Contains(t, err.Error(), "thv ai-plugin install")
		assert.NotContains(t, err.Error(), "thv plugin install")
	})
}

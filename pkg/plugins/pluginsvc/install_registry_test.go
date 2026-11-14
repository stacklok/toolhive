// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
	ociplugins "github.com/stacklok/toolhive-core/oci/plugins"
	ocimocks "github.com/stacklok/toolhive-core/oci/plugins/mocks"
	regtypes "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/plugins"
	plugmocks "github.com/stacklok/toolhive/pkg/plugins/mocks"
	"github.com/stacklok/toolhive/pkg/skills/verifier"
	verifiermocks "github.com/stacklok/toolhive/pkg/skills/verifier/mocks"
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

	t.Run("hydrates catalog provenance into first-use verification", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		projectRoot := makeProjectRoot(t)
		catalogProvenance := &regtypes.Provenance{
			SignerIdentity: testSignerIdentity,
			CertIssuer:     testCertIssuer,
		}

		ociStore, err := ociplugins.NewStore(tempDir(t))
		require.NoError(t, err)
		indexDigest := buildTestPlugin(t, ociStore, "my-plugin", "1.0.0")

		reg := ocimocks.NewMockRegistryClient(ctrl)
		reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/org/my-plugin:v1").
			Return(indexDigest, nil)
		mv := verifiermocks.NewMockVerifier(ctrl)
		mv.EXPECT().VerifyOCI(
			gomock.Any(), "ghcr.io/org/my-plugin:v1", indexDigest.String(),
			gomock.Eq(verifier.NewCatalogExpectation(catalogProvenance))).
			Return(signedResult(), nil)

		store := storemocks.NewMockPluginStore(ctrl)
		adapter := plugmocks.NewMockMaterializationAdapter(ctrl)
		store.EXPECT().Get(gomock.Any(), "my-plugin", plugins.ScopeProject, projectRoot).
			Return(plugins.InstalledPlugin{}, storage.ErrNotFound)
		adapter.EXPECT().Materialize(gomock.Any(), gomock.Any()).Return(&plugins.MaterializeResult{}, nil)
		store.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
		store.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil)

		lookup := &stubLookup{hits: []PluginSearchHit{
			{
				Name:       "my-plugin",
				Provenance: catalogProvenance,
				Packages:   []PluginPackage{{Reference: "ghcr.io/org/my-plugin:v1", Type: "oci"}},
			},
		}}
		svc := newTestService(
			WithStore(store),
			WithOCIStore(ociStore),
			WithRegistryClient(reg),
			WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": adapter}),
			WithPluginLookup(lookup),
			WithVerifier(mv),
		)

		result, err := svc.Install(t.Context(), plugins.InstallOptions{
			Name:        "my-plugin",
			Scope:       plugins.ScopeProject,
			ProjectRoot: projectRoot,
			Clients:     []string{"claude-code"},
		})
		require.NoError(t, err)
		require.NotNil(t, result.Provenance)
		assert.Equal(t, testSignerIdentity, result.Provenance.SignerIdentity)
		assert.True(t, result.Plugin.Managed)
	})

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

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestInstallRegistryCatalogRejectionHasNoSideEffects(t *testing.T) {
	tests := []struct {
		name       string
		provenance *regtypes.Provenance
		verifyErr  error
		wantCode   int
	}{
		{
			name:       "provenance mismatch",
			provenance: &regtypes.Provenance{SignerIdentity: "attacker@example.com"},
			verifyErr:  verifier.ErrSignerMismatch,
			wantCode:   http.StatusForbidden,
		},
		{
			name:       "unsupported sigstore URL",
			provenance: &regtypes.Provenance{SigstoreURL: "https://sigstore.example.com/root.json"},
			wantCode:   http.StatusUnprocessableEntity,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			ociStore, err := ociplugins.NewStore(tempDir(t))
			require.NoError(t, err)
			indexDigest := buildTestPlugin(t, ociStore, "my-plugin", "1.0.0")

			reg := ocimocks.NewMockRegistryClient(ctrl)
			reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/org/my-plugin:v1").
				Return(indexDigest, nil)
			mv := verifiermocks.NewMockVerifier(ctrl)
			if tc.verifyErr != nil {
				mv.EXPECT().VerifyOCI(
					gomock.Any(), "ghcr.io/org/my-plugin:v1", indexDigest.String(),
					gomock.Eq(verifier.NewCatalogExpectation(tc.provenance))).
					Return(nil, tc.verifyErr)
			}
			lookup := &stubLookup{hits: []PluginSearchHit{
				{
					Name:       "my-plugin",
					Provenance: tc.provenance,
					Packages:   []PluginPackage{{Reference: "ghcr.io/org/my-plugin:v1", Type: "oci"}},
				},
			}}
			svc, projectRoot := newLockTestService(t,
				WithOCIStore(ociStore),
				WithRegistryClient(reg),
				WithPluginLookup(lookup),
				WithVerifier(mv),
			)

			_, err = svc.Install(t.Context(), plugins.InstallOptions{
				Name:        "my-plugin",
				Scope:       plugins.ScopeProject,
				ProjectRoot: projectRoot,
				Clients:     []string{"claude-code"},
			})
			require.Error(t, err)
			assert.Equal(t, tc.wantCode, httperr.Code(err))

			_, ok := loadPluginLockEntry(t, projectRoot)
			assert.False(t, ok, "a rejected catalog policy must not write a lock entry")
			_, err = svc.Info(t.Context(), plugins.InfoOptions{
				Name: "my-plugin", Scope: plugins.ScopeProject, ProjectRoot: projectRoot,
			})
			require.Error(t, err, "a rejected catalog policy must not create a database record")
			assert.NoDirExists(t, filepath.Join(projectRoot, ".claude", "plugins", "my-plugin"),
				"verification must fail before plugin files are materialized")
		})
	}
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestInstallRegistryUserScopeIgnoresUnsupportedCatalogConstraints(t *testing.T) {
	tests := []struct {
		name       string
		provenance *regtypes.Provenance
	}{
		{
			name: "attestation",
			provenance: &regtypes.Provenance{
				Attestation: &regtypes.VerifiedAttestation{PredicateType: "https://slsa.dev/provenance/v1"},
			},
		},
		{
			name:       "sigstore URL",
			provenance: &regtypes.Provenance{SigstoreURL: "https://sigstore.example.com/root.json"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			ociStore, err := ociplugins.NewStore(tempDir(t))
			require.NoError(t, err)
			indexDigest := buildTestPlugin(t, ociStore, "my-plugin", "1.0.0")

			reg := ocimocks.NewMockRegistryClient(ctrl)
			reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/org/my-plugin:v1").
				Return(indexDigest, nil)
			lookup := &stubLookup{hits: []PluginSearchHit{
				{
					Name:       "my-plugin",
					Provenance: tc.provenance,
					Packages:   []PluginPackage{{Reference: "ghcr.io/org/my-plugin:v1", Type: "oci"}},
				},
			}}
			// The verifier has no expectations: catalog policy does not apply
			// to user-scoped installs because they record no lock-backed trust.
			mv := verifiermocks.NewMockVerifier(ctrl)
			svc, _ := newLockTestService(t,
				WithOCIStore(ociStore),
				WithRegistryClient(reg),
				WithPluginLookup(lookup),
				WithVerifier(mv),
			)

			result, err := svc.Install(t.Context(), plugins.InstallOptions{
				Name:    "my-plugin",
				Scope:   plugins.ScopeUser,
				Clients: []string{"claude-code"},
			})
			require.NoError(t, err)
			assert.Equal(t, "my-plugin", result.Plugin.Metadata.Name)
			assert.False(t, result.Plugin.Managed)
		})
	}
}

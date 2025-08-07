package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/registry"
)

func TestRegistryRouter(t *testing.T) {
	t.Parallel()

	logger.Initialize()

	router := RegistryRouter(nil)
	assert.NotNil(t, router)
}

func TestRegistryAPI_TypeAndSourceFields(t *testing.T) {
	logger.Initialize()

	tests := []struct {
		name           string
		setupConfig    func()
		expectedType   RegistryType
		expectedSource string
		description    string
	}{
		{
			name: "built-in registry",
			setupConfig: func() {
				config.UnsetRegistry()
				registry.ResetDefaultProvider()
			},
			expectedType:   RegistryTypeDefault,
			expectedSource: "",
			description:    "Default built-in registry should return type 'default' and empty source",
		},
		{
			name: "remote registry",
			setupConfig: func() {
				config.SetRegistryURL("https://example.com/registry.json", false)
				registry.ResetDefaultProvider()
			},
			expectedType:   RegistryTypeURL,
			expectedSource: "https://example.com/registry.json",
			description:    "Remote registry should return type 'url' and the URL as source",
		},
		{
			name: "local file registry",
			setupConfig: func() {
				config.UnsetRegistry()
				config.UpdateConfig(func(c *config.Config) {
					c.LocalRegistryPath = "/path/to/registry.json"
				})
				registry.ResetDefaultProvider()
			},
			expectedType:   RegistryTypeFile,
			expectedSource: "/path/to/registry.json",
			description:    "Local file registry should return type 'file' and the file path as source",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupConfig != nil {
				tt.setupConfig()
			}

			router := RegistryRouter(nil)

			req := httptest.NewRequest("GET", "/default", nil)

			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("name", "default")
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if tt.expectedType == RegistryTypeDefault {
				assert.Equal(t, http.StatusOK, w.Code, "GET /registry/default should succeed for built-in registry")

				var response getRegistryResponse
				err := json.NewDecoder(w.Body).Decode(&response)
				require.NoError(t, err, "Response should be valid JSON")

				assert.Equal(t, tt.expectedType, response.Type, "Registry type should match expected")
				assert.Equal(t, tt.expectedSource, response.Source, "Registry source should match expected")
				assert.Equal(t, "default", response.Name, "Registry name should be 'default'")
				assert.NotEmpty(t, response.Version, "Registry should have a version")
				assert.NotNil(t, response.Registry, "Registry data should be present")
			}

			listReq := httptest.NewRequest("GET", "/", nil)
			listW := httptest.NewRecorder()
			router.ServeHTTP(listW, listReq)

			if tt.expectedType == RegistryTypeDefault {
				assert.Equal(t, http.StatusOK, listW.Code, "GET /registry should succeed for built-in registry")

				var listResponse registryListResponse
				err := json.NewDecoder(listW.Body).Decode(&listResponse)
				require.NoError(t, err, "List response should be valid JSON")

				require.Len(t, listResponse.Registries, 1, "Should have exactly one registry")

				registry := listResponse.Registries[0]
				assert.Equal(t, tt.expectedType, registry.Type, "Registry type should match expected in list")
				assert.Equal(t, tt.expectedSource, registry.Source, "Registry source should match expected in list")
				assert.Equal(t, "default", registry.Name, "Registry name should be 'default'")
			}
		})
	}

	config.UnsetRegistry()
	registry.ResetDefaultProvider()
}

func TestRegistryAPI_CacheInvalidation(t *testing.T) {
	logger.Initialize()

	t.Run("cache invalidation after PUT", func(t *testing.T) {
		config.UnsetRegistry()
		registry.ResetDefaultProvider()

		router := RegistryRouter(nil)

		req1 := httptest.NewRequest("GET", "/default", nil)
		rctx1 := chi.NewRouteContext()
		rctx1.URLParams.Add("name", "default")
		req1 = req1.WithContext(context.WithValue(req1.Context(), chi.RouteCtxKey, rctx1))

		w1 := httptest.NewRecorder()
		router.ServeHTTP(w1, req1)

		assert.Equal(t, http.StatusOK, w1.Code, "Initial GET should succeed")

		var response1 getRegistryResponse
		err := json.NewDecoder(w1.Body).Decode(&response1)
		require.NoError(t, err)

		assert.Equal(t, RegistryTypeDefault, response1.Type, "Initially should be default registry")
		assert.Equal(t, "", response1.Source, "Initially source should be empty")

		putBody := `{"url":"https://example.com/test-registry.json"}`
		req2 := httptest.NewRequest("PUT", "/default", strings.NewReader(putBody))
		req2.Header.Set("Content-Type", "application/json")
		rctx2 := chi.NewRouteContext()
		rctx2.URLParams.Add("name", "default")
		req2 = req2.WithContext(context.WithValue(req2.Context(), chi.RouteCtxKey, rctx2))

		w2 := httptest.NewRecorder()
		router.ServeHTTP(w2, req2)

		assert.Equal(t, http.StatusOK, w2.Code, "PUT should succeed")

		req3 := httptest.NewRequest("GET", "/default", nil)
		rctx3 := chi.NewRouteContext()
		rctx3.URLParams.Add("name", "default")
		req3 = req3.WithContext(context.WithValue(req3.Context(), chi.RouteCtxKey, rctx3))

		w3 := httptest.NewRecorder()
		router.ServeHTTP(w3, req3)

		cfg, err := config.LoadOrCreateConfig()
		require.NoError(t, err)
		assert.Equal(t, "https://example.com/test-registry.json", cfg.RegistryUrl, "Config should be updated")

		registryType, source := getRegistryInfo()
		assert.Equal(t, RegistryTypeURL, registryType, "Should now detect URL registry type")
		assert.Equal(t, "https://example.com/test-registry.json", source, "Should return the URL as source")

		config.UnsetRegistry()
		registry.ResetDefaultProvider()
	})
}

func TestGetRegistryInfo(t *testing.T) {
	logger.Initialize()

	tests := []struct {
		name           string
		setupConfig    func()
		expectedType   RegistryType
		expectedSource string
	}{
		{
			name: "default registry",
			setupConfig: func() {
				config.UnsetRegistry()
			},
			expectedType:   RegistryTypeDefault,
			expectedSource: "",
		},
		{
			name: "URL registry",
			setupConfig: func() {
				config.SetRegistryURL("https://test.com/registry.json", false)
			},
			expectedType:   RegistryTypeURL,
			expectedSource: "https://test.com/registry.json",
		},
		{
			name: "file registry",
			setupConfig: func() {
				config.UnsetRegistry()
				config.UpdateConfig(func(c *config.Config) {
					c.LocalRegistryPath = "/tmp/test-registry.json"
				})
			},
			expectedType:   RegistryTypeFile,
			expectedSource: "/tmp/test-registry.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupConfig != nil {
				tt.setupConfig()
			}

			registryType, source := getRegistryInfo()
			assert.Equal(t, tt.expectedType, registryType, "Registry type should match expected")
			assert.Equal(t, tt.expectedSource, source, "Registry source should match expected")
		})
	}

	config.UnsetRegistry()
}

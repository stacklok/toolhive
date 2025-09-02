package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/logger"
)

func CreateTestConfigProvider(t *testing.T, cfg *config.Config) (config.Provider, func()) {
	t.Helper()

	// Create a temporary directory for the test
	tempDir := t.TempDir()

	// Create the config directory structure
	configDir := filepath.Join(tempDir, "toolhive")
	err := os.MkdirAll(configDir, 0755)
	require.NoError(t, err)

	// Set up the config file path
	configPath := filepath.Join(configDir, "config.yaml")

	// Create a path-based config provider
	provider := config.NewPathProvider(configPath)

	// Write the config file if one is provided
	if cfg != nil {
		err = provider.UpdateConfig(func(c *config.Config) { *c = *cfg })
		require.NoError(t, err)
	}

	return provider, func() {
		// Cleanup is handled by t.TempDir()
	}
}

func TestRegistryRouter(t *testing.T) {
	t.Parallel()

	logger.Initialize()

	router := RegistryRouter()
	assert.NotNil(t, router)
}

//nolint:paralleltest // Cannot use t.Parallel() with t.Setenv() in Go 1.24+
func TestGetRegistryInfo(t *testing.T) {
	t.Parallel()
	logger.Initialize()

	tests := []struct {
		name           string
		config         *config.Config
		expectedType   RegistryType
		expectedSource string
	}{
		{
			name: "default registry",
			config: &config.Config{
				RegistryUrl:       "",
				LocalRegistryPath: "",
			},
			expectedType:   RegistryTypeDefault,
			expectedSource: "",
		},
		{
			name: "URL registry",
			config: &config.Config{
				RegistryUrl:            "https://test.com/registry.json",
				AllowPrivateRegistryIp: false,
				LocalRegistryPath:      "",
			},
			expectedType:   RegistryTypeURL,
			expectedSource: "https://test.com/registry.json",
		},
		{
			name: "file registry",
			config: &config.Config{
				RegistryUrl:       "",
				LocalRegistryPath: "/tmp/test-registry.json",
			},
			expectedType:   RegistryTypeFile,
			expectedSource: "/tmp/test-registry.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			configProvider, cleanup := CreateTestConfigProvider(t, tt.config)
			defer cleanup()

			registryType, source := getRegistryInfoWithProvider(configProvider)
			assert.Equal(t, tt.expectedType, registryType, "Registry type should match expected")
			assert.Equal(t, tt.expectedSource, source, "Registry source should match expected")
		})
	}
}

func TestRegistryAPI_PutEndpoint(t *testing.T) {
	t.Parallel()

	logger.Initialize()

	routes := &RegistryRoutes{}

	tests := []struct {
		name         string
		requestBody  string
		expectedCode int
		description  string
	}{
		{
			name:         "valid URL registry",
			requestBody:  `{"url":"https://example.com/test-registry.json"}`,
			expectedCode: http.StatusOK,
			description:  "Valid HTTPS URL should be accepted",
		},
		{
			name:         "invalid local path registry",
			requestBody:  `{"local_path":"/tmp/test-registry.json"}`,
			expectedCode: http.StatusBadRequest,
			description:  "Non-existent local file should return 400",
		},
		{
			name:         "invalid JSON",
			requestBody:  `{"invalid":json}`,
			expectedCode: http.StatusBadRequest,
			description:  "Invalid JSON should return 400",
		},
		{
			name:         "empty body",
			requestBody:  `{}`,
			expectedCode: http.StatusOK,
			description:  "Empty request resets registry (returns 200)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("PUT", "/default", strings.NewReader(tt.requestBody))
			req.Header.Set("Content-Type", "application/json")
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("name", "default")
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

			w := httptest.NewRecorder()
			routes.updateRegistry(w, req)

			assert.Equal(t, tt.expectedCode, w.Code, tt.description)

			if w.Code == http.StatusOK {
				var response map[string]interface{}
				err := json.NewDecoder(w.Body).Decode(&response)
				require.NoError(t, err, "Success response should be valid JSON")
				assert.Contains(t, response, "message", "Success response should contain a message")
			}
		})
	}
}

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

	"github.com/adrg/xdg"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/config"
	log "github.com/stacklok/toolhive/pkg/logger"
)

func MockConfig(t *testing.T, cfg *config.Config) func() {
	t.Helper()

	// Setup logger
	logger := log.NewLogger()

	// Create a temporary directory for the test
	tempDir := t.TempDir()

	// TODO: see if there's a way to avoid changing env vars during tests.
	// Save original XDG_CONFIG_HOME
	originalXDGConfigHome := os.Getenv("XDG_CONFIG_HOME")
	t.Setenv("XDG_CONFIG_HOME", tempDir)
	xdg.Reload()

	// Create the config directory structure
	configDir := filepath.Join(tempDir, "toolhive")
	err := os.MkdirAll(configDir, 0755)
	require.NoError(t, err)

	// Write the config file if one is provided
	if cfg != nil {
		err = config.UpdateConfig(func(c *config.Config) { *c = *cfg }, logger)
		require.NoError(t, err)
	}

	return func() {
		t.Setenv("XDG_CONFIG_HOME", originalXDGConfigHome)
	}
}

func TestRegistryRouter(t *testing.T) {
	t.Parallel()

	logger := log.NewLogger()

	router := RegistryRouter(logger)
	assert.NotNil(t, router)
}

//nolint:paralleltest // Cannot use t.Parallel() with t.Setenv() in Go 1.24+
func TestGetRegistryInfo(t *testing.T) {
	logger := log.NewLogger()

	// Setup temporary config to avoid modifying user's real config
	cleanup := MockConfig(t, nil)
	t.Cleanup(cleanup)

	tests := []struct {
		name           string
		setupConfig    func()
		expectedType   RegistryType
		expectedSource string
	}{
		{
			name: "default registry",
			setupConfig: func() {
				_ = config.UnsetRegistry(logger)
			},
			expectedType:   RegistryTypeDefault,
			expectedSource: "",
		},
		{
			name: "URL registry",
			setupConfig: func() {
				_ = config.SetRegistryURL("https://test.com/registry.json", false, logger)
			},
			expectedType:   RegistryTypeURL,
			expectedSource: "https://test.com/registry.json",
		},
		{
			name: "file registry",
			setupConfig: func() {
				_ = config.UnsetRegistry(logger)
				_ = config.UpdateConfig(func(c *config.Config) {
					c.LocalRegistryPath = "/tmp/test-registry.json"
				}, logger)
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

			registryType, source := getRegistryInfo(logger)
			assert.Equal(t, tt.expectedType, registryType, "Registry type should match expected")
			assert.Equal(t, tt.expectedSource, source, "Registry source should match expected")
		})
	}
}

func TestRegistryAPI_PutEndpoint(t *testing.T) {
	t.Parallel()

	logger := log.NewLogger()

	routes := &RegistryRoutes{logger}

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

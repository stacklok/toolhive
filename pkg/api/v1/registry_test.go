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
	"github.com/stacklok/toolhive/pkg/logger"
)

func MockConfig(t *testing.T, cfg *config.Config) func() {
	t.Helper()

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
		err = config.UpdateConfig(func(c *config.Config) { *c = *cfg })
		require.NoError(t, err)
	}

	return func() {
		t.Setenv("XDG_CONFIG_HOME", originalXDGConfigHome)
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
	logger.Initialize()

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
				_ = config.UnsetRegistry()
			},
			expectedType:   RegistryTypeDefault,
			expectedSource: "",
		},
		{
			name: "URL registry",
			setupConfig: func() {
				_ = config.SetRegistryURL("https://test.com/registry.json", false)
			},
			expectedType:   RegistryTypeURL,
			expectedSource: "https://test.com/registry.json",
		},
		{
			name: "file registry",
			setupConfig: func() {
				_ = config.UnsetRegistry()
				_ = config.UpdateConfig(func(c *config.Config) {
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
}

//nolint:paralleltest // uses MockConfig (env mutation)
func TestSetRegistryURL_SchemeAndPrivateIPs(t *testing.T) {
	logger.Initialize()

	// Isolate config (XDG) for each run
	cleanup := MockConfig(t, nil)
	t.Cleanup(cleanup)

	tests := []struct {
		name            string
		url             string
		allowPrivateIPs bool
		wantErr         bool
	}{
		// Scheme enforcement when NOT allowing private IPs
		{
			name:            "reject http (public) when not allowing private IPs",
			url:             "http://example.com/registry.json",
			allowPrivateIPs: false,
			wantErr:         true,
		},
		{
			name:            "accept https (public) when not allowing private IPs",
			url:             "https://example.com/registry.json",
			allowPrivateIPs: false,
			wantErr:         false,
		},

		// When allowing private IPs, https is allowed to private hosts
		{
			name:            "accept https to loopback when allowing private IPs",
			url:             "https://127.0.0.1/registry.json",
			allowPrivateIPs: true,
			wantErr:         false,
		},
		{
			name:            "accept http to loopback when allowing private IPs",
			url:             "http://127.0.0.1/registry.json",
			allowPrivateIPs: true,
			wantErr:         false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			// Start from a clean slate each case
			require.NoError(t, config.UnsetRegistry())

			err := config.SetRegistryURL(tt.url, tt.allowPrivateIPs)
			if tt.wantErr {
				require.Error(t, err, "expected error but got nil")

				// Verify nothing was persisted
				regType, src := getRegistryInfo()
				assert.Equal(t, RegistryTypeDefault, regType, "registry should remain default on error")
				assert.Equal(t, "", src, "source should be empty on error")
				return
			}

			require.NoError(t, err, "unexpected error from SetRegistryURL")

			// Confirm via the same helper used elsewhere
			regType, src := getRegistryInfo()
			assert.Equal(t, RegistryTypeURL, regType, "should be URL type after successful SetRegistryURL")
			assert.Equal(t, tt.url, src, "source should be the URL we set")
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

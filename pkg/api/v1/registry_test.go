// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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

	// Create a test config provider to avoid using the singleton
	provider, _ := CreateTestConfigProvider(t, nil)
	routes := NewRegistryRoutesWithProvider(provider)
	assert.NotNil(t, routes)
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

			service := config.NewRegistryConfigServiceWithProvider(configProvider)
			registryType, source := service.GetRegistryInfo()
			assert.Equal(t, string(tt.expectedType), registryType, "Registry type should match expected")
			assert.Equal(t, tt.expectedSource, source, "Registry source should match expected")
		})
	}
}

//nolint:paralleltest,tparallel // Subtests cannot run in parallel as they share a mock HTTP server
func TestRegistryAPI_PutEndpoint(t *testing.T) {
	t.Parallel()

	logger.Initialize()

	// Create a mock HTTP server that serves valid registry JSON
	validRegistryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		registryData := map[string]interface{}{
			"servers": map[string]interface{}{
				"test-server": map[string]interface{}{
					"command": []string{"test"},
					"args":    []string{},
				},
			},
		}
		if err := json.NewEncoder(w).Encode(registryData); err != nil {
			t.Logf("Failed to encode registry data: %v", err)
		}
	}))
	defer validRegistryServer.Close()

	tests := []struct {
		name         string
		setupFunc    func(t *testing.T) string // Returns the request body
		expectedCode int
		description  string
	}{
		{
			name: "valid URL registry",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				// Use the mock server URL with allow_private_ip to enable HTTP for localhost
				return `{"url":"` + validRegistryServer.URL + `","allow_private_ip":true}`
			},
			expectedCode: http.StatusOK,
			description:  "Valid URL with actual registry data should be accepted",
		},
		{
			name: "valid local file registry",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				// Create a temporary file with valid registry JSON
				tempFile := filepath.Join(t.TempDir(), "valid-registry.json")
				validJSON := `{"servers": {"test-server": {"command": ["test"], "args": []}}}`
				err := os.WriteFile(tempFile, []byte(validJSON), 0600)
				require.NoError(t, err)
				return `{"local_path":"` + tempFile + `"}`
			},
			expectedCode: http.StatusOK,
			description:  "Valid local file with proper registry structure should be accepted",
		},
		{
			name: "invalid local file - non-existent",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				return `{"local_path":"/tmp/non-existent-registry-file-12345.json"}`
			},
			expectedCode: http.StatusBadRequest,
			description:  "Non-existent local file should return 400",
		},
		{
			name: "invalid local file - wrong structure",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				// Create a file with invalid registry structure
				tempFile := filepath.Join(t.TempDir(), "invalid-registry.json")
				invalidJSON := `{"test": "registry"}`
				err := os.WriteFile(tempFile, []byte(invalidJSON), 0600)
				require.NoError(t, err)
				return `{"local_path":"` + tempFile + `"}`
			},
			expectedCode: http.StatusBadRequest,
			description:  "Local file with invalid registry structure should return 400",
		},
		{
			name: "invalid URL - unreachable",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				return `{"url":"https://invalid-url-that-does-not-exist-12345.example.com/test.json"}`
			},
			expectedCode: http.StatusGatewayTimeout,
			description:  "Unreachable URL should return 504 Gateway Timeout",
		},
		{
			name: "invalid JSON",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				return `{"invalid":json}`
			},
			expectedCode: http.StatusBadRequest,
			description:  "Invalid JSON should return 400",
		},
		{
			name: "empty body",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				return `{}`
			},
			expectedCode: http.StatusOK,
			description:  "Empty request resets registry (returns 200)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Note: Not using t.Parallel() here because subtests share the mock server

			// Create a temporary config for this test
			tempDir := t.TempDir()
			configPath := filepath.Join(tempDir, "toolhive", "config.yaml")

			// Ensure the directory exists
			err := os.MkdirAll(filepath.Dir(configPath), 0755)
			require.NoError(t, err)

			// Create a test config provider
			configProvider := config.NewPathProvider(configPath)

			// Create routes with the test config provider
			routes := NewRegistryRoutesWithProvider(configProvider)

			// Get the request body from the setup function
			requestBody := tt.setupFunc(t)

			req := httptest.NewRequest("PUT", "/default", strings.NewReader(requestBody))
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

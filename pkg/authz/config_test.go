// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
)

// mustNewConfig creates a new Config from a cedar.Config or fails the test.
func mustNewConfig(t *testing.T, fullConfig interface{}) *Config {
	t.Helper()
	config, err := NewConfig(fullConfig)
	require.NoError(t, err, "Failed to create config")
	return config
}

func TestLoadConfig(t *testing.T) {
	t.Parallel()
	// Create a temporary file with a valid configuration
	tempFile, err := os.CreateTemp("", "authz-config-*.json")
	require.NoError(t, err, "Failed to create temporary file")
	defer os.Remove(tempFile.Name())

	// Create a valid configuration using the v1.0 schema
	cedarConfig := cedar.Config{
		Version: "1.0",
		Type:    cedar.ConfigType,
		Options: &cedar.ConfigOptions{
			Policies:     []string{`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`},
			EntitiesJSON: "[]",
		},
	}
	config := mustNewConfig(t, cedarConfig)

	// Marshal the configuration to JSON
	configJSON, err := json.MarshalIndent(config, "", "  ")
	require.NoError(t, err, "Failed to marshal configuration to JSON")

	// Write the configuration to the temporary file
	_, err = tempFile.Write(configJSON)
	require.NoError(t, err, "Failed to write configuration to temporary file")
	tempFile.Close()

	// Load the configuration from the temporary file
	loadedConfig, err := LoadConfig(tempFile.Name())
	require.NoError(t, err, "Failed to load configuration from file")

	// Check if the loaded configuration matches the original configuration
	assert.Equal(t, config.Version, loadedConfig.Version, "Version does not match")
	assert.Equal(t, config.Type, loadedConfig.Type, "Type does not match")

	// Verify the raw config can be parsed back to the cedar config structure
	var loadedCedarConfig cedar.Config
	err = json.Unmarshal(loadedConfig.RawConfig(), &loadedCedarConfig)
	require.NoError(t, err, "Failed to unmarshal loaded config")
	require.NotNil(t, loadedCedarConfig.Options, "Cedar config should not be nil")
	assert.Equal(t, cedarConfig.Options.Policies, loadedCedarConfig.Options.Policies, "Policies do not match")
	assert.Equal(t, cedarConfig.Options.EntitiesJSON, loadedCedarConfig.Options.EntitiesJSON, "EntitiesJSON does not match")
}

func TestLoadConfigLegacyFormat(t *testing.T) {
	t.Parallel()
	// Create a temporary file with a legacy configuration format (v1.0 schema)
	tempFile, err := os.CreateTemp("", "authz-config-legacy-*.json")
	require.NoError(t, err, "Failed to create temporary file")
	defer os.Remove(tempFile.Name())

	// Create a v1.0 configuration with "cedar" field - this IS the supported format
	legacyConfig := map[string]interface{}{
		"version": "1.0",
		"type":    "cedarv1",
		"cedar": map[string]interface{}{
			"policies":      []string{`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`},
			"entities_json": "[]",
		},
	}

	// Marshal the configuration to JSON
	configJSON, err := json.MarshalIndent(legacyConfig, "", "  ")
	require.NoError(t, err, "Failed to marshal configuration to JSON")

	// Write the configuration to the temporary file
	_, err = tempFile.Write(configJSON)
	require.NoError(t, err, "Failed to write configuration to temporary file")
	tempFile.Close()

	// Load the configuration from the temporary file
	loadedConfig, err := LoadConfig(tempFile.Name())
	require.NoError(t, err, "Failed to load configuration from file")

	// Check if the loaded configuration has the expected values
	assert.Equal(t, "1.0", loadedConfig.Version, "Version does not match")
	assert.Equal(t, ConfigType("cedarv1"), loadedConfig.Type, "Type does not match")

	// Verify the raw config can be parsed with Cedar's config
	var loadedCedarConfig cedar.Config
	err = json.Unmarshal(loadedConfig.RawConfig(), &loadedCedarConfig)
	require.NoError(t, err, "Failed to unmarshal loaded config")
	require.NotNil(t, loadedCedarConfig.Options, "Cedar config should not be nil")
	assert.Equal(t, []string{`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`}, loadedCedarConfig.Options.Policies)
}

func TestLoadConfigPathTraversal(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		path        string
		expectError bool
	}{
		{
			name:        "Directory traversal with ../",
			path:        "../../../etc/passwd",
			expectError: true,
		},
		{
			name:        "Directory traversal with ./",
			path:        "./../../../etc/passwd",
			expectError: true,
		},
		{
			name:        "Multiple directory traversals",
			path:        "../../../../../../etc/passwd",
			expectError: true,
		},
		{
			name:        "Valid relative path",
			path:        "config.json",
			expectError: false, // Will fail because file doesn't exist, not path traversal
		},
		{
			name:        "Valid absolute path",
			path:        "/tmp/config.json",
			expectError: false, // Will fail because file doesn't exist, not path traversal
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := LoadConfig(tc.path)
			if tc.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "directory traversal elements")
			} else {
				// For valid paths, we expect a "no such file or directory" error, not a traversal error
				if err != nil {
					assert.NotContains(t, err.Error(), "directory traversal elements")
				}
			}
		})
	}
}

func TestValidateConfig(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name        string
		config      *Config
		expectError bool
	}{
		{
			name: "Valid configuration",
			config: mustNewConfig(t, cedar.Config{
				Version: "1.0",
				Type:    cedar.ConfigType,
				Options: &cedar.ConfigOptions{
					Policies:     []string{`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`},
					EntitiesJSON: "[]",
				},
			}),
			expectError: false,
		},
		{
			name: "Missing version",
			config: mustNewConfig(t, cedar.Config{
				Type: cedar.ConfigType,
				Options: &cedar.ConfigOptions{
					Policies:     []string{`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`},
					EntitiesJSON: "[]",
				},
			}),
			expectError: true,
		},
		{
			name: "Missing type",
			config: mustNewConfig(t, cedar.Config{
				Version: "1.0",
				Options: &cedar.ConfigOptions{
					Policies:     []string{`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`},
					EntitiesJSON: "[]",
				},
			}),
			expectError: true,
		},
		{
			name: "Unsupported type",
			config: mustNewConfig(t, map[string]interface{}{
				"version": "1.0",
				"type":    "unsupported",
			}),
			expectError: true,
		},
		{
			name: "Missing Cedar configuration",
			config: mustNewConfig(t, map[string]interface{}{
				"version": "1.0",
				"type":    cedar.ConfigType,
				// No "cedar" field
			}),
			expectError: true,
		},
		{
			name: "Empty policies",
			config: mustNewConfig(t, cedar.Config{
				Version: "1.0",
				Type:    cedar.ConfigType,
				Options: &cedar.ConfigOptions{
					Policies:     []string{},
					EntitiesJSON: "[]",
				},
			}),
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.config.Validate()
			if tc.expectError {
				assert.Error(t, err, "Expected an error but got none")
			} else {
				assert.NoError(t, err, "Expected no error but got one")
			}
		})
	}
}

func TestCreateMiddleware(t *testing.T) {
	t.Parallel()
	// Create a valid configuration using the v1.0 schema
	config := mustNewConfig(t, cedar.Config{
		Version: "1.0",
		Type:    cedar.ConfigType,
		Options: &cedar.ConfigOptions{
			Policies:     []string{`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`},
			EntitiesJSON: "[]",
		},
	})

	// Create the middleware
	middleware, err := CreateMiddlewareFromConfig(config, "testmodule")
	require.NoError(t, err, "Failed to create middleware")
	require.NotNil(t, middleware, "Middleware is nil")

	// Create a test handler
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Apply the middleware chain: MCP parsing first, then authorization
	handler := mcpparser.ParsingMiddleware(middleware(testHandler))
	require.NotNil(t, handler, "Handler is nil")

	// Create a test request with a valid JSON-RPC message
	jsonRPCMessage := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "ping",
		"params":  map[string]interface{}{},
	}
	jsonRPCMessageBytes, err := json.Marshal(jsonRPCMessage)
	require.NoError(t, err, "Failed to marshal JSON-RPC message")

	req, err := http.NewRequest(http.MethodPost, "/messages", bytes.NewBuffer(jsonRPCMessageBytes))
	require.NoError(t, err, "Failed to create request")
	req.Header.Set("Content-Type", "application/json")

	// Add JWT claims to the request context
	claims := map[string]interface{}{
		"sub":  "user123",
		"name": "John Doe",
	}
	identity := &auth.Identity{Subject: "user123", Claims: claims}
	ctx := auth.WithIdentity(req.Context(), identity)
	req = req.WithContext(ctx)

	// Create a response recorder
	rr := httptest.NewRecorder()

	// Serve the request
	handler.ServeHTTP(rr, req)

	// Check the response
	assert.Equal(t, http.StatusOK, rr.Code, "Response status code does not match expected")
}

func TestNewConfig(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		fullConfig   interface{}
		expectError  bool
		expectedType ConfigType
	}{
		{
			name: "Valid Cedar config",
			fullConfig: cedar.Config{
				Version: "1.0",
				Type:    cedar.ConfigType,
				Options: &cedar.ConfigOptions{
					Policies:     []string{`permit(principal, action, resource);`},
					EntitiesJSON: "[]",
				},
			},
			expectError:  false,
			expectedType: ConfigType(cedar.ConfigType),
		},
		{
			name: "Config as map",
			fullConfig: map[string]interface{}{
				"version": "1.0",
				"type":    cedar.ConfigType,
				"cedar": map[string]interface{}{
					"policies":      []string{`permit(principal, action, resource);`},
					"entities_json": "[]",
				},
			},
			expectError:  false,
			expectedType: ConfigType(cedar.ConfigType),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			config, err := NewConfig(tc.fullConfig)
			if tc.expectError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.expectedType, config.Type)
			assert.NotEmpty(t, config.RawConfig())
		})
	}
}

func TestGetMiddlewareFromFile(t *testing.T) {
	t.Parallel()

	t.Run("Valid config file", func(t *testing.T) {
		t.Parallel()

		// Create a temporary file with a valid configuration
		tempFile, err := os.CreateTemp("", "authz-middleware-*.json")
		require.NoError(t, err)
		defer os.Remove(tempFile.Name())

		// Create a valid configuration using the v1.0 schema
		cedarConfig := cedar.Config{
			Version: "1.0",
			Type:    cedar.ConfigType,
			Options: &cedar.ConfigOptions{
				Policies:     []string{`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`},
				EntitiesJSON: "[]",
			},
		}
		config := mustNewConfig(t, cedarConfig)

		// Marshal and write the configuration
		configJSON, err := json.Marshal(config)
		require.NoError(t, err)
		_, err = tempFile.Write(configJSON)
		require.NoError(t, err)
		tempFile.Close()

		// Get middleware from file
		middleware, err := GetMiddlewareFromFile("testserver", tempFile.Name())
		require.NoError(t, err)
		require.NotNil(t, middleware)
	})

	t.Run("Non-existent file", func(t *testing.T) {
		t.Parallel()

		_, err := GetMiddlewareFromFile("testserver", "/nonexistent/path/config.json")
		assert.Error(t, err)
	})

	t.Run("Invalid config file", func(t *testing.T) {
		t.Parallel()

		// Create a temporary file with invalid configuration
		tempFile, err := os.CreateTemp("", "authz-middleware-invalid-*.json")
		require.NoError(t, err)
		defer os.Remove(tempFile.Name())

		// Write invalid JSON
		_, err = tempFile.WriteString(`{"invalid": "config"}`)
		require.NoError(t, err)
		tempFile.Close()

		// Get middleware from file should fail
		_, err = GetMiddlewareFromFile("testserver", tempFile.Name())
		assert.Error(t, err)
	})
}

func TestCreateMiddlewareFromConfigErrors(t *testing.T) {
	t.Parallel()

	t.Run("Unsupported config type", func(t *testing.T) {
		t.Parallel()

		config := &Config{
			Version: "1.0",
			Type:    "unsupported-type",
		}

		_, err := CreateMiddlewareFromConfig(config, "testserver")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported configuration type")
	})
}

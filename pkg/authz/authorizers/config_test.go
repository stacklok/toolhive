// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authorizers

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testConfigType is a test configuration type registered for testing
const testConfigType = "test-config-type"

// testFactory is a simple test factory for config tests
type testFactory struct{}

func (*testFactory) ValidateConfig(rawConfig json.RawMessage) error {
	var config struct {
		TestField string `json:"test_field"`
	}
	return json.Unmarshal(rawConfig, &config)
}

func (*testFactory) CreateAuthorizer(_ json.RawMessage, _ string) (Authorizer, error) {
	return &testAuthorizer{}, nil
}

type testAuthorizer struct{}

func (*testAuthorizer) AuthorizeWithJWTClaims(
	_ context.Context,
	_ MCPFeature,
	_ MCPOperation,
	_ string,
	_ map[string]interface{},
) (bool, error) {
	return true, nil
}

func init() {
	// Register a test factory type for config tests
	if !IsRegistered(testConfigType) {
		Register(testConfigType, &testFactory{})
	}
}

func TestConfigUnmarshalJSON(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name            string
		input           string
		expectedVersion string
		expectedType    ConfigType
		expectError     bool
	}{
		{
			name:            "Valid configuration",
			input:           `{"version": "1.0", "type": "test-config-type", "test_field": "value"}`,
			expectedVersion: "1.0",
			expectedType:    testConfigType,
			expectError:     false,
		},
		{
			name:            "Minimal configuration",
			input:           `{"version": "2.0", "type": "customtype"}`,
			expectedVersion: "2.0",
			expectedType:    "customtype",
			expectError:     false,
		},
		{
			name:        "Invalid JSON",
			input:       `{"version": "1.0", "type":`,
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var config Config
			err := json.Unmarshal([]byte(tc.input), &config)

			if tc.expectError {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.expectedVersion, config.Version)
			assert.Equal(t, tc.expectedType, config.Type)
			// Verify raw config is preserved
			assert.NotEmpty(t, config.rawConfig)
		})
	}
}

func TestConfigMarshalJSON(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		config      Config
		expectError bool
	}{
		{
			name: "Config with raw config",
			config: Config{
				Version:   "1.0",
				Type:      testConfigType,
				rawConfig: json.RawMessage(`{"version":"1.0","type":"test-config-type","test_field":"value"}`),
			},
			expectError: false,
		},
		{
			name: "Config without raw config (fallback)",
			config: Config{
				Version: "1.0",
				Type:    testConfigType,
			},
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			data, err := json.Marshal(&tc.config)

			if tc.expectError {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.NotEmpty(t, data)

			// Verify we can unmarshal the result
			var result map[string]interface{}
			err = json.Unmarshal(data, &result)
			require.NoError(t, err)
			assert.Equal(t, tc.config.Version, result["version"])
			assert.Equal(t, string(tc.config.Type), result["type"])
		})
	}
}

func TestConfigRawConfig(t *testing.T) {
	t.Parallel()

	rawData := json.RawMessage(`{"version":"1.0","type":"test-config-type"}`)
	config := Config{
		Version:   "1.0",
		Type:      testConfigType,
		rawConfig: rawData,
	}

	assert.Equal(t, rawData, config.RawConfig())
}

func TestLoadConfig(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		filename    string
		content     string
		expectError bool
		errorMsg    string
	}{
		{
			name:     "Valid JSON config",
			filename: "config.json",
			content:  `{"version": "1.0", "type": "test-config-type", "test_field": "value"}`,
		},
		{
			name:     "Valid YAML config",
			filename: "config.yaml",
			content: `version: "1.0"
type: test-config-type
test_field: value`,
		},
		{
			name:     "Valid YML config",
			filename: "config.yml",
			content: `version: "1.0"
type: test-config-type
test_field: value`,
		},
		{
			name:        "Invalid JSON",
			filename:    "invalid.json",
			content:     `{"version": "1.0"`,
			expectError: true,
			errorMsg:    "failed to parse JSON",
		},
		{
			name:        "Invalid YAML",
			filename:    "invalid.yaml",
			content:     "version: [invalid",
			expectError: true,
			errorMsg:    "failed to parse YAML",
		},
		{
			name:        "Unsupported extension",
			filename:    "config.txt",
			content:     `{"version": "1.0", "type": "test-config-type"}`,
			expectError: true,
			errorMsg:    "unsupported file format",
		},
		{
			name:        "Missing version",
			filename:    "missing_version.json",
			content:     `{"type": "test-config-type", "test_field": "value"}`,
			expectError: true,
			errorMsg:    "version is required",
		},
		{
			name:        "Missing type",
			filename:    "missing_type.json",
			content:     `{"version": "1.0", "test_field": "value"}`,
			expectError: true,
			errorMsg:    "type is required",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Create temp directory
			tmpDir, err := os.MkdirTemp("", "authz-config-test")
			require.NoError(t, err)
			defer os.RemoveAll(tmpDir)

			// Write test file
			filePath := filepath.Join(tmpDir, tc.filename)
			err = os.WriteFile(filePath, []byte(tc.content), 0600)
			require.NoError(t, err)

			// Load config
			config, err := LoadConfig(filePath)

			if tc.expectError {
				assert.Error(t, err)
				if tc.errorMsg != "" {
					assert.Contains(t, err.Error(), tc.errorMsg)
				}
				return
			}

			require.NoError(t, err)
			require.NotNil(t, config)
			assert.Equal(t, "1.0", config.Version)
			assert.Equal(t, ConfigType(testConfigType), config.Type)
		})
	}
}

func TestLoadConfigPathTraversal(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		path        string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "Directory traversal",
			path:        "../../../etc/passwd",
			expectError: true,
			errorMsg:    "directory traversal",
		},
		{
			name:        "Multiple traversals",
			path:        "../../../../../../etc/passwd",
			expectError: true,
			errorMsg:    "directory traversal",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := LoadConfig(tc.path)

			if tc.expectError {
				assert.Error(t, err)
				if tc.errorMsg != "" {
					assert.Contains(t, err.Error(), tc.errorMsg)
				}
			}
		})
	}
}

func TestLoadConfigNonExistentFile(t *testing.T) {
	t.Parallel()

	_, err := LoadConfig("/nonexistent/path/config.json")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read authorization configuration file")
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		config      Config
		expectError bool
		errorMsg    string
	}{
		{
			name: "Missing version",
			config: Config{
				Type:      testConfigType,
				rawConfig: json.RawMessage(`{"type":"test-config-type"}`),
			},
			expectError: true,
			errorMsg:    "version is required",
		},
		{
			name: "Missing type",
			config: Config{
				Version:   "1.0",
				rawConfig: json.RawMessage(`{"version":"1.0"}`),
			},
			expectError: true,
			errorMsg:    "type is required",
		},
		{
			name: "Unsupported type",
			config: Config{
				Version:   "1.0",
				Type:      "unsupported",
				rawConfig: json.RawMessage(`{"version":"1.0","type":"unsupported"}`),
			},
			expectError: true,
			errorMsg:    "unsupported configuration type",
		},
		{
			name: "Missing raw config",
			config: Config{
				Version: "1.0",
				Type:    testConfigType,
				// No rawConfig
			},
			expectError: true,
			errorMsg:    "configuration data is required",
		},
		{
			name: "Valid config",
			config: Config{
				Version:   "1.0",
				Type:      testConfigType,
				rawConfig: json.RawMessage(`{"version":"1.0","type":"test-config-type","test_field":"value"}`),
			},
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.config.Validate()

			if tc.expectError {
				assert.Error(t, err)
				if tc.errorMsg != "" {
					assert.Contains(t, err.Error(), tc.errorMsg)
				}
				return
			}

			assert.NoError(t, err)
		})
	}
}

func TestNewConfig(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name            string
		fullConfig      interface{}
		expectError     bool
		expectedVersion string
		expectedType    ConfigType
	}{
		{
			name: "Map config",
			fullConfig: map[string]interface{}{
				"version":    "1.0",
				"type":       testConfigType,
				"test_field": "value",
			},
			expectedVersion: "1.0",
			expectedType:    testConfigType,
		},
		{
			name: "Struct config",
			fullConfig: struct {
				Version string `json:"version"`
				Type    string `json:"type"`
			}{
				Version: "2.0",
				Type:    "testtype",
			},
			expectedVersion: "2.0",
			expectedType:    "testtype",
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
			require.NotNil(t, config)
			assert.Equal(t, tc.expectedVersion, config.Version)
			assert.Equal(t, tc.expectedType, config.Type)
			assert.NotEmpty(t, config.RawConfig())
		})
	}
}

func TestNewConfigWithInvalidInput(t *testing.T) {
	t.Parallel()

	// Test with something that can't be marshaled to JSON
	// Using a channel, which cannot be marshaled
	_, err := NewConfig(make(chan int))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to marshal configuration")
}

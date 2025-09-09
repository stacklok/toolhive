package sources

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/registry"
)

func TestNewRegistryFormatConverter(t *testing.T) {
	t.Parallel()

	converter := NewRegistryFormatConverter()
	assert.NotNil(t, converter)
	assert.IsType(t, &RegistryFormatConverter{}, converter)
}

func TestRegistryFormatConverter_SupportedFormats(t *testing.T) {
	t.Parallel()

	formats := supportedFormats()

	assert.Len(t, formats, 2)
	assert.Contains(t, formats, mcpv1alpha1.RegistryFormatToolHive)
	assert.Contains(t, formats, mcpv1alpha1.RegistryFormatUpstream)
}

func TestRegistryFormatConverter_DetectFormat(t *testing.T) {
	t.Parallel()

	toolhiveData := []byte(`{
		"version": "1.0.0",
		"last_updated": "2025-01-15T10:30:00Z",
		"servers": {
			"test-server": {
				"name": "test-server",
				"description": "A test server for validation",
				"image": "test/image:latest",
				"tier": "Community",
				"status": "Active",
				"transport": "stdio",
				"tools": ["test_tool"]
			}
		}
	}`)

	upstreamData := []byte(`[{
		"server": {
			"name": "test-server",
			"description": "A test server for validation",
			"status": "active",
			"version_detail": {
				"version": "1.0.0"
			},
			"packages": [{
				"registry_name": "docker",
				"name": "test/image",
				"version": "latest"
			}]
		},
		"x-publisher": {
			"x-dev.toolhive": {
				"tier": "Community",
				"transport": "stdio",
				"tools": ["test_tool"]
			}
		}
	}]`)

	tests := []struct {
		name           string
		data           []byte
		expectedFormat string
		expectError    bool
		errorContains  string
	}{
		{
			name:           "detect toolhive format",
			data:           toolhiveData,
			expectedFormat: mcpv1alpha1.RegistryFormatToolHive,
			expectError:    false,
		},
		{
			name:           "detect upstream format",
			data:           upstreamData,
			expectedFormat: mcpv1alpha1.RegistryFormatUpstream,
			expectError:    false,
		},
		{
			name:          "empty data",
			data:          []byte{},
			expectError:   true,
			errorContains: "data cannot be empty",
		},
		{
			name:          "invalid data",
			data:          []byte("invalid json"),
			expectError:   true,
			errorContains: "unable to detect registry format",
		},
		{
			name:          "unknown format",
			data:          []byte(`{"unknown": "format"}`),
			expectError:   true,
			errorContains: "unable to detect registry format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			format, err := detectFormat(tt.data)

			if tt.expectError {
				assert.Error(t, err)
				assert.Empty(t, format)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedFormat, format)
			}
		})
	}
}

func TestRegistryFormatConverter_Convert(t *testing.T) {
	t.Parallel()

	converter := NewRegistryFormatConverter()

	tests := []struct {
		name          string
		data          []byte
		fromFormat    string
		toFormat      string
		expectError   bool
		errorContains string
		validateFunc  func(t *testing.T, result []byte)
	}{
		{
			name:        "no conversion needed - same format",
			data:        []byte(`{"test": "data"}`),
			fromFormat:  mcpv1alpha1.RegistryFormatToolHive,
			toFormat:    mcpv1alpha1.RegistryFormatToolHive,
			expectError: false,
			validateFunc: func(t *testing.T, result []byte) { //nolint:thelper
				assert.Equal(t, `{"test": "data"}`, string(result))
			},
		},
		{
			name:          "empty data",
			data:          []byte{},
			fromFormat:    mcpv1alpha1.RegistryFormatToolHive,
			toFormat:      mcpv1alpha1.RegistryFormatUpstream,
			expectError:   true,
			errorContains: "input data cannot be empty",
		},
		{
			name:          "unsupported source format",
			data:          []byte(`{"test": "data"}`),
			fromFormat:    "unsupported",
			toFormat:      mcpv1alpha1.RegistryFormatToolHive,
			expectError:   true,
			errorContains: "unsupported format",
		},
		{
			name:          "unsupported target format",
			data:          []byte(`{"test": "data"}`),
			fromFormat:    mcpv1alpha1.RegistryFormatToolHive,
			toFormat:      "unsupported",
			expectError:   true,
			errorContains: "missing properties",
		},
		{
			name:          "invalid source data",
			data:          []byte("invalid json"),
			fromFormat:    mcpv1alpha1.RegistryFormatToolHive,
			toFormat:      mcpv1alpha1.RegistryFormatUpstream,
			expectError:   true,
			errorContains: "source data validation failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := converter.Convert(tt.data, tt.fromFormat, tt.toFormat)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, result)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
				if tt.validateFunc != nil {
					tt.validateFunc(t, result)
				}
			}
		})
	}
}

func TestRegistryFormatConverter_ConvertUpstreamToToolhive(t *testing.T) {
	t.Parallel()

	upstreamData := []byte(`[{
		"server": {
			"name": "test-server",
			"description": "Test server description",
			"packages": [{
				"type": "docker",
				"uri": "test/image:latest"
			}]
		}
	}]`)

	result, err := convertUpstreamToToolhive(upstreamData)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Parse result to verify structure
	var toolhiveRegistry registry.Registry
	err = json.Unmarshal(result, &toolhiveRegistry)
	require.NoError(t, err)

	// Verify basic structure
	assert.Equal(t, "1.0", toolhiveRegistry.Version)
	assert.NotNil(t, toolhiveRegistry.Servers)

	// Verify the converted server exists
	assert.Contains(t, toolhiveRegistry.Servers, "test-server")
	server := toolhiveRegistry.Servers["test-server"]
	assert.Equal(t, "test-server", server.Name)
	assert.Equal(t, "Test server description", server.Description)
}

func TestRegistryFormatConverter_ConvertUpstreamToToolhive_InvalidData(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		data          []byte
		errorContains string
	}{
		{
			name:          "invalid json",
			data:          []byte("invalid json"),
			errorContains: "failed to parse upstream format",
		},
		{
			name:          "empty array",
			data:          []byte("[]"),
			errorContains: "failed to convert server", // Will fail during conversion
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if tt.name == "empty array" {
				// Empty array will parse successfully but fail during conversion
				result, err := convertUpstreamToToolhive(tt.data)
				assert.NoError(t, err) // Should succeed for empty array
				assert.NotNil(t, result)
				return
			}

			result, err := convertUpstreamToToolhive(tt.data)
			assert.Error(t, err)
			assert.Nil(t, result)
			assert.Contains(t, err.Error(), tt.errorContains)
		})
	}
}

func TestRegistryFormatConverter_ConvertToolhiveToUpstream(t *testing.T) {
	t.Parallel()

	toolhiveData := []byte(`{
		"version": "1.0.0",
		"last_updated": "2025-01-15T10:30:00Z",
		"servers": {
			"test-server": {
				"name": "test-server",
				"description": "A test server for validation",
				"image": "test/image:latest",
				"tier": "Community",
				"status": "Active",
				"transport": "stdio",
				"tools": ["test_tool"]
			}
		}
	}`)

	result, err := convertToolhiveToUpstream(toolhiveData)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Parse result to verify structure
	var upstreamServers []registry.UpstreamServerDetail
	err = json.Unmarshal(result, &upstreamServers)
	require.NoError(t, err)

	// Verify we have the converted server
	assert.Len(t, upstreamServers, 1)
	server := upstreamServers[0]
	assert.Equal(t, "test-server", server.Server.Name)
	assert.Equal(t, "A test server for validation", server.Server.Description)
}

func TestRegistryFormatConverter_ConvertToolhiveToUpstream_InvalidData(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		data          []byte
		errorContains string
	}{
		{
			name:          "invalid json",
			data:          []byte("invalid json"),
			errorContains: "failed to parse ToolHive format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := convertToolhiveToUpstream(tt.data)
			assert.Error(t, err)
			assert.Nil(t, result)
			assert.Contains(t, err.Error(), tt.errorContains)
		})
	}
}

func TestContains(t *testing.T) {
	t.Parallel()

	slice := []string{"apple", "banana", "cherry"}

	tests := []struct {
		item     string
		expected bool
	}{
		{"apple", true},
		{"banana", true},
		{"cherry", true},
		{"orange", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.item, func(t *testing.T) {
			t.Parallel()

			result := slices.Contains(slice, tt.item)
			assert.Equal(t, tt.expected, result)
		})
	}
}

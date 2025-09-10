package sources

import (
	"testing"

	"github.com/stretchr/testify/assert"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestNewSyncResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		data        []byte
		serverCount int
		expectedLen int
	}{
		{
			name:        "empty data",
			data:        []byte{},
			serverCount: 0,
			expectedLen: 64, // SHA256 hex string length
		},
		{
			name:        "simple json data",
			data:        []byte(`{"servers": {"test": {}}}`),
			serverCount: 1,
			expectedLen: 64,
		},
		{
			name:        "complex data",
			data:        []byte(`{"servers": {"server1": {"name": "test1"}, "server2": {"name": "test2"}}}`),
			serverCount: 2,
			expectedLen: 64,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := NewSyncResultFromBytes(tt.data, tt.serverCount)

			rawData, err := result.GetRawData()
			if result.Registry == nil {
				assert.Error(t, err) // Should error when trying to marshal nil Registry
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, rawData)
			}
			assert.Equal(t, tt.serverCount, result.ServerCount)
			assert.Len(t, result.Hash, tt.expectedLen)
			assert.NotEmpty(t, result.Hash)
		})
	}
}

func TestSyncResultHashConsistency(t *testing.T) {
	t.Parallel()

	data := []byte(`{"test": "data"}`)
	serverCount := 5

	result1 := NewSyncResultFromBytes(data, serverCount)
	result2 := NewSyncResultFromBytes(data, serverCount)

	// Same data should produce same hash
	assert.Equal(t, result1.Hash, result2.Hash)
	assert.Equal(t, result1.ServerCount, result2.ServerCount)
}

func TestSyncResultHashDifference(t *testing.T) {
	t.Parallel()

	data1 := []byte(`{"test": "data1"}`)
	data2 := []byte(`{"test": "data2"}`)
	serverCount := 1

	result1 := NewSyncResultFromBytes(data1, serverCount)
	result2 := NewSyncResultFromBytes(data2, serverCount)

	// Different data should produce different hashes
	assert.NotEqual(t, result1.Hash, result2.Hash)
	assert.Equal(t, result1.ServerCount, result2.ServerCount)
}

func TestNewSourceDataValidator(t *testing.T) {
	t.Parallel()

	validator := NewSourceDataValidator()
	assert.NotNil(t, validator)
	assert.IsType(t, &DefaultSourceDataValidator{}, validator)
}

func TestDefaultSourceDataValidator_ValidateData(t *testing.T) {
	t.Parallel()

	validator := NewSourceDataValidator()

	validToolhiveData := []byte(`{
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

	validUpstreamData := []byte(`[{
		"server": {
			"name": "test-server",
			"description": "A test server for validation",
			"status": "Active",
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
		name          string
		data          []byte
		format        string
		expectError   bool
		errorContains string
	}{
		{
			name:        "valid toolhive format",
			data:        validToolhiveData,
			format:      mcpv1alpha1.RegistryFormatToolHive,
			expectError: false,
		},
		{
			name:        "valid upstream format",
			data:        validUpstreamData,
			format:      mcpv1alpha1.RegistryFormatUpstream,
			expectError: false,
		},
		{
			name:          "empty data",
			data:          []byte{},
			format:        mcpv1alpha1.RegistryFormatToolHive,
			expectError:   true,
			errorContains: "data cannot be empty",
		},
		{
			name:          "unsupported format",
			data:          validToolhiveData,
			format:        "unsupported",
			expectError:   true,
			errorContains: "unsupported format",
		},
		{
			name:          "invalid json for toolhive",
			data:          []byte("invalid json"),
			format:        mcpv1alpha1.RegistryFormatToolHive,
			expectError:   true,
			errorContains: "invalid",
		},
		{
			name:          "invalid json for upstream",
			data:          []byte("invalid json"),
			format:        mcpv1alpha1.RegistryFormatUpstream,
			expectError:   true,
			errorContains: "invalid upstream format",
		},
		{
			name:          "empty upstream array",
			data:          []byte("[]"),
			format:        mcpv1alpha1.RegistryFormatUpstream,
			expectError:   true,
			errorContains: "must contain at least one server",
		},
		{
			name: "upstream server missing name",
			data: []byte(`[{
				"server": {
					"description": "Test server"
				}
			}]`),
			format:        mcpv1alpha1.RegistryFormatUpstream,
			expectError:   true,
			errorContains: "name is required",
		},
		{
			name: "upstream server missing description",
			data: []byte(`[{
				"server": {
					"name": "test-server"
				}
			}]`),
			format:        mcpv1alpha1.RegistryFormatUpstream,
			expectError:   true,
			errorContains: "description is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := validator.ValidateData(tt.data, tt.format)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

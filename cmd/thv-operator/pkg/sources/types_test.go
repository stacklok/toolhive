package sources

import (
	"testing"

	"github.com/stretchr/testify/assert"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/registry"
)

func TestNewFetchResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		registryData *registry.Registry
		hash         string
		format       string
	}{
		{
			name: "empty registry",
			registryData: &registry.Registry{
				Version:       "1.0.0",
				Servers:       make(map[string]*registry.ImageMetadata),
				RemoteServers: make(map[string]*registry.RemoteServerMetadata),
			},
			hash:   "abcd1234",
			format: mcpv1alpha1.RegistryFormatToolHive,
		},
		{
			name: "registry with servers",
			registryData: &registry.Registry{
				Version: "1.0.0",
				Servers: map[string]*registry.ImageMetadata{
					"server1": {},
					"server2": {},
				},
				RemoteServers: make(map[string]*registry.RemoteServerMetadata),
			},
			hash:   "efgh5678",
			format: mcpv1alpha1.RegistryFormatToolHive,
		},
		{
			name: "registry with remote servers",
			registryData: &registry.Registry{
				Version: "1.0.0",
				Servers: make(map[string]*registry.ImageMetadata),
				RemoteServers: map[string]*registry.RemoteServerMetadata{
					"remote1": {},
				},
			},
			hash:   "ijkl9012",
			format: mcpv1alpha1.RegistryFormatUpstream,
		},
	}


	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := NewFetchResult(tt.registryData, tt.hash, tt.format)

			expectedServerCount := len(tt.registryData.Servers) + len(tt.registryData.RemoteServers)
			assert.Equal(t, expectedServerCount, result.ServerCount)
			assert.Equal(t, tt.hash, result.Hash)
			assert.Equal(t, tt.format, result.Format)
			assert.Equal(t, tt.registryData, result.Registry)
		})
	}
}

func TestFetchResultHashConsistency(t *testing.T) {
	t.Parallel()

	registryData := &registry.Registry{
		Version: "1.0.0",
		Servers: map[string]*registry.ImageMetadata{
			"server1": {},
			"server2": {},
			"server3": {},
			"server4": {},
			"server5": {},
		},
		RemoteServers: make(map[string]*registry.RemoteServerMetadata),
	}
	hash := "consistent-hash-value"
	format := mcpv1alpha1.RegistryFormatToolHive

	result1 := NewFetchResult(registryData, hash, format)
	result2 := NewFetchResult(registryData, hash, format)

	// Same data should produce same results
	assert.Equal(t, result1.Hash, result2.Hash)
	assert.Equal(t, result1.ServerCount, result2.ServerCount)
	assert.Equal(t, result1.Format, result2.Format)
	assert.Equal(t, result1.Registry, result2.Registry)
}

func TestFetchResultHashDifference(t *testing.T) {
	t.Parallel()

	registryData1 := &registry.Registry{
		Version: "1.0.0",
		Servers: map[string]*registry.ImageMetadata{
			"server1": {},
		},
		RemoteServers: make(map[string]*registry.RemoteServerMetadata),
	}

	registryData2 := &registry.Registry{
		Version: "1.0.0",
		Servers: map[string]*registry.ImageMetadata{
			"server2": {},
		},
		RemoteServers: make(map[string]*registry.RemoteServerMetadata),
	}

	hash1 := "hash-for-data1"
	hash2 := "hash-for-data2"
	format := mcpv1alpha1.RegistryFormatToolHive

	result1 := NewFetchResult(registryData1, hash1, format)
	result2 := NewFetchResult(registryData2, hash2, format)

	// Different data should produce different hashes
	assert.NotEqual(t, result1.Hash, result2.Hash)
	assert.Equal(t, result1.ServerCount, result2.ServerCount) // Both have 1 server
	assert.NotEqual(t, result1.Registry, result2.Registry)    // Different registries
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

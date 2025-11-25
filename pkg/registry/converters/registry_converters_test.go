package converters

import (
	"testing"
	"time"

	upstreamv0 "github.com/modelcontextprotocol/registry/pkg/api/v0"
	"github.com/modelcontextprotocol/registry/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	types "github.com/stacklok/toolhive/pkg/registry/registry"
)

func TestNewUpstreamRegistryFromToolhiveRegistry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		toolhiveReg *types.Registry
		expectError bool
		validate    func(*testing.T, *types.UpstreamRegistry)
	}{
		{
			name: "successful conversion with container servers",
			toolhiveReg: &types.Registry{
				Version:     "1.0.0",
				LastUpdated: "2024-01-01T00:00:00Z",
				Servers: map[string]*types.ImageMetadata{
					"test-server": {
						BaseServerMetadata: types.BaseServerMetadata{
							Name:        "test-server",
							Description: "A test server",
							Tier:        "Community",
							Status:      "Active",
							Transport:   "stdio",
							Tools:       []string{"test_tool"},
						},
						Image: "test/image:latest",
					},
				},
				RemoteServers: make(map[string]*types.RemoteServerMetadata),
			},
			expectError: false,
			validate: func(t *testing.T, sr *types.UpstreamRegistry) {
				t.Helper()
				assert.Equal(t, "1.0.0", sr.Version)
				assert.Equal(t, "2024-01-01T00:00:00Z", sr.Meta.LastUpdated)
				assert.Len(t, sr.Data.Servers, 1)
				assert.Contains(t, sr.Data.Servers[0].Name, "test-server")
				assert.Equal(t, "A test server", sr.Data.Servers[0].Description)
			},
		},
		{
			name: "successful conversion with remote servers",
			toolhiveReg: &types.Registry{
				Version:     "1.0.0",
				LastUpdated: "2024-01-01T00:00:00Z",
				Servers:     make(map[string]*types.ImageMetadata),
				RemoteServers: map[string]*types.RemoteServerMetadata{
					"remote-server": {
						BaseServerMetadata: types.BaseServerMetadata{
							Name:        "remote-server",
							Description: "A remote server",
							Tier:        "Community",
							Status:      "Active",
							Transport:   "sse",
							Tools:       []string{"remote_tool"},
						},
						URL: "https://example.com",
					},
				},
			},
			expectError: false,
			validate: func(t *testing.T, sr *types.UpstreamRegistry) {
				t.Helper()
				assert.Len(t, sr.Data.Servers, 1)
				assert.Contains(t, sr.Data.Servers[0].Name, "remote-server")
			},
		},
		{
			name: "empty registry",
			toolhiveReg: &types.Registry{
				Version:       "1.0.0",
				LastUpdated:   "2024-01-01T00:00:00Z",
				Servers:       make(map[string]*types.ImageMetadata),
				RemoteServers: make(map[string]*types.RemoteServerMetadata),
			},
			expectError: false,
			validate: func(t *testing.T, sr *types.UpstreamRegistry) {
				t.Helper()
				assert.Empty(t, sr.Data.Servers)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := NewUpstreamRegistryFromToolhiveRegistry(tt.toolhiveReg)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
				if tt.validate != nil {
					tt.validate(t, result)
				}
			}
		})
	}
}

func TestNewUpstreamRegistryFromUpstreamServers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		servers  []upstreamv0.ServerJSON
		validate func(*testing.T, *types.UpstreamRegistry)
	}{
		{
			name: "create from upstream servers",
			servers: []upstreamv0.ServerJSON{
				{
					Schema:      "https://static.modelcontextprotocol.io/schemas/2025-10-17/server.schema.json",
					Name:        "io.test/server1",
					Description: "Test server 1",
					Version:     "1.0.0",
					Packages: []model.Package{
						{
							RegistryType: "oci",
							Identifier:   "test/image:latest",
							Transport:    model.Transport{Type: "stdio"},
						},
					},
				},
			},
			validate: func(t *testing.T, sr *types.UpstreamRegistry) {
				t.Helper()
				assert.Equal(t, "1.0.0", sr.Version)
				assert.NotEmpty(t, sr.Meta.LastUpdated)
				assert.Len(t, sr.Data.Servers, 1)
				assert.Equal(t, "io.test/server1", sr.Data.Servers[0].Name)
			},
		},
		{
			name:    "create from empty slice",
			servers: []upstreamv0.ServerJSON{},
			validate: func(t *testing.T, sr *types.UpstreamRegistry) {
				t.Helper()
				assert.Empty(t, sr.Data.Servers)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := NewUpstreamRegistryFromUpstreamServers(tt.servers)

			assert.NotNil(t, result)
			if tt.validate != nil {
				tt.validate(t, result)
			}
		})
	}
}

func TestNewServerRegistryFromUpstream_DefaultValues(t *testing.T) {
	t.Parallel()

	servers := []upstreamv0.ServerJSON{
		{
			Schema:      "https://static.modelcontextprotocol.io/schemas/2025-10-17/server.schema.json",
			Name:        "io.test/server1",
			Description: "Test server",
			Version:     "1.0.0",
		},
	}

	result := NewUpstreamRegistryFromUpstreamServers(servers)

	// Verify defaults
	assert.Equal(t, "1.0.0", result.Version)
	assert.NotEmpty(t, result.Meta.LastUpdated)

	// Verify timestamp is recent (within last minute)
	parsedTime, err := time.Parse(time.RFC3339, result.Meta.LastUpdated)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), parsedTime, time.Minute)
}

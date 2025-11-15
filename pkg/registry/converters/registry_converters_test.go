package converters

import (
	"testing"
	"time"

	upstreamv0 "github.com/modelcontextprotocol/registry/pkg/api/v0"
	"github.com/modelcontextprotocol/registry/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/registry/types"
)

func TestNewServerRegistryFromToolhive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		toolhiveReg *types.Registry
		expectError bool
		validate    func(*testing.T, *types.ServerRegistry)
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
			validate: func(t *testing.T, sr *types.ServerRegistry) {
				t.Helper()
				assert.Equal(t, "1.0.0", sr.Version)
				assert.Equal(t, "2024-01-01T00:00:00Z", sr.LastUpdated)
				assert.Len(t, sr.Servers, 1)
				assert.Contains(t, sr.Servers[0].Name, "test-server")
				assert.Equal(t, "A test server", sr.Servers[0].Description)
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
			validate: func(t *testing.T, sr *types.ServerRegistry) {
				t.Helper()
				assert.Len(t, sr.Servers, 1)
				assert.Contains(t, sr.Servers[0].Name, "remote-server")
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
			validate: func(t *testing.T, sr *types.ServerRegistry) {
				t.Helper()
				assert.Empty(t, sr.Servers)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := NewServerRegistryFromToolhive(tt.toolhiveReg)

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

func TestNewServerRegistryFromUpstream(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		servers  []upstreamv0.ServerJSON
		validate func(*testing.T, *types.ServerRegistry)
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
			validate: func(t *testing.T, sr *types.ServerRegistry) {
				t.Helper()
				assert.Equal(t, "1.0.0", sr.Version)
				assert.NotEmpty(t, sr.LastUpdated)
				assert.Len(t, sr.Servers, 1)
				assert.Equal(t, "io.test/server1", sr.Servers[0].Name)
			},
		},
		{
			name:    "create from empty slice",
			servers: []upstreamv0.ServerJSON{},
			validate: func(t *testing.T, sr *types.ServerRegistry) {
				t.Helper()
				assert.Empty(t, sr.Servers)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := NewServerRegistryFromUpstream(tt.servers)

			assert.NotNil(t, result)
			if tt.validate != nil {
				tt.validate(t, result)
			}
		})
	}
}

func TestServerRegistry_ToToolhive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		serverReg    *types.ServerRegistry
		expectError  bool
		validateFunc func(*testing.T, *types.Registry)
	}{
		{
			name: "convert to toolhive with servers",
			serverReg: &types.ServerRegistry{
				Version:     "1.0.0",
				LastUpdated: "2024-01-01T00:00:00Z",
				Servers: []upstreamv0.ServerJSON{
					{
						Schema:      "https://static.modelcontextprotocol.io/schemas/2025-10-17/server.schema.json",
						Name:        "io.test/server1",
						Description: "Test server",
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
			},
			expectError: false,
			validateFunc: func(t *testing.T, reg *types.Registry) {
				t.Helper()
				assert.Equal(t, "1.0.0", reg.Version)
				assert.Equal(t, "2024-01-01T00:00:00Z", reg.LastUpdated)
				assert.Len(t, reg.Servers, 1)
				assert.Equal(t, "test/image:latest", reg.Servers["server1"].Image)
				assert.Equal(t, "stdio", reg.Servers["server1"].Transport)
				assert.Equal(t, "Test server", reg.Servers["server1"].Description)
				assert.Equal(t, "io.test/server1", reg.Servers["server1"].Name)
			},
		},
		{
			name: "convert to toolhive with remote servers",
			serverReg: &types.ServerRegistry{
				Version:     "1.0.0",
				LastUpdated: "2024-01-01T00:00:00Z",
				Servers: []upstreamv0.ServerJSON{
					{
						Schema:      "https://static.modelcontextprotocol.io/schemas/2025-10-17/server.schema.json",
						Name:        "io.test/server1",
						Description: "Test server",
						Version:     "1.0.0",
						Remotes: []model.Transport{
							{
								Type: "sse",
								URL:  "https://example.com",
							},
						},
					},
				},
			},
			expectError: false,
			validateFunc: func(t *testing.T, reg *types.Registry) {
				t.Helper()
				assert.Equal(t, "1.0.0", reg.Version)
				assert.Equal(t, "2024-01-01T00:00:00Z", reg.LastUpdated)
				assert.Len(t, reg.Servers, 0)
				assert.Len(t, reg.RemoteServers, 1)
				assert.Equal(t, "https://example.com", reg.RemoteServers["server1"].URL)
				assert.Equal(t, "sse", reg.RemoteServers["server1"].Transport)
				assert.Equal(t, "Test server", reg.RemoteServers["server1"].Description)
				assert.Equal(t, "io.test/server1", reg.RemoteServers["server1"].Name)
			},
		},
		{
			name: "convert empty registry",
			serverReg: &types.ServerRegistry{
				Version:     "1.0.0",
				LastUpdated: "2024-01-01T00:00:00Z",
				Servers:     []upstreamv0.ServerJSON{},
			},
			expectError: false,
			validateFunc: func(t *testing.T, reg *types.Registry) {
				t.Helper()
				assert.Empty(t, reg.Servers)
				assert.Empty(t, reg.RemoteServers)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := ToToolhive(tt.serverReg)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, result)
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

func TestServerRegistry_RoundTripConversion(t *testing.T) {
	t.Parallel()

	originalToolhive := &types.Registry{
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
	}

	// Convert to ServerRegistry
	serverReg, err := NewServerRegistryFromToolhive(originalToolhive)
	require.NoError(t, err)
	require.NotNil(t, serverReg)

	// Convert back to ToolHive
	convertedBack, err := ToToolhive(serverReg)
	require.NoError(t, err)
	require.NotNil(t, convertedBack)

	// Verify key fields match
	assert.Equal(t, originalToolhive.Version, convertedBack.Version)
	assert.Equal(t, originalToolhive.LastUpdated, convertedBack.LastUpdated)
	assert.Len(t, convertedBack.Servers, 1)
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

	result := NewServerRegistryFromUpstream(servers)

	// Verify defaults
	assert.Equal(t, "1.0.0", result.Version)
	assert.NotEmpty(t, result.LastUpdated)

	// Verify timestamp is recent (within last minute)
	parsedTime, err := time.Parse(time.RFC3339, result.LastUpdated)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), parsedTime, time.Minute)
}

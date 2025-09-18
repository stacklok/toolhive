package registry

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/config"
)

func TestNewRegistryProvider(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		config       *config.Config
		expectedType string
	}{
		{
			name:         "nil config returns embedded provider",
			config:       nil,
			expectedType: "*registry.LocalRegistryProvider",
		},
		{
			name: "empty registry URL returns embedded provider",
			config: &config.Config{
				RegistryUrl: "",
			},
			expectedType: "*registry.LocalRegistryProvider",
		},
		{
			name: "registry URL returns remote provider",
			config: &config.Config{
				RegistryUrl: "https://example.com/registry.json",
			},
			expectedType: "*registry.RemoteRegistryProvider",
		},
		{
			name: "local registry path returns embedded provider with file path",
			config: &config.Config{
				LocalRegistryPath: "/path/to/registry.json",
			},
			expectedType: "*registry.LocalRegistryProvider",
		},
		{
			name: "registry URL takes precedence over local path",
			config: &config.Config{
				RegistryUrl:       "https://example.com/registry.json",
				LocalRegistryPath: "/path/to/registry.json",
			},
			expectedType: "*registry.RemoteRegistryProvider",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider := NewRegistryProvider(tt.config)

			// Check the type of the provider
			providerType := getTypeName(provider)
			if providerType != tt.expectedType {
				t.Errorf("NewRegistryProvider() = %v, want %v", providerType, tt.expectedType)
			}
		})
	}
}

func TestLocalRegistryProvider(t *testing.T) {
	t.Parallel()
	provider := NewLocalRegistryProvider()

	// Test GetRegistry
	registry, err := provider.GetRegistry()
	if err != nil {
		t.Fatalf("GetRegistry() error = %v", err)
	}

	if registry == nil {
		t.Fatal("GetRegistry() returned nil registry")
		return
	}

	if len(registry.Servers) == 0 {
		t.Error("GetRegistry() returned registry with no servers")
	}

	// Test that server names are set
	for name, server := range registry.Servers {
		if server.Name != name {
			t.Errorf("ImageMetadata name not set correctly: got %s, want %s", server.Name, name)
		}
	}

	// Test ListServers
	servers, err := provider.ListServers()
	if err != nil {
		t.Fatalf("ListServers() error = %v", err)
	}

	totalServers := len(registry.Servers) + len(registry.RemoteServers)
	if len(servers) != totalServers {
		t.Errorf("ListServers() returned %d servers, want %d", len(servers), totalServers)
	}

	// Test GetServer with existing server
	if len(servers) > 0 {
		firstServer := servers[0]
		server, err := provider.GetServer(firstServer.GetName())
		if err != nil {
			t.Fatalf("GetServer() error = %v", err)
		}

		if server.GetName() != firstServer.GetName() {
			t.Errorf("GetServer() returned wrong server: got %s, want %s", server.GetName(), firstServer.GetName())
		}
	}

	// Test GetServer with non-existing server
	_, err = provider.GetServer("non-existing-server")
	if err == nil {
		t.Error("GetServer() with non-existing server should return error")
	}
}

func TestRemoteRegistryProvider(t *testing.T) {
	t.Parallel()
	// Note: This test would require a mock HTTP server for full testing
	// For now, we just test the creation
	provider := NewRemoteRegistryProvider("https://example.com/registry.json", false)

	if provider == nil {
		t.Fatal("NewRemoteRegistryProvider() returned nil")
	}

	// Test that it implements the interface
	var _ Provider = provider
}

func TestRemoteRegistryProvider_GetRegistry_Error(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		url         string
		expectError bool
	}{
		{
			name:        "invalid URL scheme",
			url:         "invalid://url",
			expectError: true,
		},
		{
			name:        "non-existent host",
			url:         "https://non-existent-host-12345.com/registry.json",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider := NewRemoteRegistryProvider(tt.url, false)
			registry, err := provider.GetRegistry()

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, registry)
			} else {
				// This case would require a working HTTP server
				assert.NoError(t, err)
				assert.NotNil(t, registry)
			}
		})
	}
}

func TestLocalRegistryProviderWithLocalFile(t *testing.T) {
	t.Parallel()

	// Create a temporary registry file
	tempDir := t.TempDir()
	registryFile := filepath.Join(tempDir, "test_registry.json")

	// Write test registry data
	testRegistry := `{
		"version": "1.0.0",
		"last_updated": "2023-01-01T00:00:00Z",
		"servers": {
			"test-server": {
				"image": "test/image:latest",
				"description": "Test server",
				"tier": "community",
				"status": "active",
				"transport": "stdio",
				"permissions": {
					"allow_local_resource_access": false,
					"allow_internet_access": false
				},
				"tools": ["test-tool"],
				"env_vars": [],
				"args": []
			}
		}
	}`

	err := os.WriteFile(registryFile, []byte(testRegistry), 0644)
	if err != nil {
		t.Fatalf("Failed to write test registry file: %v", err)
	}

	// Test with local file path
	provider := NewLocalRegistryProvider(registryFile)

	// Test GetRegistry
	registry, err := provider.GetRegistry()
	if err != nil {
		t.Fatalf("GetRegistry() error = %v", err)
	}

	if registry == nil {
		t.Fatal("GetRegistry() returned nil registry")
		return
	}

	if len(registry.Servers) != 1 {
		t.Errorf("Expected 1 server, got %d", len(registry.Servers))
	}

	server, exists := registry.Servers["test-server"]
	if !exists {
		t.Error("Expected test-server to exist in registry")
	}

	if server.Name != "test-server" {
		t.Errorf("Expected server name 'test-server', got '%s'", server.Name)
	}

	if server.Image != "test/image:latest" {
		t.Errorf("Expected image 'test/image:latest', got '%s'", server.Image)
	}
}

// getTypeName returns the type name of an interface value
func getTypeName(v interface{}) string {
	switch v.(type) {
	case *LocalRegistryProvider:
		return "*registry.LocalRegistryProvider"
	case *RemoteRegistryProvider:
		return "*registry.RemoteRegistryProvider"
	default:
		return "unknown"
	}
}

func TestGetRegistry(t *testing.T) {
	t.Parallel()

	// Create a temporary config for testing
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "toolhive", "config.yaml")

	// Ensure the directory exists
	err := os.MkdirAll(filepath.Dir(configPath), 0755)
	require.NoError(t, err)

	// Create a test config provider
	configProvider := config.NewPathProvider(configPath)

	// Create a test config
	cfg, err := configProvider.LoadOrCreateConfig()
	require.NoError(t, err)

	// Create provider with test config
	provider := NewRegistryProvider(cfg)
	reg, err := provider.GetRegistry()
	if err != nil {
		t.Fatalf("Failed to get registry: %v", err)
	}

	if reg == nil {
		t.Fatal("Registry is nil")
		return
	}

	if reg.Version == "" {
		t.Error("Registry version is empty")
	}

	if reg.LastUpdated == "" {
		t.Error("Registry last updated is empty")
	}

	if len(reg.Servers) == 0 {
		t.Error("Registry has no servers")
	}
}

func TestGetServer(t *testing.T) {
	t.Parallel()

	// Create a temporary config for testing
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "toolhive", "config.yaml")

	// Ensure the directory exists
	err := os.MkdirAll(filepath.Dir(configPath), 0755)
	require.NoError(t, err)

	// Create a test config provider
	configProvider := config.NewPathProvider(configPath)

	// Create a test config
	cfg, err := configProvider.LoadOrCreateConfig()
	require.NoError(t, err)

	// Create provider with test config
	provider := NewRegistryProvider(cfg)

	// Test getting an existing server
	server, err := provider.GetServer("osv")
	if err != nil {
		t.Fatalf("Failed to get server: %v", err)
	}

	if server == nil {
		t.Fatal("ServerMetadata is nil")
		return
	}

	// Check if it's a container server and has an image
	if !server.IsRemote() {
		if img, ok := server.(*ImageMetadata); ok {
			if img.Image == "" {
				t.Error("ImageMetadata image is empty")
			}
		}
	}

	if server.GetDescription() == "" {
		t.Error("ServerMetadata description is empty")
	}

	// Test getting a non-existent server
	_, err = provider.GetServer("non-existent-server")
	if err == nil {
		t.Error("Expected error when getting non-existent server")
	}
}

func TestSearchServers(t *testing.T) {
	t.Parallel()

	// Create a temporary config for testing
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "toolhive", "config.yaml")

	// Ensure the directory exists
	err := os.MkdirAll(filepath.Dir(configPath), 0755)
	require.NoError(t, err)

	// Create a test config provider
	configProvider := config.NewPathProvider(configPath)

	// Create a test config
	cfg, err := configProvider.LoadOrCreateConfig()
	require.NoError(t, err)

	// Create provider with test config
	provider := NewRegistryProvider(cfg)

	// Test searching for servers
	servers, err := provider.SearchServers("search")
	if err != nil {
		t.Fatalf("Failed to search servers: %v", err)
	}

	if len(servers) == 0 {
		t.Error("No servers found for search query")
	}

	// Test searching for non-existent servers
	servers, err = provider.SearchServers("non-existent-server")
	if err != nil {
		t.Fatalf("Failed to search servers: %v", err)
	}

	if len(servers) > 0 {
		t.Errorf("Expected no servers for non-existent query, got %d", len(servers))
	}
}

func TestListServers(t *testing.T) {
	t.Parallel()

	// Create a temporary config for testing
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "toolhive", "config.yaml")

	// Ensure the directory exists
	err := os.MkdirAll(filepath.Dir(configPath), 0755)
	require.NoError(t, err)

	// Create a test config provider
	configProvider := config.NewPathProvider(configPath)

	// Reset the default provider to ensure clean state
	ResetDefaultProvider()
	t.Cleanup(func() {
		ResetDefaultProvider()
	})

	provider, err := GetDefaultProviderWithConfig(configProvider)
	if err != nil {
		t.Fatalf("Failed to get registry provider: %v", err)
	}
	servers, err := provider.ListServers()
	if err != nil {
		t.Fatalf("Failed to list servers: %v", err)
	}

	if len(servers) == 0 {
		t.Error("No servers found")
	}

	// Verify that we get the same number of servers as in the registry
	reg, err := provider.GetRegistry()
	if err != nil {
		t.Fatalf("Failed to get registry: %v", err)
	}

	totalServers := len(reg.Servers) + len(reg.RemoteServers)
	if len(servers) != totalServers {
		t.Errorf("ListServers() returned %d servers, want %d", len(servers), totalServers)
	}
}

func TestParseRegistryData(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		data        []byte
		expectError bool
	}{
		{
			name: "valid registry data",
			data: []byte(`{
				"version": "1.0.0",
				"last_updated": "2023-01-01T00:00:00Z",
				"servers": {
					"test-server": {
						"image": "test/image:latest",
						"description": "Test server"
					}
				}
			}`),
			expectError: false,
		},
		{
			name:        "invalid JSON",
			data:        []byte(`invalid json`),
			expectError: true,
		},
		{
			name:        "empty data",
			data:        []byte(``),
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			registry, err := parseRegistryData(tt.data)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, registry)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, registry)
			}
		})
	}
}

func TestLocalRegistryProvider_FileReadError(t *testing.T) {
	t.Parallel()

	// Test with non-existent file path
	provider := NewLocalRegistryProvider("/non/existent/path/registry.json")

	registry, err := provider.GetRegistry()

	assert.Error(t, err)
	assert.Nil(t, registry)
	assert.Contains(t, err.Error(), "failed to read local registry file")
}

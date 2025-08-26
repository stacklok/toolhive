package runner

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/registry"
)

func TestRemoteServerMetadata_IsRemote(t *testing.T) {
	t.Parallel()

	// Test that RemoteServerMetadata correctly identifies as remote
	remoteServer := &registry.RemoteServerMetadata{
		BaseServerMetadata: registry.BaseServerMetadata{
			Name:        "test-remote-server",
			Description: "Test remote server",
			Transport:   "sse",
			Tools:       []string{"test-tool"},
			Tier:        "test",
			Status:      "active",
		},
		URL: "https://example.com/mcp",
	}

	assert.True(t, remoteServer.IsRemote(), "RemoteServerMetadata should identify as remote")
	assert.Equal(t, "https://example.com/mcp", remoteServer.URL)
	assert.Equal(t, "test-remote-server", remoteServer.GetName())
	assert.Equal(t, "sse", remoteServer.GetTransport())
}

func TestImageMetadata_IsRemote(t *testing.T) {
	t.Parallel()

	// Test that ImageMetadata correctly identifies as not remote
	containerServer := &registry.ImageMetadata{
		BaseServerMetadata: registry.BaseServerMetadata{
			Name:        "test-container-server",
			Description: "Test container server",
			Transport:   "stdio",
			Tools:       []string{"test-tool"},
			Tier:        "test",
			Status:      "active",
		},
		Image: "docker.io/test/server:latest",
	}

	assert.False(t, containerServer.IsRemote(), "ImageMetadata should not identify as remote")
	assert.Equal(t, "docker.io/test/server:latest", containerServer.Image)
	assert.Equal(t, "test-container-server", containerServer.GetName())
	assert.Equal(t, "stdio", containerServer.GetTransport())
}

func TestPreRunConfigType_Constants(t *testing.T) {
	t.Parallel()

	// Test that the PreRunConfigType constants are defined correctly
	assert.Equal(t, PreRunConfigType("registry"), PreRunConfigTypeRegistry)
	assert.Equal(t, PreRunConfigType("container_image"), PreRunConfigTypeContainerImage)
	assert.Equal(t, PreRunConfigType("protocol_scheme"), PreRunConfigTypeProtocolScheme)
	assert.Equal(t, PreRunConfigType("remote_url"), PreRunConfigTypeRemoteURL)
	assert.Equal(t, PreRunConfigType("config_file"), PreRunConfigTypeConfigFile)
}

func TestRegistrySource_Structure(t *testing.T) {
	t.Parallel()

	// Test the RegistrySource structure
	registrySource := &RegistrySource{
		ServerName: "test-server",
		IsRemote:   true,
	}

	assert.Equal(t, "test-server", registrySource.ServerName)
	assert.True(t, registrySource.IsRemote)

	// Test with container server
	containerSource := &RegistrySource{
		ServerName: "container-server",
		IsRemote:   false,
	}

	assert.Equal(t, "container-server", containerSource.ServerName)
	assert.False(t, containerSource.IsRemote)
}

func TestPreRunConfig_Structure(t *testing.T) {
	t.Parallel()

	// Test PreRunConfig structure for registry source
	preConfig := &PreRunConfig{
		Type:   PreRunConfigTypeRegistry,
		Source: "test-server",
		ParsedSource: &RegistrySource{
			ServerName: "test-server",
			IsRemote:   true,
		},
		Metadata: map[string]interface{}{
			"discovered": true,
		},
	}

	assert.Equal(t, PreRunConfigTypeRegistry, preConfig.Type)
	assert.Equal(t, "test-server", preConfig.Source)

	registrySource, ok := preConfig.ParsedSource.(*RegistrySource)
	require.True(t, ok, "ParsedSource should be a RegistrySource")
	assert.Equal(t, "test-server", registrySource.ServerName)
	assert.True(t, registrySource.IsRemote)

	assert.Equal(t, true, preConfig.Metadata["discovered"])
}

// Test the type assertion logic that was fixed in the transformer
func TestRemoteServerTypeAssertion(t *testing.T) {
	t.Parallel()

	// Test successful type assertion
	var serverMetadata registry.ServerMetadata = &registry.RemoteServerMetadata{
		BaseServerMetadata: registry.BaseServerMetadata{
			Name: "remote-server",
		},
		URL: "https://example.com/mcp",
	}

	if serverMetadata.IsRemote() {
		remoteServer, ok := serverMetadata.(*registry.RemoteServerMetadata)
		require.True(t, ok, "Should be able to cast to RemoteServerMetadata")
		assert.Equal(t, "https://example.com/mcp", remoteServer.URL)
	}

	// Test failed type assertion (this is what our fix handles)
	var invalidRemoteServer registry.ServerMetadata = &mockInvalidRemoteServer{}

	if invalidRemoteServer.IsRemote() {
		_, ok := invalidRemoteServer.(*registry.RemoteServerMetadata)
		assert.False(t, ok, "Should not be able to cast invalid server to RemoteServerMetadata")
		// This is the error case our fix handles
	}
}

// Mock server that claims to be remote but isn't RemoteServerMetadata
// This tests the error case we fixed in the transformer
type mockInvalidRemoteServer struct{}

func (*mockInvalidRemoteServer) GetName() string                   { return "invalid-remote" }
func (*mockInvalidRemoteServer) GetDescription() string            { return "Invalid remote server" }
func (*mockInvalidRemoteServer) GetTier() string                   { return "test" }
func (*mockInvalidRemoteServer) GetStatus() string                 { return "active" }
func (*mockInvalidRemoteServer) GetTransport() string              { return "sse" }
func (*mockInvalidRemoteServer) GetTools() []string                { return []string{"test-tool"} }
func (*mockInvalidRemoteServer) GetMetadata() *registry.Metadata   { return nil }
func (*mockInvalidRemoteServer) GetRepositoryURL() string          { return "" }
func (*mockInvalidRemoteServer) GetTags() []string                 { return nil }
func (*mockInvalidRemoteServer) GetCustomMetadata() map[string]any { return nil }
func (*mockInvalidRemoteServer) IsRemote() bool                    { return true } // Claims to be remote
func (*mockInvalidRemoteServer) GetEnvVars() []*registry.EnvVar    { return nil }

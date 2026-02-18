// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package converters

import (
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	upstream "github.com/modelcontextprotocol/registry/pkg/api/v0"
	"github.com/modelcontextprotocol/registry/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	types "github.com/stacklok/toolhive/pkg/registry/registry"
)

// Test Helpers

// createTestServerJSON creates a valid ServerJSON for testing with OCI package
func createTestServerJSON() *upstream.ServerJSON {
	return &upstream.ServerJSON{
		Schema:      model.CurrentSchemaURL,
		Name:        "io.github.stacklok/test-server",
		Title:       "Test Server",
		Description: "Test MCP server",
		Version:     "1.0.0",
		Repository: &model.Repository{
			URL:    "https://github.com/test/repo",
			Source: "github",
		},
		Packages: []model.Package{
			{
				RegistryType: model.RegistryTypeOCI,
				Identifier:   "ghcr.io/test/server:latest",
				Transport: model.Transport{
					Type: model.TransportTypeStdio,
				},
			},
		},
		Meta: &upstream.ServerMeta{
			PublisherProvided: map[string]interface{}{
				"io.github.stacklok": map[string]interface{}{
					"ghcr.io/test/server:latest": map[string]interface{}{
						"status":   "active",
						"tier":     "Official",
						"tools":    []interface{}{"tool1", "tool2"},
						"tags":     []interface{}{"test", "example"},
						"overview": "# Test Server\n\nA test MCP server.",
						"tool_definitions": []interface{}{
							map[string]interface{}{
								"name":        "tool1",
								"description": "First tool",
							},
						},
						"metadata": map[string]interface{}{
							"stars":        float64(100),
							"last_updated": "2025-01-01",
						},
					},
				},
			},
		},
	}
}

// createTestImageMetadata creates a valid ImageMetadata for testing
func createTestImageMetadata() *types.ImageMetadata {
	return &types.ImageMetadata{
		BaseServerMetadata: types.BaseServerMetadata{
			Title:         "Test Server",
			Description:   "Test MCP server",
			Transport:     model.TransportTypeStdio,
			RepositoryURL: "https://github.com/test/repo",
			Status:        "active",
			Tier:          "Official",
			Tools:         []string{"tool1", "tool2"},
			Tags:          []string{"test", "example"},
			Overview:      "# Test Server\n\nA test MCP server.",
			ToolDefinitions: []mcp.Tool{
				{Name: "tool1", Description: "First tool"},
			},
			Metadata: &types.Metadata{
				Stars:       100,
				LastUpdated: "2025-01-01",
			},
		},
		Image: "ghcr.io/test/server:latest",
	}
}

// createTestRemoteServerMetadata creates a valid RemoteServerMetadata for testing
func createTestRemoteServerMetadata() *types.RemoteServerMetadata {
	return &types.RemoteServerMetadata{
		BaseServerMetadata: types.BaseServerMetadata{
			Title:         "Test Remote",
			Description:   "Test remote server",
			Transport:     "sse",
			RepositoryURL: "https://github.com/test/remote",
			Status:        "active",
			Tier:          "Official",
			Tools:         []string{"tool1"},
			Tags:          []string{"remote"},
			Overview:      "# Test Remote\n\nA test remote server.",
			ToolDefinitions: []mcp.Tool{
				{Name: "tool1", Description: "Remote tool"},
			},
		},
		URL: "https://api.example.com/mcp",
	}
}

// Test Suite 1: ServerJSONToImageMetadata

func TestServerJSONToImageMetadata_Success(t *testing.T) {
	t.Parallel()

	serverJSON := createTestServerJSON()
	imageMetadata, err := ServerJSONToImageMetadata(serverJSON)

	require.NoError(t, err)
	require.NotNil(t, imageMetadata)

	assert.Equal(t, "ghcr.io/test/server:latest", imageMetadata.Image)
	assert.Equal(t, "Test Server", imageMetadata.Title)
	assert.Equal(t, "Test MCP server", imageMetadata.Description)
	assert.Equal(t, model.TransportTypeStdio, imageMetadata.Transport)
	assert.Equal(t, "https://github.com/test/repo", imageMetadata.RepositoryURL)
	assert.Equal(t, "active", imageMetadata.Status)
	assert.Equal(t, "Official", imageMetadata.Tier)
	assert.Equal(t, []string{"tool1", "tool2"}, imageMetadata.Tools)
	assert.Equal(t, []string{"test", "example"}, imageMetadata.Tags)
	assert.Equal(t, "# Test Server\n\nA test MCP server.", imageMetadata.Overview)
	require.Len(t, imageMetadata.ToolDefinitions, 1)
	assert.Equal(t, "tool1", imageMetadata.ToolDefinitions[0].Name)
	assert.NotNil(t, imageMetadata.Metadata)
	assert.Equal(t, 100, imageMetadata.Metadata.Stars)
	assert.Equal(t, "2025-01-01", imageMetadata.Metadata.LastUpdated)
}

func TestServerJSONToImageMetadata_NilInput(t *testing.T) {
	t.Parallel()

	imageMetadata, err := ServerJSONToImageMetadata(nil)

	assert.Error(t, err)
	assert.Nil(t, imageMetadata)
	assert.Contains(t, err.Error(), "serverJSON cannot be nil")
}

func TestServerJSONToImageMetadata_NoPackages(t *testing.T) {
	t.Parallel()

	serverJSON := &upstream.ServerJSON{
		Name:     "test",
		Packages: []model.Package{},
	}

	imageMetadata, err := ServerJSONToImageMetadata(serverJSON)

	assert.Error(t, err)
	assert.Nil(t, imageMetadata)
	assert.Contains(t, err.Error(), "has no packages")
}

func TestServerJSONToImageMetadata_NoOCIPackages(t *testing.T) {
	t.Parallel()

	serverJSON := &upstream.ServerJSON{
		Name: "test",
		Packages: []model.Package{
			{
				RegistryType: "npm",
				Identifier:   "test-package",
			},
		},
	}

	imageMetadata, err := ServerJSONToImageMetadata(serverJSON)

	assert.Error(t, err)
	assert.Nil(t, imageMetadata)
	assert.Contains(t, err.Error(), "has no OCI packages")
}

func TestServerJSONToImageMetadata_MultipleOCIPackages(t *testing.T) {
	t.Parallel()

	serverJSON := &upstream.ServerJSON{
		Name: "test",
		Packages: []model.Package{
			{
				RegistryType: model.RegistryTypeOCI,
				Identifier:   "image1:latest",
			},
			{
				RegistryType: model.RegistryTypeOCI,
				Identifier:   "image2:latest",
			},
		},
	}

	imageMetadata, err := ServerJSONToImageMetadata(serverJSON)

	assert.Error(t, err)
	assert.Nil(t, imageMetadata)
	assert.Contains(t, err.Error(), "has 2 OCI packages")
}

func TestServerJSONToImageMetadata_WithEnvVars(t *testing.T) {
	t.Parallel()

	serverJSON := createTestServerJSON()
	serverJSON.Packages[0].EnvironmentVariables = []model.KeyValueInput{
		{
			Name: "API_KEY",
			InputWithVariables: model.InputWithVariables{
				Input: model.Input{
					Description: "API Key",
					IsRequired:  true,
					IsSecret:    true,
					Default:     "default-key",
				},
			},
		},
		{
			Name: "DEBUG",
			InputWithVariables: model.InputWithVariables{
				Input: model.Input{
					Description: "Debug mode",
					IsRequired:  false,
					IsSecret:    false,
					Default:     "false",
				},
			},
		},
	}

	imageMetadata, err := ServerJSONToImageMetadata(serverJSON)

	require.NoError(t, err)
	require.NotNil(t, imageMetadata)
	require.Len(t, imageMetadata.EnvVars, 2)

	assert.Equal(t, "API_KEY", imageMetadata.EnvVars[0].Name)
	assert.Equal(t, "API Key", imageMetadata.EnvVars[0].Description)
	assert.True(t, imageMetadata.EnvVars[0].Required)
	assert.True(t, imageMetadata.EnvVars[0].Secret)
	assert.Equal(t, "default-key", imageMetadata.EnvVars[0].Default)

	assert.Equal(t, "DEBUG", imageMetadata.EnvVars[1].Name)
	assert.Equal(t, "Debug mode", imageMetadata.EnvVars[1].Description)
	assert.False(t, imageMetadata.EnvVars[1].Required)
	assert.False(t, imageMetadata.EnvVars[1].Secret)
	assert.Equal(t, "false", imageMetadata.EnvVars[1].Default)
}

func TestServerJSONToImageMetadata_WithTargetPort(t *testing.T) {
	t.Parallel()

	serverJSON := createTestServerJSON()
	serverJSON.Packages[0].Transport = model.Transport{
		Type: model.TransportTypeStreamableHTTP,
		URL:  "http://localhost:9090",
	}

	imageMetadata, err := ServerJSONToImageMetadata(serverJSON)

	require.NoError(t, err)
	require.NotNil(t, imageMetadata)
	assert.Equal(t, 9090, imageMetadata.TargetPort)
}

func TestServerJSONToImageMetadata_InvalidPortURL(t *testing.T) {
	t.Parallel()

	serverJSON := createTestServerJSON()
	serverJSON.Packages[0].Transport = model.Transport{
		Type: model.TransportTypeStreamableHTTP,
		URL:  "not-a-valid-url",
	}

	imageMetadata, err := ServerJSONToImageMetadata(serverJSON)

	require.NoError(t, err)
	require.NotNil(t, imageMetadata)
	assert.Equal(t, 0, imageMetadata.TargetPort) // Should default to 0 on parse failure
}

func TestServerJSONToImageMetadata_MissingPublisherExtensions(t *testing.T) {
	t.Parallel()

	serverJSON := createTestServerJSON()
	serverJSON.Meta = nil

	imageMetadata, err := ServerJSONToImageMetadata(serverJSON)

	require.NoError(t, err)
	require.NotNil(t, imageMetadata)
	assert.Equal(t, "", imageMetadata.Status)
	assert.Equal(t, "", imageMetadata.Tier)
	assert.Nil(t, imageMetadata.Tools)
	assert.Nil(t, imageMetadata.Tags)
	assert.Nil(t, imageMetadata.Metadata)
}

// Test Suite 2: ImageMetadataToServerJSON

func TestImageMetadataToServerJSON_Success(t *testing.T) {
	t.Parallel()

	imageMetadata := createTestImageMetadata()
	serverJSON, err := ImageMetadataToServerJSON("test-server", imageMetadata)

	require.NoError(t, err)
	require.NotNil(t, serverJSON)

	assert.Equal(t, model.CurrentSchemaURL, serverJSON.Schema)
	assert.Equal(t, "io.github.stacklok/test-server", serverJSON.Name)
	assert.Equal(t, "Test Server", serverJSON.Title)
	assert.Equal(t, "Test MCP server", serverJSON.Description)
	assert.Equal(t, "1.0.0", serverJSON.Version)
	assert.Equal(t, "https://github.com/test/repo", serverJSON.Repository.URL)
	assert.Len(t, serverJSON.Packages, 1)
	assert.Equal(t, model.RegistryTypeOCI, serverJSON.Packages[0].RegistryType)
	assert.Equal(t, "ghcr.io/test/server:latest", serverJSON.Packages[0].Identifier)
	assert.NotNil(t, serverJSON.Meta)
	assert.NotNil(t, serverJSON.Meta.PublisherProvided)
}

func TestImageMetadataToServerJSON_NilInput(t *testing.T) {
	t.Parallel()

	serverJSON, err := ImageMetadataToServerJSON("test", nil)

	assert.Error(t, err)
	assert.Nil(t, serverJSON)
	assert.Contains(t, err.Error(), "imageMetadata cannot be nil")
}

func TestImageMetadataToServerJSON_EmptyName(t *testing.T) {
	t.Parallel()

	imageMetadata := createTestImageMetadata()
	serverJSON, err := ImageMetadataToServerJSON("", imageMetadata)

	assert.Error(t, err)
	assert.Nil(t, serverJSON)
	assert.Contains(t, err.Error(), "name cannot be empty")
}

func TestImageMetadataToServerJSON_WithEnvVars(t *testing.T) {
	t.Parallel()

	imageMetadata := createTestImageMetadata()
	imageMetadata.EnvVars = []*types.EnvVar{
		{
			Name:        "API_KEY",
			Description: "API Key",
			Required:    true,
			Secret:      true,
			Default:     "default",
		},
	}

	serverJSON, err := ImageMetadataToServerJSON("test", imageMetadata)

	require.NoError(t, err)
	require.NotNil(t, serverJSON)
	require.Len(t, serverJSON.Packages, 1)
	require.Len(t, serverJSON.Packages[0].EnvironmentVariables, 1)

	envVar := serverJSON.Packages[0].EnvironmentVariables[0]
	assert.Equal(t, "API_KEY", envVar.Name)
	assert.Equal(t, "API Key", envVar.Description)
	assert.True(t, envVar.IsRequired)
	assert.True(t, envVar.IsSecret)
	assert.Equal(t, "default", envVar.Default)
}

func TestImageMetadataToServerJSON_WithTargetPort(t *testing.T) {
	t.Parallel()

	imageMetadata := createTestImageMetadata()
	imageMetadata.Transport = model.TransportTypeStreamableHTTP
	imageMetadata.TargetPort = 9090

	serverJSON, err := ImageMetadataToServerJSON("test", imageMetadata)

	require.NoError(t, err)
	require.NotNil(t, serverJSON)
	require.Len(t, serverJSON.Packages, 1)

	assert.Equal(t, model.TransportTypeStreamableHTTP, serverJSON.Packages[0].Transport.Type)
	assert.Equal(t, "http://localhost:9090", serverJSON.Packages[0].Transport.URL)
}

func TestImageMetadataToServerJSON_HTTPTransportNoPort(t *testing.T) {
	t.Parallel()

	imageMetadata := createTestImageMetadata()
	imageMetadata.Transport = model.TransportTypeStreamableHTTP
	imageMetadata.TargetPort = 0 // No port specified

	serverJSON, err := ImageMetadataToServerJSON("test", imageMetadata)

	require.NoError(t, err)
	require.NotNil(t, serverJSON)
	require.Len(t, serverJSON.Packages, 1)

	assert.Equal(t, model.TransportTypeStreamableHTTP, serverJSON.Packages[0].Transport.Type)
	assert.Equal(t, "http://localhost", serverJSON.Packages[0].Transport.URL) // No port in URL
}

func TestImageMetadataToServerJSON_StdioTransport(t *testing.T) {
	t.Parallel()

	imageMetadata := createTestImageMetadata()
	imageMetadata.Transport = model.TransportTypeStdio

	serverJSON, err := ImageMetadataToServerJSON("test", imageMetadata)

	require.NoError(t, err)
	require.NotNil(t, serverJSON)
	require.Len(t, serverJSON.Packages, 1)

	assert.Equal(t, model.TransportTypeStdio, serverJSON.Packages[0].Transport.Type)
	assert.Empty(t, serverJSON.Packages[0].Transport.URL)
}

func TestImageMetadataToServerJSON_EmptyTransportDefaultsToStdio(t *testing.T) {
	t.Parallel()

	imageMetadata := createTestImageMetadata()
	imageMetadata.Transport = ""

	serverJSON, err := ImageMetadataToServerJSON("test", imageMetadata)

	require.NoError(t, err)
	require.NotNil(t, serverJSON)
	require.Len(t, serverJSON.Packages, 1)

	assert.Equal(t, model.TransportTypeStdio, serverJSON.Packages[0].Transport.Type)
}

func TestImageMetadataToServerJSON_WithPublisherExtensions(t *testing.T) {
	t.Parallel()

	imageMetadata := createTestImageMetadata()
	serverJSON, err := ImageMetadataToServerJSON("test", imageMetadata)

	require.NoError(t, err)
	require.NotNil(t, serverJSON)
	require.NotNil(t, serverJSON.Meta)
	require.NotNil(t, serverJSON.Meta.PublisherProvided)

	stacklokData, ok := serverJSON.Meta.PublisherProvided["io.github.stacklok"].(map[string]interface{})
	require.True(t, ok)

	imageData, ok := stacklokData["ghcr.io/test/server:latest"].(map[string]interface{})
	require.True(t, ok)

	assert.Equal(t, "active", imageData["status"])
	assert.Equal(t, "Official", imageData["tier"])
}

func TestImageMetadataToServerJSON_EmptyStatusDefaultsToActive(t *testing.T) {
	t.Parallel()

	imageMetadata := createTestImageMetadata()
	imageMetadata.Status = "" // Empty status should default to "active"

	serverJSON, err := ImageMetadataToServerJSON("test", imageMetadata)

	require.NoError(t, err)
	require.NotNil(t, serverJSON)

	stacklokData, ok := serverJSON.Meta.PublisherProvided["io.github.stacklok"].(map[string]interface{})
	require.True(t, ok)

	imageData, ok := stacklokData["ghcr.io/test/server:latest"].(map[string]interface{})
	require.True(t, ok)

	assert.Equal(t, "active", imageData["status"])
}

func TestRemoteServerMetadataToServerJSON_EmptyStatusDefaultsToActive(t *testing.T) {
	t.Parallel()

	remoteMetadata := createTestRemoteServerMetadata()
	remoteMetadata.Status = "" // Empty status should default to "active"

	serverJSON, err := RemoteServerMetadataToServerJSON("test-remote", remoteMetadata)

	require.NoError(t, err)
	require.NotNil(t, serverJSON)

	stacklokData, ok := serverJSON.Meta.PublisherProvided["io.github.stacklok"].(map[string]interface{})
	require.True(t, ok)

	remoteData, ok := stacklokData["https://api.example.com/mcp"].(map[string]interface{})
	require.True(t, ok)

	assert.Equal(t, "active", remoteData["status"])
}

func TestImageMetadataToServerJSON_ReverseDNSName(t *testing.T) {
	t.Parallel()

	imageMetadata := createTestImageMetadata()
	serverJSON, err := ImageMetadataToServerJSON("fetch", imageMetadata)

	require.NoError(t, err)
	require.NotNil(t, serverJSON)
	assert.Equal(t, "io.github.stacklok/fetch", serverJSON.Name)
}

// Test Suite 3: ServerJSONToRemoteServerMetadata

func TestServerJSONToRemoteServerMetadata_Success(t *testing.T) {
	t.Parallel()

	serverJSON := &upstream.ServerJSON{
		Name:        "io.github.stacklok/test-remote",
		Title:       "Test Remote",
		Description: "Test remote server",
		Repository: &model.Repository{
			URL: "https://github.com/test/remote",
		},
		Remotes: []model.Transport{
			{
				Type: "sse",
				URL:  "https://api.example.com/mcp",
			},
		},
		Meta: &upstream.ServerMeta{
			PublisherProvided: map[string]interface{}{
				"io.github.stacklok": map[string]interface{}{
					"https://api.example.com/mcp": map[string]interface{}{
						"status":   "active",
						"tier":     "Official",
						"tools":    []interface{}{"tool1"},
						"overview": "# Test Remote\n\nA test remote server.",
						"tool_definitions": []interface{}{
							map[string]interface{}{
								"name":        "tool1",
								"description": "Remote tool",
							},
						},
					},
				},
			},
		},
	}

	remoteMetadata, err := ServerJSONToRemoteServerMetadata(serverJSON)

	require.NoError(t, err)
	require.NotNil(t, remoteMetadata)

	assert.Equal(t, "https://api.example.com/mcp", remoteMetadata.URL)
	assert.Equal(t, "Test Remote", remoteMetadata.Title)
	assert.Equal(t, "Test remote server", remoteMetadata.Description)
	assert.Equal(t, "sse", remoteMetadata.Transport)
	assert.Equal(t, "https://github.com/test/remote", remoteMetadata.RepositoryURL)
	assert.Equal(t, "active", remoteMetadata.Status)
	assert.Equal(t, "Official", remoteMetadata.Tier)
	assert.Equal(t, []string{"tool1"}, remoteMetadata.Tools)
	assert.Equal(t, "# Test Remote\n\nA test remote server.", remoteMetadata.Overview)
	require.Len(t, remoteMetadata.ToolDefinitions, 1)
	assert.Equal(t, "tool1", remoteMetadata.ToolDefinitions[0].Name)
}

func TestServerJSONToRemoteServerMetadata_NilInput(t *testing.T) {
	t.Parallel()

	remoteMetadata, err := ServerJSONToRemoteServerMetadata(nil)

	assert.Error(t, err)
	assert.Nil(t, remoteMetadata)
	assert.Contains(t, err.Error(), "serverJSON cannot be nil")
}

func TestServerJSONToRemoteServerMetadata_NoRemotes(t *testing.T) {
	t.Parallel()

	serverJSON := &upstream.ServerJSON{
		Name:    "test",
		Remotes: []model.Transport{},
	}

	remoteMetadata, err := ServerJSONToRemoteServerMetadata(serverJSON)

	assert.Error(t, err)
	assert.Nil(t, remoteMetadata)
	assert.Contains(t, err.Error(), "has no remotes")
}

func TestServerJSONToRemoteServerMetadata_WithHeaders(t *testing.T) {
	t.Parallel()

	serverJSON := &upstream.ServerJSON{
		Name:        "test",
		Description: "Test",
		Remotes: []model.Transport{
			{
				Type: "sse",
				URL:  "https://api.example.com",
				Headers: []model.KeyValueInput{
					{
						Name: "Authorization",
						InputWithVariables: model.InputWithVariables{
							Input: model.Input{
								Description: "Auth token",
								IsRequired:  true,
								IsSecret:    true,
							},
						},
					},
				},
			},
		},
	}

	remoteMetadata, err := ServerJSONToRemoteServerMetadata(serverJSON)

	require.NoError(t, err)
	require.NotNil(t, remoteMetadata)
	require.Len(t, remoteMetadata.Headers, 1)

	assert.Equal(t, "Authorization", remoteMetadata.Headers[0].Name)
	assert.Equal(t, "Auth token", remoteMetadata.Headers[0].Description)
	assert.True(t, remoteMetadata.Headers[0].Required)
	assert.True(t, remoteMetadata.Headers[0].Secret)
}

func TestServerJSONToRemoteServerMetadata_MissingPublisherExtensions(t *testing.T) {
	t.Parallel()

	serverJSON := &upstream.ServerJSON{
		Name:        "test",
		Description: "Test",
		Remotes: []model.Transport{
			{
				Type: "sse",
				URL:  "https://api.example.com",
			},
		},
		Meta: nil,
	}

	remoteMetadata, err := ServerJSONToRemoteServerMetadata(serverJSON)

	require.NoError(t, err)
	require.NotNil(t, remoteMetadata)
	assert.Equal(t, "", remoteMetadata.Status)
	assert.Equal(t, "", remoteMetadata.Tier)
}

// Test Suite 4: RemoteServerMetadataToServerJSON

func TestRemoteServerMetadataToServerJSON_Success(t *testing.T) {
	t.Parallel()

	remoteMetadata := createTestRemoteServerMetadata()
	serverJSON, err := RemoteServerMetadataToServerJSON("test-remote", remoteMetadata)

	require.NoError(t, err)
	require.NotNil(t, serverJSON)

	assert.Equal(t, model.CurrentSchemaURL, serverJSON.Schema)
	assert.Equal(t, "io.github.stacklok/test-remote", serverJSON.Name)
	assert.Equal(t, "Test Remote", serverJSON.Title)
	assert.Equal(t, "Test remote server", serverJSON.Description)
	assert.Equal(t, "https://github.com/test/remote", serverJSON.Repository.URL)
	assert.Len(t, serverJSON.Remotes, 1)
	assert.Equal(t, "sse", serverJSON.Remotes[0].Type)
	assert.Equal(t, "https://api.example.com/mcp", serverJSON.Remotes[0].URL)
}

func TestRemoteServerMetadataToServerJSON_NilInput(t *testing.T) {
	t.Parallel()

	serverJSON, err := RemoteServerMetadataToServerJSON("test", nil)

	assert.Error(t, err)
	assert.Nil(t, serverJSON)
	assert.Contains(t, err.Error(), "remoteMetadata cannot be nil")
}

func TestRemoteServerMetadataToServerJSON_EmptyName(t *testing.T) {
	t.Parallel()

	remoteMetadata := createTestRemoteServerMetadata()
	serverJSON, err := RemoteServerMetadataToServerJSON("", remoteMetadata)

	assert.Error(t, err)
	assert.Nil(t, serverJSON)
	assert.Contains(t, err.Error(), "name cannot be empty")
}

func TestRemoteServerMetadataToServerJSON_WithHeaders(t *testing.T) {
	t.Parallel()

	remoteMetadata := createTestRemoteServerMetadata()
	remoteMetadata.Headers = []*types.Header{
		{
			Name:        "Authorization",
			Description: "Auth header",
			Required:    true,
			Secret:      true,
		},
	}

	serverJSON, err := RemoteServerMetadataToServerJSON("test", remoteMetadata)

	require.NoError(t, err)
	require.NotNil(t, serverJSON)
	require.Len(t, serverJSON.Remotes, 1)
	require.Len(t, serverJSON.Remotes[0].Headers, 1)

	header := serverJSON.Remotes[0].Headers[0]
	assert.Equal(t, "Authorization", header.Name)
	assert.Equal(t, "Auth header", header.Description)
	assert.True(t, header.IsRequired)
	assert.True(t, header.IsSecret)
}

func TestRemoteServerMetadataToServerJSON_WithPublisherExtensions(t *testing.T) {
	t.Parallel()

	remoteMetadata := createTestRemoteServerMetadata()
	serverJSON, err := RemoteServerMetadataToServerJSON("test", remoteMetadata)

	require.NoError(t, err)
	require.NotNil(t, serverJSON)
	require.NotNil(t, serverJSON.Meta)
	require.NotNil(t, serverJSON.Meta.PublisherProvided)

	stacklokData, ok := serverJSON.Meta.PublisherProvided["io.github.stacklok"].(map[string]interface{})
	require.True(t, ok)

	remoteData, ok := stacklokData["https://api.example.com/mcp"].(map[string]interface{})
	require.True(t, ok)

	assert.Equal(t, "active", remoteData["status"])
	assert.Equal(t, "Official", remoteData["tier"])
}

// Test Suite 5: Utility Functions

func TestExtractServerName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "reverse DNS format",
			input:    "io.github.stacklok/fetch",
			expected: "fetch",
		},
		{
			name:     "no slash",
			input:    "fetch",
			expected: "fetch",
		},
		{
			name:     "returns original if multiple slashes",
			input:    "io.github.stacklok/mcp/server",
			expected: "io.github.stacklok/mcp/server", // Function only splits on first slash, returns original if not exactly 2 parts
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := ExtractServerName(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestBuildReverseDNSName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple name",
			input:    "fetch",
			expected: "io.github.stacklok/fetch",
		},
		{
			name:     "already formatted",
			input:    "io.github.stacklok/fetch",
			expected: "io.github.stacklok/fetch",
		},
		{
			name:     "other namespace",
			input:    "com.example/server",
			expected: "com.example/server",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := BuildReverseDNSName(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Test Suite 6: Round-trip Conversion Tests

func TestRoundTrip_ImageMetadata(t *testing.T) {
	t.Parallel()

	// Start with ImageMetadata
	original := createTestImageMetadata()

	// Convert to ServerJSON
	serverJSON, err := ImageMetadataToServerJSON("test-server", original)
	require.NoError(t, err)

	// Convert back to ImageMetadata
	result, err := ServerJSONToImageMetadata(serverJSON)
	require.NoError(t, err)

	// Verify data preserved
	assert.Equal(t, original.Image, result.Image)
	assert.Equal(t, original.Title, result.Title)
	assert.Equal(t, original.Description, result.Description)
	assert.Equal(t, original.Transport, result.Transport)
	assert.Equal(t, original.RepositoryURL, result.RepositoryURL)
	assert.Equal(t, original.Status, result.Status)
	assert.Equal(t, original.Tier, result.Tier)
	assert.Equal(t, original.Tools, result.Tools)
	assert.Equal(t, original.Tags, result.Tags)
	assert.Equal(t, original.Overview, result.Overview)
	assert.Len(t, result.ToolDefinitions, len(original.ToolDefinitions))

	if original.Metadata != nil {
		require.NotNil(t, result.Metadata)
		assert.Equal(t, original.Metadata.Stars, result.Metadata.Stars)
		assert.Equal(t, original.Metadata.LastUpdated, result.Metadata.LastUpdated)
	}
}

func TestRoundTrip_RemoteServerMetadata(t *testing.T) {
	t.Parallel()

	// Start with RemoteServerMetadata
	original := createTestRemoteServerMetadata()

	// Convert to ServerJSON
	serverJSON, err := RemoteServerMetadataToServerJSON("test-remote", original)
	require.NoError(t, err)

	// Convert back to RemoteServerMetadata
	result, err := ServerJSONToRemoteServerMetadata(serverJSON)
	require.NoError(t, err)

	// Verify data preserved
	assert.Equal(t, original.URL, result.URL)
	assert.Equal(t, original.Title, result.Title)
	assert.Equal(t, original.Description, result.Description)
	assert.Equal(t, original.Transport, result.Transport)
	assert.Equal(t, original.RepositoryURL, result.RepositoryURL)
	assert.Equal(t, original.Status, result.Status)
	assert.Equal(t, original.Tier, result.Tier)
	assert.Equal(t, original.Tools, result.Tools)
	assert.Equal(t, original.Tags, result.Tags)
	assert.Equal(t, original.Overview, result.Overview)
	assert.Len(t, result.ToolDefinitions, len(original.ToolDefinitions))
}

func TestRoundTrip_ImageMetadataWithAllFields(t *testing.T) {
	t.Parallel()

	// Create ImageMetadata with maximum field population
	original := &types.ImageMetadata{
		BaseServerMetadata: types.BaseServerMetadata{
			Description:   "Full featured server",
			Transport:     model.TransportTypeStreamableHTTP,
			RepositoryURL: "https://github.com/test/full",
			Status:        "active",
			Tier:          "Official",
			Tools:         []string{"tool1", "tool2", "tool3"},
			Tags:          []string{"tag1", "tag2"},
			Metadata: &types.Metadata{
				Stars:       500,
				LastUpdated: "2025-10-23",
			},
		},
		Image:      "ghcr.io/test/full:v1.0.0",
		TargetPort: 8080,
		EnvVars: []*types.EnvVar{
			{
				Name:        "API_KEY",
				Description: "API Key for authentication",
				Required:    true,
				Secret:      true,
				Default:     "",
			},
			{
				Name:        "LOG_LEVEL",
				Description: "Logging level",
				Required:    false,
				Secret:      false,
				Default:     "info",
			},
		},
	}

	// Round trip
	serverJSON, err := ImageMetadataToServerJSON("full-server", original)
	require.NoError(t, err)

	result, err := ServerJSONToImageMetadata(serverJSON)
	require.NoError(t, err)

	// Verify all fields preserved
	assert.Equal(t, original.Image, result.Image)
	assert.Equal(t, original.Description, result.Description)
	assert.Equal(t, original.Transport, result.Transport)
	assert.Equal(t, original.RepositoryURL, result.RepositoryURL)
	assert.Equal(t, original.Status, result.Status)
	assert.Equal(t, original.Tier, result.Tier)
	assert.Equal(t, original.Tools, result.Tools)
	assert.Equal(t, original.Tags, result.Tags)
	assert.Equal(t, original.TargetPort, result.TargetPort)

	require.Len(t, result.EnvVars, len(original.EnvVars))
	for i := range original.EnvVars {
		assert.Equal(t, original.EnvVars[i].Name, result.EnvVars[i].Name)
		assert.Equal(t, original.EnvVars[i].Description, result.EnvVars[i].Description)
		assert.Equal(t, original.EnvVars[i].Required, result.EnvVars[i].Required)
		assert.Equal(t, original.EnvVars[i].Secret, result.EnvVars[i].Secret)
		assert.Equal(t, original.EnvVars[i].Default, result.EnvVars[i].Default)
	}

	require.NotNil(t, result.Metadata)
	assert.Equal(t, original.Metadata.Stars, result.Metadata.Stars)
	assert.Equal(t, original.Metadata.LastUpdated, result.Metadata.LastUpdated)
}

// TestRealWorld_GitHubServer tests conversion using the actual GitHub MCP server data as a template
// This test verifies that our converters can handle real-world production data correctly
func TestRealWorld_GitHubServer(t *testing.T) {
	t.Parallel()

	// Create the official ServerJSON format (from the actual registry)
	officialFormat := &upstream.ServerJSON{
		Schema:      model.CurrentSchemaURL,
		Name:        "io.github.github/github-mcp-server",
		Description: "Connect AI assistants to GitHub - manage repos, issues, PRs, and workflows through natural language.",
		Version:     "0.19.1",
		Repository: &model.Repository{
			URL:    "https://github.com/github/github-mcp-server",
			Source: "github",
		},
		Packages: []model.Package{
			{
				RegistryType: model.RegistryTypeOCI,
				Identifier:   "ghcr.io/github/github-mcp-server:0.19.1",
				Transport: model.Transport{
					Type: model.TransportTypeStdio,
				},
				EnvironmentVariables: []model.KeyValueInput{
					{
						Name: "GITHUB_PERSONAL_ACCESS_TOKEN",
						InputWithVariables: model.InputWithVariables{
							Input: model.Input{
								Description: "Your GitHub personal access token with appropriate scopes.",
								IsRequired:  true,
								IsSecret:    true,
							},
						},
					},
				},
			},
		},
		Meta: &upstream.ServerMeta{
			PublisherProvided: map[string]interface{}{
				"io.github.stacklok": map[string]interface{}{
					"ghcr.io/github/github-mcp-server:0.19.1": map[string]interface{}{
						"status": "active",
						"tier":   "Official",
						"tools": []interface{}{
							"add_comment_to_pending_review", "add_issue_comment", "add_sub_issue",
							"assign_copilot_to_issue", "create_branch", "create_issue",
							"create_or_update_file", "create_pull_request", "create_repository",
							"delete_file", "fork_repository", "get_commit", "get_file_contents",
							"get_issue", "get_issue_comments", "get_label", "get_latest_release",
							"get_me", "get_release_by_tag", "get_tag", "get_team_members",
							"get_teams", "list_branches", "list_commits", "list_issue_types",
							"list_issues", "list_label", "list_pull_requests", "list_releases",
							"list_sub_issues", "list_tags", "merge_pull_request",
							"pull_request_read", "pull_request_review_write", "push_files",
							"remove_sub_issue", "reprioritize_sub_issue", "request_copilot_review",
							"search_code", "search_issues", "search_pull_requests",
							"search_repositories", "search_users", "update_issue",
							"update_pull_request", "update_pull_request_branch",
						},
						"tags": []interface{}{
							"api", "create", "fork", "github", "list",
							"pull-request", "push", "repository", "search", "update", "issues",
						},
						"metadata": map[string]interface{}{
							"stars":        float64(23700),
							"last_updated": "2025-10-18T02:26:51Z",
						},
					},
				},
			},
		},
	}

	// Convert official format to ImageMetadata
	imageMetadata, err := ServerJSONToImageMetadata(officialFormat)
	require.NoError(t, err, "Should convert official format to ImageMetadata")
	require.NotNil(t, imageMetadata)

	// Verify core fields
	assert.Equal(t, "Connect AI assistants to GitHub - manage repos, issues, PRs, and workflows through natural language.", imageMetadata.Description)
	assert.Equal(t, "stdio", imageMetadata.Transport)
	assert.Equal(t, "ghcr.io/github/github-mcp-server:0.19.1", imageMetadata.Image)
	assert.Equal(t, "https://github.com/github/github-mcp-server", imageMetadata.RepositoryURL)

	// Verify environment variables
	require.Len(t, imageMetadata.EnvVars, 1)
	assert.Equal(t, "GITHUB_PERSONAL_ACCESS_TOKEN", imageMetadata.EnvVars[0].Name)
	assert.Equal(t, "Your GitHub personal access token with appropriate scopes.", imageMetadata.EnvVars[0].Description)
	assert.True(t, imageMetadata.EnvVars[0].Required)
	assert.True(t, imageMetadata.EnvVars[0].Secret)

	// Verify publisher extensions were extracted
	assert.Equal(t, "active", imageMetadata.Status)
	assert.Equal(t, "Official", imageMetadata.Tier)
	require.Len(t, imageMetadata.Tools, 46, "Should have 46 tools")
	assert.Contains(t, imageMetadata.Tools, "create_pull_request")
	assert.Contains(t, imageMetadata.Tools, "search_repositories")
	require.Len(t, imageMetadata.Tags, 11, "Should have 11 tags")
	assert.Contains(t, imageMetadata.Tags, "github")
	assert.Contains(t, imageMetadata.Tags, "pull-request")

	// Verify metadata
	require.NotNil(t, imageMetadata.Metadata)
	assert.Equal(t, 23700, imageMetadata.Metadata.Stars)
	assert.Equal(t, "2025-10-18T02:26:51Z", imageMetadata.Metadata.LastUpdated)

	// Test round-trip: Convert back to ServerJSON
	resultServerJSON, err := ImageMetadataToServerJSON("github-mcp-server", imageMetadata)
	require.NoError(t, err, "Should convert ImageMetadata back to ServerJSON")
	require.NotNil(t, resultServerJSON)

	// Verify round-trip preserved core data (including original canonical name)
	assert.Equal(t, "io.github.github/github-mcp-server", resultServerJSON.Name)
	assert.Equal(t, officialFormat.Description, resultServerJSON.Description)
	assert.Equal(t, officialFormat.Repository.URL, resultServerJSON.Repository.URL)

	// Verify packages
	require.Len(t, resultServerJSON.Packages, 1)
	assert.Equal(t, model.RegistryTypeOCI, resultServerJSON.Packages[0].RegistryType)
	assert.Equal(t, "ghcr.io/github/github-mcp-server:0.19.1", resultServerJSON.Packages[0].Identifier)
	assert.Equal(t, model.TransportTypeStdio, resultServerJSON.Packages[0].Transport.Type)

	// Verify publisher extensions are present in round-trip
	require.NotNil(t, resultServerJSON.Meta)
	require.NotNil(t, resultServerJSON.Meta.PublisherProvided)
	stacklokData, ok := resultServerJSON.Meta.PublisherProvided["io.github.stacklok"].(map[string]interface{})
	require.True(t, ok, "Should have io.github.stacklok namespace")
	imageData, ok := stacklokData["ghcr.io/github/github-mcp-server:0.19.1"].(map[string]interface{})
	require.True(t, ok, "Should have image-specific extensions")

	// Verify extensions preserved
	assert.Equal(t, "active", imageData["status"])
	assert.Equal(t, "Official", imageData["tier"])

	// Verify tools are preserved as interface slice
	tools, ok := imageData["tools"].([]interface{})
	require.True(t, ok, "Tools should be []interface{}")
	assert.Len(t, tools, 46)

	// Verify tags are preserved
	tags, ok := imageData["tags"].([]interface{})
	require.True(t, ok, "Tags should be []interface{}")
	assert.Len(t, tags, 11)

	// Verify metadata is preserved
	metadata, ok := imageData["metadata"].(map[string]interface{})
	require.True(t, ok, "Metadata should be present")
	assert.Equal(t, float64(23700), metadata["stars"])
	assert.Equal(t, "2025-10-18T02:26:51Z", metadata["last_updated"])
}

// TestRealWorld_GitHubServer_ExactData tests conversion using EXACT data from the user
// This uses the actual JSON strings provided to verify visual correctness
func TestRealWorld_GitHubServer_ExactData(t *testing.T) {
	t.Parallel()

	// EXACT ImageMetadata format as provided by user (from build/registry.json)
	imageMetadataJSON := `{
  "description": "Provides integration with GitHub's APIs",
  "tier": "Official",
  "status": "Active",
  "transport": "stdio",
  "tools": [
    "add_comment_to_pending_review",
    "add_issue_comment",
    "add_sub_issue",
    "assign_copilot_to_issue",
    "create_branch",
    "create_issue",
    "create_or_update_file",
    "create_pull_request",
    "create_repository",
    "delete_file",
    "fork_repository",
    "get_commit",
    "get_file_contents",
    "get_issue",
    "get_issue_comments",
    "get_label",
    "get_latest_release",
    "get_me",
    "get_release_by_tag",
    "get_tag",
    "get_team_members",
    "get_teams",
    "list_branches",
    "list_commits",
    "list_issue_types",
    "list_issues",
    "list_label",
    "list_pull_requests",
    "list_releases",
    "list_sub_issues",
    "list_tags",
    "merge_pull_request",
    "pull_request_read",
    "pull_request_review_write",
    "push_files",
    "remove_sub_issue",
    "reprioritize_sub_issue",
    "request_copilot_review",
    "search_code",
    "search_issues",
    "search_pull_requests",
    "search_repositories",
    "search_users",
    "update_issue",
    "update_pull_request",
    "update_pull_request_branch"
  ],
  "metadata": {
    "stars": 23700,
    "last_updated": "2025-10-18T02:26:51Z"
  },
  "repository_url": "https://github.com/github/github-mcp-server",
  "tags": [
    "api",
    "create",
    "fork",
    "github",
    "list",
    "pull-request",
    "push",
    "repository",
    "search",
    "update",
    "issues"
  ],
  "image": "ghcr.io/github/github-mcp-server:v0.19.1",
  "permissions": {
    "network": {
      "outbound": {
        "allow_host": [
          ".github.com",
          ".githubusercontent.com"
        ],
        "allow_port": [
          443
        ]
      }
    }
  },
  "env_vars": [
    {
      "name": "GITHUB_PERSONAL_ACCESS_TOKEN",
      "description": "GitHub personal access token with appropriate permissions",
      "required": true,
      "secret": true
    },
    {
      "name": "GITHUB_HOST",
      "description": "GitHub Enterprise Server hostname (optional)",
      "required": false
    },
    {
      "name": "GITHUB_TOOLSETS",
      "description": "Comma-separated list of toolsets to enable (e.g., 'repos,issues,pull_requests'). If not set, all toolsets are enabled. See the README for available toolsets.",
      "required": false
    },
    {
      "name": "GITHUB_DYNAMIC_TOOLSETS",
      "description": "Set to '1' to enable dynamic toolset discovery",
      "required": false
    },
    {
      "name": "GITHUB_READ_ONLY",
      "description": "Set to '1' to enable read-only mode, preventing any modifications to GitHub resources",
      "required": false
    }
  ],
  "provenance": {
    "sigstore_url": "tuf-repo-cdn.sigstore.dev",
    "repository_uri": "https://github.com/github/github-mcp-server",
    "signer_identity": "/.github/workflows/docker-publish.yml",
    "runner_environment": "github-hosted",
    "cert_issuer": "https://token.actions.githubusercontent.com"
  }
}`

	// Parse ImageMetadata JSON
	var imageMetadata types.ImageMetadata
	err := json.Unmarshal([]byte(imageMetadataJSON), &imageMetadata)
	require.NoError(t, err, "Should parse ImageMetadata JSON")

	// Log the parsed structure for visual inspection
	t.Logf("Parsed ImageMetadata:")
	t.Logf("  Description: %s", imageMetadata.Description)
	t.Logf("  Image: %s", imageMetadata.Image)
	t.Logf("  Status: %s", imageMetadata.Status)
	t.Logf("  Tier: %s", imageMetadata.Tier)
	t.Logf("  Tools: %d items", len(imageMetadata.Tools))
	t.Logf("  EnvVars: %d items", len(imageMetadata.EnvVars))
	t.Logf("  Tags: %d items", len(imageMetadata.Tags))

	// Verify parsed data matches expectations
	assert.Equal(t, "Provides integration with GitHub's APIs", imageMetadata.Description)
	assert.Equal(t, "ghcr.io/github/github-mcp-server:v0.19.1", imageMetadata.Image)
	assert.Equal(t, "Active", imageMetadata.Status)
	assert.Equal(t, "Official", imageMetadata.Tier)
	assert.Equal(t, "stdio", imageMetadata.Transport)
	assert.Len(t, imageMetadata.Tools, 46)
	assert.Len(t, imageMetadata.EnvVars, 5)
	assert.Len(t, imageMetadata.Tags, 11)
	assert.NotNil(t, imageMetadata.Permissions)
	assert.NotNil(t, imageMetadata.Provenance)

	// Convert to official ServerJSON format
	serverJSON, err := ImageMetadataToServerJSON("github", &imageMetadata)
	require.NoError(t, err, "Should convert ImageMetadata to ServerJSON")
	require.NotNil(t, serverJSON)

	// Marshal to JSON for visual inspection
	serverJSONBytes, err := json.MarshalIndent(serverJSON, "", "  ")
	require.NoError(t, err)
	t.Logf("\n\nConverted to Official ServerJSON:\n%s", string(serverJSONBytes))

	// Verify official format structure
	assert.Equal(t, model.CurrentSchemaURL, serverJSON.Schema)
	assert.Equal(t, "io.github.stacklok/github", serverJSON.Name)
	assert.Equal(t, "Provides integration with GitHub's APIs", serverJSON.Description)
	require.Len(t, serverJSON.Packages, 1)
	assert.Equal(t, "ghcr.io/github/github-mcp-server:v0.19.1", serverJSON.Packages[0].Identifier)
	assert.Len(t, serverJSON.Packages[0].EnvironmentVariables, 5)

	// Verify publisher extensions contain all the extra data
	require.NotNil(t, serverJSON.Meta)
	require.NotNil(t, serverJSON.Meta.PublisherProvided)
	stacklokData, ok := serverJSON.Meta.PublisherProvided["io.github.stacklok"].(map[string]interface{})
	require.True(t, ok)
	extensions, ok := stacklokData["ghcr.io/github/github-mcp-server:v0.19.1"].(map[string]interface{})
	require.True(t, ok)

	// Verify extensions
	assert.Equal(t, "Active", extensions["status"])
	assert.Equal(t, "Official", extensions["tier"])
	assert.NotNil(t, extensions["tools"])
	assert.NotNil(t, extensions["tags"])
	assert.NotNil(t, extensions["metadata"])
	assert.NotNil(t, extensions["permissions"])
	assert.NotNil(t, extensions["provenance"])

	// Test round-trip: Convert back to ImageMetadata
	roundTripImageMetadata, err := ServerJSONToImageMetadata(serverJSON)
	require.NoError(t, err, "Should convert ServerJSON back to ImageMetadata")
	require.NotNil(t, roundTripImageMetadata)

	// Marshal round-trip result for visual inspection
	roundTripBytes, err := json.MarshalIndent(roundTripImageMetadata, "", "  ")
	require.NoError(t, err)
	t.Logf("\n\nRound-trip back to ImageMetadata:\n%s", string(roundTripBytes))

	// Verify round-trip preserved all data
	assert.Equal(t, imageMetadata.Description, roundTripImageMetadata.Description)
	assert.Equal(t, imageMetadata.Image, roundTripImageMetadata.Image)
	assert.Equal(t, imageMetadata.Status, roundTripImageMetadata.Status)
	assert.Equal(t, imageMetadata.Tier, roundTripImageMetadata.Tier)
	assert.Equal(t, imageMetadata.Transport, roundTripImageMetadata.Transport)
	assert.Equal(t, imageMetadata.RepositoryURL, roundTripImageMetadata.RepositoryURL)
	assert.Equal(t, imageMetadata.Tools, roundTripImageMetadata.Tools)
	assert.Equal(t, imageMetadata.Tags, roundTripImageMetadata.Tags)
	assert.Len(t, roundTripImageMetadata.EnvVars, 5)

	// Verify all 5 env vars
	envVarNames := []string{}
	for _, ev := range roundTripImageMetadata.EnvVars {
		envVarNames = append(envVarNames, ev.Name)
	}
	assert.Contains(t, envVarNames, "GITHUB_PERSONAL_ACCESS_TOKEN")
	assert.Contains(t, envVarNames, "GITHUB_HOST")
	assert.Contains(t, envVarNames, "GITHUB_TOOLSETS")
	assert.Contains(t, envVarNames, "GITHUB_DYNAMIC_TOOLSETS")
	assert.Contains(t, envVarNames, "GITHUB_READ_ONLY")

	// Verify metadata preserved
	require.NotNil(t, roundTripImageMetadata.Metadata)
	assert.Equal(t, 23700, roundTripImageMetadata.Metadata.Stars)

	// Verify permissions and provenance are preserved through the round-trip
	assert.NotNil(t, roundTripImageMetadata.Permissions)
	assert.NotNil(t, roundTripImageMetadata.Provenance)
}

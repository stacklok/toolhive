// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package converters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	upstream "github.com/modelcontextprotocol/registry/pkg/api/v0"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	types "github.com/stacklok/toolhive/pkg/registry/registry"
)

// TestConverters_Fixtures validates converter functions using JSON fixture files
// This provides a clear, maintainable way to test conversions with real-world data
func TestConverters_Fixtures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		fixtureDir   string
		inputFile    string
		expectedFile string
		serverName   string
		convertFunc  string // "ImageToServer", "ServerToImage", "RemoteToServer", "ServerToRemote"
		validateFunc func(t *testing.T, input, output []byte)
	}{
		{
			name:         "ImageMetadata to ServerJSON - GitHub",
			fixtureDir:   "testdata/image_to_server",
			inputFile:    "input_github.json",
			expectedFile: "expected_github.json",
			serverName:   "github",
			convertFunc:  "ImageToServer",
			validateFunc: validateImageToServerConversion,
		},
		{
			name:         "ServerJSON to ImageMetadata - GitHub",
			fixtureDir:   "testdata/server_to_image",
			inputFile:    "input_github.json",
			expectedFile: "expected_github.json",
			serverName:   "",
			convertFunc:  "ServerToImage",
			validateFunc: validateServerToImageConversion,
		},
		{
			name:         "RemoteServerMetadata to ServerJSON - Example",
			fixtureDir:   "testdata/remote_to_server",
			inputFile:    "input_example.json",
			expectedFile: "expected_example.json",
			serverName:   "example-remote",
			convertFunc:  "RemoteToServer",
			validateFunc: validateRemoteToServerConversion,
		},
		{
			name:         "ServerJSON to RemoteServerMetadata - Example",
			fixtureDir:   "testdata/server_to_remote",
			inputFile:    "input_example.json",
			expectedFile: "expected_example.json",
			serverName:   "",
			convertFunc:  "ServerToRemote",
			validateFunc: validateServerToRemoteConversion,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Read input fixture
			inputPath := filepath.Join(tt.fixtureDir, tt.inputFile)
			inputData, err := os.ReadFile(inputPath)
			require.NoError(t, err, "Failed to read input fixture: %s", inputPath)

			// Read expected output fixture
			expectedPath := filepath.Join(tt.fixtureDir, tt.expectedFile)
			expectedData, err := os.ReadFile(expectedPath)
			require.NoError(t, err, "Failed to read expected fixture: %s", expectedPath)

			// Perform conversion based on type
			var actualData []byte
			switch tt.convertFunc {
			case "ImageToServer":
				actualData = convertImageToServer(t, inputData, tt.serverName)
			case "ServerToImage":
				actualData = convertServerToImage(t, inputData)
			case "RemoteToServer":
				actualData = convertRemoteToServer(t, inputData, tt.serverName)
			case "ServerToRemote":
				actualData = convertServerToRemote(t, inputData)
			default:
				t.Fatalf("Unknown conversion function: %s", tt.convertFunc)
			}

			// Compare output with expected
			var expected, actual interface{}
			require.NoError(t, json.Unmarshal(expectedData, &expected), "Failed to parse expected JSON")
			require.NoError(t, json.Unmarshal(actualData, &actual), "Failed to parse actual JSON")

			// Deep equal comparison
			assert.Equal(t, expected, actual, "Conversion output doesn't match expected fixture")

			// Run additional validation if provided
			if tt.validateFunc != nil {
				tt.validateFunc(t, inputData, actualData)
			}
		})
	}
}

// Helper functions for conversions

func convertImageToServer(t *testing.T, inputData []byte, serverName string) []byte {
	t.Helper()
	var imageMetadata types.ImageMetadata
	require.NoError(t, json.Unmarshal(inputData, &imageMetadata))

	serverJSON, err := ImageMetadataToServerJSON(serverName, &imageMetadata)
	require.NoError(t, err)

	output, err := json.MarshalIndent(serverJSON, "", "  ")
	require.NoError(t, err)
	return output
}

func convertServerToImage(t *testing.T, inputData []byte) []byte {
	t.Helper()
	var serverJSON upstream.ServerJSON
	require.NoError(t, json.Unmarshal(inputData, &serverJSON))

	imageMetadata, err := ServerJSONToImageMetadata(&serverJSON)
	require.NoError(t, err)

	output, err := json.MarshalIndent(imageMetadata, "", "  ")
	require.NoError(t, err)
	return output
}

func convertRemoteToServer(t *testing.T, inputData []byte, serverName string) []byte {
	t.Helper()
	var remoteMetadata types.RemoteServerMetadata
	require.NoError(t, json.Unmarshal(inputData, &remoteMetadata))

	serverJSON, err := RemoteServerMetadataToServerJSON(serverName, &remoteMetadata)
	require.NoError(t, err)

	output, err := json.MarshalIndent(serverJSON, "", "  ")
	require.NoError(t, err)
	return output
}

func convertServerToRemote(t *testing.T, inputData []byte) []byte {
	t.Helper()
	var serverJSON upstream.ServerJSON
	require.NoError(t, json.Unmarshal(inputData, &serverJSON))

	remoteMetadata, err := ServerJSONToRemoteServerMetadata(&serverJSON)
	require.NoError(t, err)

	output, err := json.MarshalIndent(remoteMetadata, "", "  ")
	require.NoError(t, err)
	return output
}

// Validation functions - additional checks beyond JSON equality

// getServerJSONExtensions extracts the stacklok extensions from a ServerJSON by key (image ref or URL)
func getServerJSONExtensions(t *testing.T, serverJSON *upstream.ServerJSON, key string) map[string]interface{} {
	t.Helper()

	if serverJSON.Meta == nil || serverJSON.Meta.PublisherProvided == nil {
		return nil
	}

	stacklokData, ok := serverJSON.Meta.PublisherProvided["io.github.stacklok"].(map[string]interface{})
	if !ok {
		return nil
	}

	extensions, ok := stacklokData[key].(map[string]interface{})
	if !ok {
		return nil
	}

	return extensions
}

func validateImageToServerConversion(t *testing.T, inputData, outputData []byte) {
	t.Helper()
	var input types.ImageMetadata
	var output upstream.ServerJSON

	require.NoError(t, json.Unmarshal(inputData, &input))
	require.NoError(t, json.Unmarshal(outputData, &output))

	// Verify core mappings
	assert.Equal(t, input.Description, output.Description, "Description should match")
	assert.Equal(t, input.Title, output.Title, "Title should match")
	assert.Len(t, output.Packages, 1, "Should have exactly one package")
	assert.Equal(t, input.Image, output.Packages[0].Identifier, "Image identifier should match")
	assert.Equal(t, input.Transport, output.Packages[0].Transport.Type, "Transport type should match")

	// Verify environment variables count
	assert.Len(t, output.Packages[0].EnvironmentVariables, len(input.EnvVars),
		"Environment variables count should match")

	// Verify publisher extensions exist
	extensions := getServerJSONExtensions(t, &output, input.Image)
	require.NotNil(t, extensions, "Extensions should exist for image")

	// Verify key extension fields
	assert.Equal(t, input.Status, extensions["status"], "Status should be in extensions")
	assert.Equal(t, input.Tier, extensions["tier"], "Tier should be in extensions")
	assert.NotNil(t, extensions["tools"], "Tools should be in extensions")
	assert.NotNil(t, extensions["tags"], "Tags should be in extensions")

	// Verify overview and tool_definitions if present
	if input.Overview != "" {
		assert.Equal(t, input.Overview, extensions["overview"], "Overview should be in extensions")
	}
	if len(input.ToolDefinitions) > 0 {
		assert.NotNil(t, extensions["tool_definitions"], "tool_definitions should be in extensions")
	}

	// Verify docker_tags if present
	if len(input.DockerTags) > 0 {
		assert.NotNil(t, extensions["docker_tags"], "docker_tags should be in extensions")
	}

	// Verify proxy_port if present
	if input.ProxyPort > 0 {
		assert.NotNil(t, extensions["proxy_port"], "proxy_port should be in extensions")
	}

	// Verify custom_metadata if present
	if len(input.CustomMetadata) > 0 {
		assert.NotNil(t, extensions["custom_metadata"], "custom_metadata should be in extensions")
	}
}

func validateServerToImageConversion(t *testing.T, inputData, outputData []byte) {
	t.Helper()
	var input upstream.ServerJSON
	var output types.ImageMetadata

	require.NoError(t, json.Unmarshal(inputData, &input))
	require.NoError(t, json.Unmarshal(outputData, &output))

	// Verify core mappings
	assert.Equal(t, input.Description, output.Description, "Description should match")
	assert.Equal(t, input.Title, output.Title, "Title should match")
	require.Len(t, input.Packages, 1, "Input should have exactly one package")
	assert.Equal(t, input.Packages[0].Identifier, output.Image, "Image identifier should match")
	assert.Equal(t, input.Packages[0].Transport.Type, output.Transport, "Transport type should match")

	// Verify environment variables were extracted
	assert.Len(t, output.EnvVars, len(input.Packages[0].EnvironmentVariables),
		"Environment variables count should match")

	// Verify new fields were extracted from extensions if present
	extensions := getServerJSONExtensions(t, &input, output.Image)
	if extensions != nil {
		if _, hasDockerTags := extensions["docker_tags"]; hasDockerTags {
			assert.NotNil(t, output.DockerTags, "DockerTags should be extracted from extensions")
			assert.Greater(t, len(output.DockerTags), 0, "DockerTags should not be empty")
		}
		if _, hasProxyPort := extensions["proxy_port"]; hasProxyPort {
			assert.Greater(t, output.ProxyPort, 0, "ProxyPort should be extracted from extensions")
		}
		if _, hasCustomMetadata := extensions["custom_metadata"]; hasCustomMetadata {
			assert.NotNil(t, output.CustomMetadata, "CustomMetadata should be extracted from extensions")
			assert.Greater(t, len(output.CustomMetadata), 0, "CustomMetadata should not be empty")
		}
		if _, hasOverview := extensions["overview"]; hasOverview {
			assert.NotEmpty(t, output.Overview, "Overview should be extracted from extensions")
		}
		if _, hasToolDefs := extensions["tool_definitions"]; hasToolDefs {
			assert.NotNil(t, output.ToolDefinitions, "ToolDefinitions should be extracted from extensions")
			assert.Greater(t, len(output.ToolDefinitions), 0, "ToolDefinitions should not be empty")
		}
	}
}

func validateRemoteToServerConversion(t *testing.T, inputData, outputData []byte) {
	t.Helper()
	var input types.RemoteServerMetadata
	var output upstream.ServerJSON

	require.NoError(t, json.Unmarshal(inputData, &input))
	require.NoError(t, json.Unmarshal(outputData, &output))

	// Verify core mappings
	assert.Equal(t, input.Description, output.Description, "Description should match")
	assert.Equal(t, input.Title, output.Title, "Title should match")
	require.Len(t, output.Remotes, 1, "Should have exactly one remote")
	assert.Equal(t, input.URL, output.Remotes[0].URL, "Remote URL should match")
	assert.Equal(t, input.Transport, output.Remotes[0].Type, "Transport type should match")

	// Verify headers count
	assert.Len(t, output.Remotes[0].Headers, len(input.Headers),
		"Headers count should match")

	// Get extensions once and verify all fields
	extensions := getServerJSONExtensions(t, &output, input.URL)

	// Verify overview and tool_definitions if present
	if input.Overview != "" {
		require.NotNil(t, extensions, "Extensions should exist when overview is present")
		assert.Equal(t, input.Overview, extensions["overview"], "Overview should be in extensions")
	}
	if len(input.ToolDefinitions) > 0 {
		require.NotNil(t, extensions, "Extensions should exist when tool_definitions are present")
		assert.NotNil(t, extensions["tool_definitions"], "tool_definitions should be in extensions")
	}

	// Verify env_vars if input has them
	if len(input.EnvVars) > 0 {
		require.NotNil(t, extensions, "Extensions should exist when env_vars are present")
		assert.NotNil(t, extensions["env_vars"], "env_vars should be in extensions")
	}

	// Verify oauth_config if present
	if input.OAuthConfig != nil {
		require.NotNil(t, extensions, "Extensions should exist when oauth_config is present")
		assert.NotNil(t, extensions["oauth_config"], "oauth_config should be in extensions")
	}

	// Verify custom_metadata if present
	if len(input.CustomMetadata) > 0 {
		require.NotNil(t, extensions, "Extensions should exist when custom_metadata is present")
		assert.NotNil(t, extensions["custom_metadata"], "custom_metadata should be in extensions")
	}
}

func validateServerToRemoteConversion(t *testing.T, inputData, outputData []byte) {
	t.Helper()
	var input upstream.ServerJSON
	var output types.RemoteServerMetadata

	require.NoError(t, json.Unmarshal(inputData, &input))
	require.NoError(t, json.Unmarshal(outputData, &output))

	// Verify core mappings
	assert.Equal(t, input.Description, output.Description, "Description should match")
	assert.Equal(t, input.Title, output.Title, "Title should match")
	require.Len(t, input.Remotes, 1, "Input should have exactly one remote")
	assert.Equal(t, input.Remotes[0].URL, output.URL, "Remote URL should match")
	assert.Equal(t, input.Remotes[0].Type, output.Transport, "Transport type should match")

	// Verify headers were extracted
	assert.Len(t, output.Headers, len(input.Remotes[0].Headers),
		"Headers count should match")

	// Verify fields were extracted from extensions if present
	extensions := getServerJSONExtensions(t, &input, output.URL)
	if extensions != nil {
		if _, hasOverview := extensions["overview"]; hasOverview {
			assert.NotEmpty(t, output.Overview, "Overview should be extracted from extensions")
		}
		if _, hasToolDefs := extensions["tool_definitions"]; hasToolDefs {
			assert.NotNil(t, output.ToolDefinitions, "ToolDefinitions should be extracted from extensions")
			assert.Greater(t, len(output.ToolDefinitions), 0, "ToolDefinitions should not be empty")
		}
		if _, hasEnvVars := extensions["env_vars"]; hasEnvVars {
			assert.NotNil(t, output.EnvVars, "EnvVars should be extracted from extensions")
			assert.Greater(t, len(output.EnvVars), 0, "EnvVars should not be empty")
		}
		if _, hasOAuth := extensions["oauth_config"]; hasOAuth {
			assert.NotNil(t, output.OAuthConfig, "OAuthConfig should be extracted from extensions")
		}
		if _, hasCustomMetadata := extensions["custom_metadata"]; hasCustomMetadata {
			assert.NotNil(t, output.CustomMetadata, "CustomMetadata should be extracted from extensions")
			assert.Greater(t, len(output.CustomMetadata), 0, "CustomMetadata should not be empty")
		}
	}
}

package registry

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistryWithRemoteServers(t *testing.T) {
	t.Parallel()
	registry := &Registry{
		Version:     "1.0.0",
		LastUpdated: time.Now().Format(time.RFC3339),
		Servers: map[string]*ImageMetadata{
			"container-server": {
				BaseServerMetadata: BaseServerMetadata{
					Name:        "container-server",
					Description: "A containerized MCP server",
					Tier:        "Official",
					Status:      "Active",
					Transport:   "stdio",
					Tools:       []string{"tool1", "tool2"},
				},
				Image:      "mcp/example:latest",
				TargetPort: 8080,
			},
		},
		RemoteServers: map[string]*RemoteServerMetadata{
			"remote-server": {
				BaseServerMetadata: BaseServerMetadata{
					Name:        "remote-server",
					Description: "A remote MCP server",
					Tier:        "Community",
					Status:      "Active",
					Transport:   "sse",
					Tools:       []string{"remote_tool1", "remote_tool2"},
				},
				URL: "https://api.example.com/mcp",
			},
		},
	}

	// Test JSON marshaling
	data, err := json.Marshal(registry)
	require.NoError(t, err)

	// Test JSON unmarshaling
	var decoded Registry
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, registry.Version, decoded.Version)
	assert.Len(t, decoded.Servers, 1)
	assert.Len(t, decoded.RemoteServers, 1)
	assert.Equal(t, "remote-server", decoded.RemoteServers["remote-server"].Name)
	assert.Equal(t, "https://api.example.com/mcp", decoded.RemoteServers["remote-server"].URL)
}

func TestRemoteServerMetadataWithHeaders(t *testing.T) {
	t.Parallel()
	remote := &RemoteServerMetadata{
		BaseServerMetadata: BaseServerMetadata{
			Name:        "auth-server",
			Description: "Remote server with authentication headers",
			Tier:        "Official",
			Status:      "Active",
			Transport:   "sse",
			Tools:       []string{"secure_tool"},
		},
		URL: "https://secure.example.com/mcp",
		Headers: []*Header{
			{
				Name:        "X-API-Key",
				Description: "API key for authentication",
				Required:    true,
				Secret:      true,
			},
			{
				Name:        "X-Region",
				Description: "Service region",
				Required:    false,
				Default:     "us-east-1",
				Choices:     []string{"us-east-1", "eu-west-1"},
			},
		},
	}

	// Test JSON marshaling
	data, err := json.Marshal(remote)
	require.NoError(t, err)

	// Test JSON unmarshaling
	var decoded RemoteServerMetadata
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, remote.URL, decoded.URL)
	assert.Len(t, decoded.Headers, 2)
	assert.Equal(t, "X-API-Key", decoded.Headers[0].Name)
	assert.True(t, decoded.Headers[0].Required)
	assert.True(t, decoded.Headers[0].Secret)
	assert.Equal(t, "us-east-1", decoded.Headers[1].Default)
	assert.Equal(t, []string{"us-east-1", "eu-west-1"}, decoded.Headers[1].Choices)
}

func TestRemoteServerMetadataWithOAuth(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		remote *RemoteServerMetadata
	}{
		{
			name: "OIDC configuration",
			remote: &RemoteServerMetadata{
				BaseServerMetadata: BaseServerMetadata{
					Name:        "oidc-server",
					Description: "Remote server with OIDC authentication",
					Tier:        "Official",
					Status:      "Active",
					Transport:   "streamable-http",
					Tools:       []string{"oidc_tool"},
				},
				URL: "https://oidc.example.com/mcp",
				OAuthConfig: &OAuthConfig{
					Issuer:   "https://auth.example.com",
					ClientID: "mcp-client-id",
					Scopes:   []string{"openid", "profile", "email"},
					UsePKCE:  true,
				},
			},
		},
		{
			name: "Manual OAuth configuration",
			remote: &RemoteServerMetadata{
				BaseServerMetadata: BaseServerMetadata{
					Name:        "oauth-server",
					Description: "Remote server with manual OAuth endpoints",
					Tier:        "Community",
					Status:      "Active",
					Transport:   "sse",
					Tools:       []string{"oauth_tool"},
				},
				URL: "https://oauth.example.com/mcp",
				OAuthConfig: &OAuthConfig{
					AuthorizeURL: "https://custom.example.com/oauth/authorize",
					TokenURL:     "https://custom.example.com/oauth/token",
					ClientID:     "custom-client-id",
					Scopes:       []string{"read", "write"},
					UsePKCE:      false,
				},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Test JSON marshaling
			data, err := json.Marshal(tt.remote)
			require.NoError(t, err)

			// Test JSON unmarshaling
			var decoded RemoteServerMetadata
			err = json.Unmarshal(data, &decoded)
			require.NoError(t, err)

			assert.Equal(t, tt.remote.URL, decoded.URL)
			assert.NotNil(t, decoded.OAuthConfig)
			assert.Equal(t, tt.remote.OAuthConfig.ClientID, decoded.OAuthConfig.ClientID)
			assert.Equal(t, tt.remote.OAuthConfig.Scopes, decoded.OAuthConfig.Scopes)
			assert.Equal(t, tt.remote.OAuthConfig.UsePKCE, decoded.OAuthConfig.UsePKCE)

			if tt.remote.OAuthConfig.Issuer != "" {
				assert.Equal(t, tt.remote.OAuthConfig.Issuer, decoded.OAuthConfig.Issuer)
			}
			if tt.remote.OAuthConfig.AuthorizeURL != "" {
				assert.Equal(t, tt.remote.OAuthConfig.AuthorizeURL, decoded.OAuthConfig.AuthorizeURL)
				assert.Equal(t, tt.remote.OAuthConfig.TokenURL, decoded.OAuthConfig.TokenURL)
			}
		})
	}
}

func TestBaseServerMetadataInheritance(t *testing.T) {
	t.Parallel()
	// Test that both ImageMetadata and RemoteServerMetadata properly inherit BaseServerMetadata
	baseFields := BaseServerMetadata{
		Name:        "test-server",
		Description: "Test server description",
		Tier:        "Official",
		Status:      "Active",
		Transport:   "sse",
		Tools:       []string{"tool1", "tool2"},
		Metadata: &Metadata{
			Stars:       100,
			Pulls:       5000,
			LastUpdated: time.Now().Format(time.RFC3339),
		},
		RepositoryURL: "https://github.com/example/server",
		Tags:          []string{"tag1", "tag2"},
		CustomMetadata: map[string]any{
			"custom_field": "custom_value",
		},
	}

	// Test with ImageMetadata
	image := &ImageMetadata{
		BaseServerMetadata: baseFields,
		Image:              "mcp/test:latest",
	}

	imageData, err := json.Marshal(image)
	require.NoError(t, err)

	var decodedImage ImageMetadata
	err = json.Unmarshal(imageData, &decodedImage)
	require.NoError(t, err)

	assert.Equal(t, baseFields.Name, decodedImage.Name)
	assert.Equal(t, baseFields.Description, decodedImage.Description)
	assert.Equal(t, baseFields.Tier, decodedImage.Tier)
	assert.Equal(t, baseFields.Status, decodedImage.Status)
	assert.Equal(t, baseFields.Transport, decodedImage.Transport)
	assert.Equal(t, baseFields.Tools, decodedImage.Tools)
	assert.Equal(t, "mcp/test:latest", decodedImage.Image)

	// Test with RemoteServerMetadata
	remote := &RemoteServerMetadata{
		BaseServerMetadata: baseFields,
		URL:                "https://api.example.com/mcp",
	}

	remoteData, err := json.Marshal(remote)
	require.NoError(t, err)

	var decodedRemote RemoteServerMetadata
	err = json.Unmarshal(remoteData, &decodedRemote)
	require.NoError(t, err)

	assert.Equal(t, baseFields.Name, decodedRemote.Name)
	assert.Equal(t, baseFields.Description, decodedRemote.Description)
	assert.Equal(t, baseFields.Tier, decodedRemote.Tier)
	assert.Equal(t, baseFields.Status, decodedRemote.Status)
	assert.Equal(t, baseFields.Transport, decodedRemote.Transport)
	assert.Equal(t, baseFields.Tools, decodedRemote.Tools)
	assert.Equal(t, "https://api.example.com/mcp", decodedRemote.URL)
}

func TestRemoteServerTransportValidation(t *testing.T) {
	t.Parallel()
	// Test that remote servers only support sse and streamable-http transports
	validTransports := []string{"sse", "streamable-http"}

	for _, transport := range validTransports {
		remote := &RemoteServerMetadata{
			BaseServerMetadata: BaseServerMetadata{
				Name:        "test-server",
				Description: "Test server",
				Tier:        "Official",
				Status:      "Active",
				Transport:   transport,
				Tools:       []string{"tool"},
			},
			URL: "https://example.com/mcp",
		}

		data, err := json.Marshal(remote)
		require.NoError(t, err)

		var decoded RemoteServerMetadata
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)
		assert.Equal(t, transport, decoded.Transport)
	}

	// Note: stdio transport validation would be enforced by the JSON schema,
	// not by the Go types themselves
}

func TestHeaderSecretField(t *testing.T) {
	t.Parallel()
	header := &Header{
		Name:        "Authorization",
		Description: "Bearer token for authentication",
		Required:    true,
		Secret:      true,
	}

	data, err := json.Marshal(header)
	require.NoError(t, err)

	var decoded Header
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.True(t, decoded.Secret)
	assert.True(t, decoded.Required)
}

func TestMetadataParsedTime(t *testing.T) {
	t.Parallel()
	now := time.Now().Truncate(time.Second)
	metadata := &Metadata{
		Stars:       100,
		Pulls:       5000,
		LastUpdated: now.Format(time.RFC3339),
	}

	parsedTime, err := metadata.ParsedTime()
	require.NoError(t, err)
	assert.Equal(t, now.UTC(), parsedTime.UTC())
}

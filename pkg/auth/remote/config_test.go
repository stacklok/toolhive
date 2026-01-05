package remote

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/logger"
)

func TestDeriveResourceIndicator(t *testing.T) {
	t.Parallel()
	logger.Initialize()

	tests := []struct {
		name             string
		remoteServerURL  string
		expectedResource string
	}{
		{
			name:             "valid remote URL - derive and normalize",
			remoteServerURL:  "https://MCP.Example.COM/api#fragment",
			expectedResource: "https://mcp.example.com/api",
		},
		{
			name:             "remote URL with trailing slash - preserve it",
			remoteServerURL:  "https://mcp.example.com/api/",
			expectedResource: "https://mcp.example.com/api/",
		},
		{
			name:             "remote URL with port - preserve port",
			remoteServerURL:  "https://mcp.example.com:8443/api",
			expectedResource: "https://mcp.example.com:8443/api",
		},
		{
			name:             "empty remote URL - return empty",
			remoteServerURL:  "",
			expectedResource: "",
		},
		{
			name:             "invalid remote URL - return empty",
			remoteServerURL:  "ht!tp://invalid",
			expectedResource: "",
		},
		{
			name:             "derived resource with query params - preserve them",
			remoteServerURL:  "https://mcp.example.com/api?token=abc123",
			expectedResource: "https://mcp.example.com/api?token=abc123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := DefaultResourceIndicator(tt.remoteServerURL)
			assert.Equal(t, tt.expectedResource, got)
		})
	}
}

func TestConfig_BearerTokenFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		bearerToken       string
		bearerTokenFile   string
		bearerTokenEnvVar string
		expectedMsg       string
	}{
		{
			name:        "bearer token from flag",
			bearerToken: "test-token-123",
			expectedMsg: "Bearer token authentication failed. Please restart the server with a new token using --remote-auth-bearer-token",
		},
		{
			name:            "bearer token from file",
			bearerTokenFile: "/path/to/token.txt",
			expectedMsg:     "Bearer token authentication failed. Please restart the server with --remote-auth-bearer-token-file=/path/to/token.txt after updating the token file at /path/to/token.txt",
		},
		{
			name:              "bearer token from env var",
			bearerTokenEnvVar: "BEARER_TOKEN",
			expectedMsg:       "Bearer token authentication failed. Please update the BEARER_TOKEN environment variable and restart the server",
		},
		{
			name:              "all bearer token fields set (file takes precedence)",
			bearerToken:       "flag-token",
			bearerTokenFile:   "/path/to/token.txt",
			bearerTokenEnvVar: "BEARER_TOKEN",
			expectedMsg:       "Bearer token authentication failed. Please restart the server with --remote-auth-bearer-token-file=/path/to/token.txt after updating the token file at /path/to/token.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := &Config{
				BearerToken:       tt.bearerToken,
				BearerTokenFile:   tt.bearerTokenFile,
				BearerTokenEnvVar: tt.bearerTokenEnvVar,
			}

			assert.Equal(t, tt.bearerToken, config.BearerToken)
			assert.Equal(t, tt.bearerTokenFile, config.BearerTokenFile)
			assert.Equal(t, tt.bearerTokenEnvVar, config.BearerTokenEnvVar)
			assert.Equal(t, tt.expectedMsg, config.GetBearerTokenErrorMessage())
		})
	}
}

func TestConfig_UnmarshalJSON_BearerTokenFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		jsonData             string
		expectedBearerToken  string
		expectedBearerFile   string
		expectedBearerEnvVar string
		expectedMsg          string
	}{
		{
			name: "snake_case format with bearer token from flag only",
			jsonData: `{
				"bearer_token": "test-token-123"
			}`,
			expectedBearerToken:  "test-token-123",
			expectedBearerFile:   "",
			expectedBearerEnvVar: "",
			expectedMsg:          "Bearer token authentication failed. Please restart the server with a new token using --remote-auth-bearer-token",
		},
		{
			name: "snake_case format with bearer token from file",
			jsonData: `{
				"bearer_token": "test-token-456",
				"bearer_token_file": "/path/to/token2.txt"
			}`,
			expectedBearerToken:  "test-token-456",
			expectedBearerFile:   "/path/to/token2.txt",
			expectedBearerEnvVar: "",
			expectedMsg:          "Bearer token authentication failed. Please restart the server with --remote-auth-bearer-token-file=/path/to/token2.txt after updating the token file at /path/to/token2.txt",
		},
		{
			name: "PascalCase format with bearer token from file",
			jsonData: `{
				"ClientID": "",
				"BearerToken": "test-token-789",
				"BearerTokenFile": "/path/to/token3.txt"
			}`,
			expectedBearerToken:  "test-token-789",
			expectedBearerFile:   "/path/to/token3.txt",
			expectedBearerEnvVar: "",
			expectedMsg:          "Bearer token authentication failed. Please restart the server with --remote-auth-bearer-token-file=/path/to/token3.txt after updating the token file at /path/to/token3.txt",
		},
		{
			name: "env source type",
			jsonData: `{
				"bearer_token": "env-token",
				"bearer_token_env_var": "BEARER_TOKEN"
			}`,
			expectedBearerToken:  "env-token",
			expectedBearerFile:   "",
			expectedBearerEnvVar: "BEARER_TOKEN",
			expectedMsg:          "Bearer token authentication failed. Please update the BEARER_TOKEN environment variable and restart the server",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var config Config
			err := json.Unmarshal([]byte(tt.jsonData), &config)
			require.NoError(t, err)

			assert.Equal(t, tt.expectedBearerToken, config.BearerToken)
			assert.Equal(t, tt.expectedBearerFile, config.BearerTokenFile)
			assert.Equal(t, tt.expectedBearerEnvVar, config.BearerTokenEnvVar)
			assert.Equal(t, tt.expectedMsg, config.GetBearerTokenErrorMessage())
		})
	}
}

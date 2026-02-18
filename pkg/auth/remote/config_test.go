// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeriveResourceIndicator(t *testing.T) {
	t.Parallel()

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
		name            string
		bearerToken     string
		bearerTokenFile string
	}{
		{
			name:        "bearer token from flag",
			bearerToken: "test-token-123",
		},
		{
			name:            "bearer token from file",
			bearerTokenFile: "/path/to/token.txt",
		},
		{
			name:            "all bearer token fields set",
			bearerToken:     "flag-token",
			bearerTokenFile: "/path/to/token.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := &Config{
				BearerToken:     tt.bearerToken,
				BearerTokenFile: tt.bearerTokenFile,
			}

			assert.Equal(t, tt.bearerToken, config.BearerToken)
			assert.Equal(t, tt.bearerTokenFile, config.BearerTokenFile)
		})
	}
}

func TestBearerTokenEnvVarName(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "TOOLHIVE_REMOTE_AUTH_BEARER_TOKEN", BearerTokenEnvVarName)
}

func TestConfig_UnmarshalJSON_BearerTokenFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		jsonData            string
		expectedBearerToken string
		expectedBearerFile  string
	}{
		{
			name: "snake_case format with bearer token from flag only",
			jsonData: `{
				"bearer_token": "test-token-123"
			}`,
			expectedBearerToken: "test-token-123",
			expectedBearerFile:  "",
		},
		{
			name: "snake_case format with bearer token from file",
			jsonData: `{
				"bearer_token": "test-token-456",
				"bearer_token_file": "/path/to/token2.txt"
			}`,
			expectedBearerToken: "test-token-456",
			expectedBearerFile:  "/path/to/token2.txt",
		},
		{
			name: "PascalCase format with bearer token from file",
			jsonData: `{
				"ClientID": "",
				"BearerToken": "test-token-789",
				"BearerTokenFile": "/path/to/token3.txt"
			}`,
			expectedBearerToken: "test-token-789",
			expectedBearerFile:  "/path/to/token3.txt",
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
		})
	}
}

func TestConfig_HasCachedClientCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		config   Config
		expected bool
	}{
		{
			name:     "no cached credentials",
			config:   Config{},
			expected: false,
		},
		{
			name: "has cached client ID only",
			config: Config{
				CachedClientID: "test_client_id",
			},
			expected: true,
		},
		{
			name: "has both cached credentials",
			config: Config{
				CachedClientID:        "test_client_id",
				CachedClientSecretRef: "OAUTH_CLIENT_SECRET_test",
			},
			expected: true,
		},
		{
			name: "has only cached client secret (invalid state)",
			config: Config{
				CachedClientSecretRef: "OAUTH_CLIENT_SECRET_test",
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := tt.config.HasCachedClientCredentials()
			if result != tt.expected {
				t.Errorf("HasCachedClientCredentials() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestConfig_ClearCachedClientCredentials(t *testing.T) {
	t.Parallel()

	config := Config{
		CachedClientID:        "test_client_id",
		CachedClientSecretRef: "OAUTH_CLIENT_SECRET_test",
	}

	config.ClearCachedClientCredentials()

	if config.CachedClientID != "" {
		t.Errorf("CachedClientID should be empty, got %s", config.CachedClientID)
	}
	if config.CachedClientSecretRef != "" {
		t.Errorf("CachedClientSecretRef should be empty, got %s", config.CachedClientSecretRef)
	}
}

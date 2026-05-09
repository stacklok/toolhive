// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package wirefmt

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

// envVarPattern is the character set every env var name produced by this
// package must conform to. The runtime's secrets.EnvironmentProvider lookups
// expect [A-Z0-9_], so every name we emit MUST round-trip cleanly.
var envVarPattern = regexp.MustCompile(`^[A-Z0-9_]+$`)

func TestNormalizeForEnvVar(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"hyphens become underscores", "my-proxy", "MY_PROXY"},
		{"already uppercase preserved", "FOO_BAR", "FOO_BAR"},
		{"non-alnum collapses to underscore", "proxy.name@123", "PROXY_NAME_123"},
		{"mixed case lowercased to upper", "GitHub-Copilot", "GITHUB_COPILOT"},
		{"empty stays empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := NormalizeForEnvVar(tt.in)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSecretEnvVarName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                     string
		ownerName                string
		headerName               string
		expectedEnvVarName       string
		expectedSecretIdentifier string
	}{
		{
			name:                     "simple names",
			ownerName:                "my-proxy",
			headerName:               "X-API-Key",
			expectedEnvVarName:       "TOOLHIVE_SECRET_HEADER_FORWARD_X_API_KEY_MY_PROXY",
			expectedSecretIdentifier: "HEADER_FORWARD_X_API_KEY_MY_PROXY",
		},
		{
			name:                     "lowercase header upcases",
			ownerName:                "test-proxy",
			headerName:               "authorization",
			expectedEnvVarName:       "TOOLHIVE_SECRET_HEADER_FORWARD_AUTHORIZATION_TEST_PROXY",
			expectedSecretIdentifier: "HEADER_FORWARD_AUTHORIZATION_TEST_PROXY",
		},
		{
			name:                     "multiple hyphens",
			ownerName:                "my-remote-proxy",
			headerName:               "X-Custom-Header",
			expectedEnvVarName:       "TOOLHIVE_SECRET_HEADER_FORWARD_X_CUSTOM_HEADER_MY_REMOTE_PROXY",
			expectedSecretIdentifier: "HEADER_FORWARD_X_CUSTOM_HEADER_MY_REMOTE_PROXY",
		},
		{
			name:                     "special characters in owner name",
			ownerName:                "proxy.name@123",
			headerName:               "X-Token",
			expectedEnvVarName:       "TOOLHIVE_SECRET_HEADER_FORWARD_X_TOKEN_PROXY_NAME_123",
			expectedSecretIdentifier: "HEADER_FORWARD_X_TOKEN_PROXY_NAME_123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			envVarName, secretIdentifier := SecretEnvVarName(tt.ownerName, tt.headerName)

			assert.Equal(t, tt.expectedEnvVarName, envVarName)
			assert.Equal(t, tt.expectedSecretIdentifier, secretIdentifier)

			assert.Regexp(t, envVarPattern, envVarName)
			assert.Regexp(t, envVarPattern, secretIdentifier)
			assert.Equal(t, "TOOLHIVE_SECRET_"+secretIdentifier, envVarName,
				"envVarName must equal TOOLHIVE_SECRET_ + secretIdentifier")
		})
	}
}

func TestManifestEnvVarName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                   string
		ownerName              string
		expectedEnvVarName     string
		expectedNormalizedOwn  string
	}{
		{
			name:                  "hyphen owner",
			ownerName:             "github-copilot",
			expectedEnvVarName:    "TOOLHIVE_HEADER_FORWARD_GITHUB_COPILOT",
			expectedNormalizedOwn: "GITHUB_COPILOT",
		},
		{
			name:                  "already uppercase",
			ownerName:             "STRIPE",
			expectedEnvVarName:    "TOOLHIVE_HEADER_FORWARD_STRIPE",
			expectedNormalizedOwn: "STRIPE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			envVarName, normalizedOwner := ManifestEnvVarName(tt.ownerName)
			assert.Equal(t, tt.expectedEnvVarName, envVarName)
			assert.Equal(t, tt.expectedNormalizedOwn, normalizedOwner)
			assert.Regexp(t, envVarPattern, envVarName)
			assert.Equal(t, ManifestEnvVarPrefix+normalizedOwner, envVarName,
				"envVarName must equal ManifestEnvVarPrefix + normalizedOwner")
		})
	}
}

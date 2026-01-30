// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestGenerateUniqueTokenExchangeEnvVarName tests the GenerateUniqueTokenExchangeEnvVarName function
func TestGenerateUniqueTokenExchangeEnvVarName(t *testing.T) {
	t.Parallel()

	expectedPrefix := "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET"
	tests := []struct {
		name           string
		configName     string
		expectedSuffix string
	}{
		{
			name:           "simple name",
			configName:     "test-config",
			expectedSuffix: "TEST_CONFIG",
		},
		{
			name:           "multiple hyphens",
			configName:     "my-test-config",
			expectedSuffix: "MY_TEST_CONFIG",
		},
		{
			name:           "with special characters",
			configName:     "test.config@123",
			expectedSuffix: "TEST_CONFIG_123",
		},
		{
			name:           "single character",
			configName:     "a",
			expectedSuffix: "A",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := GenerateUniqueTokenExchangeEnvVarName(tt.configName)
			assert.Contains(t, result, expectedPrefix)
			assert.Contains(t, result, tt.expectedSuffix)
			// Verify format: PREFIX_SUFFIX
			assert.Contains(t, result, "_")
			// Verify all characters are valid for env vars (uppercase, alphanumeric, underscore)
			envVarPattern := regexp.MustCompile(`^[A-Z0-9_]+$`)
			assert.Regexp(t, envVarPattern, result, "Result should be a valid environment variable name")
		})
	}
}

// TestGenerateUniqueHeaderInjectionEnvVarName tests the GenerateUniqueHeaderInjectionEnvVarName function
func TestGenerateUniqueHeaderInjectionEnvVarName(t *testing.T) {
	t.Parallel()

	expectedPrefix := "TOOLHIVE_HEADER_INJECTION_VALUE"
	tests := []struct {
		name           string
		configName     string
		expectedSuffix string
	}{
		{
			name:           "simple name",
			configName:     "test-config",
			expectedSuffix: "TEST_CONFIG",
		},
		{
			name:           "multiple hyphens",
			configName:     "my-test-config",
			expectedSuffix: "MY_TEST_CONFIG",
		},
		{
			name:           "with special characters",
			configName:     "test.config@123",
			expectedSuffix: "TEST_CONFIG_123",
		},
		{
			name:           "single character",
			configName:     "x",
			expectedSuffix: "X",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := GenerateUniqueHeaderInjectionEnvVarName(tt.configName)
			assert.True(t, regexp.MustCompile("^"+expectedPrefix+"_").MatchString(result), "Result should start with prefix")
			assert.True(t, regexp.MustCompile(tt.expectedSuffix+"$").MatchString(result), "Result should end with suffix")
			// Verify format: PREFIX_SUFFIX
			assert.Contains(t, result, "_")
			// Verify all characters are valid for env vars (uppercase, alphanumeric, underscore)
			envVarPattern := regexp.MustCompile(`^[A-Z0-9_]+$`)
			assert.Regexp(t, envVarPattern, result, "Result should be a valid environment variable name")
		})
	}
}

// TestGenerateHeaderForwardSecretEnvVarName tests the GenerateHeaderForwardSecretEnvVarName function
func TestGenerateHeaderForwardSecretEnvVarName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                     string
		proxyName                string
		headerName               string
		expectedEnvVarName       string
		expectedSecretIdentifier string
	}{
		{
			name:                     "simple names",
			proxyName:                "my-proxy",
			headerName:               "X-API-Key",
			expectedEnvVarName:       "TOOLHIVE_SECRET_HEADER_FORWARD_X_API_KEY_MY_PROXY",
			expectedSecretIdentifier: "HEADER_FORWARD_X_API_KEY_MY_PROXY",
		},
		{
			name:                     "lowercase header",
			proxyName:                "test-proxy",
			headerName:               "authorization",
			expectedEnvVarName:       "TOOLHIVE_SECRET_HEADER_FORWARD_AUTHORIZATION_TEST_PROXY",
			expectedSecretIdentifier: "HEADER_FORWARD_AUTHORIZATION_TEST_PROXY",
		},
		{
			name:                     "multiple hyphens",
			proxyName:                "my-remote-proxy",
			headerName:               "X-Custom-Header",
			expectedEnvVarName:       "TOOLHIVE_SECRET_HEADER_FORWARD_X_CUSTOM_HEADER_MY_REMOTE_PROXY",
			expectedSecretIdentifier: "HEADER_FORWARD_X_CUSTOM_HEADER_MY_REMOTE_PROXY",
		},
		{
			name:                     "special characters in proxy name",
			proxyName:                "proxy.name@123",
			headerName:               "X-Token",
			expectedEnvVarName:       "TOOLHIVE_SECRET_HEADER_FORWARD_X_TOKEN_PROXY_NAME_123",
			expectedSecretIdentifier: "HEADER_FORWARD_X_TOKEN_PROXY_NAME_123",
		},
	}

	envVarPattern := regexp.MustCompile(`^[A-Z0-9_]+$`)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			envVarName, secretIdentifier := GenerateHeaderForwardSecretEnvVarName(tt.proxyName, tt.headerName)

			// Verify expected values
			assert.Equal(t, tt.expectedEnvVarName, envVarName, "envVarName should match expected")
			assert.Equal(t, tt.expectedSecretIdentifier, secretIdentifier, "secretIdentifier should match expected")

			// Verify env var name starts with TOOLHIVE_SECRET_ prefix
			assert.True(t, regexp.MustCompile("^TOOLHIVE_SECRET_").MatchString(envVarName),
				"envVarName should start with TOOLHIVE_SECRET_ prefix")

			// Verify env var name is valid
			assert.Regexp(t, envVarPattern, envVarName, "envVarName should be a valid environment variable name")
			assert.Regexp(t, envVarPattern, secretIdentifier, "secretIdentifier should be a valid identifier")

			// Verify relationship: envVarName = "TOOLHIVE_SECRET_" + secretIdentifier
			assert.Equal(t, "TOOLHIVE_SECRET_"+secretIdentifier, envVarName,
				"envVarName should equal TOOLHIVE_SECRET_ prefix + secretIdentifier")
		})
	}
}

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

// TestGenerateUniqueOBOSecretEnvVarName pins the OBO vMCP env var name format
// (readable sanitized name plus a hash of the raw name) and that names which
// sanitize identically still map to distinct env vars.
func TestGenerateUniqueOBOSecretEnvVarName(t *testing.T) {
	t.Parallel()

	envVarPattern := regexp.MustCompile(`^[A-Z0-9_]+$`)

	tests := []struct {
		name       string
		configName string
		want       string
	}{
		// Suffixes are the first 8 hex characters of sha256(configName).
		{name: "hyphenated name", configName: "my-auth-config", want: "TOOLHIVE_OBO_CLIENT_SECRET_MY_AUTH_CONFIG_EB7C1742"},
		{name: "a-b", configName: "a-b", want: "TOOLHIVE_OBO_CLIENT_SECRET_A_B_D44362D6"},
		{name: "a.b", configName: "a.b", want: "TOOLHIVE_OBO_CLIENT_SECRET_A_B_2E7336DC"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := GenerateUniqueOBOSecretEnvVarName(tt.configName)
			assert.Equal(t, tt.want, got)
			assert.Regexp(t, envVarPattern, got, "result must be a valid environment variable name")
		})
	}

	t.Run("names that sanitize identically do not collide", func(t *testing.T) {
		t.Parallel()
		assert.NotEqual(t, GenerateUniqueOBOSecretEnvVarName("a-b"), GenerateUniqueOBOSecretEnvVarName("a.b"))
		assert.NotEqual(t, GenerateUniqueOBOSecretEnvVarName("my-cfg"), GenerateUniqueOBOSecretEnvVarName("my.cfg"))
	})
}

// Tests for the header-forward env var generators moved with the
// implementation to pkg/vmcp/headerforward/wirefmt — see wirefmt_test.go.

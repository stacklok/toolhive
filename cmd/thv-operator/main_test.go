package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsFeatureEnabled tests the isFeatureEnabled function.
// Note: This test cannot use t.Parallel() because it modifies environment variables
// via t.Setenv, which is incompatible with parallel execution.
func TestIsFeatureEnabled(t *testing.T) {
	tests := []struct {
		name         string
		envVar       string
		envValue     string
		setEnv       bool
		defaultValue bool
		expected     bool
	}{
		{
			name:         "env not set returns default true",
			envVar:       "TEST_FEATURE_NOT_SET",
			setEnv:       false,
			defaultValue: true,
			expected:     true,
		},
		{
			name:         "env not set returns default false",
			envVar:       "TEST_FEATURE_NOT_SET_FALSE",
			setEnv:       false,
			defaultValue: false,
			expected:     false,
		},
		{
			name:         "env set to true returns true",
			envVar:       "TEST_FEATURE_TRUE",
			envValue:     "true",
			setEnv:       true,
			defaultValue: false,
			expected:     true,
		},
		{
			name:         "env set to TRUE (uppercase) returns true",
			envVar:       "TEST_FEATURE_TRUE_UPPER",
			envValue:     "TRUE",
			setEnv:       true,
			defaultValue: false,
			expected:     true,
		},
		{
			name:         "env set to 1 returns true",
			envVar:       "TEST_FEATURE_ONE",
			envValue:     "1",
			setEnv:       true,
			defaultValue: false,
			expected:     true,
		},
		{
			name:         "env set to false returns false",
			envVar:       "TEST_FEATURE_FALSE",
			envValue:     "false",
			setEnv:       true,
			defaultValue: true,
			expected:     false,
		},
		{
			name:         "env set to FALSE (uppercase) returns false",
			envVar:       "TEST_FEATURE_FALSE_UPPER",
			envValue:     "FALSE",
			setEnv:       true,
			defaultValue: true,
			expected:     false,
		},
		{
			name:         "env set to 0 returns false",
			envVar:       "TEST_FEATURE_ZERO",
			envValue:     "0",
			setEnv:       true,
			defaultValue: true,
			expected:     false,
		},
		{
			name:         "env set to t returns true",
			envVar:       "TEST_FEATURE_T",
			envValue:     "t",
			setEnv:       true,
			defaultValue: false,
			expected:     true,
		},
		{
			name:         "env set to f returns false",
			envVar:       "TEST_FEATURE_F",
			envValue:     "f",
			setEnv:       true,
			defaultValue: true,
			expected:     false,
		},
		{
			name:         "invalid value 'yes' returns default",
			envVar:       "TEST_FEATURE_YES",
			envValue:     "yes",
			setEnv:       true,
			defaultValue: true,
			expected:     true,
		},
		{
			name:         "invalid value 'no' returns default",
			envVar:       "TEST_FEATURE_NO",
			envValue:     "no",
			setEnv:       true,
			defaultValue: false,
			expected:     false,
		},
		{
			name:         "invalid value 'enabled' returns default",
			envVar:       "TEST_FEATURE_ENABLED",
			envValue:     "enabled",
			setEnv:       true,
			defaultValue: true,
			expected:     true,
		},
		{
			name:         "invalid value 'disabled' returns default false",
			envVar:       "TEST_FEATURE_DISABLED",
			envValue:     "disabled",
			setEnv:       true,
			defaultValue: false,
			expected:     false,
		},
		{
			name:         "empty string returns default",
			envVar:       "TEST_FEATURE_EMPTY",
			envValue:     "",
			setEnv:       true,
			defaultValue: true,
			expected:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use t.Setenv which automatically cleans up after test
			if tt.setEnv {
				t.Setenv(tt.envVar, tt.envValue)
			}

			result := isFeatureEnabled(tt.envVar, tt.defaultValue)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestControllerDependencies(t *testing.T) {
	t.Parallel()

	// Verify that the dependency map is correctly defined
	assert.Contains(t, controllerDependencies, featureVMCP, "featureVMCP should have dependencies defined")
	assert.Contains(t, controllerDependencies[featureVMCP], featureServer, "featureVMCP should depend on featureServer")
}

func TestFeatureFlagConstants(t *testing.T) {
	t.Parallel()

	// Verify that feature flag constants are correctly defined
	assert.Equal(t, "ENABLE_SERVER", featureServer)
	assert.Equal(t, "ENABLE_REGISTRY", featureRegistry)
	assert.Equal(t, "ENABLE_VMCP", featureVMCP)
}

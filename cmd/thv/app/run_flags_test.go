package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/logger"
)

// createTestConfigProvider creates a config provider for testing with the provided configuration.
func createTestConfigProvider(t *testing.T, cfg *config.Config) (config.Provider, func()) {
	t.Helper()

	// Create a temporary directory for the test
	tempDir := t.TempDir()

	// Create the config directory structure
	configDir := filepath.Join(tempDir, "toolhive")
	err := os.MkdirAll(configDir, 0755)
	require.NoError(t, err)

	// Set up the config file path
	configPath := filepath.Join(configDir, "config.yaml")

	// Create a path-based config provider
	provider := config.NewPathProvider(configPath)

	// Write the config file if one is provided
	if cfg != nil {
		err = provider.UpdateConfig(func(c *config.Config) { *c = *cfg })
		require.NoError(t, err)
	}

	return provider, func() {
		// Cleanup is handled by t.TempDir()
	}
}

func TestBuildRunnerConfig_TelemetryProcessing(t *testing.T) {
	t.Parallel()
	// Initialize logger to prevent nil pointer dereference
	logger.Initialize()

	tests := []struct {
		name                                string
		setupFlags                          func(*cobra.Command)
		configOTEL                          config.OpenTelemetryConfig
		runFlags                            *RunFlags
		expectedEndpoint                    string
		expectedSamplingRate                float64
		expectedEnvironmentVariables        []string
		expectedInsecure                    bool
		expectedEnablePrometheusMetricsPath bool
	}{
		{
			name: "CLI flags provided, taking precedence over config file",
			setupFlags: func(cmd *cobra.Command) {
				// Mark CLI flags as changed to simulate user providing them
				cmd.Flags().Set("otel-endpoint", "https://cli-endpoint.example.com")
				cmd.Flags().Set("otel-sampling-rate", "0.8")
				cmd.Flags().Set("otel-env-vars", "CLI_VAR1=value1")
				cmd.Flags().Set("otel-env-vars", "CLI_VAR2=value2")
				cmd.Flags().Set("otel-insecure", "true")
				cmd.Flags().Set("otel-enable-prometheus-metrics-path", "true")
			},
			configOTEL: config.OpenTelemetryConfig{
				Endpoint:                    "https://config-endpoint.example.com",
				SamplingRate:                0.2,
				EnvVars:                     []string{"CONFIG_VAR1=configvalue1", "CONFIG_VAR2=configvalue2"},
				Insecure:                    false,
				EnablePrometheusMetricsPath: false,
			},
			runFlags: &RunFlags{
				OtelEndpoint:                    "https://cli-endpoint.example.com",
				OtelSamplingRate:                0.8,
				OtelEnvironmentVariables:        []string{"CLI_VAR1=value1", "CLI_VAR2=value2"},
				OtelInsecure:                    true,
				OtelEnablePrometheusMetricsPath: true,
				// Set other required fields to avoid nil pointer errors
				Transport:         "sse",
				ProxyMode:         "sse",
				Host:              "localhost",
				PermissionProfile: "none",
			},
			expectedEndpoint:                    "https://cli-endpoint.example.com",
			expectedSamplingRate:                0.8,
			expectedEnvironmentVariables:        []string{"CLI_VAR1=value1", "CLI_VAR2=value2"},
			expectedInsecure:                    true,
			expectedEnablePrometheusMetricsPath: true,
		},
		{
			name: "No CLI flags provided, config takes precedence",
			setupFlags: func(_ *cobra.Command) {
				// Don't set any flags - they should remain unchanged/default
				// This simulates the case where user doesn't provide CLI flags
			},
			configOTEL: config.OpenTelemetryConfig{
				Endpoint:                    "https://config-endpoint.example.com",
				SamplingRate:                0.3,
				EnvVars:                     []string{"CONFIG_VAR1=configvalue1", "CONFIG_VAR2=configvalue2"},
				Insecure:                    true,
				EnablePrometheusMetricsPath: true,
			},
			runFlags: &RunFlags{
				OtelEndpoint:                    "",
				OtelSamplingRate:                0.1,
				OtelEnvironmentVariables:        nil,
				OtelInsecure:                    false,
				OtelEnablePrometheusMetricsPath: false,
				Transport:                       "sse",
				ProxyMode:                       "sse",
				Host:                            "localhost",
				PermissionProfile:               "none",
			},
			expectedEndpoint:                    "https://config-endpoint.example.com",
			expectedSamplingRate:                0.3,
			expectedEnvironmentVariables:        []string{"CONFIG_VAR1=configvalue1", "CONFIG_VAR2=configvalue2"},
			expectedInsecure:                    true,
			expectedEnablePrometheusMetricsPath: true,
		},
		{
			name: "Partial CLI flags provided, mix of CLI and config values",
			setupFlags: func(cmd *cobra.Command) {
				// Only set endpoint flag, leave others to use config values
				cmd.Flags().Set("otel-endpoint", "https://partial-cli-endpoint.example.com")
			},
			configOTEL: config.OpenTelemetryConfig{
				Endpoint:                    "https://config-endpoint.example.com",
				SamplingRate:                0.5,
				EnvVars:                     []string{"CONFIG_VAR1=configvalue1"},
				Insecure:                    true,
				EnablePrometheusMetricsPath: true,
			},
			runFlags: &RunFlags{
				OtelEndpoint:                    "https://partial-cli-endpoint.example.com",
				OtelSamplingRate:                0.1,
				OtelEnvironmentVariables:        nil,
				OtelInsecure:                    false,
				OtelEnablePrometheusMetricsPath: false,
				Transport:                       "sse",
				ProxyMode:                       "sse",
				Host:                            "localhost",
				PermissionProfile:               "none",
			},
			expectedEndpoint:                    "https://partial-cli-endpoint.example.com",
			expectedSamplingRate:                0.5,
			expectedEnvironmentVariables:        []string{"CONFIG_VAR1=configvalue1"},
			expectedInsecure:                    true,
			expectedEnablePrometheusMetricsPath: true,
		},
		{
			name: "Empty config values, CLI flags should be used",
			setupFlags: func(cmd *cobra.Command) {
				cmd.Flags().Set("otel-endpoint", "https://cli-only-endpoint.example.com")
				cmd.Flags().Set("otel-sampling-rate", "0.9")
			},
			configOTEL: config.OpenTelemetryConfig{
				Endpoint:                    "",
				SamplingRate:                0.0,
				EnvVars:                     nil,
				Insecure:                    false,
				EnablePrometheusMetricsPath: false,
			},
			runFlags: &RunFlags{
				OtelEndpoint:                    "https://cli-only-endpoint.example.com",
				OtelSamplingRate:                0.9,
				OtelEnvironmentVariables:        nil,
				OtelInsecure:                    false,
				OtelEnablePrometheusMetricsPath: false,
				Transport:                       "sse",
				ProxyMode:                       "sse",
				Host:                            "localhost",
				PermissionProfile:               "none",
			},
			expectedEndpoint:                    "https://cli-only-endpoint.example.com",
			expectedSamplingRate:                0.9,
			expectedEnvironmentVariables:        nil,
			expectedInsecure:                    false,
			expectedEnablePrometheusMetricsPath: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd := &cobra.Command{}
			AddRunFlags(cmd, &RunFlags{})
			tt.setupFlags(cmd)
			configProvider, cleanup := createTestConfigProvider(t, &config.Config{
				OTEL: tt.configOTEL,
			})
			defer cleanup()
			configInstance := configProvider.GetConfig()
			finalEndpoint, finalSamplingRate, finalEnvVars, finalInsecure, finalEnablePrometheusMetricsPath := getTelemetryFromFlags(
				cmd,
				configInstance,
				tt.runFlags.OtelEndpoint,
				tt.runFlags.OtelSamplingRate,
				tt.runFlags.OtelEnvironmentVariables,
				tt.runFlags.OtelInsecure,
				tt.runFlags.OtelEnablePrometheusMetricsPath,
			)

			// Assert the results
			assert.Equal(t, tt.expectedEndpoint, finalEndpoint, "OTEL endpoint should match expected value")
			assert.Equal(t, tt.expectedSamplingRate, finalSamplingRate, "OTEL sampling rate should match expected value")
			assert.Equal(t, tt.expectedEnvironmentVariables, finalEnvVars, "OTEL environment variables should match expected value")
			assert.Equal(t, tt.expectedInsecure, finalInsecure, "OTEL insecure setting should match expected value")
			assert.Equal(t, tt.expectedEnablePrometheusMetricsPath, finalEnablePrometheusMetricsPath, "OTEL enable Prometheus metrics path setting should match expected value")
		})
	}
}

func TestBuildRunnerConfig_TelemetryProcessing_Integration(t *testing.T) {
	t.Parallel()
	// This is a more complete integration test that tests telemetry processing
	// within the full BuildRunnerConfig function context
	logger.Initialize()
	cmd := &cobra.Command{}
	runFlags := &RunFlags{
		Transport:         "sse",
		ProxyMode:         "sse",
		Host:              "localhost",
		PermissionProfile: "none",
		OtelEndpoint:      "https://integration-test.example.com",
		OtelSamplingRate:  0.7,
	}
	AddRunFlags(cmd, runFlags)
	err := cmd.Flags().Set("otel-endpoint", "https://integration-test.example.com")
	require.NoError(t, err)
	err = cmd.Flags().Set("otel-sampling-rate", "0.7")
	require.NoError(t, err)
	configProvider, cleanup := createTestConfigProvider(t, &config.Config{
		OTEL: config.OpenTelemetryConfig{
			Endpoint:     "https://config-fallback.example.com",
			SamplingRate: 0.2,
			EnvVars:      []string{"CONFIG_VAR=value"},
		},
	})
	defer cleanup()

	configInstance := configProvider.GetConfig()
	finalEndpoint, finalSamplingRate, finalEnvVars, finalInsecure, finalEnablePrometheusMetricsPath := getTelemetryFromFlags(
		cmd,
		configInstance,
		runFlags.OtelEndpoint,
		runFlags.OtelSamplingRate,
		runFlags.OtelEnvironmentVariables,
		runFlags.OtelInsecure,
		runFlags.OtelEnablePrometheusMetricsPath,
	)

	// Verify that CLI values take precedence
	assert.Equal(t, "https://integration-test.example.com", finalEndpoint, "CLI endpoint should take precedence over config")
	assert.Equal(t, 0.7, finalSamplingRate, "CLI sampling rate should take precedence over config")
	assert.Equal(t, []string{"CONFIG_VAR=value"}, finalEnvVars, "Environment variables should fall back to config when not set via CLI")
	assert.Equal(t, false, finalInsecure, "Insecure setting should use runFlags value when not set via CLI")
	assert.Equal(t, false, finalEnablePrometheusMetricsPath, "Enable Prometheus metrics path should use runFlags value when not set via CLI")
}

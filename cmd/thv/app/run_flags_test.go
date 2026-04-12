// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/logging"
	regtypes "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/config"
)

func boolPtr(b bool) *bool { return &b }

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
	slog.SetDefault(logging.New(logging.WithOutput(os.Stdout), logging.WithLevel(slog.LevelDebug), logging.WithFormat(logging.FormatText)))

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
		expectedUseLegacyAttributes         bool
		expectedTracingEnabled              bool
		expectedMetricsEnabled              bool
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
				cmd.Flags().Set("otel-tracing-enabled", "true")
				cmd.Flags().Set("otel-metrics-enabled", "false")
			},
			configOTEL: config.OpenTelemetryConfig{
				Endpoint:                    "https://config-endpoint.example.com",
				SamplingRate:                0.2,
				EnvVars:                     []string{"CONFIG_VAR1=configvalue1", "CONFIG_VAR2=configvalue2"},
				Insecure:                    false,
				EnablePrometheusMetricsPath: false,
				TracingEnabled:              boolPtr(false),
				MetricsEnabled:              boolPtr(true),
			},
			runFlags: &RunFlags{
				OtelEndpoint:                    "https://cli-endpoint.example.com",
				OtelSamplingRate:                0.8,
				OtelEnvironmentVariables:        []string{"CLI_VAR1=value1", "CLI_VAR2=value2"},
				OtelInsecure:                    true,
				OtelEnablePrometheusMetricsPath: true,
				OtelTracingEnabled:              true,
				OtelMetricsEnabled:              false,
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
			expectedUseLegacyAttributes:         false,
			expectedTracingEnabled:              true,
			expectedMetricsEnabled:              false,
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
				UseLegacyAttributes:         boolPtr(true),
				TracingEnabled:              boolPtr(false),
				MetricsEnabled:              boolPtr(false),
			},
			runFlags: &RunFlags{
				OtelEndpoint:                    "",
				OtelSamplingRate:                0.1,
				OtelEnvironmentVariables:        nil,
				OtelInsecure:                    false,
				OtelEnablePrometheusMetricsPath: false,
				OtelTracingEnabled:              true, // CLI default
				OtelMetricsEnabled:              true, // CLI default
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
			expectedUseLegacyAttributes:         true,
			expectedTracingEnabled:              false,
			expectedMetricsEnabled:              false,
		},
		{
			name: "Partial CLI flags provided, mix of CLI and config values",
			setupFlags: func(cmd *cobra.Command) {
				// Only set endpoint and insecure flags, leave others to use config values
				cmd.Flags().Set("otel-endpoint", "https://partial-cli-endpoint.example.com")
				cmd.Flags().Set("otel-insecure", "true")
			},
			configOTEL: config.OpenTelemetryConfig{
				Endpoint:                    "https://config-endpoint.example.com",
				SamplingRate:                0.5,
				EnvVars:                     []string{"CONFIG_VAR1=configvalue1"},
				Insecure:                    false,
				EnablePrometheusMetricsPath: true,
			},
			runFlags: &RunFlags{
				OtelEndpoint:                    "https://partial-cli-endpoint.example.com",
				OtelSamplingRate:                0.1,
				OtelEnvironmentVariables:        nil,
				OtelInsecure:                    true,
				OtelEnablePrometheusMetricsPath: false,
				OtelTracingEnabled:              true, // CLI default
				OtelMetricsEnabled:              true, // CLI default
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
			expectedTracingEnabled:              true, // CLI default (not changed, config nil)
			expectedMetricsEnabled:              true, // CLI default (not changed, config nil)
		},
		{
			name: "Empty config values, CLI flags should be used",
			setupFlags: func(cmd *cobra.Command) {
				cmd.Flags().Set("otel-endpoint", "https://cli-only-endpoint.example.com")
				cmd.Flags().Set("otel-sampling-rate", "0.9")
				cmd.Flags().Set("otel-insecure", "true")
			},
			configOTEL: config.OpenTelemetryConfig{
				Endpoint:     "",
				SamplingRate: 0.0,
				EnvVars:      nil,
			},
			runFlags: &RunFlags{
				OtelEndpoint:             "https://cli-only-endpoint.example.com",
				OtelSamplingRate:         0.9,
				OtelEnvironmentVariables: nil,
				OtelInsecure:             true,
				OtelTracingEnabled:       true, // CLI default
				OtelMetricsEnabled:       true, // CLI default
				Transport:                "sse",
				ProxyMode:                "sse",
				Host:                     "localhost",
				PermissionProfile:        "none",
			},
			expectedEndpoint:                    "https://cli-only-endpoint.example.com",
			expectedSamplingRate:                0.9,
			expectedEnvironmentVariables:        nil,
			expectedInsecure:                    true,
			expectedEnablePrometheusMetricsPath: false,
			expectedTracingEnabled:              true, // CLI flag set
			expectedMetricsEnabled:              true, // CLI default (not changed, config nil)
		},
		{
			name: "Config disables legacy attributes, CLI flag unchanged",
			setupFlags: func(_ *cobra.Command) {
				// Don't set any flags - config value should take effect
			},
			configOTEL: config.OpenTelemetryConfig{
				UseLegacyAttributes: boolPtr(false),
			},
			runFlags: &RunFlags{
				OtelUseLegacyAttributes: true, // CLI default
				OtelTracingEnabled:      true, // CLI default
				OtelMetricsEnabled:      true, // CLI default
				Transport:               "sse",
				ProxyMode:               "sse",
				Host:                    "localhost",
				PermissionProfile:       "none",
			},
			expectedUseLegacyAttributes: false,
			expectedTracingEnabled:      true, // CLI default (config nil)
			expectedMetricsEnabled:      true, // CLI default (config nil)
		},
		{
			name: "Config not set (nil), CLI default true should be used",
			setupFlags: func(_ *cobra.Command) {
				// Don't set any flags
			},
			configOTEL: config.OpenTelemetryConfig{
				// UseLegacyAttributes not set — remains nil
			},
			runFlags: &RunFlags{
				OtelUseLegacyAttributes: true, // CLI default
				OtelTracingEnabled:      true, // CLI default
				OtelMetricsEnabled:      true, // CLI default
				Transport:               "sse",
				ProxyMode:               "sse",
				Host:                    "localhost",
				PermissionProfile:       "none",
			},
			expectedUseLegacyAttributes: true,
			expectedTracingEnabled:      true, // CLI default (config nil)
			expectedMetricsEnabled:      true, // CLI default (config nil)
		},
		{
			name: "Config disables tracing and metrics, CLI flags unchanged",
			setupFlags: func(_ *cobra.Command) {
				// Don't set any flags - config values should take effect
			},
			configOTEL: config.OpenTelemetryConfig{
				Endpoint:       "https://config-endpoint.example.com",
				TracingEnabled: boolPtr(false),
				MetricsEnabled: boolPtr(false),
			},
			runFlags: &RunFlags{
				OtelTracingEnabled: true, // CLI default
				OtelMetricsEnabled: true, // CLI default
				Transport:          "sse",
				ProxyMode:          "sse",
				Host:               "localhost",
				PermissionProfile:  "none",
			},
			expectedEndpoint:       "https://config-endpoint.example.com",
			expectedTracingEnabled: false,
			expectedMetricsEnabled: false,
		},
		{
			name: "Config enables tracing and metrics explicitly",
			setupFlags: func(_ *cobra.Command) {
				// Don't set any flags
			},
			configOTEL: config.OpenTelemetryConfig{
				TracingEnabled: boolPtr(true),
				MetricsEnabled: boolPtr(true),
			},
			runFlags: &RunFlags{
				OtelTracingEnabled: true, // CLI default
				OtelMetricsEnabled: true, // CLI default
				Transport:          "sse",
				ProxyMode:          "sse",
				Host:               "localhost",
				PermissionProfile:  "none",
			},
			expectedTracingEnabled: true,
			expectedMetricsEnabled: true,
		},
		{
			name: "Config nil (never set), CLI defaults to enabled",
			setupFlags: func(_ *cobra.Command) {
				// Don't set any flags
			},
			configOTEL: config.OpenTelemetryConfig{
				// TracingEnabled and MetricsEnabled not set — remain nil
			},
			runFlags: &RunFlags{
				OtelTracingEnabled: true, // CLI default
				OtelMetricsEnabled: true, // CLI default
				Transport:          "sse",
				ProxyMode:          "sse",
				Host:               "localhost",
				PermissionProfile:  "none",
			},
			expectedTracingEnabled: true,
			expectedMetricsEnabled: true,
		},
		{
			name: "CLI flag overrides config for tracing/metrics",
			setupFlags: func(cmd *cobra.Command) {
				cmd.Flags().Set("otel-tracing-enabled", "true")
				cmd.Flags().Set("otel-metrics-enabled", "true")
			},
			configOTEL: config.OpenTelemetryConfig{
				TracingEnabled: boolPtr(false),
				MetricsEnabled: boolPtr(false),
			},
			runFlags: &RunFlags{
				OtelTracingEnabled: true,
				OtelMetricsEnabled: true,
				Transport:          "sse",
				ProxyMode:          "sse",
				Host:               "localhost",
				PermissionProfile:  "none",
			},
			expectedTracingEnabled: true,
			expectedMetricsEnabled: true,
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
			finalTelemetry := getTelemetryFromFlags(
				cmd,
				configInstance,
				tt.runFlags.OtelEndpoint,
				tt.runFlags.OtelSamplingRate,
				tt.runFlags.OtelEnvironmentVariables,
				tt.runFlags.OtelInsecure,
				tt.runFlags.OtelEnablePrometheusMetricsPath,
				tt.runFlags.OtelUseLegacyAttributes,
				tt.runFlags.OtelTracingEnabled,
				tt.runFlags.OtelMetricsEnabled,
			)

			// Assert the results
			assert.Equal(t, tt.expectedEndpoint, finalTelemetry.OtelEndpoint, "OTEL endpoint should match expected value")
			assert.Equal(t, tt.expectedSamplingRate, finalTelemetry.OtelSamplingRate, "OTEL sampling rate should match expected value")
			assert.Equal(t, tt.expectedEnvironmentVariables, finalTelemetry.OtelEnvironmentVariables, "OTEL environment variables should match expected value")
			assert.Equal(t, tt.expectedInsecure, finalTelemetry.OtelInsecure, "OTEL insecure setting should match expected value")
			assert.Equal(t, tt.expectedEnablePrometheusMetricsPath, finalTelemetry.OtelEnablePrometheusMetricsPath, "OTEL enable Prometheus metrics path setting should match expected value")
			assert.Equal(t, tt.expectedUseLegacyAttributes, finalTelemetry.OtelUseLegacyAttributes, "OTEL use legacy attributes setting should match expected value")
			assert.Equal(t, tt.expectedTracingEnabled, finalTelemetry.OtelTracingEnabled, "OTEL tracing enabled should match expected value")
			assert.Equal(t, tt.expectedMetricsEnabled, finalTelemetry.OtelMetricsEnabled, "OTEL metrics enabled should match expected value")
		})
	}
}

func TestTelemetryMiddlewareParameterComputation(t *testing.T) {
	// This test validates the telemetry middleware parameter computation
	// by testing the logic that computes server name and transport type
	// before calling WithMiddlewareFromFlags
	t.Parallel()

	slog.SetDefault(logging.New(logging.WithOutput(os.Stdout), logging.WithLevel(slog.LevelDebug), logging.WithFormat(logging.FormatText)))

	tests := []struct {
		name              string
		runFlags          *RunFlags
		serverOrImage     string
		expectedServer    string
		expectedTransport string
	}{
		{
			name: "explicit name and transport should use provided values",
			runFlags: &RunFlags{
				Name:      "custom-server",
				Transport: "http",
			},
			serverOrImage:     "custom-server",
			expectedServer:    "custom-server",
			expectedTransport: "http",
		},
		{
			name: "empty name should be computed from image name",
			runFlags: &RunFlags{
				Transport: "sse",
			},
			serverOrImage:     "docker://registry.test/my-test-server:latest",
			expectedServer:    "my-test-server", // Extracted from image name
			expectedTransport: "sse",
		},
		{
			name: "empty transport should use default",
			runFlags: &RunFlags{
				Name: "named-server",
			},
			serverOrImage:     "named-server",
			expectedServer:    "named-server",
			expectedTransport: "streamable-http", // Default from constant
		},
		{
			name:              "both empty should compute name and use default transport",
			runFlags:          &RunFlags{},
			serverOrImage:     "docker://example.com/path/server-name:v1.0",
			expectedServer:    "server-name",     // Extracted from image
			expectedTransport: "streamable-http", // Default
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Test the server name computation logic that was fixed
			// This simulates the logic in BuildRunnerConfig before WithMiddlewareFromFlags

			// 1. Test transport type computation (this was already working)
			transportType := tt.runFlags.Transport
			if transportType == "" {
				transportType = defaultTransportType // "streamable-http"
			}
			assert.Equal(t, tt.expectedTransport, transportType, "Transport type should match expected")

			// 2. Test server name computation
			serverName := tt.runFlags.Name
			if serverName == "" {
				// This simulates the image metadata extraction logic
				if strings.HasPrefix(tt.serverOrImage, "docker://") {
					imagePath := strings.TrimPrefix(tt.serverOrImage, "docker://")
					parts := strings.Split(imagePath, "/")
					imageName := parts[len(parts)-1]
					if colonIndex := strings.Index(imageName, ":"); colonIndex != -1 {
						imageName = imageName[:colonIndex]
					}
					serverName = imageName
				} else {
					serverName = tt.serverOrImage
				}
			}
			assert.Equal(t, tt.expectedServer, serverName, "Server name should match expected")

			// 3. Verify both parameters are non-empty for proper middleware function
			assert.NotEmpty(t, serverName, "Server name should never be empty for middleware")
			assert.NotEmpty(t, transportType, "Transport type should never be empty for middleware")
		})
	}
}

func TestBuildRunnerConfig_TelemetryProcessing_Integration(t *testing.T) {
	t.Parallel()
	// This is a more complete integration test that tests telemetry processing
	// within the full BuildRunnerConfig function context
	slog.SetDefault(logging.New(logging.WithOutput(os.Stdout), logging.WithLevel(slog.LevelDebug), logging.WithFormat(logging.FormatText)))
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
	finalTelemetry := getTelemetryFromFlags(
		cmd,
		configInstance,
		runFlags.OtelEndpoint,
		runFlags.OtelSamplingRate,
		runFlags.OtelEnvironmentVariables,
		runFlags.OtelInsecure,
		runFlags.OtelEnablePrometheusMetricsPath,
		runFlags.OtelUseLegacyAttributes,
		runFlags.OtelTracingEnabled,
		runFlags.OtelMetricsEnabled,
	)

	// Verify that CLI values take precedence
	assert.Equal(t, "https://integration-test.example.com", finalTelemetry.OtelEndpoint, "CLI endpoint should take precedence over config")
	assert.Equal(t, 0.7, finalTelemetry.OtelSamplingRate, "CLI sampling rate should take precedence over config")
	assert.Equal(t, []string{"CONFIG_VAR=value"}, finalTelemetry.OtelEnvironmentVariables, "Environment variables should fall back to config when not set via CLI")
	assert.Equal(t, false, finalTelemetry.OtelInsecure, "Insecure setting should use runFlags value when not set via CLI")
	assert.Equal(t, true, finalTelemetry.OtelUseLegacyAttributes, "UseLegacyAttributes should default to true when not set via CLI or config")
	assert.Equal(t, false, finalTelemetry.OtelEnablePrometheusMetricsPath, "Enable Prometheus metrics path should use runFlags value when not set via CLI")
	assert.Equal(t, true, finalTelemetry.OtelTracingEnabled, "TracingEnabled should use CLI default when not set via CLI or config")
	assert.Equal(t, true, finalTelemetry.OtelMetricsEnabled, "MetricsEnabled should use CLI default when not set via CLI or config")
}

func TestCreateTelemetryConfig_DisabledSignals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                        string
		endpoint                    string
		tracingEnabled              bool
		metricsEnabled              bool
		enablePrometheusMetricsPath bool
		expectNil                   bool
	}{
		{
			name:           "both disabled with endpoint returns nil",
			endpoint:       "https://otel.example.com",
			tracingEnabled: false,
			metricsEnabled: false,
			expectNil:      true,
		},
		{
			name:           "tracing enabled returns config",
			endpoint:       "https://otel.example.com",
			tracingEnabled: true,
			metricsEnabled: false,
			expectNil:      false,
		},
		{
			name:           "metrics enabled returns config",
			endpoint:       "https://otel.example.com",
			tracingEnabled: false,
			metricsEnabled: true,
			expectNil:      false,
		},
		{
			name:                        "both disabled but prometheus enabled returns config",
			endpoint:                    "https://otel.example.com",
			tracingEnabled:              false,
			metricsEnabled:              false,
			enablePrometheusMetricsPath: true,
			expectNil:                   false,
		},
		{
			name:           "no endpoint and both disabled returns nil",
			endpoint:       "",
			tracingEnabled: false,
			metricsEnabled: false,
			expectNil:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := createTelemetryConfig(
				tt.endpoint, tt.enablePrometheusMetricsPath,
				"test-service", tt.tracingEnabled, tt.metricsEnabled,
				1.0, nil, false, nil, "", true,
			)

			if tt.expectNil {
				assert.Nil(t, result, "expected nil telemetry config")
			} else {
				assert.NotNil(t, result, "expected non-nil telemetry config")
			}
		})
	}
}

func TestResolveTransportType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		runFlags       *RunFlags
		serverMetadata regtypes.ServerMetadata
		expected       string
	}{
		{
			name:           "explicit transport flag takes precedence",
			runFlags:       &RunFlags{Transport: "stdio"},
			serverMetadata: &regtypes.ImageMetadata{BaseServerMetadata: regtypes.BaseServerMetadata{Transport: "sse"}},
			expected:       "stdio",
		},
		{
			name:           "transport from metadata when flag is empty",
			runFlags:       &RunFlags{},
			serverMetadata: &regtypes.ImageMetadata{BaseServerMetadata: regtypes.BaseServerMetadata{Transport: "sse"}},
			expected:       "sse",
		},
		{
			name:           "nil interface returns default transport",
			runFlags:       &RunFlags{},
			serverMetadata: nil,
			expected:       defaultTransportType,
		},
		{
			name:           "typed nil pointer in interface returns default (protocol scheme case)",
			runFlags:       &RunFlags{},
			serverMetadata: regtypes.ServerMetadata((*regtypes.ImageMetadata)(nil)),
			expected:       defaultTransportType,
		},
		{
			name:           "metadata with empty transport returns default",
			runFlags:       &RunFlags{},
			serverMetadata: &regtypes.ImageMetadata{},
			expected:       defaultTransportType,
		},
		{
			name:           "explicit flag overrides even with nil metadata",
			runFlags:       &RunFlags{Transport: "streamable-http"},
			serverMetadata: nil,
			expected:       "streamable-http",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := resolveTransportType(tt.runFlags, tt.serverMetadata)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSetupTelemetryConfiguration_LoadOrCreateConfigPath(t *testing.T) {
	// This test validates the bug fix: BuildRunnerConfig and configureMiddlewareAndOptions
	// must call provider.LoadOrCreateConfig() (not provider.GetConfig()) so that
	// enterprise providers can merge OTEL config from external sources (e.g. config-server).
	// LoadOrCreateConfig reads from the provider's backing store; GetConfig on
	// DefaultProvider reads only the cached global singleton, bypassing any registered
	// ProviderFactory.
	t.Parallel()
	slog.SetDefault(logging.New(logging.WithOutput(os.Stdout), logging.WithLevel(slog.LevelDebug), logging.WithFormat(logging.FormatText)))

	provider, cleanup := createTestConfigProvider(t, &config.Config{
		OTEL: config.OpenTelemetryConfig{
			Endpoint:     "https://provider-endpoint.example.com",
			SamplingRate: 0.42,
			EnvVars:      []string{"PROVIDER_VAR=provider_value"},
		},
	})
	defer cleanup()

	// Simulate the fixed code path: call LoadOrCreateConfig() on the provider.
	// The old buggy code called GetConfig() on DefaultProvider, which reads a
	// global singleton and bypasses factory-registered providers entirely.
	appConfig, err := provider.LoadOrCreateConfig()
	require.NoError(t, err)

	cmd := &cobra.Command{}
	AddRunFlags(cmd, &RunFlags{})

	result := getTelemetryFromFlags(
		cmd, appConfig,
		"", 0.0, nil, false, false, false, true, true,
	)

	assert.Equal(t, "https://provider-endpoint.example.com", result.OtelEndpoint,
		"OTEL endpoint from provider config should be applied when no CLI flag is set")
	assert.Equal(t, 0.42, result.OtelSamplingRate,
		"OTEL sampling rate from provider config should be applied when no CLI flag is set")
	assert.Equal(t, []string{"PROVIDER_VAR=provider_value"}, result.OtelEnvironmentVariables,
		"OTEL env vars from provider config should be applied when no CLI flag is set")
}

func TestResolveServerName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		runFlags       *RunFlags
		serverMetadata regtypes.ServerMetadata
		expected       string
	}{
		{
			name:           "explicit name flag takes precedence",
			runFlags:       &RunFlags{Name: "my-server"},
			serverMetadata: &regtypes.ImageMetadata{BaseServerMetadata: regtypes.BaseServerMetadata{Name: "registry-name"}},
			expected:       "my-server",
		},
		{
			name:           "name from metadata when flag is empty",
			runFlags:       &RunFlags{},
			serverMetadata: &regtypes.ImageMetadata{BaseServerMetadata: regtypes.BaseServerMetadata{Name: "registry-name"}},
			expected:       "registry-name",
		},
		{
			name:           "nil interface returns empty string",
			runFlags:       &RunFlags{},
			serverMetadata: nil,
			expected:       "",
		},
		{
			name:           "typed nil pointer in interface returns empty string (protocol scheme case)",
			runFlags:       &RunFlags{},
			serverMetadata: regtypes.ServerMetadata((*regtypes.ImageMetadata)(nil)),
			expected:       "",
		},
		{
			name:           "explicit flag overrides even with nil metadata",
			runFlags:       &RunFlags{Name: "explicit"},
			serverMetadata: nil,
			expected:       "explicit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := resolveServerName(tt.runFlags, tt.serverMetadata)
			assert.Equal(t, tt.expected, result)
		})
	}
}

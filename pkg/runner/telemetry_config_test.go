// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	appconfig "github.com/stacklok/toolhive/pkg/config"
)

func TestBuildTelemetryConfigFromAppConfig(t *testing.T) {
	t.Parallel()

	boolPtr := func(b bool) *bool { return &b }

	tests := []struct {
		name             string
		otel             appconfig.OpenTelemetryConfig
		serviceName      string
		headers          []string
		customAttributes string
		wantNil          bool
	}{
		{
			name:    "empty config returns nil",
			otel:    appconfig.OpenTelemetryConfig{},
			wantNil: true,
		},
		{
			name: "endpoint without enabled signals still returns config (defaults on)",
			otel: appconfig.OpenTelemetryConfig{
				Endpoint: "http://collector:4318",
			},
			serviceName: "thv-osv",
			wantNil:     false,
		},
		{
			name: "tracing and metrics both explicitly disabled with no prometheus path returns nil",
			otel: appconfig.OpenTelemetryConfig{
				Endpoint:       "http://collector:4318",
				TracingEnabled: boolPtr(false),
				MetricsEnabled: boolPtr(false),
			},
			wantNil: true,
		},
		{
			name: "prometheus path alone enables config",
			otel: appconfig.OpenTelemetryConfig{
				EnablePrometheusMetricsPath: true,
				TracingEnabled:              boolPtr(false),
				MetricsEnabled:              boolPtr(false),
			},
			wantNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := BuildTelemetryConfigFromAppConfig(tt.otel, tt.serviceName, tt.headers, tt.customAttributes)

			if tt.wantNil {
				assert.Nil(t, cfg, "expected nil telemetry config")
				return
			}
			require.NotNil(t, cfg, "expected non-nil telemetry config")
		})
	}
}

// TestBuildTelemetryConfigFromAppConfig_AppliesAllFields verifies that every
// field on OpenTelemetryConfig is actually copied to the resulting
// telemetry.Config, so the API path can't silently drop one.
func TestBuildTelemetryConfigFromAppConfig_AppliesAllFields(t *testing.T) {
	t.Parallel()

	boolPtr := func(b bool) *bool { return &b }

	otel := appconfig.OpenTelemetryConfig{
		Endpoint:                    "http://collector:4318",
		SamplingRate:                0.25,
		EnvVars:                     []string{"FOO,BAR", " BAZ "},
		MetricsEnabled:              boolPtr(true),
		TracingEnabled:              boolPtr(true),
		Insecure:                    true,
		EnablePrometheusMetricsPath: true,
		UseLegacyAttributes:         boolPtr(false),
	}

	cfg := BuildTelemetryConfigFromAppConfig(otel, "thv-osv", []string{"x=1"}, "")
	require.NotNil(t, cfg)

	assert.Equal(t, "http://collector:4318", cfg.Endpoint)
	assert.Equal(t, "thv-osv", cfg.ServiceName)
	assert.True(t, cfg.TracingEnabled)
	assert.True(t, cfg.MetricsEnabled)
	assert.True(t, cfg.Insecure)
	assert.True(t, cfg.EnablePrometheusMetricsPath)
	assert.False(t, cfg.UseLegacyAttributes)
	assert.Equal(t, []string{"FOO", "BAR", "BAZ"}, cfg.EnvironmentVariables)
	assert.Equal(t, map[string]string{"x": "1"}, cfg.Headers)
	// Sampling rate is stored as a string; just verify it was set from the float.
	assert.NotEmpty(t, cfg.SamplingRate)
}

// TestBuildTelemetryConfigFromAppConfig_OTLPHeadersFromEnvVars verifies that an
// OTEL_EXPORTER_OTLP_HEADERS entry in the app config's EnvVars is parsed into
// cfg.Headers, which is what the OTLP exporter actually sends (via WithHeaders).
// This is the path taken when headers are configured rather than passed as
// flags (e.g. the API call site passes no header flags). Without this, the
// headers are silently dropped and an auth-gated collector rejects every
// export (401).
func TestBuildTelemetryConfigFromAppConfig_OTLPHeadersFromEnvVars(t *testing.T) {
	t.Parallel()

	t.Run("single header with scheme and base64 padding is preserved", func(t *testing.T) {
		t.Parallel()
		otel := appconfig.OpenTelemetryConfig{
			Endpoint: "https://otel.example.com",
			EnvVars:  []string{"OTEL_EXPORTER_OTLP_HEADERS=Authorization=Basic dG9rZW46cHc="},
		}
		cfg := BuildTelemetryConfigFromAppConfig(otel, "thv-osv", nil, "")
		require.NotNil(t, cfg)
		// The value contains a space and '=' padding; both must survive parsing.
		assert.Equal(t, "Basic dG9rZW46cHc=", cfg.Headers["Authorization"])
	})

	t.Run("multiple comma-separated headers are all parsed", func(t *testing.T) {
		t.Parallel()
		otel := appconfig.OpenTelemetryConfig{
			Endpoint: "https://otel.example.com",
			EnvVars:  []string{"OTEL_EXPORTER_OTLP_HEADERS=Authorization=Basic abc,x-tenant-id=acme"},
		}
		cfg := BuildTelemetryConfigFromAppConfig(otel, "thv-osv", nil, "")
		require.NotNil(t, cfg)
		assert.Equal(t, "Basic abc", cfg.Headers["Authorization"])
		assert.Equal(t, "acme", cfg.Headers["x-tenant-id"])
	})

	t.Run("explicit header flag wins, other env headers still applied", func(t *testing.T) {
		t.Parallel()
		otel := appconfig.OpenTelemetryConfig{
			Endpoint: "https://otel.example.com",
			EnvVars:  []string{"OTEL_EXPORTER_OTLP_HEADERS=Authorization=fromenv,x-env=prod"},
		}
		cfg := BuildTelemetryConfigFromAppConfig(otel, "thv-osv", []string{"Authorization=fromflag"}, "")
		require.NotNil(t, cfg)
		assert.Equal(t, "fromflag", cfg.Headers["Authorization"], "explicit --otel-headers flag must take precedence")
		assert.Equal(t, "prod", cfg.Headers["x-env"], "env-provided headers other than the overridden one must still apply")
	})
}

// TestBuildTelemetryConfigFromAppConfig_DefaultsForNilBools verifies the CLI-style
// defaults: when bool fields are not set in the config, tracing/metrics/legacy
// attributes default to true. This is what makes "just configure an endpoint"
// produce a working setup.
func TestBuildTelemetryConfigFromAppConfig_DefaultsForNilBools(t *testing.T) {
	t.Parallel()

	cfg := BuildTelemetryConfigFromAppConfig(
		appconfig.OpenTelemetryConfig{Endpoint: "http://collector:4318"},
		"thv-osv",
		nil,
		"",
	)
	require.NotNil(t, cfg)
	assert.True(t, cfg.TracingEnabled, "TracingEnabled must default to true when not set in config")
	assert.True(t, cfg.MetricsEnabled, "MetricsEnabled must default to true when not set in config")
	assert.True(t, cfg.UseLegacyAttributes, "UseLegacyAttributes must default to true when not set in config")
}

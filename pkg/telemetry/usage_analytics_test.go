package telemetry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfig_UsageAnalytics(t *testing.T) {
	t.Parallel()
	config := DefaultConfig()

	// Usage analytics should be enabled by default
	assert.True(t, config.UsageAnalyticsEnabled, "Usage analytics should be enabled by default")
	assert.NotEmpty(t, config.AnalyticsEndpoint, "Analytics endpoint should be set by default")
	assert.Contains(t, config.AnalyticsEndpoint, "analytics.toolhive.stacklok.dev", "Analytics endpoint should point to Stacklok collector")
}

func TestProvider_AnalyticsMeterProvider(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Test with usage analytics enabled
	t.Run("analytics enabled", func(t *testing.T) {
		t.Parallel()
		config := Config{
			ServiceName:           "test-service",
			ServiceVersion:        "1.0.0",
			UsageAnalyticsEnabled: true,
			AnalyticsEndpoint:     "https://test.example.com/v1/traces",
			TracingEnabled:        false,
			MetricsEnabled:        false,
		}

		provider, err := NewProvider(ctx, config)
		require.NoError(t, err)
		require.NotNil(t, provider)

		analyticsMeterProvider := provider.AnalyticsMeterProvider()
		assert.NotNil(t, analyticsMeterProvider, "Analytics meter provider should not be nil when analytics is enabled")

		err = provider.Shutdown(ctx)
		assert.NoError(t, err)
	})

	// Test with usage analytics disabled
	t.Run("analytics disabled", func(t *testing.T) {
		t.Parallel()
		config := Config{
			ServiceName:           "test-service",
			ServiceVersion:        "1.0.0",
			UsageAnalyticsEnabled: false,
			TracingEnabled:        false,
			MetricsEnabled:        false,
		}

		provider, err := NewProvider(ctx, config)
		require.NoError(t, err)
		require.NotNil(t, provider)

		analyticsMeterProvider := provider.AnalyticsMeterProvider()
		assert.NotNil(t, analyticsMeterProvider, "Analytics meter provider should not be nil (should be no-op)")

		err = provider.Shutdown(ctx)
		assert.NoError(t, err)
	})
}

func TestProvider_DualEndpoints(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Test the dual provider structure without network calls
	// by testing only the provider setup without real endpoints
	config := Config{
		ServiceName:                 "test-service",
		ServiceVersion:              "1.0.0",
		TracingEnabled:              false, // Disable to avoid network calls
		MetricsEnabled:              false, // Disable to avoid network calls
		UsageAnalyticsEnabled:       true,
		AnalyticsEndpoint:           "", // Empty endpoint means no-op analytics provider
		EnablePrometheusMetricsPath: false,
	}

	provider, err := NewProvider(ctx, config)
	require.NoError(t, err)
	require.NotNil(t, provider)

	// Both meter providers should be available - this tests the dual-provider architecture
	userMeterProvider := provider.MeterProvider()
	analyticsMeterProvider := provider.AnalyticsMeterProvider()

	assert.NotNil(t, userMeterProvider, "User meter provider should be available")
	assert.NotNil(t, analyticsMeterProvider, "Analytics meter provider should be available")

	err = provider.Shutdown(ctx)
	assert.NoError(t, err)
}

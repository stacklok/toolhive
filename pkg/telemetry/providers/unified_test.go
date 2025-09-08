package providers

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnifiedMeterProvider_BothProviders(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	config := Config{
		ServiceName:                 "test-service",
		ServiceVersion:              "1.0.0",
		OTLPEndpoint:                "localhost:4318",
		Insecure:                    true,
		SamplingRate:                0.1,
		TracingEnabled:              true,
		MetricsEnabled:              true,
		EnablePrometheusMetricsPath: true,
	}

	builder := WithConfig(config)
	provider, err := builder.Build(ctx)
	require.NoError(t, err)
	require.NotNil(t, provider)
	defer provider.Shutdown(ctx)

	// Create a test metric using the meter provider
	meter := provider.MeterProvider().Meter("test")
	counter, err := meter.Int64Counter("test_unified_counter")
	require.NoError(t, err)

	// Record some values
	counter.Add(ctx, 1)
	counter.Add(ctx, 2)
	counter.Add(ctx, 3)

	// Check Prometheus endpoint
	require.NotNil(t, provider.PrometheusHandler())
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	provider.PrometheusHandler().ServeHTTP(rec, req)

	assert.Equal(t, 200, rec.Code)
	body := rec.Body.String()

	// Verify the metric appears in Prometheus output
	assert.True(t, strings.Contains(body, "test_unified_counter"),
		"Prometheus should contain our test metric")

	// The total should be 6 (1+2+3)
	assert.True(t, strings.Contains(body, "test_unified_counter_total"),
		"Prometheus should show the counter with _total suffix")
}

func TestUnifiedMeterProvider_PrometheusOnly(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	config := Config{
		ServiceName:                 "test-service",
		ServiceVersion:              "1.0.0",
		OTLPEndpoint:                "localhost:4318",
		Insecure:                    true,
		SamplingRate:                0.1,
		TracingEnabled:              false,
		MetricsEnabled:              false,
		EnablePrometheusMetricsPath: true,
	}

	builder := WithConfig(config)
	provider, err := builder.Build(ctx)
	require.NoError(t, err)
	require.NotNil(t, provider)
	defer provider.Shutdown(ctx)

	// Create a test metric
	meter := provider.MeterProvider().Meter("test")
	histogram, err := meter.Float64Histogram("test_histogram")
	require.NoError(t, err)

	// Record some values
	histogram.Record(ctx, 10.5)
	histogram.Record(ctx, 20.3)

	// Check Prometheus endpoint
	require.NotNil(t, provider.PrometheusHandler())
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	provider.PrometheusHandler().ServeHTTP(rec, req)

	assert.Equal(t, 200, rec.Code)
	assert.Contains(t, rec.Body.String(), "test_histogram")
}

func TestUnifiedMeterProvider_OTLPOnly(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	config := Config{
		ServiceName:                 "test-service",
		ServiceVersion:              "1.0.0",
		OTLPEndpoint:                "localhost:4318",
		Insecure:                    true,
		SamplingRate:                0.1,
		TracingEnabled:              true,
		MetricsEnabled:              true,
		EnablePrometheusMetricsPath: false,
	}

	builder := WithConfig(config)
	provider, err := builder.Build(ctx)
	require.NoError(t, err)
	require.NotNil(t, provider)
	defer provider.Shutdown(ctx)

	// Should have meter provider but no Prometheus handler
	assert.NotNil(t, provider.MeterProvider())
	assert.Nil(t, provider.PrometheusHandler())

	// Should still be able to create metrics (they go to OTLP)
	meter := provider.MeterProvider().Meter("test")
	gauge, err := meter.Int64UpDownCounter("test_gauge")
	require.NoError(t, err)

	// Record values
	gauge.Add(ctx, 100)
	gauge.Add(ctx, -50)
}

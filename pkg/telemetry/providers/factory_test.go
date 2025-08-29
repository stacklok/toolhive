package providers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewBuilder(t *testing.T) {
	t.Parallel()

	config := Config{
		ServiceName:    "test-service",
		ServiceVersion: "1.0.0",
	}

	builder := NewBuilder(config)
	assert.NotNil(t, builder)
	assert.Equal(t, config, builder.config)
}

func TestBuilder_Build_NoOp(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	config := Config{
		ServiceName:    "test-service",
		ServiceVersion: "1.0.0",
		// No endpoints configured
	}

	builder := NewBuilder(config)
	provider, err := builder.Build(ctx)
	require.NoError(t, err)
	require.NotNil(t, provider)

	// Should have no-op providers
	assert.NotNil(t, provider.TracerProvider())
	assert.NotNil(t, provider.MeterProvider())
	assert.Nil(t, provider.PrometheusHandler())

	// Type checking for no-op
	tracerType := fmt.Sprintf("%T", provider.TracerProvider())
	meterType := fmt.Sprintf("%T", provider.MeterProvider())
	assert.Contains(t, tracerType, "noop")
	assert.Contains(t, meterType, "noop")

	// Shutdown should work (may have warnings about already-shutdown readers)
	_ = provider.Shutdown(ctx)
}

func TestBuilder_Build_OTLPOnly(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	config := Config{
		ServiceName:    "test-service",
		ServiceVersion: "1.0.0",
		OTLPEndpoint:   "localhost:4318",
		SamplingRate:   0.5,
		Insecure:       true,
	}

	builder := NewBuilder(config)
	provider, err := builder.Build(ctx)
	require.NoError(t, err)
	require.NotNil(t, provider)

	// Should have real tracer, meter provider
	assert.NotNil(t, provider.TracerProvider())
	assert.NotNil(t, provider.MeterProvider())
	assert.Nil(t, provider.PrometheusHandler())

	// Type checking - should not be no-op
	tracerType := fmt.Sprintf("%T", provider.TracerProvider())
	assert.NotContains(t, tracerType, "noop")

	// Shutdown may fail due to network
	_ = provider.Shutdown(ctx)
}

func TestBuilder_Build_PrometheusOnly(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	config := Config{
		ServiceName:                 "test-service",
		ServiceVersion:              "1.0.0",
		EnablePrometheusMetricsPath: true,
	}

	builder := NewBuilder(config)
	provider, err := builder.Build(ctx)
	require.NoError(t, err)
	require.NotNil(t, provider)

	// Should have no-op tracer, real meter provider and handler
	assert.NotNil(t, provider.TracerProvider())
	assert.NotNil(t, provider.MeterProvider())
	assert.NotNil(t, provider.PrometheusHandler())

	// Type checking - tracer should be no-op
	tracerType := fmt.Sprintf("%T", provider.TracerProvider())
	assert.Contains(t, tracerType, "noop")

	// Test Prometheus handler
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	provider.PrometheusHandler().ServeHTTP(rec, req)
	assert.Equal(t, 200, rec.Code)

	// Shutdown should work (may have warnings about already-shutdown readers)
	_ = provider.Shutdown(ctx)
}

func TestBuilder_Build_BothProviders(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	config := Config{
		ServiceName:                 "test-service",
		ServiceVersion:              "1.0.0",
		OTLPEndpoint:                "localhost:4318",
		Headers:                     map[string]string{"x-api-key": "test"},
		SamplingRate:                0.1,
		Insecure:                    true,
		EnablePrometheusMetricsPath: true,
	}

	builder := NewBuilder(config)
	provider, err := builder.Build(ctx)
	require.NoError(t, err)
	require.NotNil(t, provider)

	// Should have all providers
	assert.NotNil(t, provider.TracerProvider())
	assert.NotNil(t, provider.MeterProvider())
	assert.NotNil(t, provider.PrometheusHandler())

	// Type checking - should not be no-op
	tracerType := fmt.Sprintf("%T", provider.TracerProvider())
	assert.NotContains(t, tracerType, "noop")

	// Test Prometheus handler
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	provider.PrometheusHandler().ServeHTTP(rec, req)
	assert.Equal(t, 200, rec.Code)

	// Shutdown may fail due to network
	_ = provider.Shutdown(ctx)
}

func TestBuilder_CreateResource(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	config := Config{
		ServiceName:    "test-service",
		ServiceVersion: "1.2.3",
	}

	builder := NewBuilder(config)
	resource, err := builder.createResource(ctx)
	require.NoError(t, err)
	require.NotNil(t, resource)

	// Check attributes
	attrs := resource.Attributes()
	hasName := false
	hasVersion := false
	for _, attr := range attrs {
		if attr.Key == "service.name" && attr.Value.AsString() == "test-service" {
			hasName = true
		}
		if attr.Key == "service.version" && attr.Value.AsString() == "1.2.3" {
			hasVersion = true
		}
	}
	assert.True(t, hasName, "service.name attribute should be present")
	assert.True(t, hasVersion, "service.version attribute should be present")
}

func TestCompositeProvider_MultipleShutdown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	config := Config{
		ServiceName:                 "test-service",
		ServiceVersion:              "1.0.0",
		OTLPEndpoint:                "localhost:4318",
		Insecure:                    true,
		EnablePrometheusMetricsPath: true,
	}

	builder := NewBuilder(config)
	provider, err := builder.Build(ctx)
	require.NoError(t, err)
	require.NotNil(t, provider)

	// Multiple shutdowns should not panic
	_ = provider.Shutdown(ctx)
	_ = provider.Shutdown(ctx)
}

func TestBuilder_Build_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		config       Config
		wantTracer   bool
		wantMeter    bool
		wantHandler  bool
		wantNoOpType bool
	}{
		{
			name: "empty config",
			config: Config{
				ServiceName:    "test",
				ServiceVersion: "1.0.0",
			},
			wantTracer:   true,
			wantMeter:    true,
			wantHandler:  false,
			wantNoOpType: true,
		},
		{
			name: "only OTLP endpoint",
			config: Config{
				ServiceName:    "test",
				ServiceVersion: "1.0.0",
				OTLPEndpoint:   "localhost:4318",
				Insecure:       true,
			},
			wantTracer:   true,
			wantMeter:    true,
			wantHandler:  false,
			wantNoOpType: false,
		},
		{
			name: "only Prometheus",
			config: Config{
				ServiceName:                 "test",
				ServiceVersion:              "1.0.0",
				EnablePrometheusMetricsPath: true,
			},
			wantTracer:   true,
			wantMeter:    true,
			wantHandler:  true,
			wantNoOpType: false, // Only tracer is no-op
		},
		{
			name: "all enabled",
			config: Config{
				ServiceName:                 "test",
				ServiceVersion:              "1.0.0",
				OTLPEndpoint:                "localhost:4318",
				EnablePrometheusMetricsPath: true,
				Insecure:                    true,
			},
			wantTracer:   true,
			wantMeter:    true,
			wantHandler:  true,
			wantNoOpType: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			builder := NewBuilder(tt.config)
			provider, err := builder.Build(ctx)
			require.NoError(t, err)
			require.NotNil(t, provider)

			if tt.wantTracer {
				assert.NotNil(t, provider.TracerProvider())
			}
			if tt.wantMeter {
				assert.NotNil(t, provider.MeterProvider())
			}
			if tt.wantHandler {
				assert.NotNil(t, provider.PrometheusHandler())
			} else {
				assert.Nil(t, provider.PrometheusHandler())
			}

			// Check if providers are no-op when expected
			if tt.wantNoOpType {
				tracerType := fmt.Sprintf("%T", provider.TracerProvider())
				meterType := fmt.Sprintf("%T", provider.MeterProvider())
				assert.Contains(t, tracerType, "noop")
				assert.Contains(t, meterType, "noop")
			}

			// Cleanup
			_ = provider.Shutdown(ctx)
		})
	}
}

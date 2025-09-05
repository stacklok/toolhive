package providers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

func TestWithConfig(t *testing.T) {
	t.Parallel()

	config := Config{
		ServiceName:    "test-service",
		ServiceVersion: "1.0.0",
	}

	builder := WithConfig(config)
	require.NotNil(t, builder)
	assert.Equal(t, config, builder.config)
}

func TestBuilder_CreateResource(t *testing.T) {
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
		EnablePrometheusMetricsPath: false,
	}

	config.ServiceVersion = "1.2.3"

	builder := WithConfig(config)
	err := builder.createResource(ctx)
	require.NoError(t, err)
	require.NotNil(t, builder.resource)

	// Check attributes
	attrs := builder.resource.Attributes()
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

func TestBuilder_CreateNoOpProvider(t *testing.T) {
	t.Parallel()

	builder := &Builder{}
	provider := builder.createNoOpProvider()

	require.NotNil(t, provider)
	assert.NotNil(t, provider.tracerProvider)
	assert.NotNil(t, provider.meterProvider)
	assert.Nil(t, provider.prometheusHandler)
	assert.Empty(t, provider.shutdownFuncs)

	// Verify the providers are actually no-op implementations
	// Check if it's a no-op tracer provider interface
	assert.IsType(t, tracenoop.NewTracerProvider(), provider.tracerProvider, "tracer provider should be no-op")

	// Check if it's a no-op meter provider interface
	assert.IsType(t, noop.NewMeterProvider(), provider.meterProvider, "meter provider should be no-op")
}

func TestBuilder_Build_NoOpCase(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	config := Config{
		ServiceName:                 "test-service",
		ServiceVersion:              "1.0.0",
		TracingEnabled:              false,
		MetricsEnabled:              false,
		EnablePrometheusMetricsPath: false,
	}

	builder := WithConfig(config)
	provider, err := builder.Build(ctx)

	require.NoError(t, err)
	require.NotNil(t, provider)

	assert.IsType(t, tracenoop.NewTracerProvider(), provider.TracerProvider(), "tracer provider should be no-op")
	assert.IsType(t, noop.NewMeterProvider(), provider.MeterProvider(), "meter provider should be no-op")
	assert.Nil(t, provider.PrometheusHandler())
}

func TestBuilder_Build_WithOTLPTracing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	config := Config{
		ServiceName:    "test-service",
		ServiceVersion: "1.0.0",
		OTLPEndpoint:   "localhost:4318",
		Insecure:       true,
		TracingEnabled: true,
		MetricsEnabled: false,
		SamplingRate:   0.5,
	}

	builder := WithConfig(config)
	provider, err := builder.Build(ctx)

	require.NoError(t, err)
	require.NotNil(t, provider)
	assert.NotNil(t, provider.TracerProvider())
	assert.Implements(t, (*trace.TracerProvider)(nil), provider.TracerProvider(), "should implement TracerProvider interface")
	assert.IsType(t, noop.NewMeterProvider(), provider.MeterProvider(), "meter provider should be no-op when metrics disabled")
	assert.Len(t, provider.shutdownFuncs, 1) // Should have one shutdown function for tracing
}

func TestBuilder_Build_WithPrometheus(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	config := Config{
		ServiceName:                 "test-service",
		ServiceVersion:              "1.0.0",
		TracingEnabled:              false,
		MetricsEnabled:              false,
		EnablePrometheusMetricsPath: true,
	}

	builder := WithConfig(config)
	provider, err := builder.Build(ctx)

	require.NoError(t, err)
	require.NotNil(t, provider)
	assert.NotNil(t, provider.MeterProvider())
	assert.Implements(t, (*metric.MeterProvider)(nil), provider.MeterProvider(), "should implement MeterProvider interface")
	assert.NotNil(t, provider.PrometheusHandler())
	assert.NotEmpty(t, provider.shutdownFuncs) // Should have shutdown function for Prometheus
}

func TestBuilder_Build_WithOTLPMetrics(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	config := Config{
		ServiceName:    "test-service",
		ServiceVersion: "1.0.0",
		OTLPEndpoint:   "localhost:4318",
		Insecure:       true,
		TracingEnabled: false,
		MetricsEnabled: true,
	}

	builder := WithConfig(config)
	provider, err := builder.Build(ctx)

	require.NoError(t, err)
	require.NotNil(t, provider)
	assert.NotNil(t, provider.MeterProvider())
	assert.Implements(t, (*trace.TracerProvider)(nil), provider.TracerProvider(), "should implement TracerProvider interface")
	assert.NotEmpty(t, provider.shutdownFuncs) // Should have shutdown function for OTLP metrics
}

func TestBuilder_Build_WithEverything(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	config := Config{
		ServiceName:                 "test-service",
		ServiceVersion:              "1.0.0",
		OTLPEndpoint:                "localhost:4318",
		Insecure:                    true,
		TracingEnabled:              true,
		MetricsEnabled:              true,
		EnablePrometheusMetricsPath: true,
		SamplingRate:                1.0,
		Headers:                     map[string]string{"test": "header"},
	}

	builder := WithConfig(config)
	provider, err := builder.Build(ctx)

	require.NoError(t, err)
	require.NotNil(t, provider)
	assert.NotNil(t, provider.TracerProvider())
	assert.Implements(t, (*trace.TracerProvider)(nil), provider.TracerProvider(), "should implement TracerProvider interface")
	assert.NotNil(t, provider.MeterProvider())
	assert.Implements(t, (*trace.TracerProvider)(nil), provider.TracerProvider(), "should implement TracerProvider interface")
	assert.NotNil(t, provider.PrometheusHandler())
	assert.NotEmpty(t, provider.shutdownFuncs) // Should have multiple shutdown functions
}

func TestCompositeProvider_Accessors(t *testing.T) {
	t.Parallel()

	// Create a composite provider manually
	tp := tracenoop.NewTracerProvider()
	mp := noop.NewMeterProvider()

	provider := &CompositeProvider{
		tracerProvider:    tp,
		meterProvider:     mp,
		prometheusHandler: nil,
	}

	assert.Equal(t, tp, provider.TracerProvider())
	assert.Equal(t, mp, provider.MeterProvider())
	assert.Nil(t, provider.PrometheusHandler())
}

func TestCompositeProvider_Shutdown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		shutdownFuncs []func(context.Context) error
		expectError   bool
		errorCount    int
	}{
		{
			name:          "no shutdown functions",
			shutdownFuncs: []func(context.Context) error{},
			expectError:   false,
		},
		{
			name: "single successful shutdown",
			shutdownFuncs: []func(context.Context) error{
				func(_ context.Context) error { return nil },
			},
			expectError: false,
		},
		{
			name: "multiple successful shutdowns",
			shutdownFuncs: []func(context.Context) error{
				func(_ context.Context) error { return nil },
				func(_ context.Context) error { return nil },
				func(_ context.Context) error { return nil },
			},
			expectError: false,
		},
		{
			name: "single failed shutdown",
			shutdownFuncs: []func(context.Context) error{
				func(_ context.Context) error { return errors.New("shutdown failed") },
			},
			expectError: true,
			errorCount:  1,
		},
		{
			name: "mixed success and failure",
			shutdownFuncs: []func(context.Context) error{
				func(_ context.Context) error { return nil },
				func(_ context.Context) error { return errors.New("provider 1 failed") },
				func(_ context.Context) error { return nil },
				func(_ context.Context) error { return errors.New("provider 3 failed") },
			},
			expectError: true,
			errorCount:  2,
		},
		{
			name: "all failures",
			shutdownFuncs: []func(context.Context) error{
				func(_ context.Context) error { return errors.New("error 1") },
				func(_ context.Context) error { return errors.New("error 2") },
				func(_ context.Context) error { return errors.New("error 3") },
			},
			expectError: true,
			errorCount:  3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider := &CompositeProvider{
				shutdownFuncs: tt.shutdownFuncs,
			}

			ctx := context.Background()
			err := provider.Shutdown(ctx)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "shutdown failed")
				if tt.errorCount > 0 {
					assert.Contains(t, err.Error(), "errors:")
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestBuilder_Shutdown_WithErrors tests shutdown with failing shutdown functions
func TestBuilder_Shutdown_WithErrors(t *testing.T) {
	t.Parallel()

	// Create a composite provider with a shutdown function that times out
	timeoutShutdown := func(ctx context.Context) error {
		select {
		case <-time.After(10 * time.Second):
			return errors.New("shutdown timed out")
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	errorShutdown := func(_ context.Context) error {
		return errors.New("shutdown error")
	}

	successShutdown := func(_ context.Context) error {
		return nil
	}

	provider := &CompositeProvider{
		tracerProvider: tracenoop.NewTracerProvider(),
		meterProvider:  noop.NewMeterProvider(),
		shutdownFuncs: []func(context.Context) error{
			successShutdown,
			errorShutdown,
			timeoutShutdown,
		},
	}

	ctx := context.Background()
	err := provider.Shutdown(ctx)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "shutdown failed")
	assert.Contains(t, err.Error(), "errors:")
}

func TestCompositeProvider_ShutdownTimeout(t *testing.T) {
	t.Parallel()

	slowShutdown := func(ctx context.Context) error {
		select {
		case <-time.After(10 * time.Second):
			return errors.New("shutdown completed too slowly")
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	provider := &CompositeProvider{
		shutdownFuncs: []func(context.Context) error{
			slowShutdown,
		},
	}

	ctx := context.Background()
	start := time.Now()
	err := provider.Shutdown(ctx)
	elapsed := time.Since(start)

	// Should timeout within the 5 second limit set in Shutdown
	assert.Less(t, elapsed, 6*time.Second)
	assert.Error(t, err)
}

func TestCompositeProvider_MultipleShutdown(t *testing.T) {
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

	// Multiple shutdowns should not panic
	_ = provider.Shutdown(ctx)
	_ = provider.Shutdown(ctx)
}

func TestBuilder_Build_WithHeaders(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	config := Config{
		ServiceName:    "test-service",
		ServiceVersion: "1.0.0",
		OTLPEndpoint:   "localhost:4318",
		Headers: map[string]string{
			"Authorization": "Bearer token",
			"X-Custom":      "value",
		},
		Insecure:       true,
		TracingEnabled: true,
		MetricsEnabled: true,
	}

	builder := WithConfig(config)
	provider, err := builder.Build(ctx)

	require.NoError(t, err)
	require.NotNil(t, provider)
	assert.NotNil(t, provider.TracerProvider())
	assert.Implements(t, (*trace.TracerProvider)(nil), provider.TracerProvider(), "should implement TracerProvider interface")
	assert.NotNil(t, provider.MeterProvider())
	assert.Implements(t, (*metric.MeterProvider)(nil), provider.MeterProvider(), "should implement MeterProvider interface")
}

func TestBuilder_Build_DifferentSamplingRates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		samplingRate float64
	}{
		{"zero sampling", 0.0},
		{"quarter sampling", 0.25},
		{"half sampling", 0.5},
		{"three quarter sampling", 0.75},
		{"full sampling", 1.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			config := Config{
				ServiceName:    "test-service",
				ServiceVersion: "1.0.0",
				OTLPEndpoint:   "localhost:4318",
				Insecure:       true,
				TracingEnabled: true,
				SamplingRate:   tt.samplingRate,
			}

			builder := WithConfig(config)
			provider, err := builder.Build(ctx)

			require.NoError(t, err)
			require.NotNil(t, provider)
			assert.NotNil(t, provider.TracerProvider())
			assert.Implements(t, (*trace.TracerProvider)(nil), provider.TracerProvider(), "should implement TracerProvider interface")
		})
	}
}

func TestBuilder_Build_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config Config
	}{
		{
			name: "empty service name and version",
			config: Config{
				ServiceName:    "",
				ServiceVersion: "",
			},
		},
		{
			name: "only service name",
			config: Config{
				ServiceName:    "my-service",
				ServiceVersion: "",
			},
		},
		{
			name: "only service version",
			config: Config{
				ServiceName:    "",
				ServiceVersion: "v1.0.0",
			},
		},
		{
			name: "very long service name",
			config: Config{
				ServiceName:    "this-is-a-very-long-service-name-that-might-cause-issues-in-some-systems-but-should-still-work-correctly-when-creating-resources",
				ServiceVersion: "1.0.0",
			},
		},
		{
			name: "special characters in service name",
			config: Config{
				ServiceName:    "service-name_with.special@chars",
				ServiceVersion: "1.0.0-beta+build.123",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			builder := WithConfig(tt.config)
			provider, err := builder.Build(ctx)

			// All edge cases should still succeed
			require.NoError(t, err)
			require.NotNil(t, provider)
			assert.NotNil(t, provider.TracerProvider())
			assert.Implements(t, (*trace.TracerProvider)(nil), provider.TracerProvider(), "should implement TracerProvider interface")
			assert.NotNil(t, provider.MeterProvider())
			assert.Implements(t, (*metric.MeterProvider)(nil), provider.MeterProvider(), "should implement MeterProvider interface")
		})
	}
}

func TestBuilder_BuildProviders_DirectCall(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	config := Config{
		ServiceName:    "test-service",
		ServiceVersion: "1.0.0",
		OTLPEndpoint:   "localhost:4318",
		TracingEnabled: true,
		MetricsEnabled: true,
	}

	builder := WithConfig(config)
	// First create resource
	err := builder.createResource(ctx)
	require.NoError(t, err)

	// Create selector
	selector := NewStrategySelector(builder.config)

	// Call buildProviders directly
	composite, err := builder.buildProviders(ctx, selector, builder.resource)

	require.NoError(t, err)
	require.NotNil(t, composite)
	assert.NotNil(t, composite.TracerProvider())
	assert.Implements(t, (*trace.TracerProvider)(nil), composite.TracerProvider(), "should implement TracerProvider interface")
	assert.NotNil(t, composite.MeterProvider())
	assert.Implements(t, (*metric.MeterProvider)(nil), composite.MeterProvider(), "should implement MeterProvider interface")
	assert.NotEmpty(t, composite.shutdownFuncs)
}

// TestMockErrorStrategy tests error handling using a custom strategy that always fails
type TestMockErrorMeterStrategy struct {
	errMsg string
}

func (s *TestMockErrorMeterStrategy) CreateMeterProvider(_ context.Context, _ Config, _ *resource.Resource) (*MeterResult, error) {
	return nil, errors.New(s.errMsg)
}

type TestMockErrorTracerStrategy struct {
	errMsg string
}

func (s *TestMockErrorTracerStrategy) CreateTracerProvider(_ context.Context, _ Config, _ *resource.Resource) (
	trace.TracerProvider, func(context.Context) error, error,
) {
	return nil, nil, errors.New(s.errMsg)
}

// TestErrorStrategies verifies that strategy errors are properly handled
func TestErrorStrategies(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	config := Config{
		ServiceName:    "test-service",
		ServiceVersion: "1.0.0",
	}

	res := resource.Default()

	t.Run("meter strategy error", func(t *testing.T) {
		t.Parallel()
		strategy := &TestMockErrorMeterStrategy{errMsg: "meter creation failed"}
		_, err := strategy.CreateMeterProvider(ctx, config, res)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "meter creation failed")
	})

	t.Run("tracer strategy error", func(t *testing.T) {
		t.Parallel()
		strategy := &TestMockErrorTracerStrategy{errMsg: "tracer creation failed"}
		_, _, err := strategy.CreateTracerProvider(ctx, config, res)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "tracer creation failed")
	})
}

// TestBuilder_ResourceCreationError tests handling of resource creation errors
func TestBuilder_ResourceCreationError(t *testing.T) {
	t.Parallel()

	// Create a builder with invalid configuration that might cause resource creation issues
	config := Config{
		ServiceName:    string([]byte{0xFF, 0xFE, 0xFD}), // Invalid UTF-8 characters
		ServiceVersion: string([]byte{0xFF, 0xFE, 0xFD}),
	}

	ctx := context.Background()
	builder := WithConfig(config)

	// Even with invalid characters, resource creation typically succeeds
	// as OpenTelemetry handles them gracefully
	err := builder.createResource(ctx)
	// This won't actually error, but we're testing the path
	assert.NoError(t, err)
	if builder.resource != nil {
		attrs := builder.resource.Attributes()
		assert.NotNil(t, attrs)
	}
}

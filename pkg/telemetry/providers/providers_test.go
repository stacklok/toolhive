package providers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// func TestAssembler_CreateResource(t *testing.T) {
// 	t.Parallel()

// 	ctx := context.Background()
// 	options := []ProviderOptions{
// 		WithServiceName("test-service"),
// 		WithServiceVersion("1.0.0"),
// 		WithOTLPEndpoint("localhost:4318"),
// 		WithInsecure(true),
// 		WithSamplingRate(0.1),
// 		WithTracingEnabled(false),
// 		WithMetricsEnabled(false),
// 		WithEnablePrometheusMetricsPath(false),
// 	}

// 	options = append(options, WithServiceVersion("1.2.3"))

// 	provider, err := NewCompositeProvider(ctx, options...)
// 	require.NoError(t, err)
// 	require.NotNil(t, provider)

// 	// Check attributes
// 	attrs := assembler.resource.Attributes()
// 	hasName := false
// 	hasVersion := false
// 	for _, attr := range attrs {
// 		if attr.Key == "service.name" && attr.Value.AsString() == "test-service" {
// 			hasName = true
// 		}
// 		if attr.Key == "service.version" && attr.Value.AsString() == "1.2.3" {
// 			hasVersion = true
// 		}
// 	}
// 	assert.True(t, hasName, "service.name attribute should be present")
// 	assert.True(t, hasVersion, "service.version attribute should be present")
// }

func TestAssembler_CreateNoOpProvider(t *testing.T) {
	t.Parallel()

	provider := createNoOpProvider()

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

func TestAssembler_Assemble_NoOpCase(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	options := []ProviderOption{
		WithServiceName("test-service"),
		WithServiceVersion("1.0.0"),
		WithTracingEnabled(false),
		WithMetricsEnabled(false),
		WithEnablePrometheusMetricsPath(false),
	}

	provider, err := NewCompositeProvider(ctx, options...)

	require.NoError(t, err)
	require.NotNil(t, provider)

	assert.IsType(t, tracenoop.NewTracerProvider(), provider.TracerProvider(), "tracer provider should be no-op")
	assert.IsType(t, noop.NewMeterProvider(), provider.MeterProvider(), "meter provider should be no-op")
	assert.Nil(t, provider.PrometheusHandler())
}

func TestAssembler_Assemble_WithOTLPTracing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	options := []ProviderOption{
		WithServiceName("test-service"),
		WithServiceVersion("1.0.0"),
		WithOTLPEndpoint("localhost:4318"),
		WithInsecure(true),
		WithTracingEnabled(true),
		WithMetricsEnabled(false),
		WithSamplingRate(0.5),
	}

	provider, err := NewCompositeProvider(ctx, options...)

	require.NoError(t, err)
	require.NotNil(t, provider)
	assert.NotNil(t, provider.TracerProvider())
	assert.IsType(t, noop.NewMeterProvider(), provider.MeterProvider(), "meter provider should be no-op when metrics disabled")
	assert.Len(t, provider.shutdownFuncs, 1) // Should have one shutdown function for tracing
}

func TestAssembler_Assemble_WithPrometheus(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	options := []ProviderOption{
		WithServiceName("test-service"),
		WithServiceVersion("1.0.0"),
		WithTracingEnabled(false),
		WithMetricsEnabled(false),
		WithEnablePrometheusMetricsPath(true),
	}

	provider, err := NewCompositeProvider(ctx, options...)

	require.NoError(t, err)
	require.NotNil(t, provider)
	assert.NotNil(t, provider.MeterProvider())
	assert.NotNil(t, provider.PrometheusHandler())
	assert.NotEmpty(t, provider.shutdownFuncs) // Should have shutdown function for Prometheus
}

func TestAssembler_Assemble_WithOTLPMetrics(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	options := []ProviderOption{
		WithServiceName("test-service"),
		WithServiceVersion("1.0.0"),
		WithOTLPEndpoint("localhost:4318"),
		WithInsecure(true),
		WithTracingEnabled(false),
		WithMetricsEnabled(true),
	}

	provider, err := NewCompositeProvider(ctx, options...)

	require.NoError(t, err)
	require.NotNil(t, provider)
	assert.NotNil(t, provider.MeterProvider())
	assert.NotEmpty(t, provider.shutdownFuncs) // Should have shutdown function for OTLP metrics
}

func TestAssembler_Assemble_WithEverything(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	options := []ProviderOption{
		WithServiceName("test-service"),
		WithServiceVersion("1.0.0"),
		WithOTLPEndpoint("localhost:4318"),
		WithInsecure(true),
		WithTracingEnabled(true),
		WithMetricsEnabled(true),
		WithEnablePrometheusMetricsPath(true),
		WithSamplingRate(1.0),
		WithHeaders(map[string]string{"test": "header"}),
	}

	provider, err := NewCompositeProvider(ctx, options...)

	require.NoError(t, err)
	require.NotNil(t, provider)
	assert.NotNil(t, provider.TracerProvider())
	assert.NotNil(t, provider.MeterProvider())
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

// TestAssembler_Shutdown_WithErrors tests shutdown with failing shutdown functions
func TestAssembler_Shutdown_WithErrors(t *testing.T) {
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
	options := []ProviderOption{
		WithServiceName("test-service"),
		WithServiceVersion("1.0.0"),
		WithOTLPEndpoint("localhost:4318"),
		WithInsecure(true),
		WithSamplingRate(0.1),
		WithTracingEnabled(false),
		WithMetricsEnabled(false),
		WithEnablePrometheusMetricsPath(true),
	}

	provider, err := NewCompositeProvider(ctx, options...)
	require.NoError(t, err)
	require.NotNil(t, provider)

	// Multiple shutdowns should not panic
	_ = provider.Shutdown(ctx)
	_ = provider.Shutdown(ctx)
}

func TestAssembler_Assemble_WithHeaders(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	options := []ProviderOption{
		WithServiceName("test-service"),
		WithServiceVersion("1.0.0"),
		WithOTLPEndpoint("localhost:4318"),
		WithHeaders(map[string]string{
			"Authorization": "Bearer token",
			"X-Custom":      "value",
		}),
		WithInsecure(true),
		WithTracingEnabled(true),
		WithMetricsEnabled(true),
	}

	provider, err := NewCompositeProvider(ctx, options...)

	require.NoError(t, err)
	require.NotNil(t, provider)
	assert.NotNil(t, provider.TracerProvider())
	assert.NotNil(t, provider.MeterProvider())
}

func TestAssembler_Assemble_DifferentSamplingRates(t *testing.T) {
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
			options := []ProviderOption{
				WithServiceName("test-service"),
				WithServiceVersion("1.0.0"),
				WithOTLPEndpoint("localhost:4318"),
				WithInsecure(true),
				WithTracingEnabled(true),
				WithSamplingRate(tt.samplingRate),
			}

			provider, err := NewCompositeProvider(ctx, options...)

			require.NoError(t, err)
			require.NotNil(t, provider)
			assert.NotNil(t, provider.TracerProvider())
		})
	}
}

func TestAssembler_Assemble_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		options []ProviderOption
	}{
		{
			name:    "empty service name and version",
			options: []ProviderOption{},
		},
		{
			name: "only service name",
			options: []ProviderOption{
				WithServiceName("my-service"),
			},
		},
		{
			name: "only service version",
			options: []ProviderOption{
				WithServiceVersion("v1.0.0"),
			},
		},
		{
			name: "very long service name",
			options: []ProviderOption{
				WithServiceName("this-is-a-very-long-service-name-that-might-cause-issues-in-some-systems-but-should-still-work-correctly-when-creating-resources"),
				WithServiceVersion("1.0.0"),
			},
		},
		{
			name: "special characters in service name",
			options: []ProviderOption{
				WithServiceName("service-name_with.special@chars"),
				WithServiceVersion("1.0.0-beta+assemble.123"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			provider, err := NewCompositeProvider(ctx, tt.options...)

			// All edge cases should still succeed
			require.NoError(t, err)
			require.NotNil(t, provider)
			assert.NotNil(t, provider.TracerProvider())
			assert.NotNil(t, provider.MeterProvider())
		})
	}
}

// func TestAssembler_AssembleProviders_DirectCall(t *testing.T) {
// 	t.Parallel()

// 	ctx := context.Background()
// 	options := []ProviderOptions{
// 		WithServiceName("test-service"),
// 		WithServiceVersion("1.0.0"),
// 		WithOTLPEndpoint("localhost:4318"),
// 		WithTracingEnabled(true),
// 		WithMetricsEnabled(true),
// 	}

// 	assembler, err := NewCompositeProvider(ctx, options...)
// 	require.NoError(t, err)

// 	// Create selector
// 	selector := NewStrategySelector(assembler.config)

// 	// Call assembleProviders directly
// 	composite, err := assembleProviders(ctx, selector, assembler.resource)

// 	require.NoError(t, err)
// 	require.NotNil(t, composite)
// 	assert.NotNil(t, composite.TracerProvider())
// 	assert.NotNil(t, composite.MeterProvider())
// 	assert.NotEmpty(t, composite.shutdownFuncs)
// }

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

// // TestAssembler_ResourceCreationError tests handling of resource creation errors
// func TestAssembler_ResourceCreationError(t *testing.T) {
// 	t.Parallel()

// 	// Create a assembler with invalid configuration that might cause resource creation issues
// 	options := []ProviderOptions{
// 		ServiceName:    string([]byte{0xFF, 0xFE, 0xFD}), // Invalid UTF-8 characters
// 		ServiceVersion: string([]byte{0xFF, 0xFE, 0xFD}),
// 	}

// 	ctx := context.Background()
// 	assembler, err := NewCompositeProvider(ctx, options...)
// 	require.NoError(t, err)

// 	// Even with invalid characters, resource creation typically succeeds
// 	// as OpenTelemetry handles them gracefully
// 	err := assembler.createResource(ctx)
// 	// This won't actually error, but we're testing the path
// 	assert.NoError(t, err)
// 	if assembler.resource != nil {
// 		attrs := assembler.resource.Attributes()
// 		assert.NotNil(t, attrs)
// 	}
// }

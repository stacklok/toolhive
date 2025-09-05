package providers

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
)

func TestStrategySelector_SelectTracerStrategy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		config       Config
		expectedType string
	}{
		{
			name: "OTLP tracer when endpoint and tracing enabled",
			config: Config{
				OTLPEndpoint:   "localhost:4318",
				TracingEnabled: true,
			},
			expectedType: "*providers.OTLPTracerStrategy",
		},
		{
			name: "NoOp tracer when endpoint but tracing disabled",
			config: Config{
				OTLPEndpoint:   "localhost:4318",
				TracingEnabled: false,
			},
			expectedType: "*providers.NoOpTracerStrategy",
		},
		{
			name: "NoOp tracer when no endpoint",
			config: Config{
				TracingEnabled: true,
			},
			expectedType: "*providers.NoOpTracerStrategy",
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			selector := NewStrategySelector(tt.config)
			strategy := selector.SelectTracerStrategy()

			assert.NotNil(t, strategy)
			assert.Equal(t, tt.expectedType, getTypeName(strategy))
		})
	}
}

func TestNoOpTracerStrategy_CreateTracerProvider(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	res := createTestResource(t)
	config := Config{}

	strategy := &NoOpTracerStrategy{}
	provider, shutdown, err := strategy.CreateTracerProvider(ctx, config, res)

	require.NoError(t, err)
	require.NotNil(t, provider)
	assert.Nil(t, shutdown, "Expected no shutdown function for no-op tracer")

	// Verify it's actually a no-op provider
	typeName := getTypeName(provider)
	assert.Contains(t, typeName, "noop", "Expected no-op tracer provider, got %s", typeName)
}

func TestOTLPTracerStrategy_CreateTracerProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		config    Config
		expectErr bool
	}{
		{
			name: "Valid OTLP config",
			config: Config{
				OTLPEndpoint: "localhost:4318",
				Insecure:     true,
				SamplingRate: 0.1,
			},
			expectErr: false,
		},
		{
			name: "Valid OTLP config with headers",
			config: Config{
				OTLPEndpoint: "localhost:4318",
				Insecure:     true,
				SamplingRate: 1.0,
				Headers:      map[string]string{"Authorization": "Bearer token"},
			},
			expectErr: false,
		},
		{
			name: "Valid secure OTLP config",
			config: Config{
				OTLPEndpoint: "https://api.example.com:4318",
				Insecure:     false,
				SamplingRate: 0.5,
			},
			expectErr: false,
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			res := createTestResource(t)
			strategy := &OTLPTracerStrategy{}

			provider, shutdown, err := strategy.CreateTracerProvider(ctx, tt.config, res)

			if tt.expectErr {
				assert.Error(t, err)
				assert.Nil(t, provider)
				assert.Nil(t, shutdown)
			} else {
				require.NoError(t, err)
				require.NotNil(t, provider)
				require.NotNil(t, shutdown, "Expected shutdown function for OTLP tracer")

				// Verify it's not a no-op provider
				typeName := getTypeName(provider)
				assert.NotContains(t, typeName, "noop", "Expected non-noop tracer provider, got %s", typeName)

				// Clean up
				if shutdown != nil {
					err := shutdown(ctx)
					assert.NoError(t, err, "Shutdown should not error")
				}
			}
		})
	}
}

func TestStrategySelector_SelectMeterStrategy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		config       Config
		expectedType string
	}{
		{
			name: "Unified meter when OTLP metrics enabled",
			config: Config{
				OTLPEndpoint:   "localhost:4318",
				MetricsEnabled: true,
			},
			expectedType: "*providers.UnifiedMeterStrategy",
		},
		{
			name: "Unified meter when Prometheus enabled",
			config: Config{
				EnablePrometheusMetricsPath: true,
			},
			expectedType: "*providers.UnifiedMeterStrategy",
		},
		{
			name: "Unified meter when both OTLP and Prometheus",
			config: Config{
				OTLPEndpoint:                "localhost:4318",
				MetricsEnabled:              true,
				EnablePrometheusMetricsPath: true,
			},
			expectedType: "*providers.UnifiedMeterStrategy",
		},
		{
			name: "NoOp meter when nothing enabled",
			config: Config{
				OTLPEndpoint:   "localhost:4318",
				MetricsEnabled: false,
			},
			expectedType: "*providers.NoOpMeterStrategy",
		},
		{
			name:         "NoOp meter when empty config",
			config:       Config{},
			expectedType: "*providers.NoOpMeterStrategy",
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			selector := NewStrategySelector(tt.config)
			strategy := selector.SelectMeterStrategy()

			assert.NotNil(t, strategy)
			assert.Equal(t, tt.expectedType, getTypeName(strategy))
		})
	}
}

func TestStrategySelector_IsFullyNoOp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		config   Config
		expected bool
	}{
		{
			name:     "fully no-op when nothing configured",
			config:   Config{},
			expected: true,
		},
		{
			name: "not no-op when OTLP tracing enabled",
			config: Config{
				OTLPEndpoint:   "localhost:4318",
				TracingEnabled: true,
			},
			expected: false,
		},
		{
			name: "not no-op when OTLP metrics enabled",
			config: Config{
				OTLPEndpoint:   "localhost:4318",
				MetricsEnabled: true,
			},
			expected: false,
		},
		{
			name: "not no-op when Prometheus enabled",
			config: Config{
				EnablePrometheusMetricsPath: true,
			},
			expected: false,
		},
		{
			name: "fully no-op when endpoint but nothing enabled",
			config: Config{
				OTLPEndpoint:   "localhost:4318",
				TracingEnabled: false,
				MetricsEnabled: false,
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			selector := NewStrategySelector(tt.config)
			result := selector.IsFullyNoOp()

			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestNoOpMeterStrategy_CreateMeterProvider(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	res := createTestResource(t)
	config := Config{}

	strategy := &NoOpMeterStrategy{}
	provider, err := strategy.CreateMeterProvider(ctx, config, res)

	require.NoError(t, err)
	require.NotNil(t, provider)

	// Verify it's actually a no-op provider
	assert.Nil(t, provider.PrometheusHandler)
	assert.Nil(t, provider.ShutdownFunc)
	typeName := getTypeName(provider.MeterProvider)
	assert.Contains(t, typeName, "noop", "Expected no-op meter provider, got %s", typeName)
}

func TestUnifiedMeterStrategy_Configurations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	res := createTestResource(t)

	tests := []struct {
		name             string
		strategy         *UnifiedMeterStrategy
		config           Config
		expectPrometheus bool
		expectOTLP       bool
		expectNoOp       bool
	}{
		{
			name: "OTLP only",
			strategy: &UnifiedMeterStrategy{
				EnableOTLP:       true,
				EnablePrometheus: false,
			},
			config: Config{
				OTLPEndpoint: "localhost:4318",
				Insecure:     true,
			},
			expectPrometheus: false,
			expectOTLP:       true,
			expectNoOp:       false,
		},
		{
			name: "Prometheus only",
			strategy: &UnifiedMeterStrategy{
				EnableOTLP:       false,
				EnablePrometheus: true,
			},
			config:           Config{},
			expectPrometheus: true,
			expectOTLP:       false,
			expectNoOp:       false,
		},
		{
			name: "Both OTLP and Prometheus",
			strategy: &UnifiedMeterStrategy{
				EnableOTLP:       true,
				EnablePrometheus: true,
			},
			config: Config{
				OTLPEndpoint: "localhost:4318",
				Insecure:     true,
			},
			expectPrometheus: true,
			expectOTLP:       true,
			expectNoOp:       false,
		},
		{
			name: "Neither enabled",
			strategy: &UnifiedMeterStrategy{
				EnableOTLP:       false,
				EnablePrometheus: false,
			},
			config:           Config{},
			expectPrometheus: false,
			expectOTLP:       false,
			expectNoOp:       true,
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := tt.strategy.CreateMeterProvider(ctx, tt.config, res)

			require.NoError(t, err)
			require.NotNil(t, result)
			require.NotNil(t, result.MeterProvider)

			if tt.expectPrometheus {
				assert.NotNil(t, result.PrometheusHandler, "Expected Prometheus handler")
			} else {
				assert.Nil(t, result.PrometheusHandler, "Expected no Prometheus handler")
			}

			// Note: OTLP handler is not exposed in MeterResult, only Prometheus handler
			// OTLP functionality is verified through the meter provider type check below

			if tt.expectNoOp {
				assert.Contains(t, getTypeName(result.MeterProvider), "noop")
				assert.Nil(t, result.ShutdownFunc)
				// Verify it's actually a noop provider - need to import noop package
				// The noop.MeterProvider is actually noop.meterProvider (unexported)
				// so we can't do a direct type assertion. Check the type name instead.
				typeName := getTypeName(result.MeterProvider)
				assert.Contains(t, typeName, "noop", "Expected noop meter provider, got %s", typeName)
			} else {
				assert.NotContains(t, getTypeName(result.MeterProvider), "noop")
				// Verify it's actually an SDK provider (not noop)
				_, isSDKProvider := result.MeterProvider.(*sdkmetric.MeterProvider)
				assert.True(t, isSDKProvider, "Expected SDK meter provider, got %T", result.MeterProvider)
				// Shutdown may or may not be nil depending on implementation
			}
		})
	}
}

// Helper functions

func getTypeName(v interface{}) string {
	if v == nil {
		return "nil"
	}
	return fmt.Sprintf("%T", v)
}

func createTestResource(t *testing.T) *resource.Resource {
	t.Helper()
	return createTestResourceWithName(t, "test-service", "1.0.0")
}

func createTestResourceWithName(t *testing.T, serviceName, serviceVersion string) *resource.Resource {
	t.Helper()
	res, err := resource.New(
		context.Background(),
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
		),
	)
	require.NoError(t, err)
	return res
}

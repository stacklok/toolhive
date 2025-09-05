package telemetry

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"

	"github.com/stacklok/toolhive/pkg/telemetry/providers"
)

// TestTelemetryProviderValidation tests the five main telemetry configuration scenarios
// with detailed validation of the created providers and their configurations.
func TestTelemetryProviderValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		name                    string
		config                  Config
		expectError             bool
		errorContains           string
		expectedTracerType      string
		expectedMeterType       string
		expectPrometheusHandler bool
		description             string
	}{
		{
			name: "Scenario 1: Prometheus-only (no OTLP endpoint) - should create Prometheus endpoint",
			config: Config{
				ServiceName:                 "test-service",
				ServiceVersion:              "1.0.0",
				Endpoint:                    "", // No OTLP endpoint
				TracingEnabled:              false,
				MetricsEnabled:              false,
				EnablePrometheusMetricsPath: true, // Only Prometheus enabled
			},
			expectError:             false,
			expectedTracerType:      "trace/noop.TracerProvider",
			expectedMeterType:       "sdk/metric.MeterProvider",
			expectPrometheusHandler: true,
			description:             "Should create no-op tracer and SDK meter with Prometheus handler",
		},
		{
			name: "Scenario 2: OTLP endpoint with both tracing and metrics disabled - should error",
			config: Config{
				ServiceName:                 "test-service",
				ServiceVersion:              "1.0.0",
				Endpoint:                    "localhost:4318", // OTLP endpoint configured
				TracingEnabled:              false,            // Tracing disabled
				MetricsEnabled:              false,            // Metrics disabled
				EnablePrometheusMetricsPath: false,
			},
			expectError:   true,
			errorContains: "OTLP endpoint is configured but both tracing and metrics are disabled",
			description:   "Should error when OTLP endpoint is configured but not used",
		},
		{
			name: "Scenario 3: OTLP endpoint with metrics enabled, tracing disabled - should configure OTLP metrics only",
			config: Config{
				ServiceName:                 "test-service",
				ServiceVersion:              "1.0.0",
				Endpoint:                    "localhost:4318", // OTLP endpoint configured
				TracingEnabled:              false,            // Tracing disabled
				MetricsEnabled:              true,             // Metrics enabled
				EnablePrometheusMetricsPath: false,
			},
			expectError:             false,
			expectedTracerType:      "trace/noop.TracerProvider",
			expectedMeterType:       "sdk/metric.MeterProvider",
			expectPrometheusHandler: false,
			description:             "Should create no-op tracer and SDK meter with OTLP reader",
		},
		{
			name: "Scenario 4: OTLP endpoint with both metrics and tracing enabled - should configure both",
			config: Config{
				ServiceName:                 "test-service",
				ServiceVersion:              "1.0.0",
				Endpoint:                    "localhost:4318", // OTLP endpoint configured
				TracingEnabled:              true,             // Tracing enabled
				MetricsEnabled:              true,             // Metrics enabled
				EnablePrometheusMetricsPath: false,
			},
			expectError:             false,
			expectedTracerType:      "sdk/trace.TracerProvider",
			expectedMeterType:       "sdk/metric.MeterProvider",
			expectPrometheusHandler: false,
			description:             "Should create SDK tracer and SDK meter with OTLP readers",
		},
		{
			name: "Scenario 5: OTLP endpoint with both metrics and tracing enabled - should configure both. With Prometheus enabled - should create metrics path",
			config: Config{
				ServiceName:                 "test-service",
				ServiceVersion:              "1.0.0",
				Endpoint:                    "localhost:4318", // OTLP endpoint configured
				TracingEnabled:              true,             // Tracing enabled
				MetricsEnabled:              true,             // Metrics enabled
				EnablePrometheusMetricsPath: true,
			},
			expectError:             false,
			expectedTracerType:      "sdk/trace.TracerProvider",
			expectedMeterType:       "sdk/metric.MeterProvider",
			expectPrometheusHandler: true,
			description:             "Should create SDK tracer and SDK meter with OTLP readers",
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider, err := NewProvider(ctx, tt.config)

			if tt.expectError {
				require.Error(t, err, tt.description)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
				return
			}

			require.NoError(t, err, tt.description)
			require.NotNil(t, provider)

			// Validate tracer provider type
			tracerProvider := provider.TracerProvider()
			require.NotNil(t, tracerProvider)
			actualTracerType := getProviderTypeName(tracerProvider)
			assert.Equal(t, tt.expectedTracerType, actualTracerType,
				"Tracer provider type should match expected for %s", tt.name)

			// Validate meter provider type
			meterProvider := provider.MeterProvider()
			require.NotNil(t, meterProvider)
			actualMeterType := getProviderTypeName(meterProvider)
			assert.Equal(t, tt.expectedMeterType, actualMeterType,
				"Meter provider type should match expected for %s", tt.name)

			// Validate Prometheus handler presence
			prometheusHandler := provider.PrometheusHandler()
			if tt.expectPrometheusHandler {
				assert.NotNil(t, prometheusHandler,
					"Should have Prometheus handler for %s", tt.name)
			} else {
				assert.Nil(t, prometheusHandler,
					"Should not have Prometheus handler for %s", tt.name)
			}

			// Clean up - ignore connection errors during test shutdown
			err = provider.Shutdown(ctx)
			if err != nil && !isConnectionError(err) {
				t.Errorf("Shutdown error (non-connection): %v", err)
			}
		})
	}
}

// TestUnifiedMeterStrategyConfiguration tests the unified meter strategy configuration
func TestUnifiedMeterStrategyConfiguration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		name        string
		strategy    *providers.UnifiedMeterStrategy
		config      providers.Config
		expectError bool
		description string
	}{
		{
			name: "OTLP only",
			strategy: &providers.UnifiedMeterStrategy{
				EnableOTLP:       true,
				EnablePrometheus: false,
			},
			config: providers.Config{
				ServiceName:  "test",
				OTLPEndpoint: "localhost:4318",
				Insecure:     true,
			},
			expectError: false,
			description: "Should create meter provider with OTLP reader only",
		},
		{
			name: "Prometheus only",
			strategy: &providers.UnifiedMeterStrategy{
				EnableOTLP:       false,
				EnablePrometheus: true,
			},
			config: providers.Config{
				ServiceName: "test",
			},
			expectError: false,
			description: "Should create meter provider with Prometheus reader only",
		},
		{
			name: "Both OTLP and Prometheus",
			strategy: &providers.UnifiedMeterStrategy{
				EnableOTLP:       true,
				EnablePrometheus: true,
			},
			config: providers.Config{
				ServiceName:  "test",
				OTLPEndpoint: "localhost:4318",
				Insecure:     true,
			},
			expectError: false,
			description: "Should create meter provider with both readers",
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create resource for testing
			res, err := createTestResource(ctx, tt.config)
			require.NoError(t, err)

			// Test meter provider creation
			result, err := tt.strategy.CreateMeterProvider(ctx, tt.config, res)

			if tt.expectError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err, tt.description)
			require.NotNil(t, result)
			require.NotNil(t, result.MeterProvider)

			// Validate provider type
			if tt.strategy.EnableOTLP || tt.strategy.EnablePrometheus {
				// Should be SDK meter provider when any reader is enabled
				assert.IsType(t, &sdkmetric.MeterProvider{}, result.MeterProvider,
					"Should create SDK meter provider when readers are configured")
			} else {
				// Should be no-op when nothing is enabled
				assert.IsType(t, noop.MeterProvider{}, result.MeterProvider,
					"Should create no-op meter provider when no readers are configured")
			}

			// Validate Prometheus handler
			if tt.strategy.EnablePrometheus {
				assert.NotNil(t, result.PrometheusHandler,
					"Should have Prometheus handler when Prometheus is enabled")
			} else {
				assert.Nil(t, result.PrometheusHandler,
					"Should not have Prometheus handler when Prometheus is disabled")
			}

			// Test shutdown if available - ignore connection errors during test shutdown
			if result.ShutdownFunc != nil {
				err := result.ShutdownFunc(ctx)
				if err != nil && !isConnectionError(err) {
					t.Errorf("Shutdown error (non-connection): %v", err)
				}
			}
		})
	}
}

// getProviderTypeName returns a readable type name for telemetry providers
func getProviderTypeName(provider interface{}) string {
	t := reflect.TypeOf(provider)
	if t.Kind() == reflect.Ptr {
		return t.Elem().PkgPath()[len("go.opentelemetry.io/otel/"):] + "." + t.Elem().Name()
	}
	return t.PkgPath()[len("go.opentelemetry.io/otel/"):] + "." + t.Name()
}

// createTestResource creates a resource for testing
func createTestResource(ctx context.Context, config providers.Config) (*resource.Resource, error) {
	return resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(config.ServiceName),
			semconv.ServiceVersion("1.0.0"),
		),
	)
}

// isConnectionError checks if the error is a connection-related error that can be ignored in tests
func isConnectionError(err error) bool {
	errorStr := err.Error()
	return strings.Contains(errorStr, "connection refused") ||
		strings.Contains(errorStr, "dial tcp") ||
		strings.Contains(errorStr, "no such host")
}

// TestDefaultConfig tests the default configuration
func TestDefaultConfig(t *testing.T) {
	t.Parallel()

	config := DefaultConfig()

	assert.Equal(t, "toolhive-mcp-proxy", config.ServiceName)
	assert.NotEmpty(t, config.ServiceVersion)
	assert.Equal(t, 0.05, config.SamplingRate)
	assert.NotNil(t, config.Headers)
	assert.Empty(t, config.Headers)
	assert.False(t, config.Insecure)
	assert.False(t, config.EnablePrometheusMetricsPath)
	assert.Empty(t, config.Endpoint)
}

// TestProvider_Middleware tests middleware creation
func TestProvider_Middleware(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	config := Config{
		ServiceName:                 "test-service",
		ServiceVersion:              "1.0.0",
		SamplingRate:                0.1,
		Headers:                     make(map[string]string),
		EnablePrometheusMetricsPath: true,
	}

	provider, err := NewProvider(ctx, config)
	require.NoError(t, err)
	require.NotNil(t, provider)

	middleware := provider.Middleware("github", "stdio")
	assert.NotNil(t, middleware)

	// Test that middleware can wrap a handler
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("test"))
	})

	wrappedHandler := middleware(testHandler)
	assert.NotNil(t, wrappedHandler)
}

// TestProvider_ShutdownTimeout tests provider shutdown with timeout
func TestProvider_ShutdownTimeout(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	config := Config{
		ServiceName:                 "test-service",
		ServiceVersion:              "1.0.0",
		TracingEnabled:              true,
		MetricsEnabled:              true,
		SamplingRate:                0.1,
		Headers:                     make(map[string]string),
		Endpoint:                    "localhost:4318",
		Insecure:                    true,
		EnablePrometheusMetricsPath: true,
	}

	provider, err := NewProvider(ctx, config)
	require.NoError(t, err)
	require.NotNil(t, provider)

	// Test shutdown with timeout
	shutdownCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()

	// Shutdown may fail due to network connection (expected in tests)
	_ = provider.Shutdown(shutdownCtx)
}

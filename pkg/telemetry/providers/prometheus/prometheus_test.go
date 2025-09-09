package prometheus

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
)

func TestNewReader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		config              Config
		wantErr             bool
		errMsg              string
		checkHandler        bool
		checkRuntimeMetrics bool
	}{
		{
			name: "valid config with runtime metrics",
			config: Config{
				EnableMetricsPath:     true,
				IncludeRuntimeMetrics: true,
			},
			wantErr:             false,
			checkHandler:        true,
			checkRuntimeMetrics: true,
		},
		{
			name: "valid config without runtime metrics",
			config: Config{
				EnableMetricsPath:     true,
				IncludeRuntimeMetrics: false,
			},
			wantErr:             false,
			checkHandler:        true,
			checkRuntimeMetrics: false,
		},
		{
			name: "metrics path not enabled",
			config: Config{
				EnableMetricsPath:     false,
				IncludeRuntimeMetrics: true,
			},
			wantErr: true,
			errMsg:  "requires EnableMetricsPath",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reader, handler, err := NewReader(tt.config)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, reader)
				assert.Nil(t, handler)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, reader)

				if tt.checkHandler {
					assert.NotNil(t, handler)
					assert.Implements(t, (*http.Handler)(nil), handler)

					// Test that the handler works
					req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
					rec := httptest.NewRecorder()
					handler.ServeHTTP(rec, req)
					assert.Equal(t, http.StatusOK, rec.Code)

					// Check for runtime metrics if expected
					if tt.checkRuntimeMetrics {
						assert.Contains(t, rec.Body.String(), "go_")
						assert.Contains(t, rec.Body.String(), "process_")
					}
				}
			}
		})
	}
}

func TestNewReader_Integration(t *testing.T) {
	t.Parallel()

	// This test ensures NewReader works correctly with a meter provider
	config := Config{
		EnableMetricsPath:     true,
		IncludeRuntimeMetrics: false,
	}

	reader, handler, err := NewReader(config)
	require.NoError(t, err)
	require.NotNil(t, reader)
	require.NotNil(t, handler)

	// Create a meter provider with the reader
	ctx := context.Background()
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName("test-service"),
			semconv.ServiceVersion("1.0.0"),
		),
	)
	require.NoError(t, err)

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(reader),
	)
	defer meterProvider.Shutdown(ctx)

	// Create a metric
	meter := meterProvider.Meter("test")
	counter, err := meter.Int64Counter("test_reader_counter")
	require.NoError(t, err)

	// Add some values
	counter.Add(ctx, 5)
	counter.Add(ctx, 10)

	// Check that metrics appear in the handler output
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "test_reader_counter")
}

package otlp

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
)

func TestCreateTraceExporter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  Config
		wantErr bool
	}{
		{
			name: "valid config",
			config: Config{
				Endpoint: "localhost:4318",
				Headers:  map[string]string{"Authorization": "Bearer token"},
				Insecure: true,
			},
			wantErr: false,
		},
		{
			name: "config with headers",
			config: Config{
				Endpoint: "localhost:4318",
				Headers: map[string]string{
					"x-api-key": "secret",
					"x-env":     "test",
				},
			},
			wantErr: false,
		},
		{
			name: "secure config",
			config: Config{
				Endpoint: "secure.example.com:4318",
				Insecure: false,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			exporter, err := createTraceExporter(ctx, tt.config)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, exporter)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, exporter)
				// Clean up
				_ = exporter.Shutdown(ctx)
			}
		})
	}
}

func TestNewTracerProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		config     Config
		expectNoOp bool
		wantErr    bool
	}{
		{
			name: "valid config with endpoint",
			config: Config{
				Endpoint:     "localhost:4318",
				SamplingRate: 0.5,
				Headers:      map[string]string{"Authorization": "Bearer token"},
				Insecure:     true,
			},
			expectNoOp: false,
			wantErr:    false,
		},
		{
			name: "no endpoint returns noop",
			config: Config{
				SamplingRate: 0.1,
			},
			expectNoOp: true,
			wantErr:    false,
		},
		{
			name: "config with custom sampling",
			config: Config{
				Endpoint:     "localhost:4318",
				SamplingRate: 1.0, // Always sample
				Insecure:     true,
			},
			expectNoOp: false,
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			res, err := resource.New(ctx,
				resource.WithAttributes(
					semconv.ServiceName("test-service"),
					semconv.ServiceVersion("1.0.0"),
				),
			)
			require.NoError(t, err)

			provider, err := NewTracerProvider(ctx, tt.config, res)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, provider)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, provider)

				// Check if it's a no-op provider
				providerType := fmt.Sprintf("%T", provider)
				if tt.expectNoOp {
					assert.Contains(t, providerType, "noop")
				} else {
					assert.NotContains(t, providerType, "noop")
				}
			}
		})
	}
}

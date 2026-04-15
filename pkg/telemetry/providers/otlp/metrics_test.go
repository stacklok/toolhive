// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package otlp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCreateMetricExporter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  Config
		ctx     func() context.Context
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			config: Config{
				Endpoint: "localhost:4318",
				Headers:  map[string]string{"x-api-key": "secret"},
				Insecure: true,
			},
			ctx:     func() context.Context { return context.Background() },
			wantErr: false,
		},
		{
			name: "config without headers",
			config: Config{
				Endpoint: "localhost:4318",
				Insecure: false,
			},
			ctx:     func() context.Context { return context.Background() },
			wantErr: false,
		},
		{
			name: "endpoint with custom path",
			config: Config{
				Endpoint: "cloud.langfuse.com/api/public/otel",
				Headers:  map[string]string{"Authorization": "Basic abc123"},
				Insecure: false,
			},
			ctx:     func() context.Context { return context.Background() },
			wantErr: false,
		},
		{
			name: "error creating metrics exporter due to invalid CA cert",
			config: Config{
				Endpoint:   "localhost:4318",
				Insecure:   false,
				CACertPath: "/nonexistent/ca.crt",
			},
			ctx:     func() context.Context { return context.Background() },
			wantErr: true,
			errMsg:  "failed to configure TLS for metric exporter",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := tt.ctx()
			exporter, err := createMetricExporter(ctx, tt.config)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, exporter)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, exporter)
				// Clean up
				_ = exporter.Shutdown(ctx)
			}
		})
	}
}

func TestNewMetricReader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  Config
		wantErr bool
		errMsg  string
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
			name: "missing endpoint",
			config: Config{
				Headers: map[string]string{"Authorization": "Bearer token"},
			},
			wantErr: true,
			errMsg:  "OTLP endpoint is required",
		},
		{
			name: "config with custom headers",
			config: Config{
				Endpoint: "otel-collector.local:4318",
				Headers: map[string]string{
					"x-api-key": "secret",
					"x-env":     "production",
				},
				Insecure: false,
			},
			wantErr: false,
		},
		{
			name: "endpoint with custom path",
			config: Config{
				Endpoint: "cloud.langfuse.com/api/public/otel",
				Headers:  map[string]string{"Authorization": "Basic abc123"},
				Insecure: false,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			reader, err := NewMetricReader(ctx, tt.config)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, reader)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, reader)
			}
		})
	}
}

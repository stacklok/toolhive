// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package strategies

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
)

func TestHeaderInjectionStrategy_Name(t *testing.T) {
	t.Parallel()

	strategy := NewHeaderInjectionStrategy()
	assert.Equal(t, "header_injection", strategy.Name())
}

func TestHeaderInjectionStrategy_Authenticate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		strategy      *authtypes.BackendAuthStrategy
		setupCtx      func() context.Context
		expectError   bool
		errorContains string
		checkHeader   func(t *testing.T, req *http.Request)
	}{
		{
			name: "skips authentication for health checks",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "X-API-Key",
					HeaderValue: "secret-key-123",
				},
			},
			setupCtx:    func() context.Context { return health.WithHealthCheckMarker(context.Background()) },
			expectError: false,
			checkHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				assert.Empty(t, req.Header.Get("X-API-Key"), "X-API-Key header should not be set for health checks")
			},
		},
		{
			name: "sets X-API-Key header correctly",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "X-API-Key",
					HeaderValue: "secret-key-123",
				},
			},
			setupCtx:    nil,
			expectError: false,
			checkHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				assert.Equal(t, "secret-key-123", req.Header.Get("X-API-Key"))
			},
		},
		{
			name: "sets Authorization header with API key",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "Authorization",
					HeaderValue: "ApiKey my-secret-key",
				},
			},
			expectError: false,
			checkHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				assert.Equal(t, "ApiKey my-secret-key", req.Header.Get("Authorization"))
			},
		},
		{
			name: "sets custom header name",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "X-Custom-Auth-Token",
					HeaderValue: "custom-token-value",
				},
			},
			expectError: false,
			checkHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				assert.Equal(t, "custom-token-value", req.Header.Get("X-Custom-Auth-Token"))
			},
		},
		{
			name: "handles complex header values",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "X-API-Key",
					HeaderValue: "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.test",
				},
			},
			expectError: false,
			checkHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				assert.Equal(t, "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.test",
					req.Header.Get("X-API-Key"))
			},
		},
		{
			name: "handles header value with special characters",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "X-API-Key",
					HeaderValue: "key-with-!@#$%^&*()-_=+[]{}|;:,.<>?",
				},
			},
			expectError: false,
			checkHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				assert.Equal(t, "key-with-!@#$%^&*()-_=+[]{}|;:,.<>?", req.Header.Get("X-API-Key"))
			},
		},
		{
			name: "returns error when header_name is missing",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "",
					HeaderValue: "my-key",
				},
			},
			expectError:   true,
			errorContains: "header_name required",
		},
		{
			name: "returns error when header_value is missing",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "X-API-Key",
					HeaderValue: "",
				},
			},
			expectError:   true,
			errorContains: "header_value required",
		},
		{
			name:          "returns error when strategy is nil",
			strategy:      nil,
			expectError:   true,
			errorContains: "header_injection configuration required",
		},
		{
			name: "returns error when header_injection config is nil",
			strategy: &authtypes.BackendAuthStrategy{
				Type:            authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: nil,
			},
			expectError:   true,
			errorContains: "header_injection configuration required",
		},
		{
			name: "overwrites existing header value",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "X-API-Key",
					HeaderValue: "new-key",
				},
			},
			expectError: false,
			checkHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				// Verify the new key was set (old-key was already set before Authenticate)
				assert.Equal(t, "new-key", req.Header.Get("X-API-Key"))
			},
		},
		{
			name: "handles very long header values",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "X-API-Key",
					HeaderValue: string(make([]byte, 10000)) + "very-long-key",
				},
			},
			expectError: false,
			checkHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				expected := string(make([]byte, 10000)) + "very-long-key"
				assert.Equal(t, expected, req.Header.Get("X-API-Key"))
			},
		},
		{
			name: "handles case-sensitive header names",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "x-api-key", // lowercase
					HeaderValue: "my-key",
				},
			},
			expectError: false,
			checkHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				// HTTP headers are case-insensitive, but Go normalizes them
				assert.Equal(t, "my-key", req.Header.Get("x-api-key"))
				assert.Equal(t, "my-key", req.Header.Get("X-Api-Key"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			strategy := NewHeaderInjectionStrategy()
			ctx := context.Background()
			if tt.setupCtx != nil {
				ctx = tt.setupCtx()
			}
			req := httptest.NewRequest(http.MethodGet, "/test", nil)

			// Special setup for the "overwrites existing header value" test
			if tt.name == "overwrites existing header value" {
				req.Header.Set("X-API-Key", "old-key")
			}

			err := strategy.Authenticate(ctx, req, tt.strategy)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
				return
			}

			require.NoError(t, err)
			if tt.checkHeader != nil {
				tt.checkHeader(t, req)
			}
		})
	}
}

func TestHeaderInjectionStrategy_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		strategy      *authtypes.BackendAuthStrategy
		expectError   bool
		errorContains string
	}{
		{
			name: "valid strategy with all required fields",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "X-API-Key",
					HeaderValue: "secret-key",
				},
			},
			expectError: false,
		},
		{
			name: "valid with different header name",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "Authorization",
					HeaderValue: "Bearer token",
				},
			},
			expectError: false,
		},
		{
			name: "returns error when header_name is missing",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "",
					HeaderValue: "secret-key",
				},
			},
			expectError:   true,
			errorContains: "header_name required",
		},
		{
			name: "returns error when header_value is missing",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "X-API-Key",
					HeaderValue: "",
				},
			},
			expectError:   true,
			errorContains: "header_value required",
		},
		{
			name: "returns error when strategy is nil",
			strategy: &authtypes.BackendAuthStrategy{
				Type:            authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: nil,
			},
			expectError:   true,
			errorContains: "header_injection configuration required",
		},
		{
			name:          "returns error when strategy parameter is nil",
			strategy:      nil,
			expectError:   true,
			errorContains: "header_injection configuration required",
		},
		{
			name: "returns error for whitespace in header_name",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "X-Custom Header",
					HeaderValue: "key",
				},
			},
			expectError:   true,
			errorContains: "invalid header_name",
		},
		{
			name: "accepts unicode in header_value",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "X-API-Key",
					HeaderValue: "key-with-unicode-日本語",
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			strategy := NewHeaderInjectionStrategy()
			err := strategy.Validate(tt.strategy)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

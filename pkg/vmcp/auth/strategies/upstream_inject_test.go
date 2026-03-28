// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package strategies

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types" // BackendAuthStrategy, ErrUpstreamTokenNotFound
	"github.com/stacklok/toolhive/pkg/vmcp/health"
)

func TestUpstreamInjectStrategy_Name(t *testing.T) {
	t.Parallel()

	strategy := NewUpstreamInjectStrategy()
	assert.Equal(t, "upstream_inject", strategy.Name())
}

func TestUpstreamInjectStrategy_Authenticate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		setupCtx      func() context.Context
		strategy      *authtypes.BackendAuthStrategy
		expectError   bool
		errorContains string
		checkSentinel bool
		checkHeader   func(t *testing.T, req *http.Request)
	}{
		{
			name: "valid token injection",
			setupCtx: func() context.Context {
				return createContextWithUpstreamTokens("user1", "incoming-token", map[string]string{
					"github": "gh-token-123",
				})
			},
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeUpstreamInject,
				UpstreamInject: &authtypes.UpstreamInjectConfig{
					ProviderName: "github",
				},
			},
			expectError: false,
			checkHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				assert.Equal(t, "Bearer gh-token-123", req.Header.Get("Authorization"))
			},
		},
		{
			name:     "missing identity in context",
			setupCtx: func() context.Context { return context.Background() },
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeUpstreamInject,
				UpstreamInject: &authtypes.UpstreamInjectConfig{
					ProviderName: "github",
				},
			},
			expectError:   true,
			errorContains: "no identity",
		},
		{
			name: "nil UpstreamTokens map",
			setupCtx: func() context.Context {
				return createContextWithUpstreamTokens("user1", "incoming-token", nil)
			},
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeUpstreamInject,
				UpstreamInject: &authtypes.UpstreamInjectConfig{
					ProviderName: "github",
				},
			},
			expectError:   true,
			errorContains: "github",
			checkSentinel: true,
		},
		{
			name: "provider not in map",
			setupCtx: func() context.Context {
				return createContextWithUpstreamTokens("user1", "incoming-token", map[string]string{
					"other": "tok",
				})
			},
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeUpstreamInject,
				UpstreamInject: &authtypes.UpstreamInjectConfig{
					ProviderName: "github",
				},
			},
			expectError:   true,
			errorContains: "github",
			checkSentinel: true,
		},
		{
			name: "empty token value",
			setupCtx: func() context.Context {
				return createContextWithUpstreamTokens("user1", "incoming-token", map[string]string{
					"github": "",
				})
			},
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeUpstreamInject,
				UpstreamInject: &authtypes.UpstreamInjectConfig{
					ProviderName: "github",
				},
			},
			expectError:   true,
			errorContains: "github",
			checkSentinel: true,
		},
		{
			name:     "health check bypass",
			setupCtx: func() context.Context { return health.WithHealthCheckMarker(context.Background()) },
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeUpstreamInject,
				UpstreamInject: &authtypes.UpstreamInjectConfig{
					ProviderName: "github",
				},
			},
			expectError: false,
			checkHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				assert.Empty(t, req.Header.Get("Authorization"), "Authorization header should not be set for health checks")
			},
		},
		{
			name: "nil strategy",
			setupCtx: func() context.Context {
				return createContextWithUpstreamTokens("user1", "incoming-token", map[string]string{
					"github": "gh-token-123",
				})
			},
			strategy:      nil,
			expectError:   true,
			errorContains: "upstream_inject configuration required",
		},
		{
			name: "nil UpstreamInject config",
			setupCtx: func() context.Context {
				return createContextWithUpstreamTokens("user1", "incoming-token", map[string]string{
					"github": "gh-token-123",
				})
			},
			strategy: &authtypes.BackendAuthStrategy{
				Type:           authtypes.StrategyTypeUpstreamInject,
				UpstreamInject: nil,
			},
			expectError:   true,
			errorContains: "upstream_inject configuration required",
		},
		{
			name: "empty ProviderName",
			setupCtx: func() context.Context {
				return createContextWithUpstreamTokens("user1", "incoming-token", map[string]string{
					"github": "gh-token-123",
				})
			},
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeUpstreamInject,
				UpstreamInject: &authtypes.UpstreamInjectConfig{
					ProviderName: "",
				},
			},
			expectError:   true,
			checkSentinel: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			strategy := NewUpstreamInjectStrategy()
			ctx := tt.setupCtx()
			req := httptest.NewRequest(http.MethodGet, "/test", nil)

			err := strategy.Authenticate(ctx, req, tt.strategy)

			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
				if tt.checkSentinel {
					assert.True(t, errors.Is(err, authtypes.ErrUpstreamTokenNotFound),
						"expected error to wrap ErrUpstreamTokenNotFound, got: %v", err)
				}
				return
			}

			require.NoError(t, err)
			if tt.checkHeader != nil {
				tt.checkHeader(t, req)
			}
		})
	}
}

func TestUpstreamInjectStrategy_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		strategy      *authtypes.BackendAuthStrategy
		expectError   bool
		errorContains string
	}{
		{
			name: "valid config with ProviderName",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeUpstreamInject,
				UpstreamInject: &authtypes.UpstreamInjectConfig{
					ProviderName: "github",
				},
			},
			expectError: false,
		},
		{
			name:          "nil strategy",
			strategy:      nil,
			expectError:   true,
			errorContains: "upstream_inject configuration required",
		},
		{
			name: "nil UpstreamInject config",
			strategy: &authtypes.BackendAuthStrategy{
				Type:           authtypes.StrategyTypeUpstreamInject,
				UpstreamInject: nil,
			},
			expectError:   true,
			errorContains: "upstream_inject configuration required",
		},
		{
			name: "empty ProviderName",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeUpstreamInject,
				UpstreamInject: &authtypes.UpstreamInjectConfig{
					ProviderName: "",
				},
			},
			expectError:   true,
			errorContains: "provider_name required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			strategy := NewUpstreamInjectStrategy()
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

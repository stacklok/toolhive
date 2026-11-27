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

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	healthcontext "github.com/stacklok/toolhive/pkg/vmcp/health/context"
)

func TestUpstreamInjectStrategy_Name(t *testing.T) {
	t.Parallel()

	strategy := NewUpstreamInjectStrategy()
	assert.Equal(t, "upstream_inject", strategy.Name())
}

func TestUpstreamInjectStrategy_NilOption(t *testing.T) {
	t.Parallel()

	ctx := createContextWithUpstreamTokens("user1", "incoming-token", map[string]string{"github": "gh-token"})
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	config := &authtypes.BackendAuthStrategy{
		Type:           authtypes.StrategyTypeUpstreamInject,
		UpstreamInject: &authtypes.UpstreamInjectConfig{ProviderName: "github"},
	}

	err := NewUpstreamInjectStrategy(nil).Authenticate(ctx, req, config)
	require.NoError(t, err)
	assert.Equal(t, "Bearer gh-token", req.Header.Get("Authorization"))
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
			setupCtx: func() context.Context { return healthcontext.WithHealthCheckMarker(context.Background()) },
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

func TestUpstreamInjectStrategy_AuthenticateDelegated(t *testing.T) {
	t.Parallel()

	const userID, provider, accessToken = "platform-user", "github", "delegated-access-token"
	identity := &auth.Identity{
		PrincipalInfo:  auth.PrincipalInfo{Subject: "different-subject", PlatformUserID: userID},
		Token:          "incoming-token",
		AdmissionRoute: auth.AdmissionRouteDelegated,
		UpstreamTokens: map[string]string{provider: "identity-token"},
	}
	config := &authtypes.BackendAuthStrategy{
		UpstreamInject: &authtypes.UpstreamInjectConfig{ProviderName: provider},
	}

	for _, tt := range []struct {
		name        string
		useReader   bool
		typedNil    bool
		readerErr   error
		healthCheck bool
		wantHeader  string
		wantErr     error
	}{
		{name: "delegated credential wins over identity token", useReader: true, wantHeader: "Bearer " + accessToken},
		{name: "no reader fails closed", wantErr: authtypes.ErrDelegatedCredentialUnavailable},
		{name: "typed-nil reader fails closed", typedNil: true, wantErr: authtypes.ErrDelegatedCredentialUnavailable},
		{
			name: "needs authorization", useReader: true, readerErr: upstreamtoken.ErrNeedsAuthorization,
			wantErr: upstreamtoken.ErrNeedsAuthorization,
		},
		{
			name: "reader timeout with live caller", useReader: true, readerErr: context.DeadlineExceeded,
			wantErr: authtypes.ErrDelegatedCredentialUnavailable,
		},
		{name: "health check bypasses delegated read", useReader: true, healthCheck: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := auth.WithIdentity(t.Context(), identity)
			if tt.healthCheck {
				ctx = healthcontext.WithHealthCheckMarker(ctx)
			}
			var opts []Option
			if tt.typedNil {
				opts = append(opts, WithUserCredentialReader((*upstreamtoken.InProcessService)(nil)))
			}
			if tt.useReader {
				reader := delegatedReaderFunc(func(_ context.Context, gotUserID, gotProvider string) (*upstreamtoken.UpstreamCredential, error) {
					assert.Equal(t, userID, gotUserID)
					assert.Equal(t, provider, gotProvider)
					if tt.readerErr != nil {
						return nil, tt.readerErr
					}
					return &upstreamtoken.UpstreamCredential{AccessToken: accessToken}, nil
				})
				if tt.healthCheck {
					reader = unexpectedCredentialReader(t)
				}
				opts = append(opts, WithUserCredentialReader(reader))
			}
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			err := NewUpstreamInjectStrategy(opts...).Authenticate(ctx, req, config)
			switch tt.wantErr {
			case nil:
				require.NoError(t, err)
			case upstreamtoken.ErrNeedsAuthorization:
				var authRequired *authtypes.DelegatedAuthRequiredError
				require.ErrorAs(t, err, &authRequired)
				assert.Equal(t, provider, authRequired.Provider)
				assert.ErrorIs(t, err, tt.wantErr)
			default:
				require.Same(t, tt.wantErr, err)
			}
			assert.Equal(t, tt.wantHeader, req.Header.Get("Authorization"))
		})
	}
}

func TestUpstreamInjectStrategy_AuthenticateNonDelegated(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		route auth.AdmissionRoute
	}{
		{name: "interactive", route: auth.AdmissionRouteInteractive},
		{name: "unspecified", route: auth.AdmissionRouteUnspecified},
		{name: "unknown future route", route: auth.AdmissionRoute("future")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := auth.WithIdentity(t.Context(), &auth.Identity{
				PrincipalInfo:  auth.PrincipalInfo{Subject: "subject", PlatformUserID: "platform-user"},
				Token:          "incoming-token",
				AdmissionRoute: tt.route,
				UpstreamTokens: map[string]string{"github": "identity-token"},
			})
			config := &authtypes.BackendAuthStrategy{
				UpstreamInject: &authtypes.UpstreamInjectConfig{ProviderName: "github"},
			}
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			s := NewUpstreamInjectStrategy(WithUserCredentialReader(unexpectedCredentialReader(t)))
			require.NoError(t, s.Authenticate(ctx, req, config))
			assert.Equal(t, "Bearer identity-token", req.Header.Get("Authorization"))
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

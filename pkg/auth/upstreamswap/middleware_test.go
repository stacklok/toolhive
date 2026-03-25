// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstreamswap

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/transport/types/mocks"
)

// requestWithIdentity creates an HTTP request with a "github" upstream token in context.
func requestWithIdentity(token string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		PrincipalInfo: auth.PrincipalInfo{
			Subject: "user123",
		},
		UpstreamTokens: map[string]string{
			"github": token,
		},
	}
	ctx := auth.WithIdentity(req.Context(), identity)
	return req.WithContext(ctx)
}

func TestValidateConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     *Config
		wantErr bool
		errMsg  string
	}{
		{
			name:    "empty config missing provider name",
			cfg:     &Config{},
			wantErr: true,
			errMsg:  "provider_name is required",
		},
		{
			name: "valid replace strategy",
			cfg: &Config{
				HeaderStrategy: HeaderStrategyReplace,
				ProviderName:   "default",
			},
			wantErr: false,
		},
		{
			name: "valid custom strategy with header name",
			cfg: &Config{
				HeaderStrategy:   HeaderStrategyCustom,
				CustomHeaderName: "X-Upstream-Token",
				ProviderName:     "default",
			},
			wantErr: false,
		},
		{
			name: "invalid header strategy",
			cfg: &Config{
				HeaderStrategy: "invalid",
				ProviderName:   "default",
			},
			wantErr: true,
			errMsg:  "invalid header_strategy",
		},
		{
			name: "custom strategy without header name",
			cfg: &Config{
				HeaderStrategy: HeaderStrategyCustom,
				ProviderName:   "default",
			},
			wantErr: true,
			errMsg:  "custom_header_name must be specified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateConfig(tt.cfg)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestMiddleware_NoIdentity(t *testing.T) {
	t.Parallel()

	cfg := &Config{ProviderName: "github"}
	middleware := createMiddlewareFunc(cfg)

	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		nextCalled = true
		assert.Empty(t, r.Header.Get("Authorization"))
	})

	handler := middleware(nextHandler)
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.True(t, nextCalled, "next handler should be called")
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestMiddleware_NilUpstreamTokens(t *testing.T) {
	t.Parallel()

	cfg := &Config{ProviderName: "github"}
	middleware := createMiddlewareFunc(cfg)

	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("next handler should NOT be called when upstream tokens are nil")
	})

	handler := middleware(nextHandler)

	// Identity exists but UpstreamTokens is nil (not populated by auth middleware)
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		PrincipalInfo: auth.PrincipalInfo{Subject: "user123"},
	}
	ctx := auth.WithIdentity(req.Context(), identity)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Contains(t, rr.Header().Get("WWW-Authenticate"), `error="invalid_token"`)
}

func TestMiddleware_ProviderMissing_Returns401(t *testing.T) {
	t.Parallel()

	cfg := &Config{ProviderName: "atlassian"}
	middleware := createMiddlewareFunc(cfg)

	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("next handler should NOT be called when provider is missing")
	})

	handler := middleware(nextHandler)
	req := requestWithIdentity("gh-token") // has github but not atlassian

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestMiddleware_EmptyToken_Returns401(t *testing.T) {
	t.Parallel()

	cfg := &Config{ProviderName: "github"}
	middleware := createMiddlewareFunc(cfg)

	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("next handler should NOT be called when token is empty")
	})

	handler := middleware(nextHandler)
	req := requestWithIdentity("") // empty token

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestMiddleware_SuccessfulSwap_ReplaceStrategy(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		HeaderStrategy: HeaderStrategyReplace,
		ProviderName:   "github",
	}
	middleware := createMiddlewareFunc(cfg)

	var capturedAuthHeader string
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		capturedAuthHeader = r.Header.Get("Authorization")
	})

	handler := middleware(nextHandler)
	req := requestWithIdentity("upstream-access-token")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, "Bearer upstream-access-token", capturedAuthHeader)
}

func TestMiddleware_SuccessfulSwap_DefaultStrategy(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		ProviderName: "github",
		// HeaderStrategy intentionally empty — defaults to replace
	}
	middleware := createMiddlewareFunc(cfg)

	var capturedAuthHeader string
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		capturedAuthHeader = r.Header.Get("Authorization")
	})

	handler := middleware(nextHandler)
	req := requestWithIdentity("default-strategy-token")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "Bearer default-strategy-token", capturedAuthHeader)
}

func TestMiddleware_SuccessfulSwap_CustomHeader(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		HeaderStrategy:   HeaderStrategyCustom,
		CustomHeaderName: "X-Upstream-Token",
		ProviderName:     "github",
	}
	middleware := createMiddlewareFunc(cfg)

	var capturedCustomHeader string
	var capturedAuthHeader string
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		capturedCustomHeader = r.Header.Get("X-Upstream-Token")
		capturedAuthHeader = r.Header.Get("Authorization")
	})

	handler := middleware(nextHandler)
	req := requestWithIdentity("upstream-access-token")
	req.Header.Set("Authorization", "Bearer original-token")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, "Bearer upstream-access-token", capturedCustomHeader)
	assert.Equal(t, "Bearer original-token", capturedAuthHeader)
}

func TestMiddleware_Close(t *testing.T) {
	t.Parallel()

	m := &Middleware{}
	err := m.Close()
	assert.NoError(t, err)
}

func TestMiddleware_Handler(t *testing.T) {
	t.Parallel()

	handler := func(next http.Handler) http.Handler {
		return next
	}
	m := &Middleware{middleware: handler}
	assert.NotNil(t, m.Handler())
}

func TestCreateInjectors(t *testing.T) {
	t.Parallel()

	t.Run("replace injector", func(t *testing.T) {
		t.Parallel()
		injector := createReplaceInjector()
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		injector(req, "test-token")
		assert.Equal(t, "Bearer test-token", req.Header.Get("Authorization"))
	})

	t.Run("custom injector", func(t *testing.T) {
		t.Parallel()
		injector := createCustomInjector("X-Custom-Header")
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer original")
		injector(req, "test-token")
		assert.Equal(t, "Bearer test-token", req.Header.Get("X-Custom-Header"))
		assert.Equal(t, "Bearer original", req.Header.Get("Authorization"))
	})
}

func TestMiddlewareWithContext(t *testing.T) {
	t.Parallel()

	cfg := &Config{ProviderName: "github"}
	middleware := createMiddlewareFunc(cfg)

	var receivedCtx context.Context
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		receivedCtx = r.Context()
	})

	handler := middleware(nextHandler)
	req := requestWithIdentity("ctx-test-token")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	identityFromCtx, ok := auth.IdentityFromContext(receivedCtx)
	assert.True(t, ok)
	assert.Equal(t, "user123", identityFromCtx.Subject)
}

// TestCreateMiddleware tests the factory function.
func TestCreateMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		params              MiddlewareParams
		expectError         bool
		errorMsg            string
		expectAddMiddleware bool
	}{
		{
			name: "valid config creates middleware",
			params: MiddlewareParams{
				Config: &Config{
					HeaderStrategy: HeaderStrategyReplace,
					ProviderName:   "default",
				},
			},
			expectError:         false,
			expectAddMiddleware: true,
		},
		{
			name:                "nil config missing provider name",
			params:              MiddlewareParams{Config: nil},
			expectError:         true,
			errorMsg:            "invalid upstream swap configuration",
			expectAddMiddleware: false,
		},
		{
			name:                "empty params missing provider name",
			params:              MiddlewareParams{},
			expectError:         true,
			errorMsg:            "invalid upstream swap configuration",
			expectAddMiddleware: false,
		},
		{
			name: "custom header strategy with header name",
			params: MiddlewareParams{
				Config: &Config{
					HeaderStrategy:   HeaderStrategyCustom,
					CustomHeaderName: "X-Upstream-Token",
					ProviderName:     "default",
				},
			},
			expectError:         false,
			expectAddMiddleware: true,
		},
		{
			name: "invalid config fails validation - custom strategy without header",
			params: MiddlewareParams{
				Config: &Config{
					HeaderStrategy: HeaderStrategyCustom,
					ProviderName:   "default",
					// Missing CustomHeaderName
				},
			},
			expectError:         true,
			errorMsg:            "invalid upstream swap configuration",
			expectAddMiddleware: false,
		},
		{
			name: "invalid header strategy fails validation",
			params: MiddlewareParams{
				Config: &Config{
					HeaderStrategy: "invalid_strategy",
					ProviderName:   "default",
				},
			},
			expectError:         true,
			errorMsg:            "invalid upstream swap configuration",
			expectAddMiddleware: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRunner := mocks.NewMockMiddlewareRunner(ctrl)

			if tt.expectAddMiddleware {
				mockRunner.EXPECT().AddMiddleware(gomock.Any(), gomock.Any()).Do(func(_ string, mw types.Middleware) {
					_, ok := mw.(*Middleware)
					assert.True(t, ok, "Expected middleware to be of type *upstreamswap.Middleware")
				})
			}

			paramsJSON, err := json.Marshal(tt.params)
			require.NoError(t, err)

			config := &types.MiddlewareConfig{
				Type:       MiddlewareType,
				Parameters: paramsJSON,
			}

			err = CreateMiddleware(config, mockRunner)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestCreateMiddleware_InvalidJSON tests error handling for malformed parameters.
func TestCreateMiddleware_InvalidJSON(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRunner := mocks.NewMockMiddlewareRunner(ctrl)

	config := &types.MiddlewareConfig{
		Type:       MiddlewareType,
		Parameters: []byte(`{invalid json}`),
	}

	err := CreateMiddleware(config, mockRunner)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to unmarshal upstream swap middleware parameters")
}

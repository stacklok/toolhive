// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstreamswap

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	storagemocks "github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/transport/types/mocks"
)

// serviceGetterFromMocks creates a ServiceGetter that wraps mock storage and refresher
// in an InProcessService. This is the standard pattern for middleware tests.
func serviceGetterFromMocks(
	stor storage.UpstreamTokenStorage,
	refresher storage.UpstreamTokenRefresher,
) ServiceGetter {
	svc := upstreamtoken.NewInProcessService(stor, refresher)
	return func() upstreamtoken.Service { return svc }
}

// nilServiceGetter returns a ServiceGetter that always returns nil (service unavailable).
func nilServiceGetter() ServiceGetter {
	return func() upstreamtoken.Service { return nil }
}

// requestWithIdentity creates an HTTP request with the given identity in context.
func requestWithIdentity(tsid string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		Subject: "user123",
		Claims: map[string]any{
			"sub":                                "user123",
			upstreamtoken.TokenSessionIDClaimKey: tsid,
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
			name:    "empty config (defaults)",
			cfg:     &Config{},
			wantErr: false,
		},
		{
			name: "valid replace strategy",
			cfg: &Config{
				HeaderStrategy: HeaderStrategyReplace,
			},
			wantErr: false,
		},
		{
			name: "valid custom strategy with header name",
			cfg: &Config{
				HeaderStrategy:   HeaderStrategyCustom,
				CustomHeaderName: "X-Upstream-Token",
			},
			wantErr: false,
		},
		{
			name: "invalid header strategy",
			cfg: &Config{
				HeaderStrategy: "invalid",
			},
			wantErr: true,
			errMsg:  "invalid header_strategy",
		},
		{
			name: "custom strategy without header name",
			cfg: &Config{
				HeaderStrategy: HeaderStrategyCustom,
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

	cfg := &Config{}
	middleware := createMiddlewareFunc(cfg, nilServiceGetter())

	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		nextCalled = true
		// Verify Authorization header was NOT modified
		assert.Empty(t, r.Header.Get("Authorization"))
	})

	handler := middleware(nextHandler)
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.True(t, nextCalled, "next handler should be called")
}

func TestMiddleware_NoTsidClaim(t *testing.T) {
	t.Parallel()

	cfg := &Config{}
	middleware := createMiddlewareFunc(cfg, nilServiceGetter())

	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		nextCalled = true
	})

	handler := middleware(nextHandler)

	// Create request with identity but no tsid claim
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		Subject: "user123",
		Claims: map[string]any{
			"sub": "user123",
			// No tsid claim
		},
	}
	ctx := auth.WithIdentity(req.Context(), identity)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.True(t, nextCalled, "next handler should be called")
}

func TestMiddleware_ServiceUnavailable_FailsClosed(t *testing.T) {
	t.Parallel()

	cfg := &Config{}
	middleware := createMiddlewareFunc(cfg, nilServiceGetter())

	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		nextCalled = true
	})

	handler := middleware(nextHandler)
	req := requestWithIdentity("session-123")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.False(t, nextCalled, "next handler should NOT be called when service is unavailable")
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestMiddleware_ClientAttributableErrors_Returns401(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		setupStorage func(*storagemocks.MockUpstreamTokenStorage)
	}{
		{
			name: "not found",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetUpstreamTokens(gomock.Any(), "session-123").
					Return(nil, storage.ErrNotFound)
			},
		},
		{
			name: "expired with nil tokens",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetUpstreamTokens(gomock.Any(), "session-123").
					Return(nil, storage.ErrExpired)
			},
		},
		{
			name: "invalid binding",
			setupStorage: func(s *storagemocks.MockUpstreamTokenStorage) {
				s.EXPECT().GetUpstreamTokens(gomock.Any(), "session-123").
					Return(nil, storage.ErrInvalidBinding)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)
			tt.setupStorage(mockStorage)

			cfg := &Config{}
			middleware := createMiddlewareFunc(cfg, serviceGetterFromMocks(mockStorage, nil))

			var nextCalled bool
			nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
				nextCalled = true
			})

			handler := middleware(nextHandler)
			req := requestWithIdentity("session-123")

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			assert.False(t, nextCalled, "next handler should NOT be called")
			assert.Equal(t, http.StatusUnauthorized, rr.Code)
			assert.Contains(t, rr.Header().Get("WWW-Authenticate"), `error="invalid_token"`)
		})
	}
}

func TestMiddleware_StorageError(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)
	mockStorage.EXPECT().
		GetUpstreamTokens(gomock.Any(), "session-123").
		Return(nil, errors.New("database error"))

	cfg := &Config{}
	middleware := createMiddlewareFunc(cfg, serviceGetterFromMocks(mockStorage, nil))

	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		nextCalled = true
	})

	handler := middleware(nextHandler)
	req := requestWithIdentity("session-123")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.False(t, nextCalled, "next handler should NOT be called on storage error")
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestMiddleware_SuccessfulSwap_AccessToken(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tokens := &storage.UpstreamTokens{
		AccessToken: "upstream-access-token",
		IDToken:     "upstream-id-token",
		ExpiresAt:   time.Now().Add(1 * time.Hour),
	}

	mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)
	mockStorage.EXPECT().
		GetUpstreamTokens(gomock.Any(), "session-123").
		Return(tokens, nil)

	cfg := &Config{
		HeaderStrategy: HeaderStrategyReplace,
	}
	middleware := createMiddlewareFunc(cfg, serviceGetterFromMocks(mockStorage, nil))

	var capturedAuthHeader string
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		capturedAuthHeader = r.Header.Get("Authorization")
	})

	handler := middleware(nextHandler)
	req := requestWithIdentity("session-123")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, "Bearer upstream-access-token", capturedAuthHeader)
}

func TestMiddleware_CustomHeader(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tokens := &storage.UpstreamTokens{
		AccessToken: "upstream-access-token",
		ExpiresAt:   time.Now().Add(1 * time.Hour),
	}

	mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)
	mockStorage.EXPECT().
		GetUpstreamTokens(gomock.Any(), "session-123").
		Return(tokens, nil)

	cfg := &Config{
		HeaderStrategy:   HeaderStrategyCustom,
		CustomHeaderName: "X-Upstream-Token",
	}
	middleware := createMiddlewareFunc(cfg, serviceGetterFromMocks(mockStorage, nil))

	var capturedCustomHeader string
	var capturedAuthHeader string
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		capturedCustomHeader = r.Header.Get("X-Upstream-Token")
		capturedAuthHeader = r.Header.Get("Authorization")
	})

	handler := middleware(nextHandler)
	req := requestWithIdentity("session-123")
	req.Header.Set("Authorization", "Bearer original-token")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, "Bearer upstream-access-token", capturedCustomHeader)
	// Original Authorization header should remain unchanged
	assert.Equal(t, "Bearer original-token", capturedAuthHeader)
}

func TestMiddleware_ExpiredTokens_Returns401(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Token is expired and has no refresh token
	tokens := &storage.UpstreamTokens{
		AccessToken: "expired-upstream-token",
		ExpiresAt:   time.Now().Add(-1 * time.Hour), // Expired 1 hour ago
	}

	mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)
	mockStorage.EXPECT().
		GetUpstreamTokens(gomock.Any(), "session-123").
		Return(tokens, nil)

	cfg := &Config{}
	middleware := createMiddlewareFunc(cfg, serviceGetterFromMocks(mockStorage, nil))

	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		nextCalled = true
	})

	handler := middleware(nextHandler)
	req := requestWithIdentity("session-123")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.False(t, nextCalled, "next handler should NOT be called for expired tokens")
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Contains(t, rr.Header().Get("WWW-Authenticate"), `error="invalid_token"`)
}

func TestMiddleware_EmptySelectedToken_FailsClosed(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Tokens exist but the access token is empty
	tokens := &storage.UpstreamTokens{
		AccessToken: "", // Empty access token
		IDToken:     "upstream-id-token",
		ExpiresAt:   time.Now().Add(1 * time.Hour),
	}

	mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)
	mockStorage.EXPECT().
		GetUpstreamTokens(gomock.Any(), "session-123").
		Return(tokens, nil)

	cfg := &Config{}
	middleware := createMiddlewareFunc(cfg, serviceGetterFromMocks(mockStorage, nil))

	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		nextCalled = true
	})

	handler := middleware(nextHandler)
	req := requestWithIdentity("session-123")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.False(t, nextCalled, "next handler should NOT be called when access token is empty")
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
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

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tokens := &storage.UpstreamTokens{
		AccessToken: "ctx-test-token",
		ExpiresAt:   time.Now().Add(1 * time.Hour),
	}

	mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)
	mockStorage.EXPECT().
		GetUpstreamTokens(gomock.Any(), "session-ctx").
		Return(tokens, nil)

	cfg := &Config{}
	middleware := createMiddlewareFunc(cfg, serviceGetterFromMocks(mockStorage, nil))

	// Test that context is properly passed through
	var receivedCtx context.Context
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		receivedCtx = r.Context()
	})

	handler := middleware(nextHandler)
	req := requestWithIdentity("session-ctx")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// Verify identity is still in context
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
				},
			},
			expectError:         false,
			expectAddMiddleware: true,
		},
		{
			name:                "nil config uses defaults",
			params:              MiddlewareParams{Config: nil},
			expectError:         false,
			expectAddMiddleware: true,
		},
		{
			name:                "empty params uses defaults",
			params:              MiddlewareParams{},
			expectError:         false,
			expectAddMiddleware: true,
		},
		{
			name: "custom header strategy with header name",
			params: MiddlewareParams{
				Config: &Config{
					HeaderStrategy:   HeaderStrategyCustom,
					CustomHeaderName: "X-Upstream-Token",
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

			// Service getter is only called if validation passes (expectAddMiddleware)
			// because validation happens before GetUpstreamTokenService is called
			if tt.expectAddMiddleware {
				mockRunner.EXPECT().GetUpstreamTokenService().Return(func() upstreamtoken.Service {
					return nil // Service availability is checked at request time
				})
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

// TestMiddleware_TsidClaimWrongType tests behavior when tsid claim exists but is wrong type.
func TestMiddleware_TsidClaimWrongType(t *testing.T) {
	t.Parallel()

	cfg := &Config{}
	middleware := createMiddlewareFunc(cfg, nilServiceGetter())

	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		nextCalled = true
	})

	handler := middleware(nextHandler)

	// Create request with identity but tsid claim is wrong type (int instead of string)
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		Subject: "user123",
		Claims: map[string]any{
			"sub":                                "user123",
			upstreamtoken.TokenSessionIDClaimKey: 12345, // Wrong type: int instead of string
		},
	}
	ctx := auth.WithIdentity(req.Context(), identity)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.True(t, nextCalled, "next handler should be called when tsid is wrong type")
}

func TestMiddleware_ExpiredTokens_RefreshSuccess(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	expiredTokens := &storage.UpstreamTokens{
		AccessToken:  "expired-access-token",
		RefreshToken: "my-refresh-token",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
	}

	refreshedTokens := &storage.UpstreamTokens{
		AccessToken:  "new-access-token",
		RefreshToken: "new-refresh-token",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	}

	mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)
	mockStorage.EXPECT().
		GetUpstreamTokens(gomock.Any(), "session-123").
		Return(expiredTokens, storage.ErrExpired)

	mockRefresher := storagemocks.NewMockUpstreamTokenRefresher(ctrl)
	mockRefresher.EXPECT().
		RefreshAndStore(gomock.Any(), "session-123", expiredTokens).
		Return(refreshedTokens, nil)

	cfg := &Config{}
	middleware := createMiddlewareFunc(cfg, serviceGetterFromMocks(mockStorage, mockRefresher))

	var nextCalled bool
	var capturedAuthHeader string
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		nextCalled = true
		capturedAuthHeader = r.Header.Get("Authorization")
	})

	handler := middleware(nextHandler)
	req := requestWithIdentity("session-123")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.True(t, nextCalled, "next handler should be called after successful refresh")
	assert.Equal(t, "Bearer new-access-token", capturedAuthHeader)
}

func TestMiddleware_ExpiredTokens_RefreshFails(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	expiredTokens := &storage.UpstreamTokens{
		AccessToken:  "expired-access-token",
		RefreshToken: "my-refresh-token",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
	}

	mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)
	mockStorage.EXPECT().
		GetUpstreamTokens(gomock.Any(), "session-123").
		Return(expiredTokens, storage.ErrExpired)

	mockRefresher := storagemocks.NewMockUpstreamTokenRefresher(ctrl)
	mockRefresher.EXPECT().
		RefreshAndStore(gomock.Any(), "session-123", expiredTokens).
		Return(nil, errors.New("refresh failed"))

	cfg := &Config{}
	middleware := createMiddlewareFunc(cfg, serviceGetterFromMocks(mockStorage, mockRefresher))

	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		nextCalled = true
	})

	handler := middleware(nextHandler)
	req := requestWithIdentity("session-123")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.False(t, nextCalled, "next handler should NOT be called when refresh fails")
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Contains(t, rr.Header().Get("WWW-Authenticate"), `error="invalid_token"`)
}

func TestMiddleware_ExpiredTokens_NoRefreshToken(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	expiredTokens := &storage.UpstreamTokens{
		AccessToken:  "expired-access-token",
		RefreshToken: "", // No refresh token
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
	}

	mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)
	mockStorage.EXPECT().
		GetUpstreamTokens(gomock.Any(), "session-123").
		Return(expiredTokens, storage.ErrExpired)

	cfg := &Config{}
	middleware := createMiddlewareFunc(cfg, serviceGetterFromMocks(mockStorage, nil))

	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		nextCalled = true
	})

	handler := middleware(nextHandler)
	req := requestWithIdentity("session-123")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.False(t, nextCalled, "next handler should NOT be called when no refresh token available")
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Contains(t, rr.Header().Get("WWW-Authenticate"), `error="invalid_token"`)
}

func TestMiddleware_DefenseInDepth_ExpiredButNoError_RefreshSuccess(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Storage returns tokens with ExpiresAt in the past but NO error
	expiredTokens := &storage.UpstreamTokens{
		AccessToken:  "expired-access-token",
		RefreshToken: "my-refresh-token",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
	}

	refreshedTokens := &storage.UpstreamTokens{
		AccessToken:  "refreshed-access-token",
		RefreshToken: "refreshed-refresh-token",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	}

	mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)
	mockStorage.EXPECT().
		GetUpstreamTokens(gomock.Any(), "session-123").
		Return(expiredTokens, nil) // No error, but token is expired

	mockRefresher := storagemocks.NewMockUpstreamTokenRefresher(ctrl)
	mockRefresher.EXPECT().
		RefreshAndStore(gomock.Any(), "session-123", expiredTokens).
		Return(refreshedTokens, nil)

	cfg := &Config{}
	middleware := createMiddlewareFunc(cfg, serviceGetterFromMocks(mockStorage, mockRefresher))

	var nextCalled bool
	var capturedAuthHeader string
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		nextCalled = true
		capturedAuthHeader = r.Header.Get("Authorization")
	})

	handler := middleware(nextHandler)
	req := requestWithIdentity("session-123")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.True(t, nextCalled, "next handler should be called after successful defense-in-depth refresh")
	assert.Equal(t, "Bearer refreshed-access-token", capturedAuthHeader)
}

// TestSingleFlightRefresh_ConcurrentRequests verifies that concurrent requests
// with the same expired session only trigger a single upstream refresh call.
// Without singleflight, providers that rotate refresh tokens (single-use)
// would fail all but the first concurrent caller.
func TestSingleFlightRefresh_ConcurrentRequests(t *testing.T) {
	t.Parallel()

	const numRequests = 10
	var refreshCallCount atomic.Int32

	expiredTokens := &storage.UpstreamTokens{
		AccessToken:  "expired-access",
		RefreshToken: "one-time-refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
	}

	refreshedTokens := &storage.UpstreamTokens{
		AccessToken:  "fresh-access",
		RefreshToken: "new-refresh",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	}

	// Use a barrier so all goroutines complete GetUpstreamTokens before any
	// enters singleflight. This guarantees they all contend on the same key.
	var storageBarrier sync.WaitGroup
	storageBarrier.Add(numRequests)
	storageGate := make(chan struct{})

	stor := &fakeTokenStorage{
		tokens:  expiredTokens,
		err:     storage.ErrExpired,
		barrier: &storageBarrier,
		gate:    storageGate,
	}
	proceed := make(chan struct{})
	refresher := &fakeRefresher{
		result:    refreshedTokens,
		callCount: &refreshCallCount,
		proceed:   proceed,
	}

	// Create service with fake storage/refresher — singleflight lives in the service
	svc := upstreamtoken.NewInProcessService(stor, refresher)
	serviceGetter := func() upstreamtoken.Service { return svc }

	cfg := &Config{}
	middleware := createMiddlewareFunc(cfg, serviceGetter)

	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {})
	handler := middleware(nextHandler)

	// Use a barrier to ensure all goroutines start at the same time
	ready := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]int, numRequests)

	for i := range numRequests {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-ready // Wait for all goroutines to be ready

			req := requestWithIdentity("sf-concurrent-session")
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			results[idx] = rr.Code
		}(i)
	}

	// Release all goroutines simultaneously
	close(ready)

	// Wait for ALL goroutines to reach GetUpstreamTokens
	storageBarrier.Wait()

	// Release them all at once — they all proceed to singleflight.Do concurrently
	close(storageGate)

	// Give goroutines a moment to enter singleflight, then let refresh complete
	// The singleflight ensures only one actually calls RefreshAndStore
	time.Sleep(10 * time.Millisecond)
	close(proceed)

	wg.Wait()

	// KEY ASSERTION: RefreshAndStore should be called exactly once.
	// Without singleflight, all 10 goroutines would call it independently.
	assert.Equal(t, int32(1), refreshCallCount.Load(),
		"RefreshAndStore should be called exactly once — singleflight deduplicates concurrent refreshes")

	// All requests should succeed
	for i, code := range results {
		assert.Equal(t, http.StatusOK, code,
			"request %d should succeed", i)
	}
}

// fakeTokenStorage returns configured tokens and optionally blocks until
// a barrier is released, ensuring all goroutines reach storage before any
// proceeds to the singleflight refresh.
type fakeTokenStorage struct {
	tokens  *storage.UpstreamTokens
	err     error
	barrier *sync.WaitGroup // if set, each call does barrier.Done() then waits
	gate    chan struct{}   // if set, blocks until closed
}

func (f *fakeTokenStorage) GetUpstreamTokens(_ context.Context, _ string) (*storage.UpstreamTokens, error) {
	if f.barrier != nil {
		f.barrier.Done()
	}
	if f.gate != nil {
		<-f.gate
	}
	return f.tokens, f.err
}

func (*fakeTokenStorage) StoreUpstreamTokens(_ context.Context, _ string, _ *storage.UpstreamTokens) error {
	return nil
}

func (*fakeTokenStorage) DeleteUpstreamTokens(_ context.Context, _ string) error {
	return nil
}

// fakeRefresher counts calls and blocks until proceed is closed.
type fakeRefresher struct {
	result    *storage.UpstreamTokens
	callCount *atomic.Int32
	proceed   chan struct{}
}

func (f *fakeRefresher) RefreshAndStore(_ context.Context, _ string, _ *storage.UpstreamTokens) (*storage.UpstreamTokens, error) {
	f.callCount.Add(1)
	<-f.proceed
	return f.result, nil
}

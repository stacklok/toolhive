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
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	storagemocks "github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/transport/types/mocks"
)

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

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)
	// Storage should NOT be called when there's no identity

	storageGetter := func() storage.UpstreamTokenStorage {
		return mockStorage
	}

	cfg := &Config{}
	middleware := createMiddlewareFunc(cfg, storageGetter, nil)

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

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)
	// Storage should NOT be called when there's no tsid claim

	storageGetter := func() storage.UpstreamTokenStorage {
		return mockStorage
	}

	cfg := &Config{}
	middleware := createMiddlewareFunc(cfg, storageGetter, nil)

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

func TestMiddleware_StorageUnavailable(t *testing.T) {
	t.Parallel()

	storageGetter := func() storage.UpstreamTokenStorage {
		return nil // Storage unavailable
	}

	cfg := &Config{}
	middleware := createMiddlewareFunc(cfg, storageGetter, nil)

	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		nextCalled = true
	})

	handler := middleware(nextHandler)

	// Create request with identity and tsid claim
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		Subject: "user123",
		Claims: map[string]any{
			"sub":                          "user123",
			session.TokenSessionIDClaimKey: "session-123",
		},
	}
	ctx := auth.WithIdentity(req.Context(), identity)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.True(t, nextCalled, "next handler should be called")
}

func TestMiddleware_ClientAttributableStorageErrors_Returns401(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
	}{
		{"not found", storage.ErrNotFound},
		{"expired", storage.ErrExpired},
		{"invalid binding", storage.ErrInvalidBinding},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)
			mockStorage.EXPECT().
				GetUpstreamTokens(gomock.Any(), "session-123").
				Return(nil, tt.err)

			storageGetter := func() storage.UpstreamTokenStorage {
				return mockStorage
			}

			cfg := &Config{}
			middleware := createMiddlewareFunc(cfg, storageGetter, nil)

			var nextCalled bool
			nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
				nextCalled = true
			})

			handler := middleware(nextHandler)

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			identity := &auth.Identity{
				Subject: "user123",
				Claims: map[string]any{
					"sub":                          "user123",
					session.TokenSessionIDClaimKey: "session-123",
				},
			}
			ctx := auth.WithIdentity(req.Context(), identity)
			req = req.WithContext(ctx)

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

	storageGetter := func() storage.UpstreamTokenStorage {
		return mockStorage
	}

	cfg := &Config{}
	middleware := createMiddlewareFunc(cfg, storageGetter, nil)

	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		nextCalled = true
	})

	handler := middleware(nextHandler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		Subject: "user123",
		Claims: map[string]any{
			"sub":                          "user123",
			session.TokenSessionIDClaimKey: "session-123",
		},
	}
	ctx := auth.WithIdentity(req.Context(), identity)
	req = req.WithContext(ctx)

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

	storageGetter := func() storage.UpstreamTokenStorage {
		return mockStorage
	}

	cfg := &Config{
		HeaderStrategy: HeaderStrategyReplace,
	}
	middleware := createMiddlewareFunc(cfg, storageGetter, nil)

	var capturedAuthHeader string
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		capturedAuthHeader = r.Header.Get("Authorization")
	})

	handler := middleware(nextHandler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		Subject: "user123",
		Claims: map[string]any{
			"sub":                          "user123",
			session.TokenSessionIDClaimKey: "session-123",
		},
	}
	ctx := auth.WithIdentity(req.Context(), identity)
	req = req.WithContext(ctx)

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

	storageGetter := func() storage.UpstreamTokenStorage {
		return mockStorage
	}

	cfg := &Config{
		HeaderStrategy:   HeaderStrategyCustom,
		CustomHeaderName: "X-Upstream-Token",
	}
	middleware := createMiddlewareFunc(cfg, storageGetter, nil)

	var capturedCustomHeader string
	var capturedAuthHeader string
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		capturedCustomHeader = r.Header.Get("X-Upstream-Token")
		capturedAuthHeader = r.Header.Get("Authorization")
	})

	handler := middleware(nextHandler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer original-token")
	identity := &auth.Identity{
		Subject: "user123",
		Claims: map[string]any{
			"sub":                          "user123",
			session.TokenSessionIDClaimKey: "session-123",
		},
	}
	ctx := auth.WithIdentity(req.Context(), identity)
	req = req.WithContext(ctx)

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

	// Token is expired
	tokens := &storage.UpstreamTokens{
		AccessToken: "expired-upstream-token",
		ExpiresAt:   time.Now().Add(-1 * time.Hour), // Expired 1 hour ago
	}

	mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)
	mockStorage.EXPECT().
		GetUpstreamTokens(gomock.Any(), "session-123").
		Return(tokens, nil)

	storageGetter := func() storage.UpstreamTokenStorage {
		return mockStorage
	}

	cfg := &Config{}
	middleware := createMiddlewareFunc(cfg, storageGetter, nil)

	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		nextCalled = true
	})

	handler := middleware(nextHandler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		Subject: "user123",
		Claims: map[string]any{
			"sub":                          "user123",
			session.TokenSessionIDClaimKey: "session-123",
		},
	}
	ctx := auth.WithIdentity(req.Context(), identity)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.False(t, nextCalled, "next handler should NOT be called for expired tokens")
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Contains(t, rr.Header().Get("WWW-Authenticate"), `error="invalid_token"`)
}

func TestMiddleware_EmptySelectedToken(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Tokens exist but the selected type is empty
	tokens := &storage.UpstreamTokens{
		AccessToken: "", // Empty access token
		IDToken:     "upstream-id-token",
		ExpiresAt:   time.Now().Add(1 * time.Hour),
	}

	mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)
	mockStorage.EXPECT().
		GetUpstreamTokens(gomock.Any(), "session-123").
		Return(tokens, nil)

	storageGetter := func() storage.UpstreamTokenStorage {
		return mockStorage
	}

	cfg := &Config{}
	middleware := createMiddlewareFunc(cfg, storageGetter, nil)

	var nextCalled bool
	var capturedAuthHeader string
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		nextCalled = true
		capturedAuthHeader = r.Header.Get("Authorization")
	})

	handler := middleware(nextHandler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		Subject: "user123",
		Claims: map[string]any{
			"sub":                          "user123",
			session.TokenSessionIDClaimKey: "session-123",
		},
	}
	ctx := auth.WithIdentity(req.Context(), identity)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.True(t, nextCalled, "next handler should be called")
	assert.Empty(t, capturedAuthHeader, "should not inject empty token")
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

	storageGetter := func() storage.UpstreamTokenStorage {
		return mockStorage
	}

	cfg := &Config{}
	middleware := createMiddlewareFunc(cfg, storageGetter, nil)

	// Test that context is properly passed through
	var receivedCtx context.Context
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		receivedCtx = r.Context()
	})

	handler := middleware(nextHandler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		Subject: "user123",
		Claims: map[string]any{
			"sub":                          "user123",
			session.TokenSessionIDClaimKey: "session-ctx",
		},
	}
	ctx := auth.WithIdentity(req.Context(), identity)
	req = req.WithContext(ctx)

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

			// Storage getter is only called if validation passes (expectAddMiddleware)
			// because validation happens before GetUpstreamTokenStorage is called
			if tt.expectAddMiddleware {
				mockRunner.EXPECT().GetUpstreamTokenStorage().Return(func() storage.UpstreamTokenStorage {
					return nil // Storage availability is checked at request time
				})
				mockRunner.EXPECT().GetUpstreamTokenRefresher().Return(func() storage.UpstreamTokenRefresher {
					return nil // Refresher availability is checked at request time
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

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)
	// Storage should NOT be called when tsid claim is wrong type

	storageGetter := func() storage.UpstreamTokenStorage {
		return mockStorage
	}

	cfg := &Config{}
	middleware := createMiddlewareFunc(cfg, storageGetter, nil)

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
			"sub":                          "user123",
			session.TokenSessionIDClaimKey: 12345, // Wrong type: int instead of string
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

	storageGetter := func() storage.UpstreamTokenStorage {
		return mockStorage
	}
	refresherGetter := func() storage.UpstreamTokenRefresher {
		return mockRefresher
	}

	cfg := &Config{}
	middleware := createMiddlewareFunc(cfg, storageGetter, refresherGetter)

	var nextCalled bool
	var capturedAuthHeader string
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		nextCalled = true
		capturedAuthHeader = r.Header.Get("Authorization")
	})

	handler := middleware(nextHandler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		Subject: "user123",
		Claims: map[string]any{
			"sub":                          "user123",
			session.TokenSessionIDClaimKey: "session-123",
		},
	}
	ctx := auth.WithIdentity(req.Context(), identity)
	req = req.WithContext(ctx)

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

	storageGetter := func() storage.UpstreamTokenStorage {
		return mockStorage
	}
	refresherGetter := func() storage.UpstreamTokenRefresher {
		return mockRefresher
	}

	cfg := &Config{}
	middleware := createMiddlewareFunc(cfg, storageGetter, refresherGetter)

	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		nextCalled = true
	})

	handler := middleware(nextHandler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		Subject: "user123",
		Claims: map[string]any{
			"sub":                          "user123",
			session.TokenSessionIDClaimKey: "session-123",
		},
	}
	ctx := auth.WithIdentity(req.Context(), identity)
	req = req.WithContext(ctx)

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

	// No refresher mock needed — refresh should not be attempted

	storageGetter := func() storage.UpstreamTokenStorage {
		return mockStorage
	}
	refresherGetter := func() storage.UpstreamTokenRefresher {
		t.Fatal("refresher getter should not be called when there is no refresh token")
		return nil
	}

	cfg := &Config{}
	middleware := createMiddlewareFunc(cfg, storageGetter, refresherGetter)

	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		nextCalled = true
	})

	handler := middleware(nextHandler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		Subject: "user123",
		Claims: map[string]any{
			"sub":                          "user123",
			session.TokenSessionIDClaimKey: "session-123",
		},
	}
	ctx := auth.WithIdentity(req.Context(), identity)
	req = req.WithContext(ctx)

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

	storageGetter := func() storage.UpstreamTokenStorage {
		return mockStorage
	}
	refresherGetter := func() storage.UpstreamTokenRefresher {
		return mockRefresher
	}

	cfg := &Config{}
	middleware := createMiddlewareFunc(cfg, storageGetter, refresherGetter)

	var nextCalled bool
	var capturedAuthHeader string
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		nextCalled = true
		capturedAuthHeader = r.Header.Get("Authorization")
	})

	handler := middleware(nextHandler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		Subject: "user123",
		Claims: map[string]any{
			"sub":                          "user123",
			session.TokenSessionIDClaimKey: "session-123",
		},
	}
	ctx := auth.WithIdentity(req.Context(), identity)
	req = req.WithContext(ctx)

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

	// Use a real (non-mock) storage and refresher to avoid gomock concurrency issues
	stor := &fakeTokenStorage{tokens: expiredTokens, err: storage.ErrExpired}
	refresher := &fakeRefresher{
		result:    refreshedTokens,
		callCount: &refreshCallCount,
	}

	storageGetter := func() storage.UpstreamTokenStorage { return stor }
	refresherGetter := func() storage.UpstreamTokenRefresher { return refresher }

	cfg := &Config{}
	middleware := createMiddlewareFunc(cfg, storageGetter, refresherGetter)

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

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			identity := &auth.Identity{
				Subject: "user123",
				Claims: map[string]any{
					"sub":                          "user123",
					session.TokenSessionIDClaimKey: "sf-concurrent-session",
				},
			}
			ctx := auth.WithIdentity(req.Context(), identity)
			req = req.WithContext(ctx)

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			results[idx] = rr.Code
		}(i)
	}

	// Release all goroutines simultaneously
	close(ready)
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

// fakeTokenStorage always returns the configured tokens and error.
type fakeTokenStorage struct {
	tokens *storage.UpstreamTokens
	err    error
}

func (f *fakeTokenStorage) GetUpstreamTokens(_ context.Context, _ string) (*storage.UpstreamTokens, error) {
	return f.tokens, f.err
}

func (f *fakeTokenStorage) StoreUpstreamTokens(_ context.Context, _ string, _ *storage.UpstreamTokens) error {
	return nil
}

func (f *fakeTokenStorage) DeleteUpstreamTokens(_ context.Context, _ string) error {
	return nil
}

// fakeRefresher counts calls and adds a small delay to allow concurrency overlap.
type fakeRefresher struct {
	result    *storage.UpstreamTokens
	callCount *atomic.Int32
}

func (f *fakeRefresher) RefreshAndStore(_ context.Context, _ string, _ *storage.UpstreamTokens) (*storage.UpstreamTokens, error) {
	f.callCount.Add(1)
	// Small delay to ensure concurrent goroutines overlap in the singleflight window
	time.Sleep(50 * time.Millisecond)
	return f.result, nil
}

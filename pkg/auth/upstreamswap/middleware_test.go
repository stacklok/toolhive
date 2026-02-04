// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstreamswap

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
			name:    "nil config (defaults)",
			cfg:     &Config{},
			wantErr: false,
		},
		{
			name: "valid access_token type",
			cfg: &Config{
				TokenType: TokenTypeAccessToken,
			},
			wantErr: false,
		},
		{
			name: "valid id_token type",
			cfg: &Config{
				TokenType: TokenTypeIDToken,
			},
			wantErr: false,
		},
		{
			name: "invalid token type",
			cfg: &Config{
				TokenType: "invalid",
			},
			wantErr: true,
			errMsg:  "invalid token_type",
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

func TestSelectToken(t *testing.T) {
	t.Parallel()

	tokens := &storage.UpstreamTokens{
		AccessToken: "access-token-value",
		IDToken:     "id-token-value",
	}

	tests := []struct {
		name      string
		tokenType string
		want      string
	}{
		{
			name:      "access token (explicit)",
			tokenType: TokenTypeAccessToken,
			want:      "access-token-value",
		},
		{
			name:      "access token (default empty)",
			tokenType: "",
			want:      "access-token-value",
		},
		{
			name:      "id token",
			tokenType: TokenTypeIDToken,
			want:      "id-token-value",
		},
		{
			name:      "unknown type defaults to access",
			tokenType: "unknown",
			want:      "access-token-value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := selectToken(tokens, tt.tokenType)
			assert.Equal(t, tt.want, got)
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
	middleware := createMiddlewareFunc(cfg, storageGetter)

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
	middleware := createMiddlewareFunc(cfg, storageGetter)

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
	middleware := createMiddlewareFunc(cfg, storageGetter)

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

func TestMiddleware_TokensNotFound(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)
	mockStorage.EXPECT().
		GetUpstreamTokens(gomock.Any(), "session-123").
		Return(nil, storage.ErrNotFound)

	storageGetter := func() storage.UpstreamTokenStorage {
		return mockStorage
	}

	cfg := &Config{}
	middleware := createMiddlewareFunc(cfg, storageGetter)

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

	assert.True(t, nextCalled, "next handler should be called")
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
	middleware := createMiddlewareFunc(cfg, storageGetter)

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

	assert.True(t, nextCalled, "next handler should be called despite error")
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
		TokenType:      TokenTypeAccessToken,
		HeaderStrategy: HeaderStrategyReplace,
	}
	middleware := createMiddlewareFunc(cfg, storageGetter)

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

func TestMiddleware_SuccessfulSwap_IDToken(t *testing.T) {
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
		TokenType:      TokenTypeIDToken,
		HeaderStrategy: HeaderStrategyReplace,
	}
	middleware := createMiddlewareFunc(cfg, storageGetter)

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

	assert.Equal(t, "Bearer upstream-id-token", capturedAuthHeader)
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
		TokenType:        TokenTypeAccessToken,
		HeaderStrategy:   HeaderStrategyCustom,
		CustomHeaderName: "X-Upstream-Token",
	}
	middleware := createMiddlewareFunc(cfg, storageGetter)

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

func TestMiddleware_ExpiredTokens_ContinuesWithWarning(t *testing.T) {
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
	middleware := createMiddlewareFunc(cfg, storageGetter)

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

	// MVP: Should continue with expired token
	assert.Equal(t, "Bearer expired-upstream-token", capturedAuthHeader)
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

	cfg := &Config{
		TokenType: TokenTypeAccessToken, // Selecting empty access token
	}
	middleware := createMiddlewareFunc(cfg, storageGetter)

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
	middleware := createMiddlewareFunc(cfg, storageGetter)

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
					TokenType:      TokenTypeAccessToken,
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
			name: "invalid token type fails validation",
			params: MiddlewareParams{
				Config: &Config{
					TokenType: "invalid_token_type",
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
	middleware := createMiddlewareFunc(cfg, storageGetter)

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

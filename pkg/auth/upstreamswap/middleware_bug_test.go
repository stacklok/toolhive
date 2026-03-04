// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstreamswap

import (
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
)

// TestFix_StorageError_Returns503 verifies that when GetUpstreamTokens fails,
// the middleware returns 503 Service Unavailable instead of silently
// forwarding the original (wrong) Authorization header to the remote server.
func TestFix_StorageError_Returns503(t *testing.T) {
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

	originalJWT := "Bearer toolhive-internal-jwt-token"
	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		nextCalled = true
	})

	handler := middleware(nextHandler)

	req := httptest.NewRequest(http.MethodPost, "/v2/mcp", nil)
	req.Header.Set("Authorization", originalJWT)
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

	// FIX VERIFIED: middleware returns 503 instead of forwarding the wrong credential
	assert.False(t, nextCalled,
		"next handler should NOT be called — request must not be forwarded with wrong token")
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code,
		"should return 503 Service Unavailable when upstream tokens cannot be retrieved")
}

// TestFix_TokenNotFound_Returns503 verifies that when tokens are not found
// in storage (race condition where OAuth callback hasn't completed), the
// middleware returns 503 instead of forwarding the original JWT.
func TestFix_TokenNotFound_Returns503(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)
	mockStorage.EXPECT().
		GetUpstreamTokens(gomock.Any(), "session-456").
		Return(nil, storage.ErrNotFound)

	storageGetter := func() storage.UpstreamTokenStorage {
		return mockStorage
	}

	cfg := &Config{}
	middleware := createMiddlewareFunc(cfg, storageGetter)

	originalJWT := "Bearer toolhive-jwt-from-embedded-auth-server"
	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		nextCalled = true
	})

	handler := middleware(nextHandler)

	req := httptest.NewRequest(http.MethodPost, "/v2/mcp", nil)
	req.Header.Set("Authorization", originalJWT)
	identity := &auth.Identity{
		Subject: "user123",
		Claims: map[string]any{
			"sub":                          "user123",
			session.TokenSessionIDClaimKey: "session-456",
		},
	}
	ctx := auth.WithIdentity(req.Context(), identity)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// FIX VERIFIED: client gets a retryable 503 instead of a permanent 401 cascade
	require.False(t, nextCalled, "next handler should NOT be called")
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code,
		"should return 503 when tokens not yet available (race condition)")
}

// TestFix_ExpiredToken_Returns503 verifies that expired upstream tokens are
// no longer forwarded to the backend. Instead, the middleware returns 503.
func TestFix_ExpiredToken_Returns503(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	expiredTokens := &storage.UpstreamTokens{
		AccessToken:  "expired-asana-oauth-token",
		RefreshToken: "valid-refresh-token",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
	}

	mockStorage := storagemocks.NewMockUpstreamTokenStorage(ctrl)
	mockStorage.EXPECT().
		GetUpstreamTokens(gomock.Any(), "session-789").
		Return(expiredTokens, nil)

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

	req := httptest.NewRequest(http.MethodPost, "/v2/mcp", nil)
	req.Header.Set("Authorization", "Bearer toolhive-jwt")
	identity := &auth.Identity{
		Subject: "user123",
		Claims: map[string]any{
			"sub":                          "user123",
			session.TokenSessionIDClaimKey: "session-789",
		},
	}
	ctx := auth.WithIdentity(req.Context(), identity)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// FIX VERIFIED: expired tokens are not forwarded to the backend
	assert.False(t, nextCalled, "next handler should NOT be called with expired token")
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code,
		"should return 503 for expired tokens")
}

// TestFix_StorageUnavailable_Returns503 verifies that when the storage
// backend is nil, the middleware returns 503 instead of forwarding the
// original JWT.
func TestFix_StorageUnavailable_Returns503(t *testing.T) {
	t.Parallel()

	storageGetter := func() storage.UpstreamTokenStorage {
		return nil
	}

	cfg := &Config{}
	middleware := createMiddlewareFunc(cfg, storageGetter)

	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		nextCalled = true
	})

	handler := middleware(nextHandler)

	req := httptest.NewRequest(http.MethodPost, "/v2/mcp", nil)
	req.Header.Set("Authorization", "Bearer toolhive-jwt")
	identity := &auth.Identity{
		Subject: "user123",
		Claims: map[string]any{
			"sub":                          "user123",
			session.TokenSessionIDClaimKey: "session-abc",
		},
	}
	ctx := auth.WithIdentity(req.Context(), identity)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.False(t, nextCalled, "next handler should NOT be called")
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code,
		"should return 503 when storage is unavailable")
}

// TestFix_AllFailureModes_Return503 verifies that every failure path in the
// middleware now returns 503 Service Unavailable instead of silently
// continuing with the wrong credential.
func TestFix_AllFailureModes_Return503(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		setupStorage func(ctrl *gomock.Controller) StorageGetter
		description  string
	}{
		{
			name: "storage_error",
			setupStorage: func(ctrl *gomock.Controller) StorageGetter {
				mock := storagemocks.NewMockUpstreamTokenStorage(ctrl)
				mock.EXPECT().
					GetUpstreamTokens(gomock.Any(), "tsid-1").
					Return(nil, errors.New("connection refused"))
				return func() storage.UpstreamTokenStorage { return mock }
			},
			description: "storage connection error",
		},
		{
			name: "token_not_found",
			setupStorage: func(ctrl *gomock.Controller) StorageGetter {
				mock := storagemocks.NewMockUpstreamTokenStorage(ctrl)
				mock.EXPECT().
					GetUpstreamTokens(gomock.Any(), "tsid-1").
					Return(nil, storage.ErrNotFound)
				return func() storage.UpstreamTokenStorage { return mock }
			},
			description: "token not found in storage",
		},
		{
			name: "empty_access_token",
			setupStorage: func(ctrl *gomock.Controller) StorageGetter {
				mock := storagemocks.NewMockUpstreamTokenStorage(ctrl)
				mock.EXPECT().
					GetUpstreamTokens(gomock.Any(), "tsid-1").
					Return(&storage.UpstreamTokens{
						AccessToken: "",
						ExpiresAt:   time.Now().Add(1 * time.Hour),
					}, nil)
				return func() storage.UpstreamTokenStorage { return mock }
			},
			description: "empty access token",
		},
		{
			name: "storage_unavailable",
			setupStorage: func(_ *gomock.Controller) StorageGetter {
				return func() storage.UpstreamTokenStorage { return nil }
			},
			description: "nil storage backend",
		},
		{
			name: "expired_token",
			setupStorage: func(ctrl *gomock.Controller) StorageGetter {
				mock := storagemocks.NewMockUpstreamTokenStorage(ctrl)
				mock.EXPECT().
					GetUpstreamTokens(gomock.Any(), "tsid-1").
					Return(&storage.UpstreamTokens{
						AccessToken: "expired-token",
						ExpiresAt:   time.Now().Add(-1 * time.Hour),
					}, nil)
				return func() storage.UpstreamTokenStorage { return mock }
			},
			description: "expired upstream token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			storageGetter := tt.setupStorage(ctrl)
			middleware := createMiddlewareFunc(&Config{}, storageGetter)

			nextCalled := false
			nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
				nextCalled = true
			})

			handler := middleware(nextHandler)

			req := httptest.NewRequest(http.MethodPost, "/v2/mcp", nil)
			req.Header.Set("Authorization", "Bearer toolhive-jwt")
			identity := &auth.Identity{
				Subject: "user123",
				Claims: map[string]any{
					"sub":                          "user123",
					session.TokenSessionIDClaimKey: "tsid-1",
				},
			}
			ctx := auth.WithIdentity(req.Context(), identity)
			req = req.WithContext(ctx)

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			// FIX VERIFIED: all failure modes now return 503
			assert.False(t, nextCalled,
				"FIX VERIFIED (%s): %s — middleware returns 503 instead of forwarding wrong token",
				tt.name, tt.description)
			assert.Equal(t, http.StatusServiceUnavailable, rr.Code,
				"should return 503 for %s", tt.description)
		})
	}
}

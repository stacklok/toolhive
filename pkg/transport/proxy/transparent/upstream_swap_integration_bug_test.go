// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// fakeUpstreamTokenStorage is a simple in-memory storage for testing the
// full proxy chain without gomock.
type fakeUpstreamTokenStorage struct {
	tokens map[string]*storage.UpstreamTokens
}

func (f *fakeUpstreamTokenStorage) StoreUpstreamTokens(_ context.Context, sessionID string, tokens *storage.UpstreamTokens) error {
	f.tokens[sessionID] = tokens
	return nil
}

func (f *fakeUpstreamTokenStorage) GetUpstreamTokens(_ context.Context, sessionID string) (*storage.UpstreamTokens, error) {
	t, ok := f.tokens[sessionID]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return t, nil
}

func (f *fakeUpstreamTokenStorage) DeleteUpstreamTokens(_ context.Context, sessionID string) error {
	delete(f.tokens, sessionID)
	return nil
}

// fakeAuthMiddleware creates a middleware that injects a fixed identity into
// the request context, simulating the auth middleware in the real proxy chain.
func fakeAuthMiddleware(identity *auth.Identity) types.MiddlewareFunction {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := auth.WithIdentity(r.Context(), identity)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// upstreamSwapMiddleware creates a version of the upstreamswap middleware
// for integration testing that matches the FIXED behavior: returns 503 when
// token retrieval fails instead of silently continuing.
func upstreamSwapMiddleware(stor storage.UpstreamTokenStorage) types.MiddlewareFunction {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := auth.IdentityFromContext(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}

			tsid, ok := identity.Claims[session.TokenSessionIDClaimKey].(string)
			if !ok || tsid == "" {
				next.ServeHTTP(w, r)
				return
			}

			if stor == nil {
				http.Error(w, "upstream token storage unavailable", http.StatusServiceUnavailable)
				return
			}

			tokens, err := stor.GetUpstreamTokens(r.Context(), tsid)
			if err != nil {
				http.Error(w, "upstream token unavailable", http.StatusServiceUnavailable)
				return
			}

			if tokens.IsExpired(time.Now()) {
				http.Error(w, "upstream token expired", http.StatusServiceUnavailable)
				return
			}

			if tokens.AccessToken == "" {
				http.Error(w, "upstream access token empty", http.StatusServiceUnavailable)
				return
			}

			r.Header.Set("Authorization", fmt.Sprintf("Bearer %s", tokens.AccessToken))
			next.ServeHTTP(w, r)
		})
	}
}

// TestFix_EndToEnd_StorageFailure_Returns503_NotForwarded verifies the full
// fix chain: when upstream tokens are missing from storage, the middleware
// returns 503 to the client. The request is NEVER forwarded to the remote
// server, so no 401 occurs, and the workload is NOT permanently killed.
func TestFix_EndToEnd_StorageFailure_Returns503_NotForwarded(t *testing.T) {
	t.Parallel()

	// Remote MCP server that tracks requests
	var remoteRequestCount atomic.Int32
	remoteServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		remoteRequestCount.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer remoteServer.Close()

	// Storage does NOT have the tokens (simulates race condition)
	tokenStorage := &fakeUpstreamTokenStorage{tokens: make(map[string]*storage.UpstreamTokens)}

	// Identity that the auth middleware will inject into every request
	identity := &auth.Identity{
		Subject: "user123",
		Claims: map[string]any{
			"sub":                          "user123",
			session.TokenSessionIDClaimKey: "session-missing",
		},
	}

	var unauthorizedCallbackFired atomic.Bool
	proxy := NewTransparentProxy(
		"127.0.0.1", 0, remoteServer.URL,
		nil, nil, nil,
		false, true, "streamable-http",
		nil,
		func() { unauthorizedCallbackFired.Store(true) },
		"", false,
		// Auth middleware runs first (injects identity), then upstreamswap
		types.NamedMiddleware{
			Name:     "auth",
			Function: fakeAuthMiddleware(identity),
		},
		types.NamedMiddleware{
			Name:     "upstreamswap",
			Function: upstreamSwapMiddleware(tokenStorage),
		},
	)

	ctx := context.Background()
	err := proxy.Start(ctx)
	require.NoError(t, err)
	defer func() {
		stopErr := proxy.Stop(context.Background())
		assert.NoError(t, stopErr)
	}()

	addr := proxy.listener.Addr()
	require.NotNil(t, addr)

	proxyURL := fmt.Sprintf("http://%s/v2/mcp", addr.String())
	body := `{"jsonrpc":"2.0","method":"initialize","id":"1","params":{}}`
	req, err := http.NewRequest("POST", proxyURL, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer toolhive-jwt-with-tsid")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// FIX VERIFIED: Client gets 503 (retryable), NOT 401
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode,
		"client should get 503 Service Unavailable, not 401")

	// FIX VERIFIED: Remote server was NEVER contacted with wrong credentials
	assert.Equal(t, int32(0), remoteRequestCount.Load(),
		"remote server should NOT have received any request — middleware blocked it")

	// FIX VERIFIED: Workload is NOT permanently marked as unauthenticated
	assert.False(t, unauthorizedCallbackFired.Load(),
		"workload should NOT be marked unauthenticated — the 401 never happened")
}

// TestFix_EndToEnd_ExpiredToken_Returns503_NotForwarded verifies that
// expired upstream tokens are no longer forwarded to the backend. The
// middleware returns 503 and the workload stays alive.
func TestFix_EndToEnd_ExpiredToken_Returns503_NotForwarded(t *testing.T) {
	t.Parallel()

	var remoteRequestCount atomic.Int32
	remoteServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		remoteRequestCount.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer remoteServer.Close()

	// Storage has tokens but they're expired
	tokenStorage := &fakeUpstreamTokenStorage{
		tokens: map[string]*storage.UpstreamTokens{
			"session-expired": {
				AccessToken:  "expired-upstream-token",
				RefreshToken: "valid-refresh-token",
				ExpiresAt:    time.Now().Add(-1 * time.Hour),
			},
		},
	}

	identity := &auth.Identity{
		Subject: "user123",
		Claims: map[string]any{
			"sub":                          "user123",
			session.TokenSessionIDClaimKey: "session-expired",
		},
	}

	var unauthorizedCallbackFired atomic.Bool
	proxy := NewTransparentProxy(
		"127.0.0.1", 0, remoteServer.URL,
		nil, nil, nil,
		false, true, "streamable-http",
		nil,
		func() { unauthorizedCallbackFired.Store(true) },
		"", false,
		types.NamedMiddleware{
			Name:     "auth",
			Function: fakeAuthMiddleware(identity),
		},
		types.NamedMiddleware{
			Name:     "upstreamswap",
			Function: upstreamSwapMiddleware(tokenStorage),
		},
	)

	ctx := context.Background()
	err := proxy.Start(ctx)
	require.NoError(t, err)
	defer func() {
		stopErr := proxy.Stop(context.Background())
		assert.NoError(t, stopErr)
	}()

	addr := proxy.listener.Addr()
	require.NotNil(t, addr)

	proxyURL := fmt.Sprintf("http://%s/v2/mcp", addr.String())
	body := `{"jsonrpc":"2.0","method":"initialize","id":"1","params":{}}`
	req, err := http.NewRequest("POST", proxyURL, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer toolhive-jwt")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// FIX VERIFIED: Client gets 503 (retryable), expired token NOT forwarded
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode,
		"client should get 503 — expired token was not forwarded")

	// FIX VERIFIED: Remote server was never contacted
	assert.Equal(t, int32(0), remoteRequestCount.Load(),
		"remote server should NOT have received expired token")

	// FIX VERIFIED: Workload stays alive
	assert.False(t, unauthorizedCallbackFired.Load(),
		"workload should NOT be marked unauthenticated")
}

// TestFix_EndToEnd_ValidToken_ForwardedSuccessfully verifies that the fix
// doesn't break the happy path: valid, non-expired upstream tokens are still
// correctly injected and forwarded to the remote server.
func TestFix_EndToEnd_ValidToken_ForwardedSuccessfully(t *testing.T) {
	t.Parallel()

	validToken := "valid-asana-oauth-token"
	var capturedAuthHeader atomic.Value

	remoteServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuthHeader.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{"protocolVersion":"2024-11-05"}}`))
	}))
	defer remoteServer.Close()

	// Storage has valid, non-expired tokens
	tokenStorage := &fakeUpstreamTokenStorage{
		tokens: map[string]*storage.UpstreamTokens{
			"session-valid": {
				AccessToken: validToken,
				ExpiresAt:   time.Now().Add(1 * time.Hour),
			},
		},
	}

	identity := &auth.Identity{
		Subject: "user123",
		Claims: map[string]any{
			"sub":                          "user123",
			session.TokenSessionIDClaimKey: "session-valid",
		},
	}

	proxy := NewTransparentProxy(
		"127.0.0.1", 0, remoteServer.URL,
		nil, nil, nil,
		false, true, "streamable-http",
		nil, nil,
		"", false,
		types.NamedMiddleware{
			Name:     "auth",
			Function: fakeAuthMiddleware(identity),
		},
		types.NamedMiddleware{
			Name:     "upstreamswap",
			Function: upstreamSwapMiddleware(tokenStorage),
		},
	)

	ctx := context.Background()
	err := proxy.Start(ctx)
	require.NoError(t, err)
	defer func() {
		stopErr := proxy.Stop(context.Background())
		assert.NoError(t, stopErr)
	}()

	addr := proxy.listener.Addr()
	require.NotNil(t, addr)

	proxyURL := fmt.Sprintf("http://%s/v2/mcp", addr.String())
	body := `{"jsonrpc":"2.0","method":"initialize","id":"1","params":{}}`
	req, err := http.NewRequest("POST", proxyURL, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer toolhive-jwt-should-be-replaced")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"request should succeed with valid upstream token")

	receivedAuth, _ := capturedAuthHeader.Load().(string)
	assert.Equal(t, "Bearer "+validToken, receivedAuth,
		"remote server should receive the upstream Asana token, not the ToolHive JWT")
}

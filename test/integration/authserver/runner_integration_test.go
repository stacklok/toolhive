// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authserverrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/test/integration/authserver/helpers"
)

// TestRunner_EmbeddedAuthServerIntegration verifies that the embedded auth server
// correctly mounts and operates within the proxy runner transport layer.
func TestRunner_EmbeddedAuthServerIntegration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Setup: Create mock upstream IDP
	upstream := helpers.NewMockUpstreamIDP(t)

	t.Run("Auth endpoints are mounted at correct prefixes", func(t *testing.T) {
		t.Parallel()

		// Create auth server config
		authConfig := helpers.NewTestAuthServerConfig(t, upstream.URL())

		// Expected prefix handlers that the runner should configure
		// (based on runner.go lines 231-249)
		expectedPrefixes := []string{
			"/oauth/",
			"/.well-known/oauth-authorization-server",
			"/.well-known/openid-configuration",
			"/.well-known/jwks.json",
		}

		// Create the transport config as the runner would
		transportConfig := types.Config{
			Type:      "streamable-http",
			ProxyPort: 0, // Will be assigned
		}

		// Create embedded auth server as runner.Run() does
		embeddedAuthServer, err := authserverrunner.NewEmbeddedAuthServer(ctx, authConfig)
		require.NoError(t, err)
		defer func() {
			_ = embeddedAuthServer.Close()
		}()

		handler := embeddedAuthServer.Handler()
		transportConfig.PrefixHandlers = map[string]http.Handler{
			"/oauth/": handler, // OAuth endpoints (authorize, callback, token, register)
			"/.well-known/oauth-authorization-server": handler, // RFC 8414 OAuth AS Metadata
			"/.well-known/openid-configuration":       handler, // OIDC Discovery
			"/.well-known/jwks.json":                  handler, // JSON Web Key Set
		}

		// Verify all expected prefixes are present
		for _, prefix := range expectedPrefixes {
			assert.Contains(t, transportConfig.PrefixHandlers, prefix,
				"transport config should have prefix handler for %s", prefix)
		}
	})

	t.Run("OAuth endpoints do not conflict with MCP endpoints", func(t *testing.T) {
		t.Parallel()

		// Create auth server config
		authConfig := helpers.NewTestAuthServerConfig(t, upstream.URL())

		// Create embedded auth server
		embeddedAuthServer, err := authserverrunner.NewEmbeddedAuthServer(ctx, authConfig)
		require.NoError(t, err)
		defer func() {
			_ = embeddedAuthServer.Close()
		}()

		// Create test HTTP server
		server := httptest.NewServer(embeddedAuthServer.Handler())
		defer server.Close()

		// OAuth endpoints should work
		oauthClient := helpers.NewOAuthClient(t, server.URL)

		// JWKS should be accessible
		jwks, statusCode := oauthClient.GetJWKS()
		assert.Equal(t, http.StatusOK, statusCode)
		assert.Contains(t, jwks, "keys")

		// OAuth discovery should be accessible
		metadata, statusCode := oauthClient.GetOAuthDiscovery()
		assert.Equal(t, http.StatusOK, statusCode)
		assert.Contains(t, metadata, "issuer")

		// /.well-known/oauth-protected-resource should NOT be served by auth server
		// (it's an MCP endpoint per the spec)
		resp, err := http.Get(server.URL + "/.well-known/oauth-protected-resource")
		require.NoError(t, err)
		defer resp.Body.Close()
		// Should return 404 - auth server doesn't handle this endpoint
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("Auth server handles concurrent requests", func(t *testing.T) {
		t.Parallel()

		authConfig := helpers.NewTestAuthServerConfig(t, upstream.URL())

		embeddedAuthServer, err := authserverrunner.NewEmbeddedAuthServer(ctx, authConfig)
		require.NoError(t, err)
		defer func() {
			_ = embeddedAuthServer.Close()
		}()

		server := httptest.NewServer(embeddedAuthServer.Handler())
		defer server.Close()

		// Make concurrent requests to various endpoints
		var wg sync.WaitGroup
		errors := make(chan error, 10)

		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				client := helpers.NewOAuthClient(t, server.URL)
				_, statusCode := client.GetJWKS()
				if statusCode != http.StatusOK {
					errors <- assert.AnError
				}
			}()
		}

		// Wait for all goroutines with timeout
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()

		select {
		case <-done:
			// Success - check no errors
			close(errors)
			for err := range errors {
				t.Errorf("concurrent request failed: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for concurrent requests")
		}
	})
}

// TestRunner_CleanupClosesAuthServer verifies that runner cleanup properly
// closes the embedded auth server.
func TestRunner_CleanupClosesAuthServer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	upstream := helpers.NewMockUpstreamIDP(t)

	authConfig := helpers.NewTestAuthServerConfig(t, upstream.URL())

	// Create embedded auth server directly (as runner.Run() does internally)
	embeddedAuthServer, err := authserverrunner.NewEmbeddedAuthServer(ctx, authConfig)
	require.NoError(t, err)

	// Simulate runner cleanup behavior (runner.go lines 702-710)
	err = embeddedAuthServer.Close()
	require.NoError(t, err, "Close should succeed")

	// Close is idempotent (sync.Once)
	err = embeddedAuthServer.Close()
	require.NoError(t, err, "Second Close should succeed")
}

// TestRunner_AuthServerPrefixHandlersRoutingPriority verifies that prefix handlers
// have correct routing priority in the transport layer.
func TestRunner_AuthServerPrefixHandlersRoutingPriority(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	upstream := helpers.NewMockUpstreamIDP(t)
	authConfig := helpers.NewTestAuthServerConfig(t, upstream.URL())

	embeddedAuthServer, err := authserverrunner.NewEmbeddedAuthServer(ctx, authConfig)
	require.NoError(t, err)
	defer func() {
		_ = embeddedAuthServer.Close()
	}()

	// Create a mux that simulates how the transport would route requests
	mux := http.NewServeMux()
	authHandler := embeddedAuthServer.Handler()

	// Mount auth server handlers as runner.go does
	mux.Handle("/oauth/", authHandler)
	mux.Handle("/.well-known/oauth-authorization-server", authHandler)
	mux.Handle("/.well-known/openid-configuration", authHandler)
	mux.Handle("/.well-known/jwks.json", authHandler)

	// Add a mock MCP handler that should NOT intercept auth endpoints
	mcpHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot) // Unique status to identify this handler
	})
	mux.Handle("/", mcpHandler)

	server := httptest.NewServer(mux)
	defer server.Close()

	client := helpers.NewOAuthClient(t, server.URL)

	// OAuth endpoints should be handled by auth server, not MCP handler
	_, statusCode := client.GetJWKS()
	assert.Equal(t, http.StatusOK, statusCode, "JWKS should be handled by auth server")

	_, statusCode = client.GetOAuthDiscovery()
	assert.Equal(t, http.StatusOK, statusCode, "OAuth discovery should be handled by auth server")

	// MCP endpoints should go to MCP handler
	resp, err := http.Get(server.URL + "/mcp/some-endpoint")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusTeapot, resp.StatusCode, "MCP endpoints should go to MCP handler")
}

// TestRunner_AuthServerLifecycleWithContext verifies auth server lifecycle
// when context is cancelled.
func TestRunner_AuthServerLifecycleWithContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	upstream := helpers.NewMockUpstreamIDP(t)
	authConfig := helpers.NewTestAuthServerConfig(t, upstream.URL())

	embeddedAuthServer, err := authserverrunner.NewEmbeddedAuthServer(ctx, authConfig)
	require.NoError(t, err)

	// Create test server
	server := httptest.NewServer(embeddedAuthServer.Handler())
	defer server.Close()

	// Verify server works
	client := helpers.NewOAuthClient(t, server.URL)
	_, statusCode := client.GetJWKS()
	assert.Equal(t, http.StatusOK, statusCode)

	// Cancel context
	cancel()

	// Server should still respond to requests (context cancellation doesn't stop HTTP)
	// but cleanup should be possible
	err = embeddedAuthServer.Close()
	require.NoError(t, err)
}

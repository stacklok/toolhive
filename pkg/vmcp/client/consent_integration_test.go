// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpserver "github.com/stacklok/toolhive-core/mcpcompat/server"
	pkgauth "github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// newUpstreamInjectClient builds a backend client whose only outgoing auth
// strategy is upstream_inject for the given provider/authorize URL.
func newUpstreamInjectClient(t *testing.T) vmcp.BackendClient {
	t.Helper()
	registry := auth.NewDefaultOutgoingAuthRegistry()
	require.NoError(t, registry.RegisterStrategy(
		authtypes.StrategyTypeUpstreamInject, strategies.NewUpstreamInjectStrategy()))
	backendClient, err := NewHTTPBackendClient(registry)
	require.NoError(t, err)
	return backendClient
}

// upstreamInjectTarget builds a BackendTarget pointing at srv with an
// upstream_inject auth strategy for the given provider.
func upstreamInjectTarget(baseURL, provider, authorizeURL string) *vmcp.BackendTarget {
	return &vmcp.BackendTarget{
		WorkloadID:    "untrusted-backend",
		WorkloadName:  "Untrusted Backend",
		BaseURL:       baseURL + "/mcp",
		TransportType: "streamable-http",
		AuthConfig: &authtypes.BackendAuthStrategy{
			Type: authtypes.StrategyTypeUpstreamInject,
			UpstreamInject: &authtypes.UpstreamInjectConfig{
				ProviderName: provider,
				AuthorizeURL: authorizeURL,
			},
		},
	}
}

// identityContext returns a context carrying an identity whose upstream-token
// map is exactly tokens.
func identityContext(tokens map[string]string) context.Context {
	return pkgauth.WithIdentity(context.Background(), &pkgauth.Identity{
		PrincipalInfo:  pkgauth.PrincipalInfo{Subject: "user1"},
		UpstreamTokens: tokens,
	})
}

// TestCallTool_ConsentRequired_FailsBeforeDispatch is the Wave 4 fail-fast
// end-to-end: a tool call to an untrusted backend whose upstream provider
// token is absent from the identity must fail with a ConsentRequiredError —
// carrying the provider and authorize URL, still classified as
// ErrAuthenticationFailed for health monitors — BEFORE any backend HTTP
// request is attempted.
func TestCallTool_ConsentRequired_FailsBeforeDispatch(t *testing.T) {
	t.Parallel()

	// Count every HTTP request hitting the "backend": the fail-fast consent
	// check must fire before the initialize request is ever sent.
	var backendRequests atomic.Int64
	mcpServer := mcpserver.NewMCPServer("untrusted-backend", "1.0.0")
	mux := http.NewServeMux()
	mux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendRequests.Add(1)
		mcpserver.NewStreamableHTTPServer(mcpServer).ServeHTTP(w, r)
	}))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	backendClient := newUpstreamInjectClient(t)
	target := upstreamInjectTarget(ts.URL, "github", "https://thv.example.com/oauth/authorize")

	ctx, cancel := context.WithTimeout(identityContext(map[string]string{}), 10*time.Second)
	defer cancel()

	result, err := backendClient.CallTool(ctx, target, "create_issue", map[string]any{}, nil)

	require.Error(t, err)
	assert.Nil(t, result)

	// Health-monitor contract: still classified as authentication failure.
	assert.ErrorIs(t, err, vmcp.ErrAuthenticationFailed)

	// Tool-result rendering contract: typed payload survives the wrap.
	var consentErr *authtypes.ConsentRequiredError
	require.ErrorAs(t, err, &consentErr)
	assert.Equal(t, "github", consentErr.Provider)
	assert.Equal(t, "https://thv.example.com/oauth/authorize", consentErr.AuthorizeURL)

	// Fail-fast: no backend HTTP request may have been attempted.
	assert.Equal(t, int64(0), backendRequests.Load(),
		"consent check must fire before any backend HTTP request")

	// No token material anywhere in the error.
	assert.NotContains(t, err.Error(), "gho_")
}

// TestCallTool_PostConsentRetrySucceeds is the Wave 4 retry end-to-end: after
// consent completes out of band, the next request's identity carries the new
// upstream token (TokenValidator.Middleware reloads upstream tokens on every
// request), so the retried tool call succeeds with no server-side watch, no
// cache invalidation, and no session/pod churn.
func TestCallTool_PostConsentRetrySucceeds(t *testing.T) {
	t.Parallel()

	mcpServer := mcpserver.NewMCPServer("untrusted-backend", "1.0.0")
	mcpServer.AddTool(
		mcp.NewTool("create_issue", mcp.WithDescription("Create an issue")),
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{mcp.NewTextContent("issue created")},
			}, nil
		},
	)
	ts := httptest.NewServer(mcpserver.NewStreamableHTTPServer(mcpServer))
	t.Cleanup(ts.Close)

	backendClient := newUpstreamInjectClient(t)
	target := upstreamInjectTarget(ts.URL, "github", "")

	// Pre-consent: token absent → typed consent error.
	preConsentCtx, cancel := context.WithTimeout(identityContext(map[string]string{}), 10*time.Second)
	defer cancel()
	_, err := backendClient.CallTool(preConsentCtx, target, "create_issue", map[string]any{}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, vmcp.ErrAuthenticationFailed)

	// Post-consent: the retried request carries the freshly loaded token and
	// succeeds against the same client/target — nothing was invalidated.
	postConsentCtx, cancel2 := context.WithTimeout(
		identityContext(map[string]string{"github": "gho_consented"}), 10*time.Second)
	defer cancel2()
	result, err := backendClient.CallTool(postConsentCtx, target, "create_issue", map[string]any{}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError)
}

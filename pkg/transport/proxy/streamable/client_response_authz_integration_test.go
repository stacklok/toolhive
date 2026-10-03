// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package streamable_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/transport/proxy/streamable"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

func unusedTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port
}

// TestStreamableProxyAcceptsClientResponseAfterAuthzDenial exercises the real
// streamable HTTP handler and session ownership checks with the production MCP
// parser and Cedar authorization middleware in front of them. A protected tool
// call must stop at authz, while a valid JSON-RPC response from that same
// session must pass through to the runner's message channel with HTTP 202.
func TestStreamableProxyAcceptsClientResponseAfterAuthzDenial(t *testing.T) {
	t.Parallel()

	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies: []string{
			`permit(principal, action == Action::"call_tool", resource == Tool::"allowed");`,
		},
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err)

	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
		Subject: "test-user",
		Claims: map[string]any{
			"iss": "https://issuer.example",
			"sub": "test-user",
		},
	}}
	identityMiddleware := types.NamedMiddleware{
		Name: "test-identity",
		Function: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), identity)))
			})
		},
	}
	parserMiddleware := types.NamedMiddleware{
		Name:     "mcp-parser",
		Function: mcpparser.ParsingMiddleware,
	}
	authorizationMiddleware := types.NamedMiddleware{
		Name: "cedar-authz",
		Function: func(next http.Handler) http.Handler {
			return authz.Middleware(authorizer, next, nil)
		},
	}

	port := unusedTCPPort(t)
	proxy := streamable.NewHTTPProxy("127.0.0.1", port, nil,
		[]types.NamedMiddleware{identityMiddleware, parserMiddleware, authorizationMiddleware},
		streamable.WithStandaloneSSE(false),
	)
	ctx := t.Context()
	require.NoError(t, proxy.Start(ctx))
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		assert.NoError(t, proxy.Stop(stopCtx))
	})

	// Complete a real initialize round-trip so the later response is attached
	// to a live, identity-owned streamable session.
	backendResult := make(chan error, 1)
	go func() {
		select {
		case msg := <-proxy.GetMessageChannel():
			req, ok := msg.(*jsonrpc2.Request)
			if !ok {
				backendResult <- fmt.Errorf("initialize reached runner as %T, want *jsonrpc2.Request", msg)
				return
			}
			if req.Method != "initialize" {
				backendResult <- fmt.Errorf("first runner request method = %q, want initialize", req.Method)
				return
			}
			response, responseErr := jsonrpc2.NewResponse(req.ID, map[string]any{
				"protocolVersion": "2025-11-25",
				"capabilities":    map[string]any{},
				"serverInfo": map[string]any{
					"name":    "integration-test",
					"version": "1",
				},
			}, nil)
			if responseErr != nil {
				backendResult <- responseErr
				return
			}
			backendResult <- proxy.ForwardResponseToClients(ctx, response)
		case <-ctx.Done():
			backendResult <- ctx.Err()
		}
	}()

	proxyURL := fmt.Sprintf("http://127.0.0.1:%d%s", port, streamable.StreamableHTTPEndpoint)
	client := &http.Client{Timeout: 5 * time.Second}
	require.Eventually(t, func() bool {
		resp, requestErr := client.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
		if requestErr != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 5*time.Second, 10*time.Millisecond, "streamable proxy did not become ready")
	post := func(body, sessionID string) *http.Response {
		t.Helper()
		req, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, proxyURL, strings.NewReader(body))
		require.NoError(t, requestErr)
		req.Header.Set("Content-Type", "application/json")
		if sessionID != "" {
			req.Header.Set("Mcp-Session-Id", sessionID)
		}
		resp, requestErr := client.Do(req)
		require.NoError(t, requestErr)
		return resp
	}

	initialize := `{"jsonrpc":"2.0","id":"initialize-1","method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"integration-test","version":"1"}}}`
	initializeResponse := post(initialize, "")
	initializeBody, err := io.ReadAll(initializeResponse.Body)
	require.NoError(t, err)
	require.NoError(t, initializeResponse.Body.Close())
	require.Equal(t, http.StatusOK, initializeResponse.StatusCode, string(initializeBody))
	require.NoError(t, <-backendResult)
	sessionID := initializeResponse.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID, "initialize must create a session for the response POST")

	protectedCall := `{"jsonrpc":"2.0","id":"denied-call","method":"tools/call","params":{"name":"protected","arguments":{}}}`
	deniedResponse := post(protectedCall, sessionID)
	deniedBody, err := io.ReadAll(deniedResponse.Body)
	require.NoError(t, err)
	require.NoError(t, deniedResponse.Body.Close())
	assert.Equal(t, http.StatusForbidden, deniedResponse.StatusCode, string(deniedBody))
	assert.Contains(t, string(deniedBody), "Unauthorized")
	select {
	case msg := <-proxy.GetMessageChannel():
		t.Fatalf("Cedar-denied tools/call reached runner message channel: %#v", msg)
	default:
	}

	clientResponse := `{"jsonrpc":"2.0","id":"client-response-1","result":{"roots":[]}}`
	acceptedResponse := post(clientResponse, sessionID)
	acceptedBody, err := io.ReadAll(acceptedResponse.Body)
	require.NoError(t, err)
	require.NoError(t, acceptedResponse.Body.Close())
	assert.Equal(t, http.StatusAccepted, acceptedResponse.StatusCode, string(acceptedBody))

	select {
	case msg := <-proxy.GetMessageChannel():
		response, ok := msg.(*jsonrpc2.Response)
		require.True(t, ok, "client response reached runner as %T", msg)
		assert.Equal(t, "client-response-1", response.ID.Raw())
		assert.NoError(t, response.Error)
		assert.JSONEq(t, `{"roots":[]}`, string(response.Result))
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for client JSON-RPC response on runner message channel")
	}
}

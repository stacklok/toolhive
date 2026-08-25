// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
)

// TestResponseFilteringWriter_Non2xxStatusPreservedOnWire reproduces the
// production transparent-proxy wiring (real HTTP server + httputil.ReverseProxy
// with FlushInterval:-1 + ResponseFilteringWriter) and asserts that a non-2xx
// backend status survives on the wire. Regression for the bypass where Flush()
// committed an implicit 200 before FlushAndFilter() ran, so the non-2xx
// passthrough branch (safe only when the client actually observes a non-2xx
// status) delivered an unfiltered list body under a fabricated 200.
func TestResponseFilteringWriter_Non2xxStatusPreservedOnWire(t *testing.T) {
	t.Parallel()

	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies:     []string{`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`},
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err)

	backendResult := mcp.ListToolsResult{Tools: []mcp.Tool{
		{Name: "weather", Description: "Get weather information"},
		{Name: "calculator", Description: "Perform calculations"},
		{Name: "admin_tool", Description: "Administrative operations"},
	}}
	resultData, err := json.Marshal(backendResult)
	require.NoError(t, err)
	backendRPCResponse := &jsonrpc2.Response{ID: jsonrpc2.Int64ID(1), Result: json.RawMessage(resultData)}
	backendBody, err := jsonrpc2.EncodeMessage(backendRPCResponse)
	require.NoError(t, err)

	// Backend returns the same list body with a caller-chosen status code.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code := http.StatusOK
		if v := r.URL.Query().Get("code"); v != "" {
			fmt.Sscanf(v, "%d", &code)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write(backendBody)
	}))
	defer backend.Close()
	backendURL, _ := url.Parse(backend.URL)

	// Frontend mirrors the authz-middleware + transparent-proxy wiring.
	frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
			Subject: "user123", Name: "Test User",
			Claims: jwt.MapClaims{"sub": "user123", "name": "Test User"},
		}}
		ctx := auth.WithIdentity(r.Context(), identity)
		parsed := &mcpparser.ParsedMCPRequest{Method: string(mcp.MethodToolsList), ID: float64(1)}
		ctx = context.WithValue(ctx, mcpparser.MCPRequestContextKey, parsed)
		r = r.WithContext(ctx)

		filteringWriter := NewResponseFilteringWriter(w, authorizer, r, string(mcp.MethodToolsList), nil, nil)
		proxy := httputil.NewSingleHostReverseProxy(backendURL)
		proxy.FlushInterval = -1 // production transparent proxy
		proxy.ServeHTTP(filteringWriter, r)
		require.NoError(t, filteringWriter.FlushAndFilter())
	}))
	defer frontend.Close()

	do := func(query string) (*http.Response, []byte) {
		resp, err := http.Get(frontend.URL + "/mcp" + query)
		require.NoError(t, err)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, body
	}

	// 2xx list responses must still be filtered (admin_tool removed).
	resp200, body200 := do("")
	assert.Equal(t, http.StatusOK, resp200.StatusCode)
	assert.Contains(t, string(body200), "weather")
	assert.NotContains(t, string(body200), "admin_tool")

	// Non-2xx list responses must reach the client with the non-2xx status, so
	// the passthrough precondition (client gates list delivery on response.ok)
	// holds.
	for _, code := range []int{http.StatusInternalServerError, http.StatusNotFound} {
		resp, body := do(fmt.Sprintf("?code=%d", code))
		assert.Equalf(t, code, resp.StatusCode,
			"non-2xx backend status %d was rewritten on the wire; the unfiltered list body would be delivered as 200", code)
		// The passthrough branch is intentionally body-preserving for error
		// responses; the security property is that the client sees the non-2xx
		// status and does not deliver the body.
		assert.Equal(t, string(backendBody), string(body))
	}
}

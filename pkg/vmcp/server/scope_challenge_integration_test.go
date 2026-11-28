// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
)

func TestScopeChallengeMixedClientsHTTPIntegration(t *testing.T) {
	t.Parallel()
	var backendCalls atomic.Int32
	backendURL := startEchoPingBackend(t, func() { backendCalls.Add(1) })
	known := server.ScopeClientProfile{Name: "compat-fixture", Version: "1.2.3"}
	unknown := server.ScopeClientProfile{Name: "standard-fixture", Version: "1.2.3"}
	// These headers model an already validated identity. This test exercises
	// transport behavior and current-request grants, not token verification or UI.
	authMw := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal := r.Header.Get("X-Test-Principal")
			if principal == "" {
				principal = "user-123"
			}
			// Encode fixture claims as JSON so malformed claim types can also be tested.
			var scopeClaim any
			if encoded := r.Header.Get("X-Test-Scope"); encoded != "" {
				if err := json.Unmarshal([]byte(encoded), &scopeClaim); err != nil {
					http.Error(w, "invalid fixture scope claim", http.StatusBadRequest)
					return
				}
			}
			identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: principal, Claims: map[string]any{
				"iss": "https://issuer.example.com", "sub": principal, "scope": scopeClaim,
			}}}
			next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), identity)))
		})
	}
	ts := buildCedarAuthzServerWithConfig(t, backendURL, nil, nil, authMw, func(cfg *server.Config) {
		cfg.ScopeAuthorization = &vmcp.ScopeAuthorizationConfig{Tools: map[string]vmcp.OAuthScopeSet{"echo": {"mcp.write"}}}
		cfg.ScopeChallenge = &server.ScopeChallengeConfig{
			ResourceURL: "https://gateway.example/mcp", ResponseMode: "auto", ToolResultClients: []server.ScopeClientProfile{known},
		}
		cfg.AuthInfoHandler = auth.NewAuthInfoHandler("https://issuer.example.com", cfg.ScopeChallenge.ResourceURL, nil)
	})
	client := &http.Client{Timeout: 10 * time.Second}
	request := func(t *testing.T, method string, params map[string]any, sessionID string, scope any, principal string, modern bool) (int, http.Header, string) {
		t.Helper()
		body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 19, "method": method, "params": params})
		require.NoError(t, err)
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/mcp", bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		encodedScope, err := json.Marshal(scope)
		require.NoError(t, err)
		req.Header.Set("X-Test-Scope", string(encodedScope))
		req.Header.Set("X-Test-Principal", principal)
		if modern {
			req.Header.Set("MCP-Protocol-Version", mcpparser.MCPVersionModern)
			req.Header.Set("Mcp-Method", method)
			if name, ok := params["name"].(string); ok {
				req.Header.Set("Mcp-Name", name)
			}
		} else if sessionID != "" {
			req.Header.Set("Mcp-Session-Id", sessionID)
		}
		resp, err := client.Do(req)
		require.NoError(t, err)
		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		return resp.StatusCode, resp.Header, string(raw)
	}
	initialize := func(profile server.ScopeClientProfile) string {
		t.Helper()
		status, headers, body := request(t, "initialize", map[string]any{
			"protocolVersion": mcpparser.MCPVersionLegacy, "capabilities": map[string]any{},
			"clientInfo": map[string]any{"name": profile.Name, "version": profile.Version},
		}, "", "", "", false)
		require.Equal(t, http.StatusOK, status, body)
		id := headers.Get("Mcp-Session-Id")
		require.NotEmpty(t, id)
		t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(cleanupCtx, http.MethodDelete, ts.URL+"/mcp", nil)
			require.NoError(t, err)
			req.Header.Set("Mcp-Session-Id", id)
			resp, err := client.Do(req)
			require.NoError(t, err)
			_, _ = io.Copy(io.Discard, resp.Body)
			require.NoError(t, resp.Body.Close())
		})
		return id
	}
	knownSession, unknownSession := initialize(known), initialize(unknown)
	callParams := func(profile *server.ScopeClientProfile, modern bool) map[string]any {
		params := map[string]any{"name": "echo", "arguments": map[string]any{"input": "scope-success"}}
		if modern {
			meta := map[string]any{"io.modelcontextprotocol/protocolVersion": mcpparser.MCPVersionModern,
				"io.modelcontextprotocol/clientCapabilities": map[string]any{}}
			if profile != nil {
				meta["io.modelcontextprotocol/clientInfo"] = map[string]any{"name": profile.Name, "version": profile.Version}
			}
			params["_meta"] = meta
		}
		return params
	}
	assertRefusal := func(t *testing.T, status int, headers http.Header, body string, toolResult bool) {
		t.Helper()
		if toolResult {
			require.Equal(t, http.StatusOK, status, body)
			require.Empty(t, headers.Get("WWW-Authenticate"))
			require.Contains(t, body, `"isError":true`)
			require.Contains(t, body, "mcp/www_authenticate")
		} else {
			require.Equal(t, http.StatusForbidden, status, body)
			require.Contains(t, headers.Get("WWW-Authenticate"), `scope="mcp.write"`)
		}
		require.Contains(t, headers.Get("Cache-Control"), "no-store")
	}
	for _, id := range []string{knownSession, unknownSession} {
		status, _, body := request(t, "tools/list", map[string]any{}, id, "", "", false)
		require.Equal(t, http.StatusOK, status, body)
		require.Contains(t, body, `"echo"`, "missing upgradeable scopes must not hide the tool")
		require.Contains(t, body, "securitySchemes", "discovery is independent of the client's encoding")
	}
	// Modern discovery must annotate mapped tools at the wire boundary, even
	// before the caller has the required grants, without annotating other tools.
	for _, profile := range []server.ScopeClientProfile{known, unknown} {
		status, _, body := request(t, "tools/list", map[string]any{"_meta": map[string]any{
			"io.modelcontextprotocol/protocolVersion":    mcpparser.MCPVersionModern,
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
			"io.modelcontextprotocol/clientInfo": map[string]any{
				"name": profile.Name, "version": profile.Version,
			},
		}}, "", "", "", true)
		require.Equal(t, http.StatusOK, status, body)
		var envelope struct {
			Result struct {
				Tools []struct {
					Name string                     `json:"name"`
					Meta map[string]json.RawMessage `json:"_meta"`
				} `json:"tools"`
			} `json:"result"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &envelope))
		require.Len(t, envelope.Result.Tools, 2)
		var names []string
		for _, tool := range envelope.Result.Tools {
			names = append(names, tool.Name)
			if tool.Name == "echo" {
				var schemes []struct {
					Type   string   `json:"type"`
					Scopes []string `json:"scopes"`
				}
				require.NoError(t, json.Unmarshal(tool.Meta["securitySchemes"], &schemes))
				require.Len(t, schemes, 1)
				require.Equal(t, "oauth2", schemes[0].Type)
				require.Equal(t, []string{"mcp.write"}, schemes[0].Scopes)
			} else {
				require.Equal(t, "ping", tool.Name)
				require.NotContains(t, tool.Meta, "securitySchemes")
			}
		}
		require.ElementsMatch(t, []string{"echo", "ping"}, names)
	}
	// No Cedar policy is configured: ordinary scope-claim failures must still
	// be HTTP 403, including for a client that uses tool-result scope upgrades.
	for _, id := range []string{knownSession, unknownSession} {
		status, headers, body := request(t, "tools/call", callParams(nil, false), id, 17, "", false)
		require.Equal(t, http.StatusForbidden, status, body)
		require.Contains(t, body, vmcp.DenyMessageToolCall)
		require.Empty(t, headers.Get("WWW-Authenticate"))
		require.NotContains(t, body, "mcp/www_authenticate")
	}
	require.Zero(t, backendCalls.Load(), "scope refusals must not execute backend tools")
	for _, tc := range []struct {
		name         string
		profile      *server.ScopeClientProfile
		id           string
		modern, tool bool
	}{
		{"legacy known", nil, knownSession, false, true}, {"legacy unknown", nil, unknownSession, false, false},
		{"modern known", &known, "", true, true}, {"modern unknown", &unknown, "", true, false},
		{"modern absent", nil, "", true, false}, {"modern known again", &known, "", true, true},
	} {
		t.Logf("Current grants: %s", tc.name)
		before := backendCalls.Load()
		status, headers, body := request(t, "tools/call", callParams(tc.profile, tc.modern), tc.id, "", "", tc.modern)
		assertRefusal(t, status, headers, body, tc.tool)
		require.Equal(t, before, backendCalls.Load())
		status, _, body = request(t, "tools/call", callParams(tc.profile, tc.modern), tc.id, "mcp.write", "", tc.modern)
		require.Equal(t, http.StatusOK, status, body)
		require.Contains(t, body, "scope-success")
		require.Equal(t, before+1, backendCalls.Load())
		status, headers, body = request(t, "tools/call", callParams(tc.profile, tc.modern), tc.id, "", "", tc.modern)
		assertRefusal(t, status, headers, body, tc.tool)
		require.Equal(t, before+1, backendCalls.Load(), "downgraded current grants must prevent another execution")
	}
	for _, tc := range []struct{ id, principal string }{{"unknown-session", ""}, {knownSession, "foreign-user"}} {
		before := backendCalls.Load()
		status, headers, body := request(t, "tools/call", callParams(nil, false), tc.id, "", tc.principal, false)
		require.Equal(t, http.StatusNotFound, status, body)
		require.Empty(t, headers.Get("WWW-Authenticate"))
		require.NotContains(t, body, "mcp/www_authenticate")
		require.Equal(t, before, backendCalls.Load())
	}
	// Run these last: parallel subtests start after the parent returns, once
	// all grant-changing calls above have finished. They share one user and URL.
	before := backendCalls.Load()
	for _, tc := range []struct {
		name, id string
		tool     bool
	}{
		{"known", knownSession, true}, {"unknown", unknownSession, false},
	} {
		t.Run("concurrent Legacy "+tc.name, func(t *testing.T) {
			t.Parallel()
			status, headers, body := request(t, "tools/call", callParams(nil, false), tc.id, "", "", false)
			assertRefusal(t, status, headers, body, tc.tool)
			require.Equal(t, before, backendCalls.Load(), "concurrent refusals must not execute tools")
		})
	}

}

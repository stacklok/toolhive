// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/sessionbinding"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/server/sessionmanager"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

const (
	ownershipTestIssuer = "https://issuer.example.com"
	ownershipTestTool   = "owned-tool"

	// Bearer tokens understood by ownershipBearerMiddleware: "<sub>.<version>".
	ownerToken          = "alice.1"
	ownerRefreshedToken = "alice.2"
	foreignToken        = "mallory.1"
	foreignSubject      = "mallory"
)

// ownershipBearerMiddleware is an OIDC-like authentication stand-in: the bearer
// token "<sub>.<version>" authenticates subject <sub> at ownershipTestIssuer (or
// at the issuer named by X-Test-Issuer), and the version lets a test present a
// refreshed token for the same principal. Requests without a token get 401, as
// with a real token validator.
func ownershipBearerMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		sub, _, _ := strings.Cut(token, ".")
		iss := ownershipTestIssuer
		if v := r.Header.Get("X-Test-Issuer"); v != "" {
			iss = v
		}
		identity := &auth.Identity{
			PrincipalInfo: auth.PrincipalInfo{
				Subject: sub,
				Claims:  map[string]any{"iss": iss, "sub": sub},
			},
			Token:     token,
			TokenType: "Bearer",
		}
		next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), identity)))
	})
}

const (
	delegatedOwnerBinding = ownershipTestIssuer + "\x00alice\x00delegated\x00owner-client\x00actor-issuer\x00owner-actor"
	delegatedOwnerToken   = "delegated-owner"
)

func delegatedOwnershipIdentity(token, clientID, actorIssuer, actorSubject string) *auth.Identity {
	claims := map[string]any{
		"iss": ownershipTestIssuer, "sub": "alice", "client_id": clientID,
		"jti": "original", "exp": int64(2_000_000_000),
		"authorization_details": []any{map[string]any{"type": "mcp", "actions": []any{"list"}}},
	}
	if actorIssuer != "" || actorSubject != "" {
		claims["act"] = map[string]any{"iss": actorIssuer, "sub": actorSubject}
	}
	return &auth.Identity{
		PrincipalInfo:  auth.PrincipalInfo{Subject: "alice", Claims: claims},
		AdmissionRoute: auth.AdmissionRouteDelegated,
		Token:          token,
	}
}

func delegatedOwnershipIdentities() map[string]*auth.Identity {
	renewed := delegatedOwnershipIdentity("renewed", "owner-client", "actor-issuer", "owner-actor")
	renewed.Claims["jti"] = "renewed"
	renewed.Claims["exp"] = int64(2_000_003_600)
	renewed.Claims["authorization_details"] = []any{map[string]any{"type": "mcp", "actions": []any{"list", "call"}}}
	return map[string]*auth.Identity{
		delegatedOwnerToken:  delegatedOwnershipIdentity(delegatedOwnerToken, "owner-client", "actor-issuer", "owner-actor"),
		"renewed":            renewed,
		"other-client":       delegatedOwnershipIdentity("other-client", "foreign-client", "actor-issuer", "owner-actor"),
		"other-actor-issuer": delegatedOwnershipIdentity("other-actor-issuer", "owner-client", "other-issuer", "owner-actor"),
		"other-actor":        delegatedOwnershipIdentity("other-actor", "owner-client", "actor-issuer", "foreign-actor"),
		"interactive": {
			PrincipalInfo:  auth.PrincipalInfo{Subject: "alice", Claims: map[string]any{"iss": ownershipTestIssuer, "sub": "alice"}},
			AdmissionRoute: auth.AdmissionRouteInteractive,
			Token:          "interactive",
		},
	}
}

func delegatedOwnershipMiddleware(next http.Handler) http.Handler {
	identities := delegatedOwnershipIdentities()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		identity := identities[token]
		if identity == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), identity)))
	})
}

// newOwnershipTestServer starts a Serve-built vMCP server over the real session
// factory, so sessions carry the real persisted identity binding. A nil
// authMiddleware leaves authentication disabled. A non-empty redisAddr makes the
// server use shared Redis session storage, as replicas do.
func newOwnershipTestServer(
	t *testing.T, authMiddleware func(http.Handler) http.Handler, redisAddr string,
) string {
	t.Helper()
	baseURL, _ := newOwnershipTestServerWithCore(t, authMiddleware, redisAddr)
	return baseURL
}

// newOwnershipTestServerWithCore is newOwnershipTestServer that also returns the
// fakeCore behind the server, so a test can inspect what the core received.
func newOwnershipTestServerWithCore(
	t *testing.T, authMiddleware func(http.Handler) http.Handler, redisAddr string,
) (string, *fakeCore) {
	t.Helper()

	authReg := vmcpauth.NewDefaultOutgoingAuthRegistry()
	require.NoError(t, authReg.RegisterStrategy(
		authtypes.StrategyTypeUnauthenticated,
		strategies.NewUnauthenticatedStrategy(),
	))

	cfg := &ServerConfig{
		SessionTTL:           time.Minute,
		AuthMiddleware:       authMiddleware,
		SessionManagerConfig: &sessionmanager.FactoryConfig{Base: vmcpsession.NewSessionFactory(authReg)},
		BackendRegistry:      vmcp.NewImmutableRegistry([]vmcp.Backend{}),
	}
	if redisAddr != "" {
		cfg.SessionStorage = &vmcpconfig.SessionStorageConfig{
			Provider:  "redis",
			Address:   redisAddr,
			KeyPrefix: "test:vmcp:ownership:",
		}
	}

	fc := &fakeCore{
		tools:             []vmcp.Tool{{Name: ownershipTestTool, Description: "ownership anchor"}},
		resources:         []vmcp.Resource{{URI: "file:///owned", Name: "owned"}},
		resourceTemplates: []vmcp.ResourceTemplate{{URITemplate: "file:///owned/{id}", Name: "owned-template"}},
		prompts:           []vmcp.Prompt{{Name: "owned-prompt"}},
		// The foreign caller gets different items, so a response shows whose
		// list it carries.
		subjectLists: map[string]*subjectList{foreignSubject: {
			tools:             []vmcp.Tool{{Name: "foreign-tool", Description: "foreign anchor"}},
			resources:         []vmcp.Resource{{URI: "file:///foreign", Name: "foreign"}},
			resourceTemplates: []vmcp.ResourceTemplate{{URITemplate: "file:///foreign/{id}", Name: "foreign-template"}},
			prompts:           []vmcp.Prompt{{Name: "foreign-prompt"}},
		}},
	}
	srv, err := Serve(context.Background(), fc, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	handler, err := srv.Handler(context.Background())
	require.NoError(t, err)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts.URL, fc
}

// ownershipResponse is the observable outcome of one request.
type ownershipResponse struct {
	status    int
	body      string // empty for an event stream, which is never read
	sessionID string // Mcp-Session-Id response header
}

// ownershipRequest sends one request to /mcp. A nil body sends no body (GET,
// DELETE). An empty token sends no Authorization header. Each session ID is
// added as its own Mcp-Session-Id header, and extra headers are added too.
// An event-stream response is left open until the test ends, so a stream the
// transport accepted keeps occupying the session's standalone stream slot.
func ownershipRequest(
	t *testing.T, baseURL, method, token string, sessionIDs []string, body map[string]any, extra http.Header,
) ownershipResponse {
	t.Helper()

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, method, baseURL+"/mcp", reader)
	require.NoError(t, err)
	switch method {
	case http.MethodGet:
		req.Header.Set("Accept", "text/event-stream")
	case http.MethodPost:
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for _, id := range sessionIDs {
		req.Header.Add("Mcp-Session-Id", id)
	}
	for k, vs := range extra {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	out := ownershipResponse{status: resp.StatusCode, sessionID: resp.Header.Get("Mcp-Session-Id")}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Cleanup(func() { _ = resp.Body.Close() })
		return out
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	out.body = string(raw)
	return out
}

// ownedRequest sends a request on sessionID as the holder of token.
func ownedRequest(t *testing.T, baseURL, method, token, sessionID string, body map[string]any) ownershipResponse {
	t.Helper()
	return ownershipRequest(t, baseURL, method, token, []string{sessionID}, body, nil)
}

// initializeOwnedSession runs the initialize handshake as the holder of token
// (empty for no Authorization header) and returns the session ID.
func initializeOwnedSession(t *testing.T, baseURL, token string) string {
	t.Helper()
	resp := ownershipRequest(t, baseURL, http.MethodPost, token, nil, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-11-25",
			"capabilities":    map[string]any{"elicitation": map[string]any{}},
			"clientInfo":      map[string]any{"name": "ownership-test", "version": "1.0"},
		},
	}, nil)
	require.Equal(t, http.StatusOK, resp.status, "initialize must succeed; body: %s", resp.body)
	require.NotEmpty(t, resp.sessionID)
	return resp.sessionID
}

func jsonRPCRequest(method string, params map[string]any) map[string]any {
	if params == nil {
		params = map[string]any{}
	}
	return map[string]any{"jsonrpc": "2.0", "id": 7, "method": method, "params": params}
}

// listCase describes one list method: where its items sit in the result, which
// field names an item, and the item the owner and the foreign caller each see.
type listCase struct {
	method, resultKey, field, ownedItem, foreignItem string
}

var listCases = []listCase{
	{"tools/list", "tools", "name", ownershipTestTool, "foreign-tool"},
	{"resources/list", "resources", "uri", "file:///owned", "file:///foreign"},
	{"resources/templates/list", "resourceTemplates", "uriTemplate", "file:///owned/{id}", "file:///foreign/{id}"},
	{"prompts/list", "prompts", "name", "owned-prompt", "foreign-prompt"},
}

// listedItems decodes a JSON-RPC list response, requires that it carries no
// error, and returns the field of each item under the case's result key.
func listedItems(t *testing.T, body string, tc listCase) []string {
	t.Helper()
	var env struct {
		Result map[string]json.RawMessage `json:"result"`
		Error  json.RawMessage            `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &env), "%s; body: %s", tc.method, body)
	require.Empty(t, env.Error, "%s must not return a JSON-RPC error; body: %s", tc.method, body)
	var items []map[string]any
	require.NoError(t, json.Unmarshal(env.Result[tc.resultKey], &items), "%s; body: %s", tc.method, body)
	var out []string
	for _, item := range items {
		if v, ok := item[tc.field].(string); ok {
			out = append(out, v)
		}
	}
	return out
}

// requireOwnerSessionIntact asserts that the owner can still list tools on the
// session and open its standalone stream (a captured stream would make it 409).
func requireOwnerSessionIntact(t *testing.T, baseURL, sessionID string) {
	t.Helper()
	list := ownedRequest(t, baseURL, http.MethodPost, ownerToken, sessionID, jsonRPCRequest("tools/list", nil))
	require.Equal(t, http.StatusOK, list.status, "owner's session must stay usable; body: %s", list.body)
	assert.Contains(t, list.body, ownershipTestTool)
	stream := ownedRequest(t, baseURL, http.MethodGet, ownerToken, sessionID, nil)
	assert.Equal(t, http.StatusOK, stream.status,
		"owner must still be able to open the standalone stream; body: %s", stream.body)
}

// TestSessionOwnership_ForeignCallerRejectedOnEveryRoute pins that a session is
// usable only by the principal that created it, on every route that carries
// Mcp-Session-Id, and that a rejected foreign request neither discloses whether
// the session exists nor disturbs the owner's session.
func TestSessionOwnership_ForeignCallerRejectedOnEveryRoute(t *testing.T) {
	t.Parallel()

	baseURL := newOwnershipTestServer(t, ownershipBearerMiddleware, "")

	// The reference rejection for each case is the same request on an unknown
	// session ID. Every foreign rejection must match it byte for byte, so a 404
	// cannot be used to confirm that a session exists.
	const unknownSessionID = "00000000-0000-0000-0000-000000000000"

	cases := []struct {
		name   string
		method string
		token  string
		body   map[string]any
		header http.Header
	}{
		{name: "standalone GET stream", method: http.MethodGet},
		{name: "DELETE", method: http.MethodDelete},
		{name: "tools/list", method: http.MethodPost, body: jsonRPCRequest("tools/list", nil)},
		{name: "resources/list", method: http.MethodPost, body: jsonRPCRequest("resources/list", nil)},
		{name: "resources/templates/list", method: http.MethodPost, body: jsonRPCRequest("resources/templates/list", nil)},
		{name: "prompts/list", method: http.MethodPost, body: jsonRPCRequest("prompts/list", nil)},
		{name: "ping", method: http.MethodPost, body: jsonRPCRequest("ping", nil)},
		{
			name:   "client response to a server request",
			method: http.MethodPost,
			body: map[string]any{
				"jsonrpc": "2.0", "id": 1,
				"result": map[string]any{"action": "accept", "content": map[string]any{"approved": true}},
			},
		},
		{
			name:   "client notification",
			method: http.MethodPost,
			body: map[string]any{
				"jsonrpc": "2.0", "method": "notifications/cancelled",
				"params": map[string]any{"requestId": 1},
			},
		},
		{
			name:   "tools/call",
			method: http.MethodPost,
			body:   jsonRPCRequest("tools/call", map[string]any{"name": ownershipTestTool, "arguments": map[string]any{}}),
		},
		{
			name:   "resources/read",
			method: http.MethodPost,
			body:   jsonRPCRequest("resources/read", map[string]any{"uri": "file:///owned"}),
		},
		{
			name:   "prompts/get",
			method: http.MethodPost,
			body:   jsonRPCRequest("prompts/get", map[string]any{"name": "owned-prompt"}),
		},
		{
			name:   "same subject at another issuer",
			method: http.MethodPost,
			token:  ownerRefreshedToken,
			body:   jsonRPCRequest("tools/list", nil),
			header: http.Header{"X-Test-Issuer": []string{"https://other-issuer.example.com"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sessionID := initializeOwnedSession(t, baseURL, ownerToken)

			token := tc.token
			if token == "" {
				token = foreignToken
			}
			resp := ownershipRequest(t, baseURL, tc.method, token, []string{sessionID}, tc.body, tc.header)
			assert.Equal(t, http.StatusNotFound, resp.status,
				"a foreign caller must not reach the session; body: %s", resp.body)
			unknown := ownershipRequest(t, baseURL, tc.method, token, []string{unknownSessionID}, tc.body, tc.header)
			require.Equal(t, http.StatusNotFound, unknown.status)
			assert.Equal(t, unknown.body, resp.body,
				"a foreign session must be indistinguishable from an unknown one")
			assert.Equal(t, "Session terminated\n", resp.body,
				"the 404 is plain text, so go-sdk clients re-initialize instead of treating it as a call rejection")

			requireOwnerSessionIntact(t, baseURL, sessionID)
		})
	}
}

func TestSessionOwnership_DelegatedRequests(t *testing.T) {
	t.Parallel()

	baseURL := newOwnershipTestServer(t, delegatedOwnershipMiddleware, "")
	type requestCase struct {
		name, method, owner, caller string
		body                        map[string]any
	}
	tests := []requestCase{
		{"GET stream", http.MethodGet, delegatedOwnerToken, "other-client", nil},
		{"DELETE", http.MethodDelete, delegatedOwnerToken, "other-client", nil},
		{"tools/call", http.MethodPost, delegatedOwnerToken, "other-client",
			jsonRPCRequest("tools/call", map[string]any{"name": ownershipTestTool, "arguments": map[string]any{}})},
	}
	for _, lc := range listCases {
		tests = append(tests, requestCase{
			lc.method, http.MethodPost, delegatedOwnerToken, "other-client", jsonRPCRequest(lc.method, nil),
		})
	}
	listBody := jsonRPCRequest("tools/list", nil)
	for _, pair := range []struct{ owner, caller string }{
		{delegatedOwnerToken, "other-actor"},
		{delegatedOwnerToken, "interactive"},
		{"interactive", delegatedOwnerToken},
	} {
		tests = append(tests, requestCase{
			"tools/list " + pair.caller + " on " + pair.owner, http.MethodPost, pair.owner, pair.caller, listBody,
		})
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sessionID := initializeOwnedSession(t, baseURL, tc.owner)
			resp := ownedRequest(t, baseURL, tc.method, tc.caller, sessionID, tc.body)
			require.Equal(t, http.StatusNotFound, resp.status)
			assert.Equal(t, "Session terminated\n", resp.body)

			list := ownedRequest(t, baseURL, http.MethodPost, tc.owner, sessionID, listBody)
			require.Equal(t, http.StatusOK, list.status, "owner's session must stay usable; body: %s", list.body)
			assert.Contains(t, list.body, ownershipTestTool)
			stream := ownedRequest(t, baseURL, http.MethodGet, tc.owner, sessionID, nil)
			assert.Equal(t, http.StatusOK, stream.status, "owner must still be able to open the stream")
		})
	}
}

func TestSessionOwnership_DelegatedRenewedToken(t *testing.T) {
	t.Parallel()

	baseURL := newOwnershipTestServer(t, delegatedOwnershipMiddleware, "")
	sessionID := initializeOwnedSession(t, baseURL, delegatedOwnerToken)
	list := ownedRequest(t, baseURL, http.MethodPost, "renewed", sessionID, jsonRPCRequest("tools/list", nil))
	require.Equal(t, http.StatusOK, list.status, "body: %s", list.body)
	assert.Contains(t, listedItems(t, list.body, listCases[0]), ownershipTestTool)
}

// TestSessionOwnership_OwnerRequests pins the positive cases: the owner, and the
// owner presenting a refreshed token for the same issuer and subject, keep full
// use of the session.
//
// Each subtest issues its first session-bearing request immediately after
// initialize and requires 200. That guards the invariant the ownership check
// relies on: the SDK shim persists the session's owner (OnRegisterSession,
// Manager.CreateSession) synchronously inside the initialize dispatch, before
// the session ID reaches the client. If a toolhive-core upgrade made that
// registration asynchronous, the first follow-up request would see an
// ownerless record and get 404 here.
func TestSessionOwnership_OwnerRequests(t *testing.T) {
	t.Parallel()

	baseURL := newOwnershipTestServer(t, ownershipBearerMiddleware, "")

	for _, token := range []string{ownerToken, ownerRefreshedToken} {
		t.Run("requests as "+token, func(t *testing.T) {
			t.Parallel()
			sessionID := initializeOwnedSession(t, baseURL, ownerToken)
			for _, lc := range listCases {
				resp := ownedRequest(t, baseURL, http.MethodPost, token, sessionID, jsonRPCRequest(lc.method, nil))
				require.Equal(t, http.StatusOK, resp.status, "%s; body: %s", lc.method, resp.body)
				assert.Contains(t, listedItems(t, resp.body, lc), lc.ownedItem, "%s", lc.method)
			}
			call := ownedRequest(t, baseURL, http.MethodPost, token, sessionID,
				jsonRPCRequest("tools/call", map[string]any{"name": ownershipTestTool, "arguments": map[string]any{}}))
			assert.Equal(t, http.StatusOK, call.status, "tools/call; body: %s", call.body)
			assert.NotContains(t, call.body, `"isError":true`)
			stream := ownedRequest(t, baseURL, http.MethodGet, token, sessionID, nil)
			assert.Equal(t, http.StatusOK, stream.status, "GET stream; body: %s", stream.body)

			del := ownedRequest(t, baseURL, http.MethodDelete, token, sessionID, nil)
			require.Equal(t, http.StatusOK, del.status, "DELETE; body: %s", del.body)
			gone := ownedRequest(t, baseURL, http.MethodPost, ownerToken, sessionID, jsonRPCRequest("tools/list", nil))
			assert.Equal(t, http.StatusNotFound, gone.status, "a terminated session must be gone")
		})
	}
}

// TestSessionOwnership_AmbiguousCarrierRejected pins that a request carrying
// more than one Mcp-Session-Id header is rejected, even when the caller owns
// both sessions, rather than letting a later layer pick one of them.
func TestSessionOwnership_AmbiguousCarrierRejected(t *testing.T) {
	t.Parallel()

	baseURL := newOwnershipTestServer(t, ownershipBearerMiddleware, "")
	first := initializeOwnedSession(t, baseURL, ownerToken)
	second := initializeOwnedSession(t, baseURL, ownerToken)
	resp := ownershipRequest(t, baseURL, http.MethodPost, ownerToken, []string{first, second},
		jsonRPCRequest("tools/list", nil), nil)
	assert.Equal(t, http.StatusNotFound, resp.status, "body: %s", resp.body)
	assert.Equal(t, "Session terminated\n", resp.body)
}

// TestSessionOwnership_ModernRequestsIgnoreSessionID pins that Modern
// (2026-07-28) list requests, which are sessionless and routed by
// classifyingHandler to dispatchModern before the ownership middleware, are
// unaffected by a stray Mcp-Session-Id, even one owned by another principal.
// The core must receive the caller's identity, not the session owner's, the
// response must be the same for a foreign, an unknown and an absent session ID,
// and the owner's session must be undisturbed.
func TestSessionOwnership_ModernRequestsIgnoreSessionID(t *testing.T) {
	t.Parallel()

	for _, lc := range listCases {
		t.Run(lc.method, func(t *testing.T) {
			t.Parallel()
			baseURL, fc := newOwnershipTestServerWithCore(t, ownershipBearerMiddleware, "")
			sessionID := initializeOwnedSession(t, baseURL, ownerToken)
			before := fc.listSubjectsFor(lc.method)

			send := func(sessionIDs []string) ownershipResponse {
				return ownershipRequest(t, baseURL, http.MethodPost, foreignToken, sessionIDs,
					map[string]any{
						"jsonrpc": "2.0", "id": 9, "method": lc.method,
						"params": map[string]any{"_meta": map[string]any{
							"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
							"io.modelcontextprotocol/clientCapabilities": map[string]any{},
						}},
					},
					http.Header{"Mcp-Protocol-Version": []string{"2026-07-28"}, "Mcp-Method": []string{lc.method}})
			}
			resp := send([]string{sessionID})
			require.Equal(t, http.StatusOK, resp.status,
				"a Modern request must not be subject to session ownership; body: %s", resp.body)
			assert.Empty(t, resp.sessionID, "a Modern response must not mint or echo a session")

			items := listedItems(t, resp.body, lc)
			assert.Contains(t, items, lc.foreignItem, "the response must carry the caller's list")
			assert.NotContains(t, items, lc.ownedItem, "the response must not carry the session owner's list")

			after := fc.listSubjectsFor(lc.method)
			require.GreaterOrEqual(t, len(after), len(before))
			assert.Equal(t, []string{foreignSubject}, after[len(before):],
				"the request must make exactly one core call, for the caller and not the session owner")

			// The response must not reveal whether the session ID is live: a foreign,
			// an unknown and an absent ID all produce the same response.
			for name, ids := range map[string][]string{"unknown ID": {"no-such-session"}, "no ID": nil} {
				other := send(ids)
				assert.Equal(t, resp.status, other.status, "%s: status", name)
				assert.Equal(t, resp.body, other.body, "%s: body", name)
				assert.Equal(t, resp.sessionID, other.sessionID, "%s: Mcp-Session-Id", name)
			}

			requireOwnerSessionIntact(t, baseURL, sessionID)
		})
	}
}

// TestSessionOwnership_SharedIdentityModes pins that the shipped incoming-auth
// modes without bearer tokens keep working on every route: with authentication
// disabled sessions bind to the unauthenticated sentinel, and with anonymous or
// local-user authentication they bind to the synthetic toolhive-local principal.
func TestSessionOwnership_SharedIdentityModes(t *testing.T) {
	t.Parallel()

	modes := map[string]func(http.Handler) http.Handler{
		"authentication disabled": nil,
		"anonymous":               auth.AnonymousMiddleware,
		"local user":              auth.LocalUserMiddleware("ownership-test-user"),
	}
	for name, middleware := range modes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			baseURL := newOwnershipTestServer(t, middleware, "")
			sessionID := initializeOwnedSession(t, baseURL, "")

			list := ownedRequest(t, baseURL, http.MethodPost, "", sessionID, jsonRPCRequest("tools/list", nil))
			assert.Equal(t, http.StatusOK, list.status, "body: %s", list.body)
			stream := ownedRequest(t, baseURL, http.MethodGet, "", sessionID, nil)
			assert.Equal(t, http.StatusOK, stream.status, "body: %s", stream.body)
			del := ownedRequest(t, baseURL, http.MethodDelete, "", sessionID, nil)
			assert.Equal(t, http.StatusOK, del.status, "body: %s", del.body)
		})
	}
}

// TestSessionOwnership_CrossReplica pins that ownership is enforced from the
// shared session store on a replica that did not create the session, before the
// session is rehydrated there, and that the owner can still use it there.
func TestSessionOwnership_CrossReplica(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	replicaA := newOwnershipTestServer(t, ownershipBearerMiddleware, mr.Addr())
	replicaB := newOwnershipTestServer(t, ownershipBearerMiddleware, mr.Addr())

	sessionID := initializeOwnedSession(t, replicaA, ownerToken)

	for _, method := range []string{http.MethodGet, http.MethodDelete, http.MethodPost} {
		var body map[string]any
		if method == http.MethodPost {
			body = jsonRPCRequest("tools/list", nil)
		}
		resp := ownedRequest(t, replicaB, method, foreignToken, sessionID, body)
		assert.Equal(t, http.StatusNotFound, resp.status, "foreign %s on another replica; body: %s", method, resp.body)
	}

	onB := ownedRequest(t, replicaB, http.MethodPost, ownerToken, sessionID, jsonRPCRequest("tools/list", nil))
	assert.Equal(t, http.StatusOK, onB.status, "owner must be served by the other replica; body: %s", onB.body)
	requireOwnerSessionIntact(t, replicaA, sessionID)

	// A storage outage is reported as retryable rather than as a missing session.
	mr.Close()
	outage := ownedRequest(t, replicaB, http.MethodPost, ownerToken, sessionID, jsonRPCRequest("tools/list", nil))
	assert.Equal(t, http.StatusServiceUnavailable, outage.status, "body: %s", outage.body)
}

// TestSessionOwnership_NonSessionKeyLooksMissing pins that a session ID naming
// another feature's key under the session prefix, such as a shared optimizer
// embedding, gets the same 404 as a missing session rather than a 503 that
// would tell the caller the key exists. The request leaves the key alone.
func TestSessionOwnership_NonSessionKeyLooksMissing(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	baseURL := newOwnershipTestServer(t, ownershipBearerMiddleware, mr.Addr())
	const otherKey = "optimizer:embedding:0123abcd"
	const vector = "\x00\x00\x80\x3f\x00\x00\x00\x40"
	require.NoError(t, mr.Set("test:vmcp:ownership:"+otherKey, vector))

	for _, method := range []string{http.MethodGet, http.MethodDelete, http.MethodPost} {
		var body map[string]any
		if method == http.MethodPost {
			body = jsonRPCRequest("tools/list", nil)
		}
		resp := ownedRequest(t, baseURL, method, ownerToken, otherKey, body)
		assert.Equal(t, http.StatusNotFound, resp.status, "%s naming a non-session key; body: %s", method, resp.body)
	}

	stored, err := mr.Get("test:vmcp:ownership:" + otherKey)
	require.NoError(t, err)
	assert.Equal(t, vector, stored, "the key must be left as it was")
}

// ownerLookupStub is a SessionManager whose LookupOwner returns a fixed result.
type ownerLookupStub struct {
	stubSessionManager
	owner string
	err   error
}

func (s *ownerLookupStub) LookupOwner(context.Context, string) (string, error) { return s.owner, s.err }

// TestSessionOwnershipMiddleware_Decisions pins the middleware's decision for
// each kind of stored owner and caller identity, including identities that
// must never be treated as unauthenticated, and the logging of lookups the
// client abandons. It also asserts the reason attribute logged for each
// rejection.
//
//nolint:paralleltest // installs a global slog default to observe log levels; must not run in parallel
func TestSessionOwnershipMiddleware_Decisions(t *testing.T) {
	const bound = ownershipTestIssuer + "\x00alice"
	claims := func(kv ...string) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	identity := func(token string, c map[string]any) *auth.Identity {
		return &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Claims: c}, Token: token}
	}

	identities := delegatedOwnershipIdentities()
	missingClient := delegatedOwnershipIdentity("missing-client", "", "actor-issuer", "owner-actor")
	delete(missingClient.Claims, "client_id")
	nonObjectActor := delegatedOwnershipIdentity("bad-act", "owner-client", "", "")
	nonObjectActor.Claims["act"] = "malformed"

	tests := []struct {
		name       string
		noSession  bool
		owner      string
		lookupErr  error
		identity   *auth.Identity
		cancelled  bool
		wantStatus int
		// wantReason is the reason attribute of the WARN for a caller that failed
		// the ownership check; empty means no such WARN.
		wantReason string
	}{
		{name: "sessionless request passes", noSession: true, wantStatus: http.StatusOK},
		{name: "owner", owner: bound, identity: identity("t1", claims("iss", ownershipTestIssuer, "sub", "alice")),
			wantStatus: http.StatusOK},
		{name: "owner with a refreshed token", owner: bound,
			identity: identity("t2", claims("iss", ownershipTestIssuer, "sub", "alice")), wantStatus: http.StatusOK},
		{name: "placeholder without an owner", owner: "", identity: nil, wantStatus: http.StatusNotFound},
		{name: "unauthenticated session, no identity", owner: sessionbinding.UnauthenticatedSentinel,
			wantStatus: http.StatusOK},
		{name: "unauthenticated session, token-less identity with issuer and subject",
			owner:    sessionbinding.UnauthenticatedSentinel,
			identity: identity("", claims("iss", ownershipTestIssuer, "sub", "bob")), wantStatus: http.StatusNotFound, wantReason: "principal_mismatch"},
		{name: "unauthenticated session, token-less identity with only a subject",
			owner: sessionbinding.UnauthenticatedSentinel, identity: identity("", claims("sub", "bob")),
			wantStatus: http.StatusNotFound, wantReason: "caller_identity_malformed"},
		{name: "unauthenticated session, token presented", owner: sessionbinding.UnauthenticatedSentinel,
			identity: identity("t1", claims("iss", ownershipTestIssuer, "sub", "bob")), wantStatus: http.StatusNotFound, wantReason: "principal_mismatch"},
		{name: "bound session, malformed identity", owner: bound, identity: identity("t1", claims("sub", "alice")),
			wantStatus: http.StatusNotFound, wantReason: "caller_identity_malformed"},
		{name: "bound session, no identity", owner: bound, wantStatus: http.StatusNotFound, wantReason: "nil_caller"},
		{name: "corrupt stored owner", owner: "alice", identity: identity("t1", claims("iss", ownershipTestIssuer, "sub", "alice")),
			wantStatus: http.StatusNotFound, wantReason: "stored_binding_invalid"},
		{name: "corrupt stored owner, no identity", owner: "alice", wantStatus: http.StatusNotFound,
			wantReason: "stored_binding_invalid"},
		{name: "delegated caller with another client", owner: delegatedOwnerBinding, identity: identities["other-client"],
			wantStatus: http.StatusNotFound, wantReason: "principal_mismatch"},
		{name: "delegated caller on interactive session", owner: bound, identity: identities[delegatedOwnerToken],
			wantStatus: http.StatusNotFound, wantReason: "principal_mismatch"},
		{name: "delegated caller missing client", owner: delegatedOwnerBinding, identity: missingClient,
			wantStatus: http.StatusNotFound, wantReason: "caller_identity_malformed"},
		{name: "delegated caller with non-object actor", owner: delegatedOwnerBinding, identity: nonObjectActor,
			wantStatus: http.StatusNotFound, wantReason: "caller_identity_malformed"},
		{name: "unknown session", lookupErr: sessionbinding.ErrNotFound, wantStatus: http.StatusNotFound},
		{name: "storage failure", lookupErr: errors.New("redis down"), wantStatus: http.StatusServiceUnavailable},
		{name: "storage failure after the client went away", lookupErr: context.Canceled, cancelled: true,
			wantStatus: http.StatusServiceUnavailable},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var logs logSyncBuffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
			t.Cleanup(func() { slog.SetDefault(previous) })

			srv := &Server{vmcpSessionMgr: &ownerLookupStub{owner: tc.owner, err: tc.lookupErr}}
			reached := false
			handler := srv.sessionOwnershipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			}))

			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			if tc.identity != nil {
				ctx = auth.WithIdentity(ctx, tc.identity)
			}
			if tc.cancelled {
				cancel()
			}
			req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/mcp", nil)
			if !tc.noSession {
				req.Header.Set("Mcp-Session-Id", "sess-1")
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, tc.wantStatus, rec.Code, "body: %s", rec.Body.String())
			assert.Equal(t, tc.wantStatus == http.StatusOK, reached, "only an owned or sessionless request may reach the transport")
			assertOwnershipLogPrivacy(t, logs.String(), "sess-1", ownershipPrivateValues...)
			if tc.wantReason == "" {
				assert.NotContains(t, logs.String(), "reason=", "only a rejected caller is logged with a reason")
			} else {
				assert.Contains(t, logs.String(), "reason="+tc.wantReason)
				assert.Equal(t, 1, strings.Count(logs.String(), "reason="), "only one rejection reason should be logged")
				assert.Contains(t, logs.String(), "level=WARN")
				if tc.identity != nil {
					assert.Contains(t, logs.String(), "token_present="+strconv.FormatBool(tc.identity.Token != ""))
				}
			}
			if tc.cancelled {
				assert.NotContains(t, logs.String(), "level=WARN", "an abandoned lookup is not an operator problem")
			}
		})
	}
}

// ownershipPrivateValues are the subjects, client IDs and actor subjects used
// by the decision cases; none may appear in the logs.
var ownershipPrivateValues = []string{"alice", "bob", "owner-client", "foreign-client", "owner-actor", "foreign-actor"}

func assertOwnershipLogPrivacy(t *testing.T, logs, sessionID string, private ...string) {
	t.Helper()
	assert.NotContains(t, logs, sessionID, "the session ID must not be logged")
	for _, key := range []string{"sub=", "client_id=", "act.sub="} {
		assert.NotContains(t, logs, key)
	}
	for _, value := range private {
		assert.NotContains(t, logs, value, "private values must not be logged")
	}
}

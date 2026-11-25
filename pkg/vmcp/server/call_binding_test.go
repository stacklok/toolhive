// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/mcp/toolcalldetails"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	"github.com/stacklok/toolhive/pkg/vmcp/callbinding"
	"github.com/stacklok/toolhive/pkg/vmcp/codemode"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
	"github.com/stacklok/toolhive/pkg/vmcp/server/sessionmanager"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

const (
	cbIssuer    = "https://as.example.com"
	cbClient    = "mecatl"
	cbServer    = "github"
	cbTool      = "github_read"
	cbOtherTool = "other_read"
	cbComposite = "composite_flow"
)

// cbTokens is the token registry of one test server: a bearer token string
// authenticates as the claims stored under it. The stand-in validator checks
// nothing else, exp included, as a validator with leeway would not.
type cbTokens struct {
	mu     sync.Mutex
	claims map[string]map[string]any
}

func (c *cbTokens) add(token string, claims map[string]any) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.claims[token] = claims
	return token
}

func (c *cbTokens) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		c.mu.Lock()
		claims, ok := c.claims[token]
		c.mu.Unlock()
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		sub, _ := claims["sub"].(string)
		identity := &auth.Identity{
			PrincipalInfo: auth.PrincipalInfo{Subject: sub, Claims: claims},
			Token:         token,
			TokenType:     "Bearer",
		}
		next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), identity)))
	})
}

// cbClock is a settable clock for the call binding, installed through
// cbServerOptions.clock.
type cbClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *cbClock) Set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

func (c *cbClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *cbClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// blockingCore holds each CallTool until the test releases it, so a test can
// act while a call is open. Register unblock with t.Cleanup after the test
// server, so it runs before the server waits for open requests.
type blockingCore struct {
	*fakeCore
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingCore() *blockingCore {
	return &blockingCore{started: make(chan struct{}, 1), release: make(chan struct{})}
}

func (b *blockingCore) unblock() { b.once.Do(func() { close(b.release) }) }

func (b *blockingCore) CallTool(
	ctx context.Context, identity *auth.Identity, name string, args map[string]any, meta map[string]any,
) (*vmcp.ToolCallResult, error) {
	select {
	case b.started <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return b.fakeCore.CallTool(ctx, identity, name, args, meta)
}

// waitStarted waits until the blocked core has started a call.
func (b *blockingCore) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-b.started:
	case <-time.After(10 * time.Second):
		require.FailNow(t, "the call did not start")
	}
}

// lookupErrCore fails LookupTool with an infrastructure error until cleared.
type lookupErrCore struct {
	*fakeCore
	mu  sync.Mutex
	err error
}

func (l *lookupErrCore) LookupTool(ctx context.Context, identity *auth.Identity, name string) (*vmcp.Tool, error) {
	l.mu.Lock()
	err := l.err
	l.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return l.fakeCore.LookupTool(ctx, identity, name)
}

type cbServerOptions struct {
	binding   callbinding.Config
	auditFile string
	wrap      func(*fakeCore) core.VMCP
	// clock, when set, is the call binding's clock.
	clock *cbClock
}

type cbTestServer struct {
	url    string
	fc     *fakeCore
	tokens *cbTokens
	srv    *Server
}

func newCallBindingTestServer(t *testing.T, opts cbServerOptions) *cbTestServer {
	t.Helper()

	authReg := vmcpauth.NewDefaultOutgoingAuthRegistry()
	require.NoError(t, authReg.RegisterStrategy(
		authtypes.StrategyTypeUnauthenticated,
		strategies.NewUnauthenticatedStrategy(),
	))
	tokens := &cbTokens{claims: map[string]map[string]any{}}
	cfg := &ServerConfig{
		SessionTTL:           time.Minute,
		AuthMiddleware:       tokens.middleware,
		SessionManagerConfig: &sessionmanager.FactoryConfig{Base: vmcpsession.NewSessionFactory(authReg)},
		BackendRegistry:      vmcp.NewImmutableRegistry([]vmcp.Backend{}),
		CallBinding:          opts.binding,
	}
	if opts.auditFile != "" {
		cfg.AuditConfig = &audit.Config{LogFile: opts.auditFile}
	}
	fc := &fakeCore{tools: []vmcp.Tool{
		{Name: cbTool, Description: "bound", BackendID: cbServer},
		{Name: cbOtherTool, Description: "other backend", BackendID: "other"},
		{Name: cbComposite, Description: "composite, no backend"},
	}}
	var v core.VMCP = fc
	if opts.wrap != nil {
		v = opts.wrap(fc)
	}
	srv, err := Serve(context.Background(), v, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })
	if opts.clock != nil {
		srv.callBinding.now = opts.clock.Now
	}
	handler, err := srv.Handler(context.Background())
	require.NoError(t, err)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return &cbTestServer{url: ts.URL, fc: fc, tokens: tokens, srv: srv}
}

func argsHash(t *testing.T, args string) string {
	t.Helper()
	hash, err := callbinding.ArgumentsSHA256(json.RawMessage(args))
	require.NoError(t, err)
	return hash
}

func callEntry(server, tool, hash string) map[string]any {
	return map[string]any{
		"type":             toolcalldetails.Type,
		"server":           server,
		"tool":             tool,
		"arguments_sha256": hash,
		"operation_id":     "op-1",
	}
}

// claimsFor returns the claims of a token for clientID with the given
// authorization_details (none when nil) that expires in an hour.
func claimsFor(clientID, jti string, details []any) map[string]any {
	claims := map[string]any{
		"iss": cbIssuer, "sub": "user-1", "client_id": clientID, "jti": jti,
		"exp": float64(time.Now().Add(time.Hour).Unix()),
	}
	if details != nil {
		claims["authorization_details"] = details
	}
	return claims
}

type cbResponse struct {
	status    int
	header    http.Header
	body      string
	sessionID string
}

// send posts a raw JSON body (or sends method with no body when body is
// empty) as the holder of token.
func (s *cbTestServer) send(
	t *testing.T, method, token, sessionID, body string, extra http.Header,
) cbResponse {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, method, s.url+"/mcp", reader)
	require.NoError(t, err)
	switch method {
	case http.MethodGet:
		req.Header.Set("Accept", "text/event-stream")
	case http.MethodPost:
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	for k, vs := range extra {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	out := cbResponse{status: resp.StatusCode, header: resp.Header, sessionID: resp.Header.Get("Mcp-Session-Id")}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") && method == http.MethodGet {
		t.Cleanup(func() { _ = resp.Body.Close() })
		return out
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	out.body = string(raw)
	return out
}

func (s *cbTestServer) initialize(t *testing.T, token string) cbResponse {
	t.Helper()
	return s.send(t, http.MethodPost, token, "",
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25",`+
			`"capabilities":{"elicitation":{}},"clientInfo":{"name":"cb-test","version":"1.0"}}}`, nil)
}

func (s *cbTestServer) session(t *testing.T, token string) string {
	t.Helper()
	resp := s.initialize(t, token)
	require.Equal(t, http.StatusOK, resp.status, "initialize: %s", resp.body)
	require.NotEmpty(t, resp.sessionID)
	return resp.sessionID
}

func toolCallBody(id int, name, rawArgs string) string {
	params := `{"name":` + cbJSON(name)
	if rawArgs != "" {
		params += `,"arguments":` + rawArgs
	}
	params += "}"
	return `{"jsonrpc":"2.0","id":` + cbJSON(id) + `,"method":"tools/call","params":` + params + `}`
}

func cbJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// cbTokenReasons are the refusals that describe the token and so get 401.
var cbTokenReasons = []string{
	callbinding.ReasonTokenExpired, callbinding.ReasonTokenReused, callbinding.ReasonMalformedEntry,
	callbinding.ReasonSingleEntry, callbinding.ReasonClaimsRequired,
}

// requireRefused checks the refusal shape. A refusal that describes the token
// gets 401 with an invalid_token challenge and an RFC 6750 error body naming
// reason. Any other gets 403 without a challenge and a JSON-RPC 403 error
// naming reason and echoing id.
func requireRefused(t *testing.T, resp cbResponse, reason string, id any) {
	t.Helper()
	if slices.Contains(cbTokenReasons, reason) {
		require.Equal(t, http.StatusUnauthorized, resp.status, "body: %s", resp.body)
		assert.Equal(t,
			`Bearer error="invalid_token", error_description="the access token is not accepted (`+reason+`)"`,
			resp.header.Get("WWW-Authenticate"))
		var body struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		require.NoError(t, json.Unmarshal([]byte(resp.body), &body), "body: %s", resp.body)
		assert.Equal(t, "invalid_token", body.Error)
		assert.Contains(t, body.Description, "("+reason+")")
		return
	}
	require.Equal(t, http.StatusForbidden, resp.status, "body: %s", resp.body)
	assert.Empty(t, resp.header.Values("WWW-Authenticate"))
	var body struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(resp.body), &body), "body: %s", resp.body)
	assert.Equal(t, "2.0", body.JSONRPC)
	assert.Equal(t, 403, body.Error.Code)
	assert.Contains(t, body.Error.Message, "("+reason+")")
	if id == nil {
		assert.Empty(t, body.ID)
	} else {
		assert.JSONEq(t, cbJSON(id), string(body.ID))
	}
}

func requireCallRan(t *testing.T, resp cbResponse) {
	t.Helper()
	require.Equal(t, http.StatusOK, resp.status, "body: %s", resp.body)
	assert.NotContains(t, resp.body, `"error"`)
	assert.Contains(t, resp.body, `"ok"`)
}

func TestCallBinding_MatchingCallRunsOnce(t *testing.T) {
	t.Parallel()
	s := newCallBindingTestServer(t, cbServerOptions{})
	args := `{"repo":"stacklok/toolhive","n":1}`
	token := s.tokens.add("bound", claimsFor(cbClient, "jti-1",
		[]any{callEntry(cbServer, cbTool, argsHash(t, args))}))

	session := s.session(t, token)
	ping := s.send(t, http.MethodPost, token, session, `{"jsonrpc":"2.0","id":2,"method":"ping"}`, nil)
	require.Equal(t, http.StatusOK, ping.status, "ping passes and does not use the token up: %s", ping.body)

	// Member order and whitespace do not change the hash.
	requireCallRan(t, s.send(t, http.MethodPost, token, session,
		toolCallBody(3, cbTool, `{ "n": 1, "repo": "stacklok/toolhive" }`), nil))
	assert.Equal(t, int32(1), s.fc.callToolCalls.Load())

	again := s.send(t, http.MethodPost, token, session, toolCallBody(4, cbTool, args), nil)
	requireRefused(t, again, callbinding.ReasonTokenReused, 4)
	assert.Equal(t, int32(1), s.fc.callToolCalls.Load(), "the second call must not reach the core")

	// The record is per token: a fresh token for the same call runs.
	fresh := s.tokens.add("bound-2", claimsFor(cbClient, "jti-2",
		[]any{callEntry(cbServer, cbTool, argsHash(t, args))}))
	requireCallRan(t, s.send(t, http.MethodPost, fresh, s.session(t, fresh), toolCallBody(5, cbTool, args), nil))
}

func TestCallBinding_Mismatches(t *testing.T) {
	t.Parallel()

	args := `{"a":1}`
	tests := []struct {
		name   string
		entry  map[string]any
		body   string
		reason string
	}{
		{name: "other tool", entry: callEntry(cbServer, cbTool, argsHash(t, args)),
			body: toolCallBody(9, cbOtherTool, args), reason: callbinding.ReasonToolMismatch},
		{name: "entry names a tool on another server", entry: callEntry(cbServer, cbOtherTool, argsHash(t, args)),
			body: toolCallBody(9, cbOtherTool, args), reason: callbinding.ReasonServerMismatch},
		{name: "other arguments", entry: callEntry(cbServer, cbTool, argsHash(t, args)),
			body: toolCallBody(9, cbTool, `{"a":2}`), reason: callbinding.ReasonArgumentsMismatch},
		{name: "number beyond 2^53", entry: callEntry(cbServer, cbTool, argsHash(t, args)),
			body: toolCallBody(9, cbTool, `{"a":9007199254740993}`), reason: callbinding.ReasonInvalidArguments},
		{name: "non-object arguments", entry: callEntry(cbServer, cbTool, argsHash(t, args)),
			body: toolCallBody(9, cbTool, `[1]`), reason: callbinding.ReasonInvalidArguments},
		{name: "arguments smuggled under a case variant", entry: callEntry(cbServer, cbTool, argsHash(t, `{}`)),
			body:   `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"github_read","Arguments":{"a":1}}}`,
			reason: callbinding.ReasonInvalidParams},
		{name: "arguments smuggled under a long s", entry: callEntry(cbServer, cbTool, argsHash(t, `{}`)),
			body:   `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"github_read","argumentſ":{"a":1}}}`,
			reason: callbinding.ReasonInvalidParams},
		{name: "unknown tool", entry: callEntry(cbServer, "nope", argsHash(t, args)),
			body: toolCallBody(9, "nope", args), reason: callbinding.ReasonToolNotFound},
		{name: "composite tool", entry: callEntry(cbServer, cbComposite, argsHash(t, args)),
			body: toolCallBody(9, cbComposite, args), reason: callbinding.ReasonToolNotBindable},
		{name: "composite tool named by an empty server", entry: callEntry("", cbComposite, argsHash(t, args)),
			body: toolCallBody(9, cbComposite, args), reason: callbinding.ReasonMalformedEntry},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newCallBindingTestServer(t, cbServerOptions{})
			token := s.tokens.add("bound", claimsFor("other-client", "jti-1", []any{tc.entry}))
			// An unbound token of the same principal opens the session, so a
			// malformed entry is checked on the call too.
			session := s.session(t, s.tokens.add("session", claimsFor("other-client", "session", nil)))
			requireRefused(t, s.send(t, http.MethodPost, token, session, tc.body, nil), tc.reason, 9)
			assert.Zero(t, s.fc.callToolCalls.Load())
		})
	}
}

// TestCallBinding_RefusalDoesNotUseUpToken checks that a refused call leaves
// the token able to start the call it names.
func TestCallBinding_RefusalDoesNotUseUpToken(t *testing.T) {
	t.Parallel()
	s := newCallBindingTestServer(t, cbServerOptions{})
	token := s.tokens.add("bound", claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))
	session := s.session(t, token)

	requireRefused(t, s.send(t, http.MethodPost, token, session, toolCallBody(2, cbTool, `{"x":1}`), nil),
		callbinding.ReasonArgumentsMismatch, 2)
	requireRefused(t, s.send(t, http.MethodPost, token, session,
		`{"jsonrpc":"2.0","id":3,"method":"resources/list"}`, nil), callbinding.ReasonMethodNotAllowed, 3)
	requireCallRan(t, s.send(t, http.MethodPost, token, session, toolCallBody(4, cbTool, ""), nil))
}

// TestCallBinding_AbsentAndNullArguments checks that absent and null
// arguments both hash as {}.
func TestCallBinding_AbsentAndNullArguments(t *testing.T) {
	t.Parallel()
	for name, rawArgs := range map[string]string{"absent": "", "null": "null", "empty": "{}"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := newCallBindingTestServer(t, cbServerOptions{})
			token := s.tokens.add("bound", claimsFor(cbClient, "jti-1",
				[]any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))
			requireCallRan(t, s.send(t, http.MethodPost, token, s.session(t, token), toolCallBody(2, cbTool, rawArgs), nil))
		})
	}
}

// TestCallBinding_SharedVectors sends each shared vector the gateway accepts
// as a real tools/call, so the middleware hashes params.arguments as received
// with the same result as the client.
func TestCallBinding_SharedVectors(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "callbinding", "testdata", "argumenthash", "vectors.json"))
	require.NoError(t, err)
	var file struct {
		Vectors []struct {
			Name           string          `json:"name"`
			Arguments      json.RawMessage `json:"arguments"`
			SHA256         string          `json:"sha256"`
			GatewayRefuses bool            `json:"gateway_refuses"`
		} `json:"vectors"`
	}
	require.NoError(t, json.Unmarshal(raw, &file))
	require.NotEmpty(t, file.Vectors)

	s := newCallBindingTestServer(t, cbServerOptions{})
	ran := 0
	for i, vector := range file.Vectors {
		if vector.GatewayRefuses {
			continue
		}
		token := s.tokens.add("v"+cbJSON(i), claimsFor(cbClient, "jti-v"+cbJSON(i),
			[]any{callEntry(cbServer, cbTool, vector.SHA256)}))
		resp := s.send(t, http.MethodPost, token, s.session(t, token),
			toolCallBody(2, cbTool, string(vector.Arguments)), nil)
		require.Equal(t, http.StatusOK, resp.status, "vector %q: %s", vector.Name, resp.body)
		ran++
	}
	assert.Equal(t, int32(ran), s.fc.callToolCalls.Load())
}

func TestCallBinding_TokenRules(t *testing.T) {
	t.Parallel()

	otherType := map[string]any{"type": "https://example.com/other", "x": "y"}
	entry := callEntry(cbServer, cbTool, "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a")
	malformed := callEntry(cbServer, cbTool, "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a")
	malformed["extra"] = "x"
	listEntry := map[string]any{"type": toolcalldetails.Type, "server": cbServer, "method": "tools/list"}
	expired := claimsFor(cbClient, "jti-1", []any{entry})
	expired["exp"] = float64(time.Now().Add(-time.Second).Unix())

	binding := callbinding.Config{Clients: []string{cbClient}}
	tests := []struct {
		name   string
		claims map[string]any
		// reason is the refusal of initialize; empty when it passes.
		initReason string
		// callReason is the refusal of a tools/call with {} arguments; empty
		// when the call runs.
		callReason string
	}{
		{name: "unlisted client without entry is unchanged", claims: claimsFor("other", "j", nil)},
		{name: "unlisted client with another type is unchanged", claims: claimsFor("other", "j", []any{otherType})},
		{name: "unlisted client with a matching entry", claims: claimsFor("other", "j", []any{entry})},
		{name: "listed client with a matching entry", claims: claimsFor(cbClient, "j", []any{otherType, entry})},
		{name: "listed client without entry", claims: claimsFor(cbClient, "j", nil),
			initReason: callbinding.ReasonEntryRequired, callReason: callbinding.ReasonEntryRequired},
		{name: "listed client with another type only", claims: claimsFor(cbClient, "j", []any{otherType}),
			initReason: callbinding.ReasonEntryRequired, callReason: callbinding.ReasonEntryRequired},
		{name: "two entries", claims: claimsFor("other", "j", []any{entry, entry}),
			initReason: callbinding.ReasonSingleEntry, callReason: callbinding.ReasonSingleEntry},
		{name: "malformed entry", claims: claimsFor("other", "j", []any{malformed}),
			initReason: callbinding.ReasonMalformedEntry, callReason: callbinding.ReasonMalformedEntry},
		{name: "list-call entry cannot start a tools/call", claims: claimsFor("other", "j", []any{listEntry}),
			callReason: callbinding.ReasonListCallUnsupported},
		{name: "expired token", claims: expired,
			initReason: callbinding.ReasonTokenExpired, callReason: callbinding.ReasonTokenExpired},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newCallBindingTestServer(t, cbServerOptions{binding: binding})
			token := s.tokens.add("tok", tc.claims)
			// A session for the call, opened with an unbound token of the same
			// principal, so the call can be checked even when initialize is
			// refused for the token under test.
			sessionToken := s.tokens.add("session", claimsFor("other", "session", nil))
			session := s.session(t, sessionToken)

			init := s.initialize(t, token)
			if tc.initReason != "" {
				requireRefused(t, init, tc.initReason, 1)
			} else {
				require.Equal(t, http.StatusOK, init.status, "initialize: %s", init.body)
			}

			call := s.send(t, http.MethodPost, token, session, toolCallBody(2, cbTool, "{}"), nil)
			if tc.callReason != "" {
				requireRefused(t, call, tc.callReason, 2)
				assert.Zero(t, s.fc.callToolCalls.Load())
			} else {
				requireCallRan(t, call)
			}
		})
	}
}

func TestCallBinding_GetAndDelete(t *testing.T) {
	t.Parallel()
	s := newCallBindingTestServer(t, cbServerOptions{})
	token := s.tokens.add("bound", claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))
	session := s.session(t, token)

	stream := s.send(t, http.MethodGet, token, session, "", nil)
	assert.Equal(t, http.StatusOK, stream.status, "GET on the token's session passes")

	// GET on a session another principal owns is answered by the ownership check.
	foreign := s.tokens.add("foreign", map[string]any{
		"iss": cbIssuer, "sub": "user-2", "client_id": cbClient, "jti": "jti-f",
		"exp":                   float64(time.Now().Add(time.Hour).Unix()),
		"authorization_details": []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))},
	})
	assert.Equal(t, http.StatusNotFound, s.send(t, http.MethodGet, foreign, session, "", nil).status)
	assert.Equal(t, http.StatusNotFound, s.send(t, http.MethodDelete, foreign, session, "", nil).status)

	// Neither uses the token up.
	requireCallRan(t, s.send(t, http.MethodPost, token, session, toolCallBody(2, cbTool, "{}"), nil))

	del := s.send(t, http.MethodDelete, token, session, "", nil)
	assert.Equal(t, http.StatusOK, del.status, "DELETE on the token's session passes: %s", del.body)
}

func TestCallBinding_ExpiredTokenGetRefused(t *testing.T) {
	t.Parallel()
	s := newCallBindingTestServer(t, cbServerOptions{})
	claims := claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))})
	claims["exp"] = float64(time.Now().Add(-time.Second).Unix())
	token := s.tokens.add("expired", claims)
	requireRefused(t, s.send(t, http.MethodGet, token, "s", "", nil), callbinding.ReasonTokenExpired, nil)
	requireRefused(t, s.send(t, http.MethodDelete, token, "s", "", nil), callbinding.ReasonTokenExpired, nil)
}

func TestCallBinding_OtherMethodsRefused(t *testing.T) {
	t.Parallel()
	s := newCallBindingTestServer(t, cbServerOptions{})
	token := s.tokens.add("bound", claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))
	session := s.session(t, token)

	for _, method := range []string{"resources/read", "prompts/get", "resources/list", "completion/complete"} {
		requireRefused(t, s.send(t, http.MethodPost, token, session,
			`{"jsonrpc":"2.0","id":5,"method":"`+method+`","params":{}}`, nil), callbinding.ReasonMethodNotAllowed, 5)
	}
	requireRefused(t, s.send(t, http.MethodPost, token, session,
		`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"github_read"}}`, nil),
		callbinding.ReasonMethodNotAllowed, nil)
	requireRefused(t, s.send(t, http.MethodPut, token, session, "", nil), callbinding.ReasonMethodNotAllowed, nil)

	// A batch is refused by the parser for every caller, before the check.
	batch := s.send(t, http.MethodPost, token, session, "["+toolCallBody(6, cbTool, "{}")+"]", nil)
	assert.Equal(t, http.StatusBadRequest, batch.status, "body: %s", batch.body)
	assert.Zero(t, s.fc.callToolCalls.Load())
}

// modernCall is a Modern (2026-07-28) tools/call for cbTool.
func cbModernCall(id int, args string) string {
	return `{"jsonrpc":"2.0","id":` + cbJSON(id) + `,"method":"tools/call","params":{"name":"github_read",` +
		`"arguments":` + args + `,"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28",` +
		`"io.modelcontextprotocol/clientCapabilities":{}}}}`
}

func cbModernHeaders() http.Header {
	return http.Header{
		"Mcp-Protocol-Version": {"2026-07-28"}, "Mcp-Method": {"tools/call"}, "Mcp-Name": {cbTool},
	}
}

func TestCallBinding_ModernToolCall(t *testing.T) {
	t.Parallel()
	s := newCallBindingTestServer(t, cbServerOptions{})
	token := s.tokens.add("bound", claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{"a":1}`))}))

	requireRefused(t, s.send(t, http.MethodPost, token, "", cbModernCall(1, `{"a":2}`), cbModernHeaders()),
		callbinding.ReasonArgumentsMismatch, 1)
	ok := s.send(t, http.MethodPost, token, "", cbModernCall(2, `{"a":1}`), cbModernHeaders())
	require.Equal(t, http.StatusOK, ok.status, "body: %s", ok.body)
	assert.Equal(t, int32(1), s.fc.callToolCalls.Load())
	requireRefused(t, s.send(t, http.MethodPost, token, "", cbModernCall(3, `{"a":1}`), cbModernHeaders()),
		callbinding.ReasonTokenReused, 3)
}

// postAsync sends a POST off the test goroutine and reports its status, or
// 0 when the request failed. It must not use require.
func (s *cbTestServer) postAsync(ctx context.Context, token, sessionID, body string) <-chan int {
	done := make(chan int, 1)
	go func() {
		status := 0
		defer func() { done <- status }()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url+"/mcp", strings.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Mcp-Session-Id", sessionID)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		status = resp.StatusCode
	}()
	return done
}

func awaitStatus(t *testing.T, done <-chan int) int {
	t.Helper()
	select {
	case status := <-done:
		return status
	case <-time.After(10 * time.Second):
		require.FailNow(t, "the request did not return")
		return 0
	}
}

func cancelBody(requestID string) string {
	return `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":` + requestID + `}}`
}

const elicitationReply = `{"jsonrpc":"2.0","id":100,"result":{"action":"accept"}}`

func TestCallBinding_OpenCallMessages(t *testing.T) {
	t.Parallel()
	bc := newBlockingCore()
	s := newCallBindingTestServer(t, cbServerOptions{wrap: func(fc *fakeCore) core.VMCP {
		bc.fakeCore = fc
		return bc
	}})
	t.Cleanup(bc.unblock)
	token := s.tokens.add("bound", claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))
	session := s.session(t, token)
	otherSession := s.session(t, token)

	// Before the call starts, nothing belongs to it.
	requireRefused(t, s.send(t, http.MethodPost, token, session, cancelBody("7"), nil), callbinding.ReasonNoOpenCall, nil)
	requireRefused(t, s.send(t, http.MethodPost, token, session, elicitationReply, nil), callbinding.ReasonNoOpenCall, nil)

	done := s.postAsync(t.Context(), token, session, toolCallBody(7, cbTool, "{}"))
	bc.waitStarted(t)

	// While the call is open: the same token cannot start another call.
	requireRefused(t, s.send(t, http.MethodPost, token, session, toolCallBody(8, cbTool, "{}"), nil),
		callbinding.ReasonTokenReused, 8)
	// A cancellation for another ID (7 and "7" differ), or on another
	// session, is refused, and so is a response on another session.
	for _, id := range []string{"8", `"7"`} {
		requireRefused(t, s.send(t, http.MethodPost, token, session, cancelBody(id), nil), callbinding.ReasonNoOpenCall, nil)
	}
	requireRefused(t, s.send(t, http.MethodPost, token, otherSession, cancelBody("7"), nil),
		callbinding.ReasonNoOpenCall, nil)
	requireRefused(t, s.send(t, http.MethodPost, token, otherSession, elicitationReply, nil),
		callbinding.ReasonNoOpenCall, nil)

	// A cancellation without a usable request ID belongs to no call, and
	// does not end the open one.
	for _, params := range []string{`{}`, `{"requestId":null}`, `{"requestId":true}`,
		`{"requestId":1.5}`, `{"requestId":{}}`} {
		body := `{"jsonrpc":"2.0","method":"notifications/cancelled","params":` + params + `}`
		requireRefused(t, s.send(t, http.MethodPost, token, session, body, nil), callbinding.ReasonNoOpenCall, nil)
	}

	// A response and a cancellation on the call's session pass the check;
	// the transport answers them. The cancellation goes last, since it ends
	// the call.
	resp := s.send(t, http.MethodPost, token, session, elicitationReply, nil)
	assert.NotEqual(t, http.StatusForbidden, resp.status, "body: %s", resp.body)
	cancel := s.send(t, http.MethodPost, token, session, cancelBody("7"), nil)
	assert.Equal(t, http.StatusAccepted, cancel.status, "body: %s", cancel.body)

	bc.unblock()
	awaitStatus(t, done)

	// Once the call's request has returned, nothing belongs to it.
	requireRefused(t, s.send(t, http.MethodPost, token, session, elicitationReply, nil), callbinding.ReasonNoOpenCall, nil)
	requireRefused(t, s.send(t, http.MethodPost, token, session, cancelBody("7"), nil), callbinding.ReasonNoOpenCall, nil)
	requireRefused(t, s.send(t, http.MethodPost, token, session, toolCallBody(9, cbTool, "{}"), nil),
		callbinding.ReasonTokenReused, 9)
}

// TestCallBinding_CancelAfterDroppedPost pins a known limitation: a Legacy
// client that closes the call's POST before it sends notifications/cancelled
// gets no_open_call, because the call's request has returned. Accepting
// messages for a call after its request returns belongs with accepting them
// after exp.
func TestCallBinding_CancelAfterDroppedPost(t *testing.T) {
	t.Parallel()
	bc := newBlockingCore()
	record := callbinding.NewMemoryRecord()
	s := newCallBindingTestServer(t, cbServerOptions{
		binding: callbinding.Config{Record: record},
		wrap: func(fc *fakeCore) core.VMCP {
			bc.fakeCore = fc
			return bc
		},
	})
	t.Cleanup(bc.unblock)
	claims := claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))})
	token := s.tokens.add("bound", claims)
	session := s.session(t, token)

	ctx, cancelPost := context.WithCancel(t.Context())
	done := s.postAsync(ctx, token, session, toolCallBody(7, cbTool, "{}"))
	bc.waitStarted(t)
	cancelPost()
	awaitStatus(t, done)

	key := cbIssuer + "\x00jti-1"
	require.Eventually(t, func() bool {
		call, found, err := record.Lookup(context.Background(), key)
		return err == nil && found && call.Ended
	}, 10*time.Second, 10*time.Millisecond, "the dropped POST must end the call's request")

	requireRefused(t, s.send(t, http.MethodPost, token, session, cancelBody("7"), nil), callbinding.ReasonNoOpenCall, nil)
}

// TestCallBinding_ResponseAfterMaxCallDuration checks that a response is
// refused once the call has run for the maximum call duration, even while
// its request is still open.
func TestCallBinding_ResponseAfterMaxCallDuration(t *testing.T) {
	t.Parallel()
	bc := newBlockingCore()
	clock := &cbClock{now: time.Now()}
	s := newCallBindingTestServer(t, cbServerOptions{
		binding: callbinding.Config{MaxCallDuration: 10 * time.Minute},
		wrap: func(fc *fakeCore) core.VMCP {
			bc.fakeCore = fc
			return bc
		},
		clock: clock,
	})
	t.Cleanup(bc.unblock)
	token := s.tokens.add("bound", claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))
	session := s.session(t, token)

	done := s.postAsync(t.Context(), token, session, toolCallBody(7, cbTool, "{}"))
	bc.waitStarted(t)

	clock.Advance(10*time.Minute - time.Second)
	resp := s.send(t, http.MethodPost, token, session, elicitationReply, nil)
	assert.NotEqual(t, http.StatusForbidden, resp.status, "within the maximum call duration: %s", resp.body)

	clock.Advance(time.Second)
	requireRefused(t, s.send(t, http.MethodPost, token, session, elicitationReply, nil), callbinding.ReasonNoOpenCall, nil)

	bc.unblock()
	awaitStatus(t, done)
}

// TestCallBinding_ModernCallHasNoMessages checks that a sessionless (Modern)
// call admits no cancellation or response: Modern clients cancel by closing
// the request.
func TestCallBinding_ModernCallHasNoMessages(t *testing.T) {
	t.Parallel()
	bc := newBlockingCore()
	s := newCallBindingTestServer(t, cbServerOptions{wrap: func(fc *fakeCore) core.VMCP {
		bc.fakeCore = fc
		return bc
	}})
	t.Cleanup(bc.unblock)
	token := s.tokens.add("bound", claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))

	done := make(chan int, 1)
	go func() {
		status := 0
		defer func() { done <- status }()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.url+"/mcp", strings.NewReader(cbModernCall(7, `{}`)))
		if err != nil {
			return
		}
		for k, v := range cbModernHeaders() {
			req.Header[k] = v
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+token)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			status = resp.StatusCode
		}
	}()
	bc.waitStarted(t)

	requireRefused(t, s.send(t, http.MethodPost, token, "", cancelBody("7"), nil), callbinding.ReasonNoOpenCall, nil)
	requireRefused(t, s.send(t, http.MethodPost, token, "", elicitationReply, nil), callbinding.ReasonNoOpenCall, nil)

	bc.unblock()
	assert.Equal(t, http.StatusOK, awaitStatus(t, done))
}

// TestCallBinding_LookupFailureKeepsToken checks that a route lookup that
// fails for a reason other than authorization answers 503 and leaves the
// token unused.
func TestCallBinding_LookupFailureKeepsToken(t *testing.T) {
	t.Parallel()
	lc := &lookupErrCore{err: errors.New("aggregation failed")}
	s := newCallBindingTestServer(t, cbServerOptions{wrap: func(fc *fakeCore) core.VMCP {
		lc.fakeCore = fc
		return lc
	}})
	token := s.tokens.add("bound", claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))
	session := s.session(t, token)

	resp := s.send(t, http.MethodPost, token, session, toolCallBody(2, cbTool, "{}"), nil)
	require.Equal(t, http.StatusServiceUnavailable, resp.status, "body: %s", resp.body)
	assert.Empty(t, resp.header.Get("WWW-Authenticate"))

	lc.mu.Lock()
	lc.err = nil
	lc.mu.Unlock()
	requireCallRan(t, s.send(t, http.MethodPost, token, session, toolCallBody(3, cbTool, "{}"), nil))
}

func TestCallBinding_RefusalIsAuditedAsDenial(t *testing.T) {
	t.Parallel()
	auditPath := filepath.Join(t.TempDir(), "audit.log")
	s := newCallBindingTestServer(t, cbServerOptions{auditFile: auditPath})
	secretArgs := `{"secret":"do-not-log"}`
	token := s.tokens.add("bound-token-value", claimsFor(cbClient, "jti-1",
		[]any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))
	session := s.session(t, token)

	requireRefused(t, s.send(t, http.MethodPost, token, session, toolCallBody(2, cbTool, secretArgs), nil),
		callbinding.ReasonArgumentsMismatch, 2)

	var denial map[string]any
	require.Eventually(t, func() bool {
		raw, err := os.ReadFile(auditPath)
		if err != nil {
			return false
		}
		for _, line := range bytes.Split(raw, []byte("\n")) {
			var event map[string]any
			if json.Unmarshal(line, &event) == nil && event["outcome"] == audit.OutcomeDenied {
				denial = event
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)

	encoded := cbJSON(denial)
	assert.Contains(t, encoded, `"denial_reason":"arguments_mismatch"`)
	assert.NotContains(t, encoded, "do-not-log", "the arguments are never audited")
	assert.NotContains(t, encoded, "bound-token-value", "the token is never audited")
}

func TestCallBinding_OptimizerToolsAreNotBindable(t *testing.T) {
	t.Parallel()
	fc := &fakeCore{tools: []vmcp.Tool{{Name: cbTool, BackendID: cbServer}}}
	s := &Server{core: fc}
	backend, err := s.callBindingRoute(context.Background(), nil, cbTool)
	require.NoError(t, err)
	assert.Equal(t, cbServer, backend)

	// With the optimizer, a session's tools are find_tool and call_tool,
	// neither of which has a backend.
	s.optimizerFactory = stubOptimizerFactory
	for _, name := range []string{"call_tool", "find_tool", cbTool} {
		backend, err := s.callBindingRoute(context.Background(), nil, name)
		require.NoError(t, err)
		assert.Empty(t, backend, name)
	}
}

func TestNewCallBindingValidates(t *testing.T) {
	t.Parallel()
	_, err := newCallBinding(callbinding.Config{MaxCallDuration: -time.Second}, nil)
	require.ErrorIs(t, err, vmcp.ErrInvalidConfig)
	_, err = newCallBinding(callbinding.Config{Clients: []string{"a", ""}}, nil)
	require.ErrorIs(t, err, vmcp.ErrInvalidConfig)

	cb, err := newCallBinding(callbinding.Config{}, nil)
	require.NoError(t, err)
	assert.Equal(t, callbinding.DefaultMaxCallDuration, cb.maxCallDuration)
	assert.NotNil(t, cb.record)
}

// spyRecord records the expiry of each Start and can fail on demand.
type spyRecord struct {
	callbinding.Record
	mu        sync.Mutex
	expiries  []time.Time
	failStart bool
	failRead  bool
	failEnd   bool
	ends      int
}

func (r *spyRecord) Start(ctx context.Context, key string, call callbinding.Call, expiresAt time.Time) (bool, error) {
	r.mu.Lock()
	r.expiries = append(r.expiries, expiresAt)
	fail := r.failStart
	r.mu.Unlock()
	if fail {
		return false, errors.New("record unavailable")
	}
	return r.Record.Start(ctx, key, call, expiresAt)
}

func (r *spyRecord) Lookup(ctx context.Context, key string) (callbinding.Call, bool, error) {
	r.mu.Lock()
	fail := r.failRead
	r.mu.Unlock()
	if fail {
		return callbinding.Call{}, false, errors.New("record unavailable")
	}
	return r.Record.Lookup(ctx, key)
}

func (r *spyRecord) End(ctx context.Context, key string) error {
	r.mu.Lock()
	r.ends++
	fail := r.failEnd
	r.mu.Unlock()
	if fail {
		return errors.New("record unavailable")
	}
	return r.Record.End(ctx, key)
}

func (r *spyRecord) endCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ends
}

func (r *spyRecord) set(failStart, failRead bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failStart, r.failRead = failStart, failRead
}

func (r *spyRecord) lastExpiry() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.expiries[len(r.expiries)-1]
}

// TestCallBinding_RecordExpiry checks the record lifetime: the later of the
// token's exp and the end of the maximum call duration, plus the retention
// margin.
func TestCallBinding_RecordExpiry(t *testing.T) {
	t.Parallel()
	start := time.Unix(1_900_000_000, 0)
	tests := []struct {
		name   string
		expIn  time.Duration
		maxDur time.Duration
		want   time.Time
	}{
		{name: "exp is later", expIn: time.Hour, maxDur: time.Minute,
			want: start.Add(time.Hour + callBindingRetentionMargin)},
		{name: "maximum call duration is later", expIn: 10 * time.Second, maxDur: 10 * time.Minute,
			want: start.Add(10*time.Minute + callBindingRetentionMargin)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			record := &spyRecord{Record: callbinding.NewMemoryRecord()}
			s := newCallBindingTestServer(t, cbServerOptions{
				binding: callbinding.Config{MaxCallDuration: tc.maxDur, Record: record},
				clock:   &cbClock{now: start},
			})
			claims := claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))})
			claims["exp"] = float64(start.Add(tc.expIn).Unix())
			token := s.tokens.add("bound", claims)

			requireCallRan(t, s.send(t, http.MethodPost, token, s.session(t, token), toolCallBody(2, cbTool, "{}"), nil))
			assert.Equal(t, tc.want, record.lastExpiry())
		})
	}
}

// TestCallBinding_RecordFailureKeepsToken checks that a record that fails
// answers 503, runs nothing, and leaves the token usable.
func TestCallBinding_RecordFailureKeepsToken(t *testing.T) {
	t.Parallel()
	record := &spyRecord{Record: callbinding.NewMemoryRecord()}
	s := newCallBindingTestServer(t, cbServerOptions{binding: callbinding.Config{Record: record}})
	token := s.tokens.add("bound", claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))
	session := s.session(t, token)

	record.set(true, true)
	for name, body := range map[string]string{
		"tools/call":   toolCallBody(2, cbTool, "{}"),
		"cancellation": cancelBody("2"),
		"response":     elicitationReply,
	} {
		resp := s.send(t, http.MethodPost, token, session, body, nil)
		assert.Equal(t, http.StatusServiceUnavailable, resp.status, "%s: %s", name, resp.body)
		assert.Empty(t, resp.header.Get("WWW-Authenticate"), name)
	}
	assert.Zero(t, s.fc.callToolCalls.Load())

	record.set(false, false)
	requireCallRan(t, s.send(t, http.MethodPost, token, session, toolCallBody(3, cbTool, "{}"), nil))
}

// TestCallBinding_RecordEndFailureKeepsTokenUsed checks that a record that
// cannot mark a call as ended neither fails the call nor frees its token.
func TestCallBinding_RecordEndFailureKeepsTokenUsed(t *testing.T) {
	t.Parallel()
	record := &spyRecord{Record: callbinding.NewMemoryRecord(), failEnd: true}
	s := newCallBindingTestServer(t, cbServerOptions{binding: callbinding.Config{Record: record}})
	token := s.tokens.add("bound", claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))
	session := s.session(t, token)

	requireCallRan(t, s.send(t, http.MethodPost, token, session, toolCallBody(2, cbTool, "{}"), nil))
	assert.Equal(t, 1, record.endCount())
	requireRefused(t, s.send(t, http.MethodPost, token, session, toolCallBody(3, cbTool, "{}"), nil),
		callbinding.ReasonTokenReused, 3)

	// The record still holds the call as open, but this replica saw its
	// request return: the call's messages are refused.
	call, found, err := record.Lookup(context.Background(), cbIssuer+"\x00jti-1")
	require.NoError(t, err)
	require.True(t, found)
	require.False(t, call.Ended)
	requireRefused(t, s.send(t, http.MethodPost, token, session, cancelBody("2"), nil), callbinding.ReasonNoOpenCall, nil)
	requireRefused(t, s.send(t, http.MethodPost, token, session, elicitationReply, nil), callbinding.ReasonNoOpenCall, nil)
}

// TestCallBinding_CodeModeIsNotBindable routes through the real code mode
// decorator, whose execute_tool_script has no backend.
func TestCallBinding_CodeModeIsNotBindable(t *testing.T) {
	t.Parallel()
	s := newCallBindingTestServer(t, cbServerOptions{wrap: func(fc *fakeCore) core.VMCP {
		return codemode.NewDecorator(fc, &codemode.Config{})
	}})
	args := `{"script":"return 1"}`
	token := s.tokens.add("bound", claimsFor(cbClient, "jti-1",
		[]any{callEntry(cbServer, "execute_tool_script", argsHash(t, args))}))
	requireRefused(t, s.send(t, http.MethodPost, token, s.session(t, token),
		toolCallBody(2, "execute_tool_script", args), nil), callbinding.ReasonToolNotBindable, 2)
	assert.Zero(t, s.fc.callToolCalls.Load())
}

// TestCallBinding_ExpBoundary checks that a token is accepted until just
// before exp and refused at exp, with the validator's zero leeway.
func TestCallBinding_ExpBoundary(t *testing.T) {
	t.Parallel()
	exp := time.Unix(1_900_000_000, 0)
	clock := &cbClock{now: exp.Add(-time.Nanosecond)}
	s := newCallBindingTestServer(t, cbServerOptions{clock: clock})
	claims := claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))})
	claims["exp"] = float64(exp.Unix())
	token := s.tokens.add("bound", claims)

	require.Equal(t, http.StatusOK, s.initialize(t, token).status)
	clock.Set(exp)
	requireRefused(t, s.initialize(t, token), callbinding.ReasonTokenExpired, 1)
}

// TestCallBinding_RefusalVectors sends each shared refusal vector as a real
// tools/call and checks which layer refuses it: the parser, with HTTP 400 and
// a JSON-RPC error, or call binding, with its reason. The core must not run.
func TestCallBinding_RefusalVectors(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "callbinding", "testdata", "argumenthash", "vectors.json"))
	require.NoError(t, err)
	var file struct {
		Refusals []struct {
			Name   string  `json:"name"`
			Text   *string `json:"arguments_text"`
			Base64 *string `json:"arguments_base64"`
		} `json:"refusals"`
	}
	require.NoError(t, json.Unmarshal(raw, &file))
	require.NotEmpty(t, file.Refusals)

	// The parser refuses what it cannot decode as one JSON-RPC message;
	// call binding refuses arguments it cannot hash (invalid_arguments) and
	// params it cannot read as dispatch does (invalid_params).
	const (
		parseError     = "-32700"
		invalidRequest = "-32600"
		badArguments   = callbinding.ReasonInvalidArguments
		badParams      = callbinding.ReasonInvalidParams
	)
	want := map[string]string{
		"2^53+1":                     badArguments,
		"-(2^53+1)":                  badArguments,
		"2^53+1 in exponent form":    badArguments,
		"2^53+0.5 rounding to 2^53":  badArguments,
		"2^53+2":                     badArguments,
		"1e21 (RFC 8785 Appendix B)": badArguments,
		"1e23 (RFC 8785 Appendix B)": badArguments,
		"295147905179352830000 (RFC 8785 Appendix B)": badArguments,
		"largest double (RFC 8785 Appendix B)":        badArguments,
		"nested beyond 2^53":                          badArguments,
		"RFC 8785 section 3.2.4 example with 1E30":    badArguments,
		"top-level array":                             badArguments,
		"top-level string":                            badArguments,
		"top-level number":                            badArguments,
		"top-level true":                              badArguments,
		"invalid UTF-8 in value":                      badParams,
		"invalid UTF-8 in key":                        badParams,
		"truncated UTF-8":                             badParams,
		"UTF-8 encoded surrogate":                     badParams,
		`lone \ud800`:                                 badParams,
		`lone \udc00`:                                 badParams,
		`lone \ud800 in key`:                          badParams,
		`\ud800 then a letter`:                        badParams,
		`\ud800 then \u0041`:                          badParams,
		"two high surrogates":                         badParams,
		"reversed pair":                               badParams,
		`\ud800 after escaped backslash`:              badParams,
		"1e400":                                       invalidRequest,
		"-1e400":                                      invalidRequest,
		"duplicate keys":                              invalidRequest,
		"duplicate keys by escape":                    invalidRequest,
		"nested duplicate keys":                       invalidRequest,
		"malformed":                                   parseError,
		"trailing value":                              parseError,
		"trailing garbage":                            parseError,
		"byte order mark before the object":           parseError,
		"no-break space before null":                  parseError,
		"vertical tab only":                           parseError,
	}

	s := newCallBindingTestServer(t, cbServerOptions{})
	for i, refusal := range file.Refusals {
		var args []byte
		switch {
		case refusal.Text != nil:
			args = []byte(*refusal.Text)
		case refusal.Base64 != nil:
			args, err = base64.StdEncoding.DecodeString(*refusal.Base64)
			require.NoError(t, err, refusal.Name)
		}
		jti := "r" + strconv.Itoa(i)
		token := s.tokens.add(jti, claimsFor(cbClient, jti, []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))
		session := s.session(t, token)
		body := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"github_read","arguments":` +
			string(args) + `}}`
		resp := s.send(t, http.MethodPost, token, session, body, nil)

		expected, ok := want[refusal.Name]
		require.True(t, ok, "refusal %q has no expected layer", refusal.Name)
		if !strings.HasPrefix(expected, "-") {
			requireRefused(t, resp, expected, 2)
			continue
		}
		require.Equal(t, http.StatusBadRequest, resp.status, "refusal %q: %s", refusal.Name, resp.body)
		var rpc struct {
			Error struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal([]byte(resp.body), &rpc), "refusal %q: %s", refusal.Name, resp.body)
		assert.Equal(t, expected, strconv.Itoa(rpc.Error.Code), "refusal %q: %s", refusal.Name, resp.body)
	}
	assert.Len(t, want, len(file.Refusals), "every expected layer names a vector")
	assert.Zero(t, s.fc.callToolCalls.Load())
}

// TestCallBinding_OnlyAtEndpoint checks that a call-bound token is refused off
// the MCP endpoint, so a stray path cannot use it up.
func TestCallBinding_OnlyAtEndpoint(t *testing.T) {
	t.Parallel()
	s := newCallBindingTestServer(t, cbServerOptions{})
	token := s.tokens.add("bound", claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))
	session := s.session(t, token)

	stray := *s
	stray.url = s.url + "/other"
	requireRefused(t, stray.send(t, http.MethodPost, token, session, toolCallBody(2, cbTool, "{}"), nil),
		callbinding.ReasonMethodNotAllowed, 2)
	requireCallRan(t, s.send(t, http.MethodPost, token, session, toolCallBody(3, cbTool, "{}"), nil))
}

// TestCallBinding_SessionRequestsAfterCall checks that once the token has
// started its call, GET and DELETE are limited to that call's session and
// initialize is refused, while before the call ownership alone limits them.
func TestCallBinding_SessionRequestsAfterCall(t *testing.T) {
	t.Parallel()
	s := newCallBindingTestServer(t, cbServerOptions{})
	token := s.tokens.add("bound", claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))
	callSession := s.session(t, token)
	otherSession := s.session(t, token)

	assert.Equal(t, http.StatusOK, s.send(t, http.MethodGet, token, otherSession, "", nil).status,
		"before the call, GET passes on any session the principal owns")

	requireCallRan(t, s.send(t, http.MethodPost, token, callSession, toolCallBody(2, cbTool, "{}"), nil))

	requireRefused(t, s.send(t, http.MethodGet, token, otherSession, "", nil), callbinding.ReasonSessionMismatch, nil)
	requireRefused(t, s.send(t, http.MethodDelete, token, otherSession, "", nil), callbinding.ReasonSessionMismatch, nil)
	requireRefused(t, s.send(t, http.MethodGet, token, "", "", nil), callbinding.ReasonSessionMismatch, nil)
	requireRefused(t, s.initialize(t, token), callbinding.ReasonTokenReused, 1)

	assert.Equal(t, http.StatusOK, s.send(t, http.MethodGet, token, callSession, "", nil).status,
		"GET on the call's session passes")
	del := s.send(t, http.MethodDelete, token, callSession, "", nil)
	assert.Equal(t, http.StatusOK, del.status, "DELETE on the call's session passes: %s", del.body)
}

// TestCallBinding_ToolsList checks that a tool-call token may list tools
// before and after its call without using itself up, and that it still
// starts exactly one call.
func TestCallBinding_ToolsList(t *testing.T) {
	t.Parallel()
	s := newCallBindingTestServer(t, cbServerOptions{})
	token := s.tokens.add("bound", claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))
	session := s.session(t, token)
	list := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`

	before := s.send(t, http.MethodPost, token, session, list, nil)
	require.Equal(t, http.StatusOK, before.status, "body: %s", before.body)
	assert.Contains(t, before.body, cbTool)
	requireCallRan(t, s.send(t, http.MethodPost, token, session, toolCallBody(3, cbTool, "{}"), nil))
	after := s.send(t, http.MethodPost, token, session, list, nil)
	require.Equal(t, http.StatusOK, after.status, "body: %s", after.body)
	requireRefused(t, s.send(t, http.MethodPost, token, session, toolCallBody(4, cbTool, "{}"), nil),
		callbinding.ReasonTokenReused, 4)
	assert.Equal(t, int32(1), s.fc.callToolCalls.Load())

	// A list-call token is refused, tools/list included.
	listToken := s.tokens.add("list", claimsFor(cbClient, "jti-2",
		[]any{map[string]any{"type": toolcalldetails.Type, "server": cbServer, "method": "tools/list"}}))
	requireRefused(t, s.send(t, http.MethodPost, listToken, s.session(t, listToken), list, nil),
		callbinding.ReasonListCallUnsupported, 2)
}

// TestCallBinding_ModernCallWithSessionHeader checks that a Modern tools/call
// that sends Mcp-Session-Id records no session, so no message is admitted on
// that session while the call is open.
func TestCallBinding_ModernCallWithSessionHeader(t *testing.T) {
	t.Parallel()
	bc := newBlockingCore()
	record := callbinding.NewMemoryRecord()
	s := newCallBindingTestServer(t, cbServerOptions{
		binding: callbinding.Config{Record: record},
		wrap: func(fc *fakeCore) core.VMCP {
			bc.fakeCore = fc
			return bc
		},
	})
	t.Cleanup(bc.unblock)
	token := s.tokens.add("bound", claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))
	session := s.session(t, token)

	done := make(chan int, 1)
	go func() {
		status := 0
		defer func() { done <- status }()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.url+"/mcp", strings.NewReader(cbModernCall(7, `{}`)))
		if err != nil {
			return
		}
		for k, v := range cbModernHeaders() {
			req.Header[k] = v
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Mcp-Session-Id", session)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			status = resp.StatusCode
		}
	}()
	bc.waitStarted(t)

	call, found, err := record.Lookup(context.Background(), cbIssuer+"\x00jti-1")
	require.NoError(t, err)
	require.True(t, found)
	assert.Empty(t, call.SessionID, "a Modern call has no session, whatever header it sends")
	requireRefused(t, s.send(t, http.MethodPost, token, session, cancelBody("7"), nil), callbinding.ReasonNoOpenCall, nil)
	requireRefused(t, s.send(t, http.MethodPost, token, session, elicitationReply, nil), callbinding.ReasonNoOpenCall, nil)

	bc.unblock()
	assert.Equal(t, http.StatusOK, awaitStatus(t, done))
}

// TestCallBinding_RefusalBeforeOwnership checks the order against session
// ownership: a call the token does not permit is refused by call binding even
// on a foreign session, and a call it permits that ownership then refuses
// still uses the token up.
func TestCallBinding_RefusalBeforeOwnership(t *testing.T) {
	t.Parallel()
	s := newCallBindingTestServer(t, cbServerOptions{})
	foreignSession := s.session(t, s.tokens.add("foreign", map[string]any{
		"iss": cbIssuer, "sub": "user-2", "client_id": cbClient, "jti": "jti-f",
		"exp": float64(time.Now().Add(time.Hour).Unix()),
	}))
	token := s.tokens.add("bound", claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))

	requireRefused(t, s.send(t, http.MethodPost, token, foreignSession, toolCallBody(2, cbTool, `{"x":1}`), nil),
		callbinding.ReasonArgumentsMismatch, 2)
	assert.Equal(t, http.StatusNotFound,
		s.send(t, http.MethodPost, token, foreignSession, toolCallBody(3, cbTool, "{}"), nil).status)
	requireRefused(t, s.send(t, http.MethodPost, token, s.session(t, s.tokens.add("own", claimsFor(cbClient, "own", nil))),
		toolCallBody(4, cbTool, "{}"), nil), callbinding.ReasonTokenReused, 4)
	assert.Zero(t, s.fc.callToolCalls.Load())
}

// TestCallBinding_AdmissionDenialUsesToken checks that a call the core's
// admission denies after call binding admitted it still uses the token up.
func TestCallBinding_AdmissionDenialUsesToken(t *testing.T) {
	t.Parallel()
	s := newCallBindingTestServer(t, cbServerOptions{wrap: func(fc *fakeCore) core.VMCP {
		fc.callErr = vmcp.ErrAuthorizationFailed
		return fc
	}})
	token := s.tokens.add("bound", claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))
	session := s.session(t, token)

	denied := s.send(t, http.MethodPost, token, session, toolCallBody(2, cbTool, "{}"), nil)
	assert.Contains(t, denied.body, "denied", "body: %s", denied.body)
	requireRefused(t, s.send(t, http.MethodPost, token, session, toolCallBody(3, cbTool, "{}"), nil),
		callbinding.ReasonTokenReused, 3)
	assert.Equal(t, int32(1), s.fc.callToolCalls.Load())
}

// TestCallBinding_ChallengeParameters checks that a 401 carries the realm,
// resource metadata and scope of the configured challenge.
func TestCallBinding_ChallengeParameters(t *testing.T) {
	t.Parallel()
	s := newCallBindingTestServer(t, cbServerOptions{binding: callbinding.Config{Challenge: auth.BearerChallenge{
		Realm: cbIssuer, ResourceURL: "https://vmcp.example.com/mcp", Scopes: []string{"mcp"},
	}}})
	token := s.tokens.add("bound", claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))
	session := s.session(t, token)
	requireCallRan(t, s.send(t, http.MethodPost, token, session, toolCallBody(2, cbTool, "{}"), nil))

	resp := s.send(t, http.MethodPost, token, session, toolCallBody(3, cbTool, "{}"), nil)
	require.Equal(t, http.StatusUnauthorized, resp.status)
	assert.Equal(t, `Bearer realm="`+cbIssuer+`", `+
		`resource_metadata="https://vmcp.example.com/.well-known/oauth-protected-resource/mcp", scope="mcp", `+
		`error="invalid_token", error_description="the access token is not accepted (token_reused)"`,
		resp.header.Get("WWW-Authenticate"))
}

func TestCallBindingReturnedCalls(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_900_000_000, 0)
	var calls returnedCalls
	assert.False(t, calls.has("a", now))
	calls.add("a", now.Add(time.Minute), now)
	assert.True(t, calls.has("a", now))
	assert.False(t, calls.has("a", now.Add(time.Minute)), "an entry ends at its expiry")
	calls.add("b", now.Add(2*time.Minute), now.Add(time.Minute))
	assert.Len(t, calls.expires, 1, "an add drops expired entries")
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	vmcpclient "github.com/stacklok/toolhive/pkg/vmcp/client"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// ---------------------------------------------------------------------------
// This file pins the two cross-era "bridge cells" of vMCP's dual-protocol
// dispatch: Modern client -> Legacy backend (Cell A) and Legacy client ->
// Modern backend (Cell B). Both already work; nothing here changes production
// behavior. RFC THV-0083 D6 plans a shared backend connection pool, which
// would silently break Cell A's per-principal isolation if it ever collapsed
// distinct backend sessions -- these tests exist so that change cannot land
// without noticing.
//
// Scope: these pins cover the TRANSPORT/SESSION dimension of isolation only
// (distinct backend connections, no cross-request credential bleed on the
// wire). Credential *derivation* caching (e.g. token-exchange result caching,
// pkg/vmcp/auth/strategies/tokenexchange.go) is a different layer and is not
// exercised here.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Cell A: Modern client -> Legacy backend
// ---------------------------------------------------------------------------

// testIdentityEchoStrategyName is the outgoing-auth strategy name registered
// by identityEchoStrategy below.
const testIdentityEchoStrategyName = "test-identity-echo"

// identityEchoStrategy stamps the calling principal's identity onto every
// outgoing backend request as an Authorization header, so a test can observe
// -- on the wire, via newRecordingBackendProxy -- which principal's
// credential reached the backend on each individual request. The default
// UnauthenticatedStrategy (used by every other real-backend test in this
// package) injects nothing, so there would be nothing to assert; this is the
// minimal seam for exercising the real property under test (the *calling*
// principal's identity reaches outgoing auth on every re-originated call)
// without needing real IdP infrastructure.
type identityEchoStrategy struct{}

func (identityEchoStrategy) Name() string { return testIdentityEchoStrategyName }

func (identityEchoStrategy) Authenticate(ctx context.Context, req *http.Request, _ *authtypes.BackendAuthStrategy) error {
	if identity, ok := auth.IdentityFromContext(ctx); ok && identity != nil {
		req.Header.Set("Authorization", "Bearer test-"+identity.Subject)
	}
	return nil
}

func (identityEchoStrategy) Validate(_ *authtypes.BackendAuthStrategy) error { return nil }

// bridgePrincipalMiddleware stamps a per-request auth.Identity derived from
// the X-Test-Principal header. It duplicates the small stamping technique in
// principalIdentityMiddleware (context_isolation_regression_test.go) rather
// than importing it: that helper is unexported in package server, and this
// file is package server_test so it can reach the real-backend helpers in
// testutil_test.go.
func bridgePrincipalMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal := r.Header.Get("X-Test-Principal")
		if principal == "" {
			next.ServeHTTP(w, r)
			return
		}
		id := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: principal, Claims: map[string]any{"sub": principal}}}
		next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), id)))
	})
}

// recordedBackendRequest captures one HTTP request newRecordingBackendProxy
// forwarded to the real backend, plus the backend's response session id (when
// present), so a test can correlate an initialize call with the tools/call
// that reused its backend session via Mcp-Session-Id.
type recordedBackendRequest struct {
	jsonRPCMethod string
	authorization string
	reqSessionID  string
	respSessionID string
	body          map[string]any
}

// newRecordingBackendProxy starts an httptest.Server that transparently
// forwards every request to backendURL while recording it. This gives the
// test real MCP semantics plus full request observability, without
// hand-rolling a fake MCP server (per the task's design suggestion). Returns
// the proxy's /mcp URL and an accessor for a snapshot of everything recorded
// so far.
// flushWriter flushes the underlying http.ResponseWriter after every Write,
// so a streamed response (the standalone SSE GET below) reaches the client
// as it arrives instead of sitting in net/http's default write buffer.
type flushWriter struct{ w http.ResponseWriter }

func (fw flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if f, ok := fw.w.(http.Flusher); ok {
		f.Flush()
	}
	return n, err
}

func newRecordingBackendProxy(t *testing.T, backendURL string) (string, func() []recordedBackendRequest) {
	t.Helper()

	var mu sync.Mutex
	var records []recordedBackendRequest

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = r.Body.Close()

		var decoded map[string]any
		_ = json.Unmarshal(bodyBytes, &decoded)

		fwdReq, err := http.NewRequestWithContext(r.Context(), r.Method, backendURL, bytes.NewReader(bodyBytes))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fwdReq.Header = r.Header.Clone()

		resp, err := http.DefaultClient.Do(fwdReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		method, _ := decoded["method"].(string)
		mu.Lock()
		records = append(records, recordedBackendRequest{
			jsonRPCMethod: method,
			authorization: r.Header.Get("Authorization"),
			reqSessionID:  r.Header.Get("Mcp-Session-Id"),
			respSessionID: resp.Header.Get("Mcp-Session-Id"),
			body:          decoded,
		})
		mu.Unlock()

		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		// Flush the status line/headers immediately: the standalone SSE GET below
		// can sit with headers sent but no body for a while (streamable.go's
		// hangResponse), and the mcpcompat client's connectSSE blocks until it
		// receives the response headers -- net/http's default buffered writer
		// would otherwise hold them back indefinitely.
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Stream and flush every chunk, don't buffer: after `initialize`, the
		// mcpcompat client opens that persistent standalone SSE GET, which the
		// real backend intentionally never closes. Buffering the full response
		// body before writing anything back would block forever on it, and a
		// plain io.Copy alone would still hold each event back in the same
		// buffered writer. flushWriter flushes after every write.
		_, _ = io.Copy(flushWriter{w}, resp.Body)
	})

	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	getRecords := func() []recordedBackendRequest {
		mu.Lock()
		defer mu.Unlock()
		out := make([]recordedBackendRequest, len(records))
		copy(out, records)
		return out
	}
	return ts.URL + "/mcp", getRecords
}

// newBridgeCellAServer builds a vMCP server with Modern dispatch's kill
// switch ON, routing a single Legacy backend (at proxyURL) through
// identityEchoStrategy so the credential that reaches the backend on each
// call is observable. Mirrors newRealModernTestHandler's construction
// (session_management_realbackend_integration_test.go), diverging only in
// the backend's AuthConfig -- that helper always uses "unauthenticated",
// which would leave nothing for this file's tests to assert.
func newBridgeCellAServer(t *testing.T, proxyURL string) *httptest.Server {
	t.Helper()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	backend := vmcp.Backend{
		ID:            "real-backend",
		Name:          "real-backend",
		BaseURL:       proxyURL,
		TransportType: "streamable-http",
		AuthConfig:    &authtypes.BackendAuthStrategy{Type: testIdentityEchoStrategyName},
	}
	mockBackendRegistry := mocks.NewMockBackendRegistry(ctrl)
	mockBackendRegistry.EXPECT().List(gomock.Any()).Return([]vmcp.Backend{backend}).AnyTimes()
	mockBackendRegistry.EXPECT().Get(gomock.Any(), gomock.Any()).Return(&backend).AnyTimes()

	authReg := vmcpauth.NewDefaultOutgoingAuthRegistry()
	require.NoError(t, authReg.RegisterStrategy(authtypes.StrategyTypeUnauthenticated, strategies.NewUnauthenticatedStrategy()))
	require.NoError(t, authReg.RegisterStrategy(testIdentityEchoStrategyName, identityEchoStrategy{}))

	backendClient, err := vmcpclient.NewHTTPBackendClient(authReg)
	require.NoError(t, err)
	resolver, err := aggregator.NewPriorityConflictResolver([]string{backend.Name})
	require.NoError(t, err)
	agg := aggregator.NewDefaultAggregator(backendClient, resolver, nil, nil)

	rt := router.NewSessionRouter(&vmcp.RoutingTable{})
	srv, err := server.New(
		context.Background(),
		&server.Config{
			Host:                  "127.0.0.1",
			Port:                  0,
			SessionTTL:            5 * time.Minute,
			ModernDispatchEnabled: true,
			SessionFactory:        vmcpsession.NewSessionFactory(authReg),
			Aggregator:            agg,
		},
		rt,
		backendClient,
		mockBackendRegistry,
		nil,
	)
	require.NoError(t, err)

	handler, err := srv.Handler(context.Background())
	require.NoError(t, err)

	ts := httptest.NewServer(bridgePrincipalMiddleware(handler))
	t.Cleanup(ts.Close)
	return ts
}

// postModernToolCallAsPrincipal sends a Modern (2026-07-28) tools/call for
// echo, stamped with X-Test-Principal so bridgePrincipalMiddleware
// authenticates the caller as principal, and carrying the reserved
// io.modelcontextprotocol/* _meta keys a real Modern client sends. Mirrors
// postModern (modern_realbackend_integration_test.go), which has no hook for
// extra headers -- needed here for the principal header.
func postModernToolCallAsPrincipal(t *testing.T, baseURL, principal, input string) *http.Response {
	t.Helper()

	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "echo",
			"arguments": map[string]any{"input": input},
			"_meta": map[string]any{
				"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
				"io.modelcontextprotocol/clientCapabilities": map[string]any{},
			},
		},
	})
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+"/mcp", bytes.NewReader(payload))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "echo")
	req.Header.Set("X-Test-Principal", principal)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// TestRegression_BridgeCellA_PerPrincipalSessionIsolation pins A1: two
// different principals issuing Modern tools/calls, bridged to a Legacy
// backend via dispatchModern -> core.CallTool -> legacyCallTool, must each
// get their OWN backend initialize/session on EVERY call, and each
// synthesized initialize/tools/call must carry that principal's own
// credential -- nothing shared, cached, or reused across principals OR
// across repeat calls from the same principal. legacyCallTool creates a
// fresh MCP client (and runs a fresh initialize) per request and
// `defer c.Close()`s it, so this holds today by construction; RFC THV-0083
// D6's planned shared backend connection pool is the future change that
// could silently break it.
//
// TWO SEQUENTIAL rounds of the same two principals, not one call each: with
// only one call per principal, per-request client creation is never actually
// exercised -- a pool keyed by (backend, principal) looks identical to
// per-request creation on a single call, since there's nothing yet for it to
// reuse. Round 2 gives such a pool its one chance to show up. The nonce is
// decoupled from the principal name ("a1"/"a2" vs "alice") specifically so
// the same principal calling twice is independently verifiable.
func TestRegression_BridgeCellA_PerPrincipalSessionIsolation(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)
	proxyURL, getRecords := newRecordingBackendProxy(t, backendURL)
	ts := newBridgeCellAServer(t, proxyURL)

	type call struct{ principal, nonce string }
	round1 := []call{{"alice", "a1"}, {"bob", "b1"}} // pool cold
	round2 := []call{{"alice", "a2"}, {"bob", "b2"}} // pool warm, if there were one
	calls := append(append([]call{}, round1...), round2...)
	wantCred := map[string]string{
		"a1": "Bearer test-alice", "b1": "Bearer test-bob",
		"a2": "Bearer test-alice", "b2": "Bearer test-bob",
	}

	doRound := func(round []call) {
		t.Helper()
		var wg sync.WaitGroup
		wg.Add(len(round))
		errs := make([]error, len(round))
		statusCodes := make([]int, len(round))
		for i, c := range round {
			i, c := i, c
			go func() {
				defer wg.Done()
				// No require/assert here: FailNow from a worker goroutine only runs
				// Goexit off the test goroutine and misreports (see testing.md).
				resp, err := func() (*http.Response, error) {
					resp := postModernToolCallAsPrincipal(t, ts.URL, c.principal, c.nonce)
					defer resp.Body.Close()
					_, readErr := io.ReadAll(resp.Body)
					return resp, readErr
				}()
				errs[i] = err
				if resp != nil {
					statusCodes[i] = resp.StatusCode
				}
			}()
		}

		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("timeout waiting for concurrent Modern tool calls")
		}
		for i, err := range errs {
			require.NoError(t, err, "principal %q", round[i].principal)
			require.Equal(t, http.StatusOK, statusCodes[i], "principal %q", round[i].principal)
		}
	}

	doRound(round1)
	doRound(round2)

	records := getRecords()
	var initRecords, callRecords []recordedBackendRequest
	for _, r := range records {
		switch r.jsonRPCMethod {
		case "initialize":
			initRecords = append(initRecords, r)
		case "tools/call":
			callRecords = append(callRecords, r)
		}
	}
	// mcpcompat's client library deterministically performs TWO initialize
	// round-trips per legacyCallTool call (a capability pre-flight with empty
	// capabilities, then the real one with elicitation/sampling added) -- so
	// each call produces one SURVIVING session (the one its tools/call
	// reuses) plus one abandoned pre-flight session. Assert "at least one",
	// not "exactly one": the pre-flight count is a library implementation
	// detail this pin does not depend on.
	require.GreaterOrEqual(t, len(initRecords), len(calls), "expected at least one backend initialize per call")
	require.Len(t, callRecords, len(calls), "expected one backend tools/call per call")

	// (a) + (c): every backend session id is distinct -- none reused, across
	// principals OR across repeat calls from the same principal (covers
	// surviving AND abandoned pre-flight sessions alike). A pool keyed by
	// (backend, principal) would reuse alice's round-1 session in round 2 and
	// get caught right here.
	seen := make(map[string]bool, len(initRecords))
	for _, r := range initRecords {
		require.NotEmpty(t, r.respSessionID, "backend initialize response must carry Mcp-Session-Id")
		require.False(t, seen[r.respSessionID], "backend session id %q was reused", r.respSessionID)
		seen[r.respSessionID] = true
	}

	// callByNonce keys each SURVIVING tools/call by its own nonce (not by
	// session id, which a pool could legitimately vary): every call must
	// appear exactly once on the wire.
	callByNonce := make(map[string]recordedBackendRequest, len(callRecords))
	for _, r := range callRecords {
		params, _ := r.body["params"].(map[string]any)
		args, _ := params["arguments"].(map[string]any)
		nonce, _ := args["input"].(string)
		callByNonce[nonce] = r
	}
	require.Len(t, callByNonce, len(calls), "every call's nonce must appear exactly once on the wire")

	// (b) + the primary data-plane check: assert the credential on the
	// tools/call itself (not only the initialize step), keyed by the call's
	// own nonce so the same principal's repeat call in round 2 is checked
	// independently of round 1. This is what actually distinguishes
	// per-request client creation from a (backend, principal)-keyed pool --
	// with only one call per principal there would be nothing to compare a
	// repeat call against.
	for _, c := range calls {
		r := callByNonce[c.nonce]
		assert.Equal(t, wantCred[c.nonce], r.authorization,
			"the tools/call for nonce %q must carry principal %q's own credential", c.nonce, c.principal)
	}

	// Correlate each SURVIVING initialize's Authorization credential with the
	// principal named by the tools/call that reused its backend session id
	// (via Mcp-Session-Id). This catches a SYMMETRIC SWAP at the initialize
	// step too: if two calls' credentials were exchanged, the session-id
	// cardinality check above still passes, but the credential recorded on
	// session X's initialize would not match the principal named by session
	// X's own tools/call. An abandoned pre-flight session (no matching
	// tools/call) has nothing to correlate against and is skipped -- its
	// uniqueness was already checked above.
	nonceBySession := make(map[string]string, len(callByNonce))
	for nonce, r := range callByNonce {
		nonceBySession[r.reqSessionID] = nonce
	}
	checked := 0
	for _, r := range initRecords {
		nonce, ok := nonceBySession[r.respSessionID]
		if !ok {
			continue
		}
		checked++
		assert.Equal(t, wantCred[nonce], r.authorization,
			"backend session %q's initialize must carry the credential of the principal behind nonce %q, not a bled/swapped one",
			r.respSessionID, nonce)
	}
	require.Equal(t, len(calls), checked, "every call's surviving session must have been credential-checked at the initialize step too")
}

// TestRegression_BridgeCellA_NoReservedMetaLeakToLegacyBackend pins A2: a
// Modern client's request carries reserved io.modelcontextprotocol/* _meta
// keys, but vMCP -- not the downstream caller -- is the Legacy backend's MCP
// peer on this hop, and go-sdk v1.7 hard-rejects (HTTP 400) ANY
// legacyRequestPassthroughMetaKeys mirrors pkg/mcp's unexported
// passthroughMetaKeys: the reserved keys StripReservedMeta deliberately does NOT
// remove, because they are end-to-end payload for the original endpoints rather
// than per-hop control. Duplicated here only because the production set is
// unexported; keep the two in sync, and prefer widening the production set over
// adding exemptions here.
var legacyRequestPassthroughMetaKeys = map[string]struct{}{
	mcpparser.ReservedMetaPrefix + "related-task":             {},
	mcpparser.ReservedMetaPrefix + "model-immediate-response": {},
}

// _meta.protocolVersion on a stateful server. legacyCallTool strips them via
// mcpparser.StripReservedMeta before forwarding
// (pkg/vmcp/client/client.go); this asserts the Legacy backend receives NO
// io.modelcontextprotocol/* key in the tools/call request body it actually
// gets. Request path only -- response _meta is issue #5986's scope.
//
// Also carries a non-reserved _meta key ("vmcp.test/nonce") and asserts it
// SURVIVES: proving absence of the reserved keys alone can't distinguish a
// surgical strip from a blanket _meta drop (or the dispatcher simply ceasing
// to forward _meta at all) -- either would also pass the reserved-key-absence
// checks below on a zero value, silently killing real per-request metadata
// (progressToken, trace context) on the Legacy hop.
func TestRegression_BridgeCellA_NoReservedMetaLeakToLegacyBackend(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)
	proxyURL, getRecords := newRecordingBackendProxy(t, backendURL)
	ts := newBridgeCellAServer(t, proxyURL)

	resp, decoded := postModern(t, ts.URL, "tools/call", map[string]any{
		"name":      "echo",
		"arguments": map[string]any{"input": "hello modern"},
		"_meta":     map[string]any{"vmcp.test/nonce": "n1"},
	}, 1, "echo")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "decoded: %+v", decoded)

	records := getRecords()
	var sawToolCall bool
	for _, r := range records {
		if r.jsonRPCMethod != "tools/call" {
			continue
		}
		sawToolCall = true
		params, _ := r.body["params"].(map[string]any)
		meta, _ := params["_meta"].(map[string]any)
		// Scan by prefix rather than against a fixed key list: #6024 replaced the
		// enumerated strip set with ReservedMetaPrefix + a passthrough exemption,
		// so a prefix scan also catches reserved keys added later. Matches the
		// assertion style in modern_envelope_test.go and revision_realbackend_test.go.
		for k := range meta {
			if _, passthrough := legacyRequestPassthroughMetaKeys[k]; passthrough {
				continue
			}
			assert.False(t, strings.HasPrefix(k, mcpparser.ReservedMetaPrefix),
				"Legacy backend must never see reserved Modern _meta key %q on the request path; got %+v", k, meta)
		}
		assert.Equal(t, "n1", meta["vmcp.test/nonce"],
			"the strip must be surgical, not a blanket _meta drop; got %+v", meta)
	}
	require.True(t, sawToolCall, "expected the recording proxy to observe a tools/call request")
}

// ---------------------------------------------------------------------------
// Cell B: Legacy client -> Modern backend
// ---------------------------------------------------------------------------

// bridgeModernBackend is a minimal REAL Modern (2026-07-28) MCP backend used
// to pin Cell B: it speaks the raw Modern wire protocol directly
// (server/discover, tools/list, tools/call), giving real MCP semantics plus
// full request observability -- mirroring
// pkg/vmcp/client/reclassify_test.go's modernDiscoverServer technique (that
// helper, and the writeModernResult/modernReq helpers it uses, are
// unexported in package client and unusable from server_test).
type bridgeModernBackend struct {
	srv *httptest.Server

	mu       sync.Mutex
	requests []string // Mcp-Method header value of every request received, in order
}

func newBridgeModernBackend(t *testing.T) *bridgeModernBackend {
	t.Helper()
	b := &bridgeModernBackend{}
	b.srv = httptest.NewServer(http.HandlerFunc(b.handle))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *bridgeModernBackend) handle(w http.ResponseWriter, r *http.Request) {
	method := r.Header.Get("Mcp-Method")

	b.mu.Lock()
	b.requests = append(b.requests, method)
	b.mu.Unlock()

	if method == "" {
		// No Mcp-Method header: a Legacy `initialize` handshake attempt (or any
		// other Legacy request) against this Modern-only backend. Reject with
		// 404, same as reclassify_test.go's modernDiscoverServer, rather than
		// silently answering it.
		w.WriteHeader(http.StatusNotFound)
		return
	}

	var body struct {
		ID     any             `json:"id"`
		Params json.RawMessage `json:"params"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	switch method {
	case "server/discover":
		b.writeResult(w, body.ID, map[string]any{
			"capabilities":      map[string]any{"tools": map[string]any{}},
			"supportedVersions": []string{mcpparser.MCPVersionModern},
		})
	case "tools/list":
		b.writeResult(w, body.ID, map[string]any{
			"tools": []any{map[string]any{"name": "ping", "inputSchema": map[string]any{"type": "object"}}},
		})
	case "tools/call":
		b.writeResult(w, body.ID, map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "pong"}},
		})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// writeResult writes a Modern JSON-RPC success envelope, wrapping result
// under resultType "complete" -- modernCall (pkg/vmcp/client/modern.go)
// treats any other resultType, or its absence, as NOT this backend's genuine
// response.
func (*bridgeModernBackend) writeResult(w http.ResponseWriter, id any, result map[string]any) {
	if result["resultType"] == nil {
		result["resultType"] = "complete"
	}
	out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

func (b *bridgeModernBackend) requestMethods() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.requests))
	copy(out, b.requests)
	return out
}

func (b *bridgeModernBackend) url() string { return b.srv.URL }

// revisionReporter mirrors the unexported interface of the same name in
// pkg/vmcp/cli (serve.go) that wires WithRevisionLookup in production;
// redeclared here because it is unexported there and this test needs the
// same type assertion against the *vmcpclient.NewHTTPBackendClient it built
// directly, without going through cli.Serve.
type revisionReporter interface {
	CachedRevision(workloadID string) (mcpparser.Revision, bool)
}

// newBridgeCellBServer builds a vMCP server routing a single Modern backend
// through backendClient/sessionFactory, shared by both Cell B tests below.
func newBridgeCellBServer(
	t *testing.T, backend vmcp.Backend, backendClient vmcp.BackendClient, sessionFactory vmcpsession.MultiSessionFactory,
) *httptest.Server {
	t.Helper()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mockBackendRegistry := mocks.NewMockBackendRegistry(ctrl)
	mockBackendRegistry.EXPECT().List(gomock.Any()).Return([]vmcp.Backend{backend}).AnyTimes()
	mockBackendRegistry.EXPECT().Get(gomock.Any(), gomock.Any()).Return(&backend).AnyTimes()

	resolver, err := aggregator.NewPriorityConflictResolver([]string{backend.Name})
	require.NoError(t, err)
	agg := aggregator.NewDefaultAggregator(backendClient, resolver, nil, nil)
	rt := router.NewSessionRouter(&vmcp.RoutingTable{})

	srv, err := server.New(
		context.Background(),
		&server.Config{
			Host:           "127.0.0.1",
			Port:           0,
			SessionTTL:     5 * time.Minute,
			SessionFactory: sessionFactory,
			Aggregator:     agg,
		},
		rt,
		backendClient,
		mockBackendRegistry,
		nil,
	)
	require.NoError(t, err)
	handler, err := srv.Handler(context.Background())
	require.NoError(t, err)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

// TestRegression_BridgeCellB_SubscriptionsListenFingerprintIsValid is a
// positive control for the fingerprint TestRegression_BridgeCellB_
// ModernBackendSetSkipsHandshakeAndStillWorks relies on below: it builds the
// SAME Modern backend and session factory but WITHOUT WithRevisionLookup, so
// the connector is never skipped and always runs go-sdk's Client.Connect().
// Self-validates that subscriptions/listen really is observed against the
// currently-pinned go-sdk version -- if a future go-sdk bump changes that
// (e.g. gating the subscribe on registered handlers, or failing Connect
// before discover completes), this test starts failing and flags the other
// test's fingerprint as stale, instead of both silently passing forever.
func TestRegression_BridgeCellB_SubscriptionsListenFingerprintIsValid(t *testing.T) {
	t.Parallel()

	modernBackend := newBridgeModernBackend(t)
	backend := vmcp.Backend{
		ID:            "modern-backend",
		Name:          "modern-backend",
		BaseURL:       modernBackend.url(),
		TransportType: "streamable-http",
	}

	authReg := vmcpauth.NewDefaultOutgoingAuthRegistry()
	require.NoError(t, authReg.RegisterStrategy(authtypes.StrategyTypeUnauthenticated, strategies.NewUnauthenticatedStrategy()))
	backendClient, err := vmcpclient.NewHTTPBackendClient(authReg)
	require.NoError(t, err)

	// No WithRevisionLookup: the connector always attempts to connect,
	// skip or no skip.
	sessionFactory := vmcpsession.NewSessionFactory(authReg)
	ts := newBridgeCellBServer(t, backend, backendClient, sessionFactory)

	client := NewMCPTestClient(t, ts.URL)
	client.InitializeSession()

	require.Eventually(t, func() bool {
		return slices.Contains(modernBackend.requestMethods(), "subscriptions/listen")
	}, 5*time.Second, 50*time.Millisecond,
		"go-sdk's Connect() must still open subscriptions/listen against a Modern backend when the connector is not skipped")
}

// TestRegression_BridgeCellB_ModernBackendSetSkipsHandshakeAndStillWorks pins
// both Cell B properties against ONE session, since B2 (capabilities/calls
// working) is the direct observable consequence of B1 (the connector being
// skipped) actually mattering -- asserting them separately would just
// duplicate this setup:
//
//   - B1: initOneBackend (pkg/vmcp/session/factory.go) must not attempt to
//     connect against a backend whose cached MCP revision is known Modern.
//     Asserted on the fake backend's own recorded request log two ways: (1)
//     no request ever carries an empty Mcp-Method header (safe even though
//     it never fires -- probeRevision is Modern-first and never sends a raw
//     Legacy initialize, see reclassify_test.go); (2) no subscriptions/listen
//     request is ever observed -- go-sdk's Client.Connect() issues that
//     UNCONDITIONALLY once server/discover negotiates Modern, and nothing
//     else in this path (this test's own priming probe, or the core's
//     per-call Modern client) ever uses a go-sdk Client, so its presence is
//     the fingerprint that the connector ran a full Connect() at all --
//     validated against the currently-pinned SDK by the sibling
//     TestRegression_BridgeCellB_SubscriptionsListenFingerprintIsValid.
//   - B2: despite the connector being skipped (so this session holds no
//     connection for the backend at all), a Legacy client session must still
//     see the backend's tools via tools/list and successfully invoke one via
//     tools/call -- because capability advertisement and calls flow through
//     core.* (the revision-aware per-call client), never through the
//     session's per-backend connections (serve_handlers.go).
func TestRegression_BridgeCellB_ModernBackendSetSkipsHandshakeAndStillWorks(t *testing.T) {
	t.Parallel()

	modernBackend := newBridgeModernBackend(t)
	backend := vmcp.Backend{
		ID:            "modern-backend",
		Name:          "modern-backend",
		BaseURL:       modernBackend.url(),
		TransportType: "streamable-http",
	}

	authReg := vmcpauth.NewDefaultOutgoingAuthRegistry()
	require.NoError(t, authReg.RegisterStrategy(authtypes.StrategyTypeUnauthenticated, strategies.NewUnauthenticatedStrategy()))

	backendClient, err := vmcpclient.NewHTTPBackendClient(authReg)
	require.NoError(t, err)

	// Prime the revision cache to "known Modern", the way the health monitor's
	// first probe (or any earlier call) would in production -- initOneBackend's
	// Modern-skip only fires for an ALREADY-classified backend (see its #5992
	// cold-start-race doc comment). ListCapabilities dispatches through
	// probeRevision on an unprobed backend, which is exactly that probe.
	target := vmcp.BackendToTarget(&backend)
	_, err = backendClient.ListCapabilities(context.Background(), target)
	require.NoError(t, err, "priming probe against the real Modern backend must succeed")

	revisions, ok := backendClient.(revisionReporter)
	require.True(t, ok, "httpBackendClient must expose CachedRevision")
	rev, known := revisions.CachedRevision(target.WorkloadID)
	require.True(t, known && rev == mcpparser.RevisionModern, "backend must be cached Modern before session creation")

	sessionFactory := vmcpsession.NewSessionFactory(authReg, vmcpsession.WithRevisionLookup(revisions.CachedRevision))
	ts := newBridgeCellBServer(t, backend, backendClient, sessionFactory)

	// Legacy client session (real initialize handshake against vMCP).
	client := NewMCPTestClient(t, ts.URL)
	sessionID := client.InitializeSession()

	// B2a: tools/list must expose the Modern backend's tool once session
	// registration (injectCoreSessionCapabilities -> core.ListTools) settles.
	require.Eventually(t, func() bool {
		names := listToolNames(t, ts.URL, sessionID)
		return len(names) == 1 && names[0] == "ping"
	}, 5*time.Second, 50*time.Millisecond, "tools/list should expose the Modern backend's 'ping' tool")

	// B2b: tools/call must reach the Modern backend and succeed.
	toolResp := client.CallTool("ping", map[string]any{})
	defer toolResp.Body.Close()
	toolBody, err := io.ReadAll(toolResp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, toolResp.StatusCode, "body: %s", string(toolBody))

	var rpc struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(toolBody, &rpc), "body: %s", string(toolBody))
	assert.False(t, rpc.Result.IsError)
	require.Len(t, rpc.Result.Content, 1)
	assert.Equal(t, "pong", rpc.Result.Content[0].Text)

	// B1: the session-level connector (backend.NewHTTPConnector) must never
	// have run its own go-sdk Client.Connect() against this backend. Checked
	// two ways:
	//  1. No request ever carries an empty Mcp-Method header (the wire
	//     signature of a raw Legacy request) -- safe even though it should
	//     never fire either way: go-sdk v1.7's client is itself Modern-first
	//     (SEP-2575), so even a "Legacy handshake attempt" against a Modern
	//     backend negotiates via server/discover, not a raw Legacy initialize
	//     (see pkg/vmcp/client/reclassify_test.go's identical observation for
	//     the call-path client).
	//  2. No subscriptions/listen request is ever observed. go-sdk's
	//     Client.Connect() issues that call UNCONDITIONALLY once
	//     server/discover negotiates Modern, and nothing else in this path
	//     (this test's own priming probe, or the core's per-call Modern
	//     client -- both raw modernCall, no go-sdk Client) ever makes it, so
	//     its presence is the fingerprint that the connector ran a full
	//     Connect() at all, skip or no skip. Validated against the
	//     currently-pinned SDK by the sibling
	//     TestRegression_BridgeCellB_SubscriptionsListenFingerprintIsValid.
	for _, m := range modernBackend.requestMethods() {
		assert.NotEmpty(t, m, "backend must never receive a request with no Mcp-Method header")
		assert.NotEqual(t, "subscriptions/listen", m,
			"the session connector must never run go-sdk Client.Connect() against a backend classified Modern")
	}
}

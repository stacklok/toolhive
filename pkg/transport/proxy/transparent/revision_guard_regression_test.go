// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/transport/session"
)

// This file is a SECURITY regression suite: it proves that a client-forgeable
// Modern revision signal (MCP-Protocol-Version header and/or params._meta)
// cannot bypass the Legacy session machinery in tracingTransport.RoundTrip.
// That machinery is deliberately keyed on Mcp-Session-Id PRESENCE, never on
// the classified revision, because revision is derived entirely from
// client-controlled input while Mcp-Session-Id is validated against
// server-side session state. See transparent_proxy.go's RoundTrip guard
// comment and revision_classification_test.go for the classification-only
// coverage this file complements.
//
// Scope: this suite covers single-request Modern-signal forgery only. Batch
// payloads are rejected outright before revision classification (batching was
// removed from MCP in 2025-06-18; see TestRoundTripRejectsBatch and #5745), so
// they can neither carry a Modern signal nor reach the unknown-session guard
// and are out of scope here.

// modernToolsCallBody is a well-formed Modern (2026-07-28) JSON-RPC request:
// not "initialize", a valid non-null id, and params._meta carrying both
// reserved keys required for a nil-error classification. Reused verbatim (and
// with a missing header in one case) across every test below so each test
// starts from a request that ClassifyRevision genuinely accepts as Modern.
const modernToolsCallBody = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":{` +
	`"io.modelcontextprotocol/protocolVersion":"` + mcp.MCPVersionModern + `",` +
	`"io.modelcontextprotocol/clientCapabilities":{}}}}`

// newGuardTransport builds a tracingTransport backed by a spy RoundTripper,
// mirroring the harness in revision_classification_test.go. targetURI is a
// syntactically valid placeholder: backendRecovery.reinitializeAndReplay
// parses it with url.Parse but the spy intercepts every call, so it is never
// actually dialed.
func newGuardTransport(spy http.RoundTripper) (*tracingTransport, *TransparentProxy) {
	p := NewTransparentProxy("127.0.0.1", 0, "http://backend", nil, nil, nil, false, false,
		"streamable-http", nil, nil, "", false)
	return newTracingTransport(spy, p), p
}

// assertClassifiesModernNil is a precondition check, not the regression
// itself: it confirms the given body/header combination really does
// classify as Modern with a nil error. If this fails, the test below it is
// not exercising a forged-Modern scenario at all.
func assertClassifiesModernNil(t *testing.T, tt *tracingTransport, body []byte, protoHeader string) {
	t.Helper()
	method, params, _, singleRequest, _ := tt.parseRPCRequest(body)
	require.True(t, singleRequest, "precondition: body must parse as a single JSON-RPC request")
	meta := mcp.ExtractMeta(params)
	rev, err := mcp.ClassifyRevision(method, meta, protoHeader)
	require.NoError(t, err, "precondition: body must classify Modern with a nil error")
	require.Equal(t, mcp.RevisionModern, rev, "precondition: body must classify as Modern")
}

// TestGuardUnknownSessionFiresDespiteForgedModernRevision is the core
// mutation-check case: a well-formed Modern request with an unknown/stale
// Mcp-Session-Id must still be rejected with the session-not-found response,
// exactly as it would be for a Legacy request (see
// TestRoundTripReturns404ForUnknownSession in backend_routing_test.go).
//
// This test FAILS if the guard in tracingTransport.RoundTrip is ever
// re-keyed on `revision == mcp.RevisionModern` (or any check derived from
// the classified revision) instead of Mcp-Session-Id presence — that would
// let a forged Modern signal skip session validation entirely.
func TestGuardUnknownSessionFiresDespiteForgedModernRevision(t *testing.T) {
	t.Parallel()

	var backendCalled atomic.Bool
	spy := roundTripFunc(func(*http.Request) (*http.Response, error) {
		backendCalled.Store(true)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}, nil
	})
	tt, _ := newGuardTransport(spy)

	assertClassifiesModernNil(t, tt, []byte(modernToolsCallBody), mcp.MCPVersionModern)

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(modernToolsCallBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("MCP-Protocol-Version", mcp.MCPVersionModern)
	req.Header.Set("Mcp-Session-Id", uuid.New().String()) // unknown/stale, never added to the session manager

	resp, err := tt.RoundTrip(req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), `"code":-32001`)
	assert.False(t, backendCalled.Load(),
		"backend must not be contacted: the unknown-session guard must fire regardless of the classified revision")
}

// TestGuardBackendSIDRewriteStillHappensWithForgedModernRevision verifies
// that a well-formed Modern request against a KNOWN session whose metadata
// carries a backend_sid still has its outbound Mcp-Session-Id rewritten to
// that backend SID — the rewrite (like the guard above) is driven by session
// metadata, not by the classified revision. Mirrors the assertion style of
// backend_routing_test.go's TestRoundTripReinitializesOnBackend404 (which
// checks sessionMetadataBackendSID) but observes the header directly via a
// spy, as in revision_classification_test.go.
func TestGuardBackendSIDRewriteStillHappensWithForgedModernRevision(t *testing.T) {
	t.Parallel()

	const backendSID = "backend-assigned-opaque-sid"
	var gotSID atomic.Value
	spy := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotSID.Store(r.Header.Get("Mcp-Session-Id"))
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}, nil
	})
	tt, p := newGuardTransport(spy)

	assertClassifiesModernNil(t, tt, []byte(modernToolsCallBody), mcp.MCPVersionModern)

	clientSID := uuid.New().String()
	sess := session.NewProxySession(clientSID)
	sess.SetMetadata(sessionMetadataBackendSID, backendSID)
	require.NoError(t, p.sessionManager.AddSession(sess))

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(modernToolsCallBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("MCP-Protocol-Version", mcp.MCPVersionModern)
	req.Header.Set("Mcp-Session-Id", clientSID)

	resp, err := tt.RoundTrip(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	assert.Equal(t, backendSID, gotSID.Load(),
		"outbound Mcp-Session-Id must be rewritten to backend_sid even though the request forges a Modern revision signal")
}

// TestGuardReinitRecoveryStillTriggersWithForgedModernRevision verifies that
// the transparent re-initialize-and-replay recovery path (triggered by a 404
// from a known session with a stored init body) still fires for a
// well-formed Modern request. Mirrors backend_routing_test.go's
// TestRoundTripReinitializesOnBackend404, but the request itself carries a
// forged Modern signal to prove recovery is unaffected by classified
// revision.
func TestGuardReinitRecoveryStillTriggersWithForgedModernRevision(t *testing.T) {
	t.Parallel()

	freshSessionID := uuid.New().String()
	var initCalls, otherCalls atomic.Int32
	spy := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"initialize"`) {
			initCalls.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Mcp-Session-Id": []string{freshSessionID}},
				Body:       http.NoBody,
			}, nil
		}
		n := otherCalls.Add(1)
		if n == 1 {
			// First non-initialize forward: simulate the backend pod having
			// lost its in-memory session state.
			return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: http.NoBody}, nil
		}
		// Second non-initialize forward is the replay after re-init.
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}, nil
	})
	tt, p := newGuardTransport(spy)

	assertClassifiesModernNil(t, tt, []byte(modernToolsCallBody), mcp.MCPVersionModern)

	clientSID := uuid.New().String()
	sess := session.NewProxySession(clientSID)
	sess.SetMetadata(sessionMetadataInitBody, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	require.NoError(t, p.sessionManager.AddSession(sess))

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(modernToolsCallBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("MCP-Protocol-Version", mcp.MCPVersionModern)
	req.Header.Set("Mcp-Session-Id", clientSID)

	resp, err := tt.RoundTrip(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"client must see 200 after transparent re-init, even for a forged-Modern request")
	assert.Equal(t, int32(1), initCalls.Load(), "exactly one re-initialize call expected")
	assert.Equal(t, int32(2), otherCalls.Load(), "original forward + replay expected")

	updated, ok := p.sessionManager.Get(normalizeSessionID(clientSID))
	require.True(t, ok, "session should still exist after re-init")
	backendSID, exists := updated.GetMetadataValue(sessionMetadataBackendSID)
	require.True(t, exists)
	assert.Equal(t, freshSessionID, backendSID,
		"backend_sid must be updated by the recovery path regardless of the classified revision")
}

// TestGuardDeleteCleanupStillWorksWithBodyMetaButNoHeader verifies that DELETE
// session cleanup (keyed on Mcp-Session-Id, req.Method and response status —
// see RoundTrip's DELETE cleanup block) still runs when the DELETE body
// carries Modern _meta but no MCP-Protocol-Version header. Note: a DELETE
// that also carried the MCP-Protocol-Version: 2026-07-28 header would be
// rejected with 405 at the header-only methodGate and would never reach
// RoundTrip at all (see method_gate_test.go's "Modern header DELETE gated"
// case) — this test exercises the one shape of forged-Modern DELETE that
// does reach RoundTrip. Mirrors delete_session_test.go's cleanup assertion.
func TestGuardDeleteCleanupStillWorksWithBodyMetaButNoHeader(t *testing.T) {
	t.Parallel()

	spy := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}, nil
	})
	tt, p := newGuardTransport(spy)

	// No MCP-Protocol-Version header: classification relies solely on the
	// reserved _meta keys, per mcp.ClassifyRevision's documented signal rules.
	assertClassifiesModernNil(t, tt, []byte(modernToolsCallBody), "")

	clientSID := uuid.New().String()
	sess := session.NewProxySession(clientSID)
	require.NoError(t, p.sessionManager.AddSession(sess))

	req := httptest.NewRequest(http.MethodDelete, "/mcp", strings.NewReader(modernToolsCallBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", clientSID)
	// Deliberately no MCP-Protocol-Version header.

	resp, err := tt.RoundTrip(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	_, ok := p.sessionManager.Get(normalizeSessionID(clientSID))
	assert.False(t, ok,
		"DELETE session cleanup must still key off Mcp-Session-Id presence even when the body carries Modern _meta")
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/versions"
)

// modernClientName is the clientInfo.name vMCP advertises to backends on Modern
// (2026-07-28) requests, matching the Legacy initialize handshake's client name
// (initializeClient in client.go).
const modernClientName = "toolhive-vmcp"

// modernResultTypeComplete is the sole envelope resultType a single-shot Modern
// call accepts; anything else (e.g. "input_required") is a multi-round retrieval
// this shim does not drive — see errModernInputRequired.
const modernResultTypeComplete = "complete"

// jsonRPCCodeMethodNotFound is the JSON-RPC "method not found" code. Declared
// locally rather than imported (mcpcompat's METHOD_NOT_FOUND is the SDK's wire
// vocabulary; the Modern layer sources its codes independently — see
// pkg/vmcp/server/modern_dispatch.go for the server-side mirror).
const jsonRPCCodeMethodNotFound = -32601

// errWrongEra is returned when a backend's response is NOT a recognized Modern
// (2026-07-28) response: a bare 404/400 with no JSON-RPC error body, an empty or
// non-JSON body, or a 200 carrying a Legacy-shaped result (a JSON-RPC result with
// no "resultType"). It signals the peer does not speak the Modern revision at all.
//
// A -32601 (or any other) carried in a well-formed JSON-RPC error object is NOT
// wrong-era: a valid JSON-RPC error body means the backend IS Modern, so a
// -32601 there surfaces as mcp.ErrMethodNotFound and every other code as an
// ordinary call error.
var errWrongEra = errors.New("backend response is not a Modern (2026-07-28) MCP response")

// errLegacyResponseBody is returned when a Modern request gets a well-formed 200
// JSON-RPC SUCCESS result that is Legacy-shaped (no resultType). Unlike
// errWrongEra (a transport/protocol rejection that proves the backend did NOT
// process the request), a success body means a lenient Legacy backend MAY have
// executed the request — so the caller MUST NOT auto-retry it (double-execution
// of a side-effecting tool). The cache may still be reclassified.
var errLegacyResponseBody = errors.New("backend returned a Legacy-shaped success result (no resultType); it may have executed the request")

// errModernInputRequired is returned when a Modern envelope decodes with a
// resultType other than "complete" (e.g. "input_required"). Multi-round tool
// retrieval is deferred; this shim detects and errors rather than returning a
// blank success.
var errModernInputRequired = errors.New("Modern response requires additional input (multi-round retrieval unsupported)")

// errModernProtocolError wraps a well-formed JSON-RPC error whose code is one of
// the Modern-specific codes (-32020/-32021/-32022): the peer validated our
// Modern headers/_meta and rejected them, so it IS Modern even though the call
// failed. It is a positive Modern signal, distinct from errWrongEra and from a
// generic JSON-RPC error (-32600/-32603, which do not prove Modern). probeRevision
// classifies it as Modern.
var errModernProtocolError = errors.New("modern backend rejected the request with a Modern protocol error")

// modernRequestID supplies monotonically increasing JSON-RPC request ids. Each
// modernCall is a single request/response, so the id only has to be unique
// enough to match a response within one SSE stream.
var modernRequestID atomic.Int64

// modernCall issues a single MCP 2026-07-28 ("Modern") stateless JSON-RPC request
// over HTTP POST and decodes the Modern response envelope into out.
//
// It hand-rolls the Modern wire shape that mcpcompat/go-sdk v1.6.1 cannot express
// (its only no-initialize primitive is the private, Legacy-shaped resumeCall),
// mirroring the server envelope in pkg/vmcp/server/modern_envelope.go: no
// initialize handshake, no Mcp-Session-Id, protocol metadata carried per-request
// in _meta, and a Mcp-Method header on every call.
//
// params may carry a caller "_meta"; its three reserved io.modelcontextprotocol/*
// keys are stripped and vMCP's authoritative values overlaid last (vMCP, not the
// caller, is the backend's MCP peer). name is sent as Mcp-Name only for the
// methods that require it (tools/call, resources/read, prompts/get) and only when
// non-empty. hc is the HTTP client whose transport carries the auth/identity/
// header-forward/trace chain (see buildBackendRoundTripper); modernCall adds no
// transport concerns of its own.
//
// Errors: errWrongEra when the peer is not Modern, mcp.ErrMethodNotFound for a
// valid -32601 error body, errModernInputRequired for a non-"complete" envelope,
// and a wrapped call error for any other JSON-RPC error.
func modernCall(
	ctx context.Context,
	hc *http.Client,
	endpoint, method string,
	params map[string]any,
	name string,
	out any,
) error {
	id := modernRequestID.Add(1)

	reqParams := maps.Clone(params)
	if reqParams == nil {
		reqParams = map[string]any{}
	}
	reqParams["_meta"] = mergeModernMeta(params["_meta"])

	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  reqParams,
	})
	if err != nil {
		return fmt.Errorf("marshaling %s request: %w", method, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", mcpparser.MCPVersionModern)
	// Mcp-Method is required on EVERY Modern request (ValidateHeaderConsistency).
	req.Header.Set("Mcp-Method", method)
	if name != "" && mcpparser.IsNameRequiredMethod(method) {
		req.Header.Set("Mcp-Name", name)
	}
	// Mcp-Session-Id is deliberately never set: Modern is stateless.

	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("sending %s request: %w", method, err)
	}
	defer func() {
		// Drain so the connection can be reused (go-style rule); the readers below
		// may stop early (SSE match) or the body may be a bare error page. Bounded
		// by maxResponseSize so a hostile backend can't stall us on an unbounded
		// drain now that this path is live in production (probeRevision).
		_, _ = io.CopyN(io.Discard, resp.Body, maxResponseSize)
		_ = resp.Body.Close()
	}()

	result, rpcErr, err := readModernEnvelope(resp, id)
	if err != nil {
		return err
	}
	if rpcErr != nil {
		if rpcErr.Code == jsonRPCCodeMethodNotFound {
			return fmt.Errorf("%w: %s", mcp.ErrMethodNotFound, rpcErr.Message)
		}
		if isModernProtocolCode(rpcErr.Code) {
			return fmt.Errorf("%w: %s (rpc code %d)", errModernProtocolError, rpcErr.Message, rpcErr.Code)
		}
		return fmt.Errorf("modern %s: rpc error %d: %s", method, rpcErr.Code, rpcErr.Message)
	}

	// The Modern result is an envelope keyed by resultType (modern_envelope.go).
	// A result with no resultType is a Legacy-shaped body: wrong era.
	var envelope struct {
		ResultType string `json:"resultType"`
	}
	if json.Unmarshal(result, &envelope) != nil {
		return errWrongEra
	}
	switch envelope.ResultType {
	case modernResultTypeComplete:
		// proceed to decode
	case "":
		// A JSON-RPC success result with no resultType is a Legacy-shaped body: a
		// lenient Legacy backend that ignored our Modern headers and executed the
		// request. Distinct from errWrongEra so the caller does not auto-retry.
		return errLegacyResponseBody
	default:
		return fmt.Errorf("%w: resultType=%q", errModernInputRequired, envelope.ResultType)
	}

	if out != nil {
		if err := json.Unmarshal(result, out); err != nil {
			return fmt.Errorf("decoding %s result: %w", method, err)
		}
	}
	return nil
}

// mergeModernMeta strips the reserved io.modelcontextprotocol/* keys from a
// caller-supplied _meta (if any) and overlays vMCP's authoritative values last.
// The caller's _meta is never mutated (maps.Clone).
func mergeModernMeta(callerMeta any) map[string]any {
	meta := map[string]any{}
	if m, ok := callerMeta.(map[string]any); ok {
		meta = maps.Clone(m)
		for _, k := range mcpparser.ReservedModernMetaKeys {
			delete(meta, k)
		}
	}
	for k, v := range mcpparser.ModernRequestMeta(modernClientName, versions.Version) {
		meta[k] = v
	}
	return meta
}

// modernRPCError is the JSON-RPC error object.
type modernRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// modernRPCEnvelope is the outer JSON-RPC response envelope. Method is set only
// on server->client requests/notifications interleaved on an SSE stream, which a
// single-shot client ignores.
type modernRPCEnvelope struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *modernRPCError `json:"error"`
	Method string          `json:"method"`
}

// readModernEnvelope reads the JSON-RPC response matching wantID, handling both
// application/json and text/event-stream bodies (mirroring mcpcompat's
// resume.go readRPCResponse). The body is bounded by maxResponseSize in both
// branches — the 100MB cap otherwise lives only inside the mcp-go client and is
// lost on this raw path. A body that is not a recognized Modern JSON-RPC response
// (empty, non-JSON, or neither result nor error) yields errWrongEra.
func readModernEnvelope(resp *http.Response, wantID int64) (json.RawMessage, *modernRPCError, error) {
	body := io.LimitReader(resp.Body, maxResponseSize)

	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return readModernSSE(body, wantID)
	}

	data, err := io.ReadAll(body)
	if err != nil {
		return nil, nil, fmt.Errorf("reading response body: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil, errWrongEra
	}
	var env modernRPCEnvelope
	if json.Unmarshal(data, &env) != nil {
		return nil, nil, errWrongEra
	}
	if env.Error == nil && len(env.Result) == 0 {
		return nil, nil, errWrongEra
	}
	return env.Result, env.Error, nil
}

// readModernSSE scans an SSE body for the response whose id matches wantID,
// consuming (ignoring) any server->client requests/notifications interleaved on
// the stream. A stream that ends without a matching response yields errWrongEra.
func readModernSSE(body io.Reader, wantID int64) (json.RawMessage, *modernRPCError, error) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue
		}
		var env modernRPCEnvelope
		if json.Unmarshal([]byte(strings.TrimSpace(data)), &env) != nil {
			continue
		}
		if env.Method != "" {
			continue // server->client request/notification; not our response
		}
		if !modernIDMatches(env.ID, wantID) {
			continue
		}
		if env.Error == nil && len(env.Result) == 0 {
			return nil, nil, errWrongEra
		}
		return env.Result, env.Error, nil
	}
	if err := sc.Err(); err != nil {
		return nil, nil, fmt.Errorf("reading SSE stream: %w", err)
	}
	return nil, nil, errWrongEra
}

// modernIDMatches reports whether the raw JSON id equals wantID.
func modernIDMatches(raw json.RawMessage, wantID int64) bool {
	if len(raw) == 0 {
		return false
	}
	var n int64
	return json.Unmarshal(raw, &n) == nil && n == wantID
}

// isModernProtocolCode reports whether code is one of the Modern-specific
// JSON-RPC error codes (header/meta validation, -3202x). Only these prove the
// peer is Modern; generic JSON-RPC codes (-32600/-32601/-32603) do not, since a
// Legacy backend also returns them.
func isModernProtocolCode(code int) bool {
	switch int64(code) {
	case mcpparser.CodeHeaderMismatch,
		mcpparser.CodeMissingClientCapability,
		mcpparser.CodeUnsupportedProtocolVersion:
		return true
	default:
		return false
	}
}

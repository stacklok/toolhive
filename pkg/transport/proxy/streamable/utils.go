// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package streamable

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"golang.org/x/exp/jsonrpc2"
)

// sseKeepAliveInterval is the cadence at which an otherwise-idle SSE stream
// writes a ": keep-alive\n\n" comment (see writeSSEKeepAlive), so
// intermediary proxies/load balancers do not close the connection for
// inactivity.
const sseKeepAliveInterval = 30 * time.Second

// isNotification returns true if the JSON-RPC message is a notification (no ID).
func isNotification(msg jsonrpc2.Message) bool {
	if req, ok := msg.(*jsonrpc2.Request); ok {
		return req.ID.Raw() == nil
	}
	return false
}

// isClientResponse returns true if msg is a *jsonrpc2.Response, i.e. a
// client's reply to a server-initiated request (see
// rejectServerRequestToBackend) rather than a client-initiated request or
// notification.
func isClientResponse(msg jsonrpc2.Message) bool {
	_, ok := msg.(*jsonrpc2.Response)
	return ok
}

// writeHTTPError writes a plain HTTP error with status.
func writeHTTPError(w http.ResponseWriter, status int, msg string) {
	http.Error(w, msg, status)
}

// writeSSEData writes data as a single SSE "data:" event to w and flushes it.
// It returns the underlying write error (if any) so the caller can decide
// whether to end the stream; it does not itself log or close anything.
func writeSSEData(w io.Writer, flusher http.Flusher, data []byte) error {
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil { //nolint:gosec // G705: SSE data from MCP protocol
		return err
	}
	flusher.Flush()
	return nil
}

// assertFlushable type-asserts w as http.Flusher, which every SSE response in
// this package requires so data can be pushed to the client as it is
// written, rather than buffered until the handler returns. On failure it
// writes the standard 500 "Streaming not supported" error to w and returns
// (nil, false); the caller must return immediately without writing anything
// else. On success it returns (w.(http.Flusher), true).
func assertFlushable(w http.ResponseWriter) (http.Flusher, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeHTTPError(w, http.StatusInternalServerError, "Streaming not supported")
		return nil, false
	}
	return flusher, true
}

// setSSEHeaders sets the standard response headers for an SSE stream:
// Content-Type: text/event-stream, Cache-Control: no-cache, and
// Connection: keep-alive.
func setSSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
}

// startSSEStream asserts that w supports flushing (see assertFlushable), sets
// the standard SSE headers (see setSSEHeaders), and flushes them to the
// client immediately so it knows the stream is open without waiting for the
// first data: frame.
//
// If extra is non-nil, it is called after the SSE headers are set but before
// the flush, letting a caller add further response headers (e.g.
// Mcp-Session-Id) while they are still guaranteed to reach the client:
// net/http locks in headers at the first Flush/WriteHeader call, so any
// header set after this function returns would be silently dropped.
//
// It returns (flusher, true) on success. If w does not implement
// http.Flusher, it writes the "Streaming not supported" 500 itself (see
// assertFlushable) and returns (nil, false); the caller must return without
// writing anything else, and extra is never called.
func startSSEStream(w http.ResponseWriter, extra func()) (http.Flusher, bool) {
	flusher, ok := assertFlushable(w)
	if !ok {
		return nil, false
	}
	setSSEHeaders(w)
	if extra != nil {
		extra()
	}
	flusher.Flush()
	return flusher, true
}

// writeSSEKeepAlive writes a single SSE comment line (": keep-alive\n\n") to
// w and flushes it, so intermediary proxies/load balancers do not close an
// otherwise-idle SSE connection for inactivity. It returns the underlying
// write error (if any), mirroring writeSSEData, so the caller can decide
// whether to end the stream; it does not itself log or close anything.
func writeSSEKeepAlive(w io.Writer, flusher http.Flusher) error {
	if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil { //nolint:gosec // G705: fixed literal, not derived from any request
		return err
	}
	flusher.Flush()
	return nil
}

// writeJSONRPC writes a jsonrpc2.Message using the library's encoder to ensure proper serialization.
func writeJSONRPC(w http.ResponseWriter, msg jsonrpc2.Message) error {
	w.Header().Set("Content-Type", "application/json")
	data, err := jsonrpc2.EncodeMessage(msg)
	if err != nil {
		return err
	}
	_, err = w.Write(data) //nolint:gosec // G705: data is JSON-RPC from MCP protocol
	return err
}

// idKeyFromID returns a stable string key for a jsonrpc2.ID using its raw value.
// We prefix with type to avoid collisions between numeric and string IDs.
func idKeyFromID(id jsonrpc2.ID) string {
	raw := id.Raw()
	switch v := raw.(type) {
	case string:
		return "s:" + v
	case float64:
		// JSON numbers decode to float64
		return fmt.Sprintf("n:%v", v)
	case json.Number:
		return "n:" + v.String()
	case nil:
		return "nil"
	default:
		return fmt.Sprintf("%T:%v", v, v)
	}
}

// compositeKey builds a stable composite key from session ID and request ID key.
func compositeKey(sessID, idKey string) string {
	return sessID + "|" + idKey
}

// extractStringParam returns params[key] as a string, and whether it was
// present and string-typed. Absent params, params that don't decode as a JSON
// object, or a non-string value at key all yield ("", false) rather than an
// error -- callers treat a missing/malformed field as "nothing to route on".
func extractStringParam(params json.RawMessage, key string) (string, bool) {
	if len(params) == 0 {
		return "", false
	}
	var m map[string]any
	if err := json.Unmarshal(params, &m); err != nil {
		return "", false
	}
	s, ok := m[key].(string)
	return s, ok
}

// rewriteRequestParam returns a new *jsonrpc2.Request identical to req except
// that its top-level params[key] is set to value (params is created if req
// had none). It never mutates req: per the "copy before mutating caller
// input" style rule, the params map is freshly decoded from req.Params before
// the assignment, so no caller-held reference (including req itself) is
// modified.
func rewriteRequestParam(req *jsonrpc2.Request, key string, value any) (*jsonrpc2.Request, error) {
	m := map[string]any{}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &m); err != nil {
			return nil, err
		}
	}
	m[key] = value
	data, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return &jsonrpc2.Request{ID: req.ID, Method: req.Method, Params: data}, nil
}

// isSupportedMCPVersion is intentionally permissive: we accept any present version string.
// This avoids being pedantic and breaking on new protocol dates while remaining compliant,
// since this proxy is transport-level and does not depend on specific MCP versions.
func isSupportedMCPVersion(_ string) bool {
	return true
}

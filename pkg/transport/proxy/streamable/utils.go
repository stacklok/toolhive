// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package streamable

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"golang.org/x/exp/jsonrpc2"
)

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

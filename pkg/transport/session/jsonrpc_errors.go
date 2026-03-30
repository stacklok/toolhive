// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const (
	// CodeSessionNotFound is the JSON-RPC error code for expired or unknown sessions.
	// This matches the MCP TypeScript SDK reference server convention and falls within
	// the JSON-RPC 2.0 implementation-defined server-errors range (-32000 to -32099).
	// MCP clients use this code to trigger automatic session recovery.
	CodeSessionNotFound int64 = -32001

	MessageSessionNotFound = "Session not found"
)

// NotFoundBody returns the JSON-encoded body for a session-not-found
// JSON-RPC error response. The requestID is the "id" from the incoming
// JSON-RPC request; pass nil when the request ID is not available (e.g.,
// DELETE requests, batch pre-parse, transparent proxy).
func NotFoundBody(requestID any) []byte {
	resp := map[string]any{
		"jsonrpc": "2.0",
		"error": map[string]any{
			"code":    CodeSessionNotFound,
			"message": MessageSessionNotFound,
		},
		"id": requestID,
	}
	data, err := json.Marshal(resp)
	if err != nil {
		// This should never happen with simple map types, but return a
		// hand-crafted fallback to guarantee a valid JSON-RPC error.
		return []byte(`{"jsonrpc":"2.0","error":{"code":-32001,"message":"Session not found"},"id":null}`)
	}
	return data
}

// WriteNotFound writes an HTTP 404 response with a JSON-RPC error body
// for session-not-found. Use this with http.ResponseWriter in the streamable
// HTTP and SSE proxies.
func WriteNotFound(w http.ResponseWriter, requestID any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	//nolint:gosec // G104: writing a static JSON error response to an HTTP client
	_, _ = w.Write(NotFoundBody(requestID))
}

// NotFoundResponse constructs an *http.Response with HTTP 404 and a
// JSON-RPC error body. Use this in httputil.ReverseProxy.ModifyResponse
// (transparent proxy) where no http.ResponseWriter is available.
func NotFoundResponse(req *http.Request) *http.Response {
	body := NotFoundBody(nil)
	hdr := make(http.Header)
	hdr.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode:    http.StatusNotFound,
		Status:        fmt.Sprintf("%d %s", http.StatusNotFound, http.StatusText(http.StatusNotFound)),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        hdr,
		ContentLength: int64(len(body)),
		Body:          io.NopCloser(bytes.NewReader(body)),
		Request:       req,
	}
}

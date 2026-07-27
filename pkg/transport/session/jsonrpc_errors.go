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

	// MessageSessionNotFound is the JSON-RPC error message for session-not-found.
	MessageSessionNotFound = "Session not found"
)

// NotFoundBody returns the JSON-encoded body for a session-not-found
// JSON-RPC error response. The requestID is the "id" from the incoming
// JSON-RPC request, echoed so a client correlating by id can match this error
// to the request that caused it.
//
// MCP narrows base JSON-RPC 2.0 here: schema/2025-11-25 types the error
// response as `id?: RequestId` where RequestId = string | number, so an
// absent id is encoded by omitting the "id" key entirely, never as null. The
// reference TypeScript SDK enforces this with a .strict() schema and a
// throwing parse(), so an "id":null response crashes a conformant client's
// transport. See HasJSONRPCID.
//
// Pass nil only when the request genuinely carries no id: a bodiless GET
// (standalone SSE) or DELETE, a notification, or a batch. Do NOT pass nil
// merely because threading the id to the call site is inconvenient — that
// was the asymmetry fixed in #5945.
func NotFoundBody(requestID any) []byte {
	resp := map[string]any{
		"jsonrpc": "2.0",
		"error": map[string]any{
			"code":    CodeSessionNotFound,
			"message": MessageSessionNotFound,
		},
	}
	if HasJSONRPCID(requestID) {
		resp["id"] = requestID
	}
	data, err := json.Marshal(resp)
	if err != nil {
		// This should never happen with simple map types, but return a
		// hand-crafted fallback to guarantee a valid JSON-RPC error.
		return []byte(`{"jsonrpc":"2.0","error":{"code":-32001,"message":"Session not found"}}`)
	}
	return data
}

// HasJSONRPCID reports whether requestID is a usable JSON-RPC id, i.e. whether
// callers should emit an "id" key at all.
//
// MCP narrows base JSON-RPC here: schema/2025-11-25 types the error response as
// `id?: RequestId` where RequestId = string | number, so an absent id is encoded
// by omitting the key, never as null. The reference TypeScript SDK enforces this
// with a .strict() schema and a throwing parse(), so "id":null crashes a
// conformant client's transport.
//
// The transparent proxy threads the incoming id through as raw bytes, so a
// json.RawMessage holding literal null -- or nothing -- is an absent id even
// though the interface value is non-nil.
func HasJSONRPCID(requestID any) bool {
	if requestID == nil {
		return false
	}
	if raw, ok := requestID.(json.RawMessage); ok {
		return len(raw) > 0 && string(raw) != "null"
	}
	return true
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
//
// requestID follows NotFoundBody: pass the incoming request's JSON-RPC id so the
// error echoes it, or nil when the request has none. Callers that reject a
// request before parsing a body (GET/DELETE) pass nil.
func NotFoundResponse(req *http.Request, requestID any) *http.Response {
	body := NotFoundBody(requestID)
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

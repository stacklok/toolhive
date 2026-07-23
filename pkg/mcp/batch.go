// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"net/http"
)

// batchUnsupported is the shared error rendered by both batch-rejection helpers.
var batchUnsupported = &BatchUnsupportedError{}

// BatchUnsupportedError indicates a client sent a JSON-RPC batch (a top-level
// JSON array). Batching was removed from MCP in the 2025-06-18 revision, and
// ToolHive serves only 2025-11-25 and 2026-07-28, so a batch can only originate
// from an unsupported pre-2025-06-18 client.
//
// Batches are rejected outright rather than executed: the parser and the
// downstream authz, audit, and tool-filter middleware each inspect a single
// parsed request per call, so a batch that reached the backend would smuggle
// every nested call past those controls (see #5745).
type BatchUnsupportedError struct{}

func (*BatchUnsupportedError) Error() string {
	return "JSON-RPC batch requests are not supported (batching was removed in MCP revision 2025-06-18)"
}

// Code implements CodedError.
func (*BatchUnsupportedError) Code() int64 { return CodeInvalidRequest }

// Data implements CodedError.
func (*BatchUnsupportedError) Data() map[string]any { return nil }

// IsBatchRequest reports whether body is a JSON-RPC batch, i.e. a top-level JSON
// array. Only the first non-whitespace byte is inspected, so a syntactically
// malformed array (e.g. "[...") still classifies as a batch and is rejected
// rather than silently mis-parsed as a single message.
//
// It trims the same Unicode whitespace as encoding/json (via bytes.TrimSpace) so
// callers that gate execution on this function and callers that later
// json-decode the body agree on what counts as a leading '[' — a narrower trim
// would let a batch prefixed with, say, a vertical-tab slip past detection yet
// still decode as an array. Every path that could forward a batch to a backend
// must gate on this one function so the two never drift (see #5745).
func IsBatchRequest(body []byte) bool {
	trimmed := bytes.TrimSpace(body)
	return len(trimmed) > 0 && trimmed[0] == '['
}

// WriteBatchUnsupportedError writes an HTTP 400 response carrying a JSON-RPC
// "Invalid Request" error for a rejected batch. The JSON-RPC id is null: a
// batch has no single request id to echo. Use this where an http.ResponseWriter
// is available (the streamable proxy, ParsingMiddleware).
func WriteBatchUnsupportedError(w http.ResponseWriter) {
	body := classificationErrorBody(nil, batchUnsupported)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	//nolint:gosec // G104: writing a JSON-RPC error response to an HTTP client
	_, _ = w.Write(body)
}

// BatchUnsupportedResponse builds an *http.Response carrying the batch-rejection
// JSON-RPC error, for proxies that intercept at the RoundTripper layer (the
// transparent proxy) where no http.ResponseWriter is available. Mirrors
// ClassificationErrorResponse; the JSON-RPC id is null.
func BatchUnsupportedResponse(req *http.Request) *http.Response {
	return jsonRPCErrorResponse(req, nil, batchUnsupported)
}

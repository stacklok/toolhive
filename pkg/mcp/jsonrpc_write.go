// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"log/slog"
	"net/http"

	"golang.org/x/exp/jsonrpc2"
)

// EncodeJSONRPCError returns the wire encoding of a jsonrpc2 error response.
//
// This is the single sanctioned way to serialize a jsonrpc2 envelope in this
// codebase. NEVER json.Marshal (or json.NewEncoder().Encode) a jsonrpc2 type:
// jsonrpc2.Response carries no json tags and jsonrpc2.ID's only field is
// unexported, so reflection produces Go-capitalized keys, drops the mandatory
// "jsonrpc":"2.0" tag, and renders every id — valid or not — as "{}" (#5950).
// jsonrpc2.EncodeMessage marshals through the library's tagged wire struct:
// lowercase keys, the version tag stamped unconditionally, and an id that is
// omitted when absent (never null, which MCP cannot express and the reference
// TypeScript SDK client rejects, #6038) while a legitimate id of 0 survives.
//
// EncodeMessage cannot fail for a Response built from an ID and a
// jsonrpc2.NewError; if it somehow does, this logs and falls back to a
// hardcoded, conformant Internal Error body rather than returning nothing —
// the original code and id are not trustworthy at that point.
func EncodeJSONRPCError(resp *jsonrpc2.Response) []byte {
	body, err := jsonrpc2.EncodeMessage(resp)
	if err != nil {
		slog.Error("failed to encode JSON-RPC error response", "error", err)
		return []byte(`{"jsonrpc":"2.0","error":{"code":-32603,"message":"Internal error"}}`)
	}
	return body
}

// WriteJSONRPCError writes resp as an HTTP response: the JSON-RPC error body
// under Content-Type application/json with the given HTTP status. The body is
// encoded before any header is written, so an encode failure never leaves a
// half-written response (see EncodeJSONRPCError for the fallback). Returns
// the body write error; callers with no recovery path may discard it.
//
// Do not use this to write into an SSE stream — a bare JSON object in a
// text/event-stream parses as an unrecognized field and is silently
// discarded (#6037); SSE paths must frame the body as an event instead.
func WriteJSONRPCError(w http.ResponseWriter, httpStatus int, resp *jsonrpc2.Response) error {
	body := EncodeJSONRPCError(resp)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	_, err := w.Write(body)
	return err
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import "net/http"

// JSONRPCCodeDenied is the application-space JSON-RPC error code ToolHive uses
// when a policy denies a call, alongside HTTP 403. Both denial paths reference
// this constant — the single-server authorization middleware
// (pkg/authz.handleUnauthorized) and the vMCP Serve-path call gate — so a denial
// is represented by one code across `thv run` and vMCP, by reference rather than
// by two copies of the literal. It is deliberately outside the reserved
// -32768..-32000 JSON-RPC range so it never collides with an SDK-generated code.
// It is also deliberately untyped: it is consumed both as an int
// (server.Denial.Code, map[string]any envelopes) and as an int64
// (jsonrpc2.NewError, JSONRPCCodeForStatus). Typing it would break
// assert.Equal sites on one side.
const JSONRPCCodeDenied = 403

// JSONRPCCodeForStatus maps an HTTP status code to the JSON-RPC error code
// that belongs in a response body's error.code field. The HTTP status and the
// JSON-RPC code are independent dimensions: ToolHive's error paths pick an
// HTTP status per failure class, and each class has exactly one correct
// JSON-RPC code. This function is the single place that mapping lives, so
// callers never pass an HTTP status straight through as the JSON-RPC code.
//
// http.StatusUnprocessableEntity maps to JSONRPCCodeDenied because ToolHive's
// mutating-webhook path relays a webhook's HTTP 422 always-deny response
// (webhook.IsAlwaysDenyError) as HTTP 422 downstream — the validating path
// relays the same condition as 403. So a 422 arriving here is semantically a
// denial.
//
// Any status with no explicit case below maps to CodeInternalError, so an
// unmapped status fails safe to a standard code instead of leaking a raw HTTP
// status into the JSON-RPC code field.
func JSONRPCCodeForStatus(status int) int64 {
	switch status {
	case http.StatusForbidden, http.StatusUnprocessableEntity:
		return JSONRPCCodeDenied
	case http.StatusRequestEntityTooLarge:
		return CodeInvalidRequest
	default:
		return CodeInternalError
	}
}

// CodedError is implemented by domain errors that should surface a stable error
// code and optional structured data in an MCP tool result.
//
// The mcp-go tool-handler seam maps returned Go errors to a generic JSON-RPC
// INTERNAL_ERROR, so Serve-path handlers convert these errors to IsError tool
// results with StructuredContent instead of returning them as handler errors.
type CodedError interface {
	error
	Code() int64
	Data() map[string]any
}

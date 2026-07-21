// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

// JSONRPCCodeDenied is the application-space JSON-RPC error code ToolHive uses
// when a policy denies a call, alongside HTTP 403. Both denial paths reference
// this constant — the single-server authorization middleware
// (pkg/authz.handleUnauthorized) and the vMCP Serve-path call gate — so a denial
// is represented by one code across `thv run` and vMCP, by reference rather than
// by two copies of the literal. It is deliberately outside the reserved
// -32768..-32000 JSON-RPC range so it never collides with an SDK-generated code.
const JSONRPCCodeDenied = 403

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

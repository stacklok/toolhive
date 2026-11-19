// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package conversion

import (
	"errors"

	sdkmcp "github.com/stacklok/toolhive-core/mcpcompat/mcp"
	thvmcp "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// ErrorToToolResult converts domain/tool execution errors into MCP tool results.
//
// A tool handler can carry a custom JSON-RPC code by returning a bare
// *jsonrpc.Error, but coded domain errors (CodedError) render as an isError
// result by choice: the code and data go in StructuredContent, so a model
// reading the result sees them and the call stays a successful JSON-RPC
// response that clients and audit treat like any other tool failure.
//
// This is the Legacy (2025-11-25) render seam for tools/call: the Serve-path
// tool handler and the optimizer call_tool meta-tool both route errors here.
// The one Legacy answer that is a JSON-RPC error, -32042 for a URL
// elicitation on a session that negotiated 2025-11-25 and declared
// elicitation.url, is rendered by the caller before it reaches this function,
// because only the caller can see the session. A URL elicitation answer
// (vmcp.URLElicitationRequiredError) that reaches this function renders as an
// isError result whose text is its LegacyFallback: the elicitation message,
// never the URL, unless the embedder supplied its own text.
func ErrorToToolResult(err error) *sdkmcp.CallToolResult {
	// Denial first (matches writeModernDispatchError): an error that is both a
	// CodedError and wraps ErrAuthorizationFailed must render as the denial,
	// never as retry-shaped coded data (the sets are disjoint today; this
	// ordering is the invariant).
	if errors.Is(err, vmcp.ErrAuthorizationFailed) {
		return sdkmcp.NewToolResultError(vmcp.DenyMessageToolCall)
	}
	if answer, ok := vmcp.URLElicitationFromError(err); ok {
		return urlElicitationFallbackResult(answer)
	}
	var coded thvmcp.CodedError
	if errors.As(err, &coded) {
		return CodedErrorResult(err, coded)
	}
	return sdkmcp.NewToolResultError(err.Error())
}

// CodedErrorResult builds an IsError tool result carrying stable machine-readable
// error details in StructuredContent. message uses err.Error() so callers can wrap
// a coded error with context using %w without losing the code or data.
func CodedErrorResult(err error, coded thvmcp.CodedError) *sdkmcp.CallToolResult {
	result := sdkmcp.NewToolResultError(err.Error())
	structured := map[string]any{
		"code":    coded.Code(),
		"message": err.Error(),
	}
	if data := coded.Data(); len(data) > 0 {
		structured["data"] = data
	}
	result.StructuredContent = structured
	return result
}

// urlElicitationFallbackResult renders the URL elicitation answer for a
// client that cannot receive a URL elicitation: answer.LegacyFallback. By
// default that is the elicitation messages (answer.Message, the same text
// Error embeds), without the URL, so a model reading the result cannot follow
// a link it never sees and skip the user consent step URL mode exists for.
// An embedder that supplied its own text with WithLegacyFallback chose what
// the result carries.
func urlElicitationFallbackResult(answer *vmcp.URLElicitationRequiredError) *sdkmcp.CallToolResult {
	return sdkmcp.NewToolResultError(answer.LegacyFallback())
}

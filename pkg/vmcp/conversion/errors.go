// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package conversion

import (
	"errors"

	sdkmcp "github.com/stacklok/toolhive-core/mcpcompat/mcp"
	thvmcp "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// ErrorToToolResult converts domain/tool execution errors into MCP tool results.
//
// A tool handler can carry a custom JSON-RPC code by returning a bare
// *jsonrpc.Error, but coded domain errors (CodedError) render as an isError
// result by choice: the code and data go in StructuredContent, so a model
// reading the result sees them and the call stays a successful JSON-RPC
// response that clients and audit treat like any other tool failure.
//
// This is the Legacy (2025-11-25) render seam for tool-result failures. The
// direct tool handler (legacyToolCallError in pkg/vmcp/server) returns bare
// JSON-RPC errors instead for a URL elicitation on a session that negotiated
// 2025-11-25 and declared elicitation.url (-32042) and for an unavailable
// delegated credential (-32603). The optimizer call_tool meta-tool passes
// those wire errors through unchanged.
//
// The precedence is the one every tools/call renderer uses: an authorization
// denial renders as the deny message; then a URL elicitation answer
// (vmcp.URLElicitationRequiredError) renders as its LegacyFallback, the
// elicitation message, never the URL, unless the embedder supplied its own
// text; then a delegated authorization requirement renders as its own text,
// which names only the provider.
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
	var authReq *authtypes.DelegatedAuthRequiredError
	if errors.As(err, &authReq) && authReq != nil {
		return sdkmcp.NewToolResultError(authReq.Error())
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

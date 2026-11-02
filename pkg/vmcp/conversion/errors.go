// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package conversion

import (
	"errors"
	"strings"

	sdkmcp "github.com/stacklok/toolhive-core/mcpcompat/mcp"
	thvmcp "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// ErrorToToolResult converts domain/tool execution errors into MCP tool results.
//
// mcp-go tool handlers cannot reliably carry custom JSON-RPC codes by returning
// Go errors: the SDK maps them to generic internal errors. For domain errors
// that opt into CodedError, preserve the code/data in StructuredContent instead.
//
// This is the Legacy (2025-11-25) render seam for tools/call: the Serve-path
// tool handler and the optimizer call_tool meta-tool both route errors here.
// A URL elicitation answer (vmcp.URLElicitationRequiredError) renders as an
// isError result whose text is the elicitation message, never the URL. The
// spec's Legacy answer, -32042, needs the session's declared capabilities,
// which the SDK seam does not expose yet (#7023).
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
// client that cannot receive a URL elicitation: the elicitation messages,
// joined, without the URL. A model reading the result cannot follow a link
// it never sees, so it cannot skip the user consent step URL mode exists for.
func urlElicitationFallbackResult(answer *vmcp.URLElicitationRequiredError) *sdkmcp.CallToolResult {
	elicitations := answer.Elicitations()
	messages := make([]string, 0, len(elicitations))
	for _, e := range elicitations {
		messages = append(messages, e.Message())
	}
	return sdkmcp.NewToolResultError(strings.Join(messages, "\n"))
}

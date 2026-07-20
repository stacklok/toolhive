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
// mcp-go tool handlers cannot reliably carry custom JSON-RPC codes by returning
// Go errors: the SDK maps them to generic internal errors. For domain errors
// that opt into CodedError, preserve the code/data in StructuredContent instead.
func ErrorToToolResult(err error) *sdkmcp.CallToolResult {
	var coded thvmcp.CodedError
	if errors.As(err, &coded) {
		return CodedErrorResult(err, coded)
	}
	if errors.Is(err, vmcp.ErrAuthorizationFailed) {
		return sdkmcp.NewToolResultError(vmcp.DenyMessageToolCall)
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
	if data := coded.Data(); data != nil {
		structured["data"] = data
	}
	result.StructuredContent = structured
	return result
}

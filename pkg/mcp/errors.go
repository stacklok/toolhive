// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

// RequestError is implemented by domain errors that should fail the MCP request
// instead of being converted to a successful tool result with IsError=true.
//
// The mcp-go tool-handler seam maps returned errors to JSON-RPC INTERNAL_ERROR,
// so this is a control-flow marker, not a custom JSON-RPC code hook.
type RequestError interface {
	error
	MCPRequestError()
}

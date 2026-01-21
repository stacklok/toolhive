// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package server implements the Virtual MCP Server that aggregates
// multiple backend MCP servers into a unified interface.
package server

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/stacklok/toolhive/pkg/vmcp/composer"
)

// sdkElicitationAdapter wraps mark3labs MCPServer to implement composer.SDKElicitationRequester.
//
// This adapter bridges the gap between the server's SDK instance and the workflow engine's
// SDK-agnostic elicitation handler. It enables:
//   - Migration to official SDK without changing workflow code
//   - Decoupling of workflow engine from specific SDK implementation
//   - Testability via mock implementations
//
// Per MCP 2025-06-18 spec: The SDK handles JSON-RPC ID correlation internally.
// Our adapter is a simple pass-through that doesn't need to manage IDs.
//
// Thread-safety: Safe for concurrent calls. The mark3labs MCPServer is thread-safe.
type sdkElicitationAdapter struct {
	// mcpServer is the mark3labs SDK server instance that handles elicitation protocol.
	mcpServer *server.MCPServer
}

// newSDKElicitationAdapter creates a new elicitation adapter that wraps the mark3labs SDK server.
//
// The returned adapter implements composer.SDKElicitationRequester by delegating to the
// SDK's RequestElicitation method. Session management and JSON-RPC ID correlation are
// handled entirely by the SDK.
func newSDKElicitationAdapter(mcpServer *server.MCPServer) composer.SDKElicitationRequester {
	return &sdkElicitationAdapter{
		mcpServer: mcpServer,
	}
}

// RequestElicitation delegates to the mark3labs SDK's RequestElicitation method.
//
// This is a synchronous blocking call that:
//  1. Forwards the request to the mark3labs SDK
//  2. Blocks until the client responds or timeout occurs
//  3. Returns the response from the SDK
//
// The SDK handles all protocol details internally:
//   - JSON-RPC ID generation and correlation
//   - Session routing (ensures request reaches correct client)
//   - Error handling and timeout management
//
// Per MCP 2025-06-18 spec: Elicitation is a synchronous request/response protocol.
// The server sends a request and blocks until the client responds.
//
// Returns ElicitationResult from the SDK or error if the request fails, times out,
// or the user declines/cancels.
func (a *sdkElicitationAdapter) RequestElicitation(
	ctx context.Context,
	request mcp.ElicitationRequest,
) (*mcp.ElicitationResult, error) {
	// Delegate to the mark3labs SDK's RequestElicitation method.
	// The SDK will:
	//   1. Extract session ID from context (set by SDK middleware)
	//   2. Generate JSON-RPC ID for the request
	//   3. Send elicitation request to client via transport
	//   4. Block until response received or timeout
	//   5. Correlate response to request using JSON-RPC ID
	//   6. Return result to us
	//
	// We don't need to manage any of this - it's all handled by the SDK.
	return a.mcpServer.RequestElicitation(ctx, request)
}

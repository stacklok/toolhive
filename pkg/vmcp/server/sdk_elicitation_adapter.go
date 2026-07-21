// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package server implements the Virtual MCP Server that aggregates
// multiple backend MCP servers into a unified interface.
package server

import (
	"context"
	"maps"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// sdkElicitationAdapter wraps mcpcompat MCPServer to implement vmcp.ElicitationRequester.
//
// It is the sole point where mcp-go elicitation types appear: it translates the
// domain ElicitationRequest/ElicitationResult to/from the SDK types, keeping the
// composer and the rest of vmcp free of SDK coupling.
//
// Per MCP 2025-06-18 spec: The SDK handles JSON-RPC ID correlation internally,
// so the adapter does not manage IDs.
//
// Thread-safety: Safe for concurrent calls. The mcpcompat MCPServer is thread-safe.
type sdkElicitationAdapter struct {
	// mcpServer is the mcpcompat SDK server instance that handles elicitation protocol.
	// Typed as the minimal mcpElicitationRequester seam so the translation logic
	// can be unit-tested against a fake SDK; *server.MCPServer satisfies it.
	mcpServer mcpElicitationRequester
}

// mcpElicitationRequester is the minimal slice of the mcpcompat SDK that the
// adapter depends on. *server.MCPServer satisfies it in production; tests
// substitute a fake to verify domain ⇄ mcp-go translation without a live session.
type mcpElicitationRequester interface {
	RequestElicitation(ctx context.Context, request mcp.ElicitationRequest) (*mcp.ElicitationResult, error)
}

// NewSDKElicitationAdapter creates a new elicitation adapter that wraps the mcpcompat SDK server.
//
// The returned adapter implements vmcp.ElicitationRequester by translating the domain
// request to/from mcp-go types and delegating to the SDK's RequestElicitation method.
// This adapter is the sole point where mcp-go elicitation types appear. Session
// management and JSON-RPC ID correlation are handled entirely by the SDK.
//
// Intended for embedders that wrap the vMCP composer in their own pipeline and need to
// drive MCP elicitation through the same SDK server that serves /mcp traffic. Pass the
// *server.MCPServer obtained from (*Server).MCPServer() so the returned requester
// correlates with the server handling incoming client sessions; a parallel MCPServer
// constructed by the caller will not work because ClientSession correlation is keyed
// to the server that received the initialize request.
func NewSDKElicitationAdapter(mcpServer *server.MCPServer) vmcp.ElicitationRequester {
	return &sdkElicitationAdapter{
		mcpServer: mcpServer,
	}
}

// RequestElicitation translates the domain request to mcp-go, delegates to the
// mcpcompat SDK's RequestElicitation method, and translates the response back.
//
// This is a synchronous blocking call that:
//  1. Maps the domain ElicitationRequest to an mcp.ElicitationRequest
//  2. Forwards the request to the mcpcompat SDK
//  3. Blocks until the client responds or timeout occurs
//  4. Maps the SDK's mcp.ElicitationResult back to the domain ElicitationResult
//
// The SDK handles all protocol details internally:
//   - JSON-RPC ID generation and correlation
//   - Session routing (ensures request reaches correct client)
//   - Error handling and timeout management
//
// Per MCP 2025-06-18 spec: Elicitation is a synchronous request/response protocol.
// The server sends a request and blocks until the client responds.
//
// Returns the domain ElicitationResult or error if the request fails, times out,
// or the user declines/cancels.
func (a *sdkElicitationAdapter) RequestElicitation(
	ctx context.Context,
	req vmcp.ElicitationRequest,
) (*vmcp.ElicitationResult, error) {
	// Translate the domain request to the SDK request type (form-mode only).
	mcpReq := mcp.ElicitationRequest{
		Params: mcp.ElicitationParams{
			Message:         req.Message,
			RequestedSchema: req.RequestedSchema,
		},
	}
	// Only attach _meta when the caller actually set it. NewMetaFromMap mutates
	// its argument (it deletes progressToken), so copy first to avoid mutating
	// the caller's map.
	if req.Meta != nil {
		mcpReq.Params.Meta = mcp.NewMetaFromMap(maps.Clone(req.Meta))
	}

	// Delegate to the mcpcompat SDK's RequestElicitation method.
	// The SDK will:
	//   1. Extract session ID from context (set by SDK middleware)
	//   2. Generate JSON-RPC ID for the request
	//   3. Send elicitation request to client via transport
	//   4. Block until response received or timeout
	//   5. Correlate response to request using JSON-RPC ID
	//   6. Return result to us
	//
	// We don't need to manage any of this - it's all handled by the SDK.
	resp, err := a.mcpServer.RequestElicitation(ctx, mcpReq)
	if err != nil {
		return nil, err
	}

	// Translate the SDK response back to the domain result. Content is passed
	// through as any so the caller performs its own map assertion unchanged.
	return &vmcp.ElicitationResult{
		Action:  string(resp.Action),
		Content: resp.Content,
	}, nil
}

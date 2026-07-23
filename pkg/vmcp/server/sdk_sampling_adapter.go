// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// sdkSamplingAdapter wraps mcpcompat MCPServer to implement vmcp.SamplingRequester.
//
// It is the sole point where mcp-go sampling types appear: it translates the
// domain SamplingRequest/SamplingResult to/from the SDK types, keeping the
// backend client and the rest of vmcp free of SDK coupling. It mirrors
// sdkElicitationAdapter.
//
// Thread-safety: Safe for concurrent calls. The mcpcompat MCPServer is thread-safe.
type sdkSamplingAdapter struct {
	// mcpServer is the mcpcompat SDK server instance that handles the sampling
	// protocol. Typed as the minimal mcpSamplingRequester seam so the translation
	// logic can be unit-tested against a fake SDK; *server.MCPServer satisfies it.
	mcpServer mcpSamplingRequester
}

// mcpSamplingRequester is the minimal slice of the mcpcompat SDK that the
// adapter depends on. *server.MCPServer satisfies it in production; tests
// substitute a fake to verify domain <-> mcp-go translation without a live session.
type mcpSamplingRequester interface {
	RequestSampling(ctx context.Context, request mcp.CreateMessageRequest) (*mcp.CreateMessageResult, error)
}

// NewSDKSamplingAdapter creates a new sampling adapter that wraps the mcpcompat
// SDK server. The returned adapter implements vmcp.SamplingRequester by
// translating the domain request to/from mcp-go types and delegating to the
// SDK's RequestSampling method. Session management and JSON-RPC ID correlation
// are handled entirely by the SDK. Pass the *server.MCPServer obtained from
// (*Server).MCPServer() so the returned requester correlates with the server
// handling incoming client sessions.
func NewSDKSamplingAdapter(mcpServer *server.MCPServer) vmcp.SamplingRequester {
	return &sdkSamplingAdapter{mcpServer: mcpServer}
}

// RequestSampling translates the domain request to mcp-go, delegates to the
// mcpcompat SDK's RequestSampling method, and translates the response back.
//
// This is a synchronous blocking call: it maps the domain SamplingRequest to an
// mcp.CreateMessageRequest, forwards it to the SDK (which routes it to the
// correct client session and blocks until the client responds), and maps the
// SDK's mcp.CreateMessageResult back to the domain SamplingResult.
//
// Returns an error if the request fails, times out, the context carries no
// downstream session, or the client did not advertise the sampling capability.
func (a *sdkSamplingAdapter) RequestSampling(
	ctx context.Context,
	req vmcp.SamplingRequest,
) (*vmcp.SamplingResult, error) {
	mcpReq := mcp.CreateMessageRequest{
		CreateMessageParams: mcp.CreateMessageParams{
			Messages:         toMCPSamplingMessages(req.Messages),
			ModelPreferences: toMCPModelPreferences(req.ModelPreferences),
			SystemPrompt:     req.SystemPrompt,
			IncludeContext:   req.IncludeContext,
			Temperature:      req.Temperature,
			MaxTokens:        req.MaxTokens,
			StopSequences:    req.StopSequences,
			Metadata:         req.Metadata,
		},
	}

	resp, err := a.mcpServer.RequestSampling(ctx, mcpReq)
	if err != nil {
		return nil, err
	}

	return &vmcp.SamplingResult{
		Role:       string(resp.Role),
		Content:    resp.Content,
		Model:      resp.Model,
		StopReason: resp.StopReason,
	}, nil
}

// toMCPSamplingMessages maps domain sampling messages to the SDK type.
func toMCPSamplingMessages(msgs []vmcp.SamplingMessage) []mcp.SamplingMessage {
	if msgs == nil {
		return nil
	}
	out := make([]mcp.SamplingMessage, len(msgs))
	for i, m := range msgs {
		out[i] = mcp.SamplingMessage{
			Role:    mcp.Role(m.Role),
			Content: m.Content,
		}
	}
	return out
}

// toMCPModelPreferences maps the domain model preferences to the SDK type,
// preserving a nil (unset) value.
func toMCPModelPreferences(p *vmcp.ModelPreferences) *mcp.ModelPreferences {
	if p == nil {
		return nil
	}
	hints := make([]mcp.ModelHint, len(p.Hints))
	for i, h := range p.Hints {
		hints[i] = mcp.ModelHint{Name: h.Name}
	}
	return &mcp.ModelPreferences{
		Hints:                hints,
		CostPriority:         p.CostPriority,
		SpeedPriority:        p.SpeedPriority,
		IntelligencePriority: p.IntelligencePriority,
	}
}

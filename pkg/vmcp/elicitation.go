// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcp

import "context"

//go:generate mockgen -destination=mocks/mock_elicitation_requester.go -package=mocks -source=elicitation.go ElicitationRequester

// ElicitationRequester sends an elicitation request to the client and blocks
// for the response.
//
// This is the domain seam for MCP elicitation: callers (e.g. the composer's
// elicitation handler) depend only on this interface and the domain
// ElicitationRequest/ElicitationResult value types, never on the underlying
// SDK. The transport adapter (sdkElicitationAdapter in pkg/vmcp/server) is the
// sole point that translates to/from mcp-go types, keeping SDK coupling
// confined to the adapter boundary.
//
// The underlying SDK handles JSON-RPC ID correlation internally, so
// implementations do not need to track request IDs.
type ElicitationRequester interface {
	// RequestElicitation sends an elicitation request and blocks until the
	// client responds or the context is cancelled.
	RequestElicitation(ctx context.Context, req ElicitationRequest) (*ElicitationResult, error)
}

// ElicitationRequest is the domain-typed elicitation request.
//
// It mirrors, one-to-one, the mcp-go fields the composer constructs today
// (form-mode only). URL-mode fields (ElicitationID/URL/Mode) are deliberately
// omitted because the composer never constructs them. The adapter in
// pkg/vmcp/server is responsible for translating this to the SDK request type.
type ElicitationRequest struct {
	// Message is the human-readable prompt shown to the user.
	Message string

	// RequestedSchema is the JSON Schema describing the expected response
	// content. It is passed through to the SDK unchanged.
	RequestedSchema any

	// Meta carries optional protocol metadata. The composer sets none today.
	Meta map[string]any
}

// ElicitationResult is the domain-typed elicitation response.
//
// Content stays typed as any so consumers can perform their own assertion to
// map[string]any (matching the SDK's wire shape) without the domain seam
// pinning a concrete type.
type ElicitationResult struct {
	// Action is what the user did: "accept", "decline", or "cancel".
	Action string

	// Content contains the user-provided data (for the accept action).
	Content any
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authorizers

import (
	"context"
)

// ToolCallImpliesRead decorates an Authorizer so that tool:call implies tool:read.
//
// A tool:read check is answered by the inner authorizer first. If that does not
// allow it, the decorator asks the inner authorizer the tool:call question. An
// inner authorizer that implements ToolReadNative and answers true (Cedar) decides
// tool:read itself, so the fallback does not apply to it. The implication is
// one-way: a tool:call check never falls back to tool:read, so a tool:read grant
// can never allow invocation.
type ToolCallImpliesRead struct {
	inner Authorizer
}

// WithToolCallImpliesRead wraps a so that tool:call implies tool:read.
// A nil authorizer is returned unchanged, because nil means "no authorization".
func WithToolCallImpliesRead(a Authorizer) Authorizer {
	if a == nil {
		return nil
	}
	return &ToolCallImpliesRead{inner: a}
}

// ToolReadNative is implemented by an authorizer that decides tool:read itself. Cedar
// does (it answers true), and so does the HTTP PDP when forward_tool_read is set.
// AnswersToolRead returns true when the authorizer answers tool:read directly; false
// means it should be asked only tool:call, which is then the visibility decision.
// An authorizer that does not implement it gets read first, then call.
type ToolReadNative interface {
	AnswersToolRead() bool
}

// AuthorizeWithJWTClaims implements the Authorizer interface. Only tool:read
// requests go through the fallback; every other request is passed through as is.
func (d *ToolCallImpliesRead) AuthorizeWithJWTClaims(
	ctx context.Context,
	feature MCPFeature,
	operation MCPOperation,
	resourceID string,
	arguments map[string]interface{},
) (bool, error) {
	if feature != MCPFeatureTool || operation != MCPOperationRead {
		return d.inner.AuthorizeWithJWTClaims(ctx, feature, operation, resourceID, arguments)
	}

	if native, ok := d.inner.(ToolReadNative); ok {
		if native.AnswersToolRead() {
			// The authorizer answers tool:read itself, once. No call fallback follows a deny.
			return d.inner.AuthorizeWithJWTClaims(ctx, feature, MCPOperationRead, resourceID, arguments)
		}
		// Call-only: one tool:call check decides visibility, so the PDP sees no tool:read.
		callAllowed, callErr := d.inner.AuthorizeWithJWTClaims(ctx, feature, MCPOperationCall, resourceID, arguments)
		if callErr != nil || !callAllowed {
			return false, callErr
		}
		return true, nil
	}

	readAllowed, readErr := d.inner.AuthorizeWithJWTClaims(ctx, feature, MCPOperationRead, resourceID, arguments)
	if readErr != nil {
		// A tool:read error is returned as is. It says nothing about tool:call, and a
		// second PDP call would double the wait on a slow PDP.
		return false, readErr
	}
	if readAllowed {
		return true, nil
	}

	// tool:read was denied, so the tool:call answer decides.
	callAllowed, callErr := d.inner.AuthorizeWithJWTClaims(ctx, feature, MCPOperationCall, resourceID, arguments)
	if callErr != nil || !callAllowed {
		return false, callErr
	}
	return true, nil
}

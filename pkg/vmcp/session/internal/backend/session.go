// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package backend defines the Session interface for a single persistent
// backend connection and provides the HTTP-based implementation used in
// production. It is internal to pkg/vmcp/session.
package backend

import (
	"context"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// Session abstracts a persistent, initialised MCP connection to a single
// backend server. It is created once per backend during session creation and
// reused for the lifetime of the parent MultiSession.
//
// Each Session is bound to exactly one backend at creation time — callers do
// not need to pass a routing target to individual method calls.
//
// Implementations must be safe for concurrent use.
type Session interface {
	// CallTool invokes a named tool on this backend.
	CallTool(
		ctx context.Context,
		toolName string,
		arguments map[string]any,
		meta map[string]any,
	) (*vmcp.ToolCallResult, error)

	// ReadResource reads a resource from this backend.
	ReadResource(ctx context.Context, uri string) (*vmcp.ResourceReadResult, error)

	// GetPrompt retrieves a prompt from this backend.
	GetPrompt(
		ctx context.Context,
		name string,
		arguments map[string]any,
	) (*vmcp.PromptGetResult, error)

	// SessionID returns the backend-assigned session ID (if any).
	// Returns "" if the backend did not assign a session ID.
	SessionID() string

	// Close closes the underlying transport connection.
	Close() error
}

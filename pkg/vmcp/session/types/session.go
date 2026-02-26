// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package types defines shared session interfaces for the vmcp/session package
// hierarchy. Placing the common types here allows both the internal backend
// package and the top-level session package to share a definition without
// introducing an import cycle.
package types

import (
	"context"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// Caller represents the ability to invoke MCP protocol operations against a
// backend. It is the common subset shared by both a single-backend
// [backend.Session] and the multi-backend [session.MultiSession].
//
// Implementations must be safe for concurrent use.
type Caller interface {
	// CallTool invokes toolName on the backend.
	//
	// arguments contains the tool input parameters.
	// meta contains protocol-level metadata (_meta) forwarded from the client.
	CallTool(
		ctx context.Context,
		toolName string,
		arguments map[string]any,
		meta map[string]any,
	) (*vmcp.ToolCallResult, error)

	// ReadResource retrieves the resource identified by uri from the backend.
	ReadResource(ctx context.Context, uri string) (*vmcp.ResourceReadResult, error)

	// GetPrompt retrieves the named prompt from the backend.
	//
	// arguments contains the prompt input parameters.
	GetPrompt(
		ctx context.Context,
		name string,
		arguments map[string]any,
	) (*vmcp.PromptGetResult, error)

	// Close releases all resources held by this caller. Implementations must
	// be idempotent: calling Close multiple times returns nil.
	Close() error
}

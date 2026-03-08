// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package types defines shared session interfaces for the vmcp/session package
// hierarchy. Placing the common types here allows both the internal backend
// package and the top-level session package to share a definition without
// introducing an import cycle.
package types

import (
	"context"
	"errors"

	"github.com/stacklok/toolhive/pkg/auth"
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
	// caller identifies the requesting user/service. For bound sessions, caller
	// must be non-nil and its identity must match the session creator. For
	// anonymous sessions, caller may be nil.
	//
	// Returns:
	//   - ErrNilCaller if caller is nil for a bound session
	//   - ErrUnauthorizedCaller if the caller identity does not match the session owner
	//
	// arguments contains the tool input parameters.
	// meta contains protocol-level metadata (_meta) forwarded from the client.
	CallTool(
		ctx context.Context,
		caller *auth.Identity,
		toolName string,
		arguments map[string]any,
		meta map[string]any,
	) (*vmcp.ToolCallResult, error)

	// ReadResource retrieves the resource identified by uri from the backend.
	//
	// caller identifies the requesting user/service. For bound sessions, caller
	// must be non-nil and its identity must match the session creator. For
	// anonymous sessions, caller may be nil.
	//
	// Returns:
	//   - ErrNilCaller if caller is nil for a bound session
	//   - ErrUnauthorizedCaller if the caller identity does not match the session owner
	ReadResource(ctx context.Context, caller *auth.Identity, uri string) (*vmcp.ResourceReadResult, error)

	// GetPrompt retrieves the named prompt from the backend.
	//
	// caller identifies the requesting user/service. For bound sessions, caller
	// must be non-nil and its identity must match the session creator. For
	// anonymous sessions, caller may be nil.
	//
	// Returns:
	//   - ErrNilCaller if caller is nil for a bound session
	//   - ErrUnauthorizedCaller if the caller identity does not match the session owner
	//
	// arguments contains the prompt input parameters.
	GetPrompt(
		ctx context.Context,
		caller *auth.Identity,
		name string,
		arguments map[string]any,
	) (*vmcp.PromptGetResult, error)

	// Close releases all resources held by this caller. Implementations must
	// be idempotent: calling Close multiple times returns nil.
	Close() error
}

// Token binding errors returned by Caller methods when caller identity
// validation fails.
var (
	// ErrUnauthorizedCaller is returned when the caller identity does not
	// match the session owner's identity (token hash mismatch).
	ErrUnauthorizedCaller = errors.New("caller identity does not match session owner")

	// ErrNilCaller is returned when a bound session receives a nil caller.
	// Bound sessions require explicit caller identity on every method call.
	ErrNilCaller = errors.New("caller identity is required for bound sessions")

	// ErrSessionOwnerUnknown is returned when the session has no bound identity
	// but is configured to require one. This indicates a configuration error.
	ErrSessionOwnerUnknown = errors.New("session has no bound identity")
)

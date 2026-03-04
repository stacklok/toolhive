// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/security"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/session/internal/backend"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

// Compile-time assertions: defaultMultiSession must implement both interfaces.
var _ MultiSession = (*defaultMultiSession)(nil)
var _ transportsession.Session = (*defaultMultiSession)(nil)

// Sentinel errors returned by defaultMultiSession methods.
var (
	// ErrSessionClosed is returned when an operation is attempted on a closed session.
	ErrSessionClosed = errors.New("session is closed")

	// ErrToolNotFound is returned when the requested tool is not in the routing table.
	ErrToolNotFound = errors.New("tool not found in session routing table")

	// ErrResourceNotFound is returned when the requested resource is not in the routing table.
	ErrResourceNotFound = errors.New("resource not found in session routing table")

	// ErrPromptNotFound is returned when the requested prompt is not in the routing table.
	ErrPromptNotFound = errors.New("prompt not found in session routing table")

	// ErrNoBackendClient is returned when the routing table references a backend
	// that has no entry in the connections map. This indicates an internal
	// invariant violation: under normal operation MakeSession always populates
	// both maps together, so this error should never be seen at runtime.
	ErrNoBackendClient = errors.New("no client available for backend")
)

// defaultMultiSession is the production MultiSession implementation.
//
// # Lifecycle
//
//  1. Created by defaultMultiSessionFactory.MakeSession (Phase 1: purely additive).
//  2. CallTool / ReadResource / GetPrompt admit via queue, perform I/O, then call done.
//  3. Close() drains the queue (blocking until all in-flight ops finish), then
//     closes all backend sessions.
//
// # Composite tools
//
// Composite tools (VirtualMCPCompositeToolDefinition) are out of scope for
// Phase 1. When they are introduced they will be resolved at a higher layer
// (e.g. the vMCP router or handler) and injected alongside the backend tool
// list, rather than being routed through the backend connections held here.
type defaultMultiSession struct {
	transportsession.Session // embedded interface — provides ID, Type, timestamps, etc.

	// All fields below are written once by MakeSession and are read-only thereafter.
	connections     map[string]backend.Session
	routingTable    *vmcp.RoutingTable
	tools           []vmcp.Tool
	resources       []vmcp.Resource
	prompts         []vmcp.Prompt
	backendSessions map[string]string

	queue AdmissionQueue

	// Token binding fields: enforce that subsequent requests come from the same
	// identity that created the session.
	// These fields are immutable after session creation (no mutex needed).
	boundTokenHash string // HMAC-SHA256 hash of creator's token (empty for anonymous)
	tokenSalt      []byte // Random salt used for HMAC (empty for anonymous)
	hmacSecret     []byte // Server-managed secret for HMAC-SHA256
	allowAnonymous bool   // Whether to allow nil caller
}

// Tools returns a snapshot copy of the tools available in this session.
func (s *defaultMultiSession) Tools() []vmcp.Tool {
	result := make([]vmcp.Tool, len(s.tools))
	copy(result, s.tools)
	return result
}

// Resources returns a snapshot copy of the resources available in this session.
func (s *defaultMultiSession) Resources() []vmcp.Resource {
	result := make([]vmcp.Resource, len(s.resources))
	copy(result, s.resources)
	return result
}

// Prompts returns a snapshot copy of the prompts available in this session.
func (s *defaultMultiSession) Prompts() []vmcp.Prompt {
	result := make([]vmcp.Prompt, len(s.prompts))
	copy(result, s.prompts)
	return result
}

// BackendSessions returns a snapshot copy of backend-assigned session IDs.
func (s *defaultMultiSession) BackendSessions() map[string]string {
	result := make(map[string]string, len(s.backendSessions))
	maps.Copy(result, s.backendSessions)
	return result
}

// validateCaller checks if the provided caller identity matches the session owner.
// Returns nil if validation succeeds, or an error if:
//   - The session requires a bound identity but caller is nil (ErrNilCaller)
//   - The caller's token hash doesn't match the session owner (ErrUnauthorizedCaller)
//   - An anonymous session receives a caller with a non-empty token (ErrUnauthorizedCaller)
//
// For anonymous sessions (allowAnonymous=true, boundTokenHash=""), validation succeeds
// only when the caller is nil or has an empty token (prevents session upgrade attacks).
func (s *defaultMultiSession) validateCaller(caller *auth.Identity) error {
	// No lock needed - token binding fields are immutable after session creation

	// Anonymous sessions: reject callers that present tokens
	if s.allowAnonymous && s.boundTokenHash == "" {
		// Prevent session upgrade attack: anonymous sessions cannot accept tokens
		if caller != nil && caller.Token != "" {
			slog.Warn("token validation failed: session upgrade attack prevented",
				"reason", "token_presented_to_anonymous_session",
			)
			return sessiontypes.ErrUnauthorizedCaller
		}
		return nil
	}

	// Bound sessions require a caller
	if caller == nil {
		slog.Warn("token validation failed: nil caller for bound session",
			"reason", "nil_caller",
		)
		return sessiontypes.ErrNilCaller
	}

	// Defensive check: bound sessions must have a non-empty token hash.
	// This prevents misconfigured sessions from accepting empty tokens.
	// Scenario: if boundTokenHash="" and caller.Token="", both would hash to "",
	// and ConstantTimeHashCompare would return true (both empty case).
	if s.boundTokenHash == "" {
		slog.Error("token validation failed: bound session has empty token hash",
			"reason", "misconfigured_session",
		)
		return sessiontypes.ErrSessionOwnerUnknown
	}

	// Compute caller's token hash using the same HMAC secret and salt
	callerHash := HashToken(caller.Token, s.hmacSecret, s.tokenSalt)

	// Constant-time comparison to prevent timing attacks
	if !security.ConstantTimeHashCompare(s.boundTokenHash, callerHash, SHA256HexLen) {
		slog.Warn("token validation failed: token hash mismatch",
			"reason", "token_hash_mismatch",
		)
		return sessiontypes.ErrUnauthorizedCaller
	}

	return nil
}

// lookupBackend resolves capName against table, admits the request via the
// admission queue, and returns the live backend session together with the done
// function that the caller MUST invoke when the I/O completes.
//
// If the queue is closed, ErrSessionClosed is returned and no done function is
// provided. On any other lookup error, done is also not provided.
func (s *defaultMultiSession) lookupBackend(
	capName string,
	table map[string]*vmcp.BackendTarget,
	notFoundErr error,
) (backend.Session, func(), error) {
	admitted, done := s.queue.TryAdmit()
	if !admitted {
		return nil, nil, ErrSessionClosed
	}

	target, ok := table[capName]
	if !ok {
		done()
		return nil, nil, fmt.Errorf("%w: %q", notFoundErr, capName)
	}
	conn, ok := s.connections[target.WorkloadID]
	if !ok {
		done()
		return nil, nil, fmt.Errorf("%w for backend %q", ErrNoBackendClient, target.WorkloadID)
	}
	return conn, done, nil
}

// CallTool invokes toolName on the appropriate backend.
// The caller parameter identifies the requesting user/service and is validated
// against the session owner's identity before proceeding.
func (s *defaultMultiSession) CallTool(
	ctx context.Context,
	caller *auth.Identity,
	toolName string,
	arguments map[string]any,
	meta map[string]any,
) (*vmcp.ToolCallResult, error) {
	// Validate caller identity
	if err := s.validateCaller(caller); err != nil {
		return nil, err
	}

	conn, done, err := s.lookupBackend(toolName, s.routingTable.Tools, ErrToolNotFound)
	if err != nil {
		return nil, err
	}
	defer done()
	return conn.CallTool(ctx, toolName, arguments, meta)
}

// ReadResource retrieves the resource identified by uri.
// The caller parameter identifies the requesting user/service and is validated
// against the session owner's identity before proceeding.
func (s *defaultMultiSession) ReadResource(
	ctx context.Context, caller *auth.Identity, uri string,
) (*vmcp.ResourceReadResult, error) {
	// Validate caller identity
	if err := s.validateCaller(caller); err != nil {
		return nil, err
	}

	conn, done, err := s.lookupBackend(uri, s.routingTable.Resources, ErrResourceNotFound)
	if err != nil {
		return nil, err
	}
	defer done()
	return conn.ReadResource(ctx, uri)
}

// GetPrompt retrieves the named prompt from the appropriate backend.
// The caller parameter identifies the requesting user/service and is validated
// against the session owner's identity before proceeding.
func (s *defaultMultiSession) GetPrompt(
	ctx context.Context,
	caller *auth.Identity,
	name string,
	arguments map[string]any,
) (*vmcp.PromptGetResult, error) {
	// Validate caller identity
	if err := s.validateCaller(caller); err != nil {
		return nil, err
	}

	conn, done, err := s.lookupBackend(name, s.routingTable.Prompts, ErrPromptNotFound)
	if err != nil {
		return nil, err
	}
	defer done()
	return conn.GetPrompt(ctx, name, arguments)
}

// Close releases all resources. CloseAndDrain blocks until in-flight
// operations complete; subsequent calls are no-ops (idempotent).
func (s *defaultMultiSession) Close() error {
	s.queue.CloseAndDrain()

	var errs []error
	for id, conn := range s.connections {
		if err := conn.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close backend %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

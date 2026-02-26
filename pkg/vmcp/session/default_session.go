// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"fmt"
	"maps"

	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/session/internal/backend"
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
func (s *defaultMultiSession) CallTool(
	ctx context.Context,
	toolName string,
	arguments map[string]any,
	meta map[string]any,
) (*vmcp.ToolCallResult, error) {
	conn, done, err := s.lookupBackend(toolName, s.routingTable.Tools, ErrToolNotFound)
	if err != nil {
		return nil, err
	}
	defer done()
	return conn.CallTool(ctx, toolName, arguments, meta)
}

// ReadResource retrieves the resource identified by uri.
func (s *defaultMultiSession) ReadResource(ctx context.Context, uri string) (*vmcp.ResourceReadResult, error) {
	conn, done, err := s.lookupBackend(uri, s.routingTable.Resources, ErrResourceNotFound)
	if err != nil {
		return nil, err
	}
	defer done()
	return conn.ReadResource(ctx, uri)
}

// GetPrompt retrieves the named prompt from the appropriate backend.
func (s *defaultMultiSession) GetPrompt(
	ctx context.Context,
	name string,
	arguments map[string]any,
) (*vmcp.PromptGetResult, error) {
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

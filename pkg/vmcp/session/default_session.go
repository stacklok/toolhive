// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"

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
// # Thread-safety model
//
// mu guards connections, closed, and the wg.Add call. RLock is held only
// long enough to retrieve state and atomically increment the in-flight counter
// (wg.Add); it is released before network I/O begins.
// routingTable, tools, resources, and prompts are written once during
// MakeSession and are read-only thereafter — they do not require lock protection.
//
// wg tracks in-flight operations. Close() sets closed=true under write lock,
// then waits for wg to reach zero before tearing down backend connections.
// Because wg.Add(1) always happens while the read lock is held (and before
// Close() acquires the write lock), there is no race between Close() and
// in-flight operations.
//
// # Lifecycle
//
//  1. Created by defaultMultiSessionFactory.MakeSession (Phase 1: purely additive).
//  2. CallTool / ReadResource / GetPrompt increment wg, perform I/O, decrement wg.
//  3. Close() sets closed=true, waits for wg, then closes all backend sessions.
//
// # Composite tools
//
// Composite tools (VirtualMCPCompositeToolDefinition) are out of scope for
// Phase 1. When they are introduced they will be resolved at a higher layer
// (e.g. the vMCP router or handler) and injected alongside the backend tool
// list, rather than being routed through the backend connections held here.
type defaultMultiSession struct {
	transportsession.Session // embedded interface — provides ID, Type, timestamps, etc.

	connections     map[string]backend.Session // backend workload ID → persistent backend session
	routingTable    *vmcp.RoutingTable
	tools           []vmcp.Tool
	resources       []vmcp.Resource
	prompts         []vmcp.Prompt
	backendSessions map[string]string // backend workload ID → backend-assigned session ID

	mu     sync.RWMutex
	wg     sync.WaitGroup
	closed bool
}

// Tools returns a snapshot copy of the tools available in this session.
func (s *defaultMultiSession) Tools() []vmcp.Tool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]vmcp.Tool, len(s.tools))
	copy(result, s.tools)
	return result
}

// Resources returns a snapshot copy of the resources available in this session.
func (s *defaultMultiSession) Resources() []vmcp.Resource {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]vmcp.Resource, len(s.resources))
	copy(result, s.resources)
	return result
}

// Prompts returns a snapshot copy of the prompts available in this session.
func (s *defaultMultiSession) Prompts() []vmcp.Prompt {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]vmcp.Prompt, len(s.prompts))
	copy(result, s.prompts)
	return result
}

// BackendSessions returns a snapshot copy of backend-assigned session IDs.
func (s *defaultMultiSession) BackendSessions() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]string, len(s.backendSessions))
	maps.Copy(result, s.backendSessions)
	return result
}

// lookupBackend resolves capName against table and returns the live backend
// session for the backend that owns it.
//
// On success, wg.Add(1) has been called before the lock is released. The
// caller MUST call wg.Done() (typically via defer) when the I/O completes.
// On error, wg.Add was never called.
func (s *defaultMultiSession) lookupBackend(
	capName string,
	table map[string]*vmcp.BackendTarget,
	notFoundErr error,
) (backend.Session, error) {
	// Hold RLock to atomically check closed and register the in-flight
	// operation. wg.Add(1) is called while the lock is held so that Close()
	// cannot slip in between "check closed" and "add to wait group".
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return nil, ErrSessionClosed
	}
	target, ok := table[capName]
	if !ok {
		s.mu.RUnlock()
		return nil, fmt.Errorf("%w: %q", notFoundErr, capName)
	}
	conn, ok := s.connections[target.WorkloadID]
	if !ok {
		s.mu.RUnlock()
		return nil, fmt.Errorf("%w for backend %q", ErrNoBackendClient, target.WorkloadID)
	}
	s.wg.Add(1) // register before releasing the lock to avoid a race with Close()
	s.mu.RUnlock()
	return conn, nil
}

// CallTool invokes toolName on the appropriate backend.
func (s *defaultMultiSession) CallTool(
	ctx context.Context,
	toolName string,
	arguments map[string]any,
	meta map[string]any,
) (*vmcp.ToolCallResult, error) {
	conn, err := s.lookupBackend(toolName, s.routingTable.Tools, ErrToolNotFound)
	if err != nil {
		return nil, err
	}
	defer s.wg.Done()
	return conn.CallTool(ctx, toolName, arguments, meta)
}

// ReadResource retrieves the resource identified by uri.
func (s *defaultMultiSession) ReadResource(ctx context.Context, uri string) (*vmcp.ResourceReadResult, error) {
	conn, err := s.lookupBackend(uri, s.routingTable.Resources, ErrResourceNotFound)
	if err != nil {
		return nil, err
	}
	defer s.wg.Done()
	return conn.ReadResource(ctx, uri)
}

// GetPrompt retrieves the named prompt from the appropriate backend.
func (s *defaultMultiSession) GetPrompt(
	ctx context.Context,
	name string,
	arguments map[string]any,
) (*vmcp.PromptGetResult, error) {
	conn, err := s.lookupBackend(name, s.routingTable.Prompts, ErrPromptNotFound)
	if err != nil {
		return nil, err
	}
	defer s.wg.Done()
	return conn.GetPrompt(ctx, name, arguments)
}

// Close releases all resources. It is idempotent: subsequent calls return nil
// without attempting to close backends again.
func (s *defaultMultiSession) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	// Wait for all in-flight operations to complete before tearing down clients.
	// No new operations can start after this point because closed=true was set
	// under the write lock, and callers check closed under the read lock.
	s.wg.Wait()

	// s.connections is read without holding mu: closed=true prevents any new
	// operation from starting, and wg.Wait() ensures all in-flight operations
	// have finished. connections is only written during MakeSession (phase 1),
	// so no concurrent writer exists at this point.
	var errs []error
	for id, conn := range s.connections {
		if err := conn.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close backend %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

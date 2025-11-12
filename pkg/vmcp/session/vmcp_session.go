// Package session provides vMCP-specific session types that extend transport sessions with domain logic.
package session

import (
	"fmt"
	"sync"

	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// Compile-time check that VMCPSession implements transportsession.Session interface.
var _ transportsession.Session = (*VMCPSession)(nil)

// VMCPSession extends StreamableSession with domain-specific routing data.
// This keeps routing table state in the application layer (pkg/vmcp/server)
// rather than polluting the transport layer (pkg/transport/session) with domain concerns.
//
// Design Rationale:
//   - Embeds StreamableSession to inherit Session interface and streamable HTTP behavior
//   - Adds routing table for per-session capability routing
//   - Maintains lifecycle synchronization with underlying transport session
//   - Provides type-safe access to routing table (vs. interface{} casting)
//
// Lifecycle:
//  1. Created by VMCPSessionFactory during sessionIDAdapter.Generate()
//  2. Routing table populated in AfterInitialize hook
//  3. Retrieved by middleware on subsequent requests via type assertion
//  4. Cleaned up automatically by session.Manager TTL worker
type VMCPSession struct {
	*transportsession.StreamableSession
	routingTable *vmcp.RoutingTable
	mu           sync.RWMutex
}

// NewVMCPSession creates a VMCPSession with initialized StreamableSession.
// The routing table is initially nil and will be populated during AfterInitialize hook.
//
// This function panics if NewStreamableSession returns an unexpected type. This is
// intentional fail-fast behavior for programming errors that should be caught during
// development/testing. The type assertion should always succeed since NewStreamableSession
// is under our control and always returns *StreamableSession. A panic here indicates a bug
// in the transport layer that needs to be fixed, not a runtime condition to handle gracefully.
func NewVMCPSession(id string) *VMCPSession {
	streamableSession := transportsession.NewStreamableSession(id)

	// Type assertion must succeed - NewStreamableSession contract guarantees *StreamableSession.
	// Panic on failure indicates programming error in transport layer.
	ss, ok := streamableSession.(*transportsession.StreamableSession)
	if !ok {
		panic(fmt.Sprintf(
			"programming error: NewStreamableSession returned unexpected type %T for session %s (expected *StreamableSession)",
			streamableSession,
			id,
		))
	}

	return &VMCPSession{
		StreamableSession: ss,
		routingTable:      nil,
	}
}

// GetRoutingTable retrieves the routing table for this session.
// Returns nil if capabilities have not been initialized yet.
func (s *VMCPSession) GetRoutingTable() *vmcp.RoutingTable {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.routingTable
}

// SetRoutingTable sets the routing table for this session.
// Called during AfterInitialize hook after capability discovery.
func (s *VMCPSession) SetRoutingTable(rt *vmcp.RoutingTable) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routingTable = rt
}

// Type identifies this as a streamable vMCP session.
func (*VMCPSession) Type() transportsession.SessionType {
	return transportsession.SessionTypeStreamable
}

// VMCPSessionFactory creates a factory function for the session manager.
func VMCPSessionFactory() transportsession.Factory {
	return func(id string) transportsession.Session {
		return NewVMCPSession(id)
	}
}

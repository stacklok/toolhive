// Package session provides an interface and implementation for managing sessions in the transport layer.
package session

// Session defines the interface for transport sessions.
type Session interface {
	ID() string
	Init()
	IsInitialized() bool
	SetIsInitialized(initialized bool)
	MCPSessionID() string
	SetMCPSessionID(mcpID string)
}

// ProxySession implements the Session interface for transport sessions.
type ProxySession struct {
	Id           string
	initialized  bool
	mcpSessionID string
}

// ID returns the session ID.
func (s *ProxySession) ID() string { return s.Id }

// SetIsInitialized sets the initialized state of the session.
func (s *ProxySession) SetIsInitialized(initialized bool) { s.initialized = initialized }

// IsInitialized returns whether the session is initialized.
func (s *ProxySession) IsInitialized() bool { return s.initialized }

// MCPSessionID returns the MCP session ID.
func (s *ProxySession) MCPSessionID() string { return s.mcpSessionID }

// SetMCPSessionID sets the MCP session ID.
func (s *ProxySession) SetMCPSessionID(mcpID string) { s.mcpSessionID = mcpID }

// Init initializes the session, setting it to uninitialized state.
func (s *ProxySession) Init() {
	s.initialized = false
	s.mcpSessionID = ""
}

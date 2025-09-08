package session

import (
	"time"

	"github.com/stacklok/toolhive/pkg/transport/ssecommon"
)

// SSESession represents an SSE (Server-Sent Events) session.
// It embeds ProxySession and adds SSE-specific functionality.
type SSESession struct {
	*ProxySession

	// SSE-specific fields
	MessageCh   chan string
	ClientInfo  *ssecommon.SSEClient
	IsConnected bool
}

// NewSSESession creates a new SSE session with the given ID.
func NewSSESession(id string) *SSESession {
	proxySession := NewTypedProxySession(id, SessionTypeSSE)
	return &SSESession{
		ProxySession: proxySession,
		MessageCh:    make(chan string, 100),
		IsConnected:  true,
	}
}

// NewSSESessionWithClient creates a new SSE session with the given ID and client info.
func NewSSESessionWithClient(id string, client *ssecommon.SSEClient) *SSESession {
	sess := NewSSESession(id)
	sess.ClientInfo = client
	if client != nil {
		sess.MessageCh = client.MessageCh
	}
	return sess
}

// Disconnect marks the session as disconnected and closes the message channel.
func (s *SSESession) Disconnect() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.IsConnected {
		s.IsConnected = false
		if s.MessageCh != nil {
			close(s.MessageCh)
		}
	}
}

// SendMessage sends a message to the SSE client if connected.
func (s *SSESession) SendMessage(msg string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.IsConnected {
		return ErrSessionDisconnected
	}

	select {
	case s.MessageCh <- msg:
		return nil
	default:
		return ErrMessageChannelFull
	}
}

// GetClientInfo returns the SSE client information.
func (s *SSESession) GetClientInfo() *ssecommon.SSEClient {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ClientInfo
}

// SetClientInfo sets the SSE client information.
func (s *SSESession) SetClientInfo(client *ssecommon.SSEClient) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ClientInfo = client
	if client != nil && client.MessageCh != nil {
		s.MessageCh = client.MessageCh
	}
}

// GetConnectionStatus returns whether the SSE session is connected.
func (s *SSESession) GetConnectionStatus() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.IsConnected
}

// Type returns the session type as SSE.
// This overrides the embedded ProxySession's Type method.
func (*SSESession) Type() SessionType {
	return SessionTypeSSE
}

// GetCreatedAt returns when the SSE session was created.
// This is useful for tracking connection duration.
func (s *SSESession) GetCreatedAt() time.Time {
	if s.ClientInfo != nil {
		return s.ClientInfo.CreatedAt
	}
	return s.CreatedAt()
}

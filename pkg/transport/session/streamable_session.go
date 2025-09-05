package session

import (
	"errors"

	"golang.org/x/exp/jsonrpc2"
)

// StreamableSession represents a Streamable HTTP session
type StreamableSession struct {
	*ProxySession
	MessageCh    chan jsonrpc2.Message
	ResponseCh   chan jsonrpc2.Message
	disconnected bool
}

// NewStreamableSession constructs a new streamable session with buffered channels
func NewStreamableSession(id string) Session {
	return &StreamableSession{
		ProxySession: &ProxySession{id: id},
		MessageCh:    make(chan jsonrpc2.Message, 100),
		ResponseCh:   make(chan jsonrpc2.Message, 100),
	}
}

// Type identifies this as a streamable session
func (*StreamableSession) Type() SessionType {
	return SessionTypeStreamable
}

// GetData exposes buffer stats for observability/debugging
func (s *StreamableSession) GetData() interface{} {
	return map[string]int{
		"message_buffer":  len(s.MessageCh),
		"response_buffer": len(s.ResponseCh),
	}
}

// SetData is a no-op for StreamableSession; channel stats are exposed via GetData.
func (*StreamableSession) SetData(interface{}) {}

// Disconnect closes channels and marks session as disconnected
func (s *StreamableSession) Disconnect() {
	if s.disconnected {
		return
	}
	close(s.MessageCh)
	close(s.ResponseCh)
	s.disconnected = true
}

// SendMessage pushes message to MessageCh
func (s *StreamableSession) SendMessage(msg jsonrpc2.Message) error {
	if s.disconnected {
		return errors.New("session disconnected")
	}
	select {
	case s.MessageCh <- msg:
		return nil
	default:
		return errors.New("message buffer full")
	}
}

// SendResponse pushes message to ResponseCh
func (s *StreamableSession) SendResponse(msg jsonrpc2.Message) error {
	if s.disconnected {
		return errors.New("session disconnected")
	}
	select {
	case s.ResponseCh <- msg:
		return nil
	default:
		return errors.New("response buffer full")
	}
}

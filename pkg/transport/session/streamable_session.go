// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"errors"
	"sync"

	"golang.org/x/exp/jsonrpc2"
)

const (
	// streamableChannelBuffer is the buffer size for per-session message channels.
	// Channels are allocated lazily on first use to avoid wasting memory when
	// the proxy routes messages through global channels instead.
	streamableChannelBuffer = 100
)

// StreamableSession represents a Streamable HTTP session
type StreamableSession struct {
	*ProxySession
	MessageCh    chan jsonrpc2.Message
	ResponseCh   chan jsonrpc2.Message
	disconnected bool
	chOnce       sync.Once // guards lazy channel initialization
}

// NewStreamableSession constructs a new streamable session.
// Channels are nil by default and allocated lazily on first SendMessage/SendResponse
// call to avoid ~3 KB of wasted memory per session when the proxy uses global channels.
func NewStreamableSession(id string) Session {
	return &StreamableSession{
		ProxySession: NewTypedProxySession(id, SessionTypeStreamable),
	}
}

// initChannels lazily allocates the buffered message channels.
func (s *StreamableSession) initChannels() {
	s.chOnce.Do(func() {
		s.MessageCh = make(chan jsonrpc2.Message, streamableChannelBuffer)
		s.ResponseCh = make(chan jsonrpc2.Message, streamableChannelBuffer)
	})
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
	// Only close channels if they were allocated
	if s.MessageCh != nil {
		close(s.MessageCh)
	}
	if s.ResponseCh != nil {
		close(s.ResponseCh)
	}
	s.disconnected = true
}

// SendMessage pushes message to MessageCh, lazily allocating the channel if needed.
func (s *StreamableSession) SendMessage(msg jsonrpc2.Message) error {
	if s.disconnected {
		return errors.New("session disconnected")
	}
	s.initChannels()
	select {
	case s.MessageCh <- msg:
		return nil
	default:
		return errors.New("message buffer full")
	}
}

// SendResponse pushes message to ResponseCh, lazily allocating the channel if needed.
func (s *StreamableSession) SendResponse(msg jsonrpc2.Message) error {
	if s.disconnected {
		return errors.New("session disconnected")
	}
	s.initChannels()
	select {
	case s.ResponseCh <- msg:
		return nil
	default:
		return errors.New("response buffer full")
	}
}

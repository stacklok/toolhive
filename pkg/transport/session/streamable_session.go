// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

// StreamableSession represents a Streamable HTTP session
type StreamableSession struct {
	*ProxySession
	disconnected bool
}

// NewStreamableSession constructs a new streamable session.
func NewStreamableSession(id string) Session {
	return &StreamableSession{
		ProxySession: NewTypedProxySession(id, SessionTypeStreamable),
	}
}

// Type identifies this as a streamable session
func (*StreamableSession) Type() SessionType {
	return SessionTypeStreamable
}

// GetData returns nil for StreamableSession.
func (*StreamableSession) GetData() interface{} {
	return nil
}

// SetData is a no-op for StreamableSession.
func (*StreamableSession) SetData(interface{}) {}

// Disconnect marks session as disconnected.
func (s *StreamableSession) Disconnect() {
	s.disconnected = true
}

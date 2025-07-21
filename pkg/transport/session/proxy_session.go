package session

import "time"

// ProxySession implements the Session interface for proxy sessions.
type ProxySession struct {
	id      string
	created time.Time
	updated time.Time
}

// NewProxySession creates a new ProxySession with the given ID.
var NewProxySession = func(id string) *ProxySession {
	now := time.Now()
	return &ProxySession{id: id, created: now, updated: now}
}

// ID returns the session ID.
func (s *ProxySession) ID() string { return s.id }

// CreatedAt returns the creation time of the session.
func (s *ProxySession) CreatedAt() time.Time { return s.created }

// UpdatedAt returns the last updated time of the session.
func (s *ProxySession) UpdatedAt() time.Time { return s.updated }

// Touch updates the session's last updated time to the current time.
func (s *ProxySession) Touch() { s.updated = time.Now() }

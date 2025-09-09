package session

import (
	"sync"
	"time"
)

// SessionType represents the type of session
//
//revive:disable-next-line:exported
type SessionType string

const (
	// SessionTypeMCP represents a standard MCP session
	SessionTypeMCP SessionType = "mcp"
	// SessionTypeSSE represents an SSE (Server-Sent Events) session
	SessionTypeSSE SessionType = "sse"
	// SessionTypeStreamable represents a streamable HTTP session
	SessionTypeStreamable SessionType = "streamable"
)

const (
	// DefaultSessionTTL is the default time-to-live for sessions (2 hours)
	DefaultSessionTTL = 2 * time.Hour
)

// ProxySession implements the Session interface for proxy sessions.
// It now includes support for session types, metadata, and custom data.
type ProxySession struct {
	id       string
	created  time.Time
	updated  time.Time
	sessType SessionType
	data     interface{}
	metadata map[string]string
	mu       sync.RWMutex // Protect concurrent access to metadata and data
}

// NewProxySession creates a new ProxySession with the given ID.
// It defaults to SessionTypeMCP for backward compatibility.
func NewProxySession(id string) *ProxySession {
	now := time.Now()
	return &ProxySession{
		id:       id,
		created:  now,
		updated:  now,
		sessType: SessionTypeMCP,
		metadata: make(map[string]string),
	}
}

// NewTypedProxySession creates a new ProxySession with the given ID and type.
func NewTypedProxySession(id string, sessType SessionType) *ProxySession {
	now := time.Now()
	return &ProxySession{
		id:       id,
		created:  now,
		updated:  now,
		sessType: sessType,
		metadata: make(map[string]string),
	}
}

// ID returns the session ID.
func (s *ProxySession) ID() string { return s.id }

// CreatedAt returns the creation time of the session.
func (s *ProxySession) CreatedAt() time.Time { return s.created }

// UpdatedAt returns the last updated time of the session.
func (s *ProxySession) UpdatedAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.updated
}

// Touch updates the session's last updated time to the current time.
func (s *ProxySession) Touch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updated = time.Now()
}

// Type returns the session type.
func (s *ProxySession) Type() SessionType { return s.sessType }

// GetData returns the session-specific data.
func (s *ProxySession) GetData() interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data
}

// SetData sets the session-specific data.
func (s *ProxySession) SetData(data interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = data
}

// GetMetadata returns all metadata as a map.
func (s *ProxySession) GetMetadata() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Return a copy to prevent external modification
	result := make(map[string]string, len(s.metadata))
	for k, v := range s.metadata {
		result[k] = v
	}
	return result
}

// SetMetadata sets a metadata key-value pair.
func (s *ProxySession) SetMetadata(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.metadata == nil {
		s.metadata = make(map[string]string)
	}
	s.metadata[key] = value
}

// GetMetadataValue gets a specific metadata value.
func (s *ProxySession) GetMetadataValue(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.metadata[key]
	return value, ok
}

// DeleteMetadata removes a metadata key.
func (s *ProxySession) DeleteMetadata(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.metadata, key)
}

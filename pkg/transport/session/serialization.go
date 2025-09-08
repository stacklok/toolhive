package session

import (
	"encoding/json"
	"fmt"
	"time"
)

// The following serialization functions are prepared for Phase 4 (Redis/Valkey implementation)
// They are currently unused but will be needed when implementing distributed storage backends.

// sessionData is the JSON representation of a session.
// This structure is used for serializing sessions to/from storage backends.
// nolint:unused // Will be used in Phase 4 for Redis/Valkey storage
type sessionData struct {
	ID        string            `json:"id"`
	Type      SessionType       `json:"type"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	Data      json.RawMessage   `json:"data,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// serializeSession converts a Session to its JSON representation.
// nolint:unused // Will be used in Phase 4 for Redis/Valkey storage
func serializeSession(s Session) ([]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("cannot serialize nil session")
	}

	data := sessionData{
		ID:        s.ID(),
		Type:      s.Type(),
		CreatedAt: s.CreatedAt(),
		UpdatedAt: s.UpdatedAt(),
		Metadata:  s.GetMetadata(),
	}

	// Handle session-specific data
	if sessionData := s.GetData(); sessionData != nil {
		jsonData, err := json.Marshal(sessionData)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal session data: %w", err)
		}
		data.Data = jsonData
	}

	return json.Marshal(data)
}

// deserializeSession reconstructs a Session from its JSON representation.
// It creates the appropriate session type based on the Type field.
// nolint:unused // Will be used in Phase 4 for Redis/Valkey storage
func deserializeSession(data []byte) (Session, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("cannot deserialize empty data")
	}

	var sd sessionData
	if err := json.Unmarshal(data, &sd); err != nil {
		return nil, fmt.Errorf("failed to unmarshal session data: %w", err)
	}

	// Create appropriate session type using existing constructors
	var session Session
	switch sd.Type {
	case SessionTypeSSE:
		// Use existing NewSSESession constructor
		sseSession := NewSSESession(sd.ID)
		// Update timestamps to match stored values
		sseSession.created = sd.CreatedAt
		sseSession.updated = sd.UpdatedAt
		// Restore metadata
		sseSession.metadata = sd.Metadata
		if sseSession.metadata == nil {
			sseSession.metadata = make(map[string]string)
		}
		// Note: SSE channels and client info will be recreated when reconnected
		session = sseSession

	case SessionTypeStreamable:
		// Use existing NewStreamableSession constructor
		streamSession := NewStreamableSession(sd.ID).(*StreamableSession)
		// Update timestamps to match stored values
		streamSession.created = sd.CreatedAt
		streamSession.updated = sd.UpdatedAt
		streamSession.sessType = SessionTypeStreamable
		// Restore metadata
		streamSession.metadata = sd.Metadata
		if streamSession.metadata == nil {
			streamSession.metadata = make(map[string]string)
		}
		session = streamSession

	case SessionTypeMCP:
		fallthrough
	default:
		// Use existing NewTypedProxySession constructor
		proxySession := NewTypedProxySession(sd.ID, sd.Type)
		// Update timestamps to match stored values
		proxySession.created = sd.CreatedAt
		proxySession.updated = sd.UpdatedAt
		// Restore metadata
		proxySession.metadata = sd.Metadata
		if proxySession.metadata == nil {
			proxySession.metadata = make(map[string]string)
		}
		session = proxySession
	}

	// Restore session-specific data if present
	if len(sd.Data) > 0 {
		// For now, we store the raw JSON. Session-specific implementations
		// can unmarshal this as needed.
		session.SetData(sd.Data)
	}

	return session, nil
}

// Package session provides a session manager with TTL cleanup.
package session

import (
	"fmt"
	"sync"
	"time"
)

// Session interface
type Session interface {
	ID() string
	CreatedAt() time.Time
	UpdatedAt() time.Time
	Touch()
}

// Manager holds sessions with TTL cleanup.
type Manager struct {
	sessions sync.Map
	ttl      time.Duration
	stopCh   chan struct{}
	factory  Factory
}

// Factory defines a function type for creating new sessions.
// It now returns the Session interface to support different session types.
type Factory func(id string) Session

// LegacyFactory is the old factory type for backward compatibility
type LegacyFactory func(id string) *ProxySession

// NewManager creates a session manager with TTL and starts cleanup worker.
// It accepts either the new Factory or the legacy factory for backward compatibility.
func NewManager(ttl time.Duration, factory interface{}) *Manager {
	var f Factory

	switch factoryFunc := factory.(type) {
	case Factory:
		f = factoryFunc
	case LegacyFactory:
		// Wrap legacy factory to return Session interface
		f = func(id string) Session {
			return factoryFunc(id)
		}
	case func(id string) *ProxySession:
		// Also support direct function for backward compatibility
		f = func(id string) Session {
			return factoryFunc(id)
		}
	default:
		// Default to creating basic ProxySession
		f = func(id string) Session {
			return NewProxySession(id)
		}
	}

	m := &Manager{
		sessions: sync.Map{},
		ttl:      ttl,
		stopCh:   make(chan struct{}),
		factory:  f,
	}
	go m.cleanupRoutine()
	return m
}

// NewTypedManager creates a session manager for a specific session type.
func NewTypedManager(ttl time.Duration, sessionType SessionType) *Manager {
	factory := func(id string) Session {
		switch sessionType {
		case SessionTypeSSE:
			return NewSSESession(id)
		case SessionTypeMCP:
			return NewProxySession(id)
		case SessionTypeStreamable:
			return NewTypedProxySession(id, sessionType)
		default:
			return NewTypedProxySession(id, sessionType)
		}
	}

	return NewManager(ttl, factory)
}

func (m *Manager) cleanupRoutine() {
	ticker := time.NewTicker(m.ttl / 2)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			cutoff := time.Now().Add(-m.ttl)
			m.sessions.Range(func(key, val any) bool {
				sess, ok := val.(Session)
				if !ok {
					// Skip invalid value
					return true
				}
				if sess.UpdatedAt().Before(cutoff) {
					m.sessions.Delete(key)
				}
				return true
			})
		case <-m.stopCh:
			return
		}
	}
}

// AddWithID creates (and adds) a new session with the provided ID.
// Returns error if ID is empty or already exists.
func (m *Manager) AddWithID(id string) error {
	if id == "" {
		return fmt.Errorf("session ID cannot be empty")
	}
	// Use LoadOrStore: returns existing if already present
	session := m.factory(id)
	_, loaded := m.sessions.LoadOrStore(id, session)
	if loaded {
		return fmt.Errorf("session ID %q already exists", id)
	}
	return nil
}

// AddSession adds an existing session to the manager.
// This is useful when you need to create a session with specific properties.
func (m *Manager) AddSession(session Session) error {
	if session == nil {
		return fmt.Errorf("session cannot be nil")
	}
	if session.ID() == "" {
		return fmt.Errorf("session ID cannot be empty")
	}

	_, loaded := m.sessions.LoadOrStore(session.ID(), session)
	if loaded {
		return fmt.Errorf("session ID %q already exists", session.ID())
	}
	return nil
}

// Get retrieves a session by ID. Returns (session, true) if found,
// and also updates its UpdatedAt timestamp.
func (m *Manager) Get(id string) (Session, bool) {
	v, ok := m.sessions.Load(id)
	if !ok {
		return nil, false
	}
	sess, ok := v.(Session)
	if !ok {
		return nil, false // Invalid session type
	}

	sess.Touch()
	return sess, true
}

// Delete removes a session by ID.
func (m *Manager) Delete(id string) {
	m.sessions.Delete(id)
}

// Stop stops the cleanup worker.
func (m *Manager) Stop() {
	close(m.stopCh)
}

// Range calls f sequentially for each key and value present in the map.
// If f returns false, range stops the iteration.
func (m *Manager) Range(f func(key, value interface{}) bool) {
	m.sessions.Range(f)
}

// Count returns the number of active sessions.
func (m *Manager) Count() int {
	count := 0
	m.sessions.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}

func (m *Manager) cleanupExpiredOnce() {
	cutoff := time.Now().Add(-m.ttl)
	m.sessions.Range(func(key, val any) bool {
		sess := val.(Session)
		if sess.UpdatedAt().Before(cutoff) {
			m.sessions.Delete(key)
		}
		return true
	})
}

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
}

// NewManager creates a session manager with TTL and starts cleanup worker.
func NewManager(ttl time.Duration) *Manager {
	m := &Manager{
		ttl:    ttl,
		stopCh: make(chan struct{}),
	}
	go m.cleanupRoutine()
	return m
}

func (m *Manager) cleanupRoutine() {
	ticker := time.NewTicker(m.ttl / 2)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			cutoff := time.Now().Add(-m.ttl)
			m.sessions.Range(func(key, val any) bool {
				sess := val.(Session)
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
	_, loaded := m.sessions.LoadOrStore(id, NewProxySession(id))
	if loaded {
		return fmt.Errorf("session ID %q already exists", id)
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
	sess := v.(Session)
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

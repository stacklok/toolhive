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
	sessions map[string]Session
	mu       sync.RWMutex
	ttl      time.Duration
	stopCh   chan struct{}
}

// NewManager creates a session manager with TTL and starts cleanup worker.
func NewManager(ttl time.Duration) *Manager {
	m := &Manager{
		sessions: make(map[string]Session),
		ttl:      ttl,
		stopCh:   make(chan struct{}),
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
			m.CleanupExpired()
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

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.sessions[id]; exists {
		return fmt.Errorf("session ID %q already exists", id)
	}

	s := NewProxySession(id)
	m.sessions[id] = s
	return nil
}

// Get retrieves a session by ID. Returns (session, true) if found,
// and also updates its UpdatedAt timestamp.
func (m *Manager) Get(id string) (Session, bool) {
	m.mu.RLock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, false
	}

	s.Touch()
	m.mu.RUnlock()

	return s, true
}

// Delete removes a session by ID.
func (m *Manager) Delete(id string) {
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
}

// CleanupExpired removes sessions that have not been updated within the TTL.
func (m *Manager) CleanupExpired() {
	cutoff := time.Now().Add(-m.ttl)
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, s := range m.sessions {
		if s.UpdatedAt().Before(cutoff) {
			delete(m.sessions, id)
		}
	}
}

// Stop stops the cleanup worker.
func (m *Manager) Stop() {
	close(m.stopCh)
}

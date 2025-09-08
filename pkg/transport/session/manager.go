// Package session provides a session manager with TTL cleanup.
package session

import (
	"context"
	"fmt"
	"time"
)

// Session interface defines the contract for all session types
type Session interface {
	ID() string
	Type() SessionType
	CreatedAt() time.Time
	UpdatedAt() time.Time
	Touch()

	// Data and metadata methods
	GetData() interface{}
	SetData(data interface{})
	GetMetadata() map[string]string
	SetMetadata(key, value string)
}

// Manager holds sessions with TTL cleanup.
type Manager struct {
	storage Storage
	ttl     time.Duration
	stopCh  chan struct{}
	factory Factory
}

// Factory defines a function type for creating new sessions.
// It now returns the Session interface to support different session types.
type Factory func(id string) Session

// LegacyFactory is the old factory type for backward compatibility
type LegacyFactory func(id string) *ProxySession

// NewManager creates a session manager with TTL and starts cleanup worker.
// It accepts either the new Factory or the legacy factory for backward compatibility.
// If storage is nil, it defaults to LocalStorage for backward compatibility.
func NewManager(ttl time.Duration, factory interface{}, storage ...Storage) *Manager {
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

	// Use provided storage or default to LocalStorage
	var s Storage
	if len(storage) > 0 && storage[0] != nil {
		s = storage[0]
	} else {
		s = NewLocalStorage()
	}

	// Set default TTL if not provided
	if ttl == 0 {
		ttl = 30 * time.Minute
	}

	m := &Manager{
		storage: s,
		ttl:     ttl,
		stopCh:  make(chan struct{}),
		factory: f,
	}

	// Only start cleanup routine for LocalStorage
	// Redis/Valkey handle TTL natively
	if _, isLocal := s.(*LocalStorage); isLocal {
		go m.cleanupRoutine()
	}

	return m
}

// NewTypedManager creates a session manager for a specific session type.
func NewTypedManager(ttl time.Duration, sessionType SessionType, storage ...Storage) *Manager {
	factory := Factory(func(id string) Session {
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
	})

	return NewManager(ttl, factory, storage...)
}

// NewManagerWithStorage creates a session manager with a custom storage backend.
func NewManagerWithStorage(ttl time.Duration, factory Factory, storage Storage) *Manager {
	if storage == nil {
		storage = NewLocalStorage()
	}
	if factory == nil {
		factory = func(id string) Session {
			return NewProxySession(id)
		}
	}
	if ttl == 0 {
		ttl = 30 * time.Minute
	}

	m := &Manager{
		storage: storage,
		ttl:     ttl,
		stopCh:  make(chan struct{}),
		factory: factory,
	}

	// Only start cleanup routine for LocalStorage
	// Redis/Valkey handle TTL natively
	if _, isLocal := storage.(*LocalStorage); isLocal {
		go m.cleanupRoutine()
	}

	return m
}

func (m *Manager) cleanupRoutine() {
	ticker := time.NewTicker(m.ttl / 2)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			cutoff := time.Now().Add(-m.ttl)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_ = m.storage.DeleteExpired(ctx, cutoff)
			cancel()
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
	// Check if session already exists
	ctx := context.Background()
	if _, err := m.storage.Load(ctx, id); err == nil {
		return fmt.Errorf("session ID %q already exists", id)
	}

	// Create and store new session
	session := m.factory(id)
	return m.storage.Store(ctx, session)
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

	// Check if session already exists
	ctx := context.Background()
	if _, err := m.storage.Load(ctx, session.ID()); err == nil {
		return fmt.Errorf("session ID %q already exists", session.ID())
	}

	return m.storage.Store(ctx, session)
}

// Get retrieves a session by ID. Returns (session, true) if found,
// and also updates its UpdatedAt timestamp.
func (m *Manager) Get(id string) (Session, bool) {
	ctx := context.Background()
	sess, err := m.storage.Load(ctx, id)
	if err != nil {
		return nil, false
	}
	return sess, true
}

// Delete removes a session by ID.
func (m *Manager) Delete(id string) {
	ctx := context.Background()
	_ = m.storage.Delete(ctx, id)
}

// Stop stops the cleanup worker and closes the storage backend.
func (m *Manager) Stop() {
	close(m.stopCh)
	if m.storage != nil {
		_ = m.storage.Close()
	}
}

// Range calls f sequentially for each key and value present in the map.
// If f returns false, range stops the iteration.
// Note: This only works with LocalStorage backend.
func (m *Manager) Range(f func(key, value interface{}) bool) {
	if localStorage, ok := m.storage.(*LocalStorage); ok {
		localStorage.Range(f)
	}
}

// Count returns the number of active sessions.
// Note: This only works with LocalStorage backend.
func (m *Manager) Count() int {
	if localStorage, ok := m.storage.(*LocalStorage); ok {
		return localStorage.Count()
	}
	return 0
}

func (m *Manager) cleanupExpiredOnce() {
	cutoff := time.Now().Add(-m.ttl)
	ctx := context.Background()
	_ = m.storage.DeleteExpired(ctx, cutoff)
}

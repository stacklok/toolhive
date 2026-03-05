// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"
)

// LocalStorage implements the Storage interface using an in-memory sync.Map.
// This is the default storage backend for single-instance deployments.
type LocalStorage struct {
	sessions sync.Map
}

// NewLocalStorage creates a new local in-memory storage backend.
func NewLocalStorage() *LocalStorage {
	return &LocalStorage{}
}

// Store saves a session to the local storage.
// For local storage, we store the session object directly without serialization.
func (s *LocalStorage) Store(_ context.Context, session Session) error {
	if session == nil {
		return fmt.Errorf("cannot store nil session")
	}
	if session.ID() == "" {
		return fmt.Errorf("cannot store session with empty ID")
	}

	s.sessions.Store(session.ID(), session)
	return nil
}

// Load retrieves a session from local storage.
func (s *LocalStorage) Load(_ context.Context, id string) (Session, error) {
	if id == "" {
		return nil, fmt.Errorf("cannot load session with empty ID")
	}

	val, ok := s.sessions.Load(id)
	if !ok {
		return nil, ErrSessionNotFound
	}

	session, ok := val.(Session)
	if !ok {
		return nil, fmt.Errorf("invalid session type in storage")
	}

	return session, nil
}

// Delete removes a session from local storage.
func (s *LocalStorage) Delete(_ context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("cannot delete session with empty ID")
	}

	s.sessions.Delete(id)
	return nil
}

// DeleteExpired removes all sessions that haven't been updated since the given time.
func (s *LocalStorage) DeleteExpired(ctx context.Context, before time.Time) error {
	var toDelete []struct {
		id      string
		session Session
	}

	// First pass: collect expired sessions
	s.sessions.Range(func(key, val any) bool {
		// Check for context cancellation
		select {
		case <-ctx.Done():
			return false
		default:
		}

		if session, ok := val.(Session); ok {
			if session.UpdatedAt().Before(before) {
				if id, ok := key.(string); ok {
					toDelete = append(toDelete, struct {
						id      string
						session Session
					}{id, session})
				}
			}
		}
		return true
	})

	// Second pass: close and delete expired sessions
	for _, item := range toDelete {
		// Check for context cancellation before processing each session
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Re-check expiration and use CompareAndDelete to handle race conditions:
		// - Session may have been touched via Manager.Get().Touch() and is no longer expired
		// - Session may have been replaced via Store/UpsertSession with a new object
		// Only proceed if the stored value is still the same session object and still expired
		if item.session.UpdatedAt().Before(before) {
			// CompareAndDelete ensures we only delete if the value hasn't been replaced
			if deleted := s.sessions.CompareAndDelete(item.id, item.session); deleted {
				// Successfully deleted - now close if implements io.Closer
				if closer, ok := item.session.(io.Closer); ok {
					if err := closer.Close(); err != nil {
						slog.Warn("failed to close session during cleanup",
							"session_id", item.id,
							"error", err)
					}
				}
			}
			// If CompareAndDelete returned false, the session was already replaced/deleted - skip it
		}
		// If re-check shows session is no longer expired (was touched), skip it
	}

	return nil
}

// Close clears all sessions from local storage.
func (s *LocalStorage) Close() error {
	// Collect keys first to avoid modifying map during iteration
	var toDelete []any
	s.sessions.Range(func(key, _ any) bool {
		toDelete = append(toDelete, key)
		return true
	})
	// Clear all sessions
	for _, key := range toDelete {
		s.sessions.Delete(key)
	}
	return nil
}

// Count returns the number of sessions in storage.
// This is a helper method not part of the Storage interface.
func (s *LocalStorage) Count() int {
	count := 0
	s.sessions.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}

// Range iterates over all sessions in storage.
// This is a helper method not part of the Storage interface.
func (s *LocalStorage) Range(f func(key, value interface{}) bool) {
	s.sessions.Range(f)
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// localEntry wraps a session with a storage-owned last-access timestamp.
// All eviction decisions in LocalStorage are based on this timestamp, not on
// any field carried by the Session itself. This ensures every session type gets
// correct TTL behaviour regardless of its own implementation details.
type localEntry struct {
	session        Session
	lastAccessNano atomic.Int64
}

func newLocalEntry(session Session) *localEntry {
	e := &localEntry{session: session}
	e.lastAccessNano.Store(time.Now().UnixNano())
	return e
}

func (e *localEntry) lastAccess() time.Time {
	return time.Unix(0, e.lastAccessNano.Load())
}

// LocalStorage implements the Storage interface using an in-memory sync.Map.
// This is the default storage backend for single-instance deployments.
type LocalStorage struct {
	sessions sync.Map // map[string]*localEntry
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

	s.sessions.Store(session.ID(), newLocalEntry(session))
	return nil
}

// Load retrieves a session from local storage and refreshes its last-access timestamp.
// The timestamp update happens inside LocalStorage so that eviction is correct for
// all session types, not just those that implement a Touch() method.
func (s *LocalStorage) Load(_ context.Context, id string) (Session, error) {
	if id == "" {
		return nil, fmt.Errorf("cannot load session with empty ID")
	}

	val, ok := s.sessions.Load(id)
	if !ok {
		return nil, ErrSessionNotFound
	}

	entry, ok := val.(*localEntry)
	if !ok {
		return nil, fmt.Errorf("invalid session type in storage")
	}

	// Refresh last-access time by swapping in a new entry pointer. This is
	// intentional: if we mutated lastAccessNano in-place, DeleteExpired could
	// still evict the session via CompareAndDelete (it holds the same pointer).
	// Swapping the pointer makes CompareAndDelete fail for any DeleteExpired
	// goroutine that snapshotted the old pointer, preventing eviction of active
	// sessions under concurrent load.
	newEntry := newLocalEntry(entry.session)
	s.sessions.CompareAndSwap(id, entry, newEntry)
	// If CAS fails, another goroutine already replaced this entry (e.g. a
	// concurrent Store or Load). Either way the map holds a fresh pointer, so
	// DeleteExpired will not evict it incorrectly.

	return entry.session, nil
}

// Peek reports whether a session exists in local storage without updating its
// last-access timestamp. This avoids extending the TTL of idle sessions during
// eviction-loop probes.
func (s *LocalStorage) Peek(_ context.Context, id string) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("cannot peek session with empty ID")
	}
	_, ok := s.sessions.Load(id)
	return ok, nil
}

// Delete removes a session from local storage.
func (s *LocalStorage) Delete(_ context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("cannot delete session with empty ID")
	}

	s.sessions.Delete(id)
	return nil
}

// DeleteExpired removes all sessions whose last-access time is before the given cutoff.
func (s *LocalStorage) DeleteExpired(ctx context.Context, before time.Time) error {
	var toDelete []struct {
		id    string
		entry *localEntry
	}

	// First pass: collect expired entries
	s.sessions.Range(func(key, val any) bool {
		// Check for context cancellation
		select {
		case <-ctx.Done():
			return false
		default:
		}

		if entry, ok := val.(*localEntry); ok {
			if entry.lastAccess().Before(before) {
				if id, ok := key.(string); ok {
					toDelete = append(toDelete, struct {
						id    string
						entry *localEntry
					}{id, entry})
				}
			}
		}
		return true
	})

	// Second pass: close and delete expired entries
	for _, item := range toDelete {
		// Check for context cancellation before processing each session
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Re-check expiration and use CompareAndDelete to handle race conditions:
		// - Entry may have been touched via LocalStorage.Load and is no longer expired
		// - Entry may have been replaced via Store/UpsertSession with a new object
		// Only proceed if the stored value is still the same entry and still expired.
		if item.entry.lastAccess().Before(before) {
			// CompareAndDelete ensures we only delete if the entry hasn't been replaced
			if deleted := s.sessions.CompareAndDelete(item.id, item.entry); deleted {
				// Successfully deleted - now close if implements io.Closer
				if closer, ok := item.entry.session.(io.Closer); ok {
					if err := closer.Close(); err != nil {
						slog.Warn("failed to close session during cleanup",
							"session_id", item.id,
							"error", err)
					}
				}
			}
			// If CompareAndDelete returned false, the entry was already replaced/deleted - skip it
		}
		// If re-check shows entry is no longer expired (was touched via Load), skip it
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

// Range iterates over all sessions in storage, passing the session (not the
// internal wrapper) to f. This is a helper method not part of the Storage interface.
func (s *LocalStorage) Range(f func(key, value interface{}) bool) {
	s.sessions.Range(func(key, val interface{}) bool {
		if entry, ok := val.(*localEntry); ok {
			return f(key, entry.session)
		}
		return f(key, val)
	})
}

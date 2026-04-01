// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"fmt"
	"maps"
	"sync"
	"sync/atomic"
	"time"
)

// localDataEntry wraps session metadata with a storage-owned last-access
// timestamp used for TTL-based eviction.
type localDataEntry struct {
	metadata       map[string]string
	lastAccessNano atomic.Int64
}

func newLocalDataEntry(metadata map[string]string) *localDataEntry {
	e := &localDataEntry{metadata: metadata}
	e.lastAccessNano.Store(time.Now().UnixNano())
	return e
}

func (e *localDataEntry) lastAccess() time.Time {
	return time.Unix(0, e.lastAccessNano.Load())
}

// LocalSessionDataStorage implements DataStorage using an in-memory
// sync.Map with TTL-based eviction.
//
// Sessions are evicted if they have not been accessed within the configured TTL.
// A background goroutine runs until Close is called.
type LocalSessionDataStorage struct {
	sessions sync.Map // map[string]*localDataEntry
	ttl      time.Duration
	stopCh   chan struct{}
	stopOnce sync.Once
}

// Store creates or updates session metadata.
func (s *LocalSessionDataStorage) Store(_ context.Context, id string, metadata map[string]string) error {
	if id == "" {
		return fmt.Errorf("cannot store session data with empty ID")
	}
	if metadata == nil {
		metadata = make(map[string]string)
	}
	// Store a defensive copy so callers cannot mutate stored data.
	copied := maps.Clone(metadata)
	s.sessions.Store(id, newLocalDataEntry(copied))
	return nil
}

// Load retrieves session metadata and refreshes its last-access timestamp.
// Returns ErrSessionNotFound if the session does not exist.
func (s *LocalSessionDataStorage) Load(_ context.Context, id string) (map[string]string, error) {
	if id == "" {
		return nil, fmt.Errorf("cannot load session data with empty ID")
	}

	val, ok := s.sessions.Load(id)
	if !ok {
		return nil, ErrSessionNotFound
	}
	entry, ok := val.(*localDataEntry)
	if !ok {
		return nil, fmt.Errorf("invalid entry type in local session data storage")
	}

	// Refresh last-access in place. deleteExpired re-checks the timestamp
	// immediately before calling CompareAndDelete, so this atomic store is
	// sufficient to prevent eviction of an actively accessed entry.
	entry.lastAccessNano.Store(time.Now().UnixNano())

	return maps.Clone(entry.metadata), nil
}

// Exists reports whether a session is present without refreshing its last-access
// timestamp. This is safe for eviction probes: it will not extend the lifetime
// of an idle session.
func (s *LocalSessionDataStorage) Exists(_ context.Context, id string) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("cannot check existence of session data with empty ID")
	}
	_, ok := s.sessions.Load(id)
	return ok, nil
}

// StoreIfAbsent atomically creates session metadata only if the session ID
// does not already exist. Uses sync.Map.LoadOrStore for atomicity.
func (s *LocalSessionDataStorage) StoreIfAbsent(_ context.Context, id string, metadata map[string]string) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("cannot store session data with empty ID")
	}
	if metadata == nil {
		metadata = make(map[string]string)
	}
	copied := maps.Clone(metadata)
	_, loaded := s.sessions.LoadOrStore(id, newLocalDataEntry(copied))
	return !loaded, nil
}

// Delete removes session metadata. Not an error if absent.
func (s *LocalSessionDataStorage) Delete(_ context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("cannot delete session data with empty ID")
	}
	s.sessions.Delete(id)
	return nil
}

// Close stops the background cleanup goroutine and clears all stored metadata.
func (s *LocalSessionDataStorage) Close() error {
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.sessions.Range(func(key, _ any) bool {
		s.sessions.Delete(key)
		return true
	})
	return nil
}

// minCleanupInterval is the floor applied to the cleanup ticker interval.
// time.NewTicker panics when given a duration ≤ 0, so any TTL smaller than
// 2ns would produce ttl/2 == 0 without this guard. 1ms is a practical floor
// that avoids the panic without restricting legitimate short TTLs in tests.
const minCleanupInterval = time.Millisecond

func (s *LocalSessionDataStorage) cleanupRoutine() {
	if s.ttl <= 0 {
		return
	}
	interval := s.ttl / 2
	if interval < minCleanupInterval {
		interval = minCleanupInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.deleteExpired()
		case <-s.stopCh:
			return
		}
	}
}

func (s *LocalSessionDataStorage) deleteExpired() {
	cutoff := time.Now().Add(-s.ttl)
	var toDelete []struct {
		id    string
		entry *localDataEntry
	}
	s.sessions.Range(func(key, val any) bool {
		entry, ok := val.(*localDataEntry)
		if ok && entry.lastAccess().Before(cutoff) {
			id, ok := key.(string)
			if ok {
				toDelete = append(toDelete, struct {
					id    string
					entry *localDataEntry
				}{id, entry})
			}
		}
		return true
	})
	for _, item := range toDelete {
		if item.entry.lastAccess().Before(cutoff) {
			s.sessions.CompareAndDelete(item.id, item.entry)
		}
	}
}

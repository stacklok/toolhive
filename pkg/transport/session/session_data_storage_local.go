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
// map with TTL-based eviction.
//
// Sessions are evicted if they have not been accessed within the configured TTL.
// A background goroutine runs until Close is called.
type LocalSessionDataStorage struct {
	sessions map[string]*localDataEntry // guarded by mu
	mu       sync.Mutex
	ttl      time.Duration
	stopCh   chan struct{}
	stopOnce sync.Once
}

// Upsert creates or updates session metadata.
func (s *LocalSessionDataStorage) Upsert(_ context.Context, id string, metadata map[string]string) error {
	if id == "" {
		return fmt.Errorf("cannot write session data with empty ID")
	}
	if metadata == nil {
		metadata = make(map[string]string)
	}
	s.mu.Lock()
	s.sessions[id] = newLocalDataEntry(maps.Clone(metadata))
	s.mu.Unlock()
	return nil
}

// Load retrieves session metadata and refreshes its last-access timestamp.
// Returns ErrSessionNotFound if the session does not exist.
func (s *LocalSessionDataStorage) Load(_ context.Context, id string) (map[string]string, error) {
	if id == "" {
		return nil, fmt.Errorf("cannot load session data with empty ID")
	}
	s.mu.Lock()
	entry, ok := s.sessions[id]
	if ok {
		entry.lastAccessNano.Store(time.Now().UnixNano())
	}
	s.mu.Unlock()
	if !ok {
		return nil, ErrSessionNotFound
	}
	return maps.Clone(entry.metadata), nil
}

// Create creates session metadata only if the session ID does not already exist.
// Returns (true, nil) if created, (false, nil) if the key already existed.
func (s *LocalSessionDataStorage) Create(_ context.Context, id string, metadata map[string]string) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("cannot write session data with empty ID")
	}
	if metadata == nil {
		metadata = make(map[string]string)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.sessions[id]; exists {
		return false, nil
	}
	s.sessions[id] = newLocalDataEntry(maps.Clone(metadata))
	return true, nil
}

// Update overwrites session metadata only if the session ID already exists.
// Returns (true, nil) if updated, (false, nil) if not found.
func (s *LocalSessionDataStorage) Update(_ context.Context, id string, metadata map[string]string) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("cannot write session data with empty ID")
	}
	if metadata == nil {
		metadata = make(map[string]string)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[id]; !ok {
		return false, nil
	}
	s.sessions[id] = newLocalDataEntry(maps.Clone(metadata))
	return true, nil
}

// Delete removes session metadata. Not an error if absent.
func (s *LocalSessionDataStorage) Delete(_ context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("cannot delete session data with empty ID")
	}
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
	return nil
}

// Close stops the background cleanup goroutine and clears all stored metadata.
func (s *LocalSessionDataStorage) Close() error {
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.mu.Lock()
	s.sessions = make(map[string]*localDataEntry)
	s.mu.Unlock()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, entry := range s.sessions {
		if entry.lastAccess().Before(cutoff) {
			delete(s.sessions, id)
		}
	}
}

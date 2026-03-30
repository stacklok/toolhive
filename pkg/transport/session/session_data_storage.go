// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"time"
)

// DataStorage stores session metadata as plain key-value pairs.
//
// Unlike the Session-based Storage interface, DataStorage never attempts
// to round-trip live session objects (MultiSession, StreamableSession, etc.).
// It stores only serialisable metadata, keeping data storage and live-object
// lifecycle as separate concerns.
//
// This separation avoids the type-assertion bug where a Redis round-trip
// deserialises a MultiSession as a plain *StreamableSession, losing all
// backend connections and routing state.
//
// # Contract
//
//   - Store creates or overwrites the metadata for id, refreshing the TTL.
//   - Load retrieves metadata and refreshes the TTL (sliding-window expiry).
//     Returns ErrSessionNotFound if the session does not exist.
//   - Delete removes the session. It is not an error if the session is absent.
//   - Close releases any resources held by the backend (connections, goroutines).
//
// # Implementations
//
// Two concrete implementations are provided:
//   - LocalSessionDataStorage (in-memory, single-process)
//   - RedisSessionDataStorage (Redis/Valkey, multi-process)
type DataStorage interface {
	// Store creates or updates session metadata with a sliding TTL.
	Store(ctx context.Context, id string, metadata map[string]string) error

	// Load retrieves session metadata and refreshes its TTL.
	// Returns ErrSessionNotFound if the session does not exist.
	Load(ctx context.Context, id string) (map[string]string, error)

	// Exists reports whether a session exists without refreshing its TTL.
	// Use this for read-only presence checks (e.g. eviction probes) where
	// touching the TTL would incorrectly extend an idle session's lifetime.
	// Returns (false, nil) when the session is not found; (false, err) on
	// storage errors.
	Exists(ctx context.Context, id string) (bool, error)

	// StoreIfAbsent atomically creates session metadata only if the session ID
	// does not already exist. Returns (true, nil) if the entry was created,
	// (false, nil) if it already existed, or (false, err) on storage errors.
	// Use this in preference to Exists+Store to avoid TOCTOU races in
	// multi-pod deployments.
	StoreIfAbsent(ctx context.Context, id string, metadata map[string]string) (bool, error)

	// Delete removes session metadata. Not an error if absent.
	Delete(ctx context.Context, id string) error

	// Close releases resources (connections, background goroutines).
	Close() error
}

// NewLocalSessionDataStorage creates a LocalSessionDataStorage with the given TTL.
// A background cleanup goroutine is started and runs until Close is called.
func NewLocalSessionDataStorage(ttl time.Duration) *LocalSessionDataStorage {
	s := &LocalSessionDataStorage{
		ttl:    ttl,
		stopCh: make(chan struct{}),
	}
	go s.cleanupRoutine()
	return s
}

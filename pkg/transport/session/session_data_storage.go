// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"fmt"
	"time"
)

// DataStorage stores session metadata as plain key-value pairs.
//
// Unlike the Session-based Storage interface, DataStorage never attempts
// to round-trip live session objects (MultiSession, StreamableSession, etc.).
// It stores only serializable metadata, keeping data storage and live-object
// lifecycle as separate concerns.
//
// This separation avoids the type-assertion bug where a Redis round-trip
// deserializes a MultiSession as a plain *StreamableSession, losing all
// backend connections and routing state.
//
// # Contract
//
//   - Create atomically creates metadata for id only if it does not already exist.
//     Use this in preference to Load+Upsert to avoid TOCTOU races.
//   - Upsert creates or overwrites the metadata for id, refreshing the TTL.
//   - Update overwrites metadata only if the key already exists (SET XX semantics).
//     Returns (true, nil) if updated, (false, nil) if the session was not found.
//     Use this instead of Load+Upsert to avoid TOCTOU resurrection races.
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
	// Upsert creates or updates session metadata with a sliding TTL.
	Upsert(ctx context.Context, id string, metadata map[string]string) error

	// Update overwrites session metadata only if the session ID already exists
	// (conditional write, equivalent to Redis SET XX). Returns (true, nil) if
	// the entry was updated, (false, nil) if it was not found, or (false, err)
	// on storage errors. Use this instead of Load+Upsert to prevent resurrections
	// after a concurrent Delete.
	Update(ctx context.Context, id string, metadata map[string]string) (bool, error)

	// Load retrieves session metadata and refreshes its TTL.
	// Returns ErrSessionNotFound if the session does not exist.
	Load(ctx context.Context, id string) (map[string]string, error)

	// Create atomically creates session metadata only if the session ID
	// does not already exist. Returns (true, nil) if the entry was created,
	// (false, nil) if it already existed, or (false, err) on storage errors.
	// Use this in preference to Load+Upsert to avoid TOCTOU races in
	// multi-pod deployments.
	Create(ctx context.Context, id string, metadata map[string]string) (bool, error)

	// Delete removes session metadata. Not an error if absent.
	Delete(ctx context.Context, id string) error

	// Close releases resources (connections, background goroutines).
	Close() error
}

// NewLocalSessionDataStorage creates a LocalSessionDataStorage with the given TTL.
// ttl must be positive; a zero or negative value returns an error.
// A background cleanup goroutine is started and runs until Close is called.
func NewLocalSessionDataStorage(ttl time.Duration) (*LocalSessionDataStorage, error) {
	if ttl <= 0 {
		return nil, fmt.Errorf("ttl must be a positive duration")
	}
	s := &LocalSessionDataStorage{
		sessions: make(map[string]*localDataEntry),
		ttl:      ttl,
		stopCh:   make(chan struct{}),
	}
	go s.cleanupRoutine()
	return s, nil
}

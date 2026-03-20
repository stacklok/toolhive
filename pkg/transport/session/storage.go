// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package session provides session management with pluggable storage backends.
package session

import (
	"context"
	"time"
)

// Storage defines the minimal interface for session storage backends.
// This interface is designed to be simple and efficient, supporting both
// local in-memory storage and distributed storage backends like Redis/Valkey.
type Storage interface {
	// Store creates or updates a session in the storage backend.
	// If the session already exists, it will be overwritten.
	Store(ctx context.Context, session Session) error

	// Load retrieves a session by ID from the storage backend.
	// Returns ErrSessionNotFound if the session doesn't exist.
	//
	// Implementations may refresh the backend's eviction TTL on every Load (e.g. Redis
	// GETEX) to prevent active sessions from expiring between reads, because Manager.Get
	// calls Touch on the returned object but does not call Store. This TTL refresh is a
	// backend-level eviction concern and is distinct from the session's application-level
	// UpdatedAt timestamp, which Load must NOT update.
	Load(ctx context.Context, id string) (Session, error)

	// Delete removes a session from the storage backend.
	// It is not an error if the session doesn't exist.
	Delete(ctx context.Context, id string) error

	// DeleteExpired removes all sessions that haven't been updated since the given time.
	// This is used by the cleanup routine to remove stale sessions.
	DeleteExpired(ctx context.Context, before time.Time) error

	// Touch refreshes the backend's eviction TTL for the given session ID without
	// loading or modifying the session data. All implementations return an error for
	// an empty id. Backends with no TTL (e.g. LocalStorage) otherwise return nil.
	// Backends with a native TTL (e.g. RedisStorage) return ErrSessionNotFound when
	// the key no longer exists, allowing callers to detect and evict stale local-cache
	// entries.
	//
	// RoutingStorage calls Touch on the remote backend for every local-cache hit.
	// If Touch returns ErrSessionNotFound the local entry is evicted and
	// ErrSessionNotFound is propagated to the caller, preventing stale sessions from
	// being served after the remote TTL has expired.
	Touch(ctx context.Context, id string) error

	// Close performs cleanup of the storage backend.
	// For local storage, this clears all sessions. For remote storage, it closes connections.
	Close() error
}

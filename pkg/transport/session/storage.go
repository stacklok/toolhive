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
	// Implementations should refresh their backend's eviction TTL on every Load to
	// prevent active sessions from expiring between reads. For Redis, this is done via
	// GETEX. For LocalStorage, Load updates a storage-owned last-access timestamp so
	// that DeleteExpired does not evict sessions that are actively being accessed.
	Load(ctx context.Context, id string) (Session, error)

	// Delete removes a session from the storage backend.
	// It is not an error if the session doesn't exist.
	Delete(ctx context.Context, id string) error

	// DeleteExpired removes all sessions that haven't been updated since the given time.
	// This is used by the cleanup routine to remove stale sessions.
	DeleteExpired(ctx context.Context, before time.Time) error

	// Close performs cleanup of the storage backend.
	// For local storage, this clears all sessions. For remote storage, it closes connections.
	Close() error
}

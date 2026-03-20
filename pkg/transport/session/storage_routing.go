// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
)

// Compile-time interface guard.
var _ Storage = (*routingStorage)(nil)

// routingStorage is an LRU-bounded, two-tier session storage.
// It checks a local in-memory LRU cache first; on a miss it falls back to the
// remote Storage (typically Redis). Evicted local entries are transparently
// recovered from remote on the next Load.
type routingStorage struct {
	local  *lru.Cache[string, Session]
	remote Storage
}

// NewRoutingStorage constructs a two-tier LRU+remote Storage with an LRU
// capacity of maxLocalEntries. maxLocalEntries must be > 0; a zero or negative
// value panics — the caller is expected to apply a sensible default (e.g. 1000)
// before calling this constructor.
func NewRoutingStorage(maxLocalEntries int, remote Storage) Storage {
	if maxLocalEntries <= 0 {
		panic(fmt.Sprintf("NewRoutingStorage: maxLocalEntries must be > 0, got %d", maxLocalEntries))
	}
	if remote == nil {
		panic("NewRoutingStorage: remote storage must not be nil")
	}
	cache, err := lru.New[string, Session](maxLocalEntries)
	if err != nil {
		// lru.New only errors when size <= 0, already guarded above.
		panic(fmt.Sprintf("NewRoutingStorage: failed to create LRU cache: %v", err))
	}
	return &routingStorage{
		local:  cache,
		remote: remote,
	}
}

// Store writes the session to remote first, then promotes it into the local
// LRU cache. If the remote write fails, the local cache is not updated and
// the error is returned.
func (r *routingStorage) Store(ctx context.Context, session Session) error {
	if session == nil {
		return fmt.Errorf("cannot store nil session")
	}
	if session.ID() == "" {
		return fmt.Errorf("cannot store session with empty ID")
	}
	if err := r.remote.Store(ctx, session); err != nil {
		return err
	}
	r.local.Add(session.ID(), session)
	return nil
}

// Load checks the local LRU cache first. On a hit it calls remote.Touch to
// refresh the backend TTL (e.g. Redis EXPIRE), then returns the cached session.
// If Touch returns ErrSessionNotFound the remote entry has expired; the local
// entry is evicted and ErrSessionNotFound is returned to the caller.
// Other Touch errors are logged but do not fail the Load.
// On a miss it fetches from remote, promotes the result into the local cache,
// and returns it. ErrSessionNotFound from remote propagates unchanged.
func (r *routingStorage) Load(ctx context.Context, id string) (Session, error) {
	if session, ok := r.local.Get(id); ok {
		if err := r.remote.Touch(ctx, id); err != nil {
			if errors.Is(err, ErrSessionNotFound) {
				r.local.Remove(id)
				return nil, ErrSessionNotFound
			}
			slog.Warn("RoutingStorage: failed to refresh remote TTL on cache hit",
				"session_id", id, "error", err)
		}
		return session, nil
	}
	session, err := r.remote.Load(ctx, id)
	if err != nil {
		return nil, err
	}
	r.local.Add(id, session)
	return session, nil
}

// Delete removes the entry from both the local LRU cache and remote.
// remote.Delete is called even if the local cache did not contain the entry.
// Errors from remote.Delete are returned to the caller.
func (r *routingStorage) Delete(ctx context.Context, id string) error {
	r.local.Remove(id)
	return r.remote.Delete(ctx, id)
}

// Touch refreshes the remote backend's eviction TTL for the given session ID.
// Delegates entirely to remote; the local LRU has no TTL of its own.
func (r *routingStorage) Touch(ctx context.Context, id string) error {
	return r.remote.Touch(ctx, id)
}

// DeleteExpired delegates entirely to remote. The local LRU cache will
// self-correct on subsequent Loads once entries expire from remote.
func (r *routingStorage) DeleteExpired(ctx context.Context, before time.Time) error {
	return r.remote.DeleteExpired(ctx, before)
}

// Close calls remote.Close(), then purges the local LRU cache.
// Any error from remote.Close() is returned.
func (r *routingStorage) Close() error {
	err := r.remote.Close()
	r.local.Purge()
	return err
}

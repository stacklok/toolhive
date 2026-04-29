// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"sync"
	"time"
)

// dcrStaleAgeThreshold is the age beyond which a cached DCR resolution is
// considered stale and logged as such by higher-level wiring. The store itself
// does not expire or evict entries — RFC 7591 client registrations are
// long-lived and are only purged by explicit RFC 7592 deregistration. This
// threshold is consumed by Step 2g observability logs introduced in the next
// PR in the DCR stack (sub-issue C, #5039); 5042 only defines the constant
// so the consumer can land without a cross-PR cycle.
//
//nolint:unused // consumed by lookupCachedResolution in #5039
const dcrStaleAgeThreshold = 90 * 24 * time.Hour

// DCRKey is the canonical lookup key for a DCR resolution. The tuple is
// designed so a future Redis-backed store can serialise it into a single key
// segment (Phase 3) without redefining the canonical form. ScopesHash rather
// than the raw scope slice is used so the key is comparable and order-
// insensitive.
type DCRKey struct {
	// Issuer is the authorization server's issuer identifier.
	Issuer string

	// RedirectURI is the redirect URI registered with the authorization server.
	RedirectURI string

	// ScopesHash is the SHA-256 hex digest of the sorted scope list.
	// See scopesHash in dcr.go for the canonical form.
	ScopesHash string
}

// DCRCredentialStore caches RFC 7591 Dynamic Client Registration resolutions
// keyed by the (Issuer, RedirectURI, ScopesHash) tuple. Implementations must
// be safe for concurrent use.
//
// The store is an in-memory cache of long-lived registrations — it is not a
// durable store, and entries are never expired or evicted by the store
// itself. Callers are responsible for invalidating entries when the
// underlying registration is revoked (e.g., via RFC 7592 deregistration).
type DCRCredentialStore interface {
	// Get returns the cached resolution for key, or (nil, false, nil) if the
	// key is not present. An error is returned only on backend failure.
	Get(ctx context.Context, key DCRKey) (*DCRResolution, bool, error)

	// Put stores the resolution for key, overwriting any existing entry.
	Put(ctx context.Context, key DCRKey, resolution *DCRResolution) error
}

// NewInMemoryDCRCredentialStore returns a thread-safe in-memory
// DCRCredentialStore. Entries are retained for the process lifetime; there is
// no TTL and no background cleanup goroutine.
//
// What this enables: serialises Get/Put against a single in-process map so
// concurrent callers within one authserver process see a consistent view of
// the cache without redundant RFC 7591 registrations.
//
// What this does NOT solve:
//   - Cross-replica sharing: each replica holds its own independent map, so a
//     registration performed on replica A is not visible to replica B. In a
//     multi-replica deployment every replica will register its own DCR client
//     against the upstream on first boot. Phase 3 introduces a Redis-backed
//     store that addresses this.
//   - Durability across restarts: process exit drops every entry; the next
//     boot re-registers. Operators relying on stable client_ids must use a
//     persistent backend.
//   - Cross-process write coordination: two processes (or replicas) calling
//     Put for the same DCRKey concurrently will both succeed against their
//     local maps; whichever registration the upstream accepts last wins on
//     that side, the loser becomes orphaned. The
//     resolveDCRCredentials-level singleflight in dcr.go only deduplicates
//     within one process.
func NewInMemoryDCRCredentialStore() DCRCredentialStore {
	return &inMemoryDCRCredentialStore{
		entries: make(map[DCRKey]*DCRResolution),
	}
}

// inMemoryDCRCredentialStore is the default DCRCredentialStore backed by a
// plain map guarded by sync.RWMutex. Modelled on
// pkg/authserver/storage/memory.go but stripped of TTL bookkeeping — DCR
// resolutions are long-lived.
type inMemoryDCRCredentialStore struct {
	mu      sync.RWMutex
	entries map[DCRKey]*DCRResolution
}

// Get implements DCRCredentialStore.
func (s *inMemoryDCRCredentialStore) Get(_ context.Context, key DCRKey) (*DCRResolution, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	res, ok := s.entries[key]
	if !ok {
		return nil, false, nil
	}
	// Return a defensive copy so mutations by the caller never reach the
	// cache entry. This mirrors the copy-before-mutate rule in
	// .claude/rules/go-style.md.
	cp := *res
	return &cp, true, nil
}

// Put implements DCRCredentialStore.
func (s *inMemoryDCRCredentialStore) Put(_ context.Context, key DCRKey, resolution *DCRResolution) error {
	if resolution == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// Defensive copy so the caller's subsequent mutations do not reach the
	// cache entry.
	cp := *resolution
	s.entries[key] = &cp
	return nil
}

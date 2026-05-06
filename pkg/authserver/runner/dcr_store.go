// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

// dcrStaleAgeThreshold is the age beyond which a cached DCR resolution is
// considered stale and logged as such by higher-level wiring. The store itself
// does not expire or evict entries — RFC 7591 client registrations are
// long-lived and are only purged by explicit RFC 7592 deregistration.
const dcrStaleAgeThreshold = 90 * 24 * time.Hour

// DCRKey is a re-export of storage.DCRKey, kept as a package-local alias so
// existing runner-side callers continue to compile against runner.DCRKey
// while the canonical definition lives in pkg/authserver/storage. The
// canonical form (and its ScopesHash constructor) MUST live in a single place
// so any future Redis backend hashes keys identically to the in-memory
// backend; see storage.DCRKey for the field documentation.
type DCRKey = storage.DCRKey

// dcrResolutionCache caches RFC 7591 Dynamic Client Registration resolutions
// keyed by the (Issuer, RedirectURI, ScopesHash) tuple. Implementations must
// be safe for concurrent use.
//
// This is a runner-internal cache of *DCRResolution values; it is distinct
// from the persistent storage.DCRCredentialStore (which holds *DCRCredentials
// and is the durable contract sub-issue 3 wires the resolver to use). Naming
// them differently keeps the two interfaces unambiguous to readers and grep
// tooling while both exist during the Phase 3 migration.
//
// The cache is in-memory and holds long-lived registrations — entries are
// never expired or evicted by the cache itself. Callers are responsible for
// invalidating entries when the underlying registration is revoked (e.g.,
// via RFC 7592 deregistration).
type dcrResolutionCache interface {
	// Get returns the cached resolution for key, or (nil, false, nil) if the
	// key is not present. An error is returned only on backend failure.
	Get(ctx context.Context, key DCRKey) (*DCRResolution, bool, error)

	// Put stores the resolution for key, overwriting any existing entry.
	// Implementations must reject a nil resolution with an error rather
	// than silently succeeding — a no-op would leave callers with no
	// debug trail for the subsequent Get miss.
	Put(ctx context.Context, key DCRKey, resolution *DCRResolution) error
}

// newInMemoryDCRResolutionCache returns a thread-safe in-memory
// dcrResolutionCache intended for tests and single-replica development
// deployments. Production deployments should use the Redis-backed store
// introduced in Phase 3, which addresses the cross-replica sharing,
// durability, and cross-process coordination gaps documented below.
//
// Entries are retained for the process lifetime; there is no TTL and no
// background cleanup goroutine. The usual concern about an unbounded
// cache leaking memory does not apply here because the key space is
// bounded by the operator-configured upstream count, and this
// implementation is not the production answer.
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
func newInMemoryDCRResolutionCache() dcrResolutionCache {
	return &inMemoryDCRResolutionCache{
		entries: make(map[DCRKey]*DCRResolution),
	}
}

// inMemoryDCRResolutionCache is the default dcrResolutionCache backed by a
// plain map guarded by sync.RWMutex. Modelled on
// pkg/authserver/storage/memory.go but stripped of TTL bookkeeping — DCR
// resolutions are long-lived.
type inMemoryDCRResolutionCache struct {
	mu      sync.RWMutex
	entries map[DCRKey]*DCRResolution
}

// Get implements dcrResolutionCache.
func (s *inMemoryDCRResolutionCache) Get(_ context.Context, key DCRKey) (*DCRResolution, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	res, ok := s.entries[key]
	if !ok {
		return nil, false, nil
	}
	// Return a defensive copy so mutations by the caller never reach the
	// cache entry — internal maps and pointers must not be reachable from
	// the caller's value.
	cp := *res
	return &cp, true, nil
}

// Put implements dcrResolutionCache.
//
// A nil resolution is rejected rather than silently no-oped: a caller
// passing nil would otherwise get a successful return, observe a miss on
// the next Get, and have no error trail to debug from. Failing loudly at
// the boundary makes such bugs visible at the first call.
func (s *inMemoryDCRResolutionCache) Put(_ context.Context, key DCRKey, resolution *DCRResolution) error {
	if resolution == nil {
		return fmt.Errorf("dcr: resolution must not be nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// Defensive copy so the caller's subsequent mutations do not reach the
	// cache entry.
	cp := *resolution
	s.entries[key] = &cp
	return nil
}

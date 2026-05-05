// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"errors"
	"fmt"
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
// This is the runner-facing interface used by the DCR resolver. It is a
// narrow re-projection of storage.DCRCredentialStore that exchanges
// *DCRResolution values (the resolver's working type) instead of
// *storage.DCRCredentials so the resolver internals stay agnostic to the
// persistence layer's exact field shape.
//
// Implementations in this package are thin adapters around a
// storage.DCRCredentialStore — the durable map / Redis hash lives over
// there, and this interface adds a per-call DCRResolution <-> DCRCredentials
// translation. There is exactly one persistence implementation per backend:
// storage.MemoryStorage and storage.RedisStorage. See newStorageBackedStore
// for the adapter.
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
// deployments. It is a thin adapter over storage.NewMemoryStorage so the
// runner-side cache and the authserver's main storage backend share a
// single in-memory implementation; production deployments configure a
// Redis-backed storage.DCRCredentialStore via storage.NewRedisStorage and
// reach this same adapter through newStorageBackedStore.
func newInMemoryDCRResolutionCache() dcrResolutionCache {
	return newStorageBackedStore(storage.NewMemoryStorage())
}

// newStorageBackedStore returns a dcrResolutionCache that delegates to a
// storage.DCRCredentialStore for durable persistence and translates
// DCRResolution values into DCRCredentials at the boundary. The returned
// store is safe for concurrent use because the underlying
// storage.DCRCredentialStore must be (per its interface contract).
func newStorageBackedStore(backend storage.DCRCredentialStore) dcrResolutionCache {
	return &storageBackedStore{backend: backend}
}

// storageBackedStore is the runner-side dcrResolutionCache wrapping a
// storage.DCRCredentialStore. Its methods are the only place that converts
// between the resolver's *DCRResolution and the persisted
// *storage.DCRCredentials shapes.
type storageBackedStore struct {
	backend storage.DCRCredentialStore
}

// Get implements dcrResolutionCache.
//
// A storage-level ErrNotFound is translated into the (nil, false, nil)
// miss-tuple advertised by the interface. Other errors propagate as-is.
func (s *storageBackedStore) Get(ctx context.Context, key DCRKey) (*DCRResolution, bool, error) {
	creds, err := s.backend.GetDCRCredentials(ctx, key)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return credentialsToResolution(creds), true, nil
}

// Put implements dcrResolutionCache.
//
// A nil resolution is rejected rather than silently no-oped: a caller
// passing nil would otherwise get a successful return, observe a miss on
// the next Get, and have no error trail to debug from. Failing loudly at
// the boundary makes such bugs visible at the first call.
func (s *storageBackedStore) Put(ctx context.Context, key DCRKey, resolution *DCRResolution) error {
	if resolution == nil {
		return fmt.Errorf("dcr: resolution must not be nil")
	}
	creds := resolutionToCredentials(key, resolution)
	return s.backend.StoreDCRCredentials(ctx, creds)
}

// resolutionToCredentials converts a resolver-side *DCRResolution into the
// persisted *storage.DCRCredentials shape. The DCRKey is supplied separately
// because storage.DCRCredentials carries the key as a struct field rather
// than implicitly via a map key, so the persistence layer can round-trip it
// across processes and backends.
//
// Fields that exist on DCRResolution but not on DCRCredentials are dropped:
//   - ClientIDIssuedAt: informational only per RFC 7591 §3.2.1; the resolver
//     does not consult it for cache invalidation, so it does not need to
//     survive a process restart.
//   - RedirectURI: already encoded into key.RedirectURI; storing it twice
//     would risk drift between the canonical key and the persisted value.
//
// CreatedAt and ClientSecretExpiresAt are preserved so cache observers
// (e.g. lookupCachedResolution's staleness Warn) and TTL-aware backends
// (Redis) keep their existing behaviour after a restart.
func resolutionToCredentials(key DCRKey, res *DCRResolution) *storage.DCRCredentials {
	if res == nil {
		return nil
	}
	return &storage.DCRCredentials{
		Key:                     key,
		ClientID:                res.ClientID,
		ClientSecret:            res.ClientSecret,
		TokenEndpointAuthMethod: res.TokenEndpointAuthMethod,
		RegistrationAccessToken: res.RegistrationAccessToken,
		RegistrationClientURI:   res.RegistrationClientURI,
		AuthorizationEndpoint:   res.AuthorizationEndpoint,
		TokenEndpoint:           res.TokenEndpoint,
		CreatedAt:               res.CreatedAt,
		ClientSecretExpiresAt:   res.ClientSecretExpiresAt,
	}
}

// credentialsToResolution is the inverse of resolutionToCredentials. The
// RedirectURI is recovered from the persisted Key so consumers that read it
// off the resolution (e.g. consumeResolution, which writes it back onto a
// run-config copy when the caller left it empty) see the canonical value.
//
// ClientIDIssuedAt is left zero because it is not persisted. Callers that
// care about it (none today) must read it directly from the live RFC 7591
// response, not from a cached resolution.
func credentialsToResolution(creds *storage.DCRCredentials) *DCRResolution {
	if creds == nil {
		return nil
	}
	return &DCRResolution{
		ClientID:                creds.ClientID,
		ClientSecret:            creds.ClientSecret,
		AuthorizationEndpoint:   creds.AuthorizationEndpoint,
		TokenEndpoint:           creds.TokenEndpoint,
		RegistrationAccessToken: creds.RegistrationAccessToken,
		RegistrationClientURI:   creds.RegistrationClientURI,
		TokenEndpointAuthMethod: creds.TokenEndpointAuthMethod,
		RedirectURI:             creds.Key.RedirectURI,
		ClientSecretExpiresAt:   creds.ClientSecretExpiresAt,
		CreatedAt:               creds.CreatedAt,
	}
}

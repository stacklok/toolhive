// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package dcr

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

// Key is a re-export of storage.DCRKey, kept as a package-local alias so
// callers in this package can reference the canonical cache key without an
// explicit storage. qualifier on every call site, while the canonical
// definition lives in pkg/authserver/storage. The canonical form (and its
// ScopesHash constructor) MUST live in a single place so any future Redis
// backend hashes keys identically to the in-memory backend; see
// storage.DCRKey for the field documentation.
type Key = storage.DCRKey

// CredentialStore caches RFC 7591 Dynamic Client Registration resolutions
// keyed by the (Issuer, RedirectURI, ScopesHash) tuple. Implementations must
// be safe for concurrent use.
//
// This is the runner-facing interface used by the DCR resolver. It is a
// narrow re-projection of storage.DCRCredentialStore that exchanges
// *Resolution values (the resolver's working type) instead of
// *storage.DCRCredentials so the resolver internals stay agnostic to the
// persistence layer's exact field shape.
//
// Implementations in this package are thin adapters around a
// storage.DCRCredentialStore — the durable map / Redis hash lives over
// there, and this interface adds a per-call Resolution <-> DCRCredentials
// translation. There is exactly one persistence implementation per backend:
// storage.MemoryStorage and storage.RedisStorage. See NewStorageBackedStore
// for the adapter.
type CredentialStore interface {
	// Get returns the cached resolution for key, or (nil, false, nil) if the
	// key is not present. An error is returned only on backend failure.
	Get(ctx context.Context, key Key) (*Resolution, bool, error)

	// Put stores the resolution for key, overwriting any existing entry.
	// Implementations must reject a nil resolution with an error rather
	// than silently succeeding — a no-op would leave callers with no
	// debug trail for the subsequent Get miss.
	Put(ctx context.Context, key Key, resolution *Resolution) error
}

// NewInMemoryStore returns a thread-safe in-memory CredentialStore intended
// for tests and single-replica development deployments. It is a thin adapter
// over storage.NewMemoryStorage so the resolver-side cache and the
// authserver's main storage backend share a single in-memory implementation.
//
// Production deployments should use a Redis-backed
// storage.DCRCredentialStore (instantiated via storage.NewRedisStorage and
// passed through this package's storage-backed adapter), which addresses
// cross-replica sharing, durability, and cross-process coordination.
func NewInMemoryStore() CredentialStore {
	return NewStorageBackedStore(storage.NewMemoryStorage())
}

// NewStorageBackedStore returns a CredentialStore that delegates to a
// storage.DCRCredentialStore for durable persistence and translates
// Resolution values into DCRCredentials at the boundary. The returned
// store is safe for concurrent use because the underlying
// storage.DCRCredentialStore must be (per its interface contract).
func NewStorageBackedStore(backend storage.DCRCredentialStore) CredentialStore {
	return &storageBackedStore{backend: backend}
}

// storageBackedStore is the dcr-package CredentialStore wrapping a
// storage.DCRCredentialStore. Its methods are the only place that converts
// between the resolver's *Resolution and the persisted
// *storage.DCRCredentials shapes.
type storageBackedStore struct {
	backend storage.DCRCredentialStore
}

// Get implements CredentialStore.
//
// A storage-level ErrNotFound is translated into the (nil, false, nil)
// miss-tuple advertised by the interface. Other errors propagate as-is.
func (s *storageBackedStore) Get(ctx context.Context, key Key) (*Resolution, bool, error) {
	creds, err := s.backend.GetDCRCredentials(ctx, key)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return credentialsToResolution(creds), true, nil
}

// Put implements CredentialStore.
//
// A nil resolution is rejected rather than silently no-oped: a caller
// passing nil would otherwise get a successful return, observe a miss on
// the next Get, and have no error trail to debug from. Failing loudly at
// the boundary makes such bugs visible at the first call.
func (s *storageBackedStore) Put(ctx context.Context, key Key, resolution *Resolution) error {
	if resolution == nil {
		return fmt.Errorf("dcr: resolution must not be nil")
	}
	creds := resolutionToCredentials(key, resolution)
	return s.backend.StoreDCRCredentials(ctx, creds)
}

// resolutionToCredentials converts a resolver-side *Resolution into the
// persisted *storage.DCRCredentials shape. The Key is supplied separately
// because storage.DCRCredentials carries the key as a struct field rather
// than implicitly via a map key, so the persistence layer can round-trip it
// across processes and backends.
//
// Fields that exist on Resolution but not on DCRCredentials are dropped:
//   - ClientIDIssuedAt: informational only per RFC 7591 §3.2.1; the resolver
//     does not consult it for cache invalidation, so it does not need to
//     survive a process restart.
//   - RedirectURI: already encoded into key.RedirectURI; storing it twice
//     would risk drift between the canonical key and the persisted value.
//
// CreatedAt and ClientSecretExpiresAt are preserved so cache observers
// (e.g. lookupCachedResolution's staleness Warn) and TTL-aware backends
// (Redis) keep their existing behaviour after a restart.
func resolutionToCredentials(key Key, res *Resolution) *storage.DCRCredentials {
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
// off the resolution (e.g. ConsumeResolution, which writes it back onto a
// run-config copy when the caller left it empty) see the canonical value.
//
// ClientIDIssuedAt is left zero because it is not persisted. Callers that
// care about it (none today) must read it directly from the live RFC 7591
// response, not from a cached resolution.
func credentialsToResolution(creds *storage.DCRCredentials) *Resolution {
	if creds == nil {
		return nil
	}
	return &Resolution{
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

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package dcr

import (
	"context"
	"errors"
	"fmt"
	"io"
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

// NewStorageBackedStore returns a CredentialStore that delegates to a
// storage.DCRCredentialStore for durable persistence and translates
// Resolution values into DCRCredentials at the boundary. The returned
// store is safe for concurrent use because the underlying
// storage.DCRCredentialStore must be (per its interface contract).
//
// Panics if backend is nil — a nil backend is unambiguously a programming
// error and silent acceptance would only delay the eventual nil-pointer
// dereference to the first Get/Put call, far from the constructor site.
func NewStorageBackedStore(backend storage.DCRCredentialStore) CredentialStore {
	if backend == nil {
		panic("dcr: NewStorageBackedStore: backend must not be nil")
	}
	return &storageBackedStore{backend: backend}
}

// CloseableCredentialStore is a CredentialStore that also releases an
// underlying resource (typically a background goroutine) when closed.
// NewInMemoryStore returns this interface so callers can call Close()
// directly without a runtime type assertion — Close() being part of the
// returned type at compile time prevents the silent-no-op failure mode
// that .claude/rules/go-style.md warns about ("Never define a local
// anonymous interface inside an option and type-assert against it to
// check capability — a silent no-op results if the target doesn't
// implement it.").
//
// NewStorageBackedStore returns the base CredentialStore because its
// backends (storage.MemoryStorage shared across the authserver,
// storage.RedisStorage) are owned by the authserver runner and closed
// through that lifecycle, not by the dcr package.
type CloseableCredentialStore interface {
	CredentialStore
	io.Closer
}

// NewInMemoryStore returns a CloseableCredentialStore whose entries live
// only for the lifetime of the returned value. This is the constructor
// consumers reach for when they have no shared durable backend — most
// notably the CLI OAuth flow, which manages cross-invocation credential
// persistence outside the resolver (in pkg/auth/remote/handler.go's
// CachedClientID / CachedClientSecretRef fields) and only needs the
// resolver's intra-call singleflight + S256 PKCE / expiry-refetch
// behaviour for one PerformOAuthFlow call.
//
// The implementation delegates to storage.NewMemoryStorage to share the
// same Get/Put/scope-hash semantics as the durable backends, including
// the background cleanup goroutine. Callers that need to release that
// goroutine before process exit MUST call Close on the returned value
// when finished — the return type is CloseableCredentialStore precisely
// so the call site can `defer store.Close()` without a runtime
// type-assertion. (CLI flows that complete in a single invocation can
// also rely on process exit.)
func NewInMemoryStore() CloseableCredentialStore {
	mem := storage.NewMemoryStorage()
	// Single backend handle is held by both the embedded storageBackedStore
	// (which serves Get/Put) and the concrete *storage.MemoryStorage field
	// (which serves Close). Sharing the handle guarantees Close releases
	// the same goroutine that Get/Put exercise — a regression introducing
	// two distinct backends would surface as a Close that leaks while Get
	// still serves entries.
	return &inMemoryStore{
		storageBackedStore: storageBackedStore{backend: mem},
		mem:                mem,
	}
}

// inMemoryStore is the CloseableCredentialStore returned by
// NewInMemoryStore. It embeds storageBackedStore for Get/Put behaviour
// and adds Close so the caller can release the underlying MemoryStorage
// cleanup goroutine.
//
// The mem field is a *storage.MemoryStorage — the concrete type whose
// Close method we want to call. Holding it directly (instead of
// type-asserting storageBackedStore.backend) satisfies the
// "Option pattern must be compile-time safe" rule in
// .claude/rules/go-style.md: the inMemoryStore type guarantees at
// compile time that the backend supports Close, so a future refactor
// that injects a non-closeable backend would fail to build rather than
// silently leak the cleanup goroutine.
type inMemoryStore struct {
	storageBackedStore
	mem *storage.MemoryStorage
}

// Close releases the embedded MemoryStorage. Safe to call multiple times;
// see storage.MemoryStorage.Close for idempotency guarantees.
func (s *inMemoryStore) Close() error {
	return s.mem.Close()
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
// off the resolution (e.g. pkg/authserver/runner.consumeResolution, which
// writes it back onto a run-config copy when the caller left it empty)
// see the canonical value.
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

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package dcr

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/stacklok/toolhive/pkg/oauthproto/dcrkey"
	"github.com/stacklok/toolhive/pkg/oauthproto/dcrstore"
)

// dcrStaleAgeThreshold is the age beyond which observers warn about a cached
// registration. Age alone does not expire it; rotation addresses a new key.
const dcrStaleAgeThreshold = 90 * 24 * time.Hour

// Key aliases the canonical OAuth-side DCR key.
type Key = dcrkey.Key

// CredentialStore caches registrations by the five-field Key and is safe for
// concurrent use. Persistence adapters belong to their owning subsystem.
type CredentialStore interface {
	// Get returns a copy, or (nil, false, nil) on a miss. Physical expired entries
	// may still be returned; the resolver decides whether they are usable.
	Get(ctx context.Context, key Key) (*Resolution, bool, error)
	// PutIfAbsent claims an absent or expired key and returns a copy of the
	// authoritative winner, never nil on success. Callers must use that result,
	// not their candidate. Nil and incomplete candidates must be rejected.
	PutIfAbsent(ctx context.Context, key Key, resolution *Resolution) (*Resolution, error)
}

// Replacer is an optional CredentialStore capability. The resolver verifies a
// cached registration against the upstream only when its store implements
// Replacer; otherwise it returns the cached registration unchecked, without
// contacting the upstream (see verifyCachedRegistration).
//
// It is a separate interface, not a CredentialStore method, so stores
// implemented outside this package keep compiling and so a wrapper that embeds
// CredentialStore does not inherit a replacement that skips whatever the
// wrapper does around writes (key namespacing, a lock that lets only one
// replica register). A wrapper that supports replacement must implement and
// forward ReplaceIfUnchanged explicitly; forgetting to do so silently turns
// the upstream check off.
type Replacer interface {
	// ReplaceIfUnchanged replaces the registration identified by expected's
	// ClientID, or returns the concurrent winner. Identity is the ClientID
	// alone, because DCR mints a new client_id on every registration; the
	// comparison does not consider expiry. A missing row is claimed as
	// PutIfAbsent would. Nil expected or replacement is an error. The returned
	// resolution is authoritative, independent, and non-nil on success.
	ReplaceIfUnchanged(ctx context.Context, key Key, expected, replacement *Resolution) (*Resolution, error)
}

// CloseableCredentialStore is a CredentialStore that also releases an
// underlying resource when closed. NewInMemoryStore returns it so callers can
// call Close directly, with no runtime type assertion that would silently do
// nothing if the target lacked the capability.
type CloseableCredentialStore interface {
	CredentialStore
	io.Closer
}

var (
	_ CloseableCredentialStore = (*inMemoryStore)(nil)
	_ Replacer                 = (*inMemoryStore)(nil)
)

// NewInMemoryStore returns an operation-scoped store, notably for CLI OAuth
// flows whose cross-invocation persistence lives outside the resolver. It has
// no sweeper or capacity policy; entries live as long as the returned value.
// Close is an idempotent no-op and does not prevent subsequent operations.
func NewInMemoryStore() CloseableCredentialStore {
	return &inMemoryStore{entries: make(map[Key]Resolution)}
}

type inMemoryStore struct {
	mu      sync.Mutex
	entries map[Key]Resolution
}

func (s *inMemoryStore) Get(_ context.Context, key Key) (*Resolution, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.entries[key]
	if !ok {
		return nil, false, nil
	}
	return &current, true, nil
}

func (s *inMemoryStore) PutIfAbsent(_ context.Context, key Key, resolution *Resolution) (*Resolution, error) {
	if resolution == nil {
		return nil, fmt.Errorf("dcr: resolution must not be nil")
	}
	if err := validateResolution(key, resolution); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.entries[key]; ok &&
		(current.ClientSecretExpiresAt.IsZero() || !time.Now().After(current.ClientSecretExpiresAt)) {
		return &current, nil
	}
	return s.write(key, resolution), nil
}

func (s *inMemoryStore) ReplaceIfUnchanged(_ context.Context, key Key, expected, replacement *Resolution) (*Resolution, error) {
	if expected == nil || replacement == nil {
		return nil, fmt.Errorf("dcr: expected and replacement resolutions must not be nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.entries[key]; ok && current.ClientID != expected.ClientID {
		return &current, nil
	}
	if err := validateResolution(key, replacement); err != nil {
		return nil, err
	}
	return s.write(key, replacement), nil
}

// Close is an idempotent no-op; this store owns no background resources.
func (*inMemoryStore) Close() error { return nil }

// write normalizes a copy to the persistence contract. s.mu must be held.
func (s *inMemoryStore) write(key Key, resolution *Resolution) *Resolution {
	normalized := *resolution
	normalized.RedirectURI = key.RedirectURI
	normalized.ClientIDIssuedAt = time.Time{}
	s.entries[key] = normalized
	return &normalized
}

// validateResolution applies the shared persistence validation so the
// operation-scoped store rejects what every durable backend rejects.
func validateResolution(key Key, resolution *Resolution) error {
	return dcrstore.Validate(resolutionToCredentials(key, resolution))
}

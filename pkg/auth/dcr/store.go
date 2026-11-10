// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package dcr

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/oauthproto/dcrkey"
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

// Replacer is an optional CredentialStore capability. The resolver probes
// cached registrations only when this capability is present. Wrappers must
// explicitly forward it so replacement cannot bypass their write policy.
type Replacer interface {
	// ReplaceIfUnchanged replaces the registration identified by expected's
	// ClientID, or returns the concurrent winner. A missing row is claimed as
	// PutIfAbsent would. Nil expected or replacement is an error. The returned
	// resolution is authoritative, independent, and non-nil on success.
	ReplaceIfUnchanged(ctx context.Context, key Key, expected, replacement *Resolution) (*Resolution, error)
}

// CloseableCredentialStore preserves lifecycle compatibility for callers.
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

// validateResolution retains the server storage validation errors without
// importing server-owned types. Public clients need not carry a secret.
func validateResolution(key Key, resolution *Resolution) error {
	if key.Issuer == "" {
		return fosite.ErrInvalidRequest.WithHint("dcr credentials key issuer cannot be empty")
	}
	if key.UpstreamID == "" {
		return fosite.ErrInvalidRequest.WithHint("dcr credentials key upstream_id cannot be empty")
	}
	if key.RedirectURI == "" {
		return fosite.ErrInvalidRequest.WithHint("dcr credentials key redirect_uri cannot be empty")
	}
	if key.ScopesHash == "" {
		return fosite.ErrInvalidRequest.WithHint("dcr credentials key scopes_hash cannot be empty")
	}
	if resolution.ClientID == "" {
		return fosite.ErrInvalidRequest.WithHint("dcr credentials client_id cannot be empty")
	}
	if resolution.AuthorizationEndpoint == "" {
		return fosite.ErrInvalidRequest.WithHint("dcr credentials authorization_endpoint cannot be empty")
	}
	if resolution.TokenEndpoint == "" {
		return fosite.ErrInvalidRequest.WithHint("dcr credentials token_endpoint cannot be empty")
	}
	return nil
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package dcrkey defines the canonical key and scope hash used by OAuth
// Dynamic Client Registration caches.
//
// This package is deliberately standard-library-only so both DCR resolvers and
// persistence adapters can share the same key definition without introducing a
// dependency on either side of that boundary.
package dcrkey

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"sort"
)

// Key is the canonical lookup key for a DCR registration. The tuple is
// designed so that any backend (in-memory or Redis) serialises it identically.
// ScopesHash is used rather than a raw scope slice so the key is comparable,
// fixed-size, and order-insensitive.
type Key struct {
	// Issuer is the registration consumer's issuer identifier. For an embedded
	// authorization server this is its own local issuer; for a CLI/direct OAuth
	// flow it is the upstream authorization server's issuer. The value keeps
	// registrations from different consumers separate.
	Issuer string

	// UpstreamID identifies the upstream authorization server this registration
	// is bound to. It is the issuer recovered from the discovery URL, or the
	// registration endpoint URL when one was configured directly. It is derived
	// by the DCR resolver from the Request and should not be hand-built at call
	// sites.
	UpstreamID string

	// RedirectURI is the redirect URI registered with the upstream authorization
	// server. Embedded-authserver callers use an AS-origin callback; CLI callers
	// use an RFC 8252 loopback callback.
	RedirectURI string

	// ScopesHash is the SHA-256 hex digest of the sorted, deduplicated scope
	// list. Use ScopesHash to compute this value.
	ScopesHash string
}

// ScopesHash returns the SHA-256 hex digest of the canonical OAuth scope set,
// suitable for use as Key.ScopesHash.
//
// The input is cloned before sorting. Scopes are sorted ascending, deduplicated,
// and joined with newlines without a trailing newline. Newlines prevent
// boundary collisions such as ["ab", "c"] and ["a", "bc"]. Nil and empty
// slices both canonicalise to the empty string and therefore have the same hash.
// Scope validation and normalization are intentionally outside this function.
func ScopesHash(scopes []string) string {
	sorted := slices.Clone(scopes)
	sort.Strings(sorted)
	sorted = slices.Compact(sorted)

	h := sha256.New()
	for i, scope := range sorted {
		if i > 0 {
			_, _ = h.Write([]byte("\n"))
		}
		_, _ = h.Write([]byte(scope))
	}
	return hex.EncodeToString(h.Sum(nil))
}

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
	// Issuer is the registration consumer's issuer identifier. The dual
	// semantic depends on the consumer profile:
	//   - Embedded authorization server: its OWN local issuer (the embedded
	//     authserver that performed the registration).
	//   - CLI / direct OAuth flow: the UPSTREAM authorization server's
	//     issuer, because the CLI has no separate local issuer of its own.
	// The cache is keyed by this value because two different consumers
	// registering against the same upstream are distinct OAuth clients and
	// must not share credentials. The (Issuer, UpstreamID, RedirectURI,
	// ScopesHash) tuple keeps the two consumer profiles' entries apart via
	// the RedirectURI component (the embedded authserver registers an
	// AS-origin callback while the CLI registers a loopback callback per
	// RFC 8252 §7.3 — the two address spaces are disjoint), so a collision
	// between profiles is impossible by construction even when the
	// upstream is the same. Public-client vs confidential-client
	// separation rides on that same disjoint-RedirectURI property at both
	// the persistent-cache and in-process singleflight layers; encoding it on
	// the key would invalidate every existing Redis-cached entry across a
	// deployment without buying additional protection. If a future
	// consumer brings the two address spaces into collision the key
	// format must gain a consumer-identifier component alongside an
	// explicit migration story.
	Issuer string

	// UpstreamID identifies the upstream authorization server this
	// registration is bound to, disambiguating upstreams that share the
	// consumer's Issuer, RedirectURI, and scope set. Within a single
	// embedded authserver every OAuth2 upstream shares the authserver's own
	// Issuer and the one defaulted {issuer}/oauth/callback RedirectURI, so
	// before this component existed two upstreams configured with equal
	// scopes collided on one cache entry — the second read back the first's
	// dynamically-registered client_id / client_secret and never registered
	// with its own authorization server (issue #5823).
	//
	// The value is the upstream's registration identity: the upstream issuer
	// recovered from the consumer's discovery URL, or the registration
	// endpoint URL when the consumer configured one directly. It is derived
	// by the dcr resolver from the Request; do not hand-build it at call
	// sites.
	//
	// On the discovery-URL path this is the derived issuer, so it identifies
	// the authorization server rather than the exact discovery URL: two
	// configs that resolve to the same issuer intentionally share one
	// registration. It does not fully disambiguate the nonstandard case of a
	// single issuer exposing distinct registration endpoints under custom
	// (non-well-known) discovery paths — see resolveUpstreamKeyIdentity in
	// pkg/auth/dcr for the full rationale.
	UpstreamID string

	// RedirectURI is the redirect URI registered with the upstream
	// authorization server. Embedded-authserver callers register an
	// AS-origin callback; CLI callers register an RFC 8252 loopback
	// callback. The two address spaces are disjoint, which is what makes
	// the per-consumer cache namespace structurally safe today.
	RedirectURI string

	// ScopesHash is the SHA-256 hex digest of the sorted, deduplicated scope
	// list. Use ScopesHash() to compute this value — do NOT hash scopes by
	// hand at call sites; the canonical form must be a single source of truth
	// so the key matches across processes and backends.
	ScopesHash string

	// RotationKey is an optional operator-chosen label that is part of the
	// cache identity but carries no OAuth meaning and is never sent to the
	// upstream. Changing it makes the resolver miss the cache and register a
	// fresh client. It is how an operator retires a registration the upstream
	// has dropped when the resolver's own registration probe cannot tell
	// (see authserver.DCRUpstreamConfig.RotationKey).
	// Empty for every row written before the field existed; backends MUST
	// encode an empty value exactly as they encoded a key without the field
	// so those rows stay reachable. Rows written under a previous value are
	// not deleted and follow the same lifetime rules as any superseded row.
	RotationKey string
}

// ScopesHash returns the SHA-256 hex digest of the canonical OAuth scope set,
// suitable for use as Key.ScopesHash.
//
// Canonicalisation:
//  1. Sort ascending so the digest is order-insensitive — e.g.
//     []string{"openid", "profile"} and []string{"profile", "openid"} hash to
//     the same value.
//  2. Deduplicate so that []string{"openid"} and []string{"openid", "openid"}
//     hash to the same value. An OAuth scope set is a set, not a multiset
//     (RFC 6749 §3.3), and without deduplication a caller that accidentally
//     duplicated a scope would miss cache entries and trigger redundant
//     RFC 7591 registrations.
//  3. Join with newlines (a character not valid in OAuth scope tokens per
//     RFC 6749 §3.3) to avoid collision between e.g. ["ab", "c"] and
//     ["a", "bc"].
//
// nil and empty slice both canonicalise to the same hash.
//
// The input is cloned before sorting; the caller's slice is not modified.
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

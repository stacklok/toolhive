// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package dcrstore defines the backend-neutral DCR persistence contract.
package dcrstore

//go:generate mockgen -destination=mocks/mock_store.go -package=mocks -source=types.go Store

import (
	"context"
	"errors"
	"time"

	"github.com/stacklok/toolhive/pkg/oauthproto/dcrkey"
)

var (
	// ErrDCRCredentialsChanged is returned by
	// Store.UpdateDCRCredentialsIfUnchanged when the stored row
	// no longer equals the caller's expected value: another writer replaced it
	// after the caller read it. Nothing is written. A row that is absent
	// altogether is reported as storageerr.ErrNotFound instead.
	ErrDCRCredentialsChanged = errors.New("storage: dcr credentials row changed concurrently")
)

// Credentials is the persisted form of an RFC 7591 Dynamic Client
// Registration result. All fields are populated from the upstream's DCR
// response. The RFC 7592 management fields (RegistrationAccessToken,
// RegistrationClientURI) are preserved verbatim so future rotation /
// management flows can use them.
//
// # Defensive copy
//
// Callers receive a defensive copy from the store. Mutations on the returned
// value do not affect persisted state, and mutations on a value passed to
// StoreDCRCredentialsIfAbsent are not observed by subsequent reads.
//
// # Lifetime
//
// Entries are long-lived — RFC 7591 client registrations do not expire unless
// the upstream asserts client_secret_expires_at. Backends without a native
// TTL, such as an in-process map, keep entries for the process lifetime; a
// backend with one, such as Redis, may expire a row when ClientSecretExpiresAt
// is non-zero.
//
// # Converter contract
//
// Code that converts between Credentials and another representation of a
// registration (the DCR resolver's adapter is the only one in this repository)
// MUST be updated when a field is added, renamed, or removed here. A field
// without a paired converter update silently fails to round-trip across a
// restart.
type Credentials struct {
	// Key is the canonical cache key: (Issuer, UpstreamID, RedirectURI,
	// ScopesHash) plus the optional RotationKey.
	Key dcrkey.Key

	// ProviderName is the upstream's UpstreamRunConfig.Name. Debug / audit
	// only — never used as a primary key. Two upstreams with different
	// ProviderName but identical Key share one credential record.
	ProviderName string

	ClientID                string
	ClientSecret            string //nolint:gosec // G117: field legitimately holds sensitive data
	TokenEndpointAuthMethod string

	// RegistrationAccessToken and RegistrationClientURI are RFC 7592 fields
	// captured for future management operations (rotation, deletion).
	RegistrationAccessToken string //nolint:gosec // G117: field legitimately holds sensitive data
	RegistrationClientURI   string

	AuthorizationEndpoint string
	TokenEndpoint         string

	// CreatedAt is the wall-clock time at which the registration completed.
	// Used to compute staleness for observability — the cache itself does
	// not expire entries based on age (see ClientSecretExpiresAt for the
	// authoritative expiry signal).
	CreatedAt time.Time

	// ClientSecretExpiresAt is the RFC 7591 §3.2.1 client_secret_expires_at
	// value converted from int64 epoch seconds to time.Time. The wire
	// convention is that 0 means "the secret does not expire"; this struct
	// represents that as the zero time.Time so callers can use IsZero()
	// rather than special-casing 0.
	//
	// When non-zero, this is the authoritative signal a backend uses to TTL
	// the persisted entry: the Redis backend plumbs it through SET with a
	// duration so the row evicts before the upstream rejects the secret at the
	// token endpoint. The in-memory backend ignores this field — entries
	// persist for the process lifetime and the resolver re-checks the
	// expiry on read.
	ClientSecretExpiresAt time.Time
}

// Store is a narrow, segregated interface for persisting
// dynamic-client-registration credentials. A durable backend, such as one
// backed by Redis, shares credentials across replicas and restarts.
// Implementations must be safe for concurrent use.
//
// # Cross-replica limitation
//
// Sharing DCR credentials does NOT imply cross-replica session / token
// delivery. Callers that need that must still route through the proxy runner
// and (if applicable) pin sessions to a replica.
//
// # Defensive copy
//
// Implementations MUST defensively copy on Store, Update, and Get so caller
// mutations cannot reach persisted state and vice versa.
//
// # TTL handling
//
// Implementations SHOULD honor a non-zero Credentials.ClientSecretExpiresAt
// as a backend-level TTL when the underlying store supports one (e.g. Redis
// SET with a duration) so an entry evicts before the upstream rejects the
// secret at the token endpoint. Backends without a native TTL (e.g. the
// in-process map) retain the field verbatim and rely on the caller —
// typically the DCR resolver — to re-check expiry on read.
// A zero ClientSecretExpiresAt means the upstream did not assert an expiry
// and no TTL is applied.
//
// # Why the key is embedded in Credentials
//
// Both StoreDCRCredentialsIfAbsent and UpdateDCRCredentialsIfPresent take a
// single (ctx, creds) argument rather than a separate (ctx, key, value) shape.
// The dcrkey.Key is embedded as
// Credentials.Key so the persisted blob is
// self-describing: a Redis SCAN, an admin-tool dump, or a cross-replica
// reconciliation path can identify a record's logical cache slot
// (Issuer, UpstreamID, RedirectURI, ScopesHash) from the value alone, without
// reconstructing it from a separately-passed key. Callers must populate
// creds.Key before Store or Update, and implementations validate the record
// with Validate.
type Store interface {
	// GetDCRCredentials returns the credentials for the given key.
	// Returns storageerr.ErrNotFound (wrapped) if no entry exists for the key.
	// The returned value is a defensive copy.
	GetDCRCredentials(ctx context.Context, key dcrkey.Key) (*Credentials, error)

	// StoreDCRCredentialsIfAbsent claims creds.Key for creds. Returns the
	// authoritative durable value: the caller's own creds on a successful
	// claim, the concurrent winner's value otherwise. Callers MUST use the
	// returned value, not their input creds — RFC 7591 dynamic registration
	// mints a unique client_id/client_secret on every call, so a caller that
	// lost the race and kept using its own creds would hold credentials the
	// durable store does not agree it owns. See the interface-level "TTL
	// handling" section for the contract on ClientSecretExpiresAt. The
	// returned *Credentials is always non-nil when err is nil.
	StoreDCRCredentialsIfAbsent(ctx context.Context, creds *Credentials) (*Credentials, error)

	// UpdateDCRCredentialsIfPresent replaces the record at creds.Key with
	// creds, iff a record currently exists at that key. Returns storageerr.ErrNotFound
	// (wrapped) if no entry exists — it never creates, so an update racing a
	// delete or a not-yet-created record fails loudly rather than silently
	// creating, keeping the create/update split explicit (the mirror of why
	// StoreDCRCredentialsIfAbsent never silently updates). On success it
	// returns the stored value (a defensive copy) and the returned
	// *Credentials is non-nil.
	//
	// This is the write path a storage decorator needs to rewrite a record's
	// persisted representation in place — re-encoding, compression, a
	// checksum, or similar — without changing its RFC 7591 identity or values:
	// StoreDCRCredentialsIfAbsent is create-only and would silently discard
	// such a rewrite on an existing key.
	//
	// # Presence is physical, not liveness
	//
	// "Present" means a row physically exists at creds.Key, regardless of
	// whether its ClientSecretExpiresAt has passed. This deliberately does NOT
	// mirror StoreDCRCredentialsIfAbsent's "an expired row counts as absent"
	// treatment: that check exists to let a fresh registration reclaim a dead
	// slot in the concurrent-registration race, whereas Update exists to let a
	// decorator rewrite a row it just read. GetDCRCredentials returns
	// physically-present rows without filtering on expiry, so gating Update on
	// liveness would break the Get→transform→Update round-trip the method is
	// for. An expired-but-present row is therefore updatable.
	//
	// Implementations MUST defensively copy on input (mirroring the Store/Get
	// contract) and MUST apply the same ClientSecretExpiresAt TTL handling
	// documented in the interface-level "TTL handling" section and on
	// StoreDCRCredentialsIfAbsent — the rewritten row's backend TTL is derived
	// from the incoming creds, so an update can extend, shorten, or clear the
	// row's TTL exactly as an initial store would.
	UpdateDCRCredentialsIfPresent(ctx context.Context, creds *Credentials) (*Credentials, error)

	// UpdateDCRCredentialsIfUnchanged replaces the record at creds.Key with
	// creds, iff the record currently stored there still equals expected —
	// the value the caller previously read via GetDCRCredentials. It is the
	// compare-and-set sibling of UpdateDCRCredentialsIfPresent, for callers
	// that coordinate re-registration across replicas and must not clobber a
	// newer row written by a concurrent writer after their read (e.g. a lock
	// holder that stalled past its lease before writing).
	//
	// Returns storageerr.ErrNotFound (wrapped) if no record exists at the key, and
	// ErrDCRCredentialsChanged if the stored record no longer equals
	// expected. In both cases nothing is written. On success it returns the
	// stored value (a defensive copy) and the returned *Credentials is
	// non-nil.
	//
	// Any other error leaves the row's state to be determined by re-reading
	// it. This includes transport failures, where the write may or may not
	// have been applied. It also includes a backend that bounds its internal
	// retries (the Redis backend) giving up because other writers kept
	// touching the key between its compare and its write; in that case this
	// call wrote nothing, and the caller may re-read and try again.
	//
	// expected must be non-nil and its Key must equal creds.Key. Equality is
	// over every persisted field, compared at the backend's storage precision
	// (the Redis backend persists times at one-second resolution), so a value
	// returned by GetDCRCredentials always compares equal to the row it was
	// read from.
	//
	// A decorator that transforms fields on the way in and out (encryption,
	// compression, …) must pass the inner store the stored form of expected —
	// what the inner GetDCRCredentials returned — not the decoded form it
	// handed to its own caller. When the transform is not reproducible (e.g.
	// encryption with a fresh nonce per seal), the decorator cannot derive
	// that stored form from the caller's decoded expected. Instead it should
	// re-read the inner row, decode it, return ErrDCRCredentialsChanged if
	// the decoded value differs from the caller's expected, and otherwise
	// call the inner UpdateDCRCredentialsIfUnchanged with the raw inner row
	// it just read as expected. That stays atomic: if the row changes between
	// the decorator's re-read and the inner write, the inner compare refuses
	// it. A decorator that recovers a row it cannot decode at all already
	// holds the raw inner row and passes it directly. A consumer outside the
	// decorator that needs to compare-and-set a row it cannot decode would
	// need an opaque version or etag instead; that is not provided today.
	//
	// Presence is physical, exactly as for UpdateDCRCredentialsIfPresent, and
	// the rewritten row's backend TTL follows the same ClientSecretExpiresAt
	// rules. Implementations MUST perform the compare and the write
	// atomically with respect to other writers of the same key, and MUST
	// touch only that one key so the operation stays valid under Redis
	// Cluster.
	UpdateDCRCredentialsIfUnchanged(ctx context.Context, creds, expected *Credentials) (*Credentials, error)
}

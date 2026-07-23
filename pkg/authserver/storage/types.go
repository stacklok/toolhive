// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package storage provides storage interfaces and implementations for the
// OAuth authorization server.
package storage

//go:generate mockgen -destination=mocks/mock_storage.go -package=mocks -source=types.go Storage,PendingAuthorizationStorage,ClientRegistry,UpstreamTokenStorage,UpstreamTokenRefresher,UserStorage,DCRCredentialStore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/ory/fosite/handler/pkce"
)

// Sentinel errors for storage operations.
// Use errors.Is() to check for these error types.
var (
	// ErrNotFound is returned when an item is not found in storage.
	ErrNotFound = errors.New("storage: item not found")

	// ErrExpired is returned when an item exists but has expired.
	ErrExpired = errors.New("storage: item expired")

	// ErrAlreadyExists is returned when attempting to create an item that already exists.
	ErrAlreadyExists = errors.New("storage: item already exists")

	// ErrInvalidBinding is returned when token binding validation fails
	// (e.g., subject or client ID mismatch).
	ErrInvalidBinding = errors.New("storage: token binding validation failed")
)

// DefaultPendingAuthorizationTTL is the default TTL for pending authorization requests.
const DefaultPendingAuthorizationTTL = 10 * time.Minute

// ExpectedBinding carries the caller-asserted identity a stored upstream-token
// row must match before it is released. A nil *ExpectedBinding means "resolve
// the user from ctx only" (see checkUpstreamBinding); nil/empty fields skip
// that dimension for backward compatibility with rows written before binding
// fields existed.
type ExpectedBinding struct {
	// UserID is the internal ToolHive user the row must belong to. When empty,
	// the canonical platform user resolved from ctx is used instead (if any).
	UserID string
	// ClientID is the OAuth client that must have consented the row.
	ClientID string
	// UpstreamSubject is the upstream IdP subject the row must carry. Asserted
	// only where the caller knows the expected subject (e.g. the chain-consistency
	// read of the first leg).
	UpstreamSubject string
	// Strict fails closed on legacy rows: when true, a stored row with an empty
	// UserID fails with ErrInvalidBinding instead of being released under the
	// permissive empty-field rule. Wave 1 sets this for untrusted workloads,
	// where a row that cannot prove its owner must never be released.
	// Default false preserves the legacy permissive behavior.
	Strict bool
}

// UpstreamTokens represents tokens obtained from an upstream Identity Provider.
// These tokens are stored with binding fields for security validation and
// ProviderID for multi-IDP support.
type UpstreamTokens struct {
	// ProviderID identifies which upstream provider issued these tokens.
	// This is the logical provider name matching UpstreamConfig.Name,
	// used as the storage key for multi-upstream token lookups.
	ProviderID string

	// Token values from the upstream IDP
	AccessToken  string //nolint:gosec // G117: field legitimately holds sensitive data
	RefreshToken string //nolint:gosec // G117: field legitimately holds sensitive data
	IDToken      string
	// ExpiresAt is when the access token expires. Zero value means the provider
	// did not assert an expiry; callers must treat it as non-expiring.
	ExpiresAt time.Time
	// SessionExpiresAt is the Fosite session expiry time. The callback handler
	// sets it from RefreshTokenLifespan on every initial write; the refresher
	// carries it forward unchanged on each subsequent refresh, and defensively
	// re-anchors it on legacy rows where both ExpiresAt and SessionExpiresAt
	// are zero. Storage backends use it as a fallback storage lifetime when
	// ExpiresAt is zero (non-expiring upstream token), bounding the row to the
	// Fosite session. Set unconditionally — including for tokens with their own
	// ExpiresAt — so the refresh path is safe even if the upstream provider stops
	// asserting expires_in on a later rotation.
	SessionExpiresAt time.Time

	// Security binding fields - validated on lookup to prevent cross-session attacks

	// UserID is the internal ToolHive user ID (references User.ID).
	// This is NOT the upstream provider's subject - it's our stable internal identifier.
	// In multi-IDP scenarios, the same UserID may have tokens from multiple providers.
	UserID string

	// UpstreamSubject is the resolved subject of the upstream IDP's ID token:
	// the "sub" claim by default, or the provider's configured SubjectClaim
	// (e.g. Entra's "oid"). This enables validation that tokens match the
	// expected upstream identity and supports audit logging of which upstream
	// identity was used.
	UpstreamSubject string

	// ClientID is the OAuth client that initiated the authorization.
	// Tokens should only be accessible to the same client that obtained them.
	ClientID string
}

// IsExpired returns true if the access token has expired.
// Returns true for nil receivers (treating nil tokens as expired).
// Zero ExpiresAt means the provider did not assert an expiry; returns false.
// The now parameter allows for deterministic testing.
//
// This method is intended for storage eviction decisions only — it checks exact
// expiry with no preemptive buffer. For "should I refresh before using?" decisions,
// use upstream.Tokens.IsExpiredAt which includes a preemptive buffer window.
func (t *UpstreamTokens) IsExpired(now time.Time) bool {
	if t == nil {
		return true
	}
	return !t.ExpiresAt.IsZero() && now.After(t.ExpiresAt)
}

// DCRKey is the canonical lookup key for a DCR registration. The tuple is
// designed so that any backend (in-memory or Redis) serialises it identically
// without redefining the canonical form. ScopesHash is used rather than a raw
// scope slice so the key is comparable, fixed-size, and order-insensitive.
//
// The key lives in the storage package because both MemoryStorage and the
// future Redis backend must hash keys identically; keeping the canonical form
// next to the persistence implementations prevents drift.
type DCRKey struct {
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
}

// ScopesHash returns the SHA-256 hex digest of the canonical OAuth scope set,
// suitable for use as DCRKey.ScopesHash.
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
func ScopesHash(scopes []string) string {
	sorted := slices.Clone(scopes)
	sort.Strings(sorted)
	sorted = slices.Compact(sorted)

	h := sha256.New()
	for i, s := range sorted {
		if i > 0 {
			_, _ = h.Write([]byte("\n"))
		}
		_, _ = h.Write([]byte(s))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// validateDCRCredentialsForStore enforces the rejection contract that every
// DCRCredentialStore implementation must apply before persisting. Extracting
// it as a free function called by every backend prevents the validation set
// from drifting across implementations: a record that fails loud against one
// backend cannot silently persist against another.
//
// Rejected inputs: nil creds, an unpopulated Key (empty Issuer, UpstreamID,
// RedirectURI, or ScopesHash), and missing RFC 7591 mandatory response fields
// (ClientID, AuthorizationEndpoint, TokenEndpoint). An empty ScopesHash is
// rejected because the canonical digest of any scope set — including the
// empty-scope set via ScopesHash(nil) — is non-empty, so an empty string can
// only be a caller bug. An empty UpstreamID is rejected because the resolver
// always derives it from the Request (issue #5823); an empty value here would
// re-open the cross-upstream collision it was added to close. ClientSecret is
// left permissive because RFC 7591 §2 public clients (auth method "none")
// legitimately register without a secret.
func validateDCRCredentialsForStore(creds *DCRCredentials) error {
	if creds == nil {
		return fosite.ErrInvalidRequest.WithHint("dcr credentials cannot be nil")
	}
	if creds.Key.Issuer == "" {
		return fosite.ErrInvalidRequest.WithHint("dcr credentials key issuer cannot be empty")
	}
	if creds.Key.UpstreamID == "" {
		return fosite.ErrInvalidRequest.WithHint("dcr credentials key upstream_id cannot be empty")
	}
	if creds.Key.RedirectURI == "" {
		return fosite.ErrInvalidRequest.WithHint("dcr credentials key redirect_uri cannot be empty")
	}
	if creds.Key.ScopesHash == "" {
		return fosite.ErrInvalidRequest.WithHint("dcr credentials key scopes_hash cannot be empty")
	}
	if creds.ClientID == "" {
		return fosite.ErrInvalidRequest.WithHint("dcr credentials client_id cannot be empty")
	}
	if creds.AuthorizationEndpoint == "" {
		return fosite.ErrInvalidRequest.WithHint("dcr credentials authorization_endpoint cannot be empty")
	}
	if creds.TokenEndpoint == "" {
		return fosite.ErrInvalidRequest.WithHint("dcr credentials token_endpoint cannot be empty")
	}
	return nil
}

// DCRCredentials is the persisted form of an RFC 7591 Dynamic Client
// Registration result. All fields are populated from the upstream's DCR
// response. The RFC 7592 management fields (RegistrationAccessToken,
// RegistrationClientURI) are preserved verbatim so future rotation /
// management flows can use them.
//
// # Defensive copy
//
// Callers receive a defensive copy from the store. Mutations on the returned
// value do not affect persisted state, and mutations on a value passed to
// StoreDCRCredentials are not observed by subsequent reads. This matches the
// UpstreamTokens contract.
//
// # Lifetime
//
// Entries are long-lived — RFC 7591 client registrations do not expire unless
// the upstream asserts client_secret_expires_at. The in-memory backend
// retains entries for the process lifetime and is intentionally excluded from
// the periodic cleanup loop. The Redis backend applies TTL via SET with a
// duration when ClientSecretExpiresAt is non-zero.
//
// # Converter contract
//
// MUST update both converters in
// pkg/auth/dcr/store.go (resolutionToCredentials and
// credentialsToResolution) when adding, renaming, or removing a field
// here. The two converters are the only translation seam between this
// persisted type and the dcr-package *dcr.Resolution; a field added here
// without a paired converter update will silently fail to round-trip
// across an authserver restart. The round-trip behaviour is pinned by
// TestResolutionCredentialsRoundTrip in
// pkg/auth/dcr/store_test.go.
type DCRCredentials struct {
	// Key is the canonical cache key: (Issuer, UpstreamID, RedirectURI, ScopesHash).
	Key DCRKey

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

// DCRCredentialStore is a narrow, segregated interface for persisting
// dynamic-client-registration credentials. Both MemoryStorage and
// RedisStorage implement it; an authserver backed by Redis shares DCR
// credentials across replicas and restarts.
//
// # Cross-replica limitation
//
// Sharing DCR credentials does NOT imply cross-replica session / token
// delivery. Callers that need that must still route through the proxy runner
// and (if applicable) pin sessions to a replica.
//
// # Defensive copy
//
// Implementations MUST defensively copy on both Store and Get so caller
// mutations cannot reach persisted state and vice versa, mirroring the
// UpstreamTokens contract.
//
// # TTL handling
//
// Implementations SHOULD honor a non-zero DCRCredentials.ClientSecretExpiresAt
// as a backend-level TTL when the underlying store supports one (e.g. Redis
// SET with a duration) so an entry evicts before the upstream rejects the
// secret at the token endpoint. Backends without a native TTL (e.g. the
// in-memory backend) retain the field verbatim and rely on the caller —
// typically the runner's resolver — to re-check expiry on read; see
// MemoryStorage.GetDCRCredentials.
// A zero ClientSecretExpiresAt means the upstream did not assert an expiry
// and no TTL is applied.
//
// # Why the key is embedded in DCRCredentials
//
// StoreDCRCredentials takes a single (ctx, creds) argument rather than the
// (ctx, key, value) shape used by sibling Store* methods on Storage. The
// DCRKey is embedded as DCRCredentials.Key so the persisted blob is
// self-describing: a Redis SCAN, an admin-tool dump, or a cross-replica
// reconciliation path can identify a record's logical cache slot
// (Issuer, UpstreamID, RedirectURI, ScopesHash) from the value alone, without
// reconstructing it from a separately-passed key. This is a deliberate
// asymmetry with the rest of the package — callers must populate creds.Key
// before Store, and implementations validate it (see MemoryStorage docs
// for the rejected-input list).
type DCRCredentialStore interface {
	// GetDCRCredentials returns the credentials for the given key.
	// Returns ErrNotFound (wrapped) if no entry exists for the key.
	// The returned value is a defensive copy.
	GetDCRCredentials(ctx context.Context, key DCRKey) (*DCRCredentials, error)

	// StoreDCRCredentials persists the credentials, overwriting any existing
	// entry for the same Key. See the interface-level "TTL handling" section
	// for the contract on ClientSecretExpiresAt.
	StoreDCRCredentials(ctx context.Context, creds *DCRCredentials) error
}

// User represents a user account in the authorization server.
// A user can have multiple linked provider identities.
// The User.ID is used as the "sub" claim in JWTs issued by ToolHive,
// providing a stable identity across multiple upstream identity providers.
type User struct {
	// ID is the internal, stable user identifier (UUID format).
	// This value is used as the "sub" claim in ToolHive-issued JWTs.
	ID string

	// CreatedAt is when the user account was created.
	CreatedAt time.Time

	// UpdatedAt is when the user account was last modified.
	UpdatedAt time.Time
}

// ProviderIdentity links a user to an upstream identity provider.
// Multiple identities can be linked to a single user for account linking,
// enabling users to authenticate via different providers (e.g., Google and GitHub)
// while maintaining a single ToolHive identity.
type ProviderIdentity struct {
	// UserID is the internal user ID this identity belongs to.
	UserID string

	// ProviderID identifies the upstream provider (e.g., "google", "github").
	ProviderID string

	// ProviderSubject is the subject identifier from the upstream provider.
	ProviderSubject string

	// LinkedAt is when this identity was linked to the user.
	LinkedAt time.Time

	// LastUsedAt is when this identity was last used to authenticate.
	LastUsedAt time.Time
}

// PendingAuthorization tracks a client's authorization request while they
// authenticate with the upstream IDP.
type PendingAuthorization struct {
	// ClientID is the ID of the OAuth client making the authorization request.
	ClientID string

	// RedirectURI is the client's callback URL where we'll redirect after authentication.
	RedirectURI string

	// State is the client's original state parameter for CSRF protection.
	State string

	// PKCEChallenge is the client's PKCE code challenge.
	PKCEChallenge string

	// PKCEMethod is the PKCE challenge method (must be "S256").
	PKCEMethod string

	// Scopes are the OAuth scopes requested by the client.
	Scopes []string

	// InternalState is our randomly generated state for correlating upstream callback.
	InternalState string

	// UpstreamPKCEVerifier is the PKCE code_verifier for the upstream IDP authorization.
	// This is generated when redirecting to the upstream IDP and used when exchanging
	// the authorization code. See RFC 7636.
	UpstreamPKCEVerifier string

	// UpstreamNonce is the OIDC nonce parameter sent to the upstream IDP for ID Token
	// replay protection. This is validated against the nonce claim in the returned
	// ID Token. See OIDC Core Section 3.1.2.1.
	UpstreamNonce string

	// UpstreamProviderName identifies the upstream provider being authenticated in this
	// authorization chain leg. Used in multi-upstream scenarios to route the callback
	// to the correct provider.
	UpstreamProviderName string

	// SessionID is the TSID for session accumulation across authorization chain legs.
	// In multi-upstream scenarios, the same session accumulates tokens from multiple
	// providers across successive authorization legs.
	SessionID string

	// ResolvedUserID is the internal user ID resolved from the primary (first) upstream.
	// Empty on the first leg; populated after the first callback for subsequent legs.
	ResolvedUserID string

	// ResolvedUserName is the user display name from the primary upstream.
	// Empty on the first leg; populated after the first callback for subsequent legs.
	ResolvedUserName string

	// ResolvedUserEmail is the user email from the primary upstream.
	// Empty on the first leg; populated after the first callback for subsequent legs.
	ResolvedUserEmail string

	// SingleLeg scopes this authorization to exactly one upstream. When true, the
	// callback issues the authorization code as soon as this leg completes instead
	// of consulting nextMissingUpstream to continue the chain. This lets a caller
	// connect a single specific provider without other configured-but-tokenless
	// upstreams hijacking the flow into a full chain walk. Defaults to false, which
	// preserves the multi-upstream chaining behavior.
	SingleLeg bool

	// ChainUpstreams is the ordered, effective set of upstream provider names this
	// authorization must walk, always led by the required first upstream. It is
	// computed once when the first leg resolves — narrowed by the optional upstream
	// filter when one is configured — and carried forward across subsequent legs so
	// the filter is not re-run per leg. Empty on the first leg's inbound pending;
	// populated on every subsequent leg. A subsequent leg that arrives with this
	// unset (e.g. a pending written before this field existed) is rejected rather
	// than recomputed, so the filter is never re-run against a later leg's context.
	ChainUpstreams []string

	// CreatedAt is when the pending authorization was created.
	CreatedAt time.Time
}

// PendingAuthorizationStorage provides storage operations for pending authorization requests.
// These track the state of in-flight authorization requests while users authenticate
// with the upstream IDP, correlating the upstream callback with the original client request.
type PendingAuthorizationStorage interface {
	// StorePendingAuthorization stores a pending authorization request.
	// The state is used to correlate the upstream IDP callback.
	StorePendingAuthorization(ctx context.Context, state string, pending *PendingAuthorization) error

	// LoadPendingAuthorization retrieves a pending authorization by internal state.
	// Returns ErrNotFound if the state does not exist.
	// Returns ErrExpired if the pending authorization has expired.
	LoadPendingAuthorization(ctx context.Context, state string) (*PendingAuthorization, error)

	// DeletePendingAuthorization removes a pending authorization.
	// Returns ErrNotFound if the state does not exist.
	DeletePendingAuthorization(ctx context.Context, state string) error
}

// ClientRegistry provides client registration and lookup operations.
// It embeds fosite.ClientManager for client lookup (GetClient) and adds
// RegisterClient for dynamic client registration (RFC 7591).
type ClientRegistry interface {
	// ClientManager provides client lookup (GetClient)
	fosite.ClientManager

	// RegisterClient registers a new OAuth client.
	// This supports both static configuration and dynamic client registration (RFC 7591).
	// Returns ErrAlreadyExists if a client with the same ID already exists.
	RegisterClient(ctx context.Context, client fosite.Client) error

	// RenewClientTTL extends the registration TTL of a public (DCR) client so an
	// actively-used client is not evicted mid-lifecycle and forced to re-register.
	// Call it on a proven-use signal (e.g. a successful token exchange), NOT on an
	// unauthenticated client read such as the /oauth/authorize lookup. Implementations
	// renew only public clients; confidential clients have no TTL. Backends without a
	// native TTL (the in-memory backend) treat this as a no-op. A renewal failure is
	// non-fatal to the caller's primary operation.
	RenewClientTTL(ctx context.Context, client fosite.Client) error
}

// UpstreamTokenStorage provides storage for tokens obtained from upstream identity providers.
// The auth server exposes this interface via Server.UpstreamTokenStorage() for use by
// middleware that needs to retrieve upstream tokens (e.g., token swap middleware that
// replaces JWT auth with upstream IDP tokens for backend requests).
//
// Tokens are keyed primarily by (sessionID, providerName) to support multiple upstream
// providers per session. Each provider's tokens are stored and retrieved independently.
// A secondary lookup by (userID, providerID) is exposed via GetLatestUpstreamTokensForUser;
// see that method for usage and security contract.
type UpstreamTokenStorage interface {
	// StoreUpstreamTokens stores the upstream IDP tokens for a session and provider.
	// The providerName identifies which upstream provider these tokens belong to.
	StoreUpstreamTokens(ctx context.Context, sessionID, providerName string, tokens *UpstreamTokens) error

	// GetUpstreamTokens retrieves the upstream IDP tokens for a session and provider.
	// Returns ErrNotFound if the session/provider combination does not exist.
	// Returns ErrInvalidBinding (with nil tokens) if the stored row's binding does
	// not match the expected binding — see ExpectedBinding for the per-dimension
	// empty-field rule and the ctx fallback. Binding is checked before expiry, so
	// a mismatched row never surfaces ErrExpired: the refresh path must not be
	// entered for a row that isn't the caller's.
	// Returns ErrExpired if the tokens have expired. When ErrExpired is returned,
	// the token data (including refresh token) is also returned to allow callers
	// to attempt a token refresh.
	GetUpstreamTokens(
		ctx context.Context, sessionID, providerName string, expected *ExpectedBinding,
	) (*UpstreamTokens, error)

	// GetAllUpstreamTokens retrieves all upstream IDP tokens for a session across all providers.
	// Returns a map of providerName -> tokens. Returns an empty map (not error) for unknown sessions.
	// Includes expired tokens (no expiry filtering at bulk-read level).
	// Binding validation (see ExpectedBinding) applies per row: a row whose binding
	// fails is excluded from the result map (a WARN is logged) rather than failing
	// the whole read — callers treat a missing provider as "needs consent", which
	// is the safe degradation.
	GetAllUpstreamTokens(ctx context.Context, sessionID string, expected *ExpectedBinding) (map[string]*UpstreamTokens, error)

	// DeleteUpstreamTokens removes all upstream IDP tokens for a session (all providers).
	// Returns ErrNotFound if the session does not exist.
	DeleteUpstreamTokens(ctx context.Context, sessionID string) error

	// DeleteUpstreamTokensForProvider removes tokens for a single (sessionID, providerName),
	// leaving sibling providers' rows intact. Deleting an absent row is NOT an error (nil).
	// Used as best-effort cleanup of a refresh token rotated at the IdP but not persisted.
	DeleteUpstreamTokensForProvider(ctx context.Context, sessionID, providerName string) error

	// GetLatestUpstreamTokensForUser returns the most recently stored upstream tokens
	// for (userID, providerID) across any session. The "latest" winner is determined
	// by treating non-expiring rows (zero ExpiresAt — providers like Slack and GitHub
	// OAuth Apps that genuinely never expire) as the strongest candidate, falling
	// back to ExpiresAt descending among finite-expiry rows. This aligns with the
	// rest of the package treating zero ExpiresAt as "alive forever" (see IsExpired,
	// cleanupExpired, marshalUpstreamTokensWithTTL). Both backends use the same
	// rule so the carry-forward decision is consistent regardless of deployment
	// shape.
	//
	// This is a secondary lookup that intentionally bypasses the primary
	// (sessionID, providerName) key. It is used by the OAuth callback to preserve a
	// previously-issued refresh token when the upstream IdP omits refresh_token on
	// re-authorization (e.g. Google without prompt=consent). When a sessionID is
	// available, callers should use GetUpstreamTokens instead. This method mirrors
	// the preservation pattern in upstreamTokenRefresher.RefreshAndStore.
	//
	// # Liveness
	//
	// The returned tokens may be expired. Callers MUST NOT assume liveness; they
	// must handle the expired case (typically by reading only the RefreshToken,
	// which remains usable past the access token's expiry). This method does NOT
	// return ErrExpired and the implementation MUST NOT filter expired rows.
	//
	// # Cross-identity safety
	//
	// The returned record is NOT filtered by upstream subject. The same internal
	// UserID can in principle map to multiple upstream subjects on the same provider
	// (account-linking edge cases or data-integrity issues). Callers MUST verify
	// that prior.UpstreamSubject == currentProviderSubject before reusing any
	// credential from the returned record. The OAuth callback applies this guard;
	// other future callers must do the same.
	//
	// Returns ErrNotFound if no record exists for the (userID, providerID) pair.
	GetLatestUpstreamTokensForUser(ctx context.Context, userID, providerID string) (*UpstreamTokens, error)
}

// UpstreamTokenRefresher can refresh expired upstream tokens using their stored refresh token.
// This is implemented by the auth server and used by the upstreamswap middleware to
// transparently refresh tokens without forcing re-authentication.
type UpstreamTokenRefresher interface {
	// RefreshAndStore refreshes the upstream tokens for the given session using
	// the stored refresh token, stores the new tokens, and returns them.
	// Returns an error if the refresh token is empty, revoked, or the refresh fails.
	RefreshAndStore(ctx context.Context, sessionID string, expired *UpstreamTokens) (*UpstreamTokens, error)
}

// UserStorage provides user and provider identity management operations.
// This interface supports multi-IDP scenarios where a single user can authenticate
// via multiple upstream identity providers (e.g., Google and GitHub).
//
// The User type represents the internal ToolHive identity. Its ID becomes the "sub"
// claim in issued JWTs, providing a stable identity across multiple providers.
//
// ProviderIdentity links a user to a specific upstream provider. The
// (ProviderID, ProviderSubject) pair uniquely identifies an upstream identity.
//
// # Account Linking Security
//
// When implementing account linking (one User with multiple ProviderIdentities),
// callers MUST verify user consent before linking. See OAuth 2.0 Security BCP.
//
// TODO(auth): When implementing double-hop auth (Company IDP -> External IDP),
// add the following to this interface:
//   - DeleteProviderIdentity(providerID, subject) - unlink specific provider
//   - Add Primary field to ProviderIdentity to distinguish Company IDP from linked External IDPs
//   - Add ConsentRecord tracking for external provider linking
type UserStorage interface {
	// CreateUser creates a new user account.
	// Returns ErrAlreadyExists if a user with the same ID already exists.
	CreateUser(ctx context.Context, user *User) error

	// GetUser retrieves a user by their internal ID.
	// Returns ErrNotFound if the user does not exist.
	GetUser(ctx context.Context, id string) (*User, error)

	// DeleteUser removes a user account.
	// Returns ErrNotFound if the user does not exist.
	DeleteUser(ctx context.Context, id string) error

	// CreateProviderIdentity links a provider identity to a user.
	// For account linking scenarios, caller MUST verify user owns the target User
	// (typically via active authenticated session) before linking a new provider.
	// Returns ErrAlreadyExists if this provider identity is already linked.
	CreateProviderIdentity(ctx context.Context, identity *ProviderIdentity) error

	// GetProviderIdentity retrieves a provider identity by provider ID and subject.
	// This is the primary lookup path during authentication callbacks.
	// Returns ErrNotFound if the identity does not exist.
	GetProviderIdentity(ctx context.Context, providerID, providerSubject string) (*ProviderIdentity, error)

	// UpdateProviderIdentityLastUsed updates the LastUsedAt timestamp for a provider identity.
	// This should be called after each successful authentication via this identity.
	// The timestamp supports OIDC auth_time claim when clients use max_age parameter.
	// Returns ErrNotFound if the identity does not exist.
	UpdateProviderIdentityLastUsed(ctx context.Context, providerID, providerSubject string, lastUsedAt time.Time) error

	// GetUserProviderIdentities returns all provider identities linked to a user.
	// This enables queries like "when did this user last authenticate via any provider"
	// which is needed for OIDC max_age enforcement.
	// Returns an empty slice (not error) if the user exists but has no linked identities.
	// Returns ErrNotFound if the user does not exist.
	GetUserProviderIdentities(ctx context.Context, userID string) ([]*ProviderIdentity, error)
}

// Storage combines fosite storage interfaces with ToolHive-specific storage for
// upstream IDP tokens, pending authorization requests, and client registration.
// The auth server requires a Storage implementation; use NewMemoryStorage() for
// single-instance deployments or NewRedisStorage() for distributed deployments.
//
// # Fosite Interface Segregation
//
// Fosite splits storage into separate interfaces (AuthorizeCodeStorage, AccessTokenStorage,
// RefreshTokenStorage, PKCERequestStorage) following the Interface Segregation Principle.
// This enables:
//   - Feature composition: Only enable OAuth features you need
//   - Testing isolation: Mock specific interfaces for focused tests
//   - Clear contracts: Each interface documents its requirements
//
// # Key Design Patterns
//
// All token storage methods store fosite.Requester (not just token values) because token
// validation requires the full authorization context (client, scopes, session).
//
// Methods use two key types:
//   - Signature: Cryptographic token identifier for token-specific operations
//   - Request ID: Grant identifier for finding all tokens from one authorization
//
// See doc.go for comprehensive documentation of fosite's storage design.
type Storage interface {
	// Embed segregated interfaces for IDP tokens, pending authorizations, client registry,
	// and user management for multi-IDP support.
	//
	// DCRCredentialStore is intentionally NOT embedded here: doing so would
	// promote GetDCRCredentials / StoreDCRCredentials onto every consumer of
	// storage.Storage (handlers, server, registration, etc.), broadening the
	// surface that can read raw client_secret / registration_access_token even
	// when those consumers have no DCR responsibility. Code that legitimately
	// needs DCR access (the runner and authserver constructors) obtains it
	// via an explicit `stor.(DCRCredentialStore)` type assertion at the
	// boundary; the per-backend `var _ DCRCredentialStore = (*MemoryStorage)(nil)`
	// and `var _ DCRCredentialStore = (*RedisStorage)(nil)` checks in
	// memory.go / redis.go provide the compile-time guarantee that production
	// backends satisfy the interface, so the runtime assertion is provably
	// safe at the boundary while keeping the wider Storage surface narrow.
	UpstreamTokenStorage
	PendingAuthorizationStorage
	ClientRegistry
	UserStorage

	// AuthorizeCodeStorage provides authorization code storage for the Authorization Code
	// Grant (RFC 6749 Section 4.1). Authorization codes are one-time-use and short-lived.
	// CreateAuthorizeCodeSession stores by code, GetAuthorizeCodeSession retrieves by code,
	// InvalidateAuthorizeCodeSession marks as used (subsequent Gets return ErrInvalidatedAuthorizeCode).
	oauth2.AuthorizeCodeStorage

	// AccessTokenStorage provides access token session storage. Methods use "signature"
	// (derived from token value) as the key for O(1) lookup when validating tokens.
	// The stored fosite.Requester contains the full authorization context including
	// the Session with expiration times via session.GetExpiresAt(fosite.AccessToken).
	oauth2.AccessTokenStorage

	// RefreshTokenStorage provides refresh token session storage. CreateRefreshTokenSession
	// accepts an accessSignature to link refresh tokens to their access tokens for rotation.
	// RotateRefreshToken uses requestID to invalidate both the refresh token and all
	// related access tokens from the same authorization grant.
	oauth2.RefreshTokenStorage

	// TokenRevocationStorage provides token revocation per RFC 7009. RevokeAccessToken
	// and RevokeRefreshToken take requestID (not signature) because RFC 7009 requires
	// revoking a refresh token SHOULD also invalidate associated access tokens, which
	// requires finding all tokens by their common grant identifier.
	oauth2.TokenRevocationStorage

	// PKCERequestStorage provides PKCE challenge/verifier storage (RFC 7636).
	// Stores the code_challenge during authorization, validates code_verifier during
	// token exchange. Keyed by the same code/signature as the authorization code.
	pkce.PKCERequestStorage

	// Health checks connectivity to the storage backend.
	// Returns nil if the storage is healthy and reachable.
	Health(ctx context.Context) error

	// Close releases any resources held by the storage implementation.
	// This should be called when the storage is no longer needed.
	Close() error
}

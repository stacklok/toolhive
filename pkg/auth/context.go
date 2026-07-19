// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package auth provides authentication and authorization utilities.
package auth

import (
	"context"
	"errors"

	"github.com/golang-jwt/jwt/v5"
)

// IdentityContextKey is the key used to store Identity in the request context.
// This provides type-safe context storage and retrieval for authenticated identities.
//
// Using an empty struct as the key prevents collisions with other context keys,
// as each empty struct type is distinct even if they have the same name in different packages.
type IdentityContextKey struct{}

// WithIdentity stores an Identity in the context.
// If identity is nil, the original context is returned unchanged.
//
// This function is typically called by authentication middleware after successful
// authentication to make the identity available to downstream handlers.
//
// Side effect: if the context carries an IdentityHolder (injected by the audit
// middleware wrapping this call), the identity is also published into it. Two
// invariants follow for callers on request-derived contexts:
//   - Last write wins: the holder reports the most recently attached identity,
//     so audit events carry that principal. Do not attach a different principal
//     (e.g. a re-minted service identity) on a request-derived context unless
//     that is the principal audit should report.
//   - Call only on the request goroutine: the audit middleware reads the holder
//     when the response is written, so off-goroutine writes would race.
//
// Example:
//
//	identity := &Identity{PrincipalInfo: PrincipalInfo{Subject: "user123", Name: "Alice"}}
//	ctx = WithIdentity(ctx, identity)
func WithIdentity(ctx context.Context, identity *Identity) context.Context {
	if identity == nil {
		return ctx
	}
	// Also publish the identity to an IdentityHolder if one is present, so
	// middleware wrapping the auth middleware (e.g. audit) can observe the
	// identity even though the derived context only flows downstream.
	if holder, ok := IdentityHolderFromContext(ctx); ok {
		holder.Identity = identity
	}
	return context.WithValue(ctx, IdentityContextKey{}, identity)
}

// IdentityHolderContextKey is the key used to store an IdentityHolder in the
// request context.
type IdentityHolderContextKey struct{}

// IdentityHolder is a mutable carrier that lets middleware running OUTSIDE the
// auth middleware observe the authenticated identity. Context values only flow
// downstream, so a wrapper such as the audit middleware cannot read the
// identity that auth attaches for inner handlers. The wrapper injects an empty
// holder via WithIdentityHolder before calling the inner chain; WithIdentity
// fills it when the identity is attached; the wrapper reads it after the inner
// chain returns.
//
// The holder is written and read by the single request goroutine (writes
// happen-before the wrapper's post-ServeHTTP read), mirroring the audit
// package's BackendInfo pattern, so no synchronization is needed.
type IdentityHolder struct {
	Identity *Identity
}

// WithIdentityHolder returns a new context carrying the given IdentityHolder.
func WithIdentityHolder(ctx context.Context, holder *IdentityHolder) context.Context {
	return context.WithValue(ctx, IdentityHolderContextKey{}, holder)
}

// IdentityHolderFromContext retrieves the IdentityHolder from the context.
// Returns (nil, false) if no holder is present.
func IdentityHolderFromContext(ctx context.Context) (*IdentityHolder, bool) {
	holder, ok := ctx.Value(IdentityHolderContextKey{}).(*IdentityHolder)
	return holder, ok && holder != nil
}

// IdentityFromContext retrieves an Identity from the context.
// Returns the identity and true if a non-nil identity is present, nil and false otherwise.
// A typed-nil *Identity stored directly in the context (bypassing WithIdentity) is
// treated as absent so callers can safely gate on the boolean without nil checks.
//
// This function is typically called by authorization middleware or handlers that need
// to check who the authenticated user is.
//
// Example:
//
//	identity, ok := IdentityFromContext(ctx)
//	if !ok {
//	    return errors.New("no authenticated identity")
//	}
//	log.Printf("Request from user: %s", identity.Subject)
func IdentityFromContext(ctx context.Context) (*Identity, bool) {
	identity, ok := ctx.Value(IdentityContextKey{}).(*Identity)
	return identity, ok && identity != nil
}

// PlatformUserContextKey is the key used to store the platform's canonical user
// identifier in the request context. It is deliberately distinct from
// IdentityContextKey.
//
// A value under this key means only "this is the canonical user to key storage
// on" — it is NOT proof that an authenticated principal is present. The two are
// kept structurally separate so a storage-scoped user id can never be mistaken
// for a validated identity: authorizers and other consumers that need the
// authenticated caller use IdentityFromContext (which carries the validated token
// and claims), while storage layers that key on the canonical user use
// CanonicalUserFromContext.
type PlatformUserContextKey struct{}

// WithPlatformUser stores the platform's canonical user identifier in the context.
// If userID is empty, the original context is returned unchanged.
//
// Use this — not WithIdentity — on paths that have resolved the user but have no
// validated identity to assert (e.g. the OAuth callback, which resolves the user
// while it is still minting the ToolHive bearer, so no validated token/claims
// exist yet). Reusing WithIdentity there would place a credential-free stub under
// the identity key that later readers could mistake for an authenticated principal.
//
// Request-serving paths that already carry a validated identity do NOT need to
// call this — CanonicalUserFromContext falls back to the Identity's PlatformUserID
// there, so the canonical user is read from one accessor without storing it twice.
func WithPlatformUser(ctx context.Context, userID string) context.Context {
	if userID == "" {
		return ctx
	}
	return context.WithValue(ctx, PlatformUserContextKey{}, userID)
}

// PlatformUserFromContext retrieves the platform's canonical user identifier from
// the dedicated platform-user key. Returns the identifier and true if present,
// "" and false otherwise.
//
// Most callers should use CanonicalUserFromContext, which also resolves the user
// from a validated Identity on request-serving paths. Use this only when you
// specifically need the dedicated key and must NOT fall back to an Identity.
func PlatformUserFromContext(ctx context.Context) (string, bool) {
	userID, ok := ctx.Value(PlatformUserContextKey{}).(string)
	return userID, ok
}

// CanonicalUserFromContext returns the platform's canonical user identifier for
// storage keying. It is the single accessor storage layers should use, regardless
// of which path produced the context.
//
// It prefers the dedicated platform-user key (set on identity-less paths such as
// the OAuth callback) and falls back to the authenticated Identity's
// PlatformUserID (set on request-serving paths). Returns "" and false if neither
// is present. The dedicated key wins so an explicit WithPlatformUser can override
// the identity-derived value.
func CanonicalUserFromContext(ctx context.Context) (string, bool) {
	if userID, ok := PlatformUserFromContext(ctx); ok {
		return userID, true
	}
	if identity, ok := IdentityFromContext(ctx); ok && identity.PlatformUserID != "" {
		return identity.PlatformUserID, true
	}
	return "", false
}

// claimsToIdentity converts JWT claims to Identity struct.
// It requires the 'sub' claim per OIDC Core 1.0 spec § 5.1.
// The original token can be provided for passthrough scenarios.
//
// Note: The Groups field is intentionally NOT populated here.
// Authorization logic MUST extract groups from the Claims map, as group claim
// names vary by provider (e.g., "groups", "roles", "cognito:groups").
func claimsToIdentity(claims jwt.MapClaims, token string) (*Identity, error) {
	// Validate required 'sub' claim per OIDC Core 1.0 spec
	sub, ok := claims["sub"].(string)
	if !ok || sub == "" {
		return nil, errors.New("missing or invalid 'sub' claim (required by OIDC Core 1.0 § 5.1)")
	}

	// Filter internal claims that should not be externalized (e.g., in
	// webhook payloads or audit logs). The tsid is a session identifier
	// used to look up upstream tokens in storage; exposing it widens the
	// attack surface if a webhook receiver is compromised.
	filteredClaims := filterInternalClaims(claims)

	identity := &Identity{
		PrincipalInfo: PrincipalInfo{
			Subject:        sub,
			PlatformUserID: sub,
			Claims:         filteredClaims,
		},
		Token:     token,
		TokenType: "Bearer",
	}

	// Extract optional standard claims
	if name, ok := claims["name"].(string); ok {
		identity.Name = name
	}
	if email, ok := claims["email"].(string); ok {
		identity.Email = email
	}

	return identity, nil
}

// internalClaims are JWT claim keys used internally by the auth server
// that must not be externalized in webhook payloads, audit logs, etc.
// "tsid" is the token session ID used to look up upstream tokens in storage.
var internalClaims = []string{"tsid"}

// filterInternalClaims returns a copy of claims with internal keys removed.
func filterInternalClaims(claims jwt.MapClaims) jwt.MapClaims {
	filtered := make(jwt.MapClaims, len(claims))
	for k, v := range claims {
		filtered[k] = v
	}
	for _, key := range internalClaims {
		delete(filtered, key)
	}
	return filtered
}

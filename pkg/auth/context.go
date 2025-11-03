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

// ClaimsContextKey is the legacy key used to store JWT claims in the context.
// This is maintained for backward compatibility but deprecated in favor of IdentityContextKey.
// New code should use WithIdentity/IdentityFromContext instead.
type ClaimsContextKey struct{}

// WithIdentity stores an Identity in the context.
// If identity is nil, the original context is returned unchanged.
//
// This function is typically called by authentication middleware after successful
// authentication to make the identity available to downstream handlers.
//
// Example:
//
//	identity := &Identity{Subject: "user123", Name: "Alice"}
//	ctx = WithIdentity(ctx, identity)
func WithIdentity(ctx context.Context, identity *Identity) context.Context {
	if identity == nil {
		return ctx
	}
	return context.WithValue(ctx, IdentityContextKey{}, identity)
}

// IdentityFromContext retrieves an Identity from the context.
// Returns the identity and true if present, nil and false otherwise.
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
	return identity, ok
}

// GetClaimsFromContext retrieves the claims from Identity in the request context.
// This is a helper function for backward compatibility with code that expects MapClaims.
// New code should use IdentityFromContext and access the Claims field directly.
func GetClaimsFromContext(ctx context.Context) (jwt.MapClaims, bool) {
	if ctx == nil {
		return nil, false
	}

	// Get Identity and return its Claims
	if identity, ok := IdentityFromContext(ctx); ok && identity != nil {
		if identity.Claims != nil {
			return jwt.MapClaims(identity.Claims), true
		}
	}

	return nil, false
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

	identity := &Identity{
		Subject:   sub,
		Claims:    claims,
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

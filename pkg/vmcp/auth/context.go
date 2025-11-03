package auth

import "context"

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

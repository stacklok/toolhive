package token

import (
	"context"

	"github.com/golang-jwt/jwt/v5"
)

// ClaimsContextKey is the key used to store claims in the request context.
type ClaimsContextKey struct{}

// GetClaimsFromContext retrieves the claims from the request context.
// This is a helper function that can be used by authorization policies
// to access the claims regardless of which middleware was used (JWT, anonymous, or local).
//
// Returns the claims and a boolean indicating whether claims were found.
func GetClaimsFromContext(ctx context.Context) (jwt.MapClaims, bool) {
	if ctx == nil {
		return nil, false
	}
	claims, ok := ctx.Value(ClaimsContextKey{}).(jwt.MapClaims)
	return claims, ok
}

package auth

import (
	"errors"
	"net/http"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/logger"
)

// IdentityMiddleware creates middleware that converts Claims → Identity.
// This middleware must run AFTER auth.TokenValidator.Middleware() which stores Claims in context.
//
// Flow:
//  1. Extracts Claims from context (stored by TokenValidator)
//  2. Extracts original Bearer token from Authorization header
//  3. Converts Claims → Identity using claimsToIdentity
//  4. Stores Identity in context for downstream handlers
//
// If no claims are present in context, the request passes through unchanged.
// This allows unauthenticated endpoints (like /health) to work without auth.
func IdentityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Get claims that were stored by TokenValidator.Middleware
		claims, ok := auth.GetClaimsFromContext(r.Context())
		if !ok {
			// No claims = unauthenticated request or auth middleware didn't run
			// Let it through - handler will decide if auth is required
			next.ServeHTTP(w, r)
			return
		}

		// Extract original token for passthrough scenarios
		token, err := auth.ExtractBearerToken(r)
		if err != nil {
			logger.Warnf("Claims present but token extraction failed: %v", err)
			token = ""
		}

		// Convert Claims → Identity
		identity, err := claimsToIdentity(claims, token)
		if err != nil {
			logger.Errorf("Failed to convert claims to identity: %v", err)
			http.Error(w, "Invalid authentication claims", http.StatusUnauthorized)
			return
		}

		// Store Identity in context
		ctx := WithIdentity(r.Context(), identity)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
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

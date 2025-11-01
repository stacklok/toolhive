package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/toolhive/pkg/auth"
)

const (
	// bearerTokenType is the token type used for Bearer authentication
	bearerTokenType = "Bearer"
)

// OIDCIncomingAuthenticator is a minimal adapter that wraps ToolHive's existing
// TokenValidator to implement the IncomingAuthenticator interface.
//
// This adapter provides a clean separation between OIDC token validation
// (handled by TokenValidator) and the virtual MCP authentication interface.
type OIDCIncomingAuthenticator struct {
	validator TokenAuthenticator
}

// NewOIDCIncomingAuthenticator creates a new OIDC incoming authenticator.
// Returns an error if the validator is nil.
func NewOIDCIncomingAuthenticator(validator TokenAuthenticator) (*OIDCIncomingAuthenticator, error) {
	if validator == nil {
		return nil, errors.New("token validator cannot be nil")
	}

	return &OIDCIncomingAuthenticator{
		validator: validator,
	}, nil
}

// Authenticate validates the incoming HTTP request and extracts identity information.
// It extracts the Bearer token from the Authorization header and validates it using
// the underlying TokenValidator.
func (o *OIDCIncomingAuthenticator) Authenticate(ctx context.Context, r *http.Request) (*Identity, error) {
	// Extract the bearer token from the Authorization header
	tokenString, err := auth.ExtractBearerToken(r)
	if err != nil {
		return nil, err
	}

	// Validate the token using the underlying TokenValidator
	claims, err := o.validator.ValidateToken(ctx, tokenString)
	if err != nil {
		return nil, fmt.Errorf("token validation failed: %w", err)
	}

	// Convert claims to Identity
	identity, err := claimsToIdentity(claims)
	if err != nil {
		return nil, fmt.Errorf("invalid token claims: %w", err)
	}
	identity.Token = tokenString
	identity.TokenType = bearerTokenType

	return identity, nil
}

// Middleware returns an HTTP middleware that validates tokens and stores the
// Identity in the request context. It wraps the TokenValidator's middleware
// and converts the claims to an Identity.
func (o *OIDCIncomingAuthenticator) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		// Create a handler that processes claims and calls next
		claimsHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Extract claims from context (set by TokenValidator middleware)
			claims, ok := r.Context().Value(auth.ClaimsContextKey{}).(jwt.MapClaims)
			if !ok {
				// This should never happen if TokenValidator middleware worked correctly
				http.Error(w, "internal error: claims not found in context", http.StatusInternalServerError)
				return
			}

			// Convert claims to Identity
			identity, err := claimsToIdentity(claims)
			if err != nil {
				http.Error(w, fmt.Sprintf("invalid token claims: %v", err), http.StatusUnauthorized)
				return
			}

			// Extract the token from Authorization header for storage
			tokenString, err := auth.ExtractBearerToken(r)
			if err == nil {
				identity.Token = tokenString
				identity.TokenType = bearerTokenType
			}

			// Store Identity in context
			ctx := WithIdentity(r.Context(), identity)

			// Continue with the next handler
			next.ServeHTTP(w, r.WithContext(ctx))
		})

		// Wrap with TokenValidator's middleware
		return o.validator.Middleware(claimsHandler)
	}
}

// claimsToIdentity converts JWT claims to an Identity structure.
//
// Groups are intentionally NOT extracted here because:
// - OIDC providers use different claim names ("groups", "roles", etc.)
// - Group extraction is an authorization concern, not authentication
// - Authorization policies access groups via the Claims map
//
// Returns an error if the required 'sub' claim is missing or invalid.
func claimsToIdentity(claims jwt.MapClaims) (*Identity, error) {
	identity := &Identity{
		Claims: make(map[string]any),
		Groups: []string{}, // Empty slice, groups stay in Claims if present
	}

	// Store all claims for later use
	for k, v := range claims {
		identity.Claims[k] = v
	}

	// REQUIRED: Extract subject (sub claim is mandatory per OIDC Core 1.0 section 5.1)
	sub, ok := claims["sub"].(string)
	if !ok || sub == "" {
		return nil, errors.New("missing or invalid 'sub' claim (required by OpenID Connect Core 1.0)")
	}
	identity.Subject = sub

	if name, ok := claims["name"].(string); ok {
		identity.Name = name
	}

	if email, ok := claims["email"].(string); ok {
		identity.Email = email
	}

	return identity, nil
}

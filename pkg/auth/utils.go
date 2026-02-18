// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package auth provides authentication and authorization utilities.
package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os/user"
	"strings"
)

// bearerTokenType defines the expected token type for Bearer authentication.
const bearerTokenType = "Bearer"

// Common Bearer token extraction errors
var (
	ErrAuthHeaderMissing       = errors.New("authorization header required")
	ErrInvalidAuthHeaderFormat = errors.New("invalid authorization header format, expected 'Bearer <token>'")
	ErrEmptyBearerToken        = errors.New("empty token in authorization header")
)

// ExtractBearerToken extracts and validates a Bearer token from the Authorization header.
// It performs the following validations:
//  1. Verifies the Authorization header is present
//  2. Checks for the "Bearer " prefix (case-sensitive per RFC 6750)
//  3. Ensures the token is not empty after removing the prefix
//
// The function returns the token string (without "Bearer " prefix) and any validation error.
// Callers are responsible for further token validation (JWT parsing, introspection, etc.)
// and for converting errors to appropriate HTTP responses.
//
// This function implements RFC 6750 Section 2.1 (Bearer Token Authorization Header).
// See: https://datatracker.ietf.org/doc/html/rfc6750#section-2.1
func ExtractBearerToken(r *http.Request) (string, error) {
	// Get the Authorization header
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return "", ErrAuthHeaderMissing
	}

	// Check for the Bearer prefix (case-sensitive per RFC 6750)
	bearerPrefix := bearerTokenType + " "
	if !strings.HasPrefix(authHeader, bearerPrefix) {
		return "", ErrInvalidAuthHeaderFormat
	}

	// Extract the token
	tokenString := strings.TrimPrefix(authHeader, bearerPrefix)

	// Check for empty token (handles "Bearer " with no token or only whitespace)
	if strings.TrimSpace(tokenString) == "" {
		return "", ErrEmptyBearerToken
	}

	return tokenString, nil
}

// GetAuthenticationMiddleware returns the appropriate authentication middleware based on the configuration.
// If OIDC config is provided, it returns JWT middleware. Otherwise, it returns local user middleware.
func GetAuthenticationMiddleware(ctx context.Context, oidcConfig *TokenValidatorConfig,
) (func(http.Handler) http.Handler, http.Handler, error) {
	if oidcConfig != nil {
		slog.Debug("OIDC validation enabled")

		// Create JWT validator
		jwtValidator, err := NewTokenValidator(ctx, *oidcConfig)
		if err != nil {
			return nil, nil, err
		}

		authInfoHandler := NewAuthInfoHandler(oidcConfig.Issuer, jwtValidator.jwksURL, oidcConfig.ResourceURL, oidcConfig.Scopes)
		return jwtValidator.Middleware, authInfoHandler, nil
	}

	slog.Debug("OIDC validation disabled, using local user authentication")

	// Get current OS user
	currentUser, err := user.Current()
	if err != nil {
		slog.Warn("Failed to get current user, using 'local' as default", "error", err)
		return LocalUserMiddleware("local"), nil, nil
	}

	slog.Debug("Using local user authentication", "user", currentUser.Username)
	return LocalUserMiddleware(currentUser.Username), nil, nil
}

// EscapeQuotes escapes quotes in a string for use in a quoted-string context.
func EscapeQuotes(s string) string {
	// Simple escape of backslashes and quotes is sufficient for quoted-string.
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

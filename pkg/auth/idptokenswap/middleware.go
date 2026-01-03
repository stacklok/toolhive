// Package idptokenswap provides middleware for swapping JWT tokens with stored IDP tokens.
// This middleware extracts the token session ID (tsid) from validated JWTs, looks up
// the corresponding IDP tokens from storage, verifies binding, and replaces the
// Authorization header with the IDP access token.
package idptokenswap

import (
	"fmt"
	"net/http"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// MiddlewareType is the type identifier for this middleware
const MiddlewareType = "idp-token-swap"

// DefaultSessionIDClaimKey is the default JWT claim key for the token session ID
const DefaultSessionIDClaimKey = "tsid"

// Config holds configuration for the IDP token swap middleware
type Config struct {
	// Storage provides access to stored IDP tokens (required)
	Storage storage.IDPTokenStorage

	// SessionIDClaimKey is the JWT claim containing the session ID
	// Defaults to "tsid" if empty
	SessionIDClaimKey string

	// VerifyBinding when true, verifies Subject and ClientID match between JWT and stored tokens
	// Defaults to true
	VerifyBinding *bool
}

// CreateIDPTokenSwapMiddleware creates the middleware function directly.
// This is the recommended approach for programmatic configuration.
func CreateIDPTokenSwapMiddleware(config Config) types.MiddlewareFunction {
	// Apply defaults
	claimKey := config.SessionIDClaimKey
	if claimKey == "" {
		claimKey = DefaultSessionIDClaimKey
	}

	verifyBinding := true
	if config.VerifyBinding != nil {
		verifyBinding = *config.VerifyBinding
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			// 1. Get identity from context (set by auth middleware)
			identity, ok := auth.IdentityFromContext(ctx)
			if !ok {
				// No identity - pass through (might be anonymous endpoint)
				logger.Debug("IDP token swap: no identity in context, passing through")
				next.ServeHTTP(w, r)
				return
			}

			// Debug: Log all JWT claims for troubleshooting
			logger.Debugf("IDP token swap: JWT claims received: sub=%q, azp=%q, client_id=%q, tsid=%q",
				identity.Claims["sub"],
				identity.Claims["azp"],
				identity.Claims["client_id"],
				identity.Claims[claimKey])

			// 2. Extract tsid from JWT claims
			sessionID, ok := identity.Claims[claimKey].(string)
			if !ok || sessionID == "" {
				logger.Warnf("IDP token swap: JWT missing session ID claim '%s'", claimKey)
				http.Error(w, "Missing session identifier", http.StatusUnauthorized)
				return
			}

			logger.Debugf("IDP token swap: extracted session ID %q from claim %q", sessionID, claimKey)

			// 3. Look up IDP tokens from storage
			idpTokens, err := config.Storage.GetIDPTokens(ctx, sessionID)
			if err != nil {
				logger.Warnf("IDP token swap: failed to retrieve IDP tokens for session %s: %v", sessionID, err)
				http.Error(w, "Session not found", http.StatusUnauthorized)
				return
			}

			// Debug: Log stored IDP token binding fields
			logger.Debugf("IDP token swap: stored IDP token binding for session %s: Subject=%q, ClientID=%q",
				sessionID, idpTokens.Subject, idpTokens.ClientID)

			// 4. Verify binding (if enabled)
			if verifyBinding {
				if err := verifyTokenBinding(identity, idpTokens, sessionID); err != nil {
					http.Error(w, "Session binding mismatch", http.StatusForbidden)
					return
				}
			}

			// 5. Check if token is expired
			if idpTokens.IsExpired() {
				logger.Warnf("IDP token swap: token expired for session %s", sessionID)
				http.Error(w, "Token expired", http.StatusUnauthorized)
				return
			}

			// 6. Replace Authorization header with IDP access token
			r.Header.Set("Authorization", fmt.Sprintf("Bearer %s", idpTokens.AccessToken))
			logger.Debugf("IDP token swap: replaced Authorization header for session %s", sessionID)

			next.ServeHTTP(w, r)
		})
	}
}

// verifyTokenBinding verifies that the JWT claims match the stored IDP token bindings.
// Returns an error if there is a mismatch.
func verifyTokenBinding(identity *auth.Identity, idpTokens *storage.IDPTokens, sessionID string) error {
	// Get subject from JWT claims
	jwtSubject, _ := identity.Claims["sub"].(string)

	// Debug: Log subject comparison details
	logger.Debugf("IDP token swap: session %s subject check: jwt=%q stored=%q",
		sessionID, jwtSubject, idpTokens.Subject)

	if idpTokens.Subject != "" && idpTokens.Subject != jwtSubject {
		logger.Warnf("IDP token swap: subject mismatch for session %s: jwt_sub=%q != stored_subject=%q",
			sessionID, jwtSubject, idpTokens.Subject)
		return fmt.Errorf("subject mismatch")
	}

	// Get client_id from JWT claims (could be in "azp" or "client_id")
	jwtClientID, _ := identity.Claims["azp"].(string)
	if jwtClientID == "" {
		jwtClientID, _ = identity.Claims["client_id"].(string)
	}

	// Debug: Log client_id comparison details
	logger.Debugf("IDP token swap: session %s client_id check: jwt=%q (azp=%v) stored=%q",
		sessionID, jwtClientID, identity.Claims["azp"], idpTokens.ClientID)

	if idpTokens.ClientID != "" && idpTokens.ClientID != jwtClientID {
		logger.Warnf("IDP token swap: client_id mismatch for session %s: jwt_client_id=%q != stored_client_id=%q",
			sessionID, jwtClientID, idpTokens.ClientID)
		return fmt.Errorf("client_id mismatch")
	}

	logger.Debugf("IDP token swap: binding verification passed for session %s", sessionID)
	return nil
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"errors"
	"fmt"

	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/auth/tokensource"
	"github.com/stacklok/toolhive/pkg/secrets"
)

// ErrTokenRequired is returned when a fresh token is needed but no cached or
// refreshable token exists and the caller is non-interactive (browser flow
// disabled). The user must first complete an interactive login so that a
// refresh token is persisted for subsequent non-interactive calls.
var ErrTokenRequired = errors.New(
	"LLM gateway authentication required: no cached credentials found; " +
		"complete an interactive login first (\"thv llm setup\" — coming soon)",
)

// TokenRefUpdater is a callback invoked when the refresh token changes — either
// after a successful browser flow (initial login) or when the OIDC provider
// rotates the refresh token during a refresh. It persists the secret key and
// the new token expiry into the application config so future CLI invocations
// can restore the session. It is NOT called on routine access-token refreshes
// where the refresh token is unchanged.
// Callers typically wire this to config.UpdateConfig.
type TokenRefUpdater = tokensource.ConfigPersister

// TokenSource provides fresh LLM gateway access tokens.
type TokenSource = tokensource.OAuthTokenSource

// NewTokenSource creates a TokenSource for the LLM gateway.
// secretsProvider may be nil if the secrets store is unavailable.
// tokenRefUpdater is called after login/refresh to persist the token reference
// into config — pass nil to skip config persistence (useful in tests).
// Set interactive to false for non-interactive callers such as thv llm token.
func NewTokenSource(
	cfg *Config, secretsProvider secrets.Provider, interactive bool, tokenRefUpdater TokenRefUpdater,
) *TokenSource {
	return tokensource.New(tokensource.Options{
		OIDC: tokensource.OIDCParams{
			Issuer:       cfg.OIDC.Issuer,
			ClientID:     cfg.OIDC.ClientID,
			Scopes:       cfg.OIDC.EffectiveScopes(),
			Audience:     cfg.OIDC.Audience,
			CallbackPort: cfg.OIDC.CallbackPort,
		},
		SecretsProvider: secretsProvider,
		Interactive:     interactive,
		KeyProvider: func() string {
			if cfg.OIDC.CachedRefreshTokenRef != "" {
				return cfg.OIDC.CachedRefreshTokenRef
			}
			return DeriveSecretKey(cfg.GatewayURL, cfg.OIDC.Issuer)
		},
		ConfigPersister: tokenRefUpdater,
		FallbackErr:     ErrTokenRequired,
	})
}

// SanitizeTokenError returns a log-safe string for a token-source error.
// If err wraps *oauth2.RetrieveError, only the error code and description are
// included — never the raw response body, which may contain bearer material
// echoed back by the IdP.
func SanitizeTokenError(err error) string {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		if re.ErrorDescription != "" {
			return fmt.Sprintf("oauth2 error %q: %s", re.ErrorCode, re.ErrorDescription)
		}
		return fmt.Sprintf("oauth2 error %q", re.ErrorCode)
	}
	return err.Error()
}

// DeriveSecretKey computes the secrets-provider key for an LLM gateway refresh
// token. The formula is: LLM_OAUTH_<8 hex chars> where the hex is derived from
// sha256(gatewayURL + "\x00" + issuer)[:4].
func DeriveSecretKey(gatewayURL, issuer string) string {
	return tokensource.DeriveSecretKey("LLM_OAUTH_", gatewayURL, issuer)
}

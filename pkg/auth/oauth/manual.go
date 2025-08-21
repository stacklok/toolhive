// Package oauth provides OAuth 2.0 and OIDC authentication functionality.
package oauth

import (
	"fmt"

	"github.com/stacklok/toolhive/pkg/networking"
)

// CreateOAuthConfigManual creates an OAuth config with manually provided endpoints
func CreateOAuthConfigManual(
	clientID, clientSecret string,
	authURL, tokenURL string,
	scopes []string,
	usePKCE bool,
	callbackPort int,
	oauthParams map[string]string,
) (*Config, error) {
	if clientID == "" {
		return nil, fmt.Errorf("client ID is required")
	}
	if authURL == "" {
		return nil, fmt.Errorf("authorization URL is required")
	}
	if tokenURL == "" {
		return nil, fmt.Errorf("token URL is required")
	}

	// Validate URLs
	if err := networking.ValidateEndpointURL(authURL); err != nil {
		return nil, fmt.Errorf("invalid authorization URL: %w", err)
	}
	if err := networking.ValidateEndpointURL(tokenURL); err != nil {
		return nil, fmt.Errorf("invalid token URL: %w", err)
	}

	// Default scopes for regular OAuth (don't assume OIDC scopes)
	if len(scopes) == 0 {
		scopes = []string{}
	}

	return &Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		AuthURL:      authURL,
		TokenURL:     tokenURL,
		Scopes:       scopes,
		UsePKCE:      usePKCE,
		CallbackPort: callbackPort,
		OAuthParams:  oauthParams,
	}, nil
}

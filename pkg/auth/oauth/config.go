// Package oauth provides OAuth 2.0 and OIDC authentication functionality.
package oauth

import (
	"fmt"

	"github.com/stacklok/toolhive/pkg/networking"
)

// Config contains configuration for OAuth authentication
type Config struct {
	// ClientID is the OAuth client ID
	ClientID string

	// ClientSecret is the OAuth client secret (optional for PKCE flow)
	ClientSecret string

	// RedirectURL is the redirect URL for the OAuth flow
	RedirectURL string

	// AuthURL is the authorization endpoint URL
	AuthURL string

	// TokenURL is the token endpoint URL
	TokenURL string

	// Scopes are the OAuth scopes to request
	Scopes []string

	// UsePKCE enables PKCE (Proof Key for Code Exchange) for enhanced security
	UsePKCE bool

	// CallbackPort is the port for the OAuth callback server (optional, 0 means auto-select)
	CallbackPort int

	// IntrospectionEndpoint is the optional introspection endpoint for validating tokens
	IntrospectionEndpoint string

	// OAuthParams are additional parameters to pass to the authorization URL
	OAuthParams map[string]string
}

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

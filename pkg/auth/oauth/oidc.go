// Package oauth provides OAuth 2.0 and OIDC authentication functionality.
package oauth

import (
	"context"

	"github.com/stacklok/toolhive/pkg/auth/oidc"
)

// UserAgent is the user agent for the ToolHive MCP client
const UserAgent = oidc.UserAgent

// OIDCDiscoveryDocument represents the OIDC discovery document structure
// This is a simplified wrapper around the OIDC discovery for backward compatibility
type OIDCDiscoveryDocument = oidc.DiscoveryDocument

// DiscoverOIDCEndpoints discovers OAuth endpoints from an OIDC issuer
// Deprecated: Use oidc.DiscoverEndpoints instead
func DiscoverOIDCEndpoints(ctx context.Context, issuer string) (*OIDCDiscoveryDocument, error) {
	return oidc.DiscoverEndpoints(ctx, issuer)
}

// DiscoverActualIssuer discovers the actual issuer from a URL that might be different from the issuer itself
// Deprecated: Use oidc.DiscoverActualIssuer instead
func DiscoverActualIssuer(ctx context.Context, metadataURL string) (*OIDCDiscoveryDocument, error) {
	return oidc.DiscoverActualIssuer(ctx, metadataURL)
}

// CreateOAuthConfigFromOIDC creates an OAuth config from OIDC discovery
func CreateOAuthConfigFromOIDC(
	ctx context.Context,
	issuer, clientID, clientSecret string,
	scopes []string,
	usePKCE bool,
	callbackPort int,
) (*Config, error) {
	// Discover OIDC endpoints
	doc, err := oidc.DiscoverEndpoints(ctx, issuer)
	if err != nil {
		return nil, err
	}

	// Default scopes for OIDC if none provided
	if len(scopes) == 0 {
		scopes = []string{"openid", "profile", "email"}
	}

	// Enable PKCE by default if supported
	if !usePKCE && len(doc.CodeChallengeMethodsSupported) > 0 {
		for _, method := range doc.CodeChallengeMethodsSupported {
			if method == "S256" {
				usePKCE = true
				break
			}
		}
	}

	return &Config{
		ClientID:              clientID,
		ClientSecret:          clientSecret,
		AuthURL:               doc.AuthorizationEndpoint,
		IntrospectionEndpoint: doc.IntrospectionEndpoint,
		TokenURL:              doc.TokenEndpoint,
		Scopes:                scopes,
		UsePKCE:               usePKCE,
		CallbackPort:          callbackPort,
	}, nil
}

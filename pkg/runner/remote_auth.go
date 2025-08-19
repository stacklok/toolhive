package runner

import (
	"context"
	"fmt"

	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/auth/discovery"
	"github.com/stacklok/toolhive/pkg/logger"
)

// RemoteAuthHandler handles authentication for remote MCP servers.
// Supports OAuth/OIDC-based authentication with automatic discovery.
type RemoteAuthHandler struct {
	config *RemoteAuthConfig
}

// NewRemoteAuthHandler creates a new remote authentication handler
func NewRemoteAuthHandler(config *RemoteAuthConfig) *RemoteAuthHandler {
	return &RemoteAuthHandler{
		config: config,
	}
}

// Authenticate is the main entry point for remote MCP server authentication
func (h *RemoteAuthHandler) Authenticate(ctx context.Context, remoteURL string) (*oauth2.TokenSource, error) {

	// First, try to detect if authentication is required
	authInfo, err := discovery.DetectAuthenticationFromServer(ctx, remoteURL, nil)
	if err != nil {
		logger.Debugf("Could not detect authentication from server: %v", err)
		return nil, nil // Not an error, just no auth detected
	}

	if authInfo != nil {
		logger.Infof("Detected authentication requirement from server - type: %s, realm: %s, resource_metadata: %s",
			authInfo.Type, authInfo.Realm, authInfo.ResourceMetadata)

		// Handle OAuth authentication
		if authInfo.Type == "OAuth" {
			// Use realm as issuer if available, otherwise derive from URL
			issuer := authInfo.Realm
			if issuer == "" {
				issuer = discovery.DeriveIssuerFromURL(remoteURL)
			}

			if issuer == "" {
				return nil, fmt.Errorf("could not determine OAuth issuer from realm or URL")
			}

			logger.Infof("Starting OAuth authentication flow with issuer: %s", issuer)

			// Create OAuth flow config from RemoteAuthConfig
			flowConfig := &discovery.OAuthFlowConfig{
				ClientID:     h.config.ClientID,
				ClientSecret: h.config.ClientSecret,
				AuthorizeURL: h.config.AuthorizeURL,
				TokenURL:     h.config.TokenURL,
				Scopes:       h.config.Scopes,
				CallbackPort: h.config.CallbackPort,
				Timeout:      h.config.Timeout,
				SkipBrowser:  h.config.SkipBrowser,
				OAuthParams:  h.config.OAuthParams,
			}

			result, err := discovery.PerformOAuthFlow(ctx, issuer, flowConfig)
			if err != nil {
				return nil, err
			}

			return result.TokenSource, nil
		}

		// Currently only OAuth-based authentication is supported
		logger.Infof("Unsupported authentication type: %s", authInfo.Type)
		return nil, nil
	}

	return nil, nil // No authentication required
}

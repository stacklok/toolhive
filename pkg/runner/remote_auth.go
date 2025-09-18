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
			// Discover the issuer and potentially update scopes
			issuer, scopes, authServerInfo, err := h.discoverIssuerAndScopes(ctx, authInfo, remoteURL)
			if err != nil {
				return nil, err
			}

			logger.Infof("Starting OAuth authentication flow with issuer: %s", issuer)

			// Create OAuth flow config from RemoteAuthConfig
			flowConfig := &discovery.OAuthFlowConfig{
				ClientID:     h.config.ClientID,
				ClientSecret: h.config.ClientSecret,
				AuthorizeURL: h.config.AuthorizeURL,
				TokenURL:     h.config.TokenURL,
				Scopes:       scopes,
				CallbackPort: h.config.CallbackPort,
				Timeout:      h.config.Timeout,
				SkipBrowser:  h.config.SkipBrowser,
				OAuthParams:  h.config.OAuthParams,
			}

			// If we have discovered endpoints from the authorization server metadata,
			// use them instead of trying to discover them again
			if authServerInfo != nil && h.config.AuthorizeURL == "" && h.config.TokenURL == "" {
				flowConfig.AuthorizeURL = authServerInfo.AuthorizationURL
				flowConfig.TokenURL = authServerInfo.TokenURL
				flowConfig.RegistrationEndpoint = authServerInfo.RegistrationEndpoint
				// Mark that we're in resource metadata discovery context
				// This allows issuer mismatch which is legitimate for resource metadata
				flowConfig.IsResourceMetadataDiscovery = true
				logger.Infof("Using discovered OAuth endpoints - authorize: %s, token: %s, registration: %s",
					authServerInfo.AuthorizationURL, authServerInfo.TokenURL, authServerInfo.RegistrationEndpoint)
			} else if h.config.Issuer == "" && h.config.AuthorizeURL == "" && h.config.TokenURL == "" {
				// If we derived the issuer from the remote URL (not explicitly configured),
				// we're in a resource metadata discovery context where issuer mismatch is allowed
				// This handles cases like Atlassian where the metadata URL differs from the issuer
				flowConfig.IsResourceMetadataDiscovery = true
				logger.Debugf("Using derived issuer from remote URL - allowing issuer mismatch per RFC 8414")
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

// discoverIssuerAndScopes attempts to discover the OAuth issuer and scopes from various sources
// following RFC 8414 and RFC 9728 standards
// If the issuer is not derived from Realm and Resource Metadata, it derives from the remote URL
func (h *RemoteAuthHandler) discoverIssuerAndScopes(
	ctx context.Context,
	authInfo *discovery.AuthInfo,
	remoteURL string,
) (string, []string, *discovery.AuthServerInfo, error) {
	// Priority 1: Use configured issuer if available
	if h.config.Issuer != "" {
		logger.Debugf("Using configured issuer: %s", h.config.Issuer)
		return h.config.Issuer, h.config.Scopes, nil, nil
	}

	// Priority 2: Try to derive from realm (RFC 8414)
	if authInfo.Realm != "" {
		derivedIssuer := discovery.DeriveIssuerFromRealm(authInfo.Realm)
		if derivedIssuer != "" {
			logger.Infof("Derived issuer from realm: %s", derivedIssuer)
			return derivedIssuer, h.config.Scopes, nil, nil
		}
	}

	// Priority 3: Fetch from resource metadata (RFC 9728)
	if authInfo.ResourceMetadata != "" {
		return h.tryDiscoverFromResourceMetadata(ctx, authInfo.ResourceMetadata)
	}

	issuer := discovery.DeriveIssuerFromURL(remoteURL)
	if issuer != "" {
		return issuer, h.config.Scopes, nil, nil
	}

	// No issuer could be determined
	return "", nil, nil, fmt.Errorf("could not determine OAuth issuer. Please provide issuer in configuration, " +
		"or ensure the server provides a valid realm parameter or resource_metadata URL in the WWW-Authenticate header")
}

// tryDiscoverFromResourceMetadata attempts to discover issuer and scopes from resource metadata
func (h *RemoteAuthHandler) tryDiscoverFromResourceMetadata(
	ctx context.Context,
	resourceMetadataURL string,
) (string, []string, *discovery.AuthServerInfo, error) {
	logger.Infof("Fetching resource metadata from: %s", resourceMetadataURL)

	metadata, err := discovery.FetchResourceMetadata(ctx, resourceMetadataURL)
	if err != nil {
		logger.Debugf("Failed to fetch resource metadata: %v", err)
		return "", nil, nil, fmt.Errorf("could not determine OAuth issuer")
	}

	if metadata == nil {
		return "", nil, nil, fmt.Errorf("could not determine OAuth issuer")
	}

	// Try to find a valid authorization server from the list
	authServerInfo, issuer := h.findValidAuthServer(ctx, metadata.AuthorizationServers)
	if authServerInfo == nil {
		if len(metadata.AuthorizationServers) > 0 {
			logger.Warnf("Resource metadata contained authorization_servers, " +
				"but none could be validated as actual OAuth authorization servers")
		}
		return "", nil, nil, fmt.Errorf("could not determine OAuth issuer")
	}

	// Determine scopes - use configured or fall back to metadata
	scopes := h.config.Scopes
	if len(scopes) == 0 && len(metadata.ScopesSupported) > 0 {
		scopes = metadata.ScopesSupported
		logger.Infof("Using scopes from resource metadata: %v", scopes)
	}

	return issuer, scopes, authServerInfo, nil
}

// findValidAuthServer validates authorization servers and returns the first valid one
func (*RemoteAuthHandler) findValidAuthServer(
	ctx context.Context,
	authServers []string,
) (*discovery.AuthServerInfo, string) {
	for _, authServer := range authServers {
		logger.Debugf("Validating authorization server: %s", authServer)

		authServerInfo, err := discovery.ValidateAndDiscoverAuthServer(ctx, authServer)
		if err != nil {
			logger.Debugf("Authorization server validation failed for %s: %v", authServer, err)
			continue
		}

		// Found a valid authorization server
		logger.Infof("Using validated authorization server: %s (actual issuer: %s)",
			authServer, authServerInfo.Issuer)
		return authServerInfo, authServerInfo.Issuer
	}

	return nil, ""
}

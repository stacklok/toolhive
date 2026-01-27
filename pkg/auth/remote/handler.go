// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"fmt"

	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/auth/discovery"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/secrets"
)

// Handler handles authentication for remote MCP servers.
// Supports OAuth/OIDC-based authentication with automatic discovery.
type Handler struct {
	config         *Config
	tokenPersister TokenPersister
	secretProvider secrets.Provider
}

// NewHandler creates a new remote authentication handler
func NewHandler(config *Config) *Handler {
	return &Handler{
		config: config,
	}
}

// SetTokenPersister sets a callback function that will be called whenever
// OAuth tokens are refreshed. This enables token persistence across restarts.
func (h *Handler) SetTokenPersister(persister TokenPersister) {
	h.tokenPersister = persister
}

// SetSecretProvider sets the secret provider used to store and retrieve cached tokens.
func (h *Handler) SetSecretProvider(provider secrets.Provider) {
	h.secretProvider = provider
}

// Authenticate is the main entry point for remote MCP server authentication
func (h *Handler) Authenticate(ctx context.Context, remoteURL string) (oauth2.TokenSource, error) {
	// Priority 1: Bearer token authentication (if configured)
	if h.config.BearerToken != "" {
		logger.Debug("Using bearer token authentication")
		return NewBearerTokenSource(h.config.BearerToken), nil
	}

	// Detect authentication requirements once (used by both cached token restore and fresh OAuth)
	authInfo, err := discovery.DetectAuthenticationFromServer(ctx, remoteURL, nil)
	if err != nil {
		logger.Debugf("Could not detect authentication from server: %v", err)
		return nil, nil // Not an error, just no auth detected
	}

	if authInfo == nil {
		return nil, nil // No authentication required
	}

	logger.Debugf("Detected authentication requirement from server - type: %s, realm: %s, resource_metadata: %s",
		authInfo.Type, authInfo.Realm, authInfo.ResourceMetadata)

	// Check if we need to handle Bearer token requirement
	if err := h.validateBearerRequirement(authInfo); err != nil {
		return nil, err
	}

	// Only proceed with OAuth if the auth type supports it
	if authInfo.Type != "OAuth" && authInfo.Type != "Bearer" {
		logger.Errorf("Unsupported authentication type: %s", authInfo.Type)
		return nil, nil
	}

	// Discover OAuth endpoints once (used by both cached token restore and fresh OAuth)
	issuer, scopes, authServerInfo, err := h.discoverIssuerAndScopes(ctx, authInfo, remoteURL)
	if err != nil {
		return nil, err
	}

	// Priority 2: Try to use cached OAuth tokens (if available)
	if h.config.HasValidCachedTokens() {
		tokenSource, err := h.tryRestoreFromCachedTokens(ctx, issuer, scopes, authServerInfo)
		if err != nil {
			logger.Warnf("Failed to restore from cached tokens, will perform fresh OAuth flow: %v", err)
			// Clear invalid cached tokens
			h.config.ClearCachedTokens()
		} else if tokenSource != nil {
			logger.Debugf("Successfully restored OAuth session from cached tokens")
			return tokenSource, nil
		}
	}

	// Priority 3: Fresh OAuth authentication flow
	return h.performOAuthFlow(ctx, issuer, scopes, authServerInfo)
}

// validateBearerRequirement checks if Bearer auth is required without OAuth fallback
func (*Handler) validateBearerRequirement(authInfo *discovery.AuthInfo) error {
	if authInfo.Type != "Bearer" {
		return nil
	}

	// For backward compatibility, fall back to OAuth flow if realm or resource_metadata is present
	// Many servers use Bearer header but support OAuth flow
	if authInfo.Realm != "" || authInfo.ResourceMetadata != "" {
		logger.Warnf("Server returned Bearer header but no bearer token configured. " +
			"Attempting OAuth flow for backward compatibility (realm or resource_metadata present)")
		return nil
	}

	// No realm or resource_metadata - likely requires static bearer token
	return fmt.Errorf("server requires bearer token authentication but no bearer token is configured. "+
		"Please provide a bearer token using --remote-auth-bearer-token flag or %s environment variable", BearerTokenEnvVarName)
}

// performOAuthFlow executes the OAuth authentication flow
func (h *Handler) performOAuthFlow(
	ctx context.Context,
	issuer string,
	scopes []string,
	authServerInfo *discovery.AuthServerInfo,
) (oauth2.TokenSource, error) {
	logger.Debugf("Starting OAuth authentication flow with issuer: %s", issuer)

	// Create OAuth flow config
	flowConfig := h.buildOAuthFlowConfig(scopes, authServerInfo)

	result, err := discovery.PerformOAuthFlow(ctx, issuer, flowConfig)
	if err != nil {
		return nil, err
	}

	// Persist and wrap the token source
	return h.wrapWithPersistence(result), nil
}

// buildOAuthFlowConfig creates the OAuth flow configuration
func (h *Handler) buildOAuthFlowConfig(scopes []string, authServerInfo *discovery.AuthServerInfo) *discovery.OAuthFlowConfig {
	flowConfig := &discovery.OAuthFlowConfig{
		ClientID:     h.config.ClientID,
		ClientSecret: h.config.ClientSecret,
		AuthorizeURL: h.config.AuthorizeURL,
		TokenURL:     h.config.TokenURL,
		Scopes:       scopes,
		CallbackPort: h.config.CallbackPort,
		Timeout:      h.config.Timeout,
		SkipBrowser:  h.config.SkipBrowser,
		Resource:     h.config.Resource,
		OAuthParams:  h.config.OAuthParams,
	}

	// If we have discovered endpoints from the authorization server metadata,
	// use them instead of trying to discover them again
	if authServerInfo != nil && h.config.AuthorizeURL == "" && h.config.TokenURL == "" {
		flowConfig.AuthorizeURL = authServerInfo.AuthorizationURL
		flowConfig.TokenURL = authServerInfo.TokenURL
		flowConfig.RegistrationEndpoint = authServerInfo.RegistrationEndpoint
		logger.Debugf("Using discovered OAuth endpoints - authorize: %s, token: %s, registration: %s",
			authServerInfo.AuthorizationURL, authServerInfo.TokenURL, authServerInfo.RegistrationEndpoint)
	}

	return flowConfig
}

// wrapWithPersistence wraps the OAuth result with token persistence
func (h *Handler) wrapWithPersistence(result *discovery.OAuthFlowResult) oauth2.TokenSource {
	// Persist the refresh token for future restarts
	if h.tokenPersister != nil && result.RefreshToken != "" {
		if err := h.tokenPersister(result.RefreshToken, result.Expiry); err != nil {
			logger.Warnf("Failed to persist OAuth tokens: %v", err)
		} else {
			logger.Debugf("Successfully persisted OAuth tokens for future restarts")
		}
	}

	// Wrap the token source to persist refreshed tokens
	tokenSource := result.TokenSource
	if h.tokenPersister != nil {
		tokenSource = NewPersistingTokenSource(result.TokenSource, h.tokenPersister)
	}

	return tokenSource
}

// tryRestoreFromCachedTokens attempts to create a TokenSource from cached tokens
func (h *Handler) tryRestoreFromCachedTokens(
	ctx context.Context,
	issuer string,
	scopes []string,
	authServerInfo *discovery.AuthServerInfo,
) (oauth2.TokenSource, error) {
	// Resolve the refresh token from the secret manager
	if h.secretProvider == nil {
		return nil, fmt.Errorf("secret provider not configured, cannot restore cached tokens")
	}

	refreshToken, err := h.secretProvider.GetSecret(ctx, h.config.CachedRefreshTokenRef)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve cached refresh token: %w", err)
	}

	// Build OAuth2 config for token refresh
	oauth2Config := &oauth2.Config{
		ClientID:     h.config.ClientID,
		ClientSecret: h.config.ClientSecret,
		Scopes:       scopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:  h.config.AuthorizeURL,
			TokenURL: h.config.TokenURL,
		},
	}

	// Use discovered endpoints if available
	if authServerInfo != nil {
		if h.config.AuthorizeURL == "" {
			oauth2Config.Endpoint.AuthURL = authServerInfo.AuthorizationURL
		}
		if h.config.TokenURL == "" {
			oauth2Config.Endpoint.TokenURL = authServerInfo.TokenURL
		}
	}

	// Create token source from cached refresh token
	baseSource := CreateTokenSourceFromCached(
		oauth2Config,
		refreshToken,
		h.config.CachedTokenExpiry,
	)

	// Try to get a token to verify the cached tokens are valid
	// This will trigger a refresh since we don't have an access token
	_, err = baseSource.Token()
	if err != nil {
		return nil, fmt.Errorf("cached tokens are invalid or expired: %w", err)
	}

	logger.Debugf("Restored OAuth session from cached tokens (issuer: %s)", issuer)

	// Wrap with persisting token source to save refreshed tokens
	if h.tokenPersister != nil {
		return NewPersistingTokenSource(baseSource, h.tokenPersister), nil
	}

	return baseSource, nil
}

// discoverIssuerAndScopes attempts to discover the OAuth issuer and scopes from various sources
// following RFC 8414 and RFC 9728 standards
// If the issuer is not derived from Realm and Resource Metadata, it derives from the remote URL
func (h *Handler) discoverIssuerAndScopes(
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
			logger.Debugf("Derived issuer from realm: %s", derivedIssuer)
			return derivedIssuer, h.config.Scopes, nil, nil
		}
	}

	// Priority 3: Fetch from resource metadata (RFC 9728)
	if authInfo.ResourceMetadata != "" {
		return h.tryDiscoverFromResourceMetadata(ctx, authInfo.ResourceMetadata)
	}

	// Priority 4: Try to discover actual issuer from the server's well-known endpoint
	// This handles cases where the issuer differs from the server URL (e.g., Atlassian)
	issuer, scopes, authServerInfo, err := h.tryDiscoverFromWellKnown(ctx, remoteURL)
	if err == nil {
		return issuer, scopes, authServerInfo, nil
	}
	logger.Debugf("Could not discover from well-known endpoint: %v", err)

	// Priority 5: Last resort - derive issuer from URL without discovery
	derivedIssuer := discovery.DeriveIssuerFromURL(remoteURL)
	if derivedIssuer != "" {
		logger.Debugf("Using derived issuer from URL: %s", derivedIssuer)
		return derivedIssuer, h.config.Scopes, nil, nil
	}

	// No issuer could be determined
	return "", nil, nil, fmt.Errorf("could not determine OAuth issuer. Please provide issuer in configuration, " +
		"or ensure the server provides a valid realm parameter or resource_metadata URL in the WWW-Authenticate header")
}

// tryDiscoverFromResourceMetadata attempts to discover issuer and scopes from resource metadata
func (h *Handler) tryDiscoverFromResourceMetadata(
	ctx context.Context,
	resourceMetadataURL string,
) (string, []string, *discovery.AuthServerInfo, error) {
	logger.Debugf("Fetching resource metadata from: %s", resourceMetadataURL)

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
		logger.Debugf("Using scopes from resource metadata: %v", scopes)
	}

	return issuer, scopes, authServerInfo, nil
}

// findValidAuthServer validates authorization servers and returns the first valid one
func (*Handler) findValidAuthServer(
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
		logger.Debugf("Using validated authorization server: %s (actual issuer: %s)",
			authServer, authServerInfo.Issuer)
		return authServerInfo, authServerInfo.Issuer
	}

	return nil, ""
}

// tryDiscoverFromWellKnown attempts to discover the actual OAuth issuer
// by probing the server's well-known endpoints without validating issuer match
// This is useful when the issuer differs from the server URL (e.g., Atlassian case)
func (h *Handler) tryDiscoverFromWellKnown(
	ctx context.Context,
	remoteURL string,
) (string, []string, *discovery.AuthServerInfo, error) {
	// First try to derive a base URL from the remote URL
	derivedURL := discovery.DeriveIssuerFromURL(remoteURL)
	if derivedURL == "" {
		return "", nil, nil, fmt.Errorf("could not derive base URL from %s", remoteURL)
	}

	// Try to discover the actual issuer without validation
	// This uses DiscoverActualIssuer which doesn't validate issuer match
	authServerInfo, err := discovery.ValidateAndDiscoverAuthServer(ctx, derivedURL)
	if err != nil {
		return "", nil, nil, fmt.Errorf("well-known discovery failed: %w", err)
	}

	// Successfully discovered the actual issuer
	if authServerInfo.Issuer != derivedURL {
		logger.Debugf("Discovered actual issuer: %s (differs from server URL: %s)",
			authServerInfo.Issuer, derivedURL)
	}

	// Determine scopes - use configured or fall back to defaults
	scopes := h.config.Scopes
	if len(scopes) == 0 {
		// Use some reasonable defaults if no scopes configured
		scopes = []string{"openid", "profile"}
		logger.Debugf("No scopes configured, using defaults: %v", scopes)
	}

	return authServerInfo.Issuer, scopes, authServerInfo, nil
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstream

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/networking"
	oauthproto "github.com/stacklok/toolhive/pkg/oauth"
)

const (
	// ProviderTypeOIDC is for OIDC providers that support discovery.
	ProviderTypeOIDC ProviderType = "oidc"
)

// OIDCConfig contains configuration for OIDC providers that support discovery.
type OIDCConfig struct {
	CommonOAuthConfig

	// Issuer is the URL of the upstream OIDC provider (e.g., https://accounts.google.com).
	// The provider will fetch endpoints from {Issuer}/.well-known/openid-configuration.
	Issuer string
}

// Validate checks that OIDCConfig has all required fields and valid values.
func (c *OIDCConfig) Validate() error {
	if c.Issuer == "" {
		return errors.New("issuer is required for OIDC providers")
	}
	if err := networking.ValidateEndpointURL(c.Issuer); err != nil {
		return fmt.Errorf("invalid issuer URL: %w", err)
	}
	return c.CommonOAuthConfig.Validate()
}

// ErrNonceMismatch is returned when the nonce claim in the ID token does not match
// the expected nonce from the authorization request.
var ErrNonceMismatch = errors.New("ID token nonce does not match expected value")

// OIDCProviderImpl implements OAuth2Provider for OIDC-compliant identity providers.
// It embeds BaseOAuth2Provider to share common OAuth 2.0 logic while adding
// OIDC-specific functionality like discovery and ID token validation.
// The ResolveIdentity method is overridden to validate ID tokens per OIDC spec.
type OIDCProviderImpl struct {
	*BaseOAuth2Provider                                   // Embed for shared OAuth 2.0 logic
	oidcConfig          *OIDCConfig                       // Store original OIDC config (Issuer + common OAuth fields)
	endpoints           *oauthproto.OIDCDiscoveryDocument // Discovered endpoints for security validation
	forceConsentScreen  bool                              // Force consent screen on auth requests
	verifier            *oidc.IDTokenVerifier             // ID token verifier from go-oidc
}

// OIDCProviderOption configures an OIDCProvider.
type OIDCProviderOption func(*OIDCProviderImpl)

// WithHTTPClient sets a custom HTTP client for the provider.
func WithHTTPClient(client *http.Client) OIDCProviderOption {
	return func(p *OIDCProviderImpl) {
		p.httpClient = client
	}
}

// WithForceConsentScreen configures the provider to always request the consent screen
// from the identity provider. When enabled, the "prompt=consent" parameter is added
// to authorization requests, forcing the user to re-consent even if they have
// previously authorized the application.
//
// This is useful for:
//   - Testing OAuth flows to verify consent screen behavior
//   - Obtaining a new refresh token when the original has been lost or revoked
//   - Ensuring the user explicitly confirms permissions after scope changes
//   - Applications that require explicit user consent on every authentication
func WithForceConsentScreen(force bool) OIDCProviderOption {
	return func(p *OIDCProviderImpl) {
		p.forceConsentScreen = force
	}
}

// NewOIDCProvider creates a new OIDC provider.
// It performs OIDC discovery to fetch endpoints and then constructs the
// underlying OAuth2 configuration from the discovered endpoints.
func NewOIDCProvider(
	ctx context.Context,
	config *OIDCConfig,
	opts ...OIDCProviderOption,
) (*OIDCProviderImpl, error) {
	if config == nil {
		return nil, errors.New("config is required")
	}

	// Validate OIDC config
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	logger.Debugw("creating OIDC provider",
		"issuer", config.Issuer,
		"client_id", config.ClientID,
	)

	// Create HTTP client for the issuer host
	issuerURL, _ := url.Parse(config.Issuer) // Error already checked in config.Validate()
	httpClient, err := newHTTPClientForHost(issuerURL.Host)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP client: %w", err)
	}

	p := &OIDCProviderImpl{
		oidcConfig: config,
		BaseOAuth2Provider: &BaseOAuth2Provider{
			httpClient: httpClient,
			// config will be set after discovery
		},
	}

	// Apply OIDC-specific options
	for _, opt := range opts {
		opt(p)
	}

	// Use go-oidc for discovery - inject custom HTTP client via context
	ctx = oidc.ClientContext(ctx, p.httpClient)
	oidcProvider, err := oidc.NewProvider(ctx, config.Issuer)
	if err != nil {
		return nil, fmt.Errorf("failed to discover OIDC endpoints: %w", err)
	}

	// Extract endpoints from provider claims for security validation.
	// go-oidc validates issuer but doesn't check endpoint origins.
	endpoints := &oauthproto.OIDCDiscoveryDocument{}
	if err := oidcProvider.Claims(endpoints); err != nil {
		return nil, fmt.Errorf("failed to extract provider claims: %w", err)
	}

	// Security validation - go-oidc doesn't check endpoint origins
	if err := validateDiscoveryDocument(endpoints, config.Issuer); err != nil {
		return nil, fmt.Errorf("invalid discovery document: %w", err)
	}

	p.endpoints = endpoints

	// Determine scopes: use configured or OIDC defaults
	scopes := config.Scopes
	if len(scopes) == 0 {
		scopes = []string{"openid", "profile", "email"}
	}

	// Now create OAuth2Config from discovered endpoints + OIDC config.
	// This allows the embedded BaseOAuth2Provider to use the discovered endpoints
	// for token requests while preserving the original OIDC config.
	// Note: UserInfoEndpoint is stored in p.endpoints, not in OAuth2Config.
	oauth2Config := &OAuth2Config{
		CommonOAuthConfig: CommonOAuthConfig{
			ClientID:     config.ClientID,
			ClientSecret: config.ClientSecret,
			Scopes:       scopes,
			RedirectURI:  config.RedirectURI,
		},
		AuthorizationEndpoint: p.endpoints.AuthorizationEndpoint,
		TokenEndpoint:         p.endpoints.TokenEndpoint,
	}
	p.config = oauth2Config

	// Create the oauth2.Config for use with golang.org/x/oauth2 library
	// Use go-oidc's endpoint which handles discovery, but explicitly set AuthStyle
	// to ensure client credentials are sent in the request body (not Basic auth header)
	// for consistent behavior across different IDP implementations.
	providerEndpoint := oidcProvider.Endpoint()
	p.oauth2Config = &oauth2.Config{
		ClientID:     config.ClientID,
		ClientSecret: config.ClientSecret,
		RedirectURL:  config.RedirectURI,
		Scopes:       scopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:   providerEndpoint.AuthURL,
			TokenURL:  providerEndpoint.TokenURL,
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}

	// Use go-oidc's built-in verifier for ID token validation
	p.verifier = oidcProvider.Verifier(&oidc.Config{
		ClientID: config.ClientID,
	})

	logger.Debugw("OIDC provider created successfully",
		"issuer", p.endpoints.Issuer,
		"pkce_supported", p.supportsPKCE(),
		"id_token_validation_enabled", p.verifier != nil,
	)

	return p, nil
}

// Type returns the provider type.
func (*OIDCProviderImpl) Type() ProviderType {
	return ProviderTypeOIDC
}

// ResolveIdentity resolves user identity from tokens by validating the ID token.
// Per OIDC Core Section 3.1.3.3, ID token MUST be present in successful token
// responses when the openid scope was requested. Returns an error if no ID token
// is present - use BaseOAuth2Provider for pure OAuth 2.0 flows without OIDC.
func (p *OIDCProviderImpl) ResolveIdentity(ctx context.Context, tokens *Tokens, nonce string) (string, error) {
	if tokens == nil {
		return "", ErrIdentityResolutionFailed
	}

	if tokens.IDToken == "" {
		return "", fmt.Errorf("%w: ID token required for OIDC provider", ErrIdentityResolutionFailed)
	}

	claims, err := p.validateIDToken(ctx, tokens.IDToken, nonce)
	if err != nil {
		return "", fmt.Errorf("%w: ID token validation failed: %v", ErrIdentityResolutionFailed, err)
	}
	return claims.Subject, nil
}

// validateIDToken validates an ID token and returns the parsed token.
// TODO: Implement full validation using p.verifier in a follow-up PR.
func (*OIDCProviderImpl) validateIDToken(_ context.Context, _, _ string) (*oidc.IDToken, error) {
	// Stub - full implementation in follow-up PR
	return nil, errors.New("ID token validation not yet implemented")
}

// supportsPKCE checks if the provider advertises S256 PKCE support.
func (p *OIDCProviderImpl) supportsPKCE() bool {
	if p.endpoints == nil {
		return false
	}
	return p.endpoints.SupportsPKCE()
}

// validateDiscoveryDocument validates the OIDC discovery document.
//
// It first delegates to OIDCDiscoveryDocument.Validate() for spec-compliant field
// validation (issuer, endpoints, jwks_uri, response_types_supported), then adds
// security validation for endpoint origins.
//
// Note: Issuer match validation (exact match per OIDC spec) is performed by go-oidc's
// NewProvider() before this function is called.
func validateDiscoveryDocument(doc *oauthproto.OIDCDiscoveryDocument, expectedIssuer string) error {
	// Validate required OIDC fields per spec
	if err := doc.Validate(true); err != nil {
		return err
	}

	// Security: validate that discovered endpoints use secure schemes.
	// This prevents a malicious discovery document from redirecting requests to attacker-controlled servers.
	if err := validateEndpointOrigin(doc.AuthorizationEndpoint, expectedIssuer); err != nil {
		return fmt.Errorf("authorization_endpoint origin mismatch: %w", err)
	}

	if err := validateEndpointOrigin(doc.TokenEndpoint, expectedIssuer); err != nil {
		return fmt.Errorf("token_endpoint origin mismatch: %w", err)
	}

	// Optional endpoints - only validate if present
	if doc.UserinfoEndpoint != "" {
		if err := validateEndpointOrigin(doc.UserinfoEndpoint, expectedIssuer); err != nil {
			return fmt.Errorf("userinfo_endpoint origin mismatch: %w", err)
		}
	}

	if doc.JWKSURI != "" {
		if err := validateEndpointOrigin(doc.JWKSURI, expectedIssuer); err != nil {
			return fmt.Errorf("jwks_uri origin mismatch: %w", err)
		}
	}

	return nil
}

// validateEndpointOrigin validates that an endpoint URL uses a secure scheme relative to the issuer.
//
// This function enforces scheme consistency (HTTPS for production, HTTP allowed for localhost testing)
// but does NOT enforce host matching. Major identity providers like Google, Microsoft, and others
// commonly use different hosts/domains for their OAuth endpoints:
//   - Google: issuer=accounts.google.com, token_endpoint=oauth2.googleapis.com
//   - Microsoft: issuer=login.microsoftonline.com, various endpoint hosts
//
// The OIDC Discovery spec (https://openid.net/specs/openid-connect-discovery-1_0.html) and
// RFC 8414 (OAuth Authorization Server Metadata) do not require endpoints to be on the same
// host as the issuer. The security model relies on:
//  1. The discovery document being fetched over HTTPS from the configured issuer
//  2. TLS certificate validation ensuring we're talking to the real issuer
//  3. The issuer being a trusted party that controls its own discovery document
//
// If an attacker could compromise the HTTPS connection to the issuer or the issuer itself,
// host validation would provide no additional protection since the attacker controls the
// discovery document contents.
func validateEndpointOrigin(endpoint, issuer string) error {
	endpointURL, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid endpoint URL: %w", err)
	}

	issuerURL, err := url.Parse(issuer)
	if err != nil {
		return fmt.Errorf("invalid issuer URL: %w", err)
	}

	// For localhost issuers (development/testing), allow HTTP schemes and any localhost endpoint
	if networking.IsLocalhost(issuerURL.Host) {
		// Endpoint must also be localhost when issuer is localhost
		if !networking.IsLocalhost(endpointURL.Host) {
			return fmt.Errorf("host mismatch: issuer is localhost but endpoint host is %q", endpointURL.Host)
		}
		// For localhost, we allow both HTTP and HTTPS, no further validation needed
		return nil
	}

	// For production issuers, enforce HTTPS on endpoints
	// This prevents protocol downgrade attacks where a malicious discovery document
	// could redirect token requests to an HTTP endpoint, exposing credentials
	if endpointURL.Scheme != networking.HttpsScheme {
		return fmt.Errorf(
			"scheme mismatch: issuer uses HTTPS but endpoint uses %q "+
				"(all endpoints must use HTTPS for non-localhost issuers)",
			endpointURL.Scheme)
	}

	// No host validation - the discovery document comes from a trusted HTTPS source
	// and major providers legitimately use different hosts for different endpoints
	return nil
}

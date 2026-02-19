// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstream

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

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

// ErrSubjectMismatch is returned when the sub claim in a refreshed ID token does not
// match the expected subject from the original token response.
// Per OIDC Core Section 12.2, the sub claim MUST be identical.
var ErrSubjectMismatch = errors.New("ID token subject does not match expected value")

// ErrNonceMissing is returned when the ID token does not contain a nonce claim
// but one was expected (because a nonce was sent in the authorization request).
var ErrNonceMissing = errors.New("ID token missing nonce claim when nonce was expected")

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

// WithNonce adds an OIDC nonce parameter to the authorization request.
// The nonce is used to associate a client session with an ID Token and to
// prevent replay attacks. See OIDC Core Section 3.1.2.1.
func WithNonce(nonce string) AuthorizationOption {
	return WithAdditionalParams(map[string]string{"nonce": nonce})
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

	slog.Debug("creating OIDC provider",
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

	// Validate that openid scope is present for OIDC provider.
	// Per OIDC Core, openid scope is mandatory for ID tokens. Without it, the IDP
	// won't return an ID token, but OIDCProviderImpl requires one for identity resolution.
	if !slices.Contains(scopes, "openid") {
		return nil, errors.New("openid scope is required for OIDC provider; use BaseOAuth2Provider for pure OAuth 2.0 flows")
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

	slog.Debug("oidc provider created successfully",
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

// ExchangeCodeForIdentity exchanges an authorization code for tokens and validates
// the ID token (including nonce) in a single atomic operation.
// Per OIDC Core Section 3.1.3.3, the ID token MUST be present. The nonce is validated
// against the ID token to prevent replay attacks (Section 3.1.3.7).
func (p *OIDCProviderImpl) ExchangeCodeForIdentity(
	ctx context.Context, code, codeVerifier, nonce string,
) (*Identity, error) {
	if p.endpoints == nil {
		return nil, errors.New("OIDC endpoints not discovered")
	}

	tokens, err := p.exchangeCodeForTokens(ctx, code, codeVerifier)
	if err != nil {
		return nil, err
	}

	// OIDC-specific: ID token MUST be present per Section 3.1.3.3.
	if tokens.IDToken == "" {
		return nil, fmt.Errorf("%w: ID token required for OIDC provider", ErrIdentityResolutionFailed)
	}

	// Validate ID token with nonce in a single pass — no double-validation.
	validatedToken, err := p.validateIDToken(ctx, tokens.IDToken, nonce)
	if err != nil {
		slog.Debug("id token validation failed", "error", err)
		return nil, fmt.Errorf("%w: %w", ErrIdentityResolutionFailed, err)
	}

	slog.Debug("authorization code exchange successful",
		"has_refresh_token", tokens.RefreshToken != "",
		"has_id_token", tokens.IDToken != "",
		"expires_at", tokens.ExpiresAt.Format(time.RFC3339),
	)

	return &Identity{
		Tokens:  tokens,
		Subject: validatedToken.Subject,
	}, nil
}

// validateIDToken validates an ID token and returns the parsed token.
func (p *OIDCProviderImpl) validateIDToken(ctx context.Context, idToken, nonce string) (*oidc.IDToken, error) {
	if p.verifier == nil {
		return nil, errors.New("ID token verifier not initialized")
	}

	token, err := p.verifier.Verify(ctx, idToken)
	if err != nil {
		return nil, fmt.Errorf("failed to verify ID token: %w", err)
	}

	// Validate nonce if expected (was sent in authorization request).
	// This ensures that when a nonce is provided, the token MUST contain it
	// and it MUST match, preventing replay attacks.
	if nonce != "" {
		if token.Nonce == "" {
			return nil, ErrNonceMissing
		}
		if token.Nonce != nonce {
			return nil, ErrNonceMismatch
		}
	}

	return token, nil
}

// supportsPKCE checks if the provider advertises S256 PKCE support.
func (p *OIDCProviderImpl) supportsPKCE() bool {
	if p.endpoints == nil {
		return false
	}
	return p.endpoints.SupportsPKCE()
}

// AuthorizationURL builds the URL to redirect the user to the upstream IDP.
// This overrides the base implementation to add OIDC-specific parameters (nonce, prompt)
// and use discovered endpoints.
func (p *OIDCProviderImpl) AuthorizationURL(state, codeChallenge string, opts ...AuthorizationOption) (string, error) {
	if p.endpoints == nil {
		return "", errors.New("OIDC endpoints not discovered")
	}

	// Apply authorization options to extract nonce for logging
	authOpts := &authorizationOptions{}
	for _, opt := range opts {
		opt(authOpts)
	}

	// Extract nonce from additionalParams if present
	nonce := ""
	if authOpts.additionalParams != nil {
		nonce = authOpts.additionalParams["nonce"]
	}

	slog.Debug("building authorization URL",
		"authorization_endpoint", p.endpoints.AuthorizationEndpoint,
		"has_pkce", codeChallenge != "",
		"has_nonce", nonce != "",
	)

	// PKCE: Per RFC 7636 Section 5, clients SHOULD send PKCE parameters to all
	// servers regardless of whether they advertise support. Servers that don't
	// support PKCE will simply ignore the parameters.
	if codeChallenge != "" && !p.supportsPKCE() {
		slog.Debug("sending PKCE to provider that does not advertise S256 support (per RFC 7636 Section 5)")
	}

	// Merge caller's opts with OIDC-specific params
	allOpts := append(opts, WithAdditionalParams(p.buildOIDCParams())) //nolint:gocritic // intentionally appending single element

	// Use the base implementation which uses oauth2Config (scopes already configured)
	return p.buildAuthorizationURL(state, codeChallenge, allOpts...)
}

// buildOIDCParams builds the OIDC-specific authorization parameters.
func (p *OIDCProviderImpl) buildOIDCParams() map[string]string {
	params := make(map[string]string)

	// Add prompt=consent if configured to force the consent screen
	if p.forceConsentScreen {
		params["prompt"] = "consent"
	}

	return params
}

// RefreshTokens refreshes the upstream IDP tokens.
// This overrides the base implementation to add OIDC-specific ID token validation.
func (p *OIDCProviderImpl) RefreshTokens(ctx context.Context, refreshToken, expectedSubject string) (*Tokens, error) {
	if p.endpoints == nil {
		return nil, errors.New("OIDC endpoints not discovered")
	}

	slog.Debug("refreshing tokens",
		"token_endpoint", p.endpoints.TokenEndpoint,
	)

	// Use base provider's implementation for token refresh
	tokens, err := p.BaseOAuth2Provider.RefreshTokens(ctx, refreshToken, expectedSubject)
	if err != nil {
		return nil, err
	}

	// OIDC-specific: Validate ID token if present.
	// Per OIDC Core Section 12.2, refresh responses MAY include a new ID token
	// (unlike ExchangeCodeForIdentity where it's required per Section 3.1.3.3).
	// Nonce validation is intentionally omitted: Section 12.2 states that
	// refreshed ID tokens SHOULD NOT contain a nonce claim, and no new
	// authorization request exists to provide an expected nonce value.
	// Full nonce validation occurs in ExchangeCodeForIdentity during the initial auth flow.
	if tokens.IDToken != "" && p.verifier != nil {
		token, err := p.validateIDToken(ctx, tokens.IDToken, "")
		if err != nil {
			return nil, fmt.Errorf("ID token validation failed: %w", err)
		}
		// OIDC Core Section 12.2: sub claim MUST be identical to the original.
		if expectedSubject != "" && token.Subject != expectedSubject {
			return nil, ErrSubjectMismatch
		}
	}

	slog.Debug("token refresh successful",
		"has_new_refresh_token", tokens.RefreshToken != "",
		"expires_at", tokens.ExpiresAt.Format(time.RFC3339),
	)

	return tokens, nil
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

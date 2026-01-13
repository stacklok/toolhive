// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/networking"
)

// OIDCConfig contains configuration for OIDC providers that support discovery.
type OIDCConfig struct {
	CommonOAuthConfig

	// Issuer is the URL of the upstream OIDC provider (e.g., https://accounts.google.com).
	// The provider will fetch endpoints from {Issuer}/.well-known/openid-configuration.
	Issuer string
}

// Validate checks that OIDCConfig has all required fields.
func (c *OIDCConfig) Validate() error {
	if c.Issuer == "" {
		return errors.New("issuer is required for OIDC providers")
	}
	return c.CommonOAuthConfig.Validate()
}

// OIDCEndpoints contains the discovered endpoints for an OIDC provider.
type OIDCEndpoints struct {
	// Issuer is the issuer identifier.
	Issuer string `json:"issuer"`

	// AuthorizationEndpoint is the URL for the authorization endpoint.
	AuthorizationEndpoint string `json:"authorization_endpoint"`

	// TokenEndpoint is the URL for the token endpoint.
	TokenEndpoint string `json:"token_endpoint"`

	// UserInfoEndpoint is the URL for the userinfo endpoint.
	UserInfoEndpoint string `json:"userinfo_endpoint,omitempty"`

	// JWKSEndpoint is the URL for the JWKS endpoint.
	JWKSEndpoint string `json:"jwks_uri,omitempty"`

	// CodeChallengeMethodsSupported lists the supported PKCE code challenge methods.
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported,omitempty"`
}

// Compile-time interface compliance checks.
var (
	_ OAuth2Provider           = (*OIDCProvider)(nil)
	_ UserInfoProvider         = (*OIDCProvider)(nil)
	_ IDTokenValidator         = (*OIDCProvider)(nil)
	_ IDTokenNonceValidator    = (*OIDCProvider)(nil)
	_ UserInfoSubjectValidator = (*OIDCProvider)(nil)
)

// OIDCProvider implements OAuth2Provider for OIDC-compliant identity providers.
// It embeds BaseOAuth2Provider to share common OAuth 2.0 logic while adding
// OIDC-specific functionality like discovery, ID token validation, and user info.
type OIDCProvider struct {
	*BaseOAuth2Provider        // Embed for shared OAuth 2.0 logic
	oidcConfig          *OIDCConfig // Store original OIDC config (Issuer + common OAuth fields)
	endpoints           *OIDCEndpoints
	forceConsentScreen  bool
	idTokenValidator    *idTokenValidator
}

// OIDCProviderOption configures an OIDCProvider.
type OIDCProviderOption func(*OIDCProvider)

// WithHTTPClient sets a custom HTTP client for the provider.
func WithHTTPClient(client networking.HTTPClient) OIDCProviderOption {
	return func(p *OIDCProvider) {
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
	return func(p *OIDCProvider) {
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
) (*OIDCProvider, error) {
	if config == nil {
		return nil, errors.New("config is required")
	}

	// Validate OIDC config
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	logger.Infow("creating OIDC provider",
		"issuer", config.Issuer,
		"client_id", config.ClientID,
	)

	// Create HTTP client for the issuer host
	issuerURL, _ := url.Parse(config.Issuer) // Error already checked in config.Validate()
	httpClient, err := newHTTPClientForHost(issuerURL.Host)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP client: %w", err)
	}

	p := &OIDCProvider{
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

	if err := p.discoverEndpoints(ctx); err != nil {
		return nil, fmt.Errorf("failed to discover OIDC endpoints: %w", err)
	}

	// Now create OAuth2Config from discovered endpoints + OIDC config.
	// This allows the embedded BaseOAuth2Provider to use the discovered endpoints
	// for token requests while preserving the original OIDC config.
	oauth2Config := &OAuth2Config{
		CommonOAuthConfig: CommonOAuthConfig{
			ClientID:     config.ClientID,
			ClientSecret: config.ClientSecret,
			Scopes:       config.Scopes,
			RedirectURI:  config.RedirectURI,
		},
		AuthorizationEndpoint: p.endpoints.AuthorizationEndpoint,
		TokenEndpoint:         p.endpoints.TokenEndpoint,
		UserInfoEndpoint:      p.endpoints.UserInfoEndpoint,
	}
	p.BaseOAuth2Provider.config = oauth2Config

	// Initialize ID token validator for validating tokens from the upstream IDP.
	// Uses the discovered issuer and our client_id as expected audience.
	// JWKS URI from discovery enables signature verification.
	validator, err := newIDTokenValidator(idTokenValidatorConfig{
		expectedIssuer:   p.endpoints.Issuer,
		expectedAudience: config.ClientID,
		jwksURI:          p.endpoints.JWKSEndpoint,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create ID token validator: %w", err)
	}
	p.idTokenValidator = validator

	logger.Infow("OIDC provider created successfully",
		"issuer", p.endpoints.Issuer,
		"pkce_supported", p.supportsPKCE(),
	)

	return p, nil
}

// Type returns the provider type.
func (*OIDCProvider) Type() ProviderType {
	return ProviderTypeOIDC
}

// AuthorizationURL builds the URL to redirect the user to the upstream IDP.
// This overrides the base implementation to add OIDC-specific parameters (nonce, prompt)
// and use discovered endpoints.
func (p *OIDCProvider) AuthorizationURL(state, codeChallenge string, opts ...AuthorizationOption) (string, error) {
	if p.endpoints == nil {
		return "", errors.New("OIDC endpoints not discovered")
	}

	// Apply authorization options to extract nonce for logging
	authOpts := &authorizationOptions{}
	for _, opt := range opts {
		opt(authOpts)
	}

	logger.Debugw("building authorization URL",
		"authorization_endpoint", p.endpoints.AuthorizationEndpoint,
		"has_pkce", codeChallenge != "",
		"has_nonce", authOpts.nonce != "",
	)

	// PKCE support check with warning for OIDC discovery
	if codeChallenge != "" && !p.supportsPKCE() {
		logger.Warn("PKCE code challenge provided but provider does not advertise S256 support, sending anyway")
	}

	// Build OIDC-specific additional params
	oidcOpts := p.buildOIDCAuthOptions(authOpts)

	// Determine scopes: use configured or OIDC defaults
	upstreamScopes := p.oidcConfig.Scopes
	if len(upstreamScopes) == 0 {
		upstreamScopes = []string{"openid", "profile", "email"}
	}

	// Merge caller's opts with our OIDC-specific opts
	allOpts := append(opts, oidcOpts) //nolint:gocritic // intentionally appending single element

	return p.buildAuthorizationURL(
		p.endpoints.AuthorizationEndpoint,
		state,
		codeChallenge,
		upstreamScopes,
		allOpts...,
	)
}

// buildOIDCAuthOptions creates additional authorization options for OIDC-specific parameters.
func (p *OIDCProvider) buildOIDCAuthOptions(authOpts *authorizationOptions) AuthorizationOption {
	return WithAdditionalParams(p.buildOIDCParams(authOpts))
}

// buildOIDCParams builds the OIDC-specific authorization parameters.
func (p *OIDCProvider) buildOIDCParams(authOpts *authorizationOptions) map[string]string {
	params := make(map[string]string)

	// Add nonce for OIDC ID Token replay protection (OIDC Core Section 3.1.2.1)
	if authOpts.nonce != "" {
		params["nonce"] = authOpts.nonce
	}

	// Add prompt=consent if configured to force the consent screen
	if p.forceConsentScreen {
		params["prompt"] = "consent"
	}

	return params
}

// ExchangeCode exchanges an authorization code for tokens with the upstream IDP.
// This overrides the base implementation to use discovered endpoints and add ID token validation.
func (p *OIDCProvider) ExchangeCode(ctx context.Context, code, codeVerifier string) (*Tokens, error) {
	if p.endpoints == nil {
		return nil, errors.New("OIDC endpoints not discovered")
	}

	if code == "" {
		return nil, errors.New("authorization code is required")
	}

	logger.Infow("exchanging authorization code for tokens",
		"token_endpoint", p.endpoints.TokenEndpoint,
		"has_pkce_verifier", codeVerifier != "",
	)

	// Use base helper for param building, tokenRequest with discovered endpoint
	params := p.buildCodeExchangeParams(code, codeVerifier)
	tokens, err := p.tokenRequest(ctx, p.endpoints.TokenEndpoint, params)
	if err != nil {
		return nil, err
	}

	// OIDC-specific: Validate ID token if present (OIDC Core Section 3.1.3.7).
	// ID token validation is REQUIRED per the spec when an ID token is returned.
	if tokens.IDToken != "" && p.idTokenValidator != nil {
		if _, err := p.idTokenValidator.validateIDToken(tokens.IDToken); err != nil {
			return nil, fmt.Errorf("ID token validation failed: %w", err)
		}
	}

	logger.Infow("authorization code exchange successful",
		"has_refresh_token", tokens.RefreshToken != "",
		"has_id_token", tokens.IDToken != "",
		"expires_at", tokens.ExpiresAt.Format(time.RFC3339),
	)

	return tokens, nil
}

// RefreshTokens refreshes the upstream IDP tokens.
// This overrides the base implementation to use discovered endpoints and add ID token validation.
func (p *OIDCProvider) RefreshTokens(ctx context.Context, refreshToken string) (*Tokens, error) {
	if p.endpoints == nil {
		return nil, errors.New("OIDC endpoints not discovered")
	}

	if refreshToken == "" {
		return nil, errors.New("refresh token is required")
	}

	logger.Infow("refreshing tokens",
		"token_endpoint", p.endpoints.TokenEndpoint,
	)

	// Use base helper for param building, tokenRequest with discovered endpoint
	params := p.buildRefreshParams(refreshToken)
	tokens, err := p.tokenRequest(ctx, p.endpoints.TokenEndpoint, params)
	if err != nil {
		return nil, err
	}

	// OIDC-specific: Validate ID token if present (per OIDC Core spec).
	// Some providers may return a new ID token on refresh.
	if tokens.IDToken != "" && p.idTokenValidator != nil {
		if _, err := p.idTokenValidator.validateIDToken(tokens.IDToken); err != nil {
			return nil, fmt.Errorf("ID token validation failed: %w", err)
		}
	}

	logger.Infow("token refresh successful",
		"has_new_refresh_token", tokens.RefreshToken != "",
		"expires_at", tokens.ExpiresAt.Format(time.RFC3339),
	)

	return tokens, nil
}

// UserInfo fetches user information from the upstream IDP.
func (p *OIDCProvider) UserInfo(ctx context.Context, accessToken string) (*UserInfo, error) {
	if p.endpoints == nil {
		return nil, errors.New("OIDC endpoints not discovered")
	}

	if p.endpoints.UserInfoEndpoint == "" {
		return nil, errors.New("userinfo endpoint not available")
	}

	if accessToken == "" {
		return nil, errors.New("access token is required")
	}

	logger.Debugw("fetching user info",
		"userinfo_endpoint", p.endpoints.UserInfoEndpoint,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoints.UserInfoEndpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("userinfo request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		// Log full response for debugging, but return sanitized error to prevent information disclosure
		logger.Debugw("userinfo request failed",
			"status", resp.StatusCode,
			"body", string(body))
		return nil, fmt.Errorf("userinfo request failed with status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("failed to read userinfo response: %w", err)
	}

	// TODO: Per OIDC Core Section 5.3.4, the UserInfo response may be returned as a signed JWT
	// (application/jwt content type) instead of JSON. Currently we only support JSON responses.
	// To support JWT responses: check Content-Type header, parse JWT, validate signature using
	// the IDP's JWKS, then extract claims from the JWT payload.

	// Parse all claims into a map
	var claims map[string]any
	if err := json.Unmarshal(body, &claims); err != nil {
		return nil, fmt.Errorf("failed to parse userinfo response: %w", err)
	}

	userInfo := &UserInfo{
		Claims: claims,
	}

	// Extract standard claims
	if sub, ok := claims["sub"].(string); ok {
		userInfo.Subject = sub
	}
	if email, ok := claims["email"].(string); ok {
		userInfo.Email = email
	}
	if name, ok := claims["name"].(string); ok {
		userInfo.Name = name
	}

	logger.Debugw("user info retrieved",
		"subject", userInfo.Subject,
		"has_email", userInfo.Email != "",
	)

	return userInfo, nil
}

// UserInfoWithSubjectValidation fetches user information from the upstream IDP and validates
// that the returned subject claim matches the expected subject from the ID Token.
// This validation is required per OIDC Core Section 5.3.4 to prevent user impersonation attacks.
//
// Per the specification: "The sub (subject) Claim MUST always be returned in the UserInfo Response."
// and "The sub Claim in the UserInfo Response MUST be verified to exactly match the sub Claim in
// the ID Token; if they do not match, the UserInfo Response values MUST NOT be used."
func (p *OIDCProvider) UserInfoWithSubjectValidation(
	ctx context.Context,
	accessToken string,
	expectedSubject string,
) (*UserInfo, error) {
	// Fetch user info from the upstream IDP
	userInfo, err := p.UserInfo(ctx, accessToken)
	if err != nil {
		return nil, err
	}

	// Validate subject matches the ID Token's subject per OIDC Core Section 5.3.4
	if userInfo.Subject != expectedSubject {
		logger.Warnw("userinfo subject mismatch",
			"expected_subject", expectedSubject,
			"actual_subject", userInfo.Subject,
		)
		return nil, fmt.Errorf("%w: expected %q, got %q",
			ErrUserInfoSubjectMismatch, expectedSubject, userInfo.Subject)
	}

	return userInfo, nil
}

// Endpoints returns the discovered OIDC endpoints.
// This method is exported to allow inspection of discovered endpoints for debugging,
// testing, and advanced use cases where callers need to access endpoint URLs directly.
// Currently only used internally in tests, but kept exported for API stability.
func (p *OIDCProvider) Endpoints() *OIDCEndpoints {
	return p.endpoints
}

// ValidateIDToken validates an ID token received from the upstream IDP.
// This performs basic claim validation (iss, aud, exp) without signature verification.
// See OIDC Core Section 3.1.3.7 for validation requirements.
//
// Note: This is a minimal implementation. For production use, signature verification
// should be enabled once JWKS fetching is implemented.
func (p *OIDCProvider) ValidateIDToken(idToken string) (*IDTokenClaims, error) {
	if p.idTokenValidator == nil {
		return nil, errors.New("ID token validator not initialized")
	}

	logger.Debugw("validating ID token",
		"issuer", p.endpoints.Issuer,
	)

	claims, err := p.idTokenValidator.validateIDToken(idToken)
	if err != nil {
		logger.Debugw("ID token validation failed",
			"error", err.Error(),
		)
		return nil, err
	}

	logger.Debugw("ID token validated successfully",
		"subject", claims.Subject,
		"expires_at", claims.ExpiresAt.Format(time.RFC3339),
	)

	return claims, nil
}

// ValidateIDTokenWithNonce validates an ID token with nonce verification.
// This performs all validations from ValidateIDToken plus validates that the
// nonce claim in the ID token matches the expected nonce that was sent in
// the authorization request.
// See OIDC Core Section 3.1.3.7 step 11 for nonce validation requirements.
func (p *OIDCProvider) ValidateIDTokenWithNonce(idToken, expectedNonce string) (*IDTokenClaims, error) {
	if p.idTokenValidator == nil {
		return nil, errors.New("ID token validator not initialized")
	}

	logger.Debugw("validating ID token with nonce",
		"issuer", p.endpoints.Issuer,
	)

	claims, err := p.idTokenValidator.validateIDTokenWithNonce(idToken, expectedNonce)
	if err != nil {
		logger.Debugw("ID token validation with nonce failed",
			"error", err.Error(),
		)
		return nil, err
	}

	logger.Debugw("ID token validated successfully with nonce",
		"subject", claims.Subject,
		"expires_at", claims.ExpiresAt.Format(time.RFC3339),
	)

	return claims, nil
}

// discoverEndpoints fetches the OIDC discovery document from {issuer}/.well-known/openid-configuration.
func (p *OIDCProvider) discoverEndpoints(ctx context.Context) error {
	discoveryURL, err := buildDiscoveryURL(p.oidcConfig.Issuer)
	if err != nil {
		return err
	}

	logger.Debugw("discovering OIDC endpoints", "url", discoveryURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create discovery request: %w", err)
	}

	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("discovery request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		// Log full response for debugging, but return sanitized error to prevent information disclosure
		logger.Debugw("discovery request failed",
			"status", resp.StatusCode,
			"body", string(body))
		return fmt.Errorf("discovery request failed with status %d", resp.StatusCode)
	}

	// Validate content type
	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(strings.ToLower(contentType), "application/json") {
		return fmt.Errorf("unexpected content type: %s", contentType)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return fmt.Errorf("failed to read discovery response: %w", err)
	}

	var endpoints OIDCEndpoints
	if err := json.Unmarshal(body, &endpoints); err != nil {
		return fmt.Errorf("failed to parse discovery document: %w", err)
	}

	if err := validateDiscoveryDocument(&endpoints, p.oidcConfig.Issuer); err != nil {
		return fmt.Errorf("invalid discovery document: %w", err)
	}

	p.endpoints = &endpoints
	logger.Debugw("discovered OIDC endpoints",
		"issuer", endpoints.Issuer,
		"authorization_endpoint", endpoints.AuthorizationEndpoint,
		"token_endpoint", endpoints.TokenEndpoint,
		"userinfo_endpoint", endpoints.UserInfoEndpoint,
	)

	return nil
}

// supportsPKCE checks if the provider advertises S256 PKCE support.
func (p *OIDCProvider) supportsPKCE() bool {
	if p.endpoints == nil {
		return false
	}
	return slices.Contains(p.endpoints.CodeChallengeMethodsSupported, pkceChallengeMethodS256)
}

// buildDiscoveryURL constructs the OIDC discovery URL from the issuer.
func buildDiscoveryURL(issuer string) (string, error) {
	if issuer == "" {
		return "", errors.New("issuer is required")
	}

	// Parse and validate the issuer URL
	issuerURL, err := url.Parse(issuer)
	if err != nil {
		return "", fmt.Errorf("invalid issuer URL: %w", err)
	}

	// Ensure HTTPS (except for localhost for testing)
	if issuerURL.Scheme != schemeHTTPS && !networking.IsLocalhost(issuerURL.Host) {
		return "", fmt.Errorf("issuer must use HTTPS: %s", issuer)
	}

	// Build discovery URL
	// Ensure no double slashes
	basePath := strings.TrimSuffix(issuer, "/")
	return basePath + "/.well-known/openid-configuration", nil
}

// validateDiscoveryDocument validates the OIDC discovery document.
func validateDiscoveryDocument(doc *OIDCEndpoints, expectedIssuer string) error {
	if doc.Issuer == "" {
		return errors.New("missing issuer")
	}

	// Issuer must match exactly (per OIDC spec)
	if doc.Issuer != expectedIssuer {
		return fmt.Errorf("issuer mismatch: expected %s, got %s", expectedIssuer, doc.Issuer)
	}

	if doc.AuthorizationEndpoint == "" {
		return errors.New("missing authorization_endpoint")
	}

	if doc.TokenEndpoint == "" {
		return errors.New("missing token_endpoint")
	}

	// Validate that discovered endpoints are under the same origin as the issuer.
	// This prevents a malicious discovery document from redirecting requests to attacker-controlled servers.
	if err := validateEndpointOrigin(doc.AuthorizationEndpoint, expectedIssuer); err != nil {
		return fmt.Errorf("authorization_endpoint origin mismatch: %w", err)
	}

	if err := validateEndpointOrigin(doc.TokenEndpoint, expectedIssuer); err != nil {
		return fmt.Errorf("token_endpoint origin mismatch: %w", err)
	}

	// Optional endpoints - only validate if present
	if doc.UserInfoEndpoint != "" {
		if err := validateEndpointOrigin(doc.UserInfoEndpoint, expectedIssuer); err != nil {
			return fmt.Errorf("userinfo_endpoint origin mismatch: %w", err)
		}
	}

	if doc.JWKSEndpoint != "" {
		if err := validateEndpointOrigin(doc.JWKSEndpoint, expectedIssuer); err != nil {
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
	if endpointURL.Scheme != schemeHTTPS {
		return fmt.Errorf(
			"scheme mismatch: issuer uses HTTPS but endpoint uses %q "+
				"(all endpoints must use HTTPS for non-localhost issuers)",
			endpointURL.Scheme)
	}

	// No host validation - the discovery document comes from a trusted HTTPS source
	// and major providers legitimately use different hosts for different endpoints
	return nil
}

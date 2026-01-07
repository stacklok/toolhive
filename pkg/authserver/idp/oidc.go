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

package idp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxResponseSize is the maximum allowed response size for HTTP requests to prevent DoS.
const maxResponseSize = 1024 * 1024 // 1MB

// Compile-time interface compliance checks.
var (
	_ Provider                 = (*OIDCProvider)(nil)
	_ IDTokenNonceValidator    = (*OIDCProvider)(nil)
	_ UserInfoSubjectValidator = (*OIDCProvider)(nil)
)

// OIDCProvider implements Provider for OIDC-compliant identity providers.
type OIDCProvider struct {
	config             *UpstreamConfig
	endpoints          *OIDCEndpoints
	client             HTTPClient
	logger             *slog.Logger
	forceConsentScreen bool
	idTokenValidator   *idTokenValidator
}

// OIDCProviderOption configures an OIDCProvider.
type OIDCProviderOption func(*OIDCProvider)

// WithHTTPClient sets a custom HTTP client for the provider.
func WithHTTPClient(client HTTPClient) OIDCProviderOption {
	return func(p *OIDCProvider) {
		p.client = client
	}
}

// WithLogger sets a custom logger for the provider.
func WithLogger(logger *slog.Logger) OIDCProviderOption {
	return func(p *OIDCProvider) {
		p.logger = logger
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
// It performs OIDC discovery to fetch endpoints.
func NewOIDCProvider(
	ctx context.Context,
	config *UpstreamConfig,
	opts ...OIDCProviderOption,
) (*OIDCProvider, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	p := &OIDCProvider{
		config: config,
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 10 * time.Second,
			},
		},
		logger: slog.Default(),
	}

	for _, opt := range opts {
		opt(p)
	}

	if err := p.discoverEndpoints(ctx); err != nil {
		return nil, fmt.Errorf("failed to discover OIDC endpoints: %w", err)
	}

	// Initialize ID token validator for validating tokens from the upstream IDP.
	// Uses the discovered issuer and our client_id as expected audience.
	validator, err := newIDTokenValidator(idTokenValidatorConfig{
		expectedIssuer:   p.endpoints.Issuer,
		expectedAudience: config.ClientID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create ID token validator: %w", err)
	}
	p.idTokenValidator = validator

	return p, nil
}

// Name returns the provider name.
func (*OIDCProvider) Name() string {
	return "oidc"
}

// AuthorizationURL builds the URL to redirect the user to the upstream IDP.
func (p *OIDCProvider) AuthorizationURL(state, codeChallenge, nonce string) (string, error) {
	if p.endpoints == nil {
		return "", errors.New("OIDC endpoints not discovered")
	}

	if state == "" {
		return "", errors.New("state parameter is required")
	}

	// For upstream requests, always use configured scopes if available.
	// Config scopes represent what the upstream integration requires (e.g., Drive API access).
	// Client-requested scopes govern the client<->server relationship, not server<->upstream.
	upstreamScopes := p.config.Scopes

	// Only fall back to defaults if no config scopes
	if len(upstreamScopes) == 0 {
		// Default to basic OIDC scopes
		upstreamScopes = []string{"openid", "profile", "email"}
	}

	params := url.Values{
		"response_type": {"code"},
		"client_id":     {p.config.ClientID},
		"redirect_uri":  {p.config.RedirectURI},
		"scope":         {strings.Join(upstreamScopes, " ")},
		"state":         {state},
	}

	// Add nonce for OIDC ID Token replay protection (OIDC Core Section 3.1.2.1)
	if nonce != "" {
		params.Set("nonce", nonce)
	}

	// Add prompt=consent if configured to force the consent screen
	if p.forceConsentScreen {
		params.Set("prompt", "consent")
	}

	// Add PKCE challenge if provided and supported
	if codeChallenge != "" {
		if p.supportsPKCE() {
			params.Set("code_challenge", codeChallenge)
			params.Set("code_challenge_method", pkceChallengeMethodS256)
		} else {
			p.logger.Warn("PKCE code challenge provided but provider does not advertise S256 support, sending anyway")
			params.Set("code_challenge", codeChallenge)
			params.Set("code_challenge_method", pkceChallengeMethodS256)
		}
	}

	return p.endpoints.AuthorizationEndpoint + "?" + params.Encode(), nil
}

// ExchangeCode exchanges an authorization code for tokens with the upstream IDP.
func (p *OIDCProvider) ExchangeCode(ctx context.Context, code, codeVerifier string) (*Tokens, error) {
	if p.endpoints == nil {
		return nil, errors.New("OIDC endpoints not discovered")
	}

	if code == "" {
		return nil, errors.New("authorization code is required")
	}

	params := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {p.config.RedirectURI},
		"client_id":     {p.config.ClientID},
		"client_secret": {p.config.ClientSecret},
	}

	// Add PKCE verifier if provided
	if codeVerifier != "" {
		params.Set("code_verifier", codeVerifier)
	}

	return p.tokenRequest(ctx, params)
}

// RefreshTokens refreshes the upstream IDP tokens.
func (p *OIDCProvider) RefreshTokens(ctx context.Context, refreshToken string) (*Tokens, error) {
	if p.endpoints == nil {
		return nil, errors.New("OIDC endpoints not discovered")
	}

	if refreshToken == "" {
		return nil, errors.New("refresh token is required")
	}

	params := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {p.config.ClientID},
		"client_secret": {p.config.ClientSecret},
	}

	return p.tokenRequest(ctx, params)
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

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoints.UserInfoEndpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("userinfo request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		// Log full response for debugging, but return sanitized error to prevent information disclosure
		p.logger.Debug("userinfo request failed",
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
		p.logger.Warn("userinfo subject mismatch",
			slog.String("expected_subject", expectedSubject),
			slog.String("actual_subject", userInfo.Subject),
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
	return p.idTokenValidator.validateIDToken(idToken)
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
	return p.idTokenValidator.validateIDTokenWithNonce(idToken, expectedNonce)
}

// discoverEndpoints fetches the OIDC discovery document from {issuer}/.well-known/openid-configuration.
func (p *OIDCProvider) discoverEndpoints(ctx context.Context) error {
	discoveryURL, err := buildDiscoveryURL(p.config.Issuer)
	if err != nil {
		return err
	}

	p.logger.Debug("discovering OIDC endpoints", "url", discoveryURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create discovery request: %w", err)
	}

	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("discovery request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		// Log full response for debugging, but return sanitized error to prevent information disclosure
		p.logger.Debug("discovery request failed",
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

	if err := validateDiscoveryDocument(&endpoints, p.config.Issuer); err != nil {
		return fmt.Errorf("invalid discovery document: %w", err)
	}

	p.endpoints = &endpoints
	p.logger.Debug("discovered OIDC endpoints",
		"issuer", endpoints.Issuer,
		"authorization_endpoint", endpoints.AuthorizationEndpoint,
		"token_endpoint", endpoints.TokenEndpoint,
		"userinfo_endpoint", endpoints.UserInfoEndpoint,
	)

	return nil
}

// tokenRequest performs a token request to the upstream IDP.
func (p *OIDCProvider) tokenRequest(ctx context.Context, params url.Values) (*Tokens, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		p.endpoints.TokenEndpoint,
		strings.NewReader(params.Encode()),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create token request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("failed to read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var tokenError tokenErrorResponse
		if err := json.Unmarshal(body, &tokenError); err == nil && tokenError.Error != "" {
			// OAuth error responses with error/error_description are standardized and safe to return
			return nil, fmt.Errorf("token request failed: %s - %s", tokenError.Error, tokenError.ErrorDescription)
		}
		// Log full response for debugging, but return sanitized error to prevent information disclosure
		p.logger.Debug("token request failed",
			"status", resp.StatusCode,
			"body", string(body))
		return nil, fmt.Errorf("token request failed with status %d", resp.StatusCode)
	}

	var tokenResp tokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return nil, errors.New("token response missing access_token")
	}

	// Validate token_type per RFC 6749 Section 5.1
	// The comparison is case-insensitive per the spec
	if !strings.EqualFold(tokenResp.TokenType, "bearer") {
		return nil, fmt.Errorf("unexpected token_type: expected \"Bearer\", got %q", tokenResp.TokenType)
	}

	// Calculate expiration time
	expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	if tokenResp.ExpiresIn == 0 {
		// Default to 1 hour if not specified
		expiresAt = time.Now().Add(time.Hour)
	}

	// Validate ID token if present (OIDC Core Section 3.1.3.7).
	// Currently logs warnings on validation failure rather than rejecting,
	// since signature verification is not yet implemented.
	if tokenResp.IDToken != "" && p.idTokenValidator != nil {
		if _, err := p.idTokenValidator.validateIDToken(tokenResp.IDToken); err != nil {
			// Log warning but don't fail - signature verification not yet implemented
			p.logger.Warn("ID token validation warning (claims only, no signature verification)",
				"error", err.Error())
		}
	}

	return &Tokens{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		IDToken:      tokenResp.IDToken,
		ExpiresAt:    expiresAt,
	}, nil
}

// supportsPKCE checks if the provider advertises S256 PKCE support.
func (p *OIDCProvider) supportsPKCE() bool {
	if p.endpoints == nil {
		return false
	}
	for _, method := range p.endpoints.CodeChallengeMethodsSupported {
		if method == pkceChallengeMethodS256 {
			return true
		}
	}
	return false
}

// tokenResponse represents the response from the token endpoint.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// tokenErrorResponse represents an error response from the token endpoint.
type tokenErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
	ErrorURI         string `json:"error_uri,omitempty"`
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
	if issuerURL.Scheme != "https" && !isLocalhost(issuerURL.Host) {
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

// validateEndpointOrigin validates that an endpoint URL has the same origin as the issuer.
// This ensures the scheme and host match, preventing redirect attacks.
func validateEndpointOrigin(endpoint, issuer string) error {
	endpointURL, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid endpoint URL: %w", err)
	}

	issuerURL, err := url.Parse(issuer)
	if err != nil {
		return fmt.Errorf("invalid issuer URL: %w", err)
	}

	// Validate scheme matches
	// For localhost, both HTTP and HTTPS are acceptable (for testing)
	// For non-localhost, both must use the same scheme
	if !isLocalhost(issuerURL.Host) {
		if endpointURL.Scheme != issuerURL.Scheme {
			return fmt.Errorf("scheme mismatch: issuer uses %q but endpoint uses %q",
				issuerURL.Scheme, endpointURL.Scheme)
		}
	} else {
		// For localhost, allow HTTP or HTTPS but must be consistent or both localhost
		if !isLocalhost(endpointURL.Host) {
			return fmt.Errorf("host mismatch: issuer is localhost but endpoint host is %q", endpointURL.Host)
		}
	}

	// Validate host matches (including port)
	if endpointURL.Host != issuerURL.Host {
		return fmt.Errorf("host mismatch: issuer host is %q but endpoint host is %q",
			issuerURL.Host, endpointURL.Host)
	}

	return nil
}

// isLocalhost checks if the host is localhost.
func isLocalhost(host string) bool {
	// Remove port if present
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

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

// OIDCProvider implements Provider for OIDC-compliant identity providers.
type OIDCProvider struct {
	config    Config
	endpoints *OIDCEndpoints
	client    HTTPClient
	logger    *slog.Logger
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

// NewOIDCProvider creates a new OIDC provider.
// It performs OIDC discovery to fetch endpoints.
func NewOIDCProvider(
	ctx context.Context,
	config Config,
	opts ...OIDCProviderOption,
) (*OIDCProvider, error) {
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

	return p, nil
}

// Name returns the provider name.
func (*OIDCProvider) Name() string {
	return "oidc"
}

// AuthorizationURL builds the URL to redirect the user to the upstream IDP.
func (p *OIDCProvider) AuthorizationURL(state, codeChallenge string, _ []string) (string, error) {
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
		"prompt":        {"consent"}, // NOSUBMIT: Force consent screen for testing
	}

	// Add PKCE challenge if provided and supported
	if codeChallenge != "" {
		if p.supportsPKCE() {
			params.Set("code_challenge", codeChallenge)
			params.Set("code_challenge_method", PKCEChallengeMethodS256)
		} else {
			p.logger.Warn("PKCE code challenge provided but provider does not advertise S256 support, sending anyway")
			params.Set("code_challenge", codeChallenge)
			params.Set("code_challenge_method", PKCEChallengeMethodS256)
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
		return nil, fmt.Errorf("userinfo request returned status %d: %s", resp.StatusCode, string(body))
	}

	// Limit response size to prevent DoS
	const maxResponseSize = 1024 * 1024 // 1MB
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("failed to read userinfo response: %w", err)
	}

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

// Endpoints returns the discovered OIDC endpoints.
func (p *OIDCProvider) Endpoints() *OIDCEndpoints {
	return p.endpoints
}

// discoverEndpoints fetches the OIDC discovery document from {issuer}/.well-known/openid-configuration.
func (p *OIDCProvider) discoverEndpoints(ctx context.Context) error {
	discoveryURL, err := BuildDiscoveryURL(p.config.Issuer)
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
		return fmt.Errorf("discovery request returned status %d: %s", resp.StatusCode, string(body))
	}

	// Validate content type
	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(strings.ToLower(contentType), "application/json") {
		return fmt.Errorf("unexpected content type: %s", contentType)
	}

	// Limit response size to prevent DoS
	const maxResponseSize = 1024 * 1024 // 1MB
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return fmt.Errorf("failed to read discovery response: %w", err)
	}

	var endpoints OIDCEndpoints
	if err := json.Unmarshal(body, &endpoints); err != nil {
		return fmt.Errorf("failed to parse discovery document: %w", err)
	}

	if err := ValidateDiscoveryDocument(&endpoints, p.config.Issuer); err != nil {
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

	// Limit response size to prevent DoS
	const maxResponseSize = 1024 * 1024 // 1MB
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("failed to read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var tokenError tokenErrorResponse
		if err := json.Unmarshal(body, &tokenError); err == nil && tokenError.Error != "" {
			return nil, fmt.Errorf("token request failed: %s - %s", tokenError.Error, tokenError.ErrorDescription)
		}
		return nil, fmt.Errorf("token request returned status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp tokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return nil, errors.New("token response missing access_token")
	}

	// Calculate expiration time
	expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	if tokenResp.ExpiresIn == 0 {
		// Default to 1 hour if not specified
		expiresAt = time.Now().Add(time.Hour)
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
		if method == PKCEChallengeMethodS256 {
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

// BuildDiscoveryURL constructs the OIDC discovery URL from the issuer.
func BuildDiscoveryURL(issuer string) (string, error) {
	if issuer == "" {
		return "", errors.New("issuer is required")
	}

	// Parse and validate the issuer URL
	issuerURL, err := url.Parse(issuer)
	if err != nil {
		return "", fmt.Errorf("invalid issuer URL: %w", err)
	}

	// Ensure HTTPS (except for localhost for testing)
	if issuerURL.Scheme != "https" && !IsLocalhost(issuerURL.Host) {
		return "", fmt.Errorf("issuer must use HTTPS: %s", issuer)
	}

	// Build discovery URL
	// Ensure no double slashes
	basePath := strings.TrimSuffix(issuer, "/")
	return basePath + "/.well-known/openid-configuration", nil
}

// ValidateDiscoveryDocument validates the OIDC discovery document.
func ValidateDiscoveryDocument(doc *OIDCEndpoints, expectedIssuer string) error {
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

	return nil
}

// IsLocalhost checks if the host is localhost.
func IsLocalhost(host string) bool {
	// Remove port if present
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

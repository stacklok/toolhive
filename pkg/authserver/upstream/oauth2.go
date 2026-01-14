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
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/networking"
)

// ProviderType identifies the type of upstream Identity Provider.
type ProviderType string

const (
	// ProviderTypeOIDC is for OpenID Connect providers that support discovery.
	ProviderTypeOIDC ProviderType = "oidc"
	// ProviderTypeOAuth2 is for pure OAuth 2.0 providers with explicit endpoints.
	ProviderTypeOAuth2 ProviderType = "oauth2"
)

// AuthorizationOption configures authorization URL generation.
type AuthorizationOption func(*authorizationOptions)

type authorizationOptions struct {
	nonce            string
	additionalParams map[string]string
}

// WithNonce sets the OIDC nonce parameter for replay protection.
// Only affects providers that support OIDC.
func WithNonce(nonce string) AuthorizationOption {
	return func(o *authorizationOptions) {
		o.nonce = nonce
	}
}

// WithAdditionalParams adds custom parameters to the authorization URL.
func WithAdditionalParams(params map[string]string) AuthorizationOption {
	return func(o *authorizationOptions) {
		if o.additionalParams == nil {
			o.additionalParams = make(map[string]string)
		}
		for k, v := range params {
			o.additionalParams[k] = v
		}
	}
}

// OAuth2Provider handles communication with an upstream Identity Provider.
// This is the base interface for all provider types.
type OAuth2Provider interface {
	// Type returns the provider type.
	Type() ProviderType

	// AuthorizationURL builds the URL to redirect the user to the upstream IDP.
	// state: our internal state to correlate callback
	// codeChallenge: PKCE challenge to send to upstream (if supported)
	// opts: optional configuration such as nonce or additional parameters
	AuthorizationURL(state, codeChallenge string, opts ...AuthorizationOption) (string, error)

	// ExchangeCode exchanges an authorization code for tokens with the upstream IDP.
	ExchangeCode(ctx context.Context, code, codeVerifier string) (*Tokens, error)

	// RefreshTokens refreshes the upstream IDP tokens.
	RefreshTokens(ctx context.Context, refreshToken string) (*Tokens, error)
}

// pkceChallengeMethodS256 is the PKCE challenge method for SHA-256.
const pkceChallengeMethodS256 = "S256"

// maxResponseSize is the maximum allowed response size for HTTP requests to prevent DoS.
const maxResponseSize = 1024 * 1024 // 1MB

// schemeHTTPS is the HTTPS URL scheme.
const schemeHTTPS = "https"

// defaultTokenExpiration is the default token lifetime when expires_in is not specified.
const defaultTokenExpiration = time.Hour

// CommonOAuthConfig contains fields shared by all OAuth provider types.
// This provides compile-time type safety by separating OIDC and OAuth2 configuration.
type CommonOAuthConfig struct {
	// ClientID is the OAuth client ID registered with the upstream IDP.
	ClientID string

	// ClientSecret is the OAuth client secret registered with the upstream IDP.
	ClientSecret string

	// Scopes are the OAuth scopes to request from the upstream IDP.
	Scopes []string

	// RedirectURI is the callback URL where the upstream IDP will redirect
	// after authentication.
	RedirectURI string
}

// Validate checks that CommonOAuthConfig has all required fields.
func (c *CommonOAuthConfig) Validate() error {
	if c.ClientID == "" {
		return errors.New("client_id is required")
	}
	if c.ClientSecret == "" {
		return errors.New("client_secret is required")
	}
	if c.RedirectURI == "" {
		return errors.New("redirect_uri is required")
	}
	return validateRedirectURI(c.RedirectURI)
}

// OAuth2Config contains configuration for pure OAuth 2.0 providers without OIDC discovery.
type OAuth2Config struct {
	CommonOAuthConfig

	// AuthorizationEndpoint is the URL for the OAuth authorization endpoint.
	AuthorizationEndpoint string

	// TokenEndpoint is the URL for the OAuth token endpoint.
	TokenEndpoint string

	// UserInfoEndpoint is the URL for fetching user information (optional).
	UserInfoEndpoint string
}

// Validate checks that OAuth2Config has all required fields.
func (c *OAuth2Config) Validate() error {
	if c.AuthorizationEndpoint == "" {
		return errors.New("authorization_endpoint is required for OAuth2 providers")
	}
	if c.TokenEndpoint == "" {
		return errors.New("token_endpoint is required for OAuth2 providers")
	}
	return c.CommonOAuthConfig.Validate()
}

// validateRedirectURI validates an OAuth redirect URI according to RFC 6749 Section 3.1.2.
// It ensures the URI is:
//   - A parseable, absolute URL with scheme and host
//   - Free of fragments (per RFC 6749 Section 3.1.2.2)
//   - Free of user credentials
//   - Using http or https scheme only
//   - Using HTTPS for non-loopback addresses (HTTP allowed only for 127.0.0.1, ::1, localhost)
//   - Not containing wildcard hostnames
func validateRedirectURI(uri string) error {
	parsed, err := url.Parse(uri)
	if err != nil {
		return errors.New("redirect_uri must be an absolute URL with scheme and host")
	}

	// Must be absolute URL (has scheme and host)
	if parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("redirect_uri must be an absolute URL with scheme and host")
	}

	// Must not contain fragment per RFC 6749 Section 3.1.2.2
	if parsed.Fragment != "" {
		return errors.New("redirect_uri must not contain a fragment (#)")
	}

	// Must not contain user credentials
	if parsed.User != nil {
		return errors.New("redirect_uri must not contain user credentials")
	}

	// Must use http or https scheme
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("redirect_uri must use http or https scheme")
	}

	// HTTP scheme is only allowed for loopback addresses
	if parsed.Scheme == "http" && !networking.IsLocalhost(parsed.Host) {
		return errors.New("redirect_uri with http scheme requires loopback address (127.0.0.1, ::1, or localhost)")
	}

	// Must not contain wildcard hostname
	if strings.Contains(parsed.Hostname(), "*") {
		return errors.New("redirect_uri must not contain wildcard hostname")
	}

	return nil
}

// tokenResponse represents the response from the token endpoint.
// Per RFC 6749 Section 5.1 (Success Response) and OpenID Connect Core Section 3.1.3.3.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// tokenErrorResponse represents an error response from the token endpoint.
// Per RFC 6749 Section 5.2 (Error Response).
type tokenErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
	ErrorURI         string `json:"error_uri,omitempty"`
}

// parseSuccessTokenResponse validates and converts a parsed token response to Tokens.
// This handles success responses per RFC 6749 Section 5.1.
func parseSuccessTokenResponse(tokenResp *tokenResponse) (*Tokens, error) {
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
		expiresAt = time.Now().Add(defaultTokenExpiration)
	}

	return &Tokens{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		IDToken:      tokenResp.IDToken,
		ExpiresAt:    expiresAt,
	}, nil
}

// Compile-time interface compliance check.
var _ OAuth2Provider = (*BaseOAuth2Provider)(nil)

// BaseOAuth2Provider implements OAuth 2.0 flows for pure OAuth 2.0 providers.
// This can be used standalone for OAuth 2.0 providers without OIDC support,
// or embedded by OIDCProvider to share common OAuth 2.0 logic.
type BaseOAuth2Provider struct {
	config     *OAuth2Config
	httpClient networking.HTTPClient
}

// OAuth2ProviderOption configures a BaseOAuth2Provider.
type OAuth2ProviderOption func(*BaseOAuth2Provider)

// WithOAuth2HTTPClient sets a custom HTTP client.
func WithOAuth2HTTPClient(client networking.HTTPClient) OAuth2ProviderOption {
	return func(p *BaseOAuth2Provider) {
		p.httpClient = client
	}
}

// newBaseOAuth2Provider creates a BaseOAuth2Provider with validated config and HTTP client.
// The hostForClient parameter determines which URL to use for HTTP client configuration
// (e.g., TokenEndpoint for OAuth2, Issuer for OIDC).
//
// IMPORTANT: Callers must ensure config is non-nil before calling this function.
func newBaseOAuth2Provider(config *OAuth2Config, hostForClient string) (*BaseOAuth2Provider, error) {
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	httpClient, err := newHTTPClientForHost(hostForClient)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP client: %w", err)
	}

	return &BaseOAuth2Provider{
		config:     config,
		httpClient: httpClient,
	}, nil
}

// NewOAuth2Provider creates a new pure OAuth 2.0 provider.
// Use this for providers that don't support OIDC discovery.
// The config must include AuthorizationEndpoint and TokenEndpoint.
func NewOAuth2Provider(config *OAuth2Config, opts ...OAuth2ProviderOption) (*BaseOAuth2Provider, error) {
	if config == nil {
		return nil, errors.New("config is required")
	}

	logger.Infow("creating OAuth2 provider",
		"authorization_endpoint", config.AuthorizationEndpoint,
		"token_endpoint", config.TokenEndpoint,
		"client_id", config.ClientID,
	)

	tokenURL, _ := url.Parse(config.TokenEndpoint) // Error already validated in config.Validate()
	p, err := newBaseOAuth2Provider(config, tokenURL.Host)
	if err != nil {
		return nil, err
	}

	for _, opt := range opts {
		opt(p)
	}

	logger.Infow("OAuth2 provider created successfully",
		"authorization_endpoint", config.AuthorizationEndpoint,
		"token_endpoint", config.TokenEndpoint,
	)

	return p, nil
}

// Type returns the provider type.
func (*BaseOAuth2Provider) Type() ProviderType {
	return ProviderTypeOAuth2
}

// authorizationEndpoint returns the authorization endpoint URL.
func (p *BaseOAuth2Provider) authorizationEndpoint() string {
	return p.config.AuthorizationEndpoint
}

// tokenEndpoint returns the token endpoint URL.
func (p *BaseOAuth2Provider) tokenEndpoint() string {
	return p.config.TokenEndpoint
}

// AuthorizationURL builds the URL to redirect the user to the upstream IDP.
func (p *BaseOAuth2Provider) AuthorizationURL(state, codeChallenge string, opts ...AuthorizationOption) (string, error) {
	logger.Debugw("building authorization URL",
		"authorization_endpoint", p.authorizationEndpoint(),
		"has_pkce", codeChallenge != "",
	)
	return p.buildAuthorizationURL(
		p.authorizationEndpoint(),
		state,
		codeChallenge,
		p.config.Scopes,
		opts...,
	)
}

// buildAuthorizationURL builds an authorization URL with the given parameters.
// This is the core implementation used by AuthorizationURL and can be extended by embedding types.
func (p *BaseOAuth2Provider) buildAuthorizationURL(
	authEndpoint string,
	state string,
	codeChallenge string,
	scopes []string,
	opts ...AuthorizationOption,
) (string, error) {
	if authEndpoint == "" {
		return "", errors.New("authorization endpoint is required")
	}
	if state == "" {
		return "", errors.New("state parameter is required")
	}

	authOpts := &authorizationOptions{}
	for _, opt := range opts {
		opt(authOpts)
	}

	params := url.Values{
		"response_type": {"code"},
		"client_id":     {p.config.ClientID},
		"redirect_uri":  {p.config.RedirectURI},
		"state":         {state},
	}

	if len(scopes) > 0 {
		params.Set("scope", strings.Join(scopes, " "))
	}

	if codeChallenge != "" {
		params.Set("code_challenge", codeChallenge)
		params.Set("code_challenge_method", pkceChallengeMethodS256)
	}

	for k, v := range authOpts.additionalParams {
		params.Set(k, v)
	}

	return authEndpoint + "?" + params.Encode(), nil
}

// ExchangeCode exchanges an authorization code for tokens with the upstream IDP.
func (p *BaseOAuth2Provider) ExchangeCode(ctx context.Context, code, codeVerifier string) (*Tokens, error) {
	if code == "" {
		return nil, errors.New("authorization code is required")
	}

	logger.Infow("exchanging authorization code for tokens",
		"token_endpoint", p.config.TokenEndpoint,
		"has_pkce_verifier", codeVerifier != "",
	)

	params := p.buildCodeExchangeParams(code, codeVerifier)

	tokens, err := p.tokenRequest(ctx, p.tokenEndpoint(), params)
	if err != nil {
		return nil, err
	}

	logger.Infow("authorization code exchange successful",
		"has_refresh_token", tokens.RefreshToken != "",
		"expires_at", tokens.ExpiresAt.Format(time.RFC3339),
	)

	return tokens, nil
}

// buildCodeExchangeParams builds the parameters for authorization code exchange.
// This is extracted as a helper so OIDCProvider can reuse it while adding OIDC-specific behavior.
func (p *BaseOAuth2Provider) buildCodeExchangeParams(code, codeVerifier string) url.Values {
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

	return params
}

// RefreshTokens refreshes the upstream IDP tokens.
func (p *BaseOAuth2Provider) RefreshTokens(ctx context.Context, refreshToken string) (*Tokens, error) {
	if refreshToken == "" {
		return nil, errors.New("refresh token is required")
	}

	logger.Infow("refreshing tokens",
		"token_endpoint", p.config.TokenEndpoint,
	)

	params := p.buildRefreshParams(refreshToken)

	tokens, err := p.tokenRequest(ctx, p.tokenEndpoint(), params)
	if err != nil {
		return nil, err
	}

	logger.Infow("token refresh successful",
		"has_new_refresh_token", tokens.RefreshToken != "",
		"expires_at", tokens.ExpiresAt.Format(time.RFC3339),
	)

	return tokens, nil
}

// buildRefreshParams builds the parameters for token refresh.
// This is extracted as a helper so OIDCProvider can reuse it while adding OIDC-specific behavior.
func (p *BaseOAuth2Provider) buildRefreshParams(refreshToken string) url.Values {
	return url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {p.config.ClientID},
		"client_secret": {p.config.ClientSecret},
	}
}

// tokenRequest performs a token request to the upstream IDP.
func (p *BaseOAuth2Provider) tokenRequest(ctx context.Context, endpoint string, params url.Values) (*Tokens, error) {
	if endpoint == "" {
		return nil, errors.New("token endpoint is required")
	}

	logger.Debugw("sending token request",
		"token_endpoint", endpoint,
		"grant_type", params.Get("grant_type"),
	)

	// Custom error handler to parse OAuth error responses per RFC 6749 Section 5.2
	errorHandler := func(resp *http.Response, body []byte) error {
		var tokenError tokenErrorResponse
		if err := json.Unmarshal(body, &tokenError); err == nil && tokenError.Error != "" {
			// OAuth error responses with error/error_description are standardized and safe to return
			return fmt.Errorf("token request failed: %s - %s", tokenError.Error, tokenError.ErrorDescription)
		}
		// Log full response for debugging, but return sanitized error to prevent information disclosure
		logger.Debugw("token request failed",
			"status", resp.StatusCode,
			"body", string(body))
		return fmt.Errorf("token request failed with status %d", resp.StatusCode)
	}

	result, err := networking.FetchJSONWithForm[tokenResponse](
		ctx,
		p.httpClient,
		endpoint,
		params,
		networking.WithErrorHandler(errorHandler),
		networking.WithoutContentTypeValidation(), // Some IDPs may not return proper Content-Type
	)
	if err != nil {
		return nil, err
	}

	return parseSuccessTokenResponse(&result.Data)
}

// newHTTPClientForHost creates an HTTP client configured for the given host.
// It enables HTTP and private IPs only for localhost (development/testing).
func newHTTPClientForHost(host string) (*http.Client, error) {
	isLocalhost := networking.IsLocalhost(host)
	return networking.NewHttpClientBuilder().
		WithInsecureAllowHTTP(isLocalhost).
		WithPrivateIPs(isLocalhost).
		Build()
}

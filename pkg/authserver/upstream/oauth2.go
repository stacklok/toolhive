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
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/networking"
	oauthproto "github.com/stacklok/toolhive/pkg/oauth"
)

const (
	// ProviderTypeOAuth2 is for pure OAuth 2.0 providers with explicit endpoints.
	ProviderTypeOAuth2 ProviderType = "oauth2"

	// maxResponseSize is the maximum allowed UserInfo response size (1MB).
	// This prevents memory exhaustion from malicious or malformed responses.
	maxResponseSize = 1 << 20
)

// AuthorizationOption configures authorization URL generation.
type AuthorizationOption func(*authorizationOptions)

type authorizationOptions struct {
	additionalParams map[string]string
}

// WithAdditionalParams adds custom parameters to the authorization URL.
func WithAdditionalParams(params map[string]string) AuthorizationOption {
	return func(o *authorizationOptions) {
		if o.additionalParams == nil {
			o.additionalParams = make(map[string]string)
		}
		maps.Copy(o.additionalParams, params)
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

	// ResolveIdentity validates tokens and returns the canonical subject.
	// For OIDC providers with ID tokens, it validates the token and nonce.
	// For OAuth2 providers or as fallback, it fetches UserInfo.
	ResolveIdentity(ctx context.Context, tokens *Tokens, nonce string) (subject string, err error)

	// FetchUserInfo retrieves user information using the provided access token.
	// Returns an error if the provider is not configured for UserInfo fetching.
	FetchUserInfo(ctx context.Context, accessToken string) (*UserInfo, error)
}

// defaultTokenExpiration is the default token lifetime when expires_in is not specified.
const defaultTokenExpiration = time.Hour

// CommonOAuthConfig contains fields shared by all OAuth provider types.
// This provides compile-time type safety by separating OIDC and OAuth2 configuration.
type CommonOAuthConfig struct {
	// ClientID is the OAuth client ID registered with the upstream IDP.
	ClientID string

	// ClientSecret is the OAuth client secret registered with the upstream IDP.
	// Optional for public clients (RFC 6749 Section 2.1) which authenticate using
	// PKCE instead of a client secret. Required for confidential clients.
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

	// UserInfo contains configuration for fetching user information (optional).
	// When nil, the provider does not support UserInfo fetching.
	UserInfo *UserInfoConfig
}

// Validate checks that OAuth2Config has all required fields.
func (c *OAuth2Config) Validate() error {
	if c.AuthorizationEndpoint == "" {
		return errors.New("authorization_endpoint is required for OAuth2 providers")
	}
	if err := networking.ValidateEndpointURL(c.AuthorizationEndpoint); err != nil {
		return fmt.Errorf("invalid authorization_endpoint: %w", err)
	}
	if c.TokenEndpoint == "" {
		return errors.New("token_endpoint is required for OAuth2 providers")
	}
	if err := networking.ValidateEndpointURL(c.TokenEndpoint); err != nil {
		return fmt.Errorf("invalid token_endpoint: %w", err)
	}
	if c.UserInfo != nil {
		if err := c.UserInfo.Validate(); err != nil {
			return fmt.Errorf("invalid userinfo config: %w", err)
		}
	}
	return c.CommonOAuthConfig.Validate()
}

// validateRedirectURI validates an OAuth redirect URI per RFC 6749 and RFC 8252.
// This is our own callback URL where upstream IDPs redirect back to us. The upstream
// IDP validates this against their registered redirect URIs, so we only do basic checks.
func validateRedirectURI(uri string) error {
	return oauthproto.ValidateRedirectURI(uri, oauthproto.RedirectURIPolicyStrict)
}

// convertOAuth2Token converts an oauth2.Token to our Tokens type.
// It extracts id_token from token extras and validates the response.
func convertOAuth2Token(token *oauth2.Token) (*Tokens, error) {
	if token.AccessToken == "" {
		return nil, errors.New("token response missing access_token")
	}

	// Validate token_type per RFC 6749 Section 5.1
	// The comparison is case-insensitive per the spec
	if !strings.EqualFold(token.TokenType, "bearer") {
		return nil, fmt.Errorf("unexpected token_type: expected \"Bearer\", got %q", token.TokenType)
	}

	// Calculate expiration time
	expiresAt := token.Expiry
	if expiresAt.IsZero() {
		// Default to 1 hour if not specified
		expiresAt = time.Now().Add(defaultTokenExpiration)
	}

	// Extract ID token from extras (OIDC providers include it here)
	var idToken string
	if idTokenVal := token.Extra("id_token"); idTokenVal != nil {
		if s, ok := idTokenVal.(string); ok {
			idToken = s
		}
	}

	return &Tokens{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		IDToken:      idToken,
		ExpiresAt:    expiresAt,
	}, nil
}

// Compile-time interface compliance check.
var _ OAuth2Provider = (*BaseOAuth2Provider)(nil)

// BaseOAuth2Provider implements OAuth 2.0 flows for pure OAuth 2.0 providers.
// This can be used standalone for OAuth 2.0 providers without OIDC support,
// or embedded by OIDCProvider to share common OAuth 2.0 logic.
type BaseOAuth2Provider struct {
	config       *OAuth2Config
	oauth2Config *oauth2.Config
	httpClient   *http.Client
}

// OAuth2ProviderOption configures a BaseOAuth2Provider.
type OAuth2ProviderOption func(*BaseOAuth2Provider)

// WithOAuth2HTTPClient sets a custom HTTP client.
func WithOAuth2HTTPClient(client *http.Client) OAuth2ProviderOption {
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

	// Create the oauth2.Config for use with golang.org/x/oauth2 library
	oauth2Cfg := &oauth2.Config{
		ClientID:     config.ClientID,
		ClientSecret: config.ClientSecret,
		RedirectURL:  config.RedirectURI,
		Scopes:       config.Scopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:   config.AuthorizationEndpoint,
			TokenURL:  config.TokenEndpoint,
			AuthStyle: oauth2.AuthStyleInParams, // Send client credentials in POST body
		},
	}

	return &BaseOAuth2Provider{
		config:       config,
		oauth2Config: oauth2Cfg,
		httpClient:   httpClient,
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
	)

	tokenURL, err := url.Parse(config.TokenEndpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid token endpoint URL: %w", err)
	}
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

// AuthorizationURL builds the URL to redirect the user to the upstream IDP.
func (p *BaseOAuth2Provider) AuthorizationURL(state, codeChallenge string, opts ...AuthorizationOption) (string, error) {
	logger.Debugw("building authorization URL",
		"authorization_endpoint", p.authorizationEndpoint(),
		"has_pkce", codeChallenge != "",
	)
	return p.buildAuthorizationURL(
		state,
		codeChallenge,
		opts...,
	)
}

// buildAuthorizationURL builds an authorization URL with the given parameters.
// This is the core implementation used by AuthorizationURL and can be extended by embedding types.
func (p *BaseOAuth2Provider) buildAuthorizationURL(
	state string,
	codeChallenge string,
	opts ...AuthorizationOption,
) (string, error) {
	if p.oauth2Config.Endpoint.AuthURL == "" {
		return "", errors.New("authorization endpoint is required")
	}
	if state == "" {
		return "", errors.New("state parameter is required")
	}

	authOpts := &authorizationOptions{}
	for _, opt := range opts {
		opt(authOpts)
	}

	// Build oauth2 AuthCodeURL options
	var oauth2Opts []oauth2.AuthCodeOption

	// Add PKCE challenge if provided
	if codeChallenge != "" {
		oauth2Opts = append(oauth2Opts,
			oauth2.SetAuthURLParam("code_challenge", codeChallenge),
			oauth2.SetAuthURLParam("code_challenge_method", oauthproto.PKCEMethodS256),
		)
	}

	// Add any additional parameters
	for k, v := range authOpts.additionalParams {
		oauth2Opts = append(oauth2Opts, oauth2.SetAuthURLParam(k, v))
	}

	return p.oauth2Config.AuthCodeURL(state, oauth2Opts...), nil
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

	// Inject our custom HTTP client into the context for oauth2 to use
	ctx = context.WithValue(ctx, oauth2.HTTPClient, p.httpClient)

	// Build exchange options
	var opts []oauth2.AuthCodeOption
	if codeVerifier != "" {
		opts = append(opts, oauth2.VerifierOption(codeVerifier))
	}

	token, err := p.oauth2Config.Exchange(ctx, code, opts...)
	if err != nil {
		return nil, formatOAuth2Error(err, "token request failed")
	}

	tokens, err := convertOAuth2Token(token)
	if err != nil {
		return nil, err
	}

	logger.Infow("authorization code exchange successful",
		"has_refresh_token", tokens.RefreshToken != "",
		"expires_at", tokens.ExpiresAt.Format(time.RFC3339),
	)

	return tokens, nil
}

// RefreshTokens refreshes the upstream IDP tokens.
func (p *BaseOAuth2Provider) RefreshTokens(ctx context.Context, refreshToken string) (*Tokens, error) {
	if refreshToken == "" {
		return nil, errors.New("refresh token is required")
	}

	logger.Infow("refreshing tokens",
		"token_endpoint", p.config.TokenEndpoint,
	)

	// Inject our custom HTTP client into the context for oauth2 to use
	ctx = context.WithValue(ctx, oauth2.HTTPClient, p.httpClient)

	// Create an expired token with the refresh token to trigger refresh
	expiredToken := &oauth2.Token{
		RefreshToken: refreshToken,
		Expiry:       time.Now().Add(-time.Hour), // Expired token forces refresh
	}

	// Use TokenSource to get a new token via refresh
	tokenSource := p.oauth2Config.TokenSource(ctx, expiredToken)
	token, err := tokenSource.Token()
	if err != nil {
		return nil, formatOAuth2Error(err, "token request failed")
	}

	tokens, err := convertOAuth2Token(token)
	if err != nil {
		return nil, err
	}

	logger.Infow("token refresh successful",
		"has_new_refresh_token", tokens.RefreshToken != "",
		"expires_at", tokens.ExpiresAt.Format(time.RFC3339),
	)

	return tokens, nil
}

// FetchUserInfo fetches user information from the configured UserInfo endpoint.
// Returns an error if no UserInfo endpoint is configured.
// The field mapping from UserInfoConfig.FieldMapping is used to extract claims
// from non-standard provider responses.
func (p *BaseOAuth2Provider) FetchUserInfo(ctx context.Context, accessToken string) (*UserInfo, error) {
	if p.config.UserInfo == nil {
		return nil, errors.New("userinfo endpoint not configured")
	}

	cfg := p.config.UserInfo

	if accessToken == "" {
		return nil, errors.New("access token is required")
	}

	logger.Debugw("fetching user info",
		"userinfo_endpoint", cfg.EndpointURL,
	)

	// Determine HTTP method (default GET per OIDC Core Section 5.3.1)
	method := cfg.HTTPMethod
	if method == "" {
		method = http.MethodGet
	}

	req, err := http.NewRequestWithContext(ctx, method, cfg.EndpointURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set authorization header per RFC 6750 (Bearer Token Usage)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	// Add any additional headers (useful for non-standard providers like GitHub)
	for k, v := range cfg.AdditionalHeaders {
		req.Header.Set(k, v)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("userinfo request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// Drain response body for connection reuse, but don't log it to avoid
		// potentially exposing sensitive information from the upstream provider.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
		logger.Debugw("userinfo request failed",
			"status", resp.StatusCode)
		return nil, fmt.Errorf("userinfo request failed with status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("failed to read userinfo response: %w", err)
	}

	var claims map[string]any
	if err := json.Unmarshal(body, &claims); err != nil {
		return nil, fmt.Errorf("failed to parse userinfo response: %w", err)
	}

	// Use configured field mapping for subject extraction
	mapping := cfg.FieldMapping

	// Extract and validate required subject claim
	sub, err := mapping.ResolveSubject(claims)
	if err != nil {
		return nil, fmt.Errorf("userinfo response missing required subject claim: %w", err)
	}

	userInfo := &UserInfo{
		Subject: sub,
		Claims:  claims,
	}

	logger.Debugw("user info retrieved",
		"subject", userInfo.Subject,
	)

	return userInfo, nil
}

// ResolveIdentity resolves the user's identity by fetching UserInfo.
// For pure OAuth2 providers, this fetches user information from the configured endpoint.
// OIDC-specific validation (ID token nonce, subject matching) will be added when
// OIDCProvider is implemented.
func (p *BaseOAuth2Provider) ResolveIdentity(ctx context.Context, tokens *Tokens, _ string) (string, error) {
	if tokens == nil {
		return "", ErrIdentityResolutionFailed
	}

	userInfo, err := p.FetchUserInfo(ctx, tokens.AccessToken)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrIdentityResolutionFailed, err)
	}
	if userInfo == nil || userInfo.Subject == "" {
		return "", ErrIdentityResolutionFailed
	}

	return userInfo.Subject, nil
}

// formatOAuth2Error extracts error details from oauth2.RetrieveError for better error messages.
func formatOAuth2Error(err error, prefix string) error {
	var retrieveErr *oauth2.RetrieveError
	if errors.As(err, &retrieveErr) {
		// RetrieveError contains the OAuth error response
		if retrieveErr.ErrorCode != "" {
			return fmt.Errorf("%s: %s - %s", prefix, retrieveErr.ErrorCode, retrieveErr.ErrorDescription)
		}
		// Log full response for debugging, but return sanitized error to prevent information disclosure
		logger.Debugw("token request failed",
			"status", retrieveErr.Response.StatusCode,
			"body", string(retrieveErr.Body))
		return fmt.Errorf("%s with status %d", prefix, retrieveErr.Response.StatusCode)
	}
	// For other errors (network errors, etc.), wrap with context
	return fmt.Errorf("request failed: %w", err)
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

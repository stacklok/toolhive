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
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/networking"
)

const (
	// ProviderTypeOAuth2 is for pure OAuth 2.0 providers with explicit endpoints.
	ProviderTypeOAuth2 ProviderType = "oauth2"
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
			oauth2.SetAuthURLParam("code_challenge_method", "S256"),
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

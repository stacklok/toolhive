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
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/networking"
)

// Compile-time interface compliance check.
var _ OAuth2Provider = (*BaseOAuth2Provider)(nil)

// BaseOAuth2Provider implements OAuth 2.0 flows for pure OAuth 2.0 providers.
// This can be used standalone for OAuth 2.0 providers without OIDC support,
// or embedded by OIDCProvider to share common OAuth 2.0 logic.
type BaseOAuth2Provider struct {
	config     *Config
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

// NewOAuth2Provider creates a new pure OAuth 2.0 provider.
// Use this for providers that don't support OIDC discovery.
// The config must have Type set to ProviderTypeOAuth2 and must include
// AuthorizationEndpoint and TokenEndpoint.
func NewOAuth2Provider(config *Config, opts ...OAuth2ProviderOption) (*BaseOAuth2Provider, error) {
	if config == nil {
		return nil, errors.New("config is required")
	}

	if config.Type != ProviderTypeOAuth2 {
		return nil, fmt.Errorf("config.Type must be ProviderTypeOAuth2, got %q", config.Type)
	}

	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	logger.Infow("creating OAuth2 provider",
		"authorization_endpoint", config.AuthorizationEndpoint,
		"token_endpoint", config.TokenEndpoint,
		"client_id", config.ClientID,
	)

	p := &BaseOAuth2Provider{
		config:     config,
		httpClient: http.DefaultClient,
	}

	// Apply options
	for _, opt := range opts {
		opt(p)
	}

	logger.Infow("OAuth2 provider created successfully",
		"authorization_endpoint", config.AuthorizationEndpoint,
		"token_endpoint", config.TokenEndpoint,
	)

	return p, nil
}

// Name returns the provider name.
func (*BaseOAuth2Provider) Name() string {
	return "oauth2"
}

// AuthorizationURL builds the URL to redirect the user to the upstream IDP.
func (p *BaseOAuth2Provider) AuthorizationURL(state, codeChallenge string, opts ...AuthorizationOption) (string, error) {
	if state == "" {
		return "", errors.New("state parameter is required")
	}

	// Apply authorization options
	authOpts := &authorizationOptions{}
	for _, opt := range opts {
		opt(authOpts)
	}

	logger.Debugw("building authorization URL",
		"authorization_endpoint", p.config.AuthorizationEndpoint,
		"has_pkce", codeChallenge != "",
	)

	// Use configured scopes if available
	upstreamScopes := p.config.Scopes

	params := url.Values{
		"response_type": {"code"},
		"client_id":     {p.config.ClientID},
		"redirect_uri":  {p.config.RedirectURI},
		"state":         {state},
	}

	// Add scopes if configured
	if len(upstreamScopes) > 0 {
		params.Set("scope", strings.Join(upstreamScopes, " "))
	}

	// Add PKCE challenge if provided
	// Note: For pure OAuth 2.0 providers, we cannot check discovery metadata for PKCE support.
	// We send the PKCE parameters and let the provider accept or ignore them.
	if codeChallenge != "" {
		params.Set("code_challenge", codeChallenge)
		params.Set("code_challenge_method", pkceChallengeMethodS256)
	}

	// Add any additional custom parameters
	for k, v := range authOpts.additionalParams {
		params.Set(k, v)
	}

	return p.config.AuthorizationEndpoint + "?" + params.Encode(), nil
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

	tokens, err := p.tokenRequest(ctx, params)
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

	params := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {p.config.ClientID},
		"client_secret": {p.config.ClientSecret},
	}

	tokens, err := p.tokenRequest(ctx, params)
	if err != nil {
		return nil, err
	}

	logger.Infow("token refresh successful",
		"has_new_refresh_token", tokens.RefreshToken != "",
		"expires_at", tokens.ExpiresAt.Format(time.RFC3339),
	)

	return tokens, nil
}

// tokenRequest performs a token request to the upstream IDP.
func (p *BaseOAuth2Provider) tokenRequest(ctx context.Context, params url.Values) (*Tokens, error) {
	logger.Debugw("sending token request",
		"token_endpoint", p.config.TokenEndpoint,
		"grant_type", params.Get("grant_type"),
	)

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		p.config.TokenEndpoint,
		strings.NewReader(params.Encode()),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create token request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("failed to read token response: %w", err)
	}

	return parseTokenResponse(body, resp.StatusCode)
}

// NewProvider creates an appropriate provider based on the config type.
// For ProviderTypeOIDC, it creates an OIDCProvider with OIDC discovery.
// For ProviderTypeOAuth2, it creates a BaseOAuth2Provider with explicit endpoints.
//
// For more control over provider options, use NewOIDCProvider or NewOAuth2Provider directly.
func NewProvider(ctx context.Context, config *Config) (OAuth2Provider, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}

	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid provider config: %w", err)
	}

	switch config.Type {
	case ProviderTypeOIDC:
		return NewOIDCProvider(ctx, config)

	case ProviderTypeOAuth2:
		return NewOAuth2Provider(config)

	default:
		return nil, fmt.Errorf("unknown provider type: %q (must be %q or %q)",
			config.Type, ProviderTypeOIDC, ProviderTypeOAuth2)
	}
}

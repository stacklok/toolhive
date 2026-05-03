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

//go:generate mockgen -destination=mocks/mock_provider.go -package=mocks -source=oauth2.go OAuth2Provider

package upstream

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"os"
	"strings"

	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/authserver/oauthparams"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/oauthproto"
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

	// ExchangeCodeForIdentity exchanges an authorization code for tokens and resolves
	// the user's identity in a single atomic operation. This ensures that OIDC nonce
	// validation (replay protection) cannot be accidentally skipped.
	// For OIDC providers, the nonce is validated against the ID token.
	// For pure OAuth2 providers, identity is resolved via the UserInfo endpoint
	// and the nonce parameter is ignored.
	ExchangeCodeForIdentity(ctx context.Context, code, codeVerifier, nonce string) (*Identity, error)

	// RefreshTokens refreshes the upstream IDP tokens.
	// expectedSubject is the original sub claim; OIDC providers validate it per
	// Section 12.2 when the response includes a new ID token. Pure OAuth2 providers
	// ignore it.
	RefreshTokens(ctx context.Context, refreshToken, expectedSubject string) (*Tokens, error)
}

// CommonOAuthConfig contains fields shared by all OAuth provider types.
// This provides compile-time type safety by separating OIDC and OAuth2 configuration.
type CommonOAuthConfig struct {
	// ClientID is the OAuth client ID registered with the upstream IDP.
	ClientID string `json:"client_id" yaml:"client_id"`

	// ClientSecret is the OAuth client secret registered with the upstream IDP.
	// Optional for public clients (RFC 6749 Section 2.1) which authenticate using
	// PKCE instead of a client secret. Required for confidential clients.
	//nolint:gosec // G117: field legitimately holds sensitive data
	ClientSecret string `json:"client_secret,omitempty" yaml:"client_secret,omitempty"`

	// Scopes are the OAuth scopes to request from the upstream IDP.
	Scopes []string `json:"scopes,omitempty" yaml:"scopes,omitempty"`

	// RedirectURI is the callback URL where the upstream IDP will redirect
	// after authentication.
	RedirectURI string `json:"redirect_uri" yaml:"redirect_uri"`

	// AdditionalAuthorizationParams are extra query parameters to include in the
	// authorization URL. This is useful for providers that require custom parameters
	// such as Google's access_type=offline for obtaining refresh tokens.
	// Framework-managed parameters (response_type, client_id, redirect_uri, scope,
	// state, code_challenge, code_challenge_method, nonce) are not allowed here
	// and will be rejected during validation.
	//nolint:lll // field tags require full JSON+YAML names
	AdditionalAuthorizationParams map[string]string `json:"additional_authorization_params,omitempty" yaml:"additional_authorization_params,omitempty"`
}

// Validate checks that CommonOAuthConfig has all required fields.
func (c *CommonOAuthConfig) Validate() error {
	if c.ClientID == "" {
		return errors.New("client_id is required")
	}
	if c.RedirectURI == "" {
		return errors.New("redirect_uri is required")
	}
	if err := oauthparams.Validate(c.AdditionalAuthorizationParams); err != nil {
		return err
	}
	return validateRedirectURI(c.RedirectURI)
}

// OAuth2Config contains configuration for pure OAuth 2.0 providers without OIDC discovery.
type OAuth2Config struct {
	CommonOAuthConfig `yaml:",inline"`

	// AuthorizationEndpoint is the URL for the OAuth authorization endpoint.
	AuthorizationEndpoint string `json:"authorization_endpoint" yaml:"authorization_endpoint"`

	// TokenEndpoint is the URL for the OAuth token endpoint.
	TokenEndpoint string `json:"token_endpoint" yaml:"token_endpoint"`

	// UserInfo contains configuration for fetching user information (optional).
	// When nil, the provider does not support UserInfo fetching.
	UserInfo *UserInfoConfig `json:"userinfo,omitempty" yaml:"userinfo,omitempty"`

	// TokenResponseMapping configures custom field extraction from non-standard token responses.
	// When set, the provider performs the token exchange HTTP call directly (bypassing
	// golang.org/x/oauth2) and extracts fields using gjson dot-notation paths.
	// When nil, standard OAuth 2.0 token response parsing is used.
	TokenResponseMapping *TokenResponseMapping `json:"token_response_mapping,omitempty" yaml:"token_response_mapping,omitempty"`
}

// TokenResponseMapping configures extraction of token fields from non-standard
// OAuth token endpoint responses using gjson dot-notation paths.
type TokenResponseMapping struct {
	// AccessTokenPath is the gjson path to the access token (required).
	AccessTokenPath string

	// ScopePath is the gjson path to the scope. Defaults to "scope".
	ScopePath string

	// RefreshTokenPath is the gjson path to the refresh token. Defaults to "refresh_token".
	RefreshTokenPath string

	// ExpiresInPath is the gjson path to the expires_in value. Defaults to "expires_in".
	ExpiresInPath string
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
	if c.TokenResponseMapping != nil {
		if c.TokenResponseMapping.AccessTokenPath == "" {
			return errors.New("token_response_mapping.access_token_path is required when token_response_mapping is set")
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
		ExpiresAt:    token.Expiry,
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

	slog.Info("creating OAuth2 provider",
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

	slog.Info("oauth2 provider created successfully",
		"authorization_endpoint", config.AuthorizationEndpoint,
		"token_endpoint", config.TokenEndpoint,
	)

	if config.UserInfo == nil {
		// Surface synthesis mode at construction so operators see it once
		// per provider rather than only inferring from missing claims later.
		slog.Warn("oauth2 upstream has no userinfo configured; using synthesis mode "+
			"(non-PII subject from access token, no Name/Email). Configure "+
			"userInfo if a real identity endpoint exists.",
			"authorization_endpoint", config.AuthorizationEndpoint,
		)
	}

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
	slog.Debug("building authorization URL",
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
	if len(p.config.AdditionalAuthorizationParams) > 0 {
		WithAdditionalParams(p.config.AdditionalAuthorizationParams)(authOpts)
	}
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

// ExchangeCodeForIdentity exchanges an authorization code for tokens and resolves
// the user's identity in a single atomic operation.
// For pure OAuth2 providers, identity is resolved via UserInfo when configured;
// otherwise Subject is synthesized via synthesizeIdentity (which rejects empty
// access tokens to prevent the well-known sha256("") subject collision) and
// Name/Email are left empty. The nonce parameter is ignored (no ID token).
func (p *BaseOAuth2Provider) ExchangeCodeForIdentity(ctx context.Context, code, codeVerifier, _ string) (*Identity, error) {
	tokens, err := p.exchangeCodeForTokens(ctx, code, codeVerifier)
	if err != nil {
		return nil, err
	}

	// No userinfo: synthesize a non-PII subject from the access token.
	// Synthetic=true tells the callback handler to bypass UserResolver — the
	// synthesized subject rotates per access token, so persisting it would
	// create a new `users` row on every re-authentication.
	if p.config.UserInfo == nil {
		return synthesizeIdentity(tokens)
	}

	userInfo, err := p.fetchUserInfo(ctx, tokens.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrIdentityResolutionFailed, err)
	}
	if userInfo == nil || userInfo.Subject == "" {
		return nil, ErrIdentityResolutionFailed
	}

	return &Identity{
		Tokens:  tokens,
		Subject: userInfo.Subject,
		Name:    userInfo.Name,
		Email:   userInfo.Email,
	}, nil
}

// synthesizedSubjectPrefix tags subjects produced by
// synthesizeSubjectFromAccessToken. The prefix is part of the package's
// externally observable contract; downstream code should recognize
// synthesized subjects via the exported IsSynthesizedSubject predicate
// rather than this constant.
const synthesizedSubjectPrefix = "tk-"

// IsSynthesizedSubject reports whether subject was produced by the
// synthesis-mode fallback (vs. resolved from a userinfo endpoint or ID
// token). Use this for code paths that only see the bare subject string —
// e.g., JWT claim consumers, audit pipelines, status conditions. Callers
// holding an *Identity should prefer Identity.Synthetic, which is set at
// the same source of truth.
//
// Purely structural — checks the prefix only, does not validate the digest.
func IsSynthesizedSubject(subject string) bool {
	return strings.HasPrefix(subject, synthesizedSubjectPrefix)
}

// synthesizeSubjectFromAccessToken returns a stable, opaque identifier
// derived from an access token, for OAuth2 upstreams with no userinfo
// endpoint. Output: synthesizedSubjectPrefix + lowercase hex of the first
// 16 bytes of SHA-256(accessToken) — 35 chars total, e.g.
// "tk-89abcdef0123456789abcdef01234567".
//
// The output is non-PII assuming the upstream issues opaque (non-JWT)
// bearer tokens; the digest reveals nothing about the input beyond what an
// attacker holding a candidate token could confirm by re-hashing. 16 bytes
// is sufficient collision resistance for a session-key role.
//
// Only reached when OAuth2Config.UserInfo is nil. OIDC providers always
// have an ID-token-derived subject. Callers must reject empty access
// tokens — sha256("") is a well-known constant, and synthesizing a
// subject from it would collapse distinct sessions onto a single
// storage bucket. synthesizeIdentity enforces this invariant.
func synthesizeSubjectFromAccessToken(accessToken string) string {
	sum := sha256.Sum256([]byte(accessToken))
	return synthesizedSubjectPrefix + hex.EncodeToString(sum[:16])
}

// synthesizeIdentity builds a synthesized Identity for an OAuth2 upstream
// with no userinfo endpoint. Returns ErrIdentityResolutionFailed when the
// access token is empty: sha256("") is the well-known constant
// e3b0c44298fc1c14…, so synthesizing a subject from an empty token would
// collapse every affected session onto a single (UserID, ProviderID)
// storage bucket — a cross-tenant state-mixing hazard. Defense-in-depth:
// convertOAuth2Token already rejects empty AccessToken at exchange time,
// so this guard is unreachable today through ExchangeCodeForIdentity. It
// exists so a future code path (e.g., a custom token-response mapping
// that drops the field) cannot bypass the invariant.
func synthesizeIdentity(tokens *Tokens) (*Identity, error) {
	if tokens.AccessToken == "" {
		return nil, fmt.Errorf("%w: empty access token, cannot synthesize subject", ErrIdentityResolutionFailed)
	}
	return &Identity{
		Tokens:    tokens,
		Subject:   synthesizeSubjectFromAccessToken(tokens.AccessToken),
		Synthetic: true,
	}, nil
}

// exchangeCodeForTokens exchanges an authorization code for tokens with the upstream IDP.
func (p *BaseOAuth2Provider) exchangeCodeForTokens(ctx context.Context, code, codeVerifier string) (*Tokens, error) {
	if code == "" {
		return nil, errors.New("authorization code is required")
	}

	slog.Info("exchanging authorization code for tokens",
		"token_endpoint", p.config.TokenEndpoint,
		"has_pkce_verifier", codeVerifier != "",
	)

	// Wrap HTTP client with token response rewriter if mapping is configured.
	// This normalizes non-standard responses (e.g., GovSlack's nested fields)
	// before the oauth2 library parses them, keeping the standard exchange flow.
	httpClient := wrapHTTPClientWithMapping(p.httpClient, p.config.TokenResponseMapping, p.config.TokenEndpoint)
	ctx = context.WithValue(ctx, oauth2.HTTPClient, httpClient)

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

	slog.Debug("authorization code exchange successful",
		"has_refresh_token", tokens.RefreshToken != "",
		"expires_at", expiresAtLogValue(tokens.ExpiresAt),
	)

	return tokens, nil
}

// RefreshTokens refreshes the upstream IDP tokens.
//
// Sends `scope` explicitly. RFC 6749 §6 makes the param optional and says the
// AS SHOULD preserve original scopes on omission, but some ASes (notably
// Entra ID v1) silently narrow refreshed tokens to the user's default consent
// set when `scope` is omitted, dropping custom resource scopes.
//
// Uses oauth2.Config.Exchange with SetAuthURLParam overrides (mirroring the
// pattern in pkg/auth/oauth/non_caching_refresher.go) because the standard
// library's TokenSource refresh path doesn't expose a way to inject scope.
// The empty code= side-effect is tolerated — ASes dispatch on grant_type first.
func (p *BaseOAuth2Provider) RefreshTokens(ctx context.Context, refreshToken, _ string) (*Tokens, error) {
	if refreshToken == "" {
		return nil, errors.New("refresh token is required")
	}

	slog.Info("refreshing tokens",
		"token_endpoint", p.config.TokenEndpoint,
		"scope_count", len(p.oauth2Config.Scopes),
	)

	// Wrap HTTP client with token response rewriter if mapping is configured.
	httpClient := wrapHTTPClientWithMapping(p.httpClient, p.config.TokenResponseMapping, p.config.TokenEndpoint)
	ctx = context.WithValue(ctx, oauth2.HTTPClient, httpClient)

	opts := []oauth2.AuthCodeOption{
		oauth2.SetAuthURLParam("grant_type", "refresh_token"),
		oauth2.SetAuthURLParam("refresh_token", refreshToken),
	}
	if len(p.oauth2Config.Scopes) > 0 {
		opts = append(opts, oauth2.SetAuthURLParam("scope", strings.Join(p.oauth2Config.Scopes, " ")))
	}

	token, err := p.oauth2Config.Exchange(ctx, "", opts...)
	if err != nil {
		return nil, formatOAuth2Error(err, "token request failed")
	}

	tokens, err := convertOAuth2Token(token)
	if err != nil {
		return nil, err
	}
	// AS may not issue a new refresh token (RFC 6749 §6); preserve the old one.
	if tokens.RefreshToken == "" {
		tokens.RefreshToken = refreshToken
	}

	slog.Debug("token refresh successful",
		// Read from token (pre-§6-fallback) so the log accurately reflects
		// whether the AS rotated the refresh token vs the fallback kicking in.
		"has_new_refresh_token", token.RefreshToken != "",
		"expires_at", expiresAtLogValue(tokens.ExpiresAt),
	)

	return tokens, nil
}

// userInfo contains user information retrieved from the upstream IDP.
type userInfo struct {
	// Subject is the unique identifier for the user (sub claim).
	Subject string `json:"sub"`

	// Email is the user's email address.
	Email string `json:"email,omitempty"`

	// Name is the user's full name.
	Name string `json:"name,omitempty"`

	// Claims contains all claims returned by the userinfo endpoint.
	Claims map[string]any `json:"-"`
}

// fetchUserInfo fetches user information from the configured UserInfo endpoint.
// Returns an error if no UserInfo endpoint is configured.
// The field mapping from UserInfoConfig.FieldMapping is used to extract claims
// from non-standard provider responses.
func (p *BaseOAuth2Provider) fetchUserInfo(ctx context.Context, accessToken string) (*userInfo, error) {
	if p.config.UserInfo == nil {
		return nil, errors.New("userinfo endpoint not configured")
	}

	cfg := p.config.UserInfo

	if accessToken == "" {
		return nil, errors.New("access token is required")
	}

	slog.Debug("fetching user info",
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

	resp, err := p.httpClient.Do(req) //nolint:gosec // G704: URL is from OIDC discovery, not user input
	if err != nil {
		return nil, fmt.Errorf("userinfo request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// Drain response body for connection reuse, but don't log it to avoid
		// potentially exposing sensitive information from the upstream provider.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
		slog.Debug("userinfo request failed", //nolint:gosec // G706: status code is an integer
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

	// Use configured field mapping for claim extraction
	mapping := cfg.FieldMapping

	// Extract and validate required subject claim
	sub, err := mapping.ResolveSubject(claims)
	if err != nil {
		return nil, fmt.Errorf("userinfo response missing required subject claim: %w", err)
	}

	userInfo := &userInfo{
		Subject: sub,
		Name:    mapping.ResolveName(claims),
		Email:   mapping.ResolveEmail(claims),
		Claims:  claims,
	}

	slog.Debug("user info retrieved",
		"subject", userInfo.Subject,
		"has_email", userInfo.Email != "",
	)

	return userInfo, nil
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
		slog.Debug("token request failed",
			"status", retrieveErr.Response.StatusCode,
			"body", string(retrieveErr.Body))
		return fmt.Errorf("%s with status %d", prefix, retrieveErr.Response.StatusCode)
	}
	// For other errors (network errors, etc.), wrap with context
	return fmt.Errorf("request failed: %w", err)
}

// newHTTPClientForHost creates an HTTP client configured for the given host.
// It enables HTTP and private IPs only for localhost (development/testing),
// or when INSECURE_DISABLE_URL_VALIDATION is set (e.g. Kubernetes dev environments).
func newHTTPClientForHost(host string) (*http.Client, error) {
	allowInsecure := networking.IsLocalhost(host) ||
		strings.EqualFold(os.Getenv("INSECURE_DISABLE_URL_VALIDATION"), "true")
	return networking.NewHttpClientBuilder().
		WithInsecureAllowHTTP(allowInsecure).
		WithPrivateIPs(allowInsecure).
		Build()
}

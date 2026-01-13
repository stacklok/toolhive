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
	"fmt"
	"net/url"
	"strings"
	"time"
)

// pkceChallengeMethodS256 is the PKCE challenge method for SHA-256.
const pkceChallengeMethodS256 = "S256"

// tokenExpirationBuffer is the time buffer before actual expiration to consider a token expired.
// This accounts for clock skew and network latency.
const tokenExpirationBuffer = 30 * time.Second

// maxResponseSize is the maximum allowed response size for HTTP requests to prevent DoS.
const maxResponseSize = 1024 * 1024 // 1MB

// schemeHTTPS is the HTTPS URL scheme.
const schemeHTTPS = "https"

// defaultTokenExpiration is the default token lifetime when expires_in is not specified.
const defaultTokenExpiration = time.Hour

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

// Tokens represents the tokens obtained from an upstream Identity Provider.
// This type is used for token exchange with the IDP, but stored separately
// (see storage.IDPTokens for the storage representation).
type Tokens struct {
	// AccessToken is the access token from the upstream IDP.
	AccessToken string

	// RefreshToken is the refresh token from the upstream IDP (if provided).
	RefreshToken string

	// IDToken is the ID token from the upstream IDP (for OIDC).
	IDToken string

	// ExpiresAt is when the access token expires.
	ExpiresAt time.Time
}

// IsExpired returns true if the access token has expired or will expire within the buffer period.
// Returns true for nil receivers (treating nil tokens as expired).
func (t *Tokens) IsExpired() bool {
	if t == nil {
		return true
	}
	return time.Now().Add(tokenExpirationBuffer).After(t.ExpiresAt)
}

// UserInfo contains user information retrieved from the upstream IDP.
type UserInfo struct {
	// Subject is the unique identifier for the user (sub claim).
	Subject string `json:"sub"`

	// Email is the user's email address.
	Email string `json:"email,omitempty"`

	// Name is the user's full name.
	Name string `json:"name,omitempty"`

	// Claims contains all claims returned by the userinfo endpoint.
	Claims map[string]any `json:"-"`
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

// OAuth2Provider handles communication with an upstream Identity Provider.
// This is the base interface for all provider types.
type OAuth2Provider interface {
	// Name returns the provider name (e.g., "google", "oidc", "oauth2").
	Name() string

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

// UserInfoProvider is implemented by providers that support fetching user information.
// This is optional - not all OAuth 2.0 providers have a UserInfo endpoint.
type UserInfoProvider interface {
	// UserInfo fetches user information from the upstream IDP.
	UserInfo(ctx context.Context, accessToken string) (*UserInfo, error)
}

// IDTokenValidator is implemented by providers that can validate ID tokens.
type IDTokenValidator interface {
	// ValidateIDToken validates an ID token and returns parsed claims.
	ValidateIDToken(idToken string) (*IDTokenClaims, error)
}

// IDTokenNonceValidator is implemented by providers that support OIDC nonce validation.
// This is used to validate the nonce claim in ID tokens to prevent replay attacks.
type IDTokenNonceValidator interface {
	// ValidateIDTokenWithNonce validates an ID token with nonce verification.
	// Returns the parsed claims if validation succeeds, or an error if validation fails.
	ValidateIDTokenWithNonce(idToken, expectedNonce string) (*IDTokenClaims, error)
}

// UserInfoSubjectValidator is implemented by providers that support UserInfo subject validation
// per OIDC Core Section 5.3.4. This validation ensures the UserInfo response's subject matches
// the ID Token's subject to prevent user impersonation attacks.
type UserInfoSubjectValidator interface {
	// UserInfoWithSubjectValidation fetches user info and validates the subject matches expected.
	// Returns ErrUserInfoSubjectMismatch if the subjects do not match.
	UserInfoWithSubjectValidation(ctx context.Context, accessToken, expectedSubject string) (*UserInfo, error)
}

// ErrUserInfoSubjectMismatch is returned when the UserInfo endpoint returns a subject
// that does not match the expected subject from the ID Token.
// This validation is required per OIDC Core Section 5.3.4 to prevent user impersonation.
var ErrUserInfoSubjectMismatch = fmt.Errorf("userinfo subject does not match expected subject")

// Config contains configuration for connecting to an upstream
// Identity Provider (e.g., Google, Okta, Auth0).
type Config struct {
	// Type specifies the provider type (oidc or oauth2).
	// Required field that determines how the provider is initialized.
	Type ProviderType

	// Issuer is the URL of the upstream IDP (required for OIDC providers).
	Issuer string

	// AuthorizationEndpoint is the URL for authorization (required for OAuth2 providers).
	AuthorizationEndpoint string

	// TokenEndpoint is the URL for token requests (required for OAuth2 providers).
	TokenEndpoint string

	// UserInfoEndpoint is the URL for user info (optional).
	UserInfoEndpoint string

	// ClientID is the OAuth client ID registered with the upstream IDP.
	ClientID string

	// ClientSecret is the OAuth client secret registered with the upstream IDP.
	ClientSecret string

	// Scopes are the OAuth scopes to request from the upstream IDP.
	Scopes []string

	// RedirectURI is the callback URL where the upstream IDP will redirect
	// after authentication. This should be our authorization server's callback endpoint.
	RedirectURI string
}

// Validate checks that the Config is valid based on the provider type.
func (c *Config) Validate() error {
	// Validate based on provider type
	switch c.Type {
	case ProviderTypeOIDC:
		if c.Issuer == "" {
			return fmt.Errorf("upstream issuer is required for OIDC providers")
		}
	case ProviderTypeOAuth2:
		if c.AuthorizationEndpoint == "" {
			return fmt.Errorf("upstream authorization_endpoint is required for OAuth2 providers")
		}
		if c.TokenEndpoint == "" {
			return fmt.Errorf("upstream token_endpoint is required for OAuth2 providers")
		}
	default:
		return fmt.Errorf("upstream provider type must be '%s' or '%s'", ProviderTypeOIDC, ProviderTypeOAuth2)
	}

	// Common required fields
	if c.ClientID == "" {
		return fmt.Errorf("upstream client_id is required")
	}
	if c.ClientSecret == "" {
		return fmt.Errorf("upstream client_secret is required")
	}
	if c.RedirectURI == "" {
		return fmt.Errorf("upstream redirect_uri is required")
	}
	if err := ValidateRedirectURI(c.RedirectURI); err != nil {
		return fmt.Errorf("upstream %w", err)
	}
	return nil
}

// IsOIDC returns true if the provider type is OIDC.
func (c *Config) IsOIDC() bool {
	return c.Type == ProviderTypeOIDC
}

// IsOAuth2 returns true if the provider type is OAuth2.
func (c *Config) IsOAuth2() bool {
	return c.Type == ProviderTypeOAuth2
}

// ValidateRedirectURI validates an OAuth redirect URI according to RFC 6749 Section 3.1.2.
// It ensures the URI is:
//   - A parseable, absolute URL with scheme and host
//   - Free of fragments (per RFC 6749 Section 3.1.2.2)
//   - Free of user credentials
//   - Using http or https scheme only
//   - Using HTTPS for non-loopback addresses (HTTP allowed only for 127.0.0.1, ::1, localhost)
//   - Not containing wildcard hostnames
func ValidateRedirectURI(uri string) error {
	parsed, err := url.Parse(uri)
	if err != nil {
		return fmt.Errorf("redirect_uri must be an absolute URL with scheme and host")
	}

	// Must be absolute URL (has scheme and host)
	if parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("redirect_uri must be an absolute URL with scheme and host")
	}

	// Must not contain fragment per RFC 6749 Section 3.1.2.2
	if parsed.Fragment != "" {
		return fmt.Errorf("redirect_uri must not contain a fragment (#)")
	}

	// Must not contain user credentials
	if parsed.User != nil {
		return fmt.Errorf("redirect_uri must not contain user credentials")
	}

	// Must use http or https scheme
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("redirect_uri must use http or https scheme")
	}

	// HTTP scheme is only allowed for loopback addresses
	if parsed.Scheme == "http" && !isLoopbackAddress(parsed.Host) {
		return fmt.Errorf("redirect_uri with http scheme requires loopback address (127.0.0.1, ::1, or localhost)")
	}

	// Must not contain wildcard hostname
	if strings.Contains(parsed.Hostname(), "*") {
		return fmt.Errorf("redirect_uri must not contain wildcard hostname")
	}

	return nil
}

// isLoopbackAddress checks if the host is a loopback address (127.0.0.1, ::1, or localhost).
// The host may include a port which is stripped before checking.
func isLoopbackAddress(host string) bool {
	// Handle IPv6 addresses in brackets (e.g., "[::1]:8080")
	hostname := host
	if strings.HasPrefix(host, "[") {
		// IPv6 with brackets, find the closing bracket
		if idx := strings.Index(host, "]"); idx != -1 {
			hostname = host[1:idx]
		}
	} else {
		// Remove port if present for non-bracketed hosts
		if idx := strings.LastIndex(host, ":"); idx != -1 {
			// Make sure this isn't an IPv6 address without brackets
			if !strings.Contains(host, "::") && strings.Count(host, ":") == 1 {
				hostname = host[:idx]
			}
		}
	}

	return hostname == "localhost" || hostname == "127.0.0.1" || hostname == "::1"
}

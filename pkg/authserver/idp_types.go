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

package authserver

import (
	"context"
	"net/http"
	"time"
)

// PKCEChallengeMethodS256 is the PKCE challenge method for SHA-256.
const PKCEChallengeMethodS256 = "S256"

// IDPTokens represents the tokens obtained from an upstream Identity Provider.
type IDPTokens struct {
	// AccessToken is the access token from the upstream IDP.
	AccessToken string

	// RefreshToken is the refresh token from the upstream IDP (if provided).
	RefreshToken string

	// IDToken is the ID token from the upstream IDP (for OIDC).
	IDToken string

	// ExpiresAt is when the access token expires.
	ExpiresAt time.Time

	// Subject is the user identifier from the IDP.
	// This binding field is validated on lookup to prevent cross-session attacks
	// by ensuring the JWT "sub" claim matches this value.
	Subject string

	// ClientID is the OAuth client that initiated the authorization.
	// This binding field is validated on lookup to prevent cross-session attacks
	// by ensuring the JWT "client_id" or "azp" claim matches this value.
	ClientID string
}

// IsExpired returns true if the access token has expired.
func (t *IDPTokens) IsExpired() bool {
	return time.Now().After(t.ExpiresAt)
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

// IDPProvider handles communication with an upstream Identity Provider.
type IDPProvider interface {
	// Name returns the provider name (e.g., "google", "oidc").
	Name() string

	// AuthorizationURL builds the URL to redirect the user to the upstream IDP.
	// state: our internal state to correlate callback
	// codeChallenge: PKCE challenge to send to upstream (if supported)
	// scopes: scopes to request from upstream
	AuthorizationURL(state, codeChallenge string, scopes []string) (string, error)

	// ExchangeCode exchanges an authorization code for tokens with the upstream IDP.
	ExchangeCode(ctx context.Context, code, codeVerifier string) (*IDPTokens, error)

	// RefreshTokens refreshes the upstream IDP tokens.
	RefreshTokens(ctx context.Context, refreshToken string) (*IDPTokens, error)

	// UserInfo fetches user information from the upstream IDP.
	UserInfo(ctx context.Context, accessToken string) (*UserInfo, error)
}

// HTTPClient is an interface for HTTP client operations.
// This allows for mocking in tests.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

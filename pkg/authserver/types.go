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

// Package authserver provides a centralized OAuth Authorization Server
// implementation using ory/fosite for issuing JWTs to clients.
package authserver

import (
	"time"
)

// DefaultPendingAuthorizationTTL is the default TTL for pending authorization requests.
const DefaultPendingAuthorizationTTL = 10 * time.Minute

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

// PendingAuthorization tracks a client's authorization request while they
// authenticate with the upstream IDP.
type PendingAuthorization struct {
	// ClientID is the ID of the OAuth client making the authorization request.
	ClientID string

	// RedirectURI is the client's callback URL where we'll redirect after authentication.
	RedirectURI string

	// State is the client's original state parameter for CSRF protection.
	State string

	// PKCEChallenge is the client's PKCE code challenge.
	PKCEChallenge string

	// PKCEMethod is the PKCE challenge method (must be "S256").
	PKCEMethod string

	// Scopes are the OAuth scopes requested by the client.
	Scopes []string

	// InternalState is our randomly generated state for correlating upstream callback.
	InternalState string

	// CreatedAt is when the pending authorization was created.
	CreatedAt time.Time
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

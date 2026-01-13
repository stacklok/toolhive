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
	"time"
)

// tokenExpirationBuffer is the time buffer before actual expiration to consider a token expired.
// This accounts for clock skew and network latency.
const tokenExpirationBuffer = 30 * time.Second

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
var ErrUserInfoSubjectMismatch = errors.New("userinfo subject does not match expected subject")

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
)

// ProviderType identifies the type of upstream Identity Provider.
type ProviderType string

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

// UserInfoFetcher is implemented by providers that support fetching user information.
// This interface enables extensible UserInfo retrieval from various endpoint types,
// including standard OIDC UserInfo endpoints and custom provider-specific APIs.
//
// Implementations may use different authentication methods, field mappings, and
// response formats as configured via UserInfoConfig.
type UserInfoFetcher interface {
	// FetchUserInfo retrieves user information using the provided access token.
	// Returns the parsed UserInfo on success, or an error if the request fails
	// or the response cannot be parsed.
	FetchUserInfo(ctx context.Context, accessToken string) (*UserInfo, error)
}

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

// Package upstream provides types and implementations for upstream Identity Provider
// communication in the OAuth authorization server.
//
// # Architecture
//
// The package is designed around a core Provider interface that abstracts
// upstream IDP operations. The interface captures essential OAuth/OIDC
// operations without leaking implementation details:
//
//   - AuthorizationURL: Build redirect URL for user authentication
//   - ExchangeCode: Exchange authorization code for tokens
//   - RefreshTokens: Refresh expired tokens
//   - UserInfo: Fetch user claims
//
// # OAuth 2.0 vs OIDC: Protocol Boundaries
//
// This package implements both OAuth 2.0 (RFC 6749) and OpenID Connect (OIDC) functionality.
// Understanding which components belong to which protocol helps when extending the package
// or integrating with providers that support only OAuth 2.0.
//
// ## OAuth 2.0 Generic (works with any OAuth 2.0 provider)
//
// These components implement pure OAuth 2.0 per RFC 6749:
//
//   - Config: Client credentials (client_id, client_secret, redirect_uri, scopes)
//   - ValidateRedirectURI: RFC 6749 Section 3.1.2 redirect URI validation
//   - Authorization Code Flow: response_type=code, state parameter
//   - Token Exchange: authorization_code and refresh_token grants
//   - PKCE (RFC 7636): code_challenge and code_verifier parameters
//   - Token response: access_token, refresh_token, expires_in, token_type
//
// ## OIDC-Specific (requires OIDC-compliant provider)
//
// These components require OIDC Core specification support:
//
//   - ID Tokens: JWT tokens containing user identity claims (OIDC Core Section 2)
//   - IDTokenClaims: Parsed claims including sub, iss, aud, exp, nonce, auth_time
//   - idTokenValidator: Validates ID Tokens per OIDC Core Section 3.1.3.7
//   - Nonce parameter: Replay protection in authorization request (Section 3.1.2.1)
//   - UserInfo endpoint: Fetches user claims (OIDC Core Section 5.3)
//   - Discovery: .well-known/openid-configuration endpoint (OIDC Discovery spec)
//   - Subject validation: UserInfo subject must match ID Token (Section 5.3.4)
//
// ## Provider Types
//
// Use ProviderTypeOIDC for providers that support OIDC discovery (Google, Okta, Azure AD).
// Use ProviderTypeOAuth2 for pure OAuth 2.0 providers with explicit endpoint configuration.
//
// ## Checking Optional Capabilities
//
// Not all providers support all features. Use type assertions to check capabilities:
//
//	// Check if provider supports UserInfo
//	if uip, ok := provider.(upstream.UserInfoProvider); ok {
//	    userInfo, err := uip.UserInfo(ctx, accessToken)
//	}
//
//	// Check if provider supports ID token validation with nonce
//	if validator, ok := provider.(upstream.IDTokenNonceValidator); ok {
//	    claims, err := validator.ValidateIDTokenWithNonce(idToken, nonce)
//	}
//
// # Optional Capability Interfaces
//
// Implementations may also implement optional capability interfaces for
// OIDC-specific features. Consumers should use type assertions to check:
//
//	if validator, ok := provider.(upstream.IDTokenNonceValidator); ok {
//	    claims, err := validator.ValidateIDTokenWithNonce(idToken, nonce)
//	}
//
// Available optional interfaces:
//   - IDTokenNonceValidator: OIDC nonce validation for replay protection
//   - UserInfoSubjectValidator: OIDC subject validation per Section 5.3.4
//
// # Type Hierarchy
//
//	Provider (interface)
//	    |
//	OIDCProvider (implementation)
//	    |-- IDTokenNonceValidator (optional)
//	    +-- UserInfoSubjectValidator (optional)
//
// # Value Objects
//
//   - Tokens: Token response from upstream IDP
//   - UserInfo: User claims from UserInfo endpoint
//   - Config: Configuration for upstream IDP connection
//   - IDTokenClaims: Parsed claims from ID token
//
// # Usage
//
//	config := &upstream.Config{
//	    Issuer:       "https://accounts.google.com",
//	    ClientID:     "your-client-id",
//	    ClientSecret: "your-client-secret",
//	    RedirectURI:  "https://your-app.com/callback",
//	    Scopes:       []string{"openid", "email", "profile"},
//	}
//
//	provider, err := upstream.NewOIDCProvider(ctx, config)
//	if err != nil {
//	    return err
//	}
//
//	// Build authorization URL
//	authURL, err := provider.AuthorizationURL(state, pkceChallenge, nonce)
//
//	// After callback, exchange code for tokens
//	tokens, err := provider.ExchangeCode(ctx, code, pkceVerifier)
//
// # Extensibility
//
// To add a new IDP type (e.g., SAML), implement the Provider interface.
// The optional capability interfaces can be implemented as needed.
package upstream

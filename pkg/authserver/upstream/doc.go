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

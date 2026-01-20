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
// The package is designed around a core OAuth2Provider interface that abstracts
// upstream IDP operations. The interface captures essential OAuth/OIDC
// operations without leaking implementation details:
//
//   - AuthorizationURL: Build redirect URL for user authentication
//   - ExchangeCode: Exchange authorization code for tokens
//   - RefreshTokens: Refresh expired tokens
//   - ResolveIdentity: Resolve user identity from tokens
//   - FetchUserInfo: Fetch user claims
//
// # Type Hierarchy
//
//	OAuth2Provider (interface)
//	    |
//	BaseOAuth2Provider (pure OAuth 2.0 implementation)
//
// Future: OIDCProvider will extend BaseOAuth2Provider with OIDC-specific
// features like ID token validation and nonce verification.
//
// # Value Objects
//
//   - Tokens: Token response from upstream IDP
//   - UserInfo: User claims from UserInfo endpoint
//   - OAuth2Config: Configuration for OAuth 2.0 providers
//   - IDTokenClaims: Parsed claims from ID token (for future OIDC support)
//
// # Usage
//
//	config := &upstream.OAuth2Config{
//	    CommonOAuthConfig: upstream.CommonOAuthConfig{
//	        ClientID:     "your-client-id",
//	        ClientSecret: "your-client-secret",
//	        RedirectURI:  "https://your-app.com/callback",
//	        Scopes:       []string{"read", "write"},
//	    },
//	    AuthorizationEndpoint: "https://provider.com/authorize",
//	    TokenEndpoint:         "https://provider.com/token",
//	    UserInfo: &upstream.UserInfoConfig{
//	        EndpointURL: "https://provider.com/userinfo",
//	    },
//	}
//
//	provider, err := upstream.NewOAuth2Provider(config)
//	if err != nil {
//	    return err
//	}
//
//	// Build authorization URL
//	authURL, err := provider.AuthorizationURL(state, pkceChallenge)
//
//	// After callback, exchange code for tokens
//	tokens, err := provider.ExchangeCode(ctx, code, pkceVerifier)
//
//	// Resolve user identity
//	subject, err := provider.ResolveIdentity(ctx, tokens, "")
//
// # Extensibility
//
// To add a new IDP type (e.g., SAML), implement the OAuth2Provider interface.
//
// # UserInfo Extensibility
//
// The package supports flexible UserInfo fetching through the OAuth2Provider
// interface's FetchUserInfo method and UserInfoConfig. This enables:
//
//   - Custom field mapping for non-standard provider responses
//   - Additional headers for provider-specific requirements
//
// All OAuth2Provider implementations support FetchUserInfo directly:
//
//	userInfo, err := provider.FetchUserInfo(ctx, accessToken)
//
// For custom provider configuration, use UserInfoConfig:
//
//	config := &upstream.UserInfoConfig{
//	    EndpointURL: "https://api.example.com/user",
//	    HTTPMethod:  "GET",  // or "POST" per OIDC Core Section 5.3.1
//	    FieldMapping: &upstream.UserInfoFieldMapping{
//	        SubjectField: "user_id",  // custom field for non-OIDC providers
//	    },
//	}
package upstream

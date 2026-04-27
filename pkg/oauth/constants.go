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

// Package oauth provides RFC-defined types, constants, and grant-request
// helpers shared between OAuth 2.0 and OpenID Connect consumers in this
// repository. It contains two kinds of material:
//
//  1. Protocol-level definitions (well-known discovery paths, grant-type
//     URNs, token-type URNs, discovery document types) used by both clients
//     and servers.
//  2. Grant-request primitives (the shared *http.Client default, NewFormRequest,
//     DoTokenRequest, ParseTokenResponse, *oauth2.RetrieveError construction)
//     consumed by grant packages such as pkg/auth/tokenexchange and future
//     pkg/oauth/jwtbearer.
//
// The grant-request helpers are intentionally NOT routed through
// pkg/networking's SSRF-protected client builder. Doing so would refuse
// loopback and RFC 1918 dials, which would break httptest.NewServer-backed
// tests and localhost-hosted IdPs (dex, Keycloak-in-Docker) that developers
// rely on.
package oauth

import "time"

// Well-known endpoint paths as defined by RFC 8414, OpenID Connect Discovery 1.0, and RFC 9728.
const (
	// WellKnownOIDCPath is the standard OIDC discovery endpoint path
	// per OpenID Connect Discovery 1.0 specification.
	WellKnownOIDCPath = "/.well-known/openid-configuration"

	// WellKnownOAuthServerPath is the standard OAuth authorization server metadata endpoint path
	// per RFC 8414 (OAuth 2.0 Authorization Server Metadata).
	WellKnownOAuthServerPath = "/.well-known/oauth-authorization-server"

	// WellKnownOAuthResourcePath is the RFC 9728 standard path for OAuth Protected Resource metadata.
	// Per RFC 9728 Section 3, this endpoint and any subpaths under it should be accessible
	// without authentication to enable OIDC/OAuth discovery.
	WellKnownOAuthResourcePath = "/.well-known/oauth-protected-resource"
)

// Grant types as defined by RFC 6749.
const (
	// GrantTypeAuthorizationCode is the authorization code grant type (RFC 6749 Section 4.1).
	GrantTypeAuthorizationCode = "authorization_code"

	// GrantTypeRefreshToken is the refresh token grant type (RFC 6749 Section 6).
	GrantTypeRefreshToken = "refresh_token"
)

// Response types as defined by RFC 6749.
const (
	// ResponseTypeCode is the authorization code response type (RFC 6749 Section 4.1.1).
	ResponseTypeCode = "code"
)

// Token endpoint authentication methods as defined by RFC 7591.
const (
	// TokenEndpointAuthMethodNone indicates no client authentication (public clients).
	// Typically used with PKCE for native/mobile applications.
	TokenEndpointAuthMethodNone = "none"
)

// PKCE (Proof Key for Code Exchange) methods as defined by RFC 7636.
const (
	// PKCEMethodS256 uses SHA-256 hash of the code verifier (recommended).
	PKCEMethodS256 = "S256"
)

// Token type URNs as defined by RFC 8693.
//
//nolint:gosec // G101: these are RFC 8693 token-type URN identifiers, not credentials
const (
	// TokenTypeAccessToken indicates an OAuth 2.0 access token (RFC 8693 Section 3).
	TokenTypeAccessToken = "urn:ietf:params:oauth:token-type:access_token"

	// TokenTypeIDToken indicates an OpenID Connect ID Token (RFC 8693 Section 3).
	TokenTypeIDToken = "urn:ietf:params:oauth:token-type:id_token"

	// TokenTypeJWT indicates a JSON Web Token (RFC 8693 Section 3).
	TokenTypeJWT = "urn:ietf:params:oauth:token-type:jwt"
)

// Grant type URNs for token exchange protocols.
//
//nolint:gosec // G101: this is an RFC 8693 grant-type URN identifier, not a credential
const (
	// GrantTypeTokenExchange is the OAuth 2.0 Token Exchange grant type (RFC 8693).
	GrantTypeTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange"
)

// HTTP client and response-handling defaults used by the OAuth grant helpers
// in this package (DoTokenRequest, ParseTokenResponse). Unexported: they are
// implementation defaults shared between grants, not part of the public API.
const (
	defaultHTTPTimeout  = 30 * time.Second
	maxResponseBodySize = 1 << 20 // 1 MiB — matches x/oauth2/internal/token.go.
)

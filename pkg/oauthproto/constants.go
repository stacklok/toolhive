// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauthproto

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

	// TokenEndpointAuthMethodClientSecretBasic sends the client credentials in the
	// HTTP Basic Authorization header at the token endpoint (RFC 6749 Section 2.3.1).
	TokenEndpointAuthMethodClientSecretBasic = "client_secret_basic"

	// TokenEndpointAuthMethodClientSecretPost sends the client credentials in the
	// request body at the token endpoint (RFC 6749 Section 2.3.1).
	TokenEndpointAuthMethodClientSecretPost = "client_secret_post"
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

	// TokenTypeIDJAG indicates an Identity Assertion JWT Authorization Grant (ID-JAG)
	// as defined by draft-ietf-oauth-identity-assertion-authz-grant. This URN is used
	// as the `requested_token_type` in an RFC 8693 token exchange when the IdP is
	// asked to mint an ID-JAG for a target application.
	TokenTypeIDJAG = "urn:ietf:params:oauth:token-type:id-jag"
)

// Grant type URNs for token exchange protocols.
//
//nolint:gosec // G101: this is an RFC 8693 grant-type URN identifier, not a credential
const (
	// GrantTypeTokenExchange is the OAuth 2.0 Token Exchange grant type (RFC 8693).
	GrantTypeTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange"

	// GrantTypeJWTBearer is the JWT Bearer grant type (RFC 7523).
	GrantTypeJWTBearer = "urn:ietf:params:oauth:grant-type:jwt-bearer"
)

// HTTP client constants.
const (
	// UserAgent is the User-Agent header value sent on all HTTP requests
	// originating from this package and its callers.
	UserAgent = "ToolHive/1.0"
)

// HTTP client and response-handling defaults used by the OAuth grant helpers
// in this package (DoTokenRequest, ParseTokenResponse). Unexported: they are
// implementation defaults shared between grants, not part of the public API.
const (
	defaultHTTPTimeout  = 30 * time.Second
	maxResponseBodySize = 1 << 20 // 1 MiB — matches x/oauth2/internal/token.go.
)

// URL scheme constants.
const (
	// schemeHTTPS is the URL scheme required for all OAuth / OIDC endpoints,
	// except when the host is a loopback address (development). Unexported
	// so the check stays internally consistent within this package.
	schemeHTTPS = "https"
)

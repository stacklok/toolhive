// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package oauth provides RFC-defined types and constants for OAuth 2.0 and OpenID Connect.
// This package contains ONLY protocol-level definitions with no business logic.
// It serves as a shared foundation for both OAuth clients (consumers) and servers (producers).
package oauth

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

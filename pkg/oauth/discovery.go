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

package oauth

// AuthorizationServerMetadata represents the OAuth 2.0 Authorization Server Metadata
// per RFC 8414. This is the base structure that OIDC Discovery extends.
type AuthorizationServerMetadata struct {
	// Issuer is the authorization server's issuer identifier (REQUIRED per RFC 8414).
	Issuer string `json:"issuer"`

	// AuthorizationEndpoint is the URL of the authorization endpoint (RECOMMENDED).
	// Note: No omitempty to maintain backward compatibility with existing JSON serialization.
	AuthorizationEndpoint string `json:"authorization_endpoint"`

	// TokenEndpoint is the URL of the token endpoint (RECOMMENDED).
	// Note: No omitempty to maintain backward compatibility with existing JSON serialization.
	TokenEndpoint string `json:"token_endpoint"`

	// JWKSURI is the URL of the JSON Web Key Set document (RECOMMENDED).
	// Note: No omitempty to maintain backward compatibility with existing JSON serialization.
	JWKSURI string `json:"jwks_uri"`

	// RegistrationEndpoint is the URL of the Dynamic Client Registration endpoint (OPTIONAL).
	RegistrationEndpoint string `json:"registration_endpoint,omitempty"`

	// IntrospectionEndpoint is the URL of the token introspection endpoint (OPTIONAL, RFC 7662).
	IntrospectionEndpoint string `json:"introspection_endpoint,omitempty"`

	// UserinfoEndpoint is the URL of the UserInfo endpoint (OPTIONAL, OIDC specific).
	// Note: No omitempty to maintain backward compatibility with existing JSON serialization.
	UserinfoEndpoint string `json:"userinfo_endpoint"`

	// ResponseTypesSupported lists the response types supported (RECOMMENDED).
	ResponseTypesSupported []string `json:"response_types_supported,omitempty"`

	// GrantTypesSupported lists the grant types supported (OPTIONAL).
	GrantTypesSupported []string `json:"grant_types_supported,omitempty"`

	// CodeChallengeMethodsSupported lists the PKCE code challenge methods supported (OPTIONAL).
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported,omitempty"`

	// TokenEndpointAuthMethodsSupported lists the authentication methods supported at the token endpoint (OPTIONAL).
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported,omitempty"`

	// ScopesSupported lists the OAuth 2.0 scope values supported (RECOMMENDED per RFC 8414).
	// For MCP authorization servers, this typically includes "openid" and "offline_access".
	ScopesSupported []string `json:"scopes_supported,omitempty"`
}

// OIDCDiscoveryDocument represents the OpenID Connect Discovery 1.0 document.
// It extends OAuth 2.0 Authorization Server Metadata (RFC 8414) with OIDC-specific fields.
// This unified type supports both producer (server) and consumer (client) use cases.
type OIDCDiscoveryDocument struct {
	// Embed OAuth 2.0 AS Metadata (RFC 8414) as the base
	AuthorizationServerMetadata

	// SubjectTypesSupported lists the subject identifier types supported (REQUIRED for OIDC).
	SubjectTypesSupported []string `json:"subject_types_supported,omitempty"`

	// IDTokenSigningAlgValuesSupported lists the JWS algorithms supported for ID tokens (REQUIRED for OIDC).
	IDTokenSigningAlgValuesSupported []string `json:"id_token_signing_alg_values_supported,omitempty"`

	// ClaimsSupported lists the claims that can be returned (RECOMMENDED for OIDC).
	ClaimsSupported []string `json:"claims_supported,omitempty"`
}

// Validate performs basic validation on the discovery document.
// It checks for required fields based on whether this is an OIDC or pure OAuth document.
func (d *OIDCDiscoveryDocument) Validate(isOIDC bool) error {
	if d.Issuer == "" {
		return ErrMissingIssuer
	}
	if d.AuthorizationEndpoint == "" {
		return ErrMissingAuthorizationEndpoint
	}
	if d.TokenEndpoint == "" {
		return ErrMissingTokenEndpoint
	}
	if isOIDC && d.JWKSURI == "" {
		return ErrMissingJWKSURI
	}
	if isOIDC && len(d.ResponseTypesSupported) == 0 {
		return ErrMissingResponseTypesSupported
	}
	return nil
}

// SupportsPKCE returns true if the authorization server supports PKCE with S256.
func (d *OIDCDiscoveryDocument) SupportsPKCE() bool {
	for _, method := range d.CodeChallengeMethodsSupported {
		if method == PKCEMethodS256 {
			return true
		}
	}
	return false
}

// SupportsGrantType returns true if the authorization server supports the given grant type.
func (d *OIDCDiscoveryDocument) SupportsGrantType(grantType string) bool {
	for _, gt := range d.GrantTypesSupported {
		if gt == grantType {
			return true
		}
	}
	return false
}

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

package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	sharedobauth "github.com/stacklok/toolhive/pkg/oauthproto"
)

// Cache-Control max-age values for discovery endpoints.
// These are not exposed to users but extracted as constants for documentation and maintainability.
const (
	// DefaultJWKSCacheMaxAge is the Cache-Control max-age for the JWKS endpoint (1 hour).
	// This balances caching efficiency with timely key rotation propagation.
	DefaultJWKSCacheMaxAge = 3600

	// DefaultDiscoveryCacheMaxAge is the Cache-Control max-age for the discovery endpoint (1 hour).
	// Aligned with Google's OIDC discovery cache policy.
	DefaultDiscoveryCacheMaxAge = 3600
)

// getSigningAlgorithms extracts the signing algorithms from the JWKS keys.
// If no keys are available, it falls back to RS256 per OIDC Core Section 15.1.
func (h *Handler) getSigningAlgorithms() []string {
	publicJWKS := h.config.PublicJWKS()
	if publicJWKS == nil || len(publicJWKS.Keys) == 0 {
		// Fall back to RS256 per OIDC Core Section 15.1 requirement
		return []string{"RS256"}
	}

	// Collect unique algorithms from keys
	seen := make(map[string]bool)
	var algs []string
	for _, key := range publicJWKS.Keys {
		if key.Algorithm != "" && !seen[key.Algorithm] {
			seen[key.Algorithm] = true
			algs = append(algs, key.Algorithm)
		}
	}

	if len(algs) == 0 {
		// No algorithms found on keys, fall back to RS256
		return []string{"RS256"}
	}

	return algs
}

// JWKSHandler handles GET /.well-known/jwks.json requests.
// It returns the public keys used for verifying JWTs.
func (h *Handler) JWKSHandler(w http.ResponseWriter, _ *http.Request) {
	publicJWKS := h.config.PublicJWKS()
	if publicJWKS == nil {
		slog.Error("no public JWKS available")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data, err := json.Marshal(publicJWKS)
	if err != nil {
		slog.Error("failed to encode JWKS",
			"error", err,
		)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", DefaultJWKSCacheMaxAge))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data) //nolint:gosec // G705: data is JSON-marshaled from internal metadata, not user input
}

// buildOAuthMetadata constructs the authorization-server metadata shared by the
// OAuth AS metadata endpoint and the OIDC discovery endpoint. Token-only
// servers intentionally omit authorization response metadata, including
// response_types_supported, because they do not implement those response types.
// They retain configured scopes because scopes describe permissions available
// through token exchange, not interactive authorization availability. This is
// ToolHive metadata for the supported token flows, not a claim of full RFC 8414
// or OIDC Discovery conformance.
func (h *Handler) buildOAuthMetadata() sharedobauth.AuthorizationServerMetadata {
	issuer := h.config.GetAccessTokenIssuer()

	metadata := sharedobauth.AuthorizationServerMetadata{
		// REQUIRED
		Issuer: issuer,

		// RECOMMENDED
		TokenEndpoint:   issuer + "/oauth/token",
		JWKSURI:         issuer + "/.well-known/jwks.json",
		ScopesSupported: h.config.ScopesSupported,

		// OPTIONAL
		GrantTypesSupported:                        h.grantTypesSupported(),
		TokenEndpointAuthMethodsSupported:          h.tokenEndpointAuthMethodsSupported(),
		TokenEndpointAuthSigningAlgValuesSupported: h.tokenEndpointAuthSigningAlgorithms(),

		// ClientIDMetadataDocumentSupported is defined in the CIMD draft as an
		// OAuth AS metadata field (RFC 8414), not in OIDC Discovery 1.0. It is
		// included here because MCP clients (e.g. VS Code) discover the AS via
		// /.well-known/openid-configuration and need this flag there to activate
		// CIMD. Spec-compliant OIDC consumers silently ignore unknown fields.
		ClientIDMetadataDocumentSupported: h.config.CIMDEnabled,
	}
	if !h.tokenOnly {
		metadata.AuthorizationEndpoint = h.config.GetAuthorizationEndpointBaseURL() + "/oauth/authorize"
		metadata.ResponseTypesSupported = []string{sharedobauth.ResponseTypeCode}
		metadata.CodeChallengeMethodsSupported = []string{crypto.PKCEChallengeMethodS256}
	}
	if !h.tokenOnly || h.config.AllowPrivateKeyJWTRegistration {
		metadata.RegistrationEndpoint = issuer + "/oauth/register"
	}
	return metadata
}

// tokenEndpointAuthMethodsSupported returns the token_endpoint_auth_methods_supported
// list for discovery, derived from config. "none" is always first — the public-client
// default. When confidential DCR is enabled or static delegate clients are configured,
// the two client_secret_* methods are appended. Static clients need these methods even
// though DCR itself remains public-only. RFC 8414 defines no ordering semantics, so
// "none"-first is a readability convention, not a security control.
func (h *Handler) tokenEndpointAuthMethodsSupported() []string {
	methods := []string{sharedobauth.TokenEndpointAuthMethodNone}
	if h.config.AllowConfidentialClientRegistration || h.config.HasStaticDelegateClients {
		methods = append(methods,
			sharedobauth.TokenEndpointAuthMethodClientSecretBasic,
			sharedobauth.TokenEndpointAuthMethodClientSecretPost,
		)
	}
	if h.config.AllowPrivateKeyJWTRegistration {
		methods = append(methods, sharedobauth.TokenEndpointAuthMethodPrivateKeyJWT)
	}
	return methods
}

// tokenEndpointAuthSigningAlgorithms returns the signing algorithms advertised
// for private_key_jwt client assertions. The list is shared with DCR validation
// so discovery cannot claim support for an algorithm registration rejects.
func (h *Handler) tokenEndpointAuthSigningAlgorithms() []string {
	if !h.config.AllowPrivateKeyJWTRegistration {
		return nil
	}
	return registration.SupportedSigningAlgorithms()
}

// grantTypesSupported returns only grant families registered with fosite.
func (h *Handler) grantTypesSupported() []string {
	grantTypes := make([]string, 0, 4)
	if h.config.TokenExchangeEnabled {
		grantTypes = append(grantTypes, sharedobauth.GrantTypeTokenExchange)
	}
	if !h.tokenOnly {
		grantTypes = append(grantTypes,
			string(fosite.GrantTypeAuthorizationCode),
			string(fosite.GrantTypeRefreshToken),
		)
	}
	if h.config.JWTBearerGrantEnabled {
		grantTypes = append(grantTypes, sharedobauth.GrantTypeJWTBearer)
	}
	return grantTypes
}

// OAuthDiscoveryHandler handles GET /.well-known/oauth-authorization-server requests.
// It returns the OAuth 2.0 Authorization Server Metadata per RFC 8414.
// This endpoint is useful for non-OIDC OAuth clients.
func (h *Handler) OAuthDiscoveryHandler(w http.ResponseWriter, _ *http.Request) {
	metadata := h.buildOAuthMetadata()

	data, err := json.Marshal(metadata)
	if err != nil {
		slog.Error("failed to encode OAuth AS metadata",
			"error", err,
		)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", DefaultDiscoveryCacheMaxAge))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data) //nolint:gosec // G705: data is JSON-marshaled from internal metadata, not user input
}

// OIDCDiscoveryHandler handles GET /.well-known/openid-configuration requests.
// With interactive authorization enabled, it returns OIDC discovery metadata
// describing the authorization server capabilities. In token-only mode, the
// endpoint is a ToolHive compatibility alias for key discovery, not a
// standards-conforming OIDC Discovery response: generic MCP and OIDC
// authorization-code clients cannot use it because it omits the authorization
// endpoint and PKCE metadata required to begin an interactive flow.
func (h *Handler) OIDCDiscoveryHandler(w http.ResponseWriter, _ *http.Request) {
	if h.tokenOnly {
		// Token-only servers do not implement OIDC authorization. This ToolHive
		// compatibility alias lets existing key-discovery clients find the token
		// endpoint without advertising unsupported OIDC response types; it is not
		// a standards-conforming OIDC Discovery response.
		data, err := json.Marshal(h.buildOAuthMetadata())
		if err != nil {
			slog.Error("failed to encode token-only discovery document", "error", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", DefaultDiscoveryCacheMaxAge))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(data)
		return
	}

	// Get signing algorithms from the actual JWKS keys
	signingAlgs := h.getSigningAlgorithms()

	discovery := sharedobauth.OIDCDiscoveryDocument{
		// Include all OAuth 2.0 AS Metadata (RFC 8414)
		AuthorizationServerMetadata: h.buildOAuthMetadata(),

		// OIDC-specific REQUIRED fields
		SubjectTypesSupported:            []string{"public"},
		IDTokenSigningAlgValuesSupported: signingAlgs,
	}

	data, err := json.Marshal(discovery)
	if err != nil {
		slog.Error("failed to encode discovery document",
			"error", err,
		)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", DefaultDiscoveryCacheMaxAge))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

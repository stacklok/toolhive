// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/logger"
	sharedobauth "github.com/stacklok/toolhive/pkg/oauth"
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
		logger.Error("no public JWKS available")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data, err := json.Marshal(publicJWKS)
	if err != nil {
		logger.Errorw("failed to encode JWKS",
			"error", err.Error(),
		)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", DefaultJWKSCacheMaxAge))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

// buildOAuthMetadata constructs the base OAuth 2.0 Authorization Server Metadata (RFC 8414).
// This is shared between the OAuth AS metadata endpoint and the OIDC discovery endpoint.
func (h *Handler) buildOAuthMetadata() sharedobauth.AuthorizationServerMetadata {
	issuer := h.config.GetAccessTokenIssuer()

	return sharedobauth.AuthorizationServerMetadata{
		// REQUIRED
		Issuer: issuer,

		// RECOMMENDED
		AuthorizationEndpoint:  issuer + "/oauth/authorize",
		TokenEndpoint:          issuer + "/oauth/token",
		JWKSURI:                issuer + "/.well-known/jwks.json",
		RegistrationEndpoint:   issuer + "/oauth/register",
		ResponseTypesSupported: []string{sharedobauth.ResponseTypeCode},

		// OPTIONAL
		GrantTypesSupported: []string{
			string(fosite.GrantTypeAuthorizationCode),
			string(fosite.GrantTypeRefreshToken),
		},
		CodeChallengeMethodsSupported:     []string{crypto.PKCEChallengeMethodS256},
		TokenEndpointAuthMethodsSupported: []string{sharedobauth.TokenEndpointAuthMethodNone},
	}
}

// OAuthDiscoveryHandler handles GET /.well-known/oauth-authorization-server requests.
// It returns the OAuth 2.0 Authorization Server Metadata per RFC 8414.
// This endpoint is useful for non-OIDC OAuth clients.
func (h *Handler) OAuthDiscoveryHandler(w http.ResponseWriter, _ *http.Request) {
	metadata := h.buildOAuthMetadata()

	data, err := json.Marshal(metadata)
	if err != nil {
		logger.Errorw("failed to encode OAuth AS metadata",
			"error", err.Error(),
		)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", DefaultDiscoveryCacheMaxAge))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

// OIDCDiscoveryHandler handles GET /.well-known/openid-configuration requests.
// It returns the OIDC discovery document describing the authorization server capabilities.
// This extends the OAuth 2.0 AS Metadata (RFC 8414) with OIDC-specific fields.
func (h *Handler) OIDCDiscoveryHandler(w http.ResponseWriter, _ *http.Request) {
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
		logger.Errorw("failed to encode discovery document",
			"error", err.Error(),
		)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", DefaultDiscoveryCacheMaxAge))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

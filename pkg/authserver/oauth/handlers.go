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

import (
	"encoding/json"
	"net/http"

	"github.com/stacklok/toolhive/pkg/logger"
)

// TokenHandler handles POST /oauth/token requests.
// It processes token requests using fosite's access request/response flow.
func (r *Router) TokenHandler(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()

	// Create a new session for the token request
	// Note: clientID is empty here as fosite will populate it from the stored authorize session
	session := NewSession("", "", "")

	// Parse and validate the access request
	accessRequest, err := r.provider.NewAccessRequest(ctx, req, session)
	if err != nil {
		logger.Errorw("failed to create access request",
			"error", err.Error(),
		)
		r.provider.WriteAccessError(ctx, w, accessRequest, err)
		return
	}

	// RFC 8707: Handle resource parameter for audience claim
	if resource := accessRequest.GetRequestForm().Get("resource"); resource != "" {
		logger.Debugw("granting audience from resource parameter",
			"resource", resource,
		)
		accessRequest.GrantAudience(resource)
	}

	// Generate the access response (tokens)
	response, err := r.provider.NewAccessResponse(ctx, accessRequest)
	if err != nil {
		logger.Errorw("failed to create access response",
			"error", err.Error(),
		)
		r.provider.WriteAccessError(ctx, w, accessRequest, err)
		return
	}

	// Write the token response
	r.provider.WriteAccessResponse(ctx, w, accessRequest, response)
}

// JWKSHandler handles GET /.well-known/jwks.json requests.
// It returns the public keys used for verifying JWTs.
func (r *Router) JWKSHandler(w http.ResponseWriter, _ *http.Request) {
	publicJWKS := r.config.PublicJWKS()
	if publicJWKS == nil {
		logger.Error("no public JWKS available")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=3600")

	if err := json.NewEncoder(w).Encode(publicJWKS); err != nil {
		logger.Errorw("failed to encode JWKS",
			"error", err.Error(),
		)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
}

// OIDCDiscoveryDocument represents the OIDC discovery document structure.
type OIDCDiscoveryDocument struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint,omitempty"`
	JWKSURI                           string   `json:"jwks_uri"`
	ResponseTypesSupported            []string `json:"response_types_supported"`
	GrantTypesSupported               []string `json:"grant_types_supported"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
}

// OIDCDiscoveryHandler handles GET /.well-known/openid-configuration requests.
// It returns the OIDC discovery document describing the authorization server capabilities.
func (r *Router) OIDCDiscoveryHandler(w http.ResponseWriter, _ *http.Request) {
	issuer := r.config.AccessTokenIssuer

	discovery := OIDCDiscoveryDocument{
		Issuer:                            issuer,
		AuthorizationEndpoint:             issuer + "/oauth/authorize",
		TokenEndpoint:                     issuer + "/oauth/token",
		RegistrationEndpoint:              issuer + "/oauth2/register",
		JWKSURI:                           issuer + "/.well-known/jwks.json",
		ResponseTypesSupported:            []string{"code"},
		GrantTypesSupported:               []string{"authorization_code", "refresh_token"},
		CodeChallengeMethodsSupported:     []string{"S256"},
		TokenEndpointAuthMethodsSupported: []string{"none"},
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=3600")

	if err := json.NewEncoder(w).Encode(discovery); err != nil {
		logger.Errorw("failed to encode discovery document",
			"error", err.Error(),
		)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
}

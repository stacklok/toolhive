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
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/ory/fosite"
)

// DCR error codes per RFC 7591 Section 3.2.2
const (
	// DCRErrorInvalidRedirectURI indicates that the value of one or more
	// redirect_uris is invalid.
	DCRErrorInvalidRedirectURI = "invalid_redirect_uri"

	// DCRErrorInvalidClientMetadata indicates that the value of one of the
	// client metadata fields is invalid and the server has rejected this request.
	DCRErrorInvalidClientMetadata = "invalid_client_metadata"
)

// DCRRequest represents an OAuth 2.0 Dynamic Client Registration request
// per RFC 7591 Section 2.
type DCRRequest struct {
	// RedirectURIs is an array of redirection URIs for the client.
	// Required for public clients.
	RedirectURIs []string `json:"redirect_uris"`

	// ClientName is a human-readable name for the client.
	ClientName string `json:"client_name,omitempty"`

	// TokenEndpointAuthMethod is the requested authentication method for the token endpoint.
	// For public clients, this must be "none".
	TokenEndpointAuthMethod string `json:"token_endpoint_auth_method,omitempty"`

	// GrantTypes is an array of OAuth 2.0 grant types the client may use.
	// Defaults to ["authorization_code"] if not specified.
	GrantTypes []string `json:"grant_types,omitempty"`

	// ResponseTypes is an array of OAuth 2.0 response types the client may use.
	// Defaults to ["code"] if not specified.
	ResponseTypes []string `json:"response_types,omitempty"`
}

// DCRResponse represents a successful OAuth 2.0 Dynamic Client Registration
// response per RFC 7591 Section 3.2.1.
type DCRResponse struct {
	// ClientID is the unique identifier for the client.
	ClientID string `json:"client_id"`

	// ClientIDIssuedAt is the time at which the client identifier was issued,
	// as a Unix timestamp.
	ClientIDIssuedAt int64 `json:"client_id_issued_at,omitempty"`

	// RedirectURIs is an array of redirection URIs for the client.
	RedirectURIs []string `json:"redirect_uris"`

	// ClientName is a human-readable name for the client.
	ClientName string `json:"client_name,omitempty"`

	// TokenEndpointAuthMethod is the authentication method for the token endpoint.
	TokenEndpointAuthMethod string `json:"token_endpoint_auth_method"`

	// GrantTypes is an array of OAuth 2.0 grant types the client may use.
	GrantTypes []string `json:"grant_types"`

	// ResponseTypes is an array of OAuth 2.0 response types the client may use.
	ResponseTypes []string `json:"response_types"`
}

// DCRError represents an OAuth 2.0 Dynamic Client Registration error
// response per RFC 7591 Section 3.2.2.
type DCRError struct {
	// Error is a single ASCII error code from the defined set.
	Error string `json:"error"`

	// ErrorDescription is a human-readable text providing additional information.
	ErrorDescription string `json:"error_description,omitempty"`
}

// DefaultGrantTypes are the default grant types for registered clients.
var DefaultGrantTypes = []string{"authorization_code", "refresh_token"}

// DefaultResponseTypes are the default response types for registered clients.
var DefaultResponseTypes = []string{"code"}

// ValidateDCRRequest validates a DCR request according to RFC 7591
// and the server's security policy (loopback-only public clients).
// Returns the validated request with defaults applied, or an error.
func ValidateDCRRequest(req *DCRRequest) (*DCRRequest, *DCRError) {
	// 1. Validate redirect_uris - required
	if len(req.RedirectURIs) == 0 {
		return nil, &DCRError{
			Error:            DCRErrorInvalidRedirectURI,
			ErrorDescription: "redirect_uris is required",
		}
	}

	// 2. Validate all redirect_uris per RFC 8252
	for _, uri := range req.RedirectURIs {
		if err := validateRedirectURI(uri); err != nil {
			return nil, err
		}
	}

	// 3. Validate/default token_endpoint_auth_method
	authMethod := req.TokenEndpointAuthMethod
	if authMethod == "" {
		authMethod = "none"
	}
	if authMethod != "none" {
		return nil, &DCRError{
			Error:            DCRErrorInvalidClientMetadata,
			ErrorDescription: "token_endpoint_auth_method must be 'none' for public clients",
		}
	}

	// 4. Validate/default grant_types
	grantTypes := req.GrantTypes
	if len(grantTypes) == 0 {
		grantTypes = DefaultGrantTypes
	}
	if !slices.Contains(grantTypes, "authorization_code") {
		return nil, &DCRError{
			Error:            DCRErrorInvalidClientMetadata,
			ErrorDescription: "grant_types must include 'authorization_code'",
		}
	}

	// 5. Validate/default response_types
	responseTypes := req.ResponseTypes
	if len(responseTypes) == 0 {
		responseTypes = DefaultResponseTypes
	}
	if !slices.Contains(responseTypes, "code") {
		return nil, &DCRError{
			Error:            DCRErrorInvalidClientMetadata,
			ErrorDescription: "response_types must include 'code'",
		}
	}

	// Return validated request with defaults applied
	return &DCRRequest{
		RedirectURIs:            req.RedirectURIs,
		ClientName:              req.ClientName,
		TokenEndpointAuthMethod: authMethod,
		GrantTypes:              grantTypes,
		ResponseTypes:           responseTypes,
	}, nil
}

// validateRedirectURI validates a redirect URI per RFC 8252:
// - HTTPS is allowed for any address (web-based redirects)
// - HTTP is only allowed for loopback addresses (127.0.0.1, [::1], localhost)
func validateRedirectURI(uri string) *DCRError {
	parsed, err := url.Parse(uri)
	if err != nil {
		return &DCRError{
			Error:            DCRErrorInvalidRedirectURI,
			ErrorDescription: "invalid redirect_uri format",
		}
	}

	scheme := parsed.Scheme
	hostname := parsed.Hostname()

	// HTTPS is allowed for any address (secure web-based redirects)
	if scheme == schemeHTTPS {
		return nil
	}

	// HTTP is only allowed for loopback addresses per RFC 8252 Section 7.3
	if scheme == schemeHTTP {
		if IsLoopbackHost(hostname) {
			return nil
		}
		return &DCRError{
			Error:            DCRErrorInvalidRedirectURI,
			ErrorDescription: "redirect_uri with http scheme must use loopback address (127.0.0.1, [::1], or localhost)",
		}
	}

	// Any other scheme is not allowed
	return &DCRError{
		Error:            DCRErrorInvalidRedirectURI,
		ErrorDescription: "redirect_uri must use http (for loopback) or https scheme",
	}
}

// RegisterClientHandler handles POST /oauth2/register requests.
// It implements RFC 7591 Dynamic Client Registration for public clients
// with loopback redirect URIs only.
func (r *Router) RegisterClientHandler(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()

	// Parse request body
	var dcrReq DCRRequest
	if err := json.NewDecoder(req.Body).Decode(&dcrReq); err != nil {
		r.writeDCRError(w, http.StatusBadRequest, &DCRError{
			Error:            DCRErrorInvalidClientMetadata,
			ErrorDescription: "invalid JSON request body",
		})
		return
	}

	// Validate request
	validated, dcrErr := ValidateDCRRequest(&dcrReq)
	if dcrErr != nil {
		r.writeDCRError(w, http.StatusBadRequest, dcrErr)
		return
	}

	// Generate client ID
	clientID := uuid.NewString()

	// Create fosite client
	defaultClient := &fosite.DefaultClient{
		ID:            clientID,
		RedirectURIs:  validated.RedirectURIs,
		ResponseTypes: validated.ResponseTypes,
		GrantTypes:    validated.GrantTypes,
		Scopes:        []string{"openid", "profile", "email"},
		Public:        true,
	}

	// Wrap in LoopbackClient for RFC 8252 Section 7.3 dynamic port matching
	client := NewLoopbackClient(defaultClient)

	// Register client
	r.storage.RegisterClient(client)

	r.logger.InfoContext(ctx, "registered new DCR client",
		slog.String("client_id", clientID),
		slog.String("client_name", validated.ClientName),
	)

	// Build response
	response := DCRResponse{
		ClientID:                clientID,
		ClientIDIssuedAt:        time.Now().Unix(),
		RedirectURIs:            validated.RedirectURIs,
		ClientName:              validated.ClientName,
		TokenEndpointAuthMethod: validated.TokenEndpointAuthMethod,
		GrantTypes:              validated.GrantTypes,
		ResponseTypes:           validated.ResponseTypes,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		r.logger.ErrorContext(ctx, "failed to encode DCR response",
			slog.String("error", err.Error()),
		)
	}
}

// writeDCRError writes a DCR error response per RFC 7591 Section 3.2.2.
func (*Router) writeDCRError(w http.ResponseWriter, statusCode int, dcrErr *DCRError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(dcrErr)
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package registration provides OAuth 2.0 Dynamic Client Registration (DCR)
// functionality per RFC 7591, including request validation and secure redirect
// URI handling for public native clients.
package registration

import (
	"context"
	"net/url"
	"slices"

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

// Validation limits to prevent DoS attacks via excessively large requests.
const (
	// MaxRedirectURILength is the maximum allowed length for a single redirect URI.
	MaxRedirectURILength = 2048

	// MaxRedirectURICount is the maximum number of redirect URIs allowed per client.
	MaxRedirectURICount = 10
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

	// 2. Validate redirect_uris count limit
	if len(req.RedirectURIs) > MaxRedirectURICount {
		return nil, &DCRError{
			Error:            DCRErrorInvalidRedirectURI,
			ErrorDescription: "too many redirect_uris (maximum 10)",
		}
	}

	// 3. Validate all redirect_uris per RFC 8252
	for _, uri := range req.RedirectURIs {
		if err := ValidateRedirectURI(uri); err != nil {
			return nil, err
		}
	}

	// 4. Validate/default token_endpoint_auth_method
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

	// 5. Validate/default grant_types
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

	// 6. Validate/default response_types
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

// ValidateRedirectURI validates a redirect URI per RFC 8252:
// - HTTPS is allowed for any address (web-based redirects)
// - HTTP is only allowed for loopback addresses (127.0.0.1, [::1], localhost)
func ValidateRedirectURI(uri string) *DCRError {
	// Check length limit before parsing (DoS protection - fosite doesn't have this)
	if len(uri) > MaxRedirectURILength {
		return &DCRError{
			Error:            DCRErrorInvalidRedirectURI,
			ErrorDescription: "redirect_uri too long (maximum 2048 characters)",
		}
	}

	parsed, err := url.Parse(uri)
	if err != nil {
		return &DCRError{
			Error:            DCRErrorInvalidRedirectURI,
			ErrorDescription: "invalid redirect_uri format",
		}
	}

	// Delegate to fosite for RFC 6749 Section 3.1.2 validation:
	// - URI must be absolute (have a scheme)
	// - URI must not have a fragment component
	if !fosite.IsValidRedirectURI(parsed) {
		return &DCRError{
			Error:            DCRErrorInvalidRedirectURI,
			ErrorDescription: "redirect_uri must be an absolute URI without a fragment",
		}
	}

	// Delegate to fosite for scheme security check per RFC 8252:
	// - HTTPS is allowed for any address
	// - HTTP is only allowed for loopback addresses (127.0.0.1, [::1], localhost, *.localhost)
	if !fosite.IsRedirectURISecureStrict(context.Background(), parsed) {
		return &DCRError{
			Error:            DCRErrorInvalidRedirectURI,
			ErrorDescription: "redirect_uri must use http (for loopback) or https scheme",
		}
	}

	return nil
}

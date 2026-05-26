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

// Package registration provides OAuth 2.0 Dynamic Client Registration (DCR)
// functionality per RFC 7591, including request validation and secure redirect
// URI handling for public native clients.
package registration

import (
	"fmt"
	"slices"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// ValidateScopeSubset checks that every scope in subset is also present in
// superset, returning an error that names fieldName and the offending scope.
//
// Shared across the layers that validate baseline-scope configuration so the
// error message format is identical wherever the violation is caught (a
// caller using YAML-loaded config and a caller constructing config
// programmatically both see the same wording).
//
// fieldName should be the wire-format or display name of the field being
// validated (e.g. "baseline_client_scopes"). It is embedded verbatim in the
// returned error.
func ValidateScopeSubset(subset, superset []string, fieldName string) error {
	if len(subset) == 0 {
		return nil
	}
	supported := make(map[string]bool, len(superset))
	for _, s := range superset {
		supported[s] = true
	}
	for _, s := range subset {
		if !supported[s] {
			return fmt.Errorf("%s contains %q which is not in scopes_supported", fieldName, s)
		}
	}
	return nil
}

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
	// MaxRedirectURICount is the maximum number of redirect URIs allowed per client.
	MaxRedirectURICount = 10

	// MaxClientNameLength is the maximum allowed length for a client name.
	MaxClientNameLength = 256

	// MaxSoftwareIDLength is the maximum allowed length for a software_id
	// value. RFC 7591 does not mandate an upper bound, so we reuse the
	// client_name cap for consistency — a software_id is a similar-purpose
	// human-oriented identifier and the same ballpark DoS concerns apply.
	MaxSoftwareIDLength = 256
)

// DCRError represents an OAuth 2.0 Dynamic Client Registration error
// response per RFC 7591 Section 3.2.2.
type DCRError struct {
	// Error is a single ASCII error code from the defined set.
	Error string `json:"error"`

	// ErrorDescription is a human-readable text providing additional information.
	ErrorDescription string `json:"error_description,omitempty"`
}

// defaultGrantTypes are the default grant types for registered clients.
var defaultGrantTypes = []string{"authorization_code", "refresh_token"}

// allowedGrantTypes defines the grant types permitted for public clients.
var allowedGrantTypes = map[string]bool{
	"authorization_code": true,
	"refresh_token":      true,
}

// defaultResponseTypes are the default response types for registered clients.
var defaultResponseTypes = []string{"code"}

// allowedResponseTypes defines the response types permitted for public clients.
var allowedResponseTypes = map[string]bool{
	"code": true,
}

// ValidateDCRRequest validates a DCR request according to RFC 7591
// and the server's security policy (loopback-only public clients).
// Returns the validated request with defaults applied, or an error.
//
// The validated request does NOT carry the requested scopes — scope
// validation against the server's supported set is a separate step,
// handled by ValidateScopes using the caller's policy inputs.
func ValidateDCRRequest(
	req *oauthproto.DynamicClientRegistrationRequest,
) (*oauthproto.DynamicClientRegistrationRequest, *DCRError) {
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

	// 4. Validate client_name length
	if len(req.ClientName) > MaxClientNameLength {
		return nil, &DCRError{
			Error:            DCRErrorInvalidClientMetadata,
			ErrorDescription: "client_name too long (maximum 256 characters)",
		}
	}

	// 4a. Validate software_id: length cap + printable-ASCII charset.
	// RFC 7591 does not mandate an upper bound or a character class for
	// software_id, but since we capture the value in audit logs we want a
	// predictable shape and a hard cap against DoS — a caller sending
	// multi-MB strings would slip past only the 64 KiB body cap otherwise.
	if dcrErr := validateSoftwareID(req.SoftwareID); dcrErr != nil {
		return nil, dcrErr
	}

	// 5. Validate/default token_endpoint_auth_method
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

	// 6. Validate/default grant_types
	grantTypes, err := validateGrantTypes(req.GrantTypes)
	if err != nil {
		return nil, err
	}

	// 7. Validate/default response_types
	responseTypes, err := validateResponseTypes(req.ResponseTypes)
	if err != nil {
		return nil, err
	}

	// Return validated request with defaults applied
	return &oauthproto.DynamicClientRegistrationRequest{
		RedirectURIs:            req.RedirectURIs,
		ClientName:              req.ClientName,
		TokenEndpointAuthMethod: authMethod,
		GrantTypes:              grantTypes,
		ResponseTypes:           responseTypes,
		SoftwareID:              req.SoftwareID,
	}, nil
}

// validateSoftwareID enforces the length cap and printable-ASCII charset
// for a DCR request's software_id. An empty value is accepted (the field
// is optional per RFC 7591).
func validateSoftwareID(softwareID string) *DCRError {
	if softwareID == "" {
		return nil
	}
	if len(softwareID) > MaxSoftwareIDLength {
		return &DCRError{
			Error:            DCRErrorInvalidClientMetadata,
			ErrorDescription: "software_id too long (maximum 256 characters)",
		}
	}
	// Allow printable ASCII (0x20..0x7E) only. Control characters and
	// non-ASCII input are rejected: we log this value and want a
	// predictable on-disk representation regardless of handler choice.
	for i := 0; i < len(softwareID); i++ {
		c := softwareID[i]
		if c < 0x20 || c > 0x7E {
			return &DCRError{
				Error:            DCRErrorInvalidClientMetadata,
				ErrorDescription: "software_id must contain only printable ASCII characters",
			}
		}
	}
	return nil
}

func validateGrantTypes(grantTypes []string) ([]string, *DCRError) {
	if len(grantTypes) == 0 {
		grantTypes = defaultGrantTypes
	}
	// Require authorization_code explicitly - provides a clearer error for the
	// "refresh_token only" case that would otherwise pass the allowlist.
	if !slices.Contains(grantTypes, "authorization_code") {
		return nil, &DCRError{
			Error:            DCRErrorInvalidClientMetadata,
			ErrorDescription: "grant_types must include 'authorization_code'",
		}
	}
	for _, gt := range grantTypes {
		if !allowedGrantTypes[gt] {
			return nil, &DCRError{
				Error:            DCRErrorInvalidClientMetadata,
				ErrorDescription: "unsupported grant_type: " + gt,
			}
		}
	}
	return grantTypes, nil
}

func validateResponseTypes(responseTypes []string) ([]string, *DCRError) {
	if len(responseTypes) == 0 {
		responseTypes = defaultResponseTypes
	}
	// Require "code" explicitly - purely defense-in-depth since the allowlist
	// currently only contains "code", but provides a clearer error message.
	if !slices.Contains(responseTypes, "code") {
		return nil, &DCRError{
			Error:            DCRErrorInvalidClientMetadata,
			ErrorDescription: "response_types must include 'code'",
		}
	}
	for _, rt := range responseTypes {
		if !allowedResponseTypes[rt] {
			return nil, &DCRError{
				Error:            DCRErrorInvalidClientMetadata,
				ErrorDescription: "unsupported response_type: " + rt,
			}
		}
	}
	return responseTypes, nil
}

// ValidateRedirectURI validates a redirect URI per RFC 8252:
// - HTTPS is allowed for any address (web-based redirects)
// - HTTP is only allowed for loopback addresses (127.0.0.1, [::1], localhost)
// - Private-use URI schemes (e.g., cursor://, vscode://) are allowed for native apps
func ValidateRedirectURI(uri string) *DCRError {
	if err := oauthproto.ValidateRedirectURI(uri, oauthproto.RedirectURIPolicyAllowPrivateSchemes); err != nil {
		return &DCRError{
			Error:            DCRErrorInvalidRedirectURI,
			ErrorDescription: err.Error(),
		}
	}
	return nil
}

// ValidateScopes validates a slice of already-parsed scope tokens against
// the server's allowed set per RFC 7591 §2.
//
//   - Empty/nil input falls back to DefaultScopes (which must itself be a
//     subset of allowedScopes; otherwise the call returns an error).
//   - Each requested scope must appear in allowedScopes; otherwise returns
//     invalid_client_metadata.
//   - Duplicates in the input are tolerated and deduplicated per RFC 6749
//     §3.3 (scope is a set of case-sensitive strings).
func ValidateScopes(requestedScopes, allowedScopes []string) ([]string, *DCRError) {
	// Build allowed scope set for O(1) lookup
	allowed := make(map[string]bool, len(allowedScopes))
	for _, s := range allowedScopes {
		allowed[s] = true
	}

	// Deduplicate while validating each requested scope against the
	// allowed set.
	var scopes []string
	if len(requestedScopes) > 0 {
		seen := make(map[string]bool)
		for _, s := range requestedScopes {
			if !allowed[s] {
				return nil, &DCRError{
					Error:            DCRErrorInvalidClientMetadata,
					ErrorDescription: "unsupported scope: " + s,
				}
			}
			if !seen[s] {
				seen[s] = true
				scopes = append(scopes, s)
			}
		}
	}

	// If no scopes requested, use defaults validated against allowed scopes
	if len(scopes) == 0 {
		for _, s := range DefaultScopes {
			if !allowed[s] {
				return nil, &DCRError{
					Error:            DCRErrorInvalidClientMetadata,
					ErrorDescription: "default scope not supported by server: " + s,
				}
			}
		}
		return DefaultScopes, nil
	}

	return scopes, nil
}

// UnionScopes returns the union of requested and baseline scopes, preserving
// the order of requested first, then appending any baseline scopes not already
// present. Duplicates are removed. Returns nil when the result is empty.
//
// Both inputs must already be validated by the caller. UnionScopes does not
// filter empty strings or validate scope syntax — it only deduplicates and
// merges in stable order.
func UnionScopes(requested, baseline []string) []string {
	seen := make(map[string]bool, len(requested)+len(baseline))
	out := make([]string, 0, len(requested)+len(baseline))
	for _, s := range requested {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, s := range baseline {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

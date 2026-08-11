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
	"strings"

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
// and the server's security policy. When allowConfidential is false the
// policy is unchanged from the historical default: public clients only.
// When true, token_endpoint_auth_method may also be client_secret_basic or
// client_secret_post; such confidential registrations are additionally
// restricted to https non-loopback redirect URIs, because a client on a
// loopback or private-scheme URI is by construction a public client
// (OAuth 2.1 §2.1) and minting it a secret ships that secret inside a
// distributed binary.
// Returns the validated request with defaults applied, or an error.
//
// The validated request does NOT carry the requested scopes — scope
// validation against the server's supported set is a separate step,
// handled by ValidateScopes using the caller's policy inputs.
func ValidateDCRRequest(
	req *oauthproto.DynamicClientRegistrationRequest,
	allowConfidential bool,
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
	authMethod, dcrErr := validateAuthMethod(req.TokenEndpointAuthMethod, req.RedirectURIs, allowConfidential)
	if dcrErr != nil {
		return nil, dcrErr
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

// validateAuthMethod validates token_endpoint_auth_method and returns the
// effective method with the empty default applied. When allowConfidential is
// false the policy is unchanged from the historical default: public clients
// only. When true, the client_secret_* methods are accepted but restricted to
// https non-loopback redirect URIs, because a client on a loopback or
// private-scheme URI is by construction a public client (OAuth 2.1 §2.1) and
// minting it a secret ships that secret inside a distributed binary.
func validateAuthMethod(
	authMethod string,
	redirectURIs []string,
	allowConfidential bool,
) (string, *DCRError) {
	if authMethod == "" {
		// RFC 7591 §2 says an omitted token_endpoint_auth_method defaults to
		// "client_secret_basic". We deliberately default to "none" instead.
		// That RFC default predates PKCE-based public clients becoming the
		// norm for native and CLI apps, which are most MCP clients today.
		// Following it here would silently turn any public client that
		// omits the field into a confidential one — and fosite pins the
		// auth method at registration, so that client's first token request
		// would fail with invalid_client because it has no secret and never
		// asked for one. It also wouldn't help the clients confidential
		// support was added for: the known cases send "none" explicitly
		// rather than omitting the field, and an explicit value always
		// overrides the default.
		authMethod = oauthproto.TokenEndpointAuthMethodNone
	}
	switch authMethod {
	case oauthproto.TokenEndpointAuthMethodNone:
		// Always accepted; the public-client default.
		return authMethod, nil
	case oauthproto.TokenEndpointAuthMethodClientSecretBasic,
		oauthproto.TokenEndpointAuthMethodClientSecretPost:
		if !allowConfidential {
			return "", &DCRError{
				Error:            DCRErrorInvalidClientMetadata,
				ErrorDescription: "this authorization server only supports token_endpoint_auth_method 'none'",
			}
		}
		// Server policy (not a spec mandate): confidential registrations must
		// use https non-loopback redirect URIs. A client reachable on loopback
		// or a private scheme is typically a distributed native app that cannot
		// keep a secret, so this server declines to mint it one.
		if dcrErr := ValidateConfidentialRedirectURIs(redirectURIs, authMethod); dcrErr != nil {
			return "", dcrErr
		}
		return authMethod, nil
	default:
		return "", &DCRError{
			Error:            DCRErrorInvalidClientMetadata,
			ErrorDescription: "unsupported token_endpoint_auth_method: " + authMethod,
		}
	}
}

// ValidateConfidentialRedirectURIs checks that every entry in redirectURIs
// meets the confidential-client policy: https non-loopback, RFC 8252 strict
// scheme rules. authMethod is embedded in the loopback-rejection message and
// should be the auth method the client is being registered with.
//
// Shared by the ordinary confidential registration path (validateAuthMethod)
// and the force-confidential override (resolveForceConfidentialOverride in
// pkg/authserver/server/handlers/dcr.go), so a registration cannot become
// confidential by either path while carrying a loopback redirect_uri.
func ValidateConfidentialRedirectURIs(redirectURIs []string, authMethod string) *DCRError {
	for _, uri := range redirectURIs {
		if err := oauthproto.ValidateRedirectURI(uri, oauthproto.RedirectURIPolicyStrict); err != nil {
			return &DCRError{
				Error:            DCRErrorInvalidRedirectURI,
				ErrorDescription: err.Error(),
			}
		}
		// isLoopbackURI is a literal-string check (127.0.0.0/8, ::1,
		// ::ffff:127.0.0.1, localhost); it does not resolve the hostname,
		// so a registrant-controlled name that merely resolves to a
		// loopback address slips through. This is acceptable: the
		// registrant controls both its own redirect URI and its own DNS,
		// so at worst this lets an actor mint a secret for its own
		// loopback app, not attack another tenant. Resolving DNS at
		// registration time would add an unreliable (TTL/rebinding) and
		// unnecessary network dependency to a request that should stay
		// purely local.
		if isLoopbackURI(uri) {
			return &DCRError{
				Error: DCRErrorInvalidRedirectURI,
				ErrorDescription: "token_endpoint_auth_method '" + authMethod +
					"' requires https non-loopback redirect_uris; native and loopback clients must use 'none'" +
					" (offending redirect_uri: " + uri + ")",
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
//   - Empty/nil input falls back to the intersection of DefaultScopes with
//     allowedScopes (RFC 7591 §2 permits the server to register a client
//     with a default set of scopes). Defaults the server does not support
//     are returned in droppedDefaults so callers can surface them; only an
//     empty intersection is rejected, because no sensible default exists
//     and the client must declare scope explicitly.
//   - Each requested scope must appear in allowedScopes; otherwise returns
//     invalid_client_metadata.
//   - Duplicates in the input are tolerated and deduplicated per RFC 6749
//     §3.3 (scope is a set of case-sensitive strings).
//
// droppedDefaults is non-nil only on the default-fallback path; requested
// scopes are validated strictly and never dropped.
func ValidateScopes(requestedScopes, allowedScopes []string) (scopes, droppedDefaults []string, dcrErr *DCRError) {
	// Build allowed scope set for O(1) lookup
	allowed := make(map[string]bool, len(allowedScopes))
	for _, s := range allowedScopes {
		allowed[s] = true
	}

	// Deduplicate while validating each requested scope against the
	// allowed set.
	if len(requestedScopes) > 0 {
		seen := make(map[string]bool)
		for _, s := range requestedScopes {
			if !allowed[s] {
				return nil, nil, &DCRError{
					Error:            DCRErrorInvalidClientMetadata,
					ErrorDescription: "unsupported scope: " + s,
				}
			}
			if !seen[s] {
				seen[s] = true
				scopes = append(scopes, s)
			}
		}
		return scopes, nil, nil
	}

	// No scopes requested: fall back to the intersection of DefaultScopes
	// with the allowed set. Requiring every default to be supported would
	// reject any client that omits scope against a server whose
	// scopes_supported does not carry the full default set, even though a
	// perfectly usable subset exists (see issue #6186).
	for _, s := range DefaultScopes {
		if allowed[s] {
			scopes = append(scopes, s)
		} else {
			droppedDefaults = append(droppedDefaults, s)
		}
	}
	if len(scopes) == 0 {
		return nil, nil, &DCRError{
			Error: DCRErrorInvalidClientMetadata,
			ErrorDescription: "none of the default scopes (" + strings.Join(DefaultScopes, " ") +
				") are supported by this server; the client must declare scope explicitly",
		}
	}
	return scopes, droppedDefaults, nil
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

// ValidatePublicGrantTypes validates the grant_types for a public OAuth client,
// applying the same rules as DCR: authorization_code must be present, and all
// declared values must be in the allowed set. Returns the validated slice (with
// defaults applied when nil/empty) or a *DCRError on violation.
func ValidatePublicGrantTypes(grantTypes []string) ([]string, *DCRError) {
	return validateGrantTypes(grantTypes)
}

// ValidatePublicResponseTypes validates the response_types for a public OAuth
// client, applying the same rules as DCR: code must be present and all declared
// values must be in the allowed set. Returns the validated slice (with defaults
// applied when nil/empty) or a *DCRError on violation.
func ValidatePublicResponseTypes(responseTypes []string) ([]string, *DCRError) {
	return validateResponseTypes(responseTypes)
}

// FilterPublicGrantTypes returns the subset of grantTypes this server supports
// for public clients, dropping unsupported entries instead of rejecting the
// whole set the way ValidatePublicGrantTypes does.
//
// This is the right semantics for CIMD: a Client ID Metadata Document
// describes the client's capabilities across every authorization server it
// talks to, and the client cannot tailor it per server — VS Code, for
// example, declares the device_code grant alongside authorization_code, and
// rejecting the document over a grant type this flow never uses breaks the
// client entirely. A DCR request, by contrast, is addressed to this server
// specifically, so ValidatePublicGrantTypes' rejection remains the correct
// feedback on that path.
//
// The surviving set must still include "authorization_code": a client whose
// supported grant types do not intersect with the only redemption flow this
// server offers cannot function against it, and a clear error beats a client
// that registers and then fails every token request. nil/empty input gets the
// same defaults as DCR.
func FilterPublicGrantTypes(grantTypes []string) ([]string, *DCRError) {
	// Clone so callers that store the result (e.g. in a cached fosite client)
	// never alias the package-level default.
	if len(grantTypes) == 0 {
		return slices.Clone(defaultGrantTypes), nil
	}
	filtered := make([]string, 0, len(grantTypes))
	for _, gt := range grantTypes {
		if allowedGrantTypes[gt] {
			filtered = append(filtered, gt)
		}
	}
	if !slices.Contains(filtered, "authorization_code") {
		return nil, &DCRError{
			Error:            DCRErrorInvalidClientMetadata,
			ErrorDescription: "grant_types must include 'authorization_code'",
		}
	}
	return filtered, nil
}

// FilterPublicResponseTypes returns the subset of responseTypes this server
// supports for public clients, dropping unsupported entries instead of
// rejecting the whole set. Same reasoning as FilterPublicGrantTypes: a CIMD
// document declares capabilities across all servers, so an entry this server
// does not support must not be fatal — but "code" must survive the filter,
// since it is the only response type this server can serve. nil/empty input
// gets the same defaults as DCR.
func FilterPublicResponseTypes(responseTypes []string) ([]string, *DCRError) {
	// Clone for the same non-aliasing reason as FilterPublicGrantTypes.
	if len(responseTypes) == 0 {
		return slices.Clone(defaultResponseTypes), nil
	}
	filtered := make([]string, 0, len(responseTypes))
	for _, rt := range responseTypes {
		if allowedResponseTypes[rt] {
			filtered = append(filtered, rt)
		}
	}
	if !slices.Contains(filtered, "code") {
		return nil, &DCRError{
			Error:            DCRErrorInvalidClientMetadata,
			ErrorDescription: "response_types must include 'code'",
		}
	}
	return filtered, nil
}

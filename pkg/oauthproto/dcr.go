// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauthproto

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ToolHiveMCPClientName is the name advertised in dynamic client registration requests.
const ToolHiveMCPClientName = "ToolHive MCP Client"

// DynamicClientRegistrationRequest represents the request for dynamic client registration (RFC 7591).
type DynamicClientRegistrationRequest struct {
	// Required field according to RFC 7591
	RedirectURIs []string `json:"redirect_uris"`

	// Essential fields for OAuth flow
	ClientName              string    `json:"client_name,omitempty"`
	TokenEndpointAuthMethod string    `json:"token_endpoint_auth_method,omitempty"`
	GrantTypes              []string  `json:"grant_types,omitempty"`
	ResponseTypes           []string  `json:"response_types,omitempty"`
	Scopes                  ScopeList `json:"scope,omitempty"`
}

// ScopeList represents the "scope" field in both dynamic client registration requests and responses.
//
// Marshaling (requests): Per RFC 7591 Section 2, scopes are serialized as a space-delimited string.
// Examples:
//   - []string{"openid", "profile", "email"} → "openid profile email"
//   - []string{"openid"}                     → "openid"
//   - nil or []string{}                      → omitted (via omitempty)
//
// Unmarshaling (responses): Some servers return scopes as a space-delimited string per RFC 7591,
// while others return a JSON array. This type normalizes both formats into []string.
// Examples:
//   - "openid profile email"       → []string{"openid", "profile", "email"}
//   - ["openid","profile","email"] → []string{"openid", "profile", "email"}
//   - null                         → nil
//   - "" or ["", "  "]             → nil
type ScopeList []string

// MarshalJSON implements custom encoding for ScopeList. It converts the slice
// of scopes into a space-delimited string as required by RFC 7591 Section 2.
//
// Important: This method does NOT handle empty slices. Go's encoding/json package
// evaluates omitempty by checking if the Go value is "empty" (len(slice) == 0)
// BEFORE calling MarshalJSON. Empty slices are omitted at the struct level, so this
// method is never invoked for empty slices. This means we don't need to return null
// or handle the empty case - omitempty does it for us automatically.
//
// See: https://pkg.go.dev/encoding/json (omitempty checks zero values before marshaling)
func (s ScopeList) MarshalJSON() ([]byte, error) {
	// Join scopes with spaces and marshal as a string (RFC 7591 Section 2)
	scopeString := strings.Join(s, " ")
	result, err := json.Marshal(scopeString)
	if err == nil {
		slog.Debug("RFC 7591: Marshaled ScopeList", "scopes", []string(s), "result", scopeString)
	}
	return result, err
}

// UnmarshalJSON implements custom decoding for ScopeList. It supports both
// string and array encodings of the "scope" field, trimming whitespace and
// normalizing empty values to nil for consistent semantics.
func (s *ScopeList) UnmarshalJSON(data []byte) error {
	// Handle explicit null
	if strings.TrimSpace(string(data)) == "null" {
		*s = nil
		return nil
	}

	// Case 1: space-delimited string
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		if strings.TrimSpace(str) == "" {
			*s = nil
			return nil
		}
		*s = strings.Fields(str)
		return nil
	}

	// Case 2: JSON array
	var arr []string
	if err := json.Unmarshal(data, &arr); err == nil {
		cleaned := make([]string, 0, len(arr))
		for _, v := range arr {
			if v = strings.TrimSpace(v); v != "" {
				cleaned = append(cleaned, v)
			}
		}
		// Normalize: treat all-empty/whitespace arrays the same as ""
		if len(cleaned) == 0 {
			*s = nil
		} else {
			*s = cleaned
		}
		return nil
	}

	return fmt.Errorf("invalid scope format: %s", string(data))
}

// DynamicClientRegistrationResponse represents the response from dynamic client registration (RFC 7591).
type DynamicClientRegistrationResponse struct {
	// Required fields
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"` //nolint:gosec // G117: field legitimately holds sensitive data

	// Optional fields that may be returned
	ClientIDIssuedAt        int64  `json:"client_id_issued_at,omitempty"`
	ClientSecretExpiresAt   int64  `json:"client_secret_expires_at,omitempty"`
	RegistrationAccessToken string `json:"registration_access_token,omitempty"`
	RegistrationClientURI   string `json:"registration_client_uri,omitempty"`

	// Echo back the essential request fields
	ClientName              string    `json:"client_name,omitempty"`
	RedirectURIs            []string  `json:"redirect_uris,omitempty"`
	TokenEndpointAuthMethod string    `json:"token_endpoint_auth_method,omitempty"`
	GrantTypes              []string  `json:"grant_types,omitempty"`
	ResponseTypes           []string  `json:"response_types,omitempty"`
	Scopes                  ScopeList `json:"scope,omitempty"`
}

// RegisterClientDynamically performs RFC 7591 Dynamic Client Registration against
// the given registrationEndpoint.
//
// If client is nil, a default *http.Client with a 30 s timeout, 10 s TLS handshake
// timeout, and 10 s response-header timeout is used. Pass a non-nil client to supply
// custom transport settings (e.g., in tests using httptest.NewServer).
func RegisterClientDynamically(
	ctx context.Context,
	registrationEndpoint string,
	request *DynamicClientRegistrationRequest,
	client *http.Client,
) (*DynamicClientRegistrationResponse, error) {
	// Validate registration endpoint URL
	if _, err := validateRegistrationEndpoint(registrationEndpoint); err != nil {
		return nil, err
	}

	// Reject a nil request before the dereference below; the nil check previously
	// lived inside validateAndSetDefaults, but the shallow copy must come first.
	if request == nil {
		return nil, fmt.Errorf("registration request cannot be nil")
	}

	// Shallow-copy the request before passing it to validateAndSetDefaults so
	// that the caller's original struct is never mutated. Slice fields (RedirectURIs,
	// GrantTypes, ResponseTypes, Scopes) share the same backing arrays, but
	// validateAndSetDefaults only assigns new slices to nil/zero fields — it never
	// appends to or modifies existing ones — so a shallow copy is safe here.
	reqCopy := *request
	if err := validateAndSetDefaults(&reqCopy); err != nil {
		return nil, err
	}

	// Create HTTP request
	req, err := createHTTPRequest(ctx, registrationEndpoint, &reqCopy)
	if err != nil {
		return nil, err
	}

	// Use caller-supplied client or build a default one
	httpClient := buildHTTPClient(client)

	// Make the request
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to perform dynamic client registration: %w", err)
	}

	// Handle response
	response, err := handleHTTPResponse(resp)
	if err != nil {
		return nil, err
	}

	//nolint:gosec // G706: client_id is public metadata from DCR response
	slog.Debug("Successfully registered OAuth client dynamically",
		"client_id", response.ClientID)
	return response, nil
}

// validateRegistrationEndpoint validates the registration endpoint URL.
func validateRegistrationEndpoint(registrationEndpoint string) (*url.URL, error) {
	registrationURL, err := url.Parse(registrationEndpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid registration endpoint URL: %w", err)
	}

	// Ensure HTTPS for security (except loopback addresses for development)
	if registrationURL.Scheme != schemeHTTPS && !IsLoopbackHost(registrationURL.Host) {
		return nil, fmt.Errorf("registration endpoint must use HTTPS: %s", registrationEndpoint)
	}

	return registrationURL, nil
}

// validateAndSetDefaults validates the request and sets default values.
func validateAndSetDefaults(request *DynamicClientRegistrationRequest) error {
	if len(request.RedirectURIs) == 0 {
		return fmt.Errorf("at least one redirect URI is required")
	}

	// Validate that individual scope values don't contain spaces (RFC 6749 Section 3.3)
	// Scopes must be space-separated tokens, so spaces within a scope value are invalid
	for _, scope := range request.Scopes {
		if strings.Contains(scope, " ") {
			return fmt.Errorf("invalid scope value %q: scope values cannot contain spaces (RFC 6749)", scope)
		}
	}

	// Set default values if not provided
	if request.ClientName == "" {
		request.ClientName = ToolHiveMCPClientName
	}
	if len(request.GrantTypes) == 0 {
		request.GrantTypes = []string{GrantTypeAuthorizationCode, GrantTypeRefreshToken}
	}
	if len(request.ResponseTypes) == 0 {
		request.ResponseTypes = []string{ResponseTypeCode}
	}
	if request.TokenEndpointAuthMethod == "" {
		request.TokenEndpointAuthMethod = TokenEndpointAuthMethodNone // For PKCE flow
	}

	return nil
}

// createHTTPRequest creates the HTTP request for dynamic client registration.
func createHTTPRequest(
	ctx context.Context,
	registrationEndpoint string,
	request *DynamicClientRegistrationRequest,
) (*http.Request, error) {
	// Serialize request to JSON
	requestBody, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal registration request: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, registrationEndpoint, strings.NewReader(string(requestBody)))
	if err != nil {
		return nil, fmt.Errorf("failed to create registration request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)

	return req, nil
}

// NewDefaultDCRClient returns the canonical bounded *http.Client used by
// RegisterClientDynamically when its caller does not supply one. It is
// exported so callers that need to wrap the transport (for example, to
// inject an RFC 7591 initial access token as an Authorization header) can
// reuse the same timeout policy and benefit automatically from any future
// tightening of these bounds.
//
// Timeouts:
//
//   - Overall request timeout: 30 s
//   - TLS handshake timeout: 10 s
//   - Response-header timeout: 10 s
func NewDefaultDCRClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
		},
	}
}

// buildHTTPClient returns the caller-supplied client, or a default client if nil.
func buildHTTPClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return NewDefaultDCRClient()
}

// handleHTTPResponse handles the HTTP response and validates it.
func handleHTTPResponse(resp *http.Response) (*DynamicClientRegistrationResponse, error) {
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Debug("Failed to close response body", "error", err)
		}
	}()

	// Check response status
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		// Try to read error response
		errorBody, _ := io.ReadAll(resp.Body)

		// Detect if DCR is not supported by the provider.
		// Common HTTP status codes when DCR is unsupported:
		//   - 404 Not Found: endpoint doesn't exist
		//   - 405 Method Not Allowed: endpoint exists but POST not allowed
		//   - 501 Not Implemented: DCR feature not implemented
		if resp.StatusCode == http.StatusNotFound ||
			resp.StatusCode == http.StatusMethodNotAllowed ||
			resp.StatusCode == http.StatusNotImplemented {
			return nil, fmt.Errorf(
				"the provider does not support RFC 7591 Dynamic Client Registration (HTTP %d); "+
					"configure client credentials out of band. Error details: %s",
				resp.StatusCode, string(errorBody))
		}

		return nil, fmt.Errorf("dynamic client registration failed with status %d: %s", resp.StatusCode, string(errorBody))
	}

	// Check content type; drain before returning to allow TCP connection reuse.
	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "application/json") {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("unexpected content type: %s", contentType)
	}

	// Limit response size to prevent DoS
	const maxResponseSize = 1024 * 1024 // 1MB
	limitedReader := io.LimitReader(resp.Body, maxResponseSize)

	// Parse the response
	var response DynamicClientRegistrationResponse
	decoder := json.NewDecoder(limitedReader)
	if err := decoder.Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to decode registration response: %w", err)
	}

	// Validate required response fields
	if response.ClientID == "" {
		return nil, fmt.Errorf("registration response missing client_id")
	}

	return &response, nil
}

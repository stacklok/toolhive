// Package oauth provides OAuth 2.0 and OIDC authentication functionality.
package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/networking"
)

// ToolHiveMCPClientName is the name of the ToolHive MCP client
const ToolHiveMCPClientName = "ToolHive MCP Client"

// AuthorizationCode is the grant type for authorization code
const AuthorizationCode = "authorization_code"

// ResponseTypeCode is the response type for code
const ResponseTypeCode = "code"

// TokenEndpointAuthMethodNone is the token endpoint auth method for none
const TokenEndpointAuthMethodNone = "none"

// DynamicClientRegistrationRequest represents the request for dynamic client registration (RFC 7591)
type DynamicClientRegistrationRequest struct {
	// Required field according to RFC 7591
	RedirectURIs []string `json:"redirect_uris"`

	// Essential fields for OAuth flow
	ClientName              string   `json:"client_name,omitempty"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method,omitempty"`
	GrantTypes              []string `json:"grant_types,omitempty"`
	ResponseTypes           []string `json:"response_types,omitempty"`
	Scopes                  []string `json:"scope,omitempty"`
}

// NewDynamicClientRegistrationRequest creates a new dynamic client registration request
func NewDynamicClientRegistrationRequest(scopes []string, callbackPort int) *DynamicClientRegistrationRequest {

	redirectURIs := []string{fmt.Sprintf("http://localhost:%d/callback", callbackPort)}

	// Create dynamic registration request
	registrationRequest := &DynamicClientRegistrationRequest{
		ClientName:              ToolHiveMCPClientName,
		RedirectURIs:            redirectURIs,
		TokenEndpointAuthMethod: "none", // For PKCE flow
		GrantTypes:              []string{AuthorizationCode},
		ResponseTypes:           []string{ResponseTypeCode},
		Scopes:                  scopes,
	}
	return registrationRequest
}

type ScopeList []string

func (s *ScopeList) UnmarshalJSON(data []byte) error {
	// Handle explicit null
	if string(data) == "null" {
		*s = nil
		return nil
	}

	// Try to decode as string first: "openid profile email"
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		str = strings.TrimSpace(str)
		if str == "" {
			*s = nil
			return nil
		}
		*s = strings.Fields(str) // split by spaces
		return nil
	}

	// Try to decode as []string: ["openid","profile","email"]
	var arr []string
	if err := json.Unmarshal(data, &arr); err == nil {
		*s = make([]string, 0, len(arr))
		for _, v := range arr {
			if v = strings.TrimSpace(v); v != "" {
				*s = append(*s, v)
			}
		}
		return nil
	}

	return fmt.Errorf("invalid scope format: %s", string(data))
}

// DynamicClientRegistrationResponse represents the response from dynamic client registration (RFC 7591)
type DynamicClientRegistrationResponse struct {
	// Required fields
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`

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

// RegisterClientDynamically performs dynamic client registration (RFC 7591)
func RegisterClientDynamically(
	ctx context.Context,
	registrationEndpoint string,
	request *DynamicClientRegistrationRequest,
) (*DynamicClientRegistrationResponse, error) {
	return registerClientDynamicallyWithClient(ctx, registrationEndpoint, request, nil)
}

// validateRegistrationEndpoint validates the registration endpoint URL
func validateRegistrationEndpoint(registrationEndpoint string) (*url.URL, error) {
	registrationURL, err := url.Parse(registrationEndpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid registration endpoint URL: %w", err)
	}

	// Ensure HTTPS for security (except localhost for development)
	if registrationURL.Scheme != "https" && !networking.IsLocalhost(registrationURL.Host) {
		return nil, fmt.Errorf("registration endpoint must use HTTPS: %s", registrationEndpoint)
	}

	return registrationURL, nil
}

// validateAndSetDefaults validates the request and sets default values
func validateAndSetDefaults(request *DynamicClientRegistrationRequest) error {
	if request == nil {
		return fmt.Errorf("registration request cannot be nil")
	}
	if len(request.RedirectURIs) == 0 {
		return fmt.Errorf("at least one redirect URI is required")
	}

	// Set default values if not provided
	if request.ClientName == "" {
		request.ClientName = ToolHiveMCPClientName
	}
	if len(request.GrantTypes) == 0 {
		request.GrantTypes = []string{AuthorizationCode}
	}
	if len(request.ResponseTypes) == 0 {
		request.ResponseTypes = []string{ResponseTypeCode}
	}
	if request.TokenEndpointAuthMethod == "" {
		request.TokenEndpointAuthMethod = TokenEndpointAuthMethodNone // For PKCE flow
	}

	return nil
}

// createHTTPRequest creates the HTTP request for dynamic client registration
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

// getHTTPClient returns the HTTP client to use for the request
func getHTTPClient(client httpClient) httpClient {
	if client != nil {
		return client
	}

	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
		},
	}
}

// handleHTTPResponse handles the HTTP response and validates it
func handleHTTPResponse(resp *http.Response) (*DynamicClientRegistrationResponse, error) {
	defer resp.Body.Close()

	// Check response status
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		// Try to read error response
		errorBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("dynamic client registration failed with status %d: %s", resp.StatusCode, string(errorBody))
	}

	// Check content type
	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "application/json") {
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

// registerClientDynamicallyWithClient performs dynamic client registration with a custom HTTP client (private for testing)
func registerClientDynamicallyWithClient(
	ctx context.Context,
	registrationEndpoint string,
	request *DynamicClientRegistrationRequest,
	client httpClient,
) (*DynamicClientRegistrationResponse, error) {
	// Validate registration endpoint URL
	if _, err := validateRegistrationEndpoint(registrationEndpoint); err != nil {
		return nil, err
	}

	// Validate request and set defaults
	if err := validateAndSetDefaults(request); err != nil {
		return nil, err
	}

	// Create HTTP request
	req, err := createHTTPRequest(ctx, registrationEndpoint, request)
	if err != nil {
		return nil, err
	}

	// Get HTTP client
	httpClient := getHTTPClient(client)

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

	logger.Infof("Successfully registered OAuth client dynamically - client_id: %s", response.ClientID)
	return response, nil
}

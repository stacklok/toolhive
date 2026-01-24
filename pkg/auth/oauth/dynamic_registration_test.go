// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	oauthproto "github.com/stacklok/toolhive/pkg/oauth"
)

func TestDiscoverOIDCEndpointsWithRegistration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		issuer         string
		response       string
		expectedError  bool
		expectedResult *oauthproto.OIDCDiscoveryDocument
	}{
		{
			name:   "valid OIDC discovery with registration endpoint",
			issuer: "https://example.com",
			response: `{
				"issuer": "{{SERVER_URL}}",
				"authorization_endpoint": "{{SERVER_URL}}/oauth/authorize",
				"token_endpoint": "{{SERVER_URL}}/oauth/token",
				"userinfo_endpoint": "{{SERVER_URL}}/oauth/userinfo",
				"jwks_uri": "{{SERVER_URL}}/oauth/jwks",
				"registration_endpoint": "{{SERVER_URL}}/oauth/register"
			}`,
			expectedError: false,
			expectedResult: &oauthproto.OIDCDiscoveryDocument{
				AuthorizationServerMetadata: oauthproto.AuthorizationServerMetadata{
					Issuer:                "https://example.com",
					AuthorizationEndpoint: "https://example.com/oauth/authorize",
					TokenEndpoint:         "https://example.com/oauth/token",
					UserinfoEndpoint:      "https://example.com/oauth/userinfo",
					JWKSURI:               "https://example.com/oauth/jwks",
					RegistrationEndpoint:  "https://example.com/oauth/register",
				},
			},
		},
		{
			name:   "valid OIDC discovery without registration endpoint",
			issuer: "https://example.com",
			response: `{
				"issuer": "{{SERVER_URL}}",
				"authorization_endpoint": "{{SERVER_URL}}/oauth/authorize",
				"token_endpoint": "{{SERVER_URL}}/oauth/token",
				"userinfo_endpoint": "{{SERVER_URL}}/oauth/userinfo",
				"jwks_uri": "{{SERVER_URL}}/oauth/jwks"
			}`,
			expectedError: false,
			expectedResult: &oauthproto.OIDCDiscoveryDocument{
				AuthorizationServerMetadata: oauthproto.AuthorizationServerMetadata{
					Issuer:                "https://example.com",
					AuthorizationEndpoint: "https://example.com/oauth/authorize",
					TokenEndpoint:         "https://example.com/oauth/token",
					UserinfoEndpoint:      "https://example.com/oauth/userinfo",
					JWKSURI:               "https://example.com/oauth/jwks",
					RegistrationEndpoint:  "",
				},
			},
		},
		{
			name:          "invalid issuer URL",
			issuer:        "not-a-url",
			expectedError: true,
		},
		{
			name:          "non-HTTPS issuer",
			issuer:        "http://example.com",
			expectedError: true,
		},
		{
			name:   "localhost HTTP allowed for development",
			issuer: "http://localhost:8080",
			response: `{
				"issuer": "{{SERVER_URL}}",
				"authorization_endpoint": "{{SERVER_URL}}/oauth/authorize",
				"token_endpoint": "{{SERVER_URL}}/oauth/token",
				"userinfo_endpoint": "{{SERVER_URL}}/oauth/userinfo",
				"jwks_uri": "{{SERVER_URL}}/oauth/jwks",
				"registration_endpoint": "{{SERVER_URL}}/oauth/register"
			}`,
			expectedError: false,
			expectedResult: &oauthproto.OIDCDiscoveryDocument{
				AuthorizationServerMetadata: oauthproto.AuthorizationServerMetadata{
					Issuer:                "http://localhost:8080",
					AuthorizationEndpoint: "http://localhost:8080/oauth/authorize",
					TokenEndpoint:         "http://localhost:8080/oauth/token",
					UserinfoEndpoint:      "http://localhost:8080/oauth/userinfo",
					JWKSURI:               "http://localhost:8080/oauth/jwks",
					RegistrationEndpoint:  "http://localhost:8080/oauth/register",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var server *httptest.Server
			var responseTemplate string

			if tt.response != "" {
				responseTemplate = tt.response
				server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					// Handle both OIDC and OAuth discovery endpoints
					if r.URL.Path == oauthproto.WellKnownOIDCPath ||
						r.URL.Path == oauthproto.WellKnownOAuthServerPath {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusOK)
						// Replace placeholder with actual server URL
						response := strings.ReplaceAll(responseTemplate, "{{SERVER_URL}}", server.URL)
						w.Write([]byte(response))
					} else {
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer server.Close()
			}

			issuer := tt.issuer
			if server != nil {
				// For test server, use the actual server URL
				issuer = server.URL
			}

			result, err := DiscoverOIDCEndpoints(context.Background(), issuer)

			if tt.expectedError {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
				if server != nil {
					// For test server, we can't predict the exact URLs, so just check structure
					assert.NotEmpty(t, result.Issuer)
					assert.NotEmpty(t, result.AuthorizationEndpoint)
					assert.NotEmpty(t, result.TokenEndpoint)
					if tt.expectedResult.RegistrationEndpoint != "" {
						assert.NotEmpty(t, result.RegistrationEndpoint)
					}
				} else {
					// For static tests, check exact values
					assert.Equal(t, tt.expectedResult.Issuer, result.Issuer)
					assert.Equal(t, tt.expectedResult.AuthorizationEndpoint, result.AuthorizationEndpoint)
					assert.Equal(t, tt.expectedResult.TokenEndpoint, result.TokenEndpoint)
					assert.Equal(t, tt.expectedResult.RegistrationEndpoint, result.RegistrationEndpoint)
				}
			}
		})
	}
}

func TestNewDynamicClientRegistrationRequest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		scopes       []string
		callbackPort int
		expected     *DynamicClientRegistrationRequest
	}{
		{
			name:         "basic request",
			scopes:       []string{"openid", "profile"},
			callbackPort: 8080,
			expected: &DynamicClientRegistrationRequest{
				ClientName:              "ToolHive MCP Client",
				RedirectURIs:            []string{"http://localhost:8080/callback"},
				TokenEndpointAuthMethod: "none",
				GrantTypes:              []string{"authorization_code", "refresh_token"},
				ResponseTypes:           []string{"code"},
				Scopes:                  []string{"openid", "profile"},
			},
		},
		{
			name:         "empty scopes",
			scopes:       []string{},
			callbackPort: 8666,
			expected: &DynamicClientRegistrationRequest{
				ClientName:              "ToolHive MCP Client",
				RedirectURIs:            []string{"http://localhost:8666/callback"},
				TokenEndpointAuthMethod: "none",
				GrantTypes:              []string{"authorization_code", "refresh_token"},
				ResponseTypes:           []string{"code"},
				Scopes:                  []string{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := NewDynamicClientRegistrationRequest(tt.scopes, tt.callbackPort)

			assert.Equal(t, tt.expected.ClientName, result.ClientName)
			assert.Equal(t, tt.expected.RedirectURIs, result.RedirectURIs)
			assert.Equal(t, tt.expected.TokenEndpointAuthMethod, result.TokenEndpointAuthMethod)
			assert.Equal(t, tt.expected.GrantTypes, result.GrantTypes)
			assert.Equal(t, tt.expected.ResponseTypes, result.ResponseTypes)
			assert.Equal(t, tt.expected.Scopes, result.Scopes)
		})
	}
}

func TestDynamicClientRegistrationRequest_ScopeSerialization(t *testing.T) {
	t.Parallel()

	// This test verifies RFC 7591 Section 2 compliance for scope serialization.
	// Per the spec, scopes MUST be serialized as a space-delimited string, not a JSON array.
	// Empty/nil scopes should result in the scope field being omitted entirely (omitempty),
	// which is RFC 7591 compliant since the scope parameter is optional.

	tests := []struct {
		name              string
		scopes            []string
		shouldOmitScope   bool
		expectedScopeJSON string // Expected scope field in JSON, empty if omitted
	}{
		{
			name:            "nil scopes should omit scope field entirely",
			scopes:          nil,
			shouldOmitScope: true,
		},
		{
			name:            "empty slice scopes should omit scope field entirely",
			scopes:          []string{},
			shouldOmitScope: true,
		},
		{
			name:              "single scope should be space-delimited string per RFC 7591",
			scopes:            []string{"openid"},
			shouldOmitScope:   false,
			expectedScopeJSON: `"scope":"openid"`,
		},
		{
			name:              "multiple scopes should be space-delimited string per RFC 7591",
			scopes:            []string{"openid", "profile"},
			shouldOmitScope:   false,
			expectedScopeJSON: `"scope":"openid profile"`,
		},
		{
			name:              "three scopes should be space-delimited string per RFC 7591",
			scopes:            []string{"openid", "profile", "email"},
			shouldOmitScope:   false,
			expectedScopeJSON: `"scope":"openid profile email"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create request with specified scopes
			request := NewDynamicClientRegistrationRequest(tt.scopes, 8666)

			// Marshal to JSON
			jsonBytes, err := json.Marshal(request)
			require.NoError(t, err, "JSON marshaling should succeed")

			jsonStr := string(jsonBytes)

			// Verify scope field behavior
			if tt.shouldOmitScope {
				assert.NotContains(t, jsonStr, `"scope"`,
					"JSON should NOT contain scope field when scopes are empty/nil (omitempty behavior)")
			} else {
				assert.Contains(t, jsonStr, tt.expectedScopeJSON,
					"JSON should contain expected scope field")
			}

			// Verify other required fields are always present
			assert.Contains(t, jsonStr, `"redirect_uris"`, "redirect_uris should be present")
			assert.Contains(t, jsonStr, `"client_name"`, "client_name should be present")
			assert.Contains(t, jsonStr, `"grant_types"`, "grant_types should be present")
		})
	}
}

func TestRegisterClientDynamically(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		request        *DynamicClientRegistrationRequest
		response       string
		responseStatus int
		expectedError  bool
		expectedResult *DynamicClientRegistrationResponse
	}{
		{
			name: "successful registration",
			request: &DynamicClientRegistrationRequest{
				ClientName:              "Test Client",
				RedirectURIs:            []string{"http://localhost:8080/callback"},
				TokenEndpointAuthMethod: "none",
				GrantTypes:              []string{"authorization_code"},
				ResponseTypes:           []string{"code"},
				Scopes:                  []string{"openid", "profile"},
			},
			response: `{
				"client_id": "test-client-id",
				"client_secret": "test-client-secret",
				"client_id_issued_at": 1234567890,
				"client_secret_expires_at": 0,
				"registration_access_token": "reg-token",
				"registration_client_uri": "https://example.com/oauth/register/test-client-id"
			}`,
			responseStatus: http.StatusCreated,
			expectedError:  false,
			expectedResult: &DynamicClientRegistrationResponse{
				ClientID:                "test-client-id",
				ClientSecret:            "test-client-secret",
				ClientIDIssuedAt:        1234567890,
				ClientSecretExpiresAt:   0,
				RegistrationAccessToken: "reg-token",
				RegistrationClientURI:   "https://example.com/oauth/register/test-client-id",
			},
		},
		{
			name: "registration without client secret (PKCE flow)",
			request: &DynamicClientRegistrationRequest{
				ClientName:              "Test Client",
				RedirectURIs:            []string{"http://localhost:8080/callback"},
				TokenEndpointAuthMethod: "none",
				GrantTypes:              []string{"authorization_code"},
				ResponseTypes:           []string{"code"},
			},
			response: `{
				"client_id": "test-client-id",
				"client_id_issued_at": 1234567890
			}`,
			responseStatus: http.StatusCreated,
			expectedError:  false,
			expectedResult: &DynamicClientRegistrationResponse{
				ClientID:         "test-client-id",
				ClientIDIssuedAt: 1234567890,
			},
		},
		{
			name: "server error",
			request: &DynamicClientRegistrationRequest{
				ClientName:   "Test Client",
				RedirectURIs: []string{"http://localhost:8080/callback"},
			},
			response:       `{"error": "invalid_request", "error_description": "Invalid request"}`,
			responseStatus: http.StatusBadRequest,
			expectedError:  true,
		},
		{
			name: "DCR not supported - 404 Not Found",
			request: &DynamicClientRegistrationRequest{
				ClientName:   "Test Client",
				RedirectURIs: []string{"http://localhost:8080/callback"},
			},
			response:       `{"error": "not_found"}`,
			responseStatus: http.StatusNotFound,
			expectedError:  true,
		},
		{
			name: "DCR not supported - 405 Method Not Allowed",
			request: &DynamicClientRegistrationRequest{
				ClientName:   "Test Client",
				RedirectURIs: []string{"http://localhost:8080/callback"},
			},
			response:       `{"error": "method_not_allowed"}`,
			responseStatus: http.StatusMethodNotAllowed,
			expectedError:  true,
		},
		{
			name: "DCR not supported - 501 Not Implemented",
			request: &DynamicClientRegistrationRequest{
				ClientName:   "Test Client",
				RedirectURIs: []string{"http://localhost:8080/callback"},
			},
			response:       `{"error": "not_implemented", "error_description": "Dynamic Client Registration is not supported"}`,
			responseStatus: http.StatusNotImplemented,
			expectedError:  true,
		},
		{
			name: "invalid request - no redirect URIs",
			request: &DynamicClientRegistrationRequest{
				ClientName: "Test Client",
			},
			expectedError: true,
		},
		{
			name: "invalid request - scope with spaces",
			request: &DynamicClientRegistrationRequest{
				ClientName:   "Test Client",
				RedirectURIs: []string{"http://localhost:8080/callback"},
				Scopes:       []string{"openid", "profile email", "another"},
			},
			expectedError: true,
		},
		{
			name: "invalid request - scope with leading space",
			request: &DynamicClientRegistrationRequest{
				ClientName:   "Test Client",
				RedirectURIs: []string{"http://localhost:8080/callback"},
				Scopes:       []string{" openid"},
			},
			expectedError: true,
		},
		{
			name: "invalid request - scope with trailing space",
			request: &DynamicClientRegistrationRequest{
				ClientName:   "Test Client",
				RedirectURIs: []string{"http://localhost:8080/callback"},
				Scopes:       []string{"openid "},
			},
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var server *httptest.Server
			if tt.response != "" {
				server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					assert.Equal(t, "POST", r.Method)
					assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
					assert.Equal(t, "application/json", r.Header.Get("Accept"))

					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tt.responseStatus)
					w.Write([]byte(tt.response))
				}))
				defer server.Close()
			}

			var registrationEndpoint string
			if server != nil {
				registrationEndpoint = server.URL
			} else {
				registrationEndpoint = "https://example.com/oauth/register"
			}

			result, err := RegisterClientDynamically(context.Background(), registrationEndpoint, tt.request)

			if tt.expectedError {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
				assert.Equal(t, tt.expectedResult.ClientID, result.ClientID)
				assert.Equal(t, tt.expectedResult.ClientSecret, result.ClientSecret)
				assert.Equal(t, tt.expectedResult.ClientIDIssuedAt, result.ClientIDIssuedAt)
				assert.Equal(t, tt.expectedResult.RegistrationAccessToken, result.RegistrationAccessToken)
				assert.Equal(t, tt.expectedResult.RegistrationClientURI, result.RegistrationClientURI)
			}
		})
	}
}

func TestDynamicClientRegistrationRequest_Defaults(t *testing.T) {
	t.Parallel()
	// Test that default values are set correctly
	request := &DynamicClientRegistrationRequest{
		RedirectURIs: []string{"http://localhost:8080/callback"},
	}

	// Serialize to JSON to verify defaults
	data, err := json.Marshal(request)
	require.NoError(t, err)

	var result map[string]interface{}
	err = json.Unmarshal(data, &result)
	require.NoError(t, err)

	// Verify that required fields are present
	assert.Contains(t, result, "redirect_uris")
	assert.Equal(t, []interface{}{"http://localhost:8080/callback"}, result["redirect_uris"])
}

// TestDynamicClientRegistrationResponse_Validation tests that the response validation works correctly
func TestDynamicClientRegistrationResponse_Validation(t *testing.T) {
	t.Parallel()
	// Test that response validation works correctly
	validResponse := &DynamicClientRegistrationResponse{
		ClientID: "test-client-id",
	}

	// Serialize to JSON
	data, err := json.Marshal(validResponse)
	require.NoError(t, err)

	var result DynamicClientRegistrationResponse
	err = json.Unmarshal(data, &result)
	require.NoError(t, err)

	assert.Equal(t, "test-client-id", result.ClientID)
}

func TestDiscoverOIDCEndpointsWithRegistrationFallback(t *testing.T) {
	t.Parallel()

	// Test case: OIDC well-known succeeds but lacks registration_endpoint,
	// OAuth authorization server well-known has it
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		baseURL := "http://" + r.Host
		switch r.URL.Path {
		case oauthproto.WellKnownOIDCPath:
			// OIDC discovery - no registration_endpoint
			response := `{
				"issuer": "` + baseURL + `",
				"authorization_endpoint": "` + baseURL + `/oauth/authorize",
				"token_endpoint": "` + baseURL + `/oauth/token",
				"userinfo_endpoint": "` + baseURL + `/oauth/userinfo",
				"jwks_uri": "` + baseURL + `/oauth/jwks"
			}`
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(response))
		case oauthproto.WellKnownOAuthServerPath:
			// OAuth authorization server - has registration_endpoint
			response := `{
				"issuer": "` + baseURL + `",
				"authorization_endpoint": "` + baseURL + `/oauth/authorize",
				"token_endpoint": "` + baseURL + `/oauth/token",
				"registration_endpoint": "` + baseURL + `/oauth/register"
			}`
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(response))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	result, err := DiscoverOIDCEndpoints(context.Background(), server.URL)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, server.URL, result.Issuer)
	assert.NotEmpty(t, result.AuthorizationEndpoint)
	assert.NotEmpty(t, result.TokenEndpoint)
	// Registration endpoint should be found from OAuth authorization server well-known
	assert.NotEmpty(t, result.RegistrationEndpoint, "registration_endpoint should be found via OAuth authorization server fallback")
	assert.Equal(t, server.URL+"/oauth/register", result.RegistrationEndpoint)
}

func TestDiscoverOIDCEndpointsWithRegistrationFallbackIssuerMismatch(t *testing.T) {
	t.Parallel()

	// Test case: OIDC and OAuth have different issuers - should not merge
	// Use DiscoverActualIssuer which doesn't validate issuer, allowing us to test the merge logic
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		baseURL := "http://" + r.Host
		switch r.URL.Path {
		case oauthproto.WellKnownOIDCPath:
			// OIDC discovery - no registration_endpoint, different issuer
			response := `{
				"issuer": "https://oidc.example.com",
				"authorization_endpoint": "` + baseURL + `/oauth/authorize",
				"token_endpoint": "` + baseURL + `/oauth/token",
				"userinfo_endpoint": "` + baseURL + `/oauth/userinfo",
				"jwks_uri": "` + baseURL + `/oauth/jwks"
			}`
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(response))
		case oauthproto.WellKnownOAuthServerPath:
			// OAuth authorization server - has registration_endpoint but different issuer
			response := `{
				"issuer": "https://oauth.example.com",
				"authorization_endpoint": "` + baseURL + `/oauth/authorize",
				"token_endpoint": "` + baseURL + `/oauth/token",
				"registration_endpoint": "` + baseURL + `/oauth/register"
			}`
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(response))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	// Use DiscoverActualIssuer which doesn't validate issuer, allowing us to test merge logic
	result, err := DiscoverActualIssuer(context.Background(), server.URL)

	require.NoError(t, err)
	require.NotNil(t, result)
	// Registration endpoint should NOT be merged due to issuer mismatch
	assert.Empty(t, result.RegistrationEndpoint, "registration_endpoint should not be merged when issuers don't match")
}

// TestIsLocalhost is already defined in oidc_test.go

// TestScopeList_MarshalJSON tests that the ScopeList marshaling works correctly
// and produces RFC 7591 compliant space-delimited strings.
func TestScopeList_MarshalJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		scopes   ScopeList
		wantJSON string
		wantOmit bool // If true, expect omitempty to hide the field
	}{
		{
			name:     "nil scopes => empty string (omitempty will hide at struct level)",
			scopes:   nil,
			wantJSON: `""`,
			wantOmit: true,
		},
		{
			name:     "empty slice => empty string (omitempty will hide at struct level)",
			scopes:   ScopeList{},
			wantJSON: `""`,
			wantOmit: true,
		},
		{
			name:     "single scope => string",
			scopes:   ScopeList{"openid"},
			wantJSON: `"openid"`,
		},
		{
			name:     "two scopes => space-delimited string",
			scopes:   ScopeList{"openid", "profile"},
			wantJSON: `"openid profile"`,
		},
		{
			name:     "three scopes => space-delimited string",
			scopes:   ScopeList{"openid", "profile", "email"},
			wantJSON: `"openid profile email"`,
		},
		{
			name:     "scopes with special characters",
			scopes:   ScopeList{"read:user", "write:repo"},
			wantJSON: `"read:user write:repo"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			jsonBytes, err := json.Marshal(tt.scopes)
			require.NoError(t, err, "marshaling should succeed")

			jsonStr := string(jsonBytes)
			assert.Equal(t, tt.wantJSON, jsonStr, "marshaled JSON should match expected format")

			// Verify omitempty behavior in a struct
			// Note: omitempty checks the Go value (empty slice) before calling MarshalJSON,
			// so empty slices are omitted regardless of what MarshalJSON returns.
			if tt.wantOmit {
				type testStruct struct {
					Scope ScopeList `json:"scope,omitempty"`
				}
				s := testStruct{Scope: tt.scopes}
				structJSON, err := json.Marshal(s)
				require.NoError(t, err)
				assert.Equal(t, "{}", string(structJSON), "omitempty should hide empty scope field")
			}
		})
	}
}

// TestScopeList_UnmarshalJSON tests that the ScopeList unmarshaling works correctly.
func TestScopeList_UnmarshalJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		jsonIn  string
		want    []string
		wantErr bool
	}{
		{
			name:   "space-delimited string",
			jsonIn: `"openid profile email"`,
			want:   []string{"openid", "profile", "email"},
		},
		{
			name:   "empty string => nil",
			jsonIn: `""`,
			want:   nil,
		},
		{
			name:   "string with extra spaces",
			jsonIn: `"  openid   profile  "`,
			want:   []string{"openid", "profile"},
		},
		{
			name:   "normal array",
			jsonIn: `["openid","profile","email"]`,
			want:   []string{"openid", "profile", "email"},
		},
		{
			name:   "array with whitespace and empties",
			jsonIn: `["  openid  ",""," profile "]`,
			want:   []string{"openid", "profile"},
		},
		{
			name:   "all-empty array => nil",
			jsonIn: `["","  "]`,
			want:   nil,
		},
		{
			name:   "explicit null => nil",
			jsonIn: `null`,
			want:   nil,
		},
		{
			name:    "invalid type (number)",
			jsonIn:  `123`,
			wantErr: true,
		},
		{
			name:    "invalid type (object)",
			jsonIn:  `{"not":"valid"}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt // capture loop variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var s ScopeList
			err := json.Unmarshal([]byte(tt.jsonIn), &s)

			if tt.wantErr {
				assert.Error(t, err, "expected error but got none")
				return
			}

			assert.NoError(t, err, "unexpected unmarshal error")
			assert.Equal(t, tt.want, []string(s))
		})
	}
}

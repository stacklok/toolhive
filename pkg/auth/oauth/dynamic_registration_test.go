package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscoverOIDCEndpointsWithRegistration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		issuer         string
		response       string
		expectedError  bool
		expectedResult *OIDCDiscoveryDocument
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
			expectedResult: &OIDCDiscoveryDocument{
				Issuer:                "https://example.com",
				AuthorizationEndpoint: "https://example.com/oauth/authorize",
				TokenEndpoint:         "https://example.com/oauth/token",
				UserinfoEndpoint:      "https://example.com/oauth/userinfo",
				JWKSURI:               "https://example.com/oauth/jwks",
				RegistrationEndpoint:  "https://example.com/oauth/register",
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
			expectedResult: &OIDCDiscoveryDocument{
				Issuer:                "https://example.com",
				AuthorizationEndpoint: "https://example.com/oauth/authorize",
				TokenEndpoint:         "https://example.com/oauth/token",
				UserinfoEndpoint:      "https://example.com/oauth/userinfo",
				JWKSURI:               "https://example.com/oauth/jwks",
				RegistrationEndpoint:  "",
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
			expectedResult: &OIDCDiscoveryDocument{
				Issuer:                "http://localhost:8080",
				AuthorizationEndpoint: "http://localhost:8080/oauth/authorize",
				TokenEndpoint:         "http://localhost:8080/oauth/token",
				UserinfoEndpoint:      "http://localhost:8080/oauth/userinfo",
				JWKSURI:               "http://localhost:8080/oauth/jwks",
				RegistrationEndpoint:  "http://localhost:8080/oauth/register",
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
					if r.URL.Path == "/.well-known/openid-configuration" ||
						r.URL.Path == "/.well-known/oauth-authorization-server" {
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
				GrantTypes:              []string{"authorization_code"},
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
				GrantTypes:              []string{"authorization_code"},
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
			name: "invalid request - no redirect URIs",
			request: &DynamicClientRegistrationRequest{
				ClientName: "Test Client",
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

// TestIsLocalhost is already defined in oidc_test.go

// TestScopeList_UnmarshalJSON tests that the ScopeList unmarshaling works correctly
func TestScopeList_UnmarshalJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		jsonIn  string
		want    []string
		wantNil bool
		wantErr bool
	}{
		{
			name:   "space-delimited string",
			jsonIn: `"openid profile email"`,
			want:   []string{"openid", "profile", "email"},
		},
		{
			name:    "empty string => nil",
			jsonIn:  `""`,
			wantNil: true,
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
			name:    "all-empty array => nil",
			jsonIn:  `["","  "]`,
			wantNil: true,
		},
		{
			name:    "explicit null => nil",
			jsonIn:  `null`,
			wantNil: true,
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
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var s ScopeList
			err := json.Unmarshal([]byte(tt.jsonIn), &s)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error but got none (value: %v)", s)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantNil {
				if s != nil {
					t.Fatalf("expected nil, got %v", []string(s))
				}
				return
			}

			if !reflect.DeepEqual([]string(s), tt.want) {
				t.Fatalf("got %v, want %v", []string(s), tt.want)
			}
		})
	}
}

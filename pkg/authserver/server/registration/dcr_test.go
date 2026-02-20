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

package registration

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/oauth"
)

func TestValidateRedirectURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		uri         string
		expectError bool
		errorCode   string
	}{
		// HTTPS - allowed for any host
		{
			name:        "https with any host",
			uri:         "https://example.com/callback",
			expectError: false,
		},
		{
			name:        "https with custom domain",
			uri:         "https://myapp.example.org:8443/oauth/callback",
			expectError: false,
		},

		// HTTP loopback addresses - allowed per RFC 8252
		{
			name:        "http with 127.0.0.1",
			uri:         "http://127.0.0.1/callback",
			expectError: false,
		},
		{
			name:        "http with 127.0.0.1 and port",
			uri:         "http://127.0.0.1:8080/callback",
			expectError: false,
		},
		{
			name:        "http with localhost",
			uri:         "http://localhost/callback",
			expectError: false,
		},
		{
			name:        "http with localhost and port",
			uri:         "http://localhost:9000/callback",
			expectError: false,
		},

		// HTTP non-loopback - not allowed
		{
			name:        "http with non-loopback host",
			uri:         "http://example.com/callback",
			expectError: true,
			errorCode:   DCRErrorInvalidRedirectURI,
		},
		{
			name:        "http with IP address that is not loopback",
			uri:         "http://192.168.1.1/callback",
			expectError: true,
			errorCode:   DCRErrorInvalidRedirectURI,
		},

		// Invalid URI format
		{
			name:        "invalid URI format - missing scheme",
			uri:         "://invalid",
			expectError: true,
			errorCode:   DCRErrorInvalidRedirectURI,
		},
		{
			name:        "invalid URI format - malformed",
			uri:         "not a valid uri",
			expectError: true,
			errorCode:   DCRErrorInvalidRedirectURI,
		},

		// Private-use URI schemes - allowed for native apps per RFC 8252 Section 7.1
		{
			name:        "custom scheme allowed for native apps",
			uri:         "myapp://callback",
			expectError: false,
		},
		{
			name:        "cursor scheme allowed",
			uri:         "cursor://callback",
			expectError: false,
		},
		{
			name:        "vscode scheme allowed",
			uri:         "vscode://callback",
			expectError: false,
		},

		// Length validation
		{
			name:        "redirect URI exceeding max length is rejected",
			uri:         "https://example.com/" + strings.Repeat("a", oauth.MaxRedirectURILength),
			expectError: true,
			errorCode:   DCRErrorInvalidRedirectURI,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateRedirectURI(tt.uri)

			if tt.expectError {
				require.NotNil(t, err, "expected error for URI %q", tt.uri)
				assert.Equal(t, tt.errorCode, err.Error)
			} else {
				assert.Nil(t, err, "unexpected error for URI %q: %v", tt.uri, err)
			}
		})
	}
}

func TestValidateDCRRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		request            *DCRRequest
		expectError        bool
		errorCode          string
		expectedAuthMethod string
		expectedGrants     []string
		expectedResponses  []string
	}{
		// Valid requests
		{
			name: "valid minimal request with loopback redirect URI",
			request: &DCRRequest{
				RedirectURIs: []string{"http://127.0.0.1/callback"},
			},
			expectError:        false,
			expectedAuthMethod: "none",
			expectedGrants:     defaultGrantTypes,
			expectedResponses:  defaultResponseTypes,
		},
		{
			name: "valid request with all fields specified",
			request: &DCRRequest{
				RedirectURIs:            []string{"http://localhost:8080/callback", "https://example.com/callback"},
				ClientName:              "My Test Client",
				TokenEndpointAuthMethod: "none",
				GrantTypes:              []string{"authorization_code", "refresh_token"},
				ResponseTypes:           []string{"code"},
			},
			expectError:        false,
			expectedAuthMethod: "none",
			expectedGrants:     []string{"authorization_code", "refresh_token"},
			expectedResponses:  []string{"code"},
		},
		{
			name: "valid request with https redirect URI",
			request: &DCRRequest{
				RedirectURIs: []string{"https://example.com/oauth/callback"},
			},
			expectError:        false,
			expectedAuthMethod: "none",
			expectedGrants:     defaultGrantTypes,
			expectedResponses:  defaultResponseTypes,
		},

		// Empty redirect_uris
		{
			name: "empty redirect_uris",
			request: &DCRRequest{
				RedirectURIs: []string{},
			},
			expectError: true,
			errorCode:   DCRErrorInvalidRedirectURI,
		},
		{
			name: "nil redirect_uris",
			request: &DCRRequest{
				RedirectURIs: nil,
			},
			expectError: true,
			errorCode:   DCRErrorInvalidRedirectURI,
		},

		// Too many redirect URIs
		{
			name: "too many redirect URIs",
			request: &DCRRequest{
				RedirectURIs: []string{
					"http://127.0.0.1:1/callback",
					"http://127.0.0.1:2/callback",
					"http://127.0.0.1:3/callback",
					"http://127.0.0.1:4/callback",
					"http://127.0.0.1:5/callback",
					"http://127.0.0.1:6/callback",
					"http://127.0.0.1:7/callback",
					"http://127.0.0.1:8/callback",
					"http://127.0.0.1:9/callback",
					"http://127.0.0.1:10/callback",
					"http://127.0.0.1:11/callback", // 11th - exceeds limit
				},
			},
			expectError: true,
			errorCode:   DCRErrorInvalidRedirectURI,
		},

		// Invalid redirect URI in list
		{
			name: "invalid redirect URI in list",
			request: &DCRRequest{
				RedirectURIs: []string{"http://127.0.0.1/callback", "http://example.com/callback"},
			},
			expectError: true,
			errorCode:   DCRErrorInvalidRedirectURI,
		},
		{
			name: "malformed redirect URI in list",
			request: &DCRRequest{
				RedirectURIs: []string{"://invalid"},
			},
			expectError: true,
			errorCode:   DCRErrorInvalidRedirectURI,
		},

		// token_endpoint_auth_method validation
		{
			name: "token_endpoint_auth_method = none",
			request: &DCRRequest{
				RedirectURIs:            []string{"http://127.0.0.1/callback"},
				TokenEndpointAuthMethod: "none",
			},
			expectError:        false,
			expectedAuthMethod: "none",
		},
		{
			name: "token_endpoint_auth_method empty defaults to none",
			request: &DCRRequest{
				RedirectURIs:            []string{"http://127.0.0.1/callback"},
				TokenEndpointAuthMethod: "",
			},
			expectError:        false,
			expectedAuthMethod: "none",
		},
		{
			name: "token_endpoint_auth_method = client_secret_basic fails",
			request: &DCRRequest{
				RedirectURIs:            []string{"http://127.0.0.1/callback"},
				TokenEndpointAuthMethod: "client_secret_basic",
			},
			expectError: true,
			errorCode:   DCRErrorInvalidClientMetadata,
		},
		{
			name: "token_endpoint_auth_method = client_secret_post fails",
			request: &DCRRequest{
				RedirectURIs:            []string{"http://127.0.0.1/callback"},
				TokenEndpointAuthMethod: "client_secret_post",
			},
			expectError: true,
			errorCode:   DCRErrorInvalidClientMetadata,
		},

		// grant_types validation
		{
			name: "grant_types defaults when empty",
			request: &DCRRequest{
				RedirectURIs: []string{"http://127.0.0.1/callback"},
				GrantTypes:   []string{},
			},
			expectError:    false,
			expectedGrants: defaultGrantTypes,
		},
		{
			name: "grant_types defaults when nil",
			request: &DCRRequest{
				RedirectURIs: []string{"http://127.0.0.1/callback"},
				GrantTypes:   nil,
			},
			expectError:    false,
			expectedGrants: defaultGrantTypes,
		},
		{
			name: "grant_types without authorization_code fails",
			request: &DCRRequest{
				RedirectURIs: []string{"http://127.0.0.1/callback"},
				GrantTypes:   []string{"refresh_token"},
			},
			expectError: true,
			errorCode:   DCRErrorInvalidClientMetadata,
		},
		{
			name: "grant_types with only client_credentials fails",
			request: &DCRRequest{
				RedirectURIs: []string{"http://127.0.0.1/callback"},
				GrantTypes:   []string{"client_credentials"},
			},
			expectError: true,
			errorCode:   DCRErrorInvalidClientMetadata,
		},
		{
			name: "grant_types with authorization_code passes",
			request: &DCRRequest{
				RedirectURIs: []string{"http://127.0.0.1/callback"},
				GrantTypes:   []string{"authorization_code"},
			},
			expectError:    false,
			expectedGrants: []string{"authorization_code"},
		},
		{
			name: "grant_types with unsupported type rejected",
			request: &DCRRequest{
				RedirectURIs: []string{"http://127.0.0.1/callback"},
				GrantTypes:   []string{"authorization_code", "client_credentials"},
			},
			expectError: true,
			errorCode:   DCRErrorInvalidClientMetadata,
		},

		// response_types validation
		{
			name: "response_types defaults when empty",
			request: &DCRRequest{
				RedirectURIs:  []string{"http://127.0.0.1/callback"},
				ResponseTypes: []string{},
			},
			expectError:       false,
			expectedResponses: defaultResponseTypes,
		},
		{
			name: "response_types defaults when nil",
			request: &DCRRequest{
				RedirectURIs:  []string{"http://127.0.0.1/callback"},
				ResponseTypes: nil,
			},
			expectError:       false,
			expectedResponses: defaultResponseTypes,
		},
		{
			name: "response_types without code fails",
			request: &DCRRequest{
				RedirectURIs:  []string{"http://127.0.0.1/callback"},
				ResponseTypes: []string{"token"},
			},
			expectError: true,
			errorCode:   DCRErrorInvalidClientMetadata,
		},
		{
			name: "response_types with only id_token fails",
			request: &DCRRequest{
				RedirectURIs:  []string{"http://127.0.0.1/callback"},
				ResponseTypes: []string{"id_token"},
			},
			expectError: true,
			errorCode:   DCRErrorInvalidClientMetadata,
		},
		{
			name: "response_types with code passes",
			request: &DCRRequest{
				RedirectURIs:  []string{"http://127.0.0.1/callback"},
				ResponseTypes: []string{"code"},
			},
			expectError:       false,
			expectedResponses: []string{"code"},
		},
		{
			name: "response_types with unsupported type rejected",
			request: &DCRRequest{
				RedirectURIs:  []string{"http://127.0.0.1/callback"},
				ResponseTypes: []string{"code", "token"},
			},
			expectError: true,
			errorCode:   DCRErrorInvalidClientMetadata,
		},

		// ClientName validation
		{
			name: "client_name is preserved",
			request: &DCRRequest{
				RedirectURIs: []string{"http://127.0.0.1/callback"},
				ClientName:   "My Application",
			},
			expectError: false,
		},
		{
			name: "client_name exceeding max length is rejected",
			request: &DCRRequest{
				RedirectURIs: []string{"http://127.0.0.1/callback"},
				ClientName:   strings.Repeat("a", MaxClientNameLength+1),
			},
			expectError: true,
			errorCode:   DCRErrorInvalidClientMetadata,
		},
		{
			name: "client_name at max length is accepted",
			request: &DCRRequest{
				RedirectURIs: []string{"http://127.0.0.1/callback"},
				ClientName:   strings.Repeat("a", MaxClientNameLength),
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := ValidateDCRRequest(tt.request)

			if tt.expectError {
				require.NotNil(t, err, "expected error")
				assert.Nil(t, result, "result should be nil on error")
				assert.Equal(t, tt.errorCode, err.Error)
			} else {
				require.Nil(t, err, "unexpected error: %v", err)
				require.NotNil(t, result, "result should not be nil on success")

				// Verify defaults/values were applied correctly
				if tt.expectedAuthMethod != "" {
					assert.Equal(t, tt.expectedAuthMethod, result.TokenEndpointAuthMethod)
				}
				if tt.expectedGrants != nil {
					assert.ElementsMatch(t, tt.expectedGrants, result.GrantTypes)
				}
				if tt.expectedResponses != nil {
					assert.ElementsMatch(t, tt.expectedResponses, result.ResponseTypes)
				}

				// Verify redirect_uris are preserved
				assert.Equal(t, tt.request.RedirectURIs, result.RedirectURIs)

				// Verify client_name is preserved
				assert.Equal(t, tt.request.ClientName, result.ClientName)
			}
		})
	}
}

func TestValidateScopes(t *testing.T) {
	t.Parallel()

	allowedScopes := []string{"openid", "profile", "email", "offline_access"}

	tests := []struct {
		name           string
		requestedScope string
		allowedScopes  []string
		expectError    bool
		errorCode      string
		expectedScopes []string
	}{
		{
			name:           "valid subset of allowed scopes",
			requestedScope: "openid profile",
			allowedScopes:  allowedScopes,
			expectedScopes: []string{"openid", "profile"},
		},
		{
			name:           "full set of allowed scopes accepted",
			requestedScope: "openid profile email offline_access",
			allowedScopes:  allowedScopes,
			expectedScopes: []string{"openid", "profile", "email", "offline_access"},
		},
		{
			name:           "unknown scope rejected",
			requestedScope: "openid sneaky_admin",
			allowedScopes:  allowedScopes,
			expectError:    true,
			errorCode:      DCRErrorInvalidClientMetadata,
		},
		{
			name:           "prefix of valid scope rejected",
			requestedScope: "openid.evil",
			allowedScopes:  allowedScopes,
			expectError:    true,
			errorCode:      DCRErrorInvalidClientMetadata,
		},
		{
			name:           "substring of valid scope rejected",
			requestedScope: "open",
			allowedScopes:  allowedScopes,
			expectError:    true,
			errorCode:      DCRErrorInvalidClientMetadata,
		},
		{
			name:           "empty input returns default scopes",
			requestedScope: "",
			allowedScopes:  allowedScopes,
			expectedScopes: DefaultScopes,
		},
		{
			name:           "duplicate scopes are deduplicated",
			requestedScope: "openid openid profile",
			allowedScopes:  allowedScopes,
			expectedScopes: []string{"openid", "profile"},
		},
		{
			name:           "empty input rejected when defaults not in allowed set",
			requestedScope: "",
			allowedScopes:  []string{"custom_scope"},
			expectError:    true,
			errorCode:      DCRErrorInvalidClientMetadata,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scopes, dcrErr := ValidateScopes(tt.requestedScope, tt.allowedScopes)

			if tt.expectError {
				require.NotNil(t, dcrErr, "expected error")
				assert.Equal(t, tt.errorCode, dcrErr.Error)
				assert.Nil(t, scopes)
			} else {
				require.Nil(t, dcrErr, "unexpected error: %v", dcrErr)
				assert.Equal(t, tt.expectedScopes, scopes)
			}
		})
	}
}

func TestDCRErrorConstants(t *testing.T) {
	t.Parallel()

	// Verify error code constants match RFC 7591 Section 3.2.2
	assert.Equal(t, "invalid_redirect_uri", DCRErrorInvalidRedirectURI)
	assert.Equal(t, "invalid_client_metadata", DCRErrorInvalidClientMetadata)
}

func TestDefaultGrantTypesAndResponseTypes(t *testing.T) {
	t.Parallel()

	// Verify default grant types include authorization_code
	assert.Contains(t, defaultGrantTypes, "authorization_code")
	assert.Contains(t, defaultGrantTypes, "refresh_token")

	// Verify default response types include code
	assert.Contains(t, defaultResponseTypes, "code")
}

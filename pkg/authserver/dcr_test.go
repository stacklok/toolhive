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

package authserver

import (
	"testing"
)

func TestValidateDCRRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		request        *DCRRequest
		expectedError  string
		expectedResult *DCRRequest
	}{
		{
			name: "valid minimal request",
			request: &DCRRequest{
				RedirectURIs: []string{"http://127.0.0.1:8080/callback"},
			},
			expectedResult: &DCRRequest{
				RedirectURIs:            []string{"http://127.0.0.1:8080/callback"},
				TokenEndpointAuthMethod: "none",
				GrantTypes:              []string{"authorization_code", "refresh_token"},
				ResponseTypes:           []string{"code"},
			},
		},
		{
			name: "valid request with all fields",
			request: &DCRRequest{
				RedirectURIs:            []string{"http://localhost:9999/callback"},
				ClientName:              "My Client",
				TokenEndpointAuthMethod: "none",
				GrantTypes:              []string{"authorization_code"},
				ResponseTypes:           []string{"code"},
			},
			expectedResult: &DCRRequest{
				RedirectURIs:            []string{"http://localhost:9999/callback"},
				ClientName:              "My Client",
				TokenEndpointAuthMethod: "none",
				GrantTypes:              []string{"authorization_code"},
				ResponseTypes:           []string{"code"},
			},
		},
		{
			name: "valid request with IPv6 loopback",
			request: &DCRRequest{
				RedirectURIs: []string{"http://[::1]:8080/callback"},
			},
			expectedResult: &DCRRequest{
				RedirectURIs:            []string{"http://[::1]:8080/callback"},
				TokenEndpointAuthMethod: "none",
				GrantTypes:              []string{"authorization_code", "refresh_token"},
				ResponseTypes:           []string{"code"},
			},
		},
		{
			name: "valid request with multiple loopback URIs",
			request: &DCRRequest{
				RedirectURIs: []string{
					"http://127.0.0.1:8080/callback",
					"http://localhost:9090/oauth",
				},
			},
			expectedResult: &DCRRequest{
				RedirectURIs: []string{
					"http://127.0.0.1:8080/callback",
					"http://localhost:9090/oauth",
				},
				TokenEndpointAuthMethod: "none",
				GrantTypes:              []string{"authorization_code", "refresh_token"},
				ResponseTypes:           []string{"code"},
			},
		},
		{
			name: "valid request with HTTPS redirect URI",
			request: &DCRRequest{
				RedirectURIs: []string{"https://vscode.dev/redirect"},
			},
			expectedResult: &DCRRequest{
				RedirectURIs:            []string{"https://vscode.dev/redirect"},
				TokenEndpointAuthMethod: "none",
				GrantTypes:              []string{"authorization_code", "refresh_token"},
				ResponseTypes:           []string{"code"},
			},
		},
		{
			name: "valid VS Code style mixed URIs",
			request: &DCRRequest{
				RedirectURIs: []string{
					"https://insiders.vscode.dev/redirect",
					"https://vscode.dev/redirect",
					"http://127.0.0.1/",
					"http://127.0.0.1:33418/",
				},
			},
			expectedResult: &DCRRequest{
				RedirectURIs: []string{
					"https://insiders.vscode.dev/redirect",
					"https://vscode.dev/redirect",
					"http://127.0.0.1/",
					"http://127.0.0.1:33418/",
				},
				TokenEndpointAuthMethod: "none",
				GrantTypes:              []string{"authorization_code", "refresh_token"},
				ResponseTypes:           []string{"code"},
			},
		},
		{
			name: "missing redirect_uris",
			request: &DCRRequest{
				ClientName: "Test Client",
			},
			expectedError: DCRErrorInvalidRedirectURI,
		},
		{
			name: "empty redirect_uris",
			request: &DCRRequest{
				RedirectURIs: []string{},
			},
			expectedError: DCRErrorInvalidRedirectURI,
		},
		{
			name: "http non-loopback redirect URI rejected",
			request: &DCRRequest{
				RedirectURIs: []string{"http://example.com/callback"},
			},
			expectedError: DCRErrorInvalidRedirectURI,
		},
		{
			name: "https scheme for loopback is valid",
			request: &DCRRequest{
				RedirectURIs: []string{"https://127.0.0.1:8080/callback"},
			},
			expectedResult: &DCRRequest{
				RedirectURIs:            []string{"https://127.0.0.1:8080/callback"},
				TokenEndpointAuthMethod: "none",
				GrantTypes:              []string{"authorization_code", "refresh_token"},
				ResponseTypes:           []string{"code"},
			},
		},
		{
			name: "invalid URI format",
			request: &DCRRequest{
				RedirectURIs: []string{"not-a-valid-uri"},
			},
			expectedError: DCRErrorInvalidRedirectURI,
		},
		{
			name: "non-none auth method",
			request: &DCRRequest{
				RedirectURIs:            []string{"http://127.0.0.1:8080/callback"},
				TokenEndpointAuthMethod: "client_secret_post",
			},
			expectedError: DCRErrorInvalidClientMetadata,
		},
		{
			name: "missing authorization_code grant type",
			request: &DCRRequest{
				RedirectURIs: []string{"http://127.0.0.1:8080/callback"},
				GrantTypes:   []string{"refresh_token"},
			},
			expectedError: DCRErrorInvalidClientMetadata,
		},
		{
			name: "missing code response type",
			request: &DCRRequest{
				RedirectURIs:  []string{"http://127.0.0.1:8080/callback"},
				ResponseTypes: []string{"token"},
			},
			expectedError: DCRErrorInvalidClientMetadata,
		},
		{
			name: "mixed loopback and http external URIs rejected",
			request: &DCRRequest{
				RedirectURIs: []string{
					"http://127.0.0.1:8080/callback",
					"http://example.com/callback",
				},
			},
			expectedError: DCRErrorInvalidRedirectURI,
		},
		{
			name: "http non-loopback IP rejected",
			request: &DCRRequest{
				RedirectURIs: []string{"http://192.168.1.1/callback"},
			},
			expectedError: DCRErrorInvalidRedirectURI,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result, dcrErr := ValidateDCRRequest(tc.request)

			if tc.expectedError != "" {
				if dcrErr == nil {
					t.Fatalf("expected error %q but got nil", tc.expectedError)
				}
				if dcrErr.Error != tc.expectedError {
					t.Errorf("expected error code %q, got %q", tc.expectedError, dcrErr.Error)
				}
				return
			}

			if dcrErr != nil {
				t.Fatalf("unexpected error: %s - %s", dcrErr.Error, dcrErr.ErrorDescription)
			}

			if result == nil {
				t.Fatal("expected result but got nil")
			}

			// Compare redirect URIs
			if len(result.RedirectURIs) != len(tc.expectedResult.RedirectURIs) {
				t.Errorf("expected %d redirect URIs, got %d",
					len(tc.expectedResult.RedirectURIs), len(result.RedirectURIs))
			}
			for i, uri := range result.RedirectURIs {
				if uri != tc.expectedResult.RedirectURIs[i] {
					t.Errorf("redirect URI %d: expected %q, got %q",
						i, tc.expectedResult.RedirectURIs[i], uri)
				}
			}

			// Compare other fields
			if result.ClientName != tc.expectedResult.ClientName {
				t.Errorf("expected client_name %q, got %q",
					tc.expectedResult.ClientName, result.ClientName)
			}
			if result.TokenEndpointAuthMethod != tc.expectedResult.TokenEndpointAuthMethod {
				t.Errorf("expected token_endpoint_auth_method %q, got %q",
					tc.expectedResult.TokenEndpointAuthMethod, result.TokenEndpointAuthMethod)
			}
			if len(result.GrantTypes) != len(tc.expectedResult.GrantTypes) {
				t.Errorf("expected %d grant types, got %d",
					len(tc.expectedResult.GrantTypes), len(result.GrantTypes))
			}
			if len(result.ResponseTypes) != len(tc.expectedResult.ResponseTypes) {
				t.Errorf("expected %d response types, got %d",
					len(tc.expectedResult.ResponseTypes), len(result.ResponseTypes))
			}
		})
	}
}

func TestValidateRedirectURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		uri           string
		expectedError string
	}{
		// HTTP loopback URIs - valid per RFC 8252
		{
			name: "valid http IPv4 loopback",
			uri:  "http://127.0.0.1:8080/callback",
		},
		{
			name: "valid http IPv6 loopback",
			uri:  "http://[::1]:8080/callback",
		},
		{
			name: "valid http localhost",
			uri:  "http://localhost:9999/callback",
		},
		{
			name: "valid http localhost uppercase",
			uri:  "http://LOCALHOST:9999/callback",
		},
		{
			name: "valid http no port",
			uri:  "http://127.0.0.1/callback",
		},
		{
			name: "valid http root path",
			uri:  "http://127.0.0.1:8080/",
		},
		// HTTPS URIs - valid for any address
		{
			name: "valid https external host",
			uri:  "https://example.com/callback",
		},
		{
			name: "valid https vscode.dev",
			uri:  "https://vscode.dev/redirect",
		},
		{
			name: "valid https insiders.vscode.dev",
			uri:  "https://insiders.vscode.dev/redirect",
		},
		{
			name: "valid https loopback",
			uri:  "https://127.0.0.1:8080/callback",
		},
		{
			name: "valid https localhost",
			uri:  "https://localhost:9999/callback",
		},
		// HTTP non-loopback - invalid (security risk)
		{
			name:          "invalid http external host",
			uri:           "http://example.com:8080/callback",
			expectedError: DCRErrorInvalidRedirectURI,
		},
		{
			name:          "invalid http non-loopback IP",
			uri:           "http://192.168.1.1:8080/callback",
			expectedError: DCRErrorInvalidRedirectURI,
		},
		{
			name:          "invalid http public IP",
			uri:           "http://8.8.8.8/callback",
			expectedError: DCRErrorInvalidRedirectURI,
		},
		// Other invalid cases
		{
			name:          "invalid custom scheme",
			uri:           "myapp://callback",
			expectedError: DCRErrorInvalidRedirectURI,
		},
		{
			name:          "invalid malformed URI",
			uri:           "://invalid",
			expectedError: DCRErrorInvalidRedirectURI,
		},
		{
			name:          "invalid ftp scheme",
			uri:           "ftp://example.com/callback",
			expectedError: DCRErrorInvalidRedirectURI,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dcrErr := validateRedirectURI(tc.uri)

			if tc.expectedError != "" {
				if dcrErr == nil {
					t.Fatalf("expected error %q but got nil", tc.expectedError)
				}
				if dcrErr.Error != tc.expectedError {
					t.Errorf("expected error code %q, got %q", tc.expectedError, dcrErr.Error)
				}
				return
			}

			if dcrErr != nil {
				t.Fatalf("unexpected error: %s - %s", dcrErr.Error, dcrErr.ErrorDescription)
			}
		})
	}
}

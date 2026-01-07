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
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

const (
	// testTokenAuthMethodNone is the expected token auth method for public clients.
	testTokenAuthMethodNone = "none"
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

func TestRegisterClientHandler(t *testing.T) {
	t.Parallel()

	// Create a minimal storage for testing
	stor := storage.NewMemoryStorage()
	t.Cleanup(func() { stor.Close() })

	// Create a router with minimal dependencies
	router := &Router{
		storage: stor,
	}

	tests := []struct {
		name           string
		requestBody    any
		expectedStatus int
		checkResponse  func(t *testing.T, body []byte)
	}{
		{
			name: "valid DCR request",
			requestBody: DCRRequest{
				RedirectURIs: []string{"http://127.0.0.1:8080/callback"},
				ClientName:   "Test Client",
			},
			expectedStatus: http.StatusCreated,
			checkResponse: func(t *testing.T, body []byte) {
				t.Helper()
				var resp DCRResponse
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("failed to unmarshal response: %v", err)
				}
				if resp.ClientID == "" {
					t.Error("expected client_id to be set")
				}
				if resp.ClientName != "Test Client" {
					t.Errorf("expected client_name %q, got %q", "Test Client", resp.ClientName)
				}
				if resp.TokenEndpointAuthMethod != testTokenAuthMethodNone {
					t.Errorf("expected token_endpoint_auth_method %q, got %q", testTokenAuthMethodNone, resp.TokenEndpointAuthMethod)
				}
				if len(resp.RedirectURIs) != 1 || resp.RedirectURIs[0] != "http://127.0.0.1:8080/callback" {
					t.Errorf("unexpected redirect_uris: %v", resp.RedirectURIs)
				}
				if resp.ClientIDIssuedAt == 0 {
					t.Error("expected client_id_issued_at to be set")
				}
			},
		},
		{
			name: "valid DCR request with localhost",
			requestBody: DCRRequest{
				RedirectURIs: []string{"http://localhost:9999/callback"},
			},
			expectedStatus: http.StatusCreated,
			checkResponse: func(t *testing.T, body []byte) {
				t.Helper()
				var resp DCRResponse
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("failed to unmarshal response: %v", err)
				}
				if resp.ClientID == "" {
					t.Error("expected client_id to be set")
				}
				// Default values should be applied
				if len(resp.GrantTypes) == 0 {
					t.Error("expected grant_types to have defaults")
				}
				if len(resp.ResponseTypes) == 0 {
					t.Error("expected response_types to have defaults")
				}
			},
		},
		{
			name:           "invalid JSON",
			requestBody:    "not-valid-json",
			expectedStatus: http.StatusBadRequest,
			checkResponse: func(t *testing.T, body []byte) {
				t.Helper()
				var resp DCRError
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("failed to unmarshal error response: %v", err)
				}
				if resp.Error != DCRErrorInvalidClientMetadata {
					t.Errorf("expected error %q, got %q", DCRErrorInvalidClientMetadata, resp.Error)
				}
			},
		},
		{
			name: "missing redirect_uris",
			requestBody: DCRRequest{
				ClientName: "Test Client",
			},
			expectedStatus: http.StatusBadRequest,
			checkResponse: func(t *testing.T, body []byte) {
				t.Helper()
				var resp DCRError
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("failed to unmarshal error response: %v", err)
				}
				if resp.Error != DCRErrorInvalidRedirectURI {
					t.Errorf("expected error %q, got %q", DCRErrorInvalidRedirectURI, resp.Error)
				}
			},
		},
		{
			name: "https scheme allowed for any address",
			requestBody: DCRRequest{
				RedirectURIs: []string{"https://vscode.dev/redirect"},
			},
			expectedStatus: http.StatusCreated,
			checkResponse: func(t *testing.T, body []byte) {
				t.Helper()
				var resp DCRResponse
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("failed to unmarshal response: %v", err)
				}
				if resp.ClientID == "" {
					t.Error("expected client_id to be set")
				}
				if len(resp.RedirectURIs) != 1 || resp.RedirectURIs[0] != "https://vscode.dev/redirect" {
					t.Errorf("unexpected redirect_uris: %v", resp.RedirectURIs)
				}
			},
		},
		{
			name: "http scheme not allowed for non-loopback",
			requestBody: DCRRequest{
				RedirectURIs: []string{"http://example.com/callback"},
			},
			expectedStatus: http.StatusBadRequest,
			checkResponse: func(t *testing.T, body []byte) {
				t.Helper()
				var resp DCRError
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("failed to unmarshal error response: %v", err)
				}
				if resp.Error != DCRErrorInvalidRedirectURI {
					t.Errorf("expected error %q, got %q", DCRErrorInvalidRedirectURI, resp.Error)
				}
			},
		},
		{
			name: "non-none auth method",
			requestBody: DCRRequest{
				RedirectURIs:            []string{"http://127.0.0.1:8080/callback"},
				TokenEndpointAuthMethod: "client_secret_post",
			},
			expectedStatus: http.StatusBadRequest,
			checkResponse: func(t *testing.T, body []byte) {
				t.Helper()
				var resp DCRError
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("failed to unmarshal error response: %v", err)
				}
				if resp.Error != DCRErrorInvalidClientMetadata {
					t.Errorf("expected error %q, got %q", DCRErrorInvalidClientMetadata, resp.Error)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Encode request body
			var body []byte
			var err error
			if s, ok := tc.requestBody.(string); ok {
				body = []byte(s)
			} else {
				body, err = json.Marshal(tc.requestBody)
				if err != nil {
					t.Fatalf("failed to marshal request body: %v", err)
				}
			}

			// Create request
			req := httptest.NewRequest(http.MethodPost, "/oauth2/register", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")

			// Create response recorder
			w := httptest.NewRecorder()

			// Call handler
			router.RegisterClientHandler(w, req)

			// Check status code
			if w.Code != tc.expectedStatus {
				t.Errorf("expected status %d, got %d", tc.expectedStatus, w.Code)
			}

			// Check Content-Type header
			contentType := w.Header().Get("Content-Type")
			if contentType != "application/json" {
				t.Errorf("expected Content-Type %q, got %q", "application/json", contentType)
			}

			// Run custom checks
			if tc.checkResponse != nil {
				tc.checkResponse(t, w.Body.Bytes())
			}
		})
	}
}

func TestRegisterClientHandler_ClientIsStored(t *testing.T) {
	t.Parallel()

	// Create storage
	stor := storage.NewMemoryStorage()
	t.Cleanup(func() { stor.Close() })

	// Create router
	router := &Router{
		storage: stor,
	}

	// Register a client
	reqBody, err := json.Marshal(DCRRequest{
		RedirectURIs: []string{"http://127.0.0.1:8080/callback"},
		ClientName:   "Stored Client",
	})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/oauth2/register", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.RegisterClientHandler(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, w.Code)
	}

	// Parse response
	var resp DCRResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	// Verify client is stored
	client, err := stor.GetClient(req.Context(), resp.ClientID)
	if err != nil {
		t.Fatalf("failed to get client from storage: %v", err)
	}

	if client == nil {
		t.Fatal("expected client to be stored")
	}

	// Verify client is a LoopbackClient
	loopbackClient, ok := client.(*LoopbackClient)
	if !ok {
		t.Fatalf("expected LoopbackClient, got %T", client)
	}

	// Verify client properties
	if loopbackClient.GetID() != resp.ClientID {
		t.Errorf("expected client ID %q, got %q", resp.ClientID, loopbackClient.GetID())
	}

	if !loopbackClient.IsPublic() {
		t.Error("expected client to be public")
	}

	redirectURIs := loopbackClient.GetRedirectURIs()
	if len(redirectURIs) != 1 || redirectURIs[0] != "http://127.0.0.1:8080/callback" {
		t.Errorf("unexpected redirect URIs: %v", redirectURIs)
	}
}

func TestRegisterClientHandler_UniqueClientIDs(t *testing.T) {
	t.Parallel()

	// Create storage
	stor := storage.NewMemoryStorage()
	t.Cleanup(func() { stor.Close() })

	// Create router
	router := &Router{
		storage: stor,
	}

	clientIDs := make(map[string]bool)

	// Register multiple clients
	for i := 0; i < 10; i++ {
		reqBody, err := json.Marshal(DCRRequest{
			RedirectURIs: []string{"http://127.0.0.1:8080/callback"},
		})
		if err != nil {
			t.Fatalf("failed to marshal request: %v", err)
		}

		req := httptest.NewRequest(http.MethodPost, "/oauth2/register", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		router.RegisterClientHandler(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("expected status %d, got %d", http.StatusCreated, w.Code)
		}

		var resp DCRResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}

		if clientIDs[resp.ClientID] {
			t.Errorf("duplicate client ID: %s", resp.ClientID)
		}
		clientIDs[resp.ClientID] = true
	}
}

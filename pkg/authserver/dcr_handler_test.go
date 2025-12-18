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
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

const (
	// testTokenAuthMethodNone is the expected token auth method for public clients.
	testTokenAuthMethodNone = "none"
)

func TestRegisterClientHandler(t *testing.T) {
	t.Parallel()

	// Create a minimal storage for testing
	storage := NewMemoryStorage()
	t.Cleanup(func() { storage.Close() })

	// Create a router with minimal dependencies
	router := &Router{
		logger:  slog.Default(),
		storage: storage,
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
	storage := NewMemoryStorage()
	t.Cleanup(func() { storage.Close() })

	// Create router
	router := &Router{
		logger:  slog.Default(),
		storage: storage,
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
	client, err := storage.GetClient(req.Context(), resp.ClientID)
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
	storage := NewMemoryStorage()
	t.Cleanup(func() { storage.Close() })

	// Create router
	router := &Router{
		logger:  slog.Default(),
		storage: storage,
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

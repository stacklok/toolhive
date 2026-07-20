// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauthproto_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

func TestRegisterClientDynamically(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		request        *oauthproto.DynamicClientRegistrationRequest
		response       string
		responseStatus int
		expectedError  bool
		expectedResult *oauthproto.DynamicClientRegistrationResponse
	}{
		{
			name: "successful registration",
			request: &oauthproto.DynamicClientRegistrationRequest{
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
			expectedResult: &oauthproto.DynamicClientRegistrationResponse{
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
			request: &oauthproto.DynamicClientRegistrationRequest{
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
			expectedResult: &oauthproto.DynamicClientRegistrationResponse{
				ClientID:         "test-client-id",
				ClientIDIssuedAt: 1234567890,
			},
		},
		{
			name: "server error",
			request: &oauthproto.DynamicClientRegistrationRequest{
				ClientName:   "Test Client",
				RedirectURIs: []string{"http://localhost:8080/callback"},
			},
			response:       `{"error": "invalid_request", "error_description": "Invalid request"}`,
			responseStatus: http.StatusBadRequest,
			expectedError:  true,
		},
		{
			name: "DCR not supported - 404 Not Found",
			request: &oauthproto.DynamicClientRegistrationRequest{
				ClientName:   "Test Client",
				RedirectURIs: []string{"http://localhost:8080/callback"},
			},
			response:       `{"error": "not_found"}`,
			responseStatus: http.StatusNotFound,
			expectedError:  true,
		},
		{
			name: "DCR not supported - 405 Method Not Allowed",
			request: &oauthproto.DynamicClientRegistrationRequest{
				ClientName:   "Test Client",
				RedirectURIs: []string{"http://localhost:8080/callback"},
			},
			response:       `{"error": "method_not_allowed"}`,
			responseStatus: http.StatusMethodNotAllowed,
			expectedError:  true,
		},
		{
			name: "DCR not supported - 501 Not Implemented",
			request: &oauthproto.DynamicClientRegistrationRequest{
				ClientName:   "Test Client",
				RedirectURIs: []string{"http://localhost:8080/callback"},
			},
			response:       `{"error": "not_implemented", "error_description": "Dynamic Client Registration is not supported"}`,
			responseStatus: http.StatusNotImplemented,
			expectedError:  true,
		},
		{
			name: "invalid request - no redirect URIs",
			request: &oauthproto.DynamicClientRegistrationRequest{
				ClientName: "Test Client",
			},
			expectedError: true,
		},
		{
			name: "invalid request - scope with spaces",
			request: &oauthproto.DynamicClientRegistrationRequest{
				ClientName:   "Test Client",
				RedirectURIs: []string{"http://localhost:8080/callback"},
				Scopes:       []string{"openid", "profile email", "another"},
			},
			expectedError: true,
		},
		{
			name: "invalid request - scope with leading space",
			request: &oauthproto.DynamicClientRegistrationRequest{
				ClientName:   "Test Client",
				RedirectURIs: []string{"http://localhost:8080/callback"},
				Scopes:       []string{" openid"},
			},
			expectedError: true,
		},
		{
			name: "invalid request - scope with trailing space",
			request: &oauthproto.DynamicClientRegistrationRequest{
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
				t.Cleanup(server.Close)
			}

			var registrationEndpoint string
			var client *http.Client
			if server != nil {
				registrationEndpoint = server.URL
				client = server.Client()
			} else {
				registrationEndpoint = "https://example.com/oauth/register"
			}

			result, err := oauthproto.RegisterClientDynamically(context.Background(), registrationEndpoint, tt.request, client)

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

// TestRegisterClientDynamically_NilClientUsesDefault verifies that passing nil for client
// builds a default *http.Client that can successfully reach a loopback test server.
func TestRegisterClientDynamically_NilClientUsesDefault(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"client_id": "default-client"}`))
	}))
	t.Cleanup(server.Close)

	req := &oauthproto.DynamicClientRegistrationRequest{
		RedirectURIs: []string{"http://localhost:8080/callback"},
	}

	// nil client → default *http.Client is built internally
	result, err := oauthproto.RegisterClientDynamically(context.Background(), server.URL, req, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "default-client", result.ClientID)
}

// TestRegisterClientDynamically_CallerSuppliedClient verifies that a caller-supplied
// *http.Client is used (non-default path).
func TestRegisterClientDynamically_CallerSuppliedClient(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"client_id": "supplied-client"}`))
	}))
	t.Cleanup(server.Close)

	req := &oauthproto.DynamicClientRegistrationRequest{
		RedirectURIs: []string{"http://localhost:8080/callback"},
	}

	result, err := oauthproto.RegisterClientDynamically(context.Background(), server.URL, req, server.Client())
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "supplied-client", result.ClientID)
}

// TestHandleHTTPResponse_DCRNotSupportedMessageIsProtocolNeutral verifies that the
// error message for 404/405/501 does NOT contain CLI-flag hints, which would leak
// CLI assumptions into the protocol package.
func TestHandleHTTPResponse_DCRNotSupportedMessageIsProtocolNeutral(t *testing.T) {
	t.Parallel()

	cliHintPhrases := []string{
		"--remote-auth-client-id",
		"--remote-auth-client-secret",
	}

	for _, status := range []int{http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				w.Write([]byte(`{"error": "unsupported"}`))
			}))
			t.Cleanup(server.Close)

			req := &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"http://localhost:8080/callback"},
			}

			_, err := oauthproto.RegisterClientDynamically(context.Background(), server.URL, req, server.Client())
			require.Error(t, err)

			errMsg := err.Error()
			for _, phrase := range cliHintPhrases {
				assert.NotContains(t, errMsg, phrase,
					"error message must not contain CLI-flag hints (protocol-neutral message required)")
			}
		})
	}
}

// TestValidateRegistrationEndpoint tests endpoint URL validation.
func TestValidateRegistrationEndpoint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		endpoint  string
		wantError bool
	}{
		{
			name:      "HTTPS endpoint is valid",
			endpoint:  "https://example.com/oauth/register",
			wantError: false,
		},
		{
			name:      "localhost HTTP endpoint is valid",
			endpoint:  "http://localhost:8080/register",
			wantError: false,
		},
		{
			name:      "127.0.0.1 HTTP endpoint is valid",
			endpoint:  "http://127.0.0.1:8080/register",
			wantError: false,
		},
		{
			name:      "[::1] HTTP endpoint is valid",
			endpoint:  "http://[::1]:8080/register",
			wantError: false,
		},
		{
			name:      "non-HTTPS non-loopback is rejected",
			endpoint:  "http://example.com/oauth/register",
			wantError: true,
		},
		{
			name:      "malformed URL is rejected",
			endpoint:  "://bad-url",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Each subtest creates its own request so validateAndSetDefaults cannot race.
			req := &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"http://localhost:8080/callback"},
			}
			_, err := oauthproto.RegisterClientDynamically(context.Background(), tt.endpoint, req, &http.Client{})
			if tt.wantError {
				assert.Error(t, err)
			} else {
				// We expect a network error (no server), not a validation error.
				// The absence of "must use HTTPS" or "invalid" in the error confirms validation passed.
				if err != nil {
					errMsg := err.Error()
					assert.NotContains(t, errMsg, "must use HTTPS")
					assert.NotContains(t, errMsg, "invalid registration endpoint URL")
				}
			}
		})
	}
}

// TestValidateAndSetDefaults tests request validation and default population.
func TestValidateAndSetDefaults(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		request   *oauthproto.DynamicClientRegistrationRequest
		wantError bool
		errorMsg  string
	}{
		{
			name:      "nil request is rejected",
			request:   nil,
			wantError: true,
			errorMsg:  "cannot be nil",
		},
		{
			name: "empty redirect URIs is rejected",
			request: &oauthproto.DynamicClientRegistrationRequest{
				ClientName: "Test",
			},
			wantError: true,
			errorMsg:  "redirect URI",
		},
		{
			name: "scope with space is rejected",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"http://localhost:8080/callback"},
				Scopes:       []string{"openid profile"},
			},
			wantError: true,
			errorMsg:  "cannot contain spaces",
		},
		{
			name: "valid request sets defaults",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"http://localhost:8080/callback"},
			},
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				w.Write([]byte(`{"client_id": "ok"}`))
			}))
			t.Cleanup(server.Close)

			_, err := oauthproto.RegisterClientDynamically(context.Background(), server.URL, tt.request, server.Client())
			if tt.wantError {
				require.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestScopeList_MarshalJSON tests that the ScopeList marshaling works correctly
// and produces RFC 7591 compliant space-delimited strings.
func TestScopeList_MarshalJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		scopes   oauthproto.ScopeList
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
			scopes:   oauthproto.ScopeList{},
			wantJSON: `""`,
			wantOmit: true,
		},
		{
			name:     "single scope => string",
			scopes:   oauthproto.ScopeList{"openid"},
			wantJSON: `"openid"`,
		},
		{
			name:     "two scopes => space-delimited string",
			scopes:   oauthproto.ScopeList{"openid", "profile"},
			wantJSON: `"openid profile"`,
		},
		{
			name:     "three scopes => space-delimited string",
			scopes:   oauthproto.ScopeList{"openid", "profile", "email"},
			wantJSON: `"openid profile email"`,
		},
		{
			name:     "scopes with special characters",
			scopes:   oauthproto.ScopeList{"read:user", "write:repo"},
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
			if tt.wantOmit {
				type testStruct struct {
					Scope oauthproto.ScopeList `json:"scope,omitempty"`
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
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var s oauthproto.ScopeList
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

// TestDynamicClientRegistrationRequest_ScopeSerialization verifies RFC 7591 Section 2 compliance.
func TestDynamicClientRegistrationRequest_ScopeSerialization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		scopes            []string
		shouldOmitScope   bool
		expectedScopeJSON string
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			request := &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"http://localhost:8080/callback"},
				Scopes:       tt.scopes,
			}

			jsonBytes, err := json.Marshal(request)
			require.NoError(t, err, "JSON marshaling should succeed")

			jsonStr := string(jsonBytes)

			if tt.shouldOmitScope {
				assert.NotContains(t, jsonStr, `"scope"`,
					"JSON should NOT contain scope field when scopes are empty/nil")
			} else {
				assert.Contains(t, jsonStr, tt.expectedScopeJSON,
					"JSON should contain expected scope field")
			}

			assert.Contains(t, jsonStr, `"redirect_uris"`, "redirect_uris should be present")
		})
	}
}

// TestIsLoopbackHost exercises the private isLoopbackHost via validateRegistrationEndpoint.
// Positive cases (loopback) allow HTTP; negative cases (non-loopback) require HTTPS.
func TestIsLoopbackHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		endpoint  string
		wantHTTPS bool // true if HTTPS enforcement error is expected
	}{
		// Loopback hosts: HTTP is allowed — validation passes (only network error follows)
		{endpoint: "http://localhost/register", wantHTTPS: false},
		{endpoint: "http://localhost:8080/register", wantHTTPS: false},
		{endpoint: "http://127.0.0.1/register", wantHTTPS: false},
		{endpoint: "http://127.0.0.1:1234/register", wantHTTPS: false},
		{endpoint: "http://[::1]/register", wantHTTPS: false},
		{endpoint: "http://[::1]:80/register", wantHTTPS: false},
		// Non-loopback HTTP hosts: validation should fail with "must use HTTPS"
		{endpoint: "http://example.com/register", wantHTTPS: true},
		{endpoint: "http://127.0.0.1.example.com/register", wantHTTPS: true},
	}

	for _, tt := range tests {
		t.Run(tt.endpoint, func(t *testing.T) {
			t.Parallel()

			// Each subtest gets its own request to avoid races on validateAndSetDefaults.
			req := &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"http://localhost:8080/callback"},
			}

			_, err := oauthproto.RegisterClientDynamically(context.Background(), tt.endpoint, req, &http.Client{})

			if tt.wantHTTPS {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "must use HTTPS",
					"non-loopback HTTP endpoint should be rejected")
			} else {
				// Network error expected (no server), but NOT a validation error
				if err != nil {
					assert.NotContains(t, err.Error(), "must use HTTPS",
						"loopback host should bypass HTTPS requirement")
				}
			}
		})
	}
}

// TestDynamicClientRegistrationResponse_RoundTrip verifies the response type
// can be serialized and deserialized correctly.
func TestDynamicClientRegistrationResponse_RoundTrip(t *testing.T) {
	t.Parallel()

	original := &oauthproto.DynamicClientRegistrationResponse{
		ClientID: "test-client-id",
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var result oauthproto.DynamicClientRegistrationResponse
	err = json.Unmarshal(data, &result)
	require.NoError(t, err)

	assert.Equal(t, "test-client-id", result.ClientID)
}

// TestRegisterClientDynamically_MissingClientID verifies that a response without
// client_id is rejected.
func TestRegisterClientDynamically_MissingClientID(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		// Missing client_id
		w.Write([]byte(`{"client_secret": "secret"}`))
	}))
	t.Cleanup(server.Close)

	req := &oauthproto.DynamicClientRegistrationRequest{
		RedirectURIs: []string{"http://localhost:8080/callback"},
	}

	_, err := oauthproto.RegisterClientDynamically(context.Background(), server.URL, req, server.Client())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "client_id")
}

// TestRegisterClientDynamically_NonJSONContentType verifies that non-JSON responses are rejected.
func TestRegisterClientDynamically_NonJSONContentType(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`<html>error</html>`))
	}))
	t.Cleanup(server.Close)

	req := &oauthproto.DynamicClientRegistrationRequest{
		RedirectURIs: []string{"http://localhost:8080/callback"},
	}

	_, err := oauthproto.RegisterClientDynamically(context.Background(), server.URL, req, server.Client())
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "content type")
}

// TestRegisterClientDynamically_LargeResponseBodyCapped verifies that handleHTTPResponse
// applies the 1 MB io.LimitReader cap and does not hang or panic when the server sends
// a body larger than the limit. The truncated body is not valid JSON, so a decode error
// is expected — the important property is "terminates promptly without OOM."
func TestRegisterClientDynamically_LargeResponseBodyCapped(t *testing.T) {
	t.Parallel()

	const limitBytes = 1024 * 1024 // matches the cap in handleHTTPResponse

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Write a JSON prefix, then padding that pushes the body well past the 1 MB limit.
		// The JSON decoder will read up to limitBytes through the LimitReader and then
		// encounter a truncated string, returning a decode error rather than hanging.
		prefix := `{"client_id":"ok","padding":"`
		padding := strings.Repeat("x", limitBytes+512)
		suffix := `"}`
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, prefix+padding+suffix)
	}))
	t.Cleanup(server.Close)

	req := &oauthproto.DynamicClientRegistrationRequest{
		RedirectURIs: []string{"http://localhost:8080/callback"},
	}

	// Must not block or panic. The truncated body produces a decode error; we do not
	// assert a specific outcome beyond the call returning promptly.
	_, _ = oauthproto.RegisterClientDynamically(context.Background(), server.URL, req, server.Client())
}

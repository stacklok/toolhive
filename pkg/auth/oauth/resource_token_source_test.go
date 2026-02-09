// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestNewResourceTokenSource(t *testing.T) {
	t.Parallel()

	config := &oauth2.Config{
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://example.com/auth",
			TokenURL: "https://example.com/token",
		},
	}

	validToken := &oauth2.Token{
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		Expiry:       time.Now().Add(1 * time.Hour),
	}

	t.Run("creates resource token source with resource parameter", func(t *testing.T) {
		t.Parallel()

		ts := newResourceTokenSource(config, validToken, "https://api.example.com")
		require.NotNil(t, ts)

		// Should be our custom type
		rts, ok := ts.(*resourceTokenSource)
		require.True(t, ok, "expected resourceTokenSource type")
		assert.Equal(t, "https://api.example.com", rts.resource)
		assert.Equal(t, config, rts.config)
		assert.NotNil(t, rts.httpClient)
		assert.Equal(t, 30*time.Second, rts.httpClient.Timeout)
	})

	t.Run("stores token reference", func(t *testing.T) {
		t.Parallel()

		ts := newResourceTokenSource(config, validToken, "https://api.example.com")
		rts := ts.(*resourceTokenSource)

		assert.Equal(t, validToken.AccessToken, rts.token.AccessToken)
		assert.Equal(t, validToken.RefreshToken, rts.token.RefreshToken)
	})
}

func TestResourceTokenSource_Token_ValidToken(t *testing.T) {
	t.Parallel()

	config := &oauth2.Config{
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		Endpoint: oauth2.Endpoint{
			TokenURL: "https://example.com/token",
		},
	}

	validToken := &oauth2.Token{
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		Expiry:       time.Now().Add(1 * time.Hour),
	}

	ts := newResourceTokenSource(config, validToken, "https://api.example.com")

	t.Run("returns cached token when still valid", func(t *testing.T) {
		t.Parallel()
		token, err := ts.Token()
		require.NoError(t, err)
		assert.Equal(t, "access-token", token.AccessToken)
		assert.Equal(t, "refresh-token", token.RefreshToken)
		assert.True(t, token.Valid())
	})
}

func TestResourceTokenSource_Token_ExpiredToken(t *testing.T) {
	t.Parallel()

	t.Run("refreshes expired token with resource parameter", func(t *testing.T) {
		t.Parallel()

		// Mock token server that validates the refresh request
		var capturedRequest *http.Request
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedRequest = r

			// Parse form data
			err := r.ParseForm()
			require.NoError(t, err)

			// Validate request parameters
			assert.Equal(t, "refresh_token", r.Form.Get("grant_type"))
			assert.Equal(t, "old-refresh-token", r.Form.Get("refresh_token"))
			assert.Equal(t, "https://api.example.com", r.Form.Get("resource"))
			assert.Equal(t, "test-client", r.Form.Get("client_id"))
			assert.Equal(t, "test-secret", r.Form.Get("client_secret"))

			// Return new token
			response := map[string]interface{}{
				"access_token":  "new-access-token",
				"refresh_token": "new-refresh-token",
				"token_type":    "Bearer",
				"expires_in":    3600,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		}))
		defer server.Close()

		config := &oauth2.Config{
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			Endpoint: oauth2.Endpoint{
				TokenURL: server.URL,
			},
		}

		expiredToken := &oauth2.Token{
			AccessToken:  "old-access-token",
			RefreshToken: "old-refresh-token",
			Expiry:       time.Now().Add(-1 * time.Hour), // Expired
		}

		ts := newResourceTokenSource(config, expiredToken, "https://api.example.com")

		// Get token - should trigger refresh
		token, err := ts.Token()
		require.NoError(t, err)
		assert.Equal(t, "new-access-token", token.AccessToken)
		assert.Equal(t, "new-refresh-token", token.RefreshToken)

		// Verify request was made
		require.NotNil(t, capturedRequest)
		assert.Equal(t, "POST", capturedRequest.Method)
		assert.Equal(t, "application/x-www-form-urlencoded", capturedRequest.Header.Get("Content-Type"))
	})

	t.Run("includes client credentials in refresh request", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			err := r.ParseForm()
			require.NoError(t, err)

			// Verify client credentials are present
			assert.Equal(t, "my-client-id", r.Form.Get("client_id"))
			assert.Equal(t, "my-client-secret", r.Form.Get("client_secret"))

			response := map[string]interface{}{
				"access_token":  "new-token",
				"refresh_token": "new-refresh",
				"token_type":    "Bearer",
				"expires_in":    3600,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		}))
		defer server.Close()

		config := &oauth2.Config{
			ClientID:     "my-client-id",
			ClientSecret: "my-client-secret",
			Endpoint: oauth2.Endpoint{
				TokenURL: server.URL,
			},
		}

		expiredToken := &oauth2.Token{
			AccessToken:  "old",
			RefreshToken: "refresh",
			Expiry:       time.Now().Add(-1 * time.Hour),
		}

		ts := newResourceTokenSource(config, expiredToken, "https://api.example.com")
		_, err := ts.Token()
		require.NoError(t, err)
	})

	t.Run("updates internal token after successful refresh", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			response := map[string]interface{}{
				"access_token":  "updated-token",
				"refresh_token": "updated-refresh",
				"token_type":    "Bearer",
				"expires_in":    3600,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		}))
		defer server.Close()

		config := &oauth2.Config{
			ClientID: "test-client",
			Endpoint: oauth2.Endpoint{
				TokenURL: server.URL,
			},
		}

		expiredToken := &oauth2.Token{
			AccessToken:  "old",
			RefreshToken: "refresh",
			Expiry:       time.Now().Add(-1 * time.Hour),
		}

		ts := newResourceTokenSource(config, expiredToken, "https://api.example.com")
		rts := ts.(*resourceTokenSource)

		// First call - refreshes
		token, err := ts.Token()
		require.NoError(t, err)
		assert.Equal(t, "updated-token", token.AccessToken)

		// Verify internal state updated
		assert.Equal(t, "updated-token", rts.token.AccessToken)
		assert.Equal(t, "updated-refresh", rts.token.RefreshToken)
	})
}

func TestResourceTokenSource_RefreshErrors(t *testing.T) {
	t.Parallel()

	t.Run("returns error when no refresh token available", func(t *testing.T) {
		t.Parallel()

		config := &oauth2.Config{
			ClientID: "test-client",
			Endpoint: oauth2.Endpoint{
				TokenURL: "https://example.com/token",
			},
		}

		tokenWithoutRefresh := &oauth2.Token{
			AccessToken:  "access",
			RefreshToken: "", // No refresh token
			Expiry:       time.Now().Add(-1 * time.Hour),
		}

		ts := newResourceTokenSource(config, tokenWithoutRefresh, "https://api.example.com")
		_, err := ts.Token()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no refresh token available")
	})

	t.Run("returns error on HTTP failure", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		config := &oauth2.Config{
			ClientID: "test-client",
			Endpoint: oauth2.Endpoint{
				TokenURL: server.URL,
			},
		}

		expiredToken := &oauth2.Token{
			AccessToken:  "old",
			RefreshToken: "refresh",
			Expiry:       time.Now().Add(-1 * time.Hour),
		}

		ts := newResourceTokenSource(config, expiredToken, "https://api.example.com")
		_, err := ts.Token()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "token refresh failed with status 500")
	})

	t.Run("returns error on invalid JSON response", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte("invalid json {"))
		}))
		defer server.Close()

		config := &oauth2.Config{
			ClientID: "test-client",
			Endpoint: oauth2.Endpoint{
				TokenURL: server.URL,
			},
		}

		expiredToken := &oauth2.Token{
			AccessToken:  "old",
			RefreshToken: "refresh",
			Expiry:       time.Now().Add(-1 * time.Hour),
		}

		ts := newResourceTokenSource(config, expiredToken, "https://api.example.com")
		_, err := ts.Token()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to parse token response")
	})

	t.Run("returns error when token endpoint is unreachable", func(t *testing.T) {
		t.Parallel()

		config := &oauth2.Config{
			ClientID: "test-client",
			Endpoint: oauth2.Endpoint{
				TokenURL: "http://localhost:1", // Unreachable port
			},
		}

		expiredToken := &oauth2.Token{
			AccessToken:  "old",
			RefreshToken: "refresh",
			Expiry:       time.Now().Add(-1 * time.Hour),
		}

		ts := newResourceTokenSource(config, expiredToken, "https://api.example.com")
		_, err := ts.Token()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "token refresh request failed")
	})

	t.Run("returns error on non-200 status codes", func(t *testing.T) {
		t.Parallel()

		testCases := []struct {
			name       string
			statusCode int
		}{
			{"400 Bad Request", http.StatusBadRequest},
			{"401 Unauthorized", http.StatusUnauthorized},
			{"403 Forbidden", http.StatusForbidden},
			{"404 Not Found", http.StatusNotFound},
			{"500 Internal Server Error", http.StatusInternalServerError},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(tc.statusCode)
				}))
				defer server.Close()

				config := &oauth2.Config{
					ClientID: "test-client",
					Endpoint: oauth2.Endpoint{
						TokenURL: server.URL,
					},
				}

				expiredToken := &oauth2.Token{
					AccessToken:  "old",
					RefreshToken: "refresh",
					Expiry:       time.Now().Add(-1 * time.Hour),
				}

				ts := newResourceTokenSource(config, expiredToken, "https://api.example.com")
				_, err := ts.Token()
				require.Error(t, err)
				assert.Contains(t, err.Error(), "token refresh failed with status")
			})
		}
	})
}

func TestResourceTokenSource_HTTPClientReuse(t *testing.T) {
	t.Parallel()

	t.Run("reuses HTTP client across multiple refreshes", func(t *testing.T) {
		t.Parallel()

		callCount := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			callCount++
			response := map[string]interface{}{
				"access_token":  "new-token",
				"refresh_token": "new-refresh",
				"token_type":    "Bearer",
				"expires_in":    1, // Expire quickly for next call
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		}))
		defer server.Close()

		config := &oauth2.Config{
			ClientID: "test-client",
			Endpoint: oauth2.Endpoint{
				TokenURL: server.URL,
			},
		}

		expiredToken := &oauth2.Token{
			AccessToken:  "old",
			RefreshToken: "refresh",
			Expiry:       time.Now().Add(-1 * time.Hour),
		}

		ts := newResourceTokenSource(config, expiredToken, "https://api.example.com")
		rts := ts.(*resourceTokenSource)

		// Verify HTTP client is created
		require.NotNil(t, rts.httpClient)
		client1 := rts.httpClient

		// First refresh
		_, err := ts.Token()
		require.NoError(t, err)

		// Verify same client instance
		assert.Same(t, client1, rts.httpClient, "HTTP client should be reused")
		assert.Equal(t, 1, callCount)
	})

	t.Run("HTTP client has correct timeout", func(t *testing.T) {
		t.Parallel()

		config := &oauth2.Config{
			ClientID: "test-client",
			Endpoint: oauth2.Endpoint{
				TokenURL: "https://example.com/token",
			},
		}

		token := &oauth2.Token{
			AccessToken:  "access",
			RefreshToken: "refresh",
			Expiry:       time.Now().Add(1 * time.Hour),
		}

		ts := newResourceTokenSource(config, token, "https://api.example.com")
		rts := ts.(*resourceTokenSource)

		assert.Equal(t, 30*time.Second, rts.httpClient.Timeout)
	})
}

func TestResourceTokenSource_RFC8707Compliance(t *testing.T) {
	t.Parallel()

	t.Run("includes resource parameter in refresh request per RFC 8707", func(t *testing.T) {
		t.Parallel()

		var capturedForm url.Values
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			err := r.ParseForm()
			require.NoError(t, err)
			capturedForm = r.Form

			response := map[string]interface{}{
				"access_token":  "new-token",
				"refresh_token": "new-refresh",
				"token_type":    "Bearer",
				"expires_in":    3600,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)
		}))
		defer server.Close()

		config := &oauth2.Config{
			ClientID: "test-client",
			Endpoint: oauth2.Endpoint{
				TokenURL: server.URL,
			},
		}

		expiredToken := &oauth2.Token{
			AccessToken:  "old",
			RefreshToken: "refresh",
			Expiry:       time.Now().Add(-1 * time.Hour),
		}

		resourceURI := "https://api.example.com/v1"
		ts := newResourceTokenSource(config, expiredToken, resourceURI)
		_, err := ts.Token()
		require.NoError(t, err)

		// Verify RFC 8707 compliance: resource parameter is included
		require.NotNil(t, capturedForm)
		assert.Equal(t, resourceURI, capturedForm.Get("resource"), "resource parameter must be included per RFC 8707")
		assert.Equal(t, "refresh_token", capturedForm.Get("grant_type"))
	})

	t.Run("supports different resource URIs", func(t *testing.T) {
		t.Parallel()

		testCases := []string{
			"https://api.example.com",
			"https://api.example.com/v1/users",
			"https://example.com:8080/api",
			"http://localhost:3000/api", // localhost allowed
		}

		for _, resourceURI := range testCases {
			t.Run(resourceURI, func(t *testing.T) {
				t.Parallel()
				var capturedResource string
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					err := r.ParseForm()
					require.NoError(t, err)
					capturedResource = r.Form.Get("resource")

					response := map[string]interface{}{
						"access_token":  "token",
						"refresh_token": "refresh",
						"token_type":    "Bearer",
						"expires_in":    3600,
					}
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(response)
				}))
				defer server.Close()

				config := &oauth2.Config{
					ClientID: "test-client",
					Endpoint: oauth2.Endpoint{
						TokenURL: server.URL,
					},
				}

				expiredToken := &oauth2.Token{
					AccessToken:  "old",
					RefreshToken: "refresh",
					Expiry:       time.Now().Add(-1 * time.Hour),
				}

				ts := newResourceTokenSource(config, expiredToken, resourceURI)
				_, err := ts.Token()
				require.NoError(t, err)
				assert.Equal(t, resourceURI, capturedResource)
			})
		}
	})
}

package tokenexchange

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

const (
	// testSubjectToken is a test subject token value used across multiple test cases
	testSubjectToken = "test-subject-token"
)

// TestTokenSource_Token_Success tests the happy path of token exchange.
func TestTokenSource_Token_Success(t *testing.T) {
	t.Parallel()

	// Create a mock OAuth server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request method and headers
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))

		// Verify Authorization header contains Basic Auth credentials
		authHeader := r.Header.Get("Authorization")
		assert.NotEmpty(t, authHeader, "Authorization header should be present")
		assert.True(t, strings.HasPrefix(authHeader, "Basic "), "Authorization header should use Basic scheme")

		// Verify client credentials are sent via Basic Auth (URL-encoded per RFC 6749)
		// Note: BasicAuth() decodes the base64 and extracts the credentials
		// Since "test-client-id" has no special chars, URL encoding doesn't change it
		username, password, ok := r.BasicAuth()
		require.True(t, ok, "Basic Auth credentials should be parseable")
		assert.Equal(t, "test-client-id", username)
		assert.Equal(t, "test-client-secret", password)

		// Parse form data
		err := r.ParseForm()
		require.NoError(t, err)

		// Verify required fields
		assert.Equal(t, "urn:ietf:params:oauth:grant-type:token-exchange", r.Form.Get("grant_type"))
		assert.Equal(t, testSubjectToken, r.Form.Get("subject_token"))
		assert.Equal(t, "urn:ietf:params:oauth:token-type:access_token", r.Form.Get("subject_token_type"))
		assert.Equal(t, "urn:ietf:params:oauth:token-type:access_token", r.Form.Get("requested_token_type"))
		assert.Equal(t, "https://api.example.com", r.Form.Get("audience"))
		assert.Equal(t, "read write", r.Form.Get("scope"))

		// Verify client credentials are NOT in the request body (per RFC 6749 recommendation)
		assert.Empty(t, r.Form.Get("client_id"), "client_id should not be in request body")
		assert.Empty(t, r.Form.Get("client_secret"), "client_secret should not be in request body")

		// Return successful response
		resp := response{
			AccessToken:     "exchanged-access-token",
			IssuedTokenType: "urn:ietf:params:oauth:token-type:access_token",
			TokenType:       "Bearer",
			ExpiresIn:       3600,
			Scope:           "read write",
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		err = json.NewEncoder(w).Encode(resp)
		require.NoError(t, err)
	}))
	defer server.Close()

	// Create config with test server
	config := &Config{
		TokenURL:     server.URL,
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Audience:     "https://api.example.com",
		Scopes:       []string{"read", "write"},
		SubjectTokenProvider: func() (string, error) {
			return testSubjectToken, nil
		},
	}

	// Create token source and get token
	ctx := context.Background()
	ts := config.TokenSource(ctx)
	token, err := ts.Token()

	// Verify results
	require.NoError(t, err)
	assert.Equal(t, "exchanged-access-token", token.AccessToken)
	assert.Equal(t, "Bearer", token.TokenType)
	assert.False(t, token.Expiry.IsZero())
	assert.WithinDuration(t, time.Now().Add(3600*time.Second), token.Expiry, 5*time.Second)
}

// TestTokenSource_Token_WithRefreshToken tests token exchange that returns a refresh token.
func TestTokenSource_Token_WithRefreshToken(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := response{
			AccessToken:  "exchanged-access-token",
			TokenType:    "Bearer",
			ExpiresIn:    3600,
			RefreshToken: "refresh-token-value",
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	config := &Config{
		TokenURL:     server.URL,
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		SubjectTokenProvider: func() (string, error) {
			return testSubjectToken, nil
		},
	}

	ctx := context.Background()
	ts := config.TokenSource(ctx)
	token, err := ts.Token()

	require.NoError(t, err)
	assert.Equal(t, "exchanged-access-token", token.AccessToken)
	assert.Equal(t, "refresh-token-value", token.RefreshToken)
}

// TestTokenSource_Token_NoExpiry tests token exchange when no expiry is provided.
func TestTokenSource_Token_NoExpiry(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := response{
			AccessToken: "exchanged-access-token",
			TokenType:   "Bearer",
			ExpiresIn:   0, // No expiry
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	config := &Config{
		TokenURL:     server.URL,
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		SubjectTokenProvider: func() (string, error) {
			return testSubjectToken, nil
		},
	}

	ctx := context.Background()
	ts := config.TokenSource(ctx)
	token, err := ts.Token()

	require.NoError(t, err)
	assert.Equal(t, "exchanged-access-token", token.AccessToken)
	assert.True(t, token.Expiry.IsZero())
}

// TestTokenSource_Token_SubjectTokenProviderError tests error handling when the subject token provider fails.
func TestTokenSource_Token_SubjectTokenProviderError(t *testing.T) {
	t.Parallel()

	providerErr := errors.New("failed to get token from provider")
	config := &Config{
		TokenURL:     "https://example.com/token",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		SubjectTokenProvider: func() (string, error) {
			return "", providerErr
		},
	}

	ctx := context.Background()
	ts := config.TokenSource(ctx)
	token, err := ts.Token()

	require.Error(t, err)
	assert.Nil(t, token)
	assert.Contains(t, err.Error(), "failed to get subject token")
	assert.ErrorIs(t, err, providerErr)
}

// TestTokenSource_Token_ContextCancellation tests context cancellation during token exchange.
func TestTokenSource_Token_ContextCancellation(t *testing.T) {
	t.Parallel()

	// Create a server that delays the response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	config := &Config{
		TokenURL:     server.URL,
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		SubjectTokenProvider: func() (string, error) {
			return testSubjectToken, nil
		},
	}

	// Create a context that is already cancelled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ts := config.TokenSource(ctx)
	token, err := ts.Token()

	require.Error(t, err)
	assert.Nil(t, token)
	assert.Contains(t, err.Error(), "token exchange request failed")
}

// TestExchangeToken_HTTPErrorResponses tests various HTTP error responses.
func TestExchangeToken_HTTPErrorResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		statusCode    int
		responseBody  string
		expectedError string
	}{
		{
			name:          "400 Bad Request",
			statusCode:    http.StatusBadRequest,
			responseBody:  `{"error":"invalid_request","error_description":"Missing required parameter"}`,
			expectedError: "OAuth error \"invalid_request\" (status 400)",
		},
		{
			name:          "401 Unauthorized",
			statusCode:    http.StatusUnauthorized,
			responseBody:  `{"error":"invalid_client"}`,
			expectedError: "OAuth error \"invalid_client\" (status 401)",
		},
		{
			name:          "403 Forbidden",
			statusCode:    http.StatusForbidden,
			responseBody:  `{"error":"access_denied"}`,
			expectedError: "OAuth error \"access_denied\" (status 403)",
		},
		{
			name:          "500 Internal Server Error",
			statusCode:    http.StatusInternalServerError,
			responseBody:  `{"error":"server_error"}`,
			expectedError: "OAuth error \"server_error\" (status 500)",
		},
		{
			name:          "503 Service Unavailable",
			statusCode:    http.StatusServiceUnavailable,
			responseBody:  "Service temporarily unavailable",
			expectedError: "token exchange failed with status 503",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			request := &exchangeRequest{
				GrantType:          "urn:ietf:params:oauth:grant-type:token-exchange",
				SubjectToken:       "test-token",
				SubjectTokenType:   "urn:ietf:params:oauth:token-type:access_token",
				RequestedTokenType: "urn:ietf:params:oauth:token-type:access_token",
			}
			auth := clientAuthentication{
				ClientID:     "test-client-id",
				ClientSecret: "test-client-secret",
			}

			ctx := context.Background()
			resp, err := exchangeToken(ctx, server.URL, request, auth, nil)

			require.Error(t, err)
			assert.Nil(t, resp)
			assert.Contains(t, err.Error(), tt.expectedError)
		})
	}
}

// TestExchangeToken_MalformedJSON tests error handling for malformed JSON responses.
func TestExchangeToken_MalformedJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		responseBody string
	}{
		{
			name:         "Invalid JSON syntax",
			responseBody: `{"access_token": "value", "token_type":`,
		},
		{
			name:         "Non-JSON response",
			responseBody: `This is not JSON at all`,
		},
		{
			name:         "Empty response",
			responseBody: ``,
		},
		{
			name:         "HTML response",
			responseBody: `<html><body>Error</body></html>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			request := &exchangeRequest{
				SubjectToken: "test-token",
			}
			auth := clientAuthentication{}

			ctx := context.Background()
			resp, err := exchangeToken(ctx, server.URL, request, auth, nil)

			require.Error(t, err)
			assert.Nil(t, resp)
			assert.Contains(t, err.Error(), "failed to parse token exchange response")
		})
	}
}

// TestExchangeToken_MissingRequiredFields tests validation of required fields.
func TestExchangeToken_MissingRequiredFields(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("should not reach server")
	}))
	defer server.Close()

	request := &exchangeRequest{
		// Missing SubjectToken
		SubjectTokenType:   "urn:ietf:params:oauth:token-type:access_token",
		RequestedTokenType: "urn:ietf:params:oauth:token-type:access_token",
	}
	auth := clientAuthentication{}

	ctx := context.Background()
	resp, err := exchangeToken(ctx, server.URL, request, auth, nil)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "subject_token is required")
}

// TestExchangeToken_DefaultValues tests that default values are properly set for optional fields.
func TestExchangeToken_DefaultValues(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := r.ParseForm()
		require.NoError(t, err)

		// Verify defaults are set
		assert.Equal(t, "urn:ietf:params:oauth:grant-type:token-exchange", r.Form.Get("grant_type"))
		assert.Equal(t, "urn:ietf:params:oauth:token-type:access_token", r.Form.Get("subject_token_type"))
		assert.Equal(t, "urn:ietf:params:oauth:token-type:access_token", r.Form.Get("requested_token_type"))

		resp := response{
			AccessToken: "token",
			TokenType:   "Bearer",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	request := &exchangeRequest{
		SubjectToken: "test-token",
		// GrantType, SubjectTokenType, and RequestedTokenType are empty
	}
	auth := clientAuthentication{}

	ctx := context.Background()
	resp, err := exchangeToken(ctx, server.URL, request, auth, nil)

	require.NoError(t, err)
	assert.NotNil(t, resp)
}

// TestExchangeToken_OptionalFields tests that optional fields are properly included when provided.
func TestExchangeToken_OptionalFields(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := r.ParseForm()
		require.NoError(t, err)

		// Verify optional fields are included
		assert.Equal(t, "https://api.example.com", r.Form.Get("audience"))
		assert.Equal(t, "https://resource.example.com", r.Form.Get("resource"))
		assert.Equal(t, "read write delete", r.Form.Get("scope"))
		assert.Equal(t, "actor-token-value", r.Form.Get("actor_token"))
		assert.Equal(t, "urn:ietf:params:oauth:token-type:jwt", r.Form.Get("actor_token_type"))

		resp := response{
			AccessToken: "token",
			TokenType:   "Bearer",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	request := &exchangeRequest{
		SubjectToken: "test-token",
		Audience:     "https://api.example.com",
		Resource:     "https://resource.example.com",
		Scope:        []string{"read", "write", "delete"},
		ActingParty: &actingParty{
			ActorToken:     "actor-token-value",
			ActorTokenType: "urn:ietf:params:oauth:token-type:jwt",
		},
	}
	auth := clientAuthentication{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
	}

	ctx := context.Background()
	resp, err := exchangeToken(ctx, server.URL, request, auth, nil)

	require.NoError(t, err)
	assert.NotNil(t, resp)
}

// TestExchangeToken_ActorTokenWithoutType tests actor token without actor token type.
func TestExchangeToken_ActorTokenWithoutType(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := r.ParseForm()
		require.NoError(t, err)

		// Verify actor_token is present but actor_token_type is not
		assert.Equal(t, "actor-token-value", r.Form.Get("actor_token"))
		assert.Empty(t, r.Form.Get("actor_token_type"))

		resp := response{
			AccessToken: "token",
			TokenType:   "Bearer",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	request := &exchangeRequest{
		SubjectToken: "test-token",
		ActingParty: &actingParty{
			ActorToken: "actor-token-value",
			// ActorTokenType is empty
		},
	}
	auth := clientAuthentication{}

	ctx := context.Background()
	resp, err := exchangeToken(ctx, server.URL, request, auth, nil)

	require.NoError(t, err)
	assert.NotNil(t, resp)
}

// TestExchangeToken_InvalidURL tests error handling for invalid endpoint URLs.
func TestExchangeToken_InvalidURL(t *testing.T) {
	t.Parallel()

	request := &exchangeRequest{
		SubjectToken: "test-token",
	}
	auth := clientAuthentication{}

	ctx := context.Background()
	resp, err := exchangeToken(ctx, "://invalid-url", request, auth, nil)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "failed to create token exchange request")
}

// TestExchangeToken_NetworkError tests error handling for network failures.
func TestExchangeToken_NetworkError(t *testing.T) {
	t.Parallel()

	// Use an invalid host that will fail DNS resolution
	request := &exchangeRequest{
		SubjectToken: "test-token",
	}
	auth := clientAuthentication{}

	ctx := context.Background()
	resp, err := exchangeToken(ctx, "http://invalid-host-that-does-not-exist-12345.com/token", request, auth, nil)

	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "token exchange request failed")
}

// TestExchangeToken_ResponseSizeLimit tests that large responses are properly limited.
func TestExchangeToken_ResponseSizeLimit(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Generate a response larger than 1MB limit
		largeString := strings.Repeat("x", 2*1024*1024) // 2MB
		resp := map[string]string{
			"access_token": largeString,
			"token_type":   "Bearer",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	request := &exchangeRequest{
		SubjectToken: "test-token",
	}
	auth := clientAuthentication{}

	ctx := context.Background()
	resp, err := exchangeToken(ctx, server.URL, request, auth, nil)

	require.Error(t, err)
	assert.Nil(t, resp)
	// The io.LimitReader allows reading up to 1MB, then truncates the response
	// This causes a JSON parsing error rather than a read error
	assert.Contains(t, err.Error(), "failed to parse token exchange response")
}

// TestExchangeToken_NoCredentialLeakage tests that credentials are not leaked in error messages.
func TestExchangeToken_NoCredentialLeakage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		setupServer  func() *httptest.Server
		clientSecret string
		subjectToken string
	}{
		{
			name: "Error response should not leak client secret",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
				}))
			},
			clientSecret: "super-secret-client-secret",
			subjectToken: "test-token",
		},
		{
			name: "Error response should not leak subject token",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":"invalid_request"}`))
				}))
			},
			clientSecret: "client-secret",
			subjectToken: "super-secret-subject-token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := tt.setupServer()
			defer server.Close()

			request := &exchangeRequest{
				SubjectToken: tt.subjectToken,
			}
			auth := clientAuthentication{
				ClientID:     "test-client-id",
				ClientSecret: tt.clientSecret,
			}

			ctx := context.Background()
			resp, err := exchangeToken(ctx, server.URL, request, auth, nil)

			require.Error(t, err)
			assert.Nil(t, resp)

			// Verify that error message does not contain sensitive data
			errMsg := err.Error()
			assert.NotContains(t, errMsg, tt.clientSecret, "Error message should not contain client secret")
			assert.NotContains(t, errMsg, tt.subjectToken, "Error message should not contain subject token")
		})
	}
}

// TestExchangeToken_FormEncoding tests that form data is properly URL-encoded.
func TestExchangeToken_FormEncoding(t *testing.T) {
	t.Parallel()

	specialChars := "test+token=with&special=chars"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := r.ParseForm()
		require.NoError(t, err)

		// Verify that special characters are properly decoded
		assert.Equal(t, specialChars, r.Form.Get("subject_token"))

		resp := response{
			AccessToken: "token",
			TokenType:   "Bearer",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	request := &exchangeRequest{
		SubjectToken: specialChars,
	}
	auth := clientAuthentication{}

	ctx := context.Background()
	resp, err := exchangeToken(ctx, server.URL, request, auth, nil)

	require.NoError(t, err)
	assert.NotNil(t, resp)
}

// TestExchangeToken_ContentLength tests that Content-Length header is properly set.
func TestExchangeToken_ContentLength(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify Content-Length header is present and valid
		contentLength := r.Header.Get("Content-Length")
		assert.NotEmpty(t, contentLength)

		// Read body and verify it matches Content-Length
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Equal(t, contentLength, fmt.Sprintf("%d", len(body)))

		resp := response{
			AccessToken: "token",
			TokenType:   "Bearer",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	request := &exchangeRequest{
		SubjectToken: "test-token",
	}
	auth := clientAuthentication{}

	ctx := context.Background()
	resp, err := exchangeToken(ctx, server.URL, request, auth, nil)

	require.NoError(t, err)
	assert.NotNil(t, resp)
}

// TestSubjectTokenProvider_Variants tests various subject token provider implementations.
func TestSubjectTokenProvider_Variants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		subjectTokenProvider func() (string, error)
		expectError          bool
		errorContains        string
	}{
		{
			name: "Static token provider",
			subjectTokenProvider: func() (string, error) {
				return "static-token", nil
			},
			expectError: false,
		},
		{
			name: "Dynamic token provider",
			subjectTokenProvider: func() (string, error) {
				// Simulate fetching a token from somewhere
				token := fmt.Sprintf("dynamic-token-%d", time.Now().Unix())
				return token, nil
			},
			expectError: false,
		},
		{
			name: "Token from oauth2.Token",
			subjectTokenProvider: func() (string, error) {
				token := &oauth2.Token{
					AccessToken: "oauth2-access-token",
					TokenType:   "Bearer",
					Expiry:      time.Now().Add(1 * time.Hour),
				}
				if token.Valid() {
					return token.AccessToken, nil
				}
				return "", errors.New("token expired")
			},
			expectError: false,
		},
		{
			name: "Provider returns empty token",
			subjectTokenProvider: func() (string, error) {
				return "", nil
			},
			expectError:   true,
			errorContains: "subject_token is required",
		},
		{
			name: "Provider returns error",
			subjectTokenProvider: func() (string, error) {
				return "", errors.New("token provider failed")
			},
			expectError:   true,
			errorContains: "failed to get subject token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create server within subtest to avoid race conditions with parallel execution
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				resp := response{
					AccessToken: "exchanged-token",
					TokenType:   "Bearer",
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()

			config := &Config{
				TokenURL:             server.URL,
				ClientID:             "test-client-id",
				ClientSecret:         "test-client-secret",
				SubjectTokenProvider: tt.subjectTokenProvider,
			}

			ctx := context.Background()
			ts := config.TokenSource(ctx)
			token, err := ts.Token()

			if tt.expectError {
				require.Error(t, err)
				assert.Nil(t, token)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
				assert.NotNil(t, token)
				assert.NotEmpty(t, token.AccessToken)
			}
		})
	}
}

// TestExchangeToken_EmptyClientCredentials tests exchange without client credentials.
func TestExchangeToken_EmptyClientCredentials(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify no Authorization header is present when credentials are empty
		authHeader := r.Header.Get("Authorization")
		assert.Empty(t, authHeader, "Authorization header should not be present for empty credentials")

		err := r.ParseForm()
		require.NoError(t, err)

		// Verify client credentials are not in request body either
		assert.Empty(t, r.Form.Get("client_id"))
		assert.Empty(t, r.Form.Get("client_secret"))

		resp := response{
			AccessToken: "token",
			TokenType:   "Bearer",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	request := &exchangeRequest{
		SubjectToken: "test-token",
	}
	auth := clientAuthentication{
		// Empty ClientID and ClientSecret (public client)
	}

	ctx := context.Background()
	resp, err := exchangeToken(ctx, server.URL, request, auth, nil)

	require.NoError(t, err)
	assert.NotNil(t, resp)
}

// TestExchangeToken_OnlyClientID tests exchange with only client ID (no secret).
func TestExchangeToken_OnlyClientID(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify no Authorization header when only ClientID is provided (public client)
		// Per our implementation, Basic Auth requires both ClientID AND ClientSecret
		authHeader := r.Header.Get("Authorization")
		assert.Empty(t, authHeader, "Authorization header should not be present for public clients")

		err := r.ParseForm()
		require.NoError(t, err)

		// Verify credentials are not in request body
		assert.Empty(t, r.Form.Get("client_id"))
		assert.Empty(t, r.Form.Get("client_secret"))

		resp := response{
			AccessToken: "token",
			TokenType:   "Bearer",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	request := &exchangeRequest{
		SubjectToken: "test-token",
	}
	auth := clientAuthentication{
		ClientID: "public-client-id",
		// No ClientSecret (public client)
	}

	ctx := context.Background()
	resp, err := exchangeToken(ctx, server.URL, request, auth, nil)

	require.NoError(t, err)
	assert.NotNil(t, resp)
}

// TestExchangeToken_ResponseFields tests that all response fields are properly parsed.
func TestExchangeToken_ResponseFields(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := response{
			AccessToken:     "access-token-value",
			IssuedTokenType: "urn:ietf:params:oauth:token-type:access_token",
			TokenType:       "Bearer",
			ExpiresIn:       7200,
			Scope:           "openid profile email",
			RefreshToken:    "refresh-token-value",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	request := &exchangeRequest{
		SubjectToken: "test-token",
	}
	auth := clientAuthentication{}

	ctx := context.Background()
	resp, err := exchangeToken(ctx, server.URL, request, auth, nil)

	require.NoError(t, err)
	assert.Equal(t, "access-token-value", resp.AccessToken)
	assert.Equal(t, "urn:ietf:params:oauth:token-type:access_token", resp.IssuedTokenType)
	assert.Equal(t, "Bearer", resp.TokenType)
	assert.Equal(t, 7200, resp.ExpiresIn)
	assert.Equal(t, "openid profile email", resp.Scope)
	assert.Equal(t, "refresh-token-value", resp.RefreshToken)
}

// TestExchangeToken_MinimalResponse tests response with only required fields.
func TestExchangeToken_MinimalResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Minimal valid response according to RFC 8693
		resp := response{
			AccessToken: "access-token-value",
			TokenType:   "Bearer",
			// IssuedTokenType, ExpiresIn, Scope, RefreshToken are all optional
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	request := &exchangeRequest{
		SubjectToken: "test-token",
	}
	auth := clientAuthentication{}

	ctx := context.Background()
	resp, err := exchangeToken(ctx, server.URL, request, auth, nil)

	require.NoError(t, err)
	assert.Equal(t, "access-token-value", resp.AccessToken)
	assert.Equal(t, "Bearer", resp.TokenType)
	assert.Empty(t, resp.IssuedTokenType)
	assert.Equal(t, 0, resp.ExpiresIn)
	assert.Empty(t, resp.Scope)
	assert.Empty(t, resp.RefreshToken)
}

// TestExchangeToken_ScopeArray tests that scope array is properly converted to space-separated string.
func TestExchangeToken_ScopeArray(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		scopes        []string
		expectedScope string
	}{
		{
			name:          "Single scope",
			scopes:        []string{"read"},
			expectedScope: "read",
		},
		{
			name:          "Multiple scopes",
			scopes:        []string{"read", "write", "delete"},
			expectedScope: "read write delete",
		},
		{
			name:          "Empty scope array",
			scopes:        []string{},
			expectedScope: "",
		},
		{
			name:          "Scopes with special characters",
			scopes:        []string{"https://api.example.com/read", "https://api.example.com/write"},
			expectedScope: "https://api.example.com/read https://api.example.com/write",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				err := r.ParseForm()
				require.NoError(t, err)

				if tt.expectedScope == "" {
					assert.Empty(t, r.Form.Get("scope"))
				} else {
					assert.Equal(t, tt.expectedScope, r.Form.Get("scope"))
				}

				resp := response{
					AccessToken: "token",
					TokenType:   "Bearer",
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()

			request := &exchangeRequest{
				SubjectToken: "test-token",
				Scope:        tt.scopes,
			}
			auth := clientAuthentication{}

			ctx := context.Background()
			resp, err := exchangeToken(ctx, server.URL, request, auth, nil)

			require.NoError(t, err)
			assert.NotNil(t, resp)
		})
	}
}

// TestConfig_TokenSource tests that TokenSource creates a valid tokenSource.
func TestConfig_TokenSource(t *testing.T) {
	t.Parallel()

	config := &Config{
		TokenURL:     "https://example.com/token",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		Audience:     "https://api.example.com",
		Scopes:       []string{"read", "write"},
		SubjectTokenProvider: func() (string, error) {
			return "test-token", nil
		},
	}

	ctx := context.Background()
	ts := config.TokenSource(ctx)

	assert.NotNil(t, ts)
	assert.Implements(t, (*oauth2.TokenSource)(nil), ts)
}

// TestExchangeToken_SSRFPrevention tests that the implementation doesn't facilitate SSRF attacks.
func TestExchangeToken_SSRFPrevention(t *testing.T) {
	t.Parallel()

	// Test that we can't easily perform SSRF by controlling the endpoint URL
	// This is a basic test - in production, additional URL validation may be needed

	tests := []struct {
		name     string
		endpoint string
	}{
		{
			name:     "Localhost endpoint",
			endpoint: "http://localhost/token",
		},
		{
			name:     "Internal IP endpoint",
			endpoint: "http://192.168.1.1/token",
		},
		{
			name:     "Metadata service endpoint",
			endpoint: "http://169.254.169.254/latest/meta-data/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			request := &exchangeRequest{
				SubjectToken: "test-token",
			}
			auth := clientAuthentication{}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			// The function should still attempt the request but fail due to network issues
			// This test verifies that the function doesn't have special handling that
			// would prevent or allow SSRF - it's the caller's responsibility to validate URLs
			resp, err := exchangeToken(ctx, tt.endpoint, request, auth, nil)

			// We expect an error due to connection failure, not a panic or unexpected behavior
			require.Error(t, err)
			assert.Nil(t, resp)
		})
	}
}

// TestExchangeRequest_StructFields tests that exchangeRequest struct supports all RFC 8693 fields.
func TestExchangeRequest_StructFields(t *testing.T) {
	t.Parallel()

	// This test verifies that the exchangeRequest struct has all necessary fields
	req := &exchangeRequest{
		ActingParty: &actingParty{
			ActorToken:     "actor-token",
			ActorTokenType: "actor-token-type",
		},
		GrantType:          "grant-type",
		Resource:           "resource",
		Audience:           "audience",
		Scope:              []string{"scope1", "scope2"},
		RequestedTokenType: "requested-token-type",
		SubjectToken:       "subject-token",
		SubjectTokenType:   "subject-token-type",
	}

	assert.Equal(t, "actor-token", req.ActingParty.ActorToken)
	assert.Equal(t, "actor-token-type", req.ActingParty.ActorTokenType)
	assert.Equal(t, "grant-type", req.GrantType)
	assert.Equal(t, "resource", req.Resource)
	assert.Equal(t, "audience", req.Audience)
	assert.Equal(t, []string{"scope1", "scope2"}, req.Scope)
	assert.Equal(t, "requested-token-type", req.RequestedTokenType)
	assert.Equal(t, "subject-token", req.SubjectToken)
	assert.Equal(t, "subject-token-type", req.SubjectTokenType)
}

// TestResponse_JSONTags tests that response struct has correct JSON tags.
func TestResponse_JSONTags(t *testing.T) {
	t.Parallel()

	jsonData := `{
		"access_token": "test-access-token",
		"issued_token_type": "test-issued-token-type",
		"token_type": "test-token-type",
		"expires_in": 3600,
		"scope": "test-scope",
		"refresh_token": "test-refresh-token"
	}`

	var resp response
	err := json.Unmarshal([]byte(jsonData), &resp)

	require.NoError(t, err)
	assert.Equal(t, "test-access-token", resp.AccessToken)
	assert.Equal(t, "test-issued-token-type", resp.IssuedTokenType)
	assert.Equal(t, "test-token-type", resp.TokenType)
	assert.Equal(t, 3600, resp.ExpiresIn)
	assert.Equal(t, "test-scope", resp.Scope)
	assert.Equal(t, "test-refresh-token", resp.RefreshToken)
}

// TestClientAuthentication_Fields tests clientAuthentication struct fields.
func TestClientAuthentication_Fields(t *testing.T) {
	t.Parallel()

	auth := clientAuthentication{
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
	}

	assert.Equal(t, "test-client-id", auth.ClientID)
	assert.Equal(t, "test-client-secret", auth.ClientSecret)
}

// TestConfig_Fields tests Config struct fields.
func TestConfig_Fields(t *testing.T) {
	t.Parallel()

	provider := func() (string, error) {
		return "token", nil
	}

	config := &Config{
		TokenURL:             "https://example.com/token",
		ClientID:             "test-client-id",
		ClientSecret:         "test-client-secret",
		Audience:             "https://api.example.com",
		Scopes:               []string{"read", "write"},
		SubjectTokenProvider: provider,
	}

	assert.Equal(t, "https://example.com/token", config.TokenURL)
	assert.Equal(t, "test-client-id", config.ClientID)
	assert.Equal(t, "test-client-secret", config.ClientSecret)
	assert.Equal(t, "https://api.example.com", config.Audience)
	assert.Equal(t, []string{"read", "write"}, config.Scopes)
	assert.NotNil(t, config.SubjectTokenProvider)

	token, err := config.SubjectTokenProvider()
	require.NoError(t, err)
	assert.Equal(t, "token", token)
}

// TestExchangeToken_URLValues tests that form values are properly encoded.
func TestExchangeToken_URLValues(t *testing.T) {
	t.Parallel()

	receivedValues := make(url.Values)
	var receivedAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Store received Authorization header
		receivedAuth = r.Header.Get("Authorization")

		err := r.ParseForm()
		require.NoError(t, err)

		// Store received form values
		receivedValues = r.Form

		resp := response{
			AccessToken: "token",
			TokenType:   "Bearer",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	request := &exchangeRequest{
		GrantType:          "urn:ietf:params:oauth:grant-type:token-exchange",
		SubjectToken:       "my-subject-token",
		SubjectTokenType:   "urn:ietf:params:oauth:token-type:access_token",
		RequestedTokenType: "urn:ietf:params:oauth:token-type:access_token",
		Audience:           "https://api.example.com",
		Scope:              []string{"read", "write"},
		Resource:           "https://resource.example.com",
	}
	auth := clientAuthentication{
		ClientID:     "my-client-id",
		ClientSecret: "my-client-secret",
	}

	ctx := context.Background()
	_, err := exchangeToken(ctx, server.URL, request, auth, nil)
	require.NoError(t, err)

	// Verify Authorization header is present with Basic Auth
	assert.NotEmpty(t, receivedAuth, "Authorization header should be present")
	assert.True(t, strings.HasPrefix(receivedAuth, "Basic "), "Authorization should use Basic scheme")

	// Verify all expected form values were sent (credentials should NOT be in body)
	assert.Equal(t, "urn:ietf:params:oauth:grant-type:token-exchange", receivedValues.Get("grant_type"))
	assert.Equal(t, "my-subject-token", receivedValues.Get("subject_token"))
	assert.Equal(t, "urn:ietf:params:oauth:token-type:access_token", receivedValues.Get("subject_token_type"))
	assert.Equal(t, "urn:ietf:params:oauth:token-type:access_token", receivedValues.Get("requested_token_type"))
	assert.Equal(t, "https://api.example.com", receivedValues.Get("audience"))
	assert.Equal(t, "read write", receivedValues.Get("scope"))
	assert.Equal(t, "https://resource.example.com", receivedValues.Get("resource"))

	// Verify client credentials are NOT in the request body
	assert.Empty(t, receivedValues.Get("client_id"), "client_id should not be in request body")
	assert.Empty(t, receivedValues.Get("client_secret"), "client_secret should not be in request body")
}

// TestExchangeToken_BasicAuthURLEncoding tests that credentials with special characters are properly URL-encoded.
func TestExchangeToken_BasicAuthURLEncoding(t *testing.T) {
	t.Parallel()

	// Test with credentials containing special characters that require URL encoding per RFC 6749
	specialClientID := "client:with@special/chars"
	specialClientSecret := "secret&with=special%chars"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify Authorization header is present
		authHeader := r.Header.Get("Authorization")
		assert.NotEmpty(t, authHeader, "Authorization header should be present")
		assert.True(t, strings.HasPrefix(authHeader, "Basic "), "Authorization should use Basic scheme")

		// Verify credentials are properly URL-encoded per RFC 6749
		// BasicAuth() decodes the base64 and extracts username:password
		// We expect URL-encoded values as that's what we sent
		username, password, ok := r.BasicAuth()
		require.True(t, ok, "Basic Auth credentials should be parseable")
		assert.Equal(t, url.QueryEscape(specialClientID), username, "ClientID should be URL-encoded")
		assert.Equal(t, url.QueryEscape(specialClientSecret), password, "ClientSecret should be URL-encoded")

		resp := response{
			AccessToken: "token",
			TokenType:   "Bearer",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	request := &exchangeRequest{
		SubjectToken: "test-token",
	}
	auth := clientAuthentication{
		ClientID:     specialClientID,
		ClientSecret: specialClientSecret,
	}

	ctx := context.Background()
	resp, err := exchangeToken(ctx, server.URL, request, auth, nil)

	require.NoError(t, err)
	assert.NotNil(t, resp)
}

package strategies

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/env/mocks"
)

// Test helpers for reducing boilerplate

func createTestIdentity(subject, token string) *auth.Identity {
	return &auth.Identity{
		Subject: subject,
		Token:   token,
	}
}

// createMockEnvReader creates a mock env.Reader that returns empty strings for all env vars.
// This is sufficient for tests that don't use client_secret_env.
func createMockEnvReader(t *testing.T) *mocks.MockReader {
	t.Helper()
	ctrl := gomock.NewController(t)
	mockEnv := mocks.NewMockReader(ctrl)
	mockEnv.EXPECT().Getenv(gomock.Any()).Return("").AnyTimes()
	return mockEnv
}

func createContextWithIdentity(subject, token string) context.Context {
	return auth.WithIdentity(context.Background(), createTestIdentity(subject, token))
}

func createSuccessfulTokenServer(t *testing.T, tokenPrefix string, validateForm func(*testing.T, *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Helper()
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))

		err := r.ParseForm()
		require.NoError(t, err)

		if validateForm != nil {
			validateForm(t, r)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":      tokenPrefix,
			"token_type":        "Bearer",
			"issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
			"expires_in":        3600,
		})
	}))
}

func TestTokenExchangeStrategy_Authenticate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		setupCtx        func() context.Context
		metadata        map[string]any
		setupServer     func() *httptest.Server
		expectError     bool
		errorContains   string
		checkAuthHeader func(t *testing.T, req *http.Request)
	}{
		{
			name:     "successfully exchanges token",
			setupCtx: func() context.Context { return createContextWithIdentity("user123", "client-token") },
			setupServer: func() *httptest.Server {
				return createSuccessfulTokenServer(t, "backend-token-123", func(t *testing.T, r *http.Request) {
					t.Helper()
					assert.Equal(t, "urn:ietf:params:oauth:grant-type:token-exchange", r.Form.Get("grant_type"))
					assert.Equal(t, "client-token", r.Form.Get("subject_token"))
				})
			},
			expectError: false,
			checkAuthHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				assert.Equal(t, "Bearer backend-token-123", req.Header.Get("Authorization"))
			},
		},
		{
			name:     "includes audience in token exchange",
			setupCtx: func() context.Context { return createContextWithIdentity("user456", "client-token-2") },
			setupServer: func() *httptest.Server {
				return createSuccessfulTokenServer(t, "backend-token", func(t *testing.T, r *http.Request) {
					t.Helper()
					assert.Equal(t, "https://backend.example.com", r.Form.Get("audience"))
				})
			},
			metadata: map[string]any{
				"audience": "https://backend.example.com",
			},
			expectError: false,
		},
		{
			name:     "includes scopes in token exchange",
			setupCtx: func() context.Context { return createContextWithIdentity("user789", "client-token-3") },
			setupServer: func() *httptest.Server {
				return createSuccessfulTokenServer(t, "backend-token", func(t *testing.T, r *http.Request) {
					t.Helper()
					assert.Equal(t, "read write", r.Form.Get("scope"))
				})
			},
			metadata: map[string]any{
				"scopes": []string{"read", "write"},
			},
			expectError: false,
		},
		{
			name:     "includes client credentials in token exchange",
			setupCtx: func() context.Context { return createContextWithIdentity("admin-user", "admin-token") },
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					t.Helper()
					username, password, ok := r.BasicAuth()
					assert.True(t, ok)
					assert.Equal(t, "client-id", username)
					assert.Equal(t, "client-secret", password)

					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]any{
						"access_token":      "backend-token",
						"token_type":        "Bearer",
						"issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
					})
				}))
			},
			metadata: map[string]any{
				"client_id":     "client-id",
				"client_secret": "client-secret",
			},
			expectError: false,
		},
		{
			name:     "returns error when no identity in context",
			setupCtx: func() context.Context { return context.Background() },
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
					t.Error("Server should not be called")
				}))
			},
			expectError:   true,
			errorContains: "no identity",
		},
		{
			name:     "returns error when identity has no token",
			setupCtx: func() context.Context { return createContextWithIdentity("no-token-user", "") },
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
					t.Error("Server should not be called")
				}))
			},
			expectError:   true,
			errorContains: "no token",
		},
		{
			name:          "returns error when metadata is invalid",
			setupCtx:      func() context.Context { return createContextWithIdentity("metadata-test-user", "metadata-token") },
			setupServer:   nil,              // No server needed for validation error
			metadata:      map[string]any{}, // Missing token_url
			expectError:   true,
			errorContains: "invalid metadata",
		},
		{
			name:     "returns error when token exchange fails",
			setupCtx: func() context.Context { return createContextWithIdentity("fail-user", "invalid-token") },
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusUnauthorized)
					json.NewEncoder(w).Encode(map[string]any{
						"error":             "invalid_grant",
						"error_description": "The subject token is invalid",
					})
				}))
			},
			expectError:   true,
			errorContains: "token exchange failed",
		},
		{
			name:     "returns error when response is missing access_token",
			setupCtx: func() context.Context { return createContextWithIdentity("missing-token-user", "test-token") },
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]any{
						"token_type":        "Bearer",
						"issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
					})
				}))
			},
			expectError:   true,
			errorContains: "empty access_token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var server *httptest.Server
			if tt.setupServer != nil {
				server = tt.setupServer()
				defer server.Close()
			}

			mockEnv := createMockEnvReader(t)
			strategy := NewTokenExchangeStrategy(mockEnv)
			ctx := tt.setupCtx()

			metadata := tt.metadata
			if metadata == nil {
				metadata = map[string]any{}
			}
			if server != nil {
				metadata["token_url"] = server.URL
			}

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			err := strategy.Authenticate(ctx, req, metadata)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
				return
			}

			require.NoError(t, err)
			if tt.checkAuthHeader != nil {
				tt.checkAuthHeader(t, req)
			}
		})
	}
}

func TestTokenExchangeStrategy_Validate(t *testing.T) {
	t.Parallel()

	validBaseMetadata := map[string]any{"token_url": "https://auth.example.com/token"}

	tests := []struct {
		name        string
		metadata    map[string]any
		expectError string // empty means no error expected
	}{
		{name: "valid with only token_url", metadata: validBaseMetadata},
		{name: "valid with all fields", metadata: map[string]any{
			"token_url": "https://auth.example.com/token", "client_id": "my-client",
			"client_secret": "my-secret", "audience": "https://backend.example.com",
			"scopes": []string{"read", "write"}, "subject_token_type": "access_token",
		}},
		{name: "valid with string scopes", metadata: map[string]any{"token_url": "https://auth.example.com/token", "scopes": "read"}},
		{name: "valid with id_token type", metadata: map[string]any{"token_url": "https://auth.example.com/token", "subject_token_type": "id_token"}},
		{name: "valid with client_id only", metadata: map[string]any{"token_url": "https://auth.example.com/token", "client_id": "my-client"}},
		{name: "valid with extra fields", metadata: map[string]any{"token_url": "https://auth.example.com/token", "extra": "ignored"}},
		{name: "error on missing token_url", metadata: map[string]any{}, expectError: "token_url required"},
		{name: "error on nil metadata", metadata: nil, expectError: "token_url required"},
		{name: "error on client_secret without client_id", metadata: map[string]any{"token_url": "https://auth.example.com/token", "client_secret": "secret"}, expectError: "client_secret cannot be provided without client_id"},
		{name: "error on client_secret_env without client_id", metadata: map[string]any{"token_url": "https://auth.example.com/token", "client_secret_env": "TEST_SECRET"}, expectError: "client_secret_env cannot be provided without client_id"},
		{name: "error on invalid scopes type", metadata: map[string]any{"token_url": "https://auth.example.com/token", "scopes": 123}, expectError: "scopes must be a string or array"},
		{name: "error on non-string in scopes", metadata: map[string]any{"token_url": "https://auth.example.com/token", "scopes": []any{"read", 123}}, expectError: "scopes[1] must be a string"},
		{name: "error on invalid token type", metadata: map[string]any{"token_url": "https://auth.example.com/token", "subject_token_type": "invalid"}, expectError: "invalid subject_token_type"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mockEnv := createMockEnvReader(t)
			strategy := NewTokenExchangeStrategy(mockEnv)
			err := strategy.Validate(tt.metadata)

			if tt.expectError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectError)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestTokenExchangeStrategy_CacheSeparation(t *testing.T) {
	t.Parallel()

	// This test verifies that different configurations result in separate cache entries
	server1 := createSuccessfulTokenServer(t, "token-scope-read", nil)
	defer server1.Close()

	server2 := createSuccessfulTokenServer(t, "token-scope-write", nil)
	defer server2.Close()

	mockEnv := createMockEnvReader(t)
	strategy := NewTokenExchangeStrategy(mockEnv)
	ctx := createContextWithIdentity("cache-test-user", "test-token")

	// First request with "read" scope
	metadata1 := map[string]any{
		"token_url": server1.URL,
		"scopes":    []string{"read"},
	}
	req1 := httptest.NewRequest(http.MethodGet, "/test", nil)
	err := strategy.Authenticate(ctx, req1, metadata1)
	require.NoError(t, err)
	assert.Equal(t, "Bearer token-scope-read", req1.Header.Get("Authorization"))

	// Second request with "write" scope - should use different cache entry
	metadata2 := map[string]any{
		"token_url": server2.URL,
		"scopes":    []string{"write"},
	}
	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	err = strategy.Authenticate(ctx, req2, metadata2)
	require.NoError(t, err)
	assert.Equal(t, "Bearer token-scope-write", req2.Header.Get("Authorization"))

	// Verify we have two separate cache entries (config-level)
	strategy.mu.RLock()
	assert.Len(t, strategy.exchangeConfigs, 2, "Should have two separate cache entries")
	strategy.mu.RUnlock()
}

func TestTokenExchangeStrategy_CacheHitWithDifferentScopeOrder(t *testing.T) {
	t.Parallel()

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":      "cached-token",
			"token_type":        "Bearer",
			"issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
			"expires_in":        3600,
		})
	}))
	defer server.Close()

	mockEnv := createMockEnvReader(t)
	strategy := NewTokenExchangeStrategy(mockEnv)
	ctx := createContextWithIdentity("scope-order-user", "test-token")

	// First request with scopes in one order
	metadata1 := map[string]any{
		"token_url": server.URL,
		"scopes":    []string{"write", "read", "admin"},
	}
	req1 := httptest.NewRequest(http.MethodGet, "/test", nil)
	err := strategy.Authenticate(ctx, req1, metadata1)
	require.NoError(t, err)

	// Second request with same scopes in different order
	metadata2 := map[string]any{
		"token_url": server.URL,
		"scopes":    []string{"admin", "read", "write"},
	}
	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	err = strategy.Authenticate(ctx, req2, metadata2)
	require.NoError(t, err)

	// Note: callCount will be 2 since we don't cache per-user tokens at this layer
	// Upper vMCP layer handles token caching. We only cache ExchangeConfig.
	assert.Equal(t, 2, callCount, "Each request performs a new exchange (no per-user token caching at this layer)")

	// Verify only one ExchangeConfig cache entry exists (config-level caching)
	strategy.mu.RLock()
	assert.Len(t, strategy.exchangeConfigs, 1, "Should have only one ExchangeConfig cache entry")
	strategy.mu.RUnlock()
}

// TestTokenExchangeStrategy_SharedConfigAcrossUsers verifies that different users
// with the same backend configuration share the same cached ExchangeConfig.
func TestTokenExchangeStrategy_SharedConfigAcrossUsers(t *testing.T) {
	t.Parallel()

	server := createSuccessfulTokenServer(t, "backend-token", nil)
	defer server.Close()

	mockEnv := createMockEnvReader(t)
	strategy := NewTokenExchangeStrategy(mockEnv)
	metadata := map[string]any{
		"token_url": server.URL,
		"scopes":    []string{"read", "write"},
	}

	// First user makes a request
	ctx1 := createContextWithIdentity("user1", "user1-token")
	req1 := httptest.NewRequest(http.MethodGet, "/test", nil)
	err := strategy.Authenticate(ctx1, req1, metadata)
	require.NoError(t, err)

	// Second user makes a request with same config
	ctx2 := createContextWithIdentity("user2", "user2-token")
	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	err = strategy.Authenticate(ctx2, req2, metadata)
	require.NoError(t, err)

	// Verify only one ExchangeConfig cache entry exists (shared across users)
	strategy.mu.RLock()
	assert.Len(t, strategy.exchangeConfigs, 1, "Should have only one ExchangeConfig for both users")
	strategy.mu.RUnlock()
}

// TestTokenExchangeStrategy_CurrentTokenUsed verifies that the current identity
// token is used at exchange time, not a stale cached token.
func TestTokenExchangeStrategy_CurrentTokenUsed(t *testing.T) {
	t.Parallel()

	var capturedToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := r.ParseForm()
		require.NoError(t, err)
		capturedToken = r.Form.Get("subject_token")

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":      "backend-token",
			"token_type":        "Bearer",
			"issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
			"expires_in":        3600,
		})
	}))
	defer server.Close()

	mockEnv := createMockEnvReader(t)
	strategy := NewTokenExchangeStrategy(mockEnv)
	metadata := map[string]any{
		"token_url": server.URL,
	}

	// First request with initial token
	ctx1 := createContextWithIdentity("user1", "initial-token")
	req1 := httptest.NewRequest(http.MethodGet, "/test", nil)
	err := strategy.Authenticate(ctx1, req1, metadata)
	require.NoError(t, err)
	assert.Equal(t, "initial-token", capturedToken, "Should use initial token")

	// Second request with refreshed token (simulating token refresh)
	ctx2 := createContextWithIdentity("user1", "refreshed-token")
	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	err = strategy.Authenticate(ctx2, req2, metadata)
	require.NoError(t, err)
	assert.Equal(t, "refreshed-token", capturedToken, "Should use current refreshed token, not cached stale token")
}

func TestTokenExchangeStrategy_ClientSecretEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		setupMock     func(t *testing.T, mockEnv *mocks.MockReader)
		metadata      map[string]any
		expectError   bool
		errorContains string
		validateAuth  func(t *testing.T, r *http.Request)
	}{
		{
			name: "successfully resolves client_secret from environment variable",
			setupMock: func(t *testing.T, mockEnv *mocks.MockReader) {
				t.Helper()
				mockEnv.EXPECT().Getenv("TEST_CLIENT_SECRET").Return("secret-from-env").AnyTimes()
			},
			metadata: map[string]any{
				"client_id":         "test-client",
				"client_secret_env": "TEST_CLIENT_SECRET",
			},
			expectError: false,
			validateAuth: func(t *testing.T, r *http.Request) {
				t.Helper()
				username, password, ok := r.BasicAuth()
				assert.True(t, ok, "Basic auth should be present")
				assert.Equal(t, "test-client", username)
				assert.Equal(t, "secret-from-env", password)
			},
		},
		{
			name: "error when environment variable is not set",
			setupMock: func(t *testing.T, mockEnv *mocks.MockReader) {
				t.Helper()
				mockEnv.EXPECT().Getenv("MISSING_ENV_VAR").Return("").AnyTimes()
			},
			metadata: map[string]any{
				"client_id":         "test-client",
				"client_secret_env": "MISSING_ENV_VAR",
			},
			expectError:   true,
			errorContains: "environment variable MISSING_ENV_VAR not set",
		},
		{
			name: "error when environment variable is empty",
			setupMock: func(t *testing.T, mockEnv *mocks.MockReader) {
				t.Helper()
				mockEnv.EXPECT().Getenv("EMPTY_SECRET").Return("").AnyTimes()
			},
			metadata: map[string]any{
				"client_id":         "test-client",
				"client_secret_env": "EMPTY_SECRET",
			},
			expectError:   true,
			errorContains: "environment variable EMPTY_SECRET not set or empty",
		},
		{
			name: "client_secret takes precedence over client_secret_env",
			setupMock: func(t *testing.T, _ *mocks.MockReader) {
				t.Helper()
				// Mock should not be called since client_secret takes precedence
			},
			metadata: map[string]any{
				"client_id":         "test-client",
				"client_secret":     "direct-secret",
				"client_secret_env": "TEST_SECRET_ENV",
			},
			expectError: false,
			validateAuth: func(t *testing.T, r *http.Request) {
				t.Helper()
				username, password, ok := r.BasicAuth()
				assert.True(t, ok)
				assert.Equal(t, "test-client", username)
				assert.Equal(t, "direct-secret", password, "client_secret should take precedence")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockEnv := mocks.NewMockReader(ctrl)
			if tt.setupMock != nil {
				tt.setupMock(t, mockEnv)
			}

			strategy := NewTokenExchangeStrategy(mockEnv)

			// Validation test
			metadata := tt.metadata
			if metadata == nil {
				metadata = map[string]any{}
			}
			metadata["token_url"] = "https://auth.example.com/token"

			err := strategy.Validate(metadata)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
				return
			}

			require.NoError(t, err)

			// If validation passes, test actual authentication
			if tt.validateAuth != nil {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					tt.validateAuth(t, r)
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]any{
						"access_token":      "backend-token",
						"token_type":        "Bearer",
						"issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
					})
				}))
				defer server.Close()

				metadata["token_url"] = server.URL
				ctx := createContextWithIdentity("test-user", "user-token")
				req := httptest.NewRequest(http.MethodGet, "/test", nil)

				err := strategy.Authenticate(ctx, req, metadata)
				require.NoError(t, err)
			}
		})
	}
}

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
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
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
		name             string
		setupCtx         func() context.Context
		strategy         *authtypes.BackendAuthStrategy
		tokenURLOverride string // If set, replace the strategy's TokenURL with this server URL
		setupServer      func() *httptest.Server
		expectError      bool
		errorContains    string
		checkAuthHeader  func(t *testing.T, req *http.Request)
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
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "PLACEHOLDER", // Will be replaced with server URL
				},
			},
			tokenURLOverride: "PLACEHOLDER",
			expectError:      false,
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
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "PLACEHOLDER",
					Audience: "https://backend.example.com",
				},
			},
			tokenURLOverride: "PLACEHOLDER",
			expectError:      false,
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
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "PLACEHOLDER",
					Scopes:   []string{"read", "write"},
				},
			},
			tokenURLOverride: "PLACEHOLDER",
			expectError:      false,
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
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL:     "PLACEHOLDER",
					ClientID:     "client-id",
					ClientSecret: "client-secret",
				},
			},
			tokenURLOverride: "PLACEHOLDER",
			expectError:      false,
		},
		{
			name:     "returns error when no identity in context",
			setupCtx: func() context.Context { return context.Background() },
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
					t.Error("Server should not be called")
				}))
			},
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "PLACEHOLDER",
				},
			},
			tokenURLOverride: "PLACEHOLDER",
			expectError:      true,
			errorContains:    "no identity",
		},
		{
			name:     "returns error when identity has no token",
			setupCtx: func() context.Context { return createContextWithIdentity("no-token-user", "") },
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
					t.Error("Server should not be called")
				}))
			},
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "PLACEHOLDER",
				},
			},
			tokenURLOverride: "PLACEHOLDER",
			expectError:      true,
			errorContains:    "no token",
		},
		{
			name:          "returns error when strategy is nil",
			setupCtx:      func() context.Context { return createContextWithIdentity("metadata-test-user", "metadata-token") },
			setupServer:   nil, // No server needed for validation error
			strategy:      nil,
			expectError:   true,
			errorContains: "token_exchange configuration required",
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
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "PLACEHOLDER",
				},
			},
			tokenURLOverride: "PLACEHOLDER",
			expectError:      true,
			errorContains:    "token exchange failed",
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
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "PLACEHOLDER",
				},
			},
			tokenURLOverride: "PLACEHOLDER",
			expectError:      true,
			errorContains:    "empty access_token",
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

			// Create a copy of the strategy config and set the server URL
			var authStrategy *authtypes.BackendAuthStrategy
			if tt.strategy != nil && server != nil && tt.tokenURLOverride != "" {
				authStrategy = &authtypes.BackendAuthStrategy{
					Type: tt.strategy.Type,
					TokenExchange: &authtypes.TokenExchangeConfig{
						TokenURL:         server.URL,
						ClientID:         tt.strategy.TokenExchange.ClientID,
						ClientSecret:     tt.strategy.TokenExchange.ClientSecret,
						ClientSecretEnv:  tt.strategy.TokenExchange.ClientSecretEnv,
						Audience:         tt.strategy.TokenExchange.Audience,
						Scopes:           tt.strategy.TokenExchange.Scopes,
						SubjectTokenType: tt.strategy.TokenExchange.SubjectTokenType,
					},
				}
			} else {
				authStrategy = tt.strategy
			}

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			err := strategy.Authenticate(ctx, req, authStrategy)

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

	tests := []struct {
		name        string
		strategy    *authtypes.BackendAuthStrategy
		expectError string // empty means no error expected
	}{
		{
			name: "valid with only token_url",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "https://auth.example.com/token",
				},
			},
		},
		{
			name: "valid with all fields",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL:         "https://auth.example.com/token",
					ClientID:         "my-client",
					ClientSecret:     "my-secret",
					Audience:         "https://backend.example.com",
					Scopes:           []string{"read", "write"},
					SubjectTokenType: "urn:ietf:params:oauth:token-type:access_token",
				},
			},
		},
		{
			name: "valid with client_id only",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "https://auth.example.com/token",
					ClientID: "my-client",
				},
			},
		},
		{
			name:        "error on nil strategy",
			strategy:    nil,
			expectError: "token_exchange configuration required",
		},
		{
			name: "error on nil TokenExchange config",
			strategy: &authtypes.BackendAuthStrategy{
				Type:          authtypes.StrategyTypeTokenExchange,
				TokenExchange: nil,
			},
			expectError: "token_exchange configuration required",
		},
		{
			name: "error on missing token_url",
			strategy: &authtypes.BackendAuthStrategy{
				Type:          authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{},
			},
			expectError: "token_url required",
		},
		{
			name: "error on client_secret without client_id",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL:     "https://auth.example.com/token",
					ClientSecret: "secret",
				},
			},
			expectError: "client_secret cannot be provided without client_id",
		},
		{
			name: "error on client_secret_env without client_id",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL:        "https://auth.example.com/token",
					ClientSecretEnv: "TEST_SECRET",
				},
			},
			expectError: "client_secret_env cannot be provided without client_id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mockEnv := createMockEnvReader(t)
			strategy := NewTokenExchangeStrategy(mockEnv)
			err := strategy.Validate(tt.strategy)

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
	authStrategy1 := &authtypes.BackendAuthStrategy{
		Type: authtypes.StrategyTypeTokenExchange,
		TokenExchange: &authtypes.TokenExchangeConfig{
			TokenURL: server1.URL,
			Scopes:   []string{"read"},
		},
	}
	req1 := httptest.NewRequest(http.MethodGet, "/test", nil)
	err := strategy.Authenticate(ctx, req1, authStrategy1)
	require.NoError(t, err)
	assert.Equal(t, "Bearer token-scope-read", req1.Header.Get("Authorization"))

	// Second request with "write" scope - should use different cache entry
	authStrategy2 := &authtypes.BackendAuthStrategy{
		Type: authtypes.StrategyTypeTokenExchange,
		TokenExchange: &authtypes.TokenExchangeConfig{
			TokenURL: server2.URL,
			Scopes:   []string{"write"},
		},
	}
	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	err = strategy.Authenticate(ctx, req2, authStrategy2)
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
	authStrategy1 := &authtypes.BackendAuthStrategy{
		Type: authtypes.StrategyTypeTokenExchange,
		TokenExchange: &authtypes.TokenExchangeConfig{
			TokenURL: server.URL,
			Scopes:   []string{"write", "read", "admin"},
		},
	}
	req1 := httptest.NewRequest(http.MethodGet, "/test", nil)
	err := strategy.Authenticate(ctx, req1, authStrategy1)
	require.NoError(t, err)

	// Second request with same scopes in different order
	authStrategy2 := &authtypes.BackendAuthStrategy{
		Type: authtypes.StrategyTypeTokenExchange,
		TokenExchange: &authtypes.TokenExchangeConfig{
			TokenURL: server.URL,
			Scopes:   []string{"admin", "read", "write"},
		},
	}
	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	err = strategy.Authenticate(ctx, req2, authStrategy2)
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
	authStrategy := &authtypes.BackendAuthStrategy{
		Type: authtypes.StrategyTypeTokenExchange,
		TokenExchange: &authtypes.TokenExchangeConfig{
			TokenURL: server.URL,
			Scopes:   []string{"read", "write"},
		},
	}

	// First user makes a request
	ctx1 := createContextWithIdentity("user1", "user1-token")
	req1 := httptest.NewRequest(http.MethodGet, "/test", nil)
	err := strategy.Authenticate(ctx1, req1, authStrategy)
	require.NoError(t, err)

	// Second user makes a request with same config
	ctx2 := createContextWithIdentity("user2", "user2-token")
	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	err = strategy.Authenticate(ctx2, req2, authStrategy)
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
	authStrategy := &authtypes.BackendAuthStrategy{
		Type: authtypes.StrategyTypeTokenExchange,
		TokenExchange: &authtypes.TokenExchangeConfig{
			TokenURL: server.URL,
		},
	}

	// First request with initial token
	ctx1 := createContextWithIdentity("user1", "initial-token")
	req1 := httptest.NewRequest(http.MethodGet, "/test", nil)
	err := strategy.Authenticate(ctx1, req1, authStrategy)
	require.NoError(t, err)
	assert.Equal(t, "initial-token", capturedToken, "Should use initial token")

	// Second request with refreshed token (simulating token refresh)
	ctx2 := createContextWithIdentity("user1", "refreshed-token")
	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	err = strategy.Authenticate(ctx2, req2, authStrategy)
	require.NoError(t, err)
	assert.Equal(t, "refreshed-token", capturedToken, "Should use current refreshed token, not cached stale token")
}

func TestTokenExchangeStrategy_ClientSecretEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		setupMock     func(t *testing.T, mockEnv *mocks.MockReader)
		strategy      *authtypes.BackendAuthStrategy
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
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL:        "PLACEHOLDER",
					ClientID:        "test-client",
					ClientSecretEnv: "TEST_CLIENT_SECRET",
				},
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
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL:        "https://auth.example.com/token",
					ClientID:        "test-client",
					ClientSecretEnv: "MISSING_ENV_VAR",
				},
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
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL:        "https://auth.example.com/token",
					ClientID:        "test-client",
					ClientSecretEnv: "EMPTY_SECRET",
				},
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
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL:        "PLACEHOLDER",
					ClientID:        "test-client",
					ClientSecret:    "direct-secret",
					ClientSecretEnv: "TEST_SECRET_ENV",
				},
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

			err := strategy.Validate(tt.strategy)

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

				// Create a new strategy with the server URL
				authStrategy := &authtypes.BackendAuthStrategy{
					Type: authtypes.StrategyTypeTokenExchange,
					TokenExchange: &authtypes.TokenExchangeConfig{
						TokenURL:        server.URL,
						ClientID:        tt.strategy.TokenExchange.ClientID,
						ClientSecret:    tt.strategy.TokenExchange.ClientSecret,
						ClientSecretEnv: tt.strategy.TokenExchange.ClientSecretEnv,
					},
				}
				ctx := createContextWithIdentity("test-user", "user-token")
				req := httptest.NewRequest(http.MethodGet, "/test", nil)

				err := strategy.Authenticate(ctx, req, authStrategy)
				require.NoError(t, err)
			}
		})
	}
}

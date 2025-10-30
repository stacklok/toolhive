package strategies

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp/auth"
)

func TestTokenExchangeStrategy_Name(t *testing.T) {
	t.Parallel()

	strategy := NewTokenExchangeStrategy()
	assert.Equal(t, "token_exchange", strategy.Name())
}

func TestTokenExchangeStrategy_Authenticate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		setupCtx            func() context.Context
		metadata            map[string]any
		setupServer         func() *httptest.Server
		expectError         bool
		errorContains       string
		checkAuthHeader     func(t *testing.T, req *http.Request)
		validateServerCalls func(t *testing.T, callCount int)
	}{
		{
			name: "successfully exchanges token",
			setupCtx: func() context.Context {
				return auth.WithIdentity(context.Background(), &auth.Identity{
					Subject: "user123",
					Token:   "client-token",
				})
			},
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					t.Helper()
					// Verify request format
					assert.Equal(t, "POST", r.Method)
					assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))

					// Parse form
					err := r.ParseForm()
					require.NoError(t, err)

					// Verify token exchange parameters
					assert.Equal(t, "urn:ietf:params:oauth:grant-type:token-exchange", r.Form.Get("grant_type"))
					assert.Equal(t, "client-token", r.Form.Get("subject_token"))

					// Return successful response
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]any{
						"access_token":      "backend-token-123",
						"token_type":        "Bearer",
						"issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
						"expires_in":        3600,
					})
				}))
			},
			expectError: false,
			checkAuthHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				assert.Equal(t, "Bearer backend-token-123", req.Header.Get("Authorization"))
			},
		},
		{
			name: "includes audience in token exchange",
			setupCtx: func() context.Context {
				return auth.WithIdentity(context.Background(), &auth.Identity{
					Subject: "user123",
					Token:   "client-token",
				})
			},
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					t.Helper()
					err := r.ParseForm()
					require.NoError(t, err)

					// Verify audience is included
					assert.Equal(t, "https://backend.example.com", r.Form.Get("audience"))

					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]any{
						"access_token":      "backend-token",
						"token_type":        "Bearer",
						"issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
					})
				}))
			},
			expectError: false,
		},
		{
			name: "includes scopes in token exchange",
			setupCtx: func() context.Context {
				return auth.WithIdentity(context.Background(), &auth.Identity{
					Subject: "user123",
					Token:   "client-token",
				})
			},
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					t.Helper()
					err := r.ParseForm()
					require.NoError(t, err)

					// Verify scopes are included
					assert.Equal(t, "read write", r.Form.Get("scope"))

					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]any{
						"access_token":      "backend-token",
						"token_type":        "Bearer",
						"issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
					})
				}))
			},
			expectError: false,
		},
		{
			name: "includes client credentials in token exchange",
			setupCtx: func() context.Context {
				return auth.WithIdentity(context.Background(), &auth.Identity{
					Subject: "user123",
					Token:   "client-token",
				})
			},
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					t.Helper()
					// Verify Basic Auth header
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
			expectError: false,
		},
		{
			name: "caches token source for same user and backend",
			setupCtx: func() context.Context {
				return auth.WithIdentity(context.Background(), &auth.Identity{
					Subject: "user123",
					Token:   "client-token",
				})
			},
			setupServer: func() *httptest.Server {
				callCount := 0
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					callCount++

					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]any{
						"access_token":      fmt.Sprintf("backend-token-%d", callCount),
						"token_type":        "Bearer",
						"issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
						"expires_in":        3600, // Token valid for 1 hour
					})
				}))
			},
			expectError: false,
			validateServerCalls: func(t *testing.T, callCount int) {
				t.Helper()
				// Should only call server once due to caching
				assert.Equal(t, 1, callCount)
			},
		},
		{
			name: "returns error when no identity in context",
			setupCtx: func() context.Context {
				return context.Background() // No identity
			},
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
					t.Error("Server should not be called")
				}))
			},
			expectError:   true,
			errorContains: "no identity",
		},
		{
			name: "returns error when identity has no token",
			setupCtx: func() context.Context {
				return auth.WithIdentity(context.Background(), &auth.Identity{
					Subject: "user123",
					Token:   "",
				})
			},
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
					t.Error("Server should not be called")
				}))
			},
			expectError:   true,
			errorContains: "no token",
		},
		{
			name: "returns error when metadata is invalid",
			setupCtx: func() context.Context {
				return auth.WithIdentity(context.Background(), &auth.Identity{
					Subject: "user123",
					Token:   "client-token",
				})
			},
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
					t.Error("Server should not be called")
				}))
			},
			expectError:   true,
			errorContains: "invalid metadata",
		},
		{
			name: "returns error when token exchange fails",
			setupCtx: func() context.Context {
				return auth.WithIdentity(context.Background(), &auth.Identity{
					Subject: "user123",
					Token:   "client-token",
				})
			},
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
			name: "returns error when response is missing access_token",
			setupCtx: func() context.Context {
				return auth.WithIdentity(context.Background(), &auth.Identity{
					Subject: "user123",
					Token:   "client-token",
				})
			},
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
		{
			name: "handles concurrent requests for same user",
			setupCtx: func() context.Context {
				return auth.WithIdentity(context.Background(), &auth.Identity{
					Subject: "user123",
					Token:   "client-token",
				})
			},
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					// Simulate slow token exchange
					time.Sleep(10 * time.Millisecond)

					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]any{
						"access_token":      "backend-token",
						"token_type":        "Bearer",
						"issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
						"expires_in":        3600,
					})
				}))
			},
			expectError: false,
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

			strategy := NewTokenExchangeStrategy()
			ctx := tt.setupCtx()

			// Build metadata with server URL if available
			metadata := tt.metadata
			if metadata == nil {
				metadata = map[string]any{}
			}
			if server != nil {
				metadata["token_url"] = server.URL
			}
			if tt.name == "includes audience in token exchange" {
				metadata["audience"] = "https://backend.example.com"
			}
			if tt.name == "includes scopes in token exchange" {
				metadata["scopes"] = []string{"read", "write"}
			}
			if tt.name == "includes client credentials in token exchange" {
				metadata["client_id"] = "client-id"
				metadata["client_secret"] = "client-secret"
			}
			if tt.name == "returns error when metadata is invalid" {
				// Intentionally leave token_url missing
				delete(metadata, "token_url")
			}

			// For concurrent test, make multiple requests with separate request objects
			if tt.name == "handles concurrent requests for same user" {
				var wg sync.WaitGroup
				for i := 0; i < 5; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						// Each goroutine gets its own request object to avoid data races
						req := httptest.NewRequest(http.MethodGet, "/test", nil)
						err := strategy.Authenticate(ctx, req, metadata)
						require.NoError(t, err)
						// Verify token was set correctly
						assert.Equal(t, "Bearer backend-token", req.Header.Get("Authorization"))
					}()
				}
				wg.Wait()
				return
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

			// Test caching by making a second request
			if tt.name == "caches token source for same user and backend" {
				req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
				err := strategy.Authenticate(ctx, req2, metadata)
				require.NoError(t, err)
				// Token should be cached, so same token returned
				assert.Equal(t, "Bearer backend-token-1", req2.Header.Get("Authorization"))
			}
		})
	}
}

func TestTokenExchangeStrategy_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		metadata      map[string]any
		expectError   bool
		errorContains string
	}{
		{
			name: "valid with only token_url",
			metadata: map[string]any{
				"token_url": "https://auth.example.com/token",
			},
			expectError: false,
		},
		{
			name: "valid with all optional fields",
			metadata: map[string]any{
				"token_url":          "https://auth.example.com/token",
				"client_id":          "my-client",
				"client_secret":      "my-secret",
				"audience":           "https://backend.example.com",
				"scopes":             []string{"read", "write"},
				"subject_token_type": "access_token",
			},
			expectError: false,
		},
		{
			name: "valid with string scopes",
			metadata: map[string]any{
				"token_url": "https://auth.example.com/token",
				"scopes":    "read",
			},
			expectError: false,
		},
		{
			name: "valid with full URN subject_token_type",
			metadata: map[string]any{
				"token_url":          "https://auth.example.com/token",
				"subject_token_type": "urn:ietf:params:oauth:token-type:access_token",
			},
			expectError: false,
		},
		{
			name: "valid with id_token subject_token_type",
			metadata: map[string]any{
				"token_url":          "https://auth.example.com/token",
				"subject_token_type": "id_token",
			},
			expectError: false,
		},
		{
			name: "valid with jwt subject_token_type",
			metadata: map[string]any{
				"token_url":          "https://auth.example.com/token",
				"subject_token_type": "jwt",
			},
			expectError: false,
		},
		{
			name: "valid with client_id but no client_secret",
			metadata: map[string]any{
				"token_url": "https://auth.example.com/token",
				"client_id": "my-client",
			},
			expectError: false,
		},
		{
			name: "returns error when token_url is missing",
			metadata: map[string]any{
				"client_id": "my-client",
			},
			expectError:   true,
			errorContains: "token_url required",
		},
		{
			name: "returns error when token_url is empty",
			metadata: map[string]any{
				"token_url": "",
			},
			expectError:   true,
			errorContains: "token_url required",
		},
		{
			name: "returns error when token_url is not a string",
			metadata: map[string]any{
				"token_url": 123,
			},
			expectError:   true,
			errorContains: "token_url required",
		},
		{
			name: "returns error when client_secret without client_id",
			metadata: map[string]any{
				"token_url":     "https://auth.example.com/token",
				"client_secret": "my-secret",
			},
			expectError:   true,
			errorContains: "client_secret cannot be provided without client_id",
		},
		{
			name: "returns error when scopes is invalid type",
			metadata: map[string]any{
				"token_url": "https://auth.example.com/token",
				"scopes":    123,
			},
			expectError:   true,
			errorContains: "scopes must be a string or array",
		},
		{
			name: "returns error when scopes array contains non-string",
			metadata: map[string]any{
				"token_url": "https://auth.example.com/token",
				"scopes":    []any{"read", 123, "write"},
			},
			expectError:   true,
			errorContains: "scopes[1] must be a string",
		},
		{
			name: "returns error when subject_token_type is invalid",
			metadata: map[string]any{
				"token_url":          "https://auth.example.com/token",
				"subject_token_type": "invalid_type",
			},
			expectError:   true,
			errorContains: "invalid subject_token_type",
		},
		{
			name:          "returns error when metadata is nil",
			metadata:      nil,
			expectError:   true,
			errorContains: "token_url required",
		},
		{
			name:          "returns error when metadata is empty",
			metadata:      map[string]any{},
			expectError:   true,
			errorContains: "token_url required",
		},
		{
			name: "valid with empty scopes array",
			metadata: map[string]any{
				"token_url": "https://auth.example.com/token",
				"scopes":    []string{},
			},
			expectError: false,
		},
		{
			name: "valid with extra metadata fields",
			metadata: map[string]any{
				"token_url": "https://auth.example.com/token",
				"extra":     "ignored",
				"count":     123,
			},
			expectError: false,
		},
		{
			name: "returns error when client_id is not a string",
			metadata: map[string]any{
				"token_url": "https://auth.example.com/token",
				"client_id": 123,
			},
			expectError: false, // client_id is optional, wrong type is ignored
		},
		{
			name: "returns error when audience is not a string",
			metadata: map[string]any{
				"token_url": "https://auth.example.com/token",
				"audience":  123,
			},
			expectError: false, // audience is optional, wrong type is ignored
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			strategy := NewTokenExchangeStrategy()
			err := strategy.Validate(tt.metadata)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestParseTokenExchangeMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		metadata       map[string]any
		expectError    bool
		errorContains  string
		validateConfig func(t *testing.T, config *tokenExchangeConfig)
	}{
		{
			name: "parses all fields correctly",
			metadata: map[string]any{
				"token_url":          "https://auth.example.com/token",
				"client_id":          "my-client",
				"client_secret":      "my-secret",
				"audience":           "https://backend.example.com",
				"scopes":             []string{"read", "write"},
				"subject_token_type": "access_token",
			},
			expectError: false,
			validateConfig: func(t *testing.T, config *tokenExchangeConfig) {
				t.Helper()
				assert.Equal(t, "https://auth.example.com/token", config.TokenURL)
				assert.Equal(t, "my-client", config.ClientID)
				assert.Equal(t, "my-secret", config.ClientSecret)
				assert.Equal(t, "https://backend.example.com", config.Audience)
				assert.Equal(t, []string{"read", "write"}, config.Scopes)
				assert.Equal(t, "urn:ietf:params:oauth:token-type:access_token", config.SubjectTokenType)
			},
		},
		{
			name: "handles []any scopes",
			metadata: map[string]any{
				"token_url": "https://auth.example.com/token",
				"scopes":    []any{"read", "write"},
			},
			expectError: false,
			validateConfig: func(t *testing.T, config *tokenExchangeConfig) {
				t.Helper()
				assert.Equal(t, []string{"read", "write"}, config.Scopes)
			},
		},
		{
			name: "handles single string scope",
			metadata: map[string]any{
				"token_url": "https://auth.example.com/token",
				"scopes":    "read",
			},
			expectError: false,
			validateConfig: func(t *testing.T, config *tokenExchangeConfig) {
				t.Helper()
				assert.Equal(t, []string{"read"}, config.Scopes)
			},
		},
		{
			name: "normalizes subject_token_type",
			metadata: map[string]any{
				"token_url":          "https://auth.example.com/token",
				"subject_token_type": "id_token",
			},
			expectError: false,
			validateConfig: func(t *testing.T, config *tokenExchangeConfig) {
				t.Helper()
				assert.Equal(t, "urn:ietf:params:oauth:token-type:id_token", config.SubjectTokenType)
			},
		},
		{
			name: "returns error for invalid scopes type",
			metadata: map[string]any{
				"token_url": "https://auth.example.com/token",
				"scopes":    123,
			},
			expectError:   true,
			errorContains: "scopes must be a string or array",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config, err := parseTokenExchangeMetadata(tt.metadata)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, config)
			if tt.validateConfig != nil {
				tt.validateConfig(t, config)
			}
		})
	}
}

func TestBuildCacheKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		subject     string
		config      *tokenExchangeConfig
		expectedKey string
	}{
		{
			name:    "creates unique key from subject, token_url, and audience",
			subject: "user123",
			config: &tokenExchangeConfig{
				TokenURL: "https://auth.example.com/token",
				Audience: "https://backend.example.com",
			},
			expectedKey: "user123:https://auth.example.com/token:https://backend.example.com",
		},
		{
			name:    "different subjects produce different keys",
			subject: "user456",
			config: &tokenExchangeConfig{
				TokenURL: "https://auth.example.com/token",
				Audience: "https://backend.example.com",
			},
			expectedKey: "user456:https://auth.example.com/token:https://backend.example.com",
		},
		{
			name:    "different audiences produce different keys",
			subject: "user123",
			config: &tokenExchangeConfig{
				TokenURL: "https://auth.example.com/token",
				Audience: "https://different-backend.example.com",
			},
			expectedKey: "user123:https://auth.example.com/token:https://different-backend.example.com",
		},
		{
			name:    "handles empty audience",
			subject: "user123",
			config: &tokenExchangeConfig{
				TokenURL: "https://auth.example.com/token",
				Audience: "",
			},
			expectedKey: "user123:https://auth.example.com/token:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			key := buildCacheKey(tt.subject, tt.config)
			assert.Equal(t, tt.expectedKey, key)
		})
	}

	// Additional test: verify different subjects produce different keys
	t.Run("verify different subjects produce different keys", func(t *testing.T) {
		config := &tokenExchangeConfig{
			TokenURL: "https://auth.example.com/token",
			Audience: "https://backend.example.com",
		}
		key1 := buildCacheKey("user123", config)
		key2 := buildCacheKey("user456", config)
		assert.NotEqual(t, key1, key2)
	})
}

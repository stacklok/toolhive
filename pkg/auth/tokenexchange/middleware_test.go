package tokenexchange

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/transport/types/mocks"
)

// TestValidateTokenExchangeConfig tests configuration validation.
func TestValidateTokenExchangeConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		config      *Config
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid replace strategy explicit",
			config: &Config{
				HeaderStrategy: HeaderStrategyReplace,
			},
			expectError: false,
		},
		{
			name: "valid custom strategy with header name",
			config: &Config{
				HeaderStrategy:          HeaderStrategyCustom,
				ExternalTokenHeaderName: "X-Upstream-Token",
			},
			expectError: false,
		},
		{
			name: "valid empty strategy defaults to replace",
			config: &Config{
				HeaderStrategy: "",
			},
			expectError: false,
		},
		{
			name: "invalid custom strategy missing header name",
			config: &Config{
				HeaderStrategy: HeaderStrategyCustom,
			},
			expectError: true,
			errorMsg:    "external_token_header_name must be specified",
		},
		{
			name: "invalid strategy name",
			config: &Config{
				HeaderStrategy: "invalid-strategy",
			},
			expectError: true,
			errorMsg:    "invalid header_strategy",
		},
		{
			name: "unknown strategy",
			config: &Config{
				HeaderStrategy: "query-param",
			},
			expectError: true,
			errorMsg:    "invalid header_strategy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateTokenExchangeConfig(tt.config)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestInjectToken tests the token injection strategies.
func TestInjectToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		config               Config
		originalAuthHeader   string
		newToken             string
		expectError          bool
		errorMsg             string
		expectedAuthHeader   string
		expectedCustomHeader string
		customHeaderName     string
	}{
		{
			name: "replace strategy replaces Authorization header",
			config: Config{
				HeaderStrategy: HeaderStrategyReplace,
			},
			originalAuthHeader: "Bearer original-token",
			newToken:           "new-token",
			expectError:        false,
			expectedAuthHeader: "Bearer new-token",
		},
		{
			name: "empty strategy defaults to replace",
			config: Config{
				HeaderStrategy: "",
			},
			originalAuthHeader: "Bearer original-token",
			newToken:           "new-token",
			expectError:        false,
			expectedAuthHeader: "Bearer new-token",
		},
		{
			name: "custom strategy preserves original and adds custom header",
			config: Config{
				HeaderStrategy:          HeaderStrategyCustom,
				ExternalTokenHeaderName: "X-Upstream-Token",
			},
			originalAuthHeader:   "Bearer original-token",
			newToken:             "new-token",
			expectError:          false,
			expectedAuthHeader:   "Bearer original-token",
			expectedCustomHeader: "Bearer new-token",
			customHeaderName:     "X-Upstream-Token",
		},
		{
			name: "custom strategy with different header name",
			config: Config{
				HeaderStrategy:          HeaderStrategyCustom,
				ExternalTokenHeaderName: "X-External-Auth",
			},
			originalAuthHeader:   "Bearer original-token",
			newToken:             "exchanged-token",
			expectError:          false,
			expectedAuthHeader:   "Bearer original-token",
			expectedCustomHeader: "Bearer exchanged-token",
			customHeaderName:     "X-External-Auth",
		},
		{
			name: "custom strategy missing header name fails",
			config: Config{
				HeaderStrategy: HeaderStrategyCustom,
			},
			newToken:    "new-token",
			expectError: true,
			errorMsg:    "external_token_header_name must be specified",
		},
		{
			name: "unsupported strategy fails",
			config: Config{
				HeaderStrategy: "unsupported-strategy",
			},
			newToken:    "new-token",
			expectError: true,
			errorMsg:    "unsupported header_strategy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			if tt.originalAuthHeader != "" {
				req.Header.Set("Authorization", tt.originalAuthHeader)
			}

			// Create the injector function based on the strategy (mimics createTokenExchangeMiddleware)
			strategy := tt.config.HeaderStrategy
			if strategy == "" {
				strategy = HeaderStrategyReplace
			}

			var injectToken injectionFunc
			switch strategy {
			case HeaderStrategyReplace:
				injectToken = createReplaceInjector()
			case HeaderStrategyCustom:
				injectToken = createCustomInjector(tt.config.ExternalTokenHeaderName)
			default:
				injectToken = func(_ *http.Request, _ string) error {
					return fmt.Errorf("unsupported header_strategy: %s (valid values: '%s', '%s')",
						strategy, HeaderStrategyReplace, HeaderStrategyCustom)
				}
			}

			err := injectToken(req, tt.newToken)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedAuthHeader, req.Header.Get("Authorization"))
				if tt.customHeaderName != "" {
					assert.Equal(t, tt.expectedCustomHeader, req.Header.Get(tt.customHeaderName))
				}
			}
		})
	}
}

// TestCreateTokenExchangeMiddleware_Success tests successful token exchange flow.
func TestCreateTokenExchangeMiddleware_Success(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                   string
		headerStrategy         string
		customHeaderName       string
		scopes                 []string
		expectedAuthHeader     string
		expectedCustomHeader   string
		expectedScopesReceived string
	}{
		{
			name:                   "replace strategy",
			headerStrategy:         HeaderStrategyReplace,
			scopes:                 nil,
			expectedAuthHeader:     "Bearer exchanged-token",
			expectedScopesReceived: "",
		},
		{
			name:                   "custom strategy",
			headerStrategy:         HeaderStrategyCustom,
			customHeaderName:       "X-Upstream-Token",
			scopes:                 nil,
			expectedAuthHeader:     "Bearer original-token",
			expectedCustomHeader:   "Bearer exchanged-token",
			expectedScopesReceived: "",
		},
		{
			name:                   "with scopes",
			headerStrategy:         HeaderStrategyReplace,
			scopes:                 []string{"read", "write", "admin"},
			expectedAuthHeader:     "Bearer exchanged-token",
			expectedScopesReceived: "read write admin",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var receivedScopes string

			// Create mock OAuth server
			exchangeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.expectedScopesReceived != "" {
					_ = r.ParseForm()
					receivedScopes = r.Form.Get("scope")
				}

				resp := response{
					AccessToken:     "exchanged-token",
					TokenType:       "Bearer",
					IssuedTokenType: "urn:ietf:params:oauth:token-type:access_token",
					ExpiresIn:       3600,
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer exchangeServer.Close()

			config := Config{
				TokenURL:                exchangeServer.URL,
				ClientID:                "test-client-id",
				ClientSecret:            "test-client-secret",
				Audience:                "https://api.example.com",
				Scopes:                  tt.scopes,
				HeaderStrategy:          tt.headerStrategy,
				ExternalTokenHeaderName: tt.customHeaderName,
			}

			middleware, err := createTokenExchangeMiddleware(config, nil, defaultEnvGetter)
			require.NoError(t, err)

			// Test handler verifies token injection
			testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, tt.expectedAuthHeader, r.Header.Get("Authorization"))
				if tt.customHeaderName != "" {
					assert.Equal(t, tt.expectedCustomHeader, r.Header.Get(tt.customHeaderName))
				}
				w.WriteHeader(http.StatusOK)
			})

			// Create request with claims and token
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req.Header.Set("Authorization", "Bearer original-token")
			claims := jwt.MapClaims{
				"sub": "user123",
				"aud": "test-audience",
			}
			ctx := context.WithValue(req.Context(), auth.ClaimsContextKey{}, claims)
			req = req.WithContext(ctx)

			// Execute middleware
			rec := httptest.NewRecorder()
			handler := middleware(testHandler)
			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code)
			if tt.expectedScopesReceived != "" {
				assert.Equal(t, tt.expectedScopesReceived, receivedScopes)
			}
		})
	}
}

// TestCreateTokenExchangeMiddleware_PassThrough tests cases where middleware passes through.
func TestCreateTokenExchangeMiddleware_PassThrough(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setupReq    func(*http.Request) *http.Request
		description string
	}{
		{
			name: "no claims in context",
			setupReq: func(req *http.Request) *http.Request {
				req.Header.Set("Authorization", "Bearer original-token")
				return req
			},
			description: "should pass through without token exchange",
		},
		{
			name: "no Authorization header",
			setupReq: func(req *http.Request) *http.Request {
				claims := jwt.MapClaims{"sub": "user123"}
				ctx := context.WithValue(req.Context(), auth.ClaimsContextKey{}, claims)
				return req.WithContext(ctx)
			},
			description: "should pass through without token exchange",
		},
		{
			name: "non-Bearer token",
			setupReq: func(req *http.Request) *http.Request {
				req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
				claims := jwt.MapClaims{"sub": "user123"}
				ctx := context.WithValue(req.Context(), auth.ClaimsContextKey{}, claims)
				return req.WithContext(ctx)
			},
			description: "should pass through with non-Bearer auth",
		},
		{
			name: "empty Bearer token",
			setupReq: func(req *http.Request) *http.Request {
				req.Header.Set("Authorization", "Bearer ")
				claims := jwt.MapClaims{"sub": "user123"}
				ctx := context.WithValue(req.Context(), auth.ClaimsContextKey{}, claims)
				return req.WithContext(ctx)
			},
			description: "should pass through with empty Bearer token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := Config{
				TokenURL:     "https://example.com/token",
				ClientID:     "test-client-id",
				ClientSecret: "test-client-secret",
			}

			middleware, err := createTokenExchangeMiddleware(config, nil, defaultEnvGetter)
			require.NoError(t, err)

			handlerCalled := false
			testHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				handlerCalled = true
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req = tt.setupReq(req)

			rec := httptest.NewRecorder()
			handler := middleware(testHandler)
			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code, tt.description)
			assert.True(t, handlerCalled, "handler should be called")
		})
	}
}

// TestCreateTokenExchangeMiddleware_Failures tests error scenarios.
func TestCreateTokenExchangeMiddleware_Failures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		serverResponse     func(w http.ResponseWriter, r *http.Request)
		headerStrategy     string
		customHeaderName   string
		expectedStatusCode int
		expectedBodyMsg    string
	}{
		{
			name: "token exchange returns 401",
			serverResponse: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
			},
			headerStrategy:     HeaderStrategyReplace,
			expectedStatusCode: http.StatusUnauthorized,
			expectedBodyMsg:    "Token exchange failed",
		},
		{
			name: "token exchange returns 500",
			serverResponse: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"server_error"}`))
			},
			headerStrategy:     HeaderStrategyReplace,
			expectedStatusCode: http.StatusUnauthorized,
			expectedBodyMsg:    "Token exchange failed",
		},
		{
			name: "invalid injection config",
			serverResponse: func(w http.ResponseWriter, _ *http.Request) {
				resp := response{
					AccessToken:     "exchanged-token",
					TokenType:       "Bearer",
					IssuedTokenType: "urn:ietf:params:oauth:token-type:access_token",
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)
			},
			headerStrategy:     HeaderStrategyCustom,
			customHeaderName:   "", // Missing header name causes injection failure
			expectedStatusCode: http.StatusInternalServerError,
			expectedBodyMsg:    "Token injection failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			exchangeServer := httptest.NewServer(http.HandlerFunc(tt.serverResponse))
			defer exchangeServer.Close()

			config := Config{
				TokenURL:                exchangeServer.URL,
				ClientID:                "test-client-id",
				ClientSecret:            "test-client-secret",
				HeaderStrategy:          tt.headerStrategy,
				ExternalTokenHeaderName: tt.customHeaderName,
			}

			middleware, err := createTokenExchangeMiddleware(config, nil, defaultEnvGetter)
			require.NoError(t, err)

			testHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
				t.Fatal("handler should not be called on failure")
			})

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req.Header.Set("Authorization", "Bearer original-token")
			claims := jwt.MapClaims{"sub": "user123"}
			ctx := context.WithValue(req.Context(), auth.ClaimsContextKey{}, claims)
			req = req.WithContext(ctx)

			rec := httptest.NewRecorder()
			handler := middleware(testHandler)
			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.expectedStatusCode, rec.Code)
			assert.Contains(t, rec.Body.String(), tt.expectedBodyMsg)
		})
	}
}

// TestCreateMiddleware tests the factory function.
func TestCreateMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		params              MiddlewareParams
		expectError         bool
		errorMsg            string
		expectAddMiddleware bool
	}{
		{
			name: "valid config creates middleware",
			params: MiddlewareParams{
				TokenExchangeConfig: &Config{
					TokenURL:       "https://example.com/token",
					ClientID:       "test-client-id",
					ClientSecret:   "test-client-secret",
					HeaderStrategy: HeaderStrategyReplace,
				},
			},
			expectError:         false,
			expectAddMiddleware: true,
		},
		{
			name: "nil config returns error",
			params: MiddlewareParams{
				TokenExchangeConfig: nil,
			},
			expectError:         true,
			errorMsg:            "token exchange configuration is required",
			expectAddMiddleware: false,
		},
		{
			name: "invalid config fails validation",
			params: MiddlewareParams{
				TokenExchangeConfig: &Config{
					HeaderStrategy: HeaderStrategyCustom,
					// Missing ExternalTokenHeaderName
				},
			},
			expectError:         true,
			errorMsg:            "invalid token exchange configuration",
			expectAddMiddleware: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockRunner := mocks.NewMockMiddlewareRunner(ctrl)

			if tt.expectAddMiddleware {
				mockRunner.EXPECT().AddMiddleware(gomock.Any()).Do(func(mw types.Middleware) {
					_, ok := mw.(*Middleware)
					assert.True(t, ok, "Expected middleware to be of type *tokenexchange.Middleware")
				})
			}

			paramsJSON, err := json.Marshal(tt.params)
			require.NoError(t, err)

			config := &types.MiddlewareConfig{
				Type:       MiddlewareType,
				Parameters: paramsJSON,
			}

			err = CreateMiddleware(config, mockRunner)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestCreateMiddleware_InvalidJSON tests error handling for malformed parameters.
func TestCreateMiddleware_InvalidJSON(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRunner := mocks.NewMockMiddlewareRunner(ctrl)

	config := &types.MiddlewareConfig{
		Type:       MiddlewareType,
		Parameters: []byte(`{invalid json}`),
	}

	err := CreateMiddleware(config, mockRunner)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to unmarshal token exchange middleware parameters")
}

// TestMiddleware_Methods tests the Middleware struct methods.
func TestMiddleware_Methods(t *testing.T) {
	t.Parallel()

	middlewareFunc := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
		})
	}

	mw := &Middleware{
		middleware: middlewareFunc,
	}

	// Test Handler returns the function
	handler := mw.Handler()
	assert.NotNil(t, handler)

	// Test Close returns no error
	err := mw.Close()
	assert.NoError(t, err)
}

// TestCreateTokenExchangeMiddleware_EnvironmentVariable tests client secret from environment variable.
func TestCreateTokenExchangeMiddleware_EnvironmentVariable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		configClientSecret   string
		envClientSecret      string
		expectedClientSecret string
		description          string
	}{
		{
			name:                 "config secret takes precedence over env var",
			configClientSecret:   "config-secret",
			envClientSecret:      "env-secret",
			expectedClientSecret: "config-secret",
			description:          "should use client secret from config when provided",
		},
		{
			name:                 "env var used when config secret is empty",
			configClientSecret:   "",
			envClientSecret:      "env-secret",
			expectedClientSecret: "env-secret",
			description:          "should fallback to environment variable when config is empty",
		},
		{
			name:                 "empty when both are empty",
			configClientSecret:   "",
			envClientSecret:      "",
			expectedClientSecret: "",
			description:          "should be empty when neither config nor env var is set",
		},
		{
			name:                 "config secret used when env var is empty",
			configClientSecret:   "config-secret",
			envClientSecret:      "",
			expectedClientSecret: "config-secret",
			description:          "should use config secret when env var is empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Track which client secret was actually used in the request
			var receivedClientSecret string

			// Create mock OAuth server that captures the client secret
			exchangeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Extract client secret from Basic Auth header
				_, password, ok := r.BasicAuth()
				if ok {
					receivedClientSecret = password
				}

				resp := response{
					AccessToken:     "exchanged-token",
					TokenType:       "Bearer",
					IssuedTokenType: "urn:ietf:params:oauth:token-type:access_token",
					ExpiresIn:       3600,
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer exchangeServer.Close()

			// Mock environment getter
			mockEnvGetter := func(key string) string {
				if key == EnvClientSecret {
					return tt.envClientSecret
				}
				return ""
			}

			config := Config{
				TokenURL:     exchangeServer.URL,
				ClientID:     "test-client-id",
				ClientSecret: tt.configClientSecret,
				Audience:     "https://api.example.com",
			}

			// Use the internal function with mock env getter
			middleware, err := createTokenExchangeMiddleware(config, nil, mockEnvGetter)
			require.NoError(t, err)

			// Test handler
			testHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			// Create request with claims and token
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req.Header.Set("Authorization", "Bearer original-token")
			claims := jwt.MapClaims{
				"sub": "user123",
				"aud": "test-audience",
			}
			ctx := context.WithValue(req.Context(), auth.ClaimsContextKey{}, claims)
			req = req.WithContext(ctx)

			// Execute middleware
			rec := httptest.NewRecorder()
			handler := middleware(testHandler)
			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, tt.expectedClientSecret, receivedClientSecret, tt.description)
		})
	}
}

func TestCreateMiddlewareFromTokenSource(t *testing.T) {
	tests := []struct {
		name               string
		subjectTokenType   string
		tokenSourceFactory func() oauth2.TokenSource
		expectedToken      string
		expectError        bool
		errorContains      string
	}{
		{
			name:             "access token (default)",
			subjectTokenType: "",
			tokenSourceFactory: func() oauth2.TokenSource {
				return oauth2.StaticTokenSource(&oauth2.Token{
					AccessToken: "test-access-token",
				})
			},
			expectedToken: "test-access-token",
			expectError:   false,
		},
		{
			name:             "access token (explicit)",
			subjectTokenType: "urn:ietf:params:oauth:token-type:access_token",
			tokenSourceFactory: func() oauth2.TokenSource {
				return oauth2.StaticTokenSource(&oauth2.Token{
					AccessToken: "test-access-token-explicit",
				})
			},
			expectedToken: "test-access-token-explicit",
			expectError:   false,
		},
		{
			name:             "ID token",
			subjectTokenType: "urn:ietf:params:oauth:token-type:id_token",
			tokenSourceFactory: func() oauth2.TokenSource {
				token := &oauth2.Token{
					AccessToken: "test-access-token",
				}
				// Simulate ID token in Extra field
				token = token.WithExtra(map[string]interface{}{
					"id_token": "test-id-token-jwt",
				})
				return oauth2.StaticTokenSource(token)
			},
			expectedToken: "test-id-token-jwt",
			expectError:   false,
		},
		{
			name:             "JWT token type",
			subjectTokenType: "urn:ietf:params:oauth:token-type:jwt",
			tokenSourceFactory: func() oauth2.TokenSource {
				token := &oauth2.Token{
					AccessToken: "test-access-token",
				}
				token = token.WithExtra(map[string]interface{}{
					"id_token": "test-jwt-token",
				})
				return oauth2.StaticTokenSource(token)
			},
			expectedToken: "test-jwt-token",
			expectError:   false,
		},
		{
			name:             "ID token not available",
			subjectTokenType: "urn:ietf:params:oauth:token-type:id_token",
			tokenSourceFactory: func() oauth2.TokenSource {
				// Token without ID token in Extra
				return oauth2.StaticTokenSource(&oauth2.Token{
					AccessToken: "test-access-token",
				})
			},
			expectError:   true,
			errorContains: "Token exchange failed",
		},
		{
			name:             "unsupported token type",
			subjectTokenType: "urn:ietf:params:oauth:token-type:saml2",
			tokenSourceFactory: func() oauth2.TokenSource {
				return oauth2.StaticTokenSource(&oauth2.Token{
					AccessToken: "test-access-token",
				})
			},
			expectError:   true,
			errorContains: "invalid SubjectTokenType",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up mock token exchange server
			var receivedSubjectToken string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				err := r.ParseForm()
				require.NoError(t, err)

				receivedSubjectToken = r.FormValue("subject_token")

				resp := map[string]interface{}{
					"access_token":      "exchanged-token",
					"issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
					"token_type":        "Bearer",
					"expires_in":        3600,
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()

			// Create config
			config := Config{
				TokenURL:         server.URL,
				ClientID:         "test-client-id",
				ClientSecret:     "test-client-secret",
				Audience:         "https://api.example.com",
				SubjectTokenType: tt.subjectTokenType,
			}

			// Create token source
			tokenSource := tt.tokenSourceFactory()

			// Create middleware
			middleware, err := CreateMiddlewareFromTokenSource(config, tokenSource)

			if tt.expectError && tt.errorContains == "invalid SubjectTokenType" {
				// Error expected at middleware creation time
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
				return
			}

			require.NoError(t, err)

			// Create test request with claims
			req := httptest.NewRequest("GET", "/test", nil)
			claims := jwt.MapClaims{
				"sub": "test-user",
				"aud": "test-audience",
			}
			ctx := context.WithValue(req.Context(), auth.ClaimsContextKey{}, claims)
			req = req.WithContext(ctx)

			// Execute middleware
			rec := httptest.NewRecorder()
			testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			handler := middleware(testHandler)
			handler.ServeHTTP(rec, req)

			if tt.expectError {
				// Middleware should return an error status
				assert.NotEqual(t, http.StatusOK, rec.Code)
				assert.Contains(t, rec.Body.String(), tt.errorContains)
			} else {
				// Middleware should succeed
				assert.Equal(t, http.StatusOK, rec.Code)
				// Verify the correct token was sent to exchange endpoint
				assert.Equal(t, tt.expectedToken, receivedSubjectToken)
			}
		})
	}
}

func TestCreateMiddlewareFromTokenSource_NilTokenSource(t *testing.T) {
	config := Config{
		TokenURL:     "https://sts.example.com/token",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
	}

	_, err := CreateMiddlewareFromTokenSource(config, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tokenSource cannot be nil")
}

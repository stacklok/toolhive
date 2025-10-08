package tokenexchange

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

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

			err := injectToken(req, tt.newToken, tt.config)

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

// TestCreateTokenExchangeMiddlewareFromClaims_Success tests successful token exchange flow.
func TestCreateTokenExchangeMiddlewareFromClaims_Success(t *testing.T) {
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

			middleware := CreateTokenExchangeMiddlewareFromClaims(config)

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

// TestCreateTokenExchangeMiddlewareFromClaims_PassThrough tests cases where middleware passes through.
func TestCreateTokenExchangeMiddlewareFromClaims_PassThrough(t *testing.T) {
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

			middleware := CreateTokenExchangeMiddlewareFromClaims(config)

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

// TestCreateTokenExchangeMiddlewareFromClaims_Failures tests error scenarios.
func TestCreateTokenExchangeMiddlewareFromClaims_Failures(t *testing.T) {
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

			middleware := CreateTokenExchangeMiddlewareFromClaims(config)

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
			name: "nil config skips middleware creation",
			params: MiddlewareParams{
				TokenExchangeConfig: nil,
			},
			expectError:         false,
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

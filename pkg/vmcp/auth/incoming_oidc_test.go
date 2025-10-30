package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/mocks"
)

// TestOIDCIncomingAuth_NilValidator tests that constructor returns error for nil validator
func TestOIDCIncomingAuth_NilValidator(t *testing.T) {
	t.Parallel()

	authenticator, err := NewOIDCIncomingAuthenticator(nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "token validator cannot be nil")
	assert.Nil(t, authenticator)
}

// TestNewOIDCIncomingAuthenticator tests the constructor with valid input
func TestNewOIDCIncomingAuthenticator(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockValidator := mocks.NewMockTokenAuthenticator(ctrl)

	authenticator, err := NewOIDCIncomingAuthenticator(mockValidator)

	require.NoError(t, err)
	assert.NotNil(t, authenticator)
	assert.Equal(t, mockValidator, authenticator.validator)
}

// TestOIDCIncomingAuth_Authenticate_RequestValidation tests Authenticate method
// with various request formats WITHOUT needing to mock TokenValidator
func TestOIDCIncomingAuth_Authenticate_RequestValidation(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockValidator := mocks.NewMockTokenAuthenticator(ctrl)
	authenticator := &OIDCIncomingAuthenticator{validator: mockValidator}

	tests := []struct {
		name        string
		setupReq    func() *http.Request
		wantErr     bool
		errContains string
	}{
		{
			name: "missing authorization header",
			setupReq: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "/", nil)
			},
			wantErr:     true,
			errContains: "authorization header required",
		},
		{
			name: "invalid authorization format - Basic auth",
			setupReq: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.Header.Set("Authorization", "Basic user:pass")
				return req
			},
			wantErr:     true,
			errContains: "invalid authorization header format",
		},
		{
			name: "empty token after Bearer",
			setupReq: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.Header.Set("Authorization", "Bearer ")
				return req
			},
			wantErr:     true,
			errContains: "empty token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := tt.setupReq()
			identity, err := authenticator.Authenticate(context.Background(), req)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				assert.Nil(t, identity)
			} else {
				require.NoError(t, err)
				require.NotNil(t, identity)
			}
		})
	}
}

// TestOIDCIncomingAuth_Middleware tests that the middleware properly integrates
// with TokenValidator and converts claims to Identity.
func TestOIDCIncomingAuth_Middleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		claims         jwt.MapClaims // Claims that TokenValidator would set
		authHeader     string        // Authorization header value
		expectIdentity *Identity     // Expected Identity in context
	}{
		{
			name: "successful authentication with all claims",
			claims: jwt.MapClaims{
				"sub":   "user123",
				"name":  "Alice Smith",
				"email": "alice@example.com",
				"custom": map[string]interface{}{
					"org": "acme",
				},
			},
			authHeader: "Bearer test-token-123",
			expectIdentity: &Identity{
				Subject:   "user123",
				Name:      "Alice Smith",
				Email:     "alice@example.com",
				Groups:    []string{}, // Empty by default
				Token:     "test-token-123",
				TokenType: "Bearer",
				Claims: map[string]any{
					"sub":   "user123",
					"name":  "Alice Smith",
					"email": "alice@example.com",
					"custom": map[string]interface{}{
						"org": "acme",
					},
				},
			},
		},
		{
			name: "minimal claims - only subject",
			claims: jwt.MapClaims{
				"sub": "user456",
			},
			authHeader: "Bearer minimal-token",
			expectIdentity: &Identity{
				Subject:   "user456",
				Name:      "",
				Email:     "",
				Groups:    []string{},
				Token:     "minimal-token",
				TokenType: "Bearer",
				Claims: map[string]any{
					"sub": "user456",
				},
			},
		},
		{
			name: "claims with groups not extracted to Groups field",
			claims: jwt.MapClaims{
				"sub":    "user789",
				"groups": []string{"admin", "users"},
				"roles":  []string{"manager"},
			},
			authHeader: "Bearer groups-token",
			expectIdentity: &Identity{
				Subject:   "user789",
				Groups:    []string{}, // Groups field stays empty
				Token:     "groups-token",
				TokenType: "Bearer",
				Claims: map[string]any{
					"sub":    "user789",
					"groups": []string{"admin", "users"},
					"roles":  []string{"manager"},
				},
			},
		},
		{
			name: "token without Bearer prefix - token not stored",
			claims: jwt.MapClaims{
				"sub": "user999",
			},
			authHeader: "NoBearer token-value",
			expectIdentity: &Identity{
				Subject:   "user999",
				Token:     "", // Not extracted due to wrong prefix
				TokenType: "",
				Groups:    []string{},
				Claims: map[string]any{
					"sub": "user999",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockValidator := mocks.NewMockTokenAuthenticator(ctrl)

			// Mock the Middleware method to simulate what TokenValidator.Middleware does:
			// it sets claims in the context using auth.ClaimsContextKey
			mockValidator.EXPECT().
				Middleware(gomock.Any()).
				DoAndReturn(func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						// Simulate TokenValidator.Middleware: set claims in context
						ctx := context.WithValue(r.Context(), auth.ClaimsContextKey{}, tt.claims)
						next.ServeHTTP(w, r.WithContext(ctx))
					})
				})

			authenticator := &OIDCIncomingAuthenticator{validator: mockValidator}

			// Create a test handler that verifies the Identity is in context
			var capturedIdentity *Identity
			testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				identity, ok := IdentityFromContext(r.Context())
				if ok {
					capturedIdentity = identity
				}
				w.WriteHeader(http.StatusOK)
			})

			// Get the middleware from authenticator
			middleware := authenticator.Middleware()

			// Wrap the test handler
			wrappedHandler := middleware(testHandler)

			// Create the request with Authorization header
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req.Header.Set("Authorization", tt.authHeader)
			recorder := httptest.NewRecorder()

			// Execute the request
			wrappedHandler.ServeHTTP(recorder, req)

			// Verify the request succeeded
			require.Equal(t, http.StatusOK, recorder.Code, "Expected successful response")

			// Verify Identity was captured
			require.NotNil(t, capturedIdentity, "Identity should be in context")

			// Verify all Identity fields
			assert.Equal(t, tt.expectIdentity.Subject, capturedIdentity.Subject)
			assert.Equal(t, tt.expectIdentity.Name, capturedIdentity.Name)
			assert.Equal(t, tt.expectIdentity.Email, capturedIdentity.Email)
			assert.Equal(t, tt.expectIdentity.Groups, capturedIdentity.Groups)
			assert.Equal(t, tt.expectIdentity.Token, capturedIdentity.Token)
			assert.Equal(t, tt.expectIdentity.TokenType, capturedIdentity.TokenType)

			// Verify Claims are preserved
			require.NotNil(t, capturedIdentity.Claims)
			assert.Equal(t, len(tt.expectIdentity.Claims), len(capturedIdentity.Claims))
			for k, v := range tt.expectIdentity.Claims {
				assert.Equal(t, v, capturedIdentity.Claims[k], "Claim %s should match", k)
			}
		})
	}
}

// TestOIDCIncomingAuth_Middleware_MissingSubClaim tests that the middleware
// returns an error when the sub claim is missing
func TestOIDCIncomingAuth_Middleware_MissingSubClaim(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockValidator := mocks.NewMockTokenAuthenticator(ctrl)

	// Claims without sub
	invalidClaims := jwt.MapClaims{
		"name":  "Alice",
		"email": "alice@example.com",
	}

	// Mock the Middleware method
	mockValidator.EXPECT().
		Middleware(gomock.Any()).
		DoAndReturn(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Simulate TokenValidator.Middleware: set invalid claims in context
				ctx := context.WithValue(r.Context(), auth.ClaimsContextKey{}, invalidClaims)
				next.ServeHTTP(w, r.WithContext(ctx))
			})
		})

	authenticator := &OIDCIncomingAuthenticator{validator: mockValidator}

	// Create a test handler that should not be reached
	testHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler should not be called when sub claim is missing")
	})

	// Get the middleware from authenticator
	middleware := authenticator.Middleware()

	// Wrap the test handler
	wrappedHandler := middleware(testHandler)

	// Create the request
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	recorder := httptest.NewRecorder()

	// Execute the request
	wrappedHandler.ServeHTTP(recorder, req)

	// Verify the request failed with Unauthorized
	require.Equal(t, http.StatusUnauthorized, recorder.Code, "Expected Unauthorized response")
	assert.Contains(t, recorder.Body.String(), "missing or invalid 'sub' claim")
}

// TestClaimsToIdentity tests the claim conversion logic
func TestClaimsToIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		claims        jwt.MapClaims
		expectError   bool
		errorContains string
		validate      func(*testing.T, *Identity, error)
	}{
		{
			name: "all standard fields present",
			claims: jwt.MapClaims{
				"sub":   "user123",
				"name":  "Alice Smith",
				"email": "alice@example.com",
			},
			expectError: false,
			validate: func(t *testing.T, identity *Identity, err error) {
				t.Helper()
				require.NoError(t, err)
				require.NotNil(t, identity)
				assert.Equal(t, "user123", identity.Subject)
				assert.Equal(t, "Alice Smith", identity.Name)
				assert.Equal(t, "alice@example.com", identity.Email)
				assert.Empty(t, identity.Groups) // Groups not extracted
				assert.Len(t, identity.Claims, 3)
			},
		},
		{
			name: "groups in claims not extracted to Groups field",
			claims: jwt.MapClaims{
				"sub":    "user123",
				"groups": []string{"admin", "users"},
			},
			expectError: false,
			validate: func(t *testing.T, identity *Identity, err error) {
				t.Helper()
				require.NoError(t, err)
				require.NotNil(t, identity)
				assert.Equal(t, "user123", identity.Subject)
				assert.Empty(t, identity.Groups)              // Groups field stays empty
				assert.Contains(t, identity.Claims, "groups") // But available in Claims
			},
		},
		{
			name: "custom claims preserved",
			claims: jwt.MapClaims{
				"sub":        "user123",
				"custom1":    "value1",
				"custom2":    123,
				"custom3":    true,
				"custom_obj": map[string]any{"nested": "value"},
			},
			expectError: false,
			validate: func(t *testing.T, identity *Identity, err error) {
				t.Helper()
				require.NoError(t, err)
				require.NotNil(t, identity)
				assert.Equal(t, "user123", identity.Subject)
				assert.Equal(t, "value1", identity.Claims["custom1"])
				assert.Equal(t, 123, identity.Claims["custom2"])
				assert.Equal(t, true, identity.Claims["custom3"])
				assert.Contains(t, identity.Claims, "custom_obj")
			},
		},
		{
			name: "minimal claims - only sub",
			claims: jwt.MapClaims{
				"sub": "user123",
			},
			expectError: false,
			validate: func(t *testing.T, identity *Identity, err error) {
				t.Helper()
				require.NoError(t, err)
				require.NotNil(t, identity)
				assert.Equal(t, "user123", identity.Subject)
				assert.Empty(t, identity.Name)
				assert.Empty(t, identity.Email)
				assert.Empty(t, identity.Groups)
				assert.Len(t, identity.Claims, 1)
			},
		},
		{
			name: "missing sub claim returns error",
			claims: jwt.MapClaims{
				"name":  "Alice",
				"email": "alice@example.com",
			},
			expectError:   true,
			errorContains: "missing or invalid 'sub' claim",
			validate: func(t *testing.T, identity *Identity, err error) {
				t.Helper()
				require.Error(t, err)
				assert.Contains(t, err.Error(), "missing or invalid 'sub' claim")
				assert.Nil(t, identity)
			},
		},
		{
			name: "empty sub claim returns error",
			claims: jwt.MapClaims{
				"sub":  "",
				"name": "Alice",
			},
			expectError:   true,
			errorContains: "missing or invalid 'sub' claim",
			validate: func(t *testing.T, identity *Identity, err error) {
				t.Helper()
				require.Error(t, err)
				assert.Contains(t, err.Error(), "missing or invalid 'sub' claim")
				assert.Nil(t, identity)
			},
		},
		{
			name: "non-string sub claim returns error",
			claims: jwt.MapClaims{
				"sub":  12345,
				"name": "Alice",
			},
			expectError:   true,
			errorContains: "missing or invalid 'sub' claim",
			validate: func(t *testing.T, identity *Identity, err error) {
				t.Helper()
				require.Error(t, err)
				assert.Contains(t, err.Error(), "missing or invalid 'sub' claim")
				assert.Nil(t, identity)
			},
		},
		{
			name: "non-string field types ignored for standard fields except sub",
			claims: jwt.MapClaims{
				"sub":   "user123",  // Valid sub
				"name":  true,       // Wrong type for Name
				"email": []string{}, // Wrong type for Email
			},
			expectError: false,
			validate: func(t *testing.T, identity *Identity, err error) {
				t.Helper()
				require.NoError(t, err)
				require.NotNil(t, identity)
				assert.Equal(t, "user123", identity.Subject) // Sub is extracted
				assert.Empty(t, identity.Name)               // Not extracted due to wrong type
				assert.Empty(t, identity.Email)
				assert.Len(t, identity.Claims, 3) // But still preserved in Claims
			},
		},
		{
			name:          "empty claims returns error",
			claims:        jwt.MapClaims{},
			expectError:   true,
			errorContains: "missing or invalid 'sub' claim",
			validate: func(t *testing.T, identity *Identity, err error) {
				t.Helper()
				require.Error(t, err)
				assert.Contains(t, err.Error(), "missing or invalid 'sub' claim")
				assert.Nil(t, identity)
			},
		},
		{
			name: "mixed claim types including arrays and objects",
			claims: jwt.MapClaims{
				"sub":   "user123",
				"roles": []interface{}{"admin", "user"},
				"metadata": map[string]interface{}{
					"tenant": "acme",
					"region": "us-west",
				},
				"exp": 1234567890,
				"iat": 1234567800,
			},
			expectError: false,
			validate: func(t *testing.T, identity *Identity, err error) {
				t.Helper()
				require.NoError(t, err)
				require.NotNil(t, identity)
				assert.Equal(t, "user123", identity.Subject)
				assert.Empty(t, identity.Groups) // Not extracted
				assert.Contains(t, identity.Claims, "roles")
				assert.Contains(t, identity.Claims, "metadata")
				assert.Contains(t, identity.Claims, "exp")
				assert.Contains(t, identity.Claims, "iat")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			identity, err := claimsToIdentity(tt.claims)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
				assert.Nil(t, identity)
			} else {
				require.NoError(t, err)
				require.NotNil(t, identity)
				require.NotNil(t, identity.Claims)
				require.NotNil(t, identity.Groups)
			}

			tt.validate(t, identity, err)
		})
	}
}

// TestClaimsToIdentity_Immutability tests that original claims are not affected
func TestClaimsToIdentity_Immutability(t *testing.T) {
	t.Parallel()

	originalClaims := jwt.MapClaims{
		"sub":   "user123",
		"name":  "Alice",
		"extra": "value",
	}

	// Make a copy to compare later
	claimsCopy := jwt.MapClaims{}
	for k, v := range originalClaims {
		claimsCopy[k] = v
	}

	identity, err := claimsToIdentity(originalClaims)

	// Verify identity was created correctly
	require.NoError(t, err)
	require.NotNil(t, identity)
	assert.Equal(t, "user123", identity.Subject)
	assert.Equal(t, "Alice", identity.Name)

	// Verify original claims weren't modified
	assert.Equal(t, claimsCopy, originalClaims)

	// Verify modifying identity claims doesn't affect original
	identity.Claims["new_key"] = "new_value"
	assert.NotContains(t, originalClaims, "new_key")
}

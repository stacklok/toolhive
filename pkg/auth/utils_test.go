package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/logger"
)

func TestExtractBearerToken(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		authHeader    string
		expectedToken string
		expectedError error
	}{
		{
			name:          "valid_bearer_token",
			authHeader:    "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
			expectedToken: "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
			expectedError: nil,
		},
		{
			name:          "missing_authorization_header",
			authHeader:    "",
			expectedToken: "",
			expectedError: ErrAuthHeaderMissing,
		},
		{
			name:          "invalid_format_no_bearer_prefix",
			authHeader:    "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
			expectedToken: "",
			expectedError: ErrInvalidAuthHeaderFormat,
		},
		{
			name:          "lowercase_bearer",
			authHeader:    "bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
			expectedToken: "",
			expectedError: ErrInvalidAuthHeaderFormat,
		},
		{
			name:          "empty_token_after_prefix",
			authHeader:    "Bearer ",
			expectedToken: "",
			expectedError: ErrEmptyBearerToken,
		},
		{
			name:          "empty_token_with_trailing_spaces",
			authHeader:    "Bearer    ",
			expectedToken: "",
			expectedError: ErrEmptyBearerToken,
		},
		{
			name:          "token_with_spaces_valid_per_rfc",
			authHeader:    "Bearer token with spaces",
			expectedToken: "token with spaces",
			expectedError: nil,
		},
		{
			name:          "basic_auth_instead_of_bearer",
			authHeader:    "Basic dXNlcjpwYXNz",
			expectedToken: "",
			expectedError: ErrInvalidAuthHeaderFormat,
		},
		{
			name:          "token_with_special_characters",
			authHeader:    "Bearer eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0In0.abc-def_ghi",
			expectedToken: "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0In0.abc-def_ghi",
			expectedError: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Create a test request with the authorization header
			req := httptest.NewRequest("GET", "/test", nil)
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}

			// Extract the bearer token
			token, err := ExtractBearerToken(req)

			// Check the error
			if tc.expectedError != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tc.expectedError), "Expected error %v, got %v", tc.expectedError, err)
				assert.Empty(t, token)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.expectedToken, token)
			}
		})
	}
}

func TestGetClaimsFromContext(t *testing.T) {
	t.Parallel()
	// Test with claims in context
	claims := jwt.MapClaims{
		"sub": "testuser",
		"iss": "test-issuer",
		"aud": "test-audience",
	}
	identity := &Identity{Subject: "testuser", Claims: claims}
	ctx := WithIdentity(context.Background(), identity)

	retrievedClaims, ok := GetClaimsFromContext(ctx)
	require.True(t, ok, "Expected to retrieve claims from context")
	assert.Equal(t, "testuser", retrievedClaims["sub"])
	assert.Equal(t, "test-issuer", retrievedClaims["iss"])

	// Test with no claims in context
	emptyCtx := context.Background()
	_, ok = GetClaimsFromContext(emptyCtx)
	assert.False(t, ok, "Expected no claims to be found in empty context")

	// Test with wrong type in context
	wrongCtx := context.WithValue(context.Background(), ClaimsContextKey{}, "not-claims")
	_, ok = GetClaimsFromContext(wrongCtx)
	assert.False(t, ok, "Expected no claims to be found when wrong type is in context")

	// Test with nil context - we intentionally pass nil to test the nil check
	//nolint:staticcheck // SA1012: Testing nil context handling is intentional
	_, ok = GetClaimsFromContext(nil)
	assert.False(t, ok, "Expected no claims to be found with nil context")
}

func TestGetClaimsFromContextWithDifferentClaimTypes(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name     string
		claims   jwt.MapClaims
		expected map[string]interface{}
	}{
		{
			name: "string_claims",
			claims: jwt.MapClaims{
				"sub":   "user123",
				"email": "user@example.com",
				"name":  "Test User",
			},
			expected: map[string]interface{}{
				"sub":   "user123",
				"email": "user@example.com",
				"name":  "Test User",
			},
		},
		{
			name: "mixed_claims",
			claims: jwt.MapClaims{
				"sub":   "user123",
				"exp":   int64(1234567890),
				"iat":   int64(1234567800),
				"admin": true,
			},
			expected: map[string]interface{}{
				"sub":   "user123",
				"exp":   int64(1234567890),
				"iat":   int64(1234567800),
				"admin": true,
			},
		},
		{
			name:     "empty_claims",
			claims:   jwt.MapClaims{},
			expected: map[string]interface{}{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			identity := &Identity{Subject: "test-user", Claims: tc.claims}
			ctx := WithIdentity(context.Background(), identity)
			retrievedClaims, ok := GetClaimsFromContext(ctx)

			require.True(t, ok, "Expected to retrieve claims from context")

			for key, expectedValue := range tc.expected {
				assert.Equal(t, expectedValue, retrievedClaims[key], "Expected %s to be %v, got %v", key, expectedValue, retrievedClaims[key])
			}
		})
	}
}

func TestGetAuthenticationMiddleware(t *testing.T) {
	t.Parallel()
	// Initialize logger for testing
	logger.Initialize()

	ctx := context.Background()

	// Test with nil OIDC config (should return local user middleware)
	middleware, _, err := GetAuthenticationMiddleware(ctx, nil)
	require.NoError(t, err, "Expected no error when OIDC config is nil")
	require.NotNil(t, middleware, "Expected middleware to be returned")

	// Test that the middleware works by creating a test handler
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := GetClaimsFromContext(r.Context())
		require.True(t, ok, "Expected claims to be present in context")
		assert.Equal(t, "toolhive-local", claims["iss"])
		w.WriteHeader(http.StatusOK)
	})

	// Wrap the test handler with the middleware
	wrappedHandler := middleware(testHandler)

	// Create a test request
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	// Execute the request
	wrappedHandler.ServeHTTP(w, req)

	// Check the response
	assert.Equal(t, http.StatusOK, w.Code)
}

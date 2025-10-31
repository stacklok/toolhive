package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
)

func TestClaimsToIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		claims    jwt.MapClaims
		token     string
		wantErr   bool
		errMsg    string
		checkFunc func(t *testing.T, identity *Identity)
	}{
		{
			name: "valid_oidc_claims",
			claims: jwt.MapClaims{
				"sub":   "user123",
				"name":  "John Doe",
				"email": "john@example.com",
			},
			token:   "test-token",
			wantErr: false,
			checkFunc: func(t *testing.T, identity *Identity) {
				assert.Equal(t, "user123", identity.Subject)
				assert.Equal(t, "John Doe", identity.Name)
				assert.Equal(t, "john@example.com", identity.Email)
				assert.Equal(t, "test-token", identity.Token)
				assert.Equal(t, "Bearer", identity.TokenType)
				assert.Empty(t, identity.Groups, "Groups should not be populated")
			},
		},
		{
			name: "minimal_claims_only_sub",
			claims: jwt.MapClaims{
				"sub": "user123",
			},
			token:   "test-token",
			wantErr: false,
			checkFunc: func(t *testing.T, identity *Identity) {
				assert.Equal(t, "user123", identity.Subject)
				assert.Empty(t, identity.Name)
				assert.Empty(t, identity.Email)
			},
		},
		{
			name: "missing_sub_claim",
			claims: jwt.MapClaims{
				"name":  "John Doe",
				"email": "john@example.com",
			},
			token:   "test-token",
			wantErr: true,
			errMsg:  "missing or invalid 'sub' claim",
		},
		{
			name: "empty_sub_claim",
			claims: jwt.MapClaims{
				"sub": "",
			},
			token:   "test-token",
			wantErr: true,
			errMsg:  "missing or invalid 'sub' claim",
		},
		{
			name: "non_string_sub_claim",
			claims: jwt.MapClaims{
				"sub": 12345,
			},
			token:   "test-token",
			wantErr: true,
			errMsg:  "missing or invalid 'sub' claim",
		},
		{
			name: "groups_in_claims_not_extracted",
			claims: jwt.MapClaims{
				"sub":    "user123",
				"groups": []string{"admin", "users"},
			},
			token:   "test-token",
			wantErr: false,
			checkFunc: func(t *testing.T, identity *Identity) {
				assert.Equal(t, "user123", identity.Subject)
				assert.Empty(t, identity.Groups, "Groups should not be extracted to Identity.Groups")
				assert.Contains(t, identity.Claims, "groups", "Groups should remain in Claims map")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			identity, err := claimsToIdentity(tt.claims, tt.token)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
				assert.Nil(t, identity)
			} else {
				require.NoError(t, err)
				require.NotNil(t, identity)
				if tt.checkFunc != nil {
					tt.checkFunc(t, identity)
				}
			}
		})
	}
}

func TestIdentityMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		setupRequest   func(*http.Request) *http.Request
		expectedStatus int
		expectIdentity bool
		checkIdentity  func(t *testing.T, identity *Identity)
	}{
		{
			name: "converts_claims_to_identity",
			setupRequest: func(r *http.Request) *http.Request {
				// Simulate TokenValidator setting claims in context
				claims := jwt.MapClaims{
					"sub":   "user123",
					"name":  "Alice",
					"email": "alice@example.com",
				}
				ctx := context.WithValue(r.Context(), auth.ClaimsContextKey{}, claims)
				r = r.WithContext(ctx)
				r.Header.Set("Authorization", "Bearer test-token-value")
				return r
			},
			expectedStatus: http.StatusOK,
			expectIdentity: true,
			checkIdentity: func(t *testing.T, identity *Identity) {
				assert.Equal(t, "user123", identity.Subject)
				assert.Equal(t, "Alice", identity.Name)
				assert.Equal(t, "alice@example.com", identity.Email)
				assert.Equal(t, "test-token-value", identity.Token)
			},
		},
		{
			name: "no_claims_passes_through",
			setupRequest: func(r *http.Request) *http.Request {
				// No claims in context - simulates unauthenticated request
				return r
			},
			expectedStatus: http.StatusOK,
			expectIdentity: false,
		},
		{
			name: "invalid_sub_returns_401",
			setupRequest: func(r *http.Request) *http.Request {
				// Claims without 'sub'
				claims := jwt.MapClaims{
					"name": "Alice",
				}
				ctx := context.WithValue(r.Context(), auth.ClaimsContextKey{}, claims)
				return r.WithContext(ctx)
			},
			expectedStatus: http.StatusUnauthorized,
			expectIdentity: false,
		},
		{
			name: "missing_token_header_still_creates_identity",
			setupRequest: func(r *http.Request) *http.Request {
				claims := jwt.MapClaims{
					"sub": "user123",
				}
				ctx := context.WithValue(r.Context(), auth.ClaimsContextKey{}, claims)
				// No Authorization header
				return r.WithContext(ctx)
			},
			expectedStatus: http.StatusOK,
			expectIdentity: true,
			checkIdentity: func(t *testing.T, identity *Identity) {
				assert.Equal(t, "user123", identity.Subject)
				assert.Empty(t, identity.Token, "Token should be empty when header is missing")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create test handler that checks for Identity in context
			var capturedIdentity *Identity
			testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				identity, ok := IdentityFromContext(r.Context())
				if ok {
					capturedIdentity = identity
				}
				w.WriteHeader(http.StatusOK)
			})

			// Wrap with IdentityMiddleware
			handler := IdentityMiddleware(testHandler)

			// Create request
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req = tt.setupRequest(req)

			// Execute request
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)

			// Assertions
			assert.Equal(t, tt.expectedStatus, recorder.Code)

			if tt.expectIdentity {
				require.NotNil(t, capturedIdentity, "Expected identity to be set in context")
				if tt.checkIdentity != nil {
					tt.checkIdentity(t, capturedIdentity)
				}
			} else {
				assert.Nil(t, capturedIdentity, "Expected no identity in context")
			}
		})
	}
}

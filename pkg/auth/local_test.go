package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalUserMiddleware(t *testing.T) {
	t.Parallel()
	username := "testuser"

	// Create a test handler that checks for identity in the context
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := IdentityFromContext(r.Context())
		require.True(t, ok, "Expected identity to be present in context")
		require.NotNil(t, identity, "Expected identity to be non-nil")

		// Verify the identity fields
		assert.Equal(t, username, identity.Subject)
		assert.Equal(t, "Local User: "+username, identity.Name)
		assert.Equal(t, username+"@localhost", identity.Email)

		// Verify the local user claims
		require.NotNil(t, identity.Claims)
		assert.Equal(t, username, identity.Claims["sub"])
		assert.Equal(t, "toolhive-local", identity.Claims["iss"])
		assert.Equal(t, "toolhive", identity.Claims["aud"])
		assert.Equal(t, username+"@localhost", identity.Claims["email"])
		assert.Equal(t, "Local User: "+username, identity.Claims["name"])

		// Verify timestamps are reasonable
		now := time.Now().Unix()
		exp, ok := identity.Claims["exp"].(int64)
		require.True(t, ok, "Expected exp to be present and be an int64")
		assert.Greater(t, exp, now, "Expected exp to be in the future")

		iat, ok := identity.Claims["iat"].(int64)
		require.True(t, ok, "Expected iat to be present and be an int64")
		assert.LessOrEqual(t, iat, now+1, "Expected iat to be current time or earlier (with 1 second tolerance)")

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// Wrap the test handler with the local user middleware
	middleware := LocalUserMiddleware(username)(testHandler)

	// Create a test request
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	// Execute the request
	middleware.ServeHTTP(w, req)

	// Check the response
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "OK", w.Body.String())
}

func TestLocalUserMiddlewareWithDifferentUsernames(t *testing.T) {
	t.Parallel()
	testCases := []string{"alice", "bob", "admin", "user123"}

	for _, username := range testCases {
		t.Run("username_"+username, func(t *testing.T) {
			t.Parallel()
			testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				identity, ok := IdentityFromContext(r.Context())
				require.True(t, ok, "Expected identity to be present in context")
				require.NotNil(t, identity, "Expected identity to be non-nil")

				assert.Equal(t, username, identity.Subject)
				assert.Equal(t, username+"@localhost", identity.Email)

				w.WriteHeader(http.StatusOK)
			})

			middleware := LocalUserMiddleware(username)(testHandler)
			req := httptest.NewRequest("GET", "/test", nil)
			w := httptest.NewRecorder()

			middleware.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)
		})
	}
}

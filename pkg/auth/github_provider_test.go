package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitHubProvider_Name(t *testing.T) {
	t.Parallel()
	provider, err := NewGitHubProvider("https://api.github.com/applications/test/token", "test", "test", "", false)
	require.NoError(t, err)
	assert.Equal(t, "github", provider.Name())
}

func TestGitHubProvider_CanHandle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		introspectURL  string
		expectedResult bool
	}{
		{
			name:           "Valid GitHub.com API URL",
			introspectURL:  "https://api.github.com/applications/Ov23li1234567890/token",
			expectedResult: true,
		},
		{
			name:           "Non-GitHub URL",
			introspectURL:  "https://oauth2.googleapis.com/tokeninfo",
			expectedResult: false,
		},
		{
			name:           "RFC 7662 endpoint",
			introspectURL:  "https://auth.example.com/oauth/introspect",
			expectedResult: false,
		},
		{
			name:           "HTTP (not HTTPS)",
			introspectURL:  "http://api.github.com/applications/test/token",
			expectedResult: false,
		},
		{
			name:           "Malicious URL with github in path",
			introspectURL:  "https://evil.com/api.github.com/applications/fake/token",
			expectedResult: false,
		},
		{
			name:           "Wrong host (GitHub Enterprise)",
			introspectURL:  "https://github.company.com/api/applications/test/token",
			expectedResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider, err := NewGitHubProvider("https://api.github.com/applications/test/token", "test", "test", "", false)
			require.NoError(t, err)
			result := provider.CanHandle(tt.introspectURL)
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestGitHubProvider_IntrospectToken_Success(t *testing.T) {
	t.Parallel()

	// Create a mock GitHub API server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request method and headers
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "application/json", r.Header.Get("Accept"))

		// Verify Basic Auth
		username, password, ok := r.BasicAuth()
		assert.True(t, ok)
		assert.Equal(t, "test-client-id", username)
		assert.Equal(t, "test-client-secret", password)

		// Verify request body
		var reqBody map[string]string
		err := json.NewDecoder(r.Body).Decode(&reqBody)
		require.NoError(t, err)
		assert.Equal(t, "gho_test_token", reqBody["access_token"])

		// Return mock GitHub response
		response := map[string]interface{}{
			"id":    123456,
			"token": "gho_test_token",
			"user": map[string]interface{}{
				"login":      "octocat",
				"id":         1,
				"node_id":    "MDQ6VXNlcjE=",
				"email":      "octocat@github.com",
				"name":       "The Octocat",
				"type":       "User",
				"site_admin": false,
			},
			"scopes":     []string{"repo", "user"},
			"created_at": "2011-09-06T20:39:23Z",
			"updated_at": "2011-09-06T20:39:23Z",
			"app": map[string]interface{}{
				"name":      "My OAuth App",
				"url":       "https://github.com/apps/my-oauth-app",
				"client_id": "Ov23li1234567890",
			},
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		err = json.NewEncoder(w).Encode(response)
		require.NoError(t, err)
	}))
	defer mockServer.Close()

	// Create provider with mock server URL and custom HTTP client for testing
	provider, err := newGitHubProviderWithClient(mockServer.URL, "test-client-id", "test-client-secret", "", false, http.DefaultClient)
	require.NoError(t, err)

	// Test introspection
	claims, err := provider.IntrospectToken(context.Background(), "gho_test_token")
	require.NoError(t, err)
	require.NotNil(t, claims)

	// Verify standard claims
	assert.Equal(t, "https://github.com", claims["iss"])
	assert.Equal(t, "https://github.com", claims["aud"])
	assert.Equal(t, "1", claims["sub"])
	assert.Equal(t, "octocat@github.com", claims["email"])
	assert.Equal(t, "octocat", claims["preferred_username"])
	assert.Equal(t, "octocat", claims["login"])
	assert.Equal(t, "The Octocat", claims["name"])
	assert.Equal(t, true, claims["active"])

	// Verify scopes
	scopes, ok := claims["scopes"].([]string)
	require.True(t, ok)
	assert.Equal(t, []string{"repo", "user"}, scopes)
	assert.Equal(t, "repo user", claims["scope"])

	// Verify GitHub-specific claims
	assert.Equal(t, "User", claims["user_type"])
	assert.Equal(t, "My OAuth App", claims["app_name"])

	// Verify iat (issued at) is present
	_, hasIat := claims["iat"]
	assert.True(t, hasIat)
}

func TestGitHubProvider_IntrospectToken_InvalidToken(t *testing.T) {
	t.Parallel()

	// Create a mock GitHub API server that returns 404
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		response := map[string]interface{}{
			"message":           "Not Found",
			"documentation_url": "https://docs.github.com/rest/apps/oauth-applications#check-a-token",
		}
		err := json.NewEncoder(w).Encode(response)
		require.NoError(t, err)
	}))
	defer mockServer.Close()

	provider, err := newGitHubProviderWithClient(mockServer.URL, "test-client-id", "test-client-secret", "", false, http.DefaultClient)
	require.NoError(t, err)

	// Test with invalid token
	claims, err := provider.IntrospectToken(context.Background(), "invalid_token")
	assert.ErrorIs(t, err, ErrInvalidToken)
	assert.Nil(t, claims)
}

func TestGitHubProvider_IntrospectToken_ServerError(t *testing.T) {
	t.Parallel()

	// Create a mock server that returns 500
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, err := w.Write([]byte("Internal Server Error"))
		require.NoError(t, err)
	}))
	defer mockServer.Close()

	provider, err := newGitHubProviderWithClient(mockServer.URL, "test-client-id", "test-client-secret", "", false, http.DefaultClient)
	require.NoError(t, err)

	// Test with server error
	claims, err := provider.IntrospectToken(context.Background(), "gho_test_token")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "github validation failed with status 500")
	assert.Nil(t, claims)
}

func TestGitHubProvider_IntrospectToken_MalformedResponse(t *testing.T) {
	t.Parallel()

	// Create a mock server that returns invalid JSON
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte("not valid json"))
		require.NoError(t, err)
	}))
	defer mockServer.Close()

	provider, err := newGitHubProviderWithClient(mockServer.URL, "test-client-id", "test-client-secret", "", false, http.DefaultClient)
	require.NoError(t, err)

	// Test with malformed response
	claims, err := provider.IntrospectToken(context.Background(), "gho_test_token")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decode GitHub response")
	assert.Nil(t, claims)
}

func TestGitHubProvider_IntrospectToken_MissingUserID(t *testing.T) {
	t.Parallel()

	// Create a mock server that returns response without user ID
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		response := map[string]interface{}{
			"id":    123456,
			"token": "gho_test_token",
			"user": map[string]interface{}{
				"login": "octocat",
				// Missing "id" field
			},
			"scopes":     []string{"repo"},
			"created_at": "2011-09-06T20:39:23Z",
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		err := json.NewEncoder(w).Encode(response)
		require.NoError(t, err)
	}))
	defer mockServer.Close()

	provider, err := newGitHubProviderWithClient(mockServer.URL, "test-client-id", "test-client-secret", "", false, http.DefaultClient)
	require.NoError(t, err)

	// Test with missing user ID
	claims, err := provider.IntrospectToken(context.Background(), "gho_test_token")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing user ID")
	assert.Nil(t, claims)
}

func TestGitHubProvider_IntrospectToken_MinimalResponse(t *testing.T) {
	t.Parallel()

	// Create a mock server with minimal valid response
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		response := map[string]interface{}{
			"id":    123456,
			"token": "gho_test_token",
			"user": map[string]interface{}{
				"login": "octocat",
				"id":    1,
			},
			"scopes":     []string{},
			"created_at": "2011-09-06T20:39:23Z",
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		err := json.NewEncoder(w).Encode(response)
		require.NoError(t, err)
	}))
	defer mockServer.Close()

	provider, err := newGitHubProviderWithClient(mockServer.URL, "test-client-id", "test-client-secret", "", false, http.DefaultClient)
	require.NoError(t, err)

	// Test with minimal response
	claims, err := provider.IntrospectToken(context.Background(), "gho_test_token")
	require.NoError(t, err)
	require.NotNil(t, claims)

	// Verify required claims are present
	assert.Equal(t, "https://github.com", claims["iss"])
	assert.Equal(t, "1", claims["sub"])
	assert.Equal(t, "octocat", claims["login"])
	assert.Equal(t, true, claims["active"])

	// Optional claims should be absent or empty
	_, hasEmail := claims["email"]
	assert.False(t, hasEmail)
}

func TestGitHubProvider_IntrospectToken_SiteAdmin(t *testing.T) {
	t.Parallel()

	// Create a mock server for site admin user
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		response := map[string]interface{}{
			"id":    123456,
			"token": "gho_test_token",
			"user": map[string]interface{}{
				"login":      "admin",
				"id":         999,
				"site_admin": true,
			},
			"scopes":     []string{"admin:org"},
			"created_at": "2011-09-06T20:39:23Z",
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		err := json.NewEncoder(w).Encode(response)
		require.NoError(t, err)
	}))
	defer mockServer.Close()

	provider, err := newGitHubProviderWithClient(mockServer.URL, "test-client-id", "test-client-secret", "", false, http.DefaultClient)
	require.NoError(t, err)

	// Test with site admin
	claims, err := provider.IntrospectToken(context.Background(), "gho_test_token")
	require.NoError(t, err)
	require.NotNil(t, claims)

	// Verify site_admin claim
	assert.Equal(t, true, claims["site_admin"])
}

func TestGitHubProvider_IntrospectToken_RateLimited(t *testing.T) {
	t.Parallel()

	// Create a mock server that returns 429 (rate limited)
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1234567890")
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		response := map[string]interface{}{
			"message":           "API rate limit exceeded",
			"documentation_url": "https://docs.github.com/rest/overview/resources-in-the-rest-api#rate-limiting",
		}
		err := json.NewEncoder(w).Encode(response)
		require.NoError(t, err)
	}))
	defer mockServer.Close()

	provider, err := newGitHubProviderWithClient(mockServer.URL, "test-client-id", "test-client-secret", "", false, http.DefaultClient)
	require.NoError(t, err)

	// Test with rate limited response
	claims, err := provider.IntrospectToken(context.Background(), "gho_test_token")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "github rate limit exceeded")
	assert.Contains(t, err.Error(), "retry after: 60")
	assert.Nil(t, claims)
}

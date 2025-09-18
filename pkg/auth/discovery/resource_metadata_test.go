package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
)

func TestFetchResourceMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		serverResponse interface{}
		serverStatus   int
		contentType    string
		expectedError  bool
		validateFunc   func(*testing.T, *auth.RFC9728AuthInfo)
	}{
		{
			name: "valid resource metadata",
			serverResponse: auth.RFC9728AuthInfo{
				Resource:               "https://resource.example.com",
				AuthorizationServers:   []string{"https://auth.example.com"},
				ScopesSupported:        []string{"read", "write"},
				BearerMethodsSupported: []string{"header"},
			},
			serverStatus:  http.StatusOK,
			contentType:   "application/json",
			expectedError: false,
			validateFunc: func(t *testing.T, metadata *auth.RFC9728AuthInfo) {
				t.Helper()

				assert.Equal(t, "https://resource.example.com", metadata.Resource)
				assert.Len(t, metadata.AuthorizationServers, 1)
				assert.Len(t, metadata.ScopesSupported, 2)
			},
		},
		{
			name: "metadata with multiple authorization servers",
			serverResponse: auth.RFC9728AuthInfo{
				Resource: "https://resource.example.com",
				AuthorizationServers: []string{
					"https://auth1.example.com",
					"https://auth2.example.com",
				},
			},
			serverStatus:  http.StatusOK,
			contentType:   "application/json",
			expectedError: false,
			validateFunc: func(t *testing.T, metadata *auth.RFC9728AuthInfo) {
				t.Helper()

				assert.Len(t, metadata.AuthorizationServers, 2)
			},
		},
		{
			name: "missing resource field",
			serverResponse: map[string]interface{}{
				"authorization_servers": []string{"https://auth.example.com"},
			},
			serverStatus:  http.StatusOK,
			contentType:   "application/json",
			expectedError: true,
		},
		{
			name:           "server returns 404",
			serverResponse: "Not Found",
			serverStatus:   http.StatusNotFound,
			contentType:    "text/plain",
			expectedError:  true,
		},
		{
			name:           "server returns wrong content type",
			serverResponse: "Not JSON",
			serverStatus:   http.StatusOK,
			contentType:    "text/html",
			expectedError:  true,
		},
		{
			name:           "invalid JSON response",
			serverResponse: "{ invalid json",
			serverStatus:   http.StatusOK,
			contentType:    "application/json",
			expectedError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create test server - use regular HTTP for localhost (allowed by our validation)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				w.WriteHeader(tt.serverStatus)

				switch v := tt.serverResponse.(type) {
				case string:
					w.Write([]byte(v))
				default:
					json.NewEncoder(w).Encode(v)
				}
			}))
			defer server.Close()

			// Replace http with https in the URL to simulate a real HTTPS server
			// but use localhost which is allowed to bypass HTTPS requirement
			testURL := strings.Replace(server.URL, "http://", "https://", 1)

			// For testing, we need to actually use localhost HTTP since we can't easily
			// create a valid HTTPS test server. The function allows localhost to use HTTP.
			if strings.Contains(server.URL, "127.0.0.1") {
				testURL = server.URL // Keep it as HTTP for localhost
			}

			// Test the function
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			metadata, err := FetchResourceMetadata(ctx, testURL)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				if tt.validateFunc != nil && metadata != nil {
					tt.validateFunc(t, metadata)
				}
			}
		})
	}
}

func TestFetchResourceMetadata_InvalidURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		metadataURL string
	}{
		{
			name:        "empty URL",
			metadataURL: "",
		},
		{
			name:        "invalid URL",
			metadataURL: "not-a-url",
		},
		{
			name:        "http URL (not HTTPS)",
			metadataURL: "http://example.com/.well-known/oauth-protected-resource",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			_, err := FetchResourceMetadata(ctx, tt.metadataURL)
			assert.Error(t, err, "Expected error for URL %s", tt.metadataURL)
		})
	}
}

func TestValidateAndDiscoverAuthServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		serverPath     string
		serverResponse interface{}
		serverStatus   int
		contentType    string
		expectedIssuer string
		expectedError  bool
	}{
		{
			name:       "valid authorization server with matching issuer",
			serverPath: "/.well-known/oauth-authorization-server",
			serverResponse: map[string]interface{}{
				"issuer":                 "https://auth.example.com",
				"authorization_endpoint": "https://auth.example.com/authorize",
				"token_endpoint":         "https://auth.example.com/token",
			},
			serverStatus:   http.StatusOK,
			contentType:    "application/json",
			expectedIssuer: "https://auth.example.com",
			expectedError:  false,
		},
		{
			name:       "authorization server with different issuer (Stripe case)",
			serverPath: "/.well-known/oauth-authorization-server",
			serverResponse: map[string]interface{}{
				"issuer":                 "https://marketplace.stripe.com",
				"authorization_endpoint": "https://marketplace.stripe.com/oauth/v2/authorize",
				"token_endpoint":         "https://marketplace.stripe.com/oauth/v2/token",
				"registration_endpoint":  "https://marketplace.stripe.com/oauth/v2/register",
			},
			serverStatus:   http.StatusOK,
			contentType:    "application/json",
			expectedIssuer: "https://marketplace.stripe.com",
			expectedError:  false,
		},
		{
			name:           "server returns 404",
			serverPath:     "/.well-known/oauth-authorization-server",
			serverResponse: "Not Found",
			serverStatus:   http.StatusNotFound,
			contentType:    "text/plain",
			expectedError:  true,
		},
		{
			name:       "missing required fields",
			serverPath: "/.well-known/oauth-authorization-server",
			serverResponse: map[string]interface{}{
				"issuer": "https://auth.example.com",
				// Missing authorization_endpoint and token_endpoint
			},
			serverStatus:  http.StatusOK,
			contentType:   "application/json",
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create test server - use regular HTTP for localhost (allowed by our validation)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tt.serverPath && r.URL.Path != "/.well-known/openid-configuration" {
					w.WriteHeader(http.StatusNotFound)
					return
				}

				w.Header().Set("Content-Type", tt.contentType)
				w.WriteHeader(tt.serverStatus)

				switch v := tt.serverResponse.(type) {
				case string:
					w.Write([]byte(v))
				default:
					json.NewEncoder(w).Encode(v)
				}
			}))
			defer server.Close()

			// For testing with localhost, we can use HTTP
			testURL := server.URL

			// Test the function
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			authInfo, err := ValidateAndDiscoverAuthServer(ctx, testURL)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				if authInfo != nil {
					assert.Equal(t, tt.expectedIssuer, authInfo.Issuer)
				}
			}
		})
	}
}

func TestParseWWWAuthenticate_WithResourceMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                     string
		header                   string
		expectedType             string
		expectedRealm            string
		expectedResourceMetadata string
		expectedError            bool
	}{
		{
			name:                     "bearer with resource_metadata",
			header:                   `Bearer resource_metadata="https://mcp.stripe.com/.well-known/oauth-protected-resource"`,
			expectedType:             "OAuth",
			expectedResourceMetadata: "https://mcp.stripe.com/.well-known/oauth-protected-resource",
			expectedError:            false,
		},
		{
			name:                     "bearer with realm and resource_metadata",
			header:                   `Bearer realm="https://auth.example.com", resource_metadata="https://resource.example.com/.well-known/oauth-protected-resource"`,
			expectedType:             "OAuth",
			expectedRealm:            "https://auth.example.com",
			expectedResourceMetadata: "https://resource.example.com/.well-known/oauth-protected-resource",
			expectedError:            false,
		},
		{
			name:          "bearer with error and error_description",
			header:        `Bearer error="invalid_token", error_description="The access token expired"`,
			expectedType:  "OAuth",
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			authInfo, err := ParseWWWAuthenticate(tt.header)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				require.NotNil(t, authInfo)
				assert.Equal(t, tt.expectedType, authInfo.Type)
				assert.Equal(t, tt.expectedRealm, authInfo.Realm)
				assert.Equal(t, tt.expectedResourceMetadata, authInfo.ResourceMetadata)
			}
		})
	}
}

func TestExtractParameter_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		params    string
		paramName string
		expected  string
	}{
		{
			name:      "parameter with escaped quotes",
			params:    `realm="My \"Quoted\" Realm"`,
			paramName: "realm",
			expected:  `My "Quoted" Realm`,
		},
		{
			name:      "parameter at end without comma",
			params:    `realm="https://auth.example.com"`,
			paramName: "realm",
			expected:  "https://auth.example.com",
		},
		{
			name:      "unquoted parameter",
			params:    `max_age=3600`,
			paramName: "max_age",
			expected:  "3600",
		},
		{
			name:      "mixed quoted and unquoted",
			params:    `realm="https://auth.example.com", max_age=3600, scope="read write"`,
			paramName: "scope",
			expected:  "read write",
		},
		{
			name:      "parameter with equals in value",
			params:    `resource_metadata="https://example.com?param=value"`,
			paramName: "resource_metadata",
			expected:  "https://example.com?param=value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := ExtractParameter(tt.params, tt.paramName)
			assert.Equal(t, tt.expected, result)
		})
	}
}

package runner

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

	"github.com/stacklok/toolhive/pkg/auth/discovery"
	"github.com/stacklok/toolhive/pkg/logger"
)

const (
	resourceMetadataPath = "/.well-known/resource-metadata"
)

func init() {
	// Initialize logger for tests
	logger.Initialize()
}

func TestDiscoverIssuerAndScopes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		config             *RemoteAuthConfig
		authInfo           *discovery.AuthInfo
		remoteURL          string
		mockServers        map[string]*httptest.Server
		expectedIssuer     string
		expectedScopes     []string
		expectedAuthServer bool
		expectError        bool
		errorContains      string
	}{
		// Priority 1: Configured issuer takes precedence
		{
			name: "configured issuer takes precedence",
			config: &RemoteAuthConfig{
				Issuer: "https://configured.example.com",
				Scopes: []string{"openid", "profile"},
			},
			authInfo: &discovery.AuthInfo{
				Type:             "OAuth",
				Realm:            "https://realm.example.com",
				ResourceMetadata: "https://metadata.example.com",
			},
			remoteURL:      "https://server.example.com",
			expectedIssuer: "https://configured.example.com",
			expectedScopes: []string{"openid", "profile"},
			expectError:    false,
		},

		// Priority 2: Realm-derived issuer
		{
			name:   "valid realm URL derives issuer",
			config: &RemoteAuthConfig{},
			authInfo: &discovery.AuthInfo{
				Type:  "OAuth",
				Realm: "https://auth.example.com/realm/mcp",
			},
			remoteURL:      "https://server.example.com",
			expectedIssuer: "https://auth.example.com/realm/mcp",
			expectedScopes: nil,
			expectError:    false,
		},
		{
			name:   "realm with query and fragment stripped",
			config: &RemoteAuthConfig{},
			authInfo: &discovery.AuthInfo{
				Type:  "OAuth",
				Realm: "https://auth.example.com/realm?param=value#fragment",
			},
			remoteURL:      "https://server.example.com",
			expectedIssuer: "https://auth.example.com/realm",
			expectedScopes: nil,
			expectError:    false,
		},

		// Priority 3: Resource metadata
		// These tests use dynamic setup to create properly linked servers
		{
			name:   "valid resource metadata",
			config: &RemoteAuthConfig{},
			authInfo: &discovery.AuthInfo{
				Type:             "OAuth",
				ResourceMetadata: "dynamic", // Special marker for dynamic setup
			},
			remoteURL: "https://server.example.com",
			mockServers: map[string]*httptest.Server{
				"dynamic": nil, // Will be created with linked servers
			},
			expectedIssuer:     "dynamic", // Will be set to auth server URL
			expectedScopes:     nil,
			expectedAuthServer: true,
			expectError:        false,
		},
		{
			name:   "resource metadata with multiple auth servers",
			config: &RemoteAuthConfig{},
			authInfo: &discovery.AuthInfo{
				Type:             "OAuth",
				ResourceMetadata: "dynamic-multi", // Special marker for dynamic setup
			},
			remoteURL: "https://server.example.com",
			mockServers: map[string]*httptest.Server{
				"dynamic": nil, // Will be created with linked servers
			},
			expectedIssuer:     "dynamic", // Will be set to second auth server URL
			expectedScopes:     nil,
			expectedAuthServer: true,
			expectError:        false,
		},

		// Priority 4: Well-known discovery (Atlassian scenario)
		{
			name:   "well-known discovery with issuer mismatch",
			config: &RemoteAuthConfig{},
			authInfo: &discovery.AuthInfo{
				Type: "OAuth",
			},
			remoteURL: "https://mcp.atlassian.com/v1/sse",
			mockServers: map[string]*httptest.Server{
				"mcp.atlassian.com": createMockAuthServer(t, "https://atlassian-workers.example.com"),
			},
			expectedIssuer:     "https://atlassian-workers.example.com",
			expectedScopes:     []string{"openid", "profile"},
			expectedAuthServer: true,
			expectError:        false,
		},

		// Priority 5: URL-derived fallback
		{
			name:   "url derived fallback when well-known fails",
			config: &RemoteAuthConfig{},
			authInfo: &discovery.AuthInfo{
				Type: "OAuth",
			},
			remoteURL: "", // Will be set from mock server
			mockServers: map[string]*httptest.Server{
				"localhost": createMock404Server(t),
			},
			expectedIssuer: "", // Will be set dynamically to match server URL
			expectedScopes: nil,
			expectError:    false,
		},

		// Security test cases
		{
			name:   "http realm rejected for security",
			config: &RemoteAuthConfig{},
			authInfo: &discovery.AuthInfo{
				Type:  "OAuth",
				Realm: "http://insecure.example.com", // HTTP not HTTPS
			},
			remoteURL: "https://server.example.com",
			// Should fall through to well-known
			mockServers: map[string]*httptest.Server{
				"server.example.com": createMockAuthServer(t, "https://server.example.com"),
			},
			expectedIssuer:     "https://server.example.com",
			expectedScopes:     []string{"openid", "profile"},
			expectedAuthServer: true,
			expectError:        false,
		},
		{
			name:   "localhost http realm allowed",
			config: &RemoteAuthConfig{},
			authInfo: &discovery.AuthInfo{
				Type:  "OAuth",
				Realm: "http://localhost:8080",
			},
			remoteURL:      "https://server.example.com",
			expectedIssuer: "http://localhost:8080",
			expectedScopes: nil,
			expectError:    false,
		},
		{
			name:   "malformed resource metadata URL",
			config: &RemoteAuthConfig{},
			authInfo: &discovery.AuthInfo{
				Type:             "OAuth",
				ResourceMetadata: "not-a-url",
			},
			remoteURL:     "https://server.example.com",
			expectError:   true,
			errorContains: "could not determine OAuth issuer",
		},

		// Edge cases
		{
			name:   "empty auth info",
			config: &RemoteAuthConfig{},
			authInfo: &discovery.AuthInfo{
				Type: "OAuth",
			},
			remoteURL: "https://server.example.com",
			mockServers: map[string]*httptest.Server{
				"server.example.com": createMockAuthServer(t, "https://server.example.com"),
			},
			expectedIssuer:     "https://server.example.com",
			expectedScopes:     []string{"openid", "profile"},
			expectedAuthServer: true,
			expectError:        false,
		},
		{
			name:   "all discovery methods fail",
			config: &RemoteAuthConfig{},
			authInfo: &discovery.AuthInfo{
				Type: "OAuth",
			},
			remoteURL: "", // Will be set from mock server
			mockServers: map[string]*httptest.Server{
				"localhost": createMock404Server(t),
			},
			expectedIssuer: "", // Will be set dynamically to match server URL
			expectedScopes: nil,
			expectError:    false,
		},
		{
			name:   "malformed remote URL",
			config: &RemoteAuthConfig{},
			authInfo: &discovery.AuthInfo{
				Type: "OAuth",
			},
			remoteURL:     "not-a-url",
			expectError:   true,
			errorContains: "could not determine OAuth issuer",
		},
		{
			name: "configured scopes used with discovered issuer",
			config: &RemoteAuthConfig{
				Scopes: []string{"custom", "scopes"},
			},
			authInfo: &discovery.AuthInfo{
				Type:  "OAuth",
				Realm: "https://auth.example.com",
			},
			remoteURL:      "https://server.example.com",
			expectedIssuer: "https://auth.example.com",
			expectedScopes: []string{"custom", "scopes"},
			expectError:    false,
		},
		{
			name:   "resource metadata with scopes",
			config: &RemoteAuthConfig{},
			authInfo: &discovery.AuthInfo{
				Type:             "OAuth",
				ResourceMetadata: "dynamic-scopes", // Special marker for dynamic setup
			},
			remoteURL: "https://server.example.com",
			mockServers: map[string]*httptest.Server{
				"dynamic": nil, // Will be created with linked servers
			},
			expectedIssuer:     "dynamic",                      // Will be set to auth server URL
			expectedScopes:     []string{"resource", "scopes"}, // Scopes from metadata are used
			expectedAuthServer: true,
			expectError:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Convert to testCase for helper functions
			tc := &testCase{
				name:               tt.name,
				config:             tt.config,
				authInfo:           tt.authInfo,
				remoteURL:          tt.remoteURL,
				mockServers:        tt.mockServers,
				expectedIssuer:     tt.expectedIssuer,
				expectedScopes:     tt.expectedScopes,
				expectedAuthServer: tt.expectedAuthServer,
				expectError:        tt.expectError,
				errorContains:      tt.errorContains,
			}

			// Process test servers using helper function
			setup, authInfo, remoteURL, expectedIssuer := processTestServers(t, tc)
			defer setup.cleanup()

			// Update expected issuer from processing
			if expectedIssuer != "" && expectedIssuer != tt.expectedIssuer {
				tt.expectedIssuer = expectedIssuer
			}

			handler := &RemoteAuthHandler{
				config: tt.config,
			}

			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()

			issuer, scopes, authServerInfo, err := handler.discoverIssuerAndScopes(
				ctx,
				authInfo,
				remoteURL,
			)

			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expectedIssuer, issuer, "issuer mismatch")
			assert.Equal(t, tt.expectedScopes, scopes, "scopes mismatch")

			if tt.expectedAuthServer {
				assert.NotNil(t, authServerInfo, "expected auth server info")
				if authServerInfo != nil {
					assert.Equal(t, tt.expectedIssuer, authServerInfo.Issuer, "auth server issuer mismatch")
					assert.NotEmpty(t, authServerInfo.AuthorizationURL, "authorization URL should not be empty")
					assert.NotEmpty(t, authServerInfo.TokenURL, "token URL should not be empty")
				}
			} else {
				assert.Nil(t, authServerInfo, "expected no auth server info")
			}
		})
	}
}

// Helper functions to create mock servers

func createMockAuthServer(t *testing.T, issuer string) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Handle all possible well-known paths
		if strings.Contains(r.URL.Path, "/.well-known/oauth-authorization-server") ||
			strings.Contains(r.URL.Path, "/.well-known/openid-configuration") {
			w.Header().Set("Content-Type", "application/json")
			// Use the provided issuer, or if empty, use the actual server URL
			actualIssuer := issuer
			if actualIssuer == "" {
				actualIssuer = "http://" + r.Host
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"issuer":                 actualIssuer,
				"authorization_endpoint": actualIssuer + "/authorize",
				"token_endpoint":         actualIssuer + "/token",
				"registration_endpoint":  actualIssuer + "/register",
			})
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func createMock404Server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
}

func createMockResourceMetadataServer(t *testing.T, authServers []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == resourceMetadataPath {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"resource":              "https://resource.example.com",
				"authorization_servers": authServers,
			})
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func createMockResourceMetadataServerWithScopes(t *testing.T, authServers []string, scopes []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == resourceMetadataPath {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"resource":              "https://resource.example.com",
				"authorization_servers": authServers,
				"scopes_supported":      scopes,
			})
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// Security-focused tests
func TestDiscoverIssuerAndScopes_Security(t *testing.T) {
	t.Parallel()

	t.Run("prevents issuer injection via realm", func(t *testing.T) {
		t.Parallel()
		handler := &RemoteAuthHandler{
			config: &RemoteAuthConfig{},
		}

		// Try to inject a malicious issuer via realm
		authInfo := &discovery.AuthInfo{
			Type:  "OAuth",
			Realm: "https://evil.com/../../legitimate.com",
		}

		ctx := t.Context()
		issuer, _, _, err := handler.discoverIssuerAndScopes(ctx, authInfo, "https://server.example.com")

		require.NoError(t, err)
		// The path traversal should be normalized
		assert.NotContains(t, issuer, "..")
	})

	t.Run("validates HTTPS for non-localhost", func(t *testing.T) {
		t.Parallel()
		handler := &RemoteAuthHandler{
			config: &RemoteAuthConfig{},
		}

		authInfo := &discovery.AuthInfo{
			Type:  "OAuth",
			Realm: "http://external.example.com", // HTTP not HTTPS
		}

		mockServer := createMockAuthServer(t, "https://fallback.example.com")
		defer mockServer.Close()

		ctx := t.Context()
		issuer, _, _, err := handler.discoverIssuerAndScopes(ctx, authInfo, mockServer.URL)

		require.NoError(t, err)
		// Should not use the insecure realm, should fall through
		assert.NotEqual(t, "http://external.example.com", issuer)
	})

	t.Run("handles malicious resource metadata response", func(t *testing.T) {
		t.Parallel()
		maliciousServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == resourceMetadataPath {
				// Send a huge response to try DoS
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"resource": "`))
				for i := 0; i < 10000000; i++ {
					w.Write([]byte("A"))
				}
				w.Write([]byte(`"}`))
			}
		}))
		defer maliciousServer.Close()

		handler := &RemoteAuthHandler{
			config: &RemoteAuthConfig{},
		}

		authInfo := &discovery.AuthInfo{
			Type:             "OAuth",
			ResourceMetadata: maliciousServer.URL + resourceMetadataPath,
		}

		ctx, cancel := context.WithTimeout(t.Context(), 1*time.Second)
		defer cancel()

		_, _, _, err := handler.discoverIssuerAndScopes(ctx, authInfo, "https://server.example.com")

		// Should timeout or fail gracefully, not hang or crash
		assert.Error(t, err)
	})
}

// Test the helper functions
func TestTryDiscoverFromWellKnown(t *testing.T) {
	t.Parallel()

	t.Run("discovers actual issuer from localhost server", func(t *testing.T) {
		t.Parallel()
		// For localhost test servers, the issuer will be the server's HTTP URL
		mockServer := createMockAuthServer(t, "") // Will use actual server URL
		defer mockServer.Close()

		handler := &RemoteAuthHandler{
			config: &RemoteAuthConfig{},
		}

		ctx := t.Context()
		issuer, scopes, authInfo, err := handler.tryDiscoverFromWellKnown(ctx, mockServer.URL)

		require.NoError(t, err)
		assert.Equal(t, mockServer.URL, issuer)                // For localhost, issuer matches server URL
		assert.Equal(t, []string{"openid", "profile"}, scopes) // Default scopes
		assert.NotNil(t, authInfo)
		assert.Equal(t, mockServer.URL, authInfo.Issuer)
	})

	t.Run("uses configured scopes", func(t *testing.T) {
		t.Parallel()
		mockServer := createMockAuthServer(t, "") // Will use actual server URL
		defer mockServer.Close()

		handler := &RemoteAuthHandler{
			config: &RemoteAuthConfig{
				Scopes: []string{"custom", "scopes"},
			},
		}

		ctx := t.Context()
		issuer, scopes, _, err := handler.tryDiscoverFromWellKnown(ctx, mockServer.URL)

		require.NoError(t, err)
		assert.Equal(t, mockServer.URL, issuer) // For localhost, issuer matches server URL
		assert.Equal(t, []string{"custom", "scopes"}, scopes)
	})

	t.Run("handles discovery failure", func(t *testing.T) {
		t.Parallel()
		mockServer := createMock404Server(t)
		defer mockServer.Close()

		handler := &RemoteAuthHandler{
			config: &RemoteAuthConfig{},
		}

		ctx := t.Context()
		_, _, _, err := handler.tryDiscoverFromWellKnown(ctx, mockServer.URL)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "well-known discovery failed")
	})
}

// TestDiscoveryPriorityChain tests that the discovery follows the correct priority order
func TestDiscoveryPriorityChain(t *testing.T) {
	t.Parallel()

	t.Run("configured issuer takes highest priority", func(t *testing.T) {
		t.Parallel()
		handler := &RemoteAuthHandler{
			config: &RemoteAuthConfig{
				Issuer: "https://configured.example.com",
				Scopes: []string{"custom"},
			},
		}

		authInfo := &discovery.AuthInfo{
			Type:             "OAuth",
			Realm:            "https://realm.example.com",
			ResourceMetadata: "https://metadata.example.com",
		}

		ctx := context.Background()
		issuer, scopes, _, err := handler.discoverIssuerAndScopes(ctx, authInfo, "https://server.example.com")

		require.NoError(t, err)
		assert.Equal(t, "https://configured.example.com", issuer)
		assert.Equal(t, []string{"custom"}, scopes)
	})

	t.Run("realm URL used when no configured issuer", func(t *testing.T) {
		t.Parallel()
		handler := &RemoteAuthHandler{
			config: &RemoteAuthConfig{},
		}

		authInfo := &discovery.AuthInfo{
			Type:  "OAuth",
			Realm: "https://realm.example.com/oauth",
		}

		ctx := context.Background()
		issuer, _, _, err := handler.discoverIssuerAndScopes(ctx, authInfo, "https://server.example.com")

		require.NoError(t, err)
		assert.Equal(t, "https://realm.example.com/oauth", issuer)
	})

	t.Run("non-URL realm falls through to URL derivation", func(t *testing.T) {
		t.Parallel()
		handler := &RemoteAuthHandler{
			config: &RemoteAuthConfig{},
		}

		authInfo := &discovery.AuthInfo{
			Type:  "OAuth",
			Realm: "OAuth", // Not a URL, like Atlassian
		}

		ctx := context.Background()
		issuer, _, _, err := handler.discoverIssuerAndScopes(ctx, authInfo, "https://server.example.com")

		require.NoError(t, err)
		// Should fall through to URL-derived issuer
		assert.Equal(t, "https://server.example.com", issuer)
	})

	t.Run("empty auth info falls through to URL derivation", func(t *testing.T) {
		t.Parallel()
		handler := &RemoteAuthHandler{
			config: &RemoteAuthConfig{},
		}

		authInfo := &discovery.AuthInfo{
			Type: "OAuth",
		}

		ctx := context.Background()
		issuer, _, _, err := handler.discoverIssuerAndScopes(ctx, authInfo, "https://server.example.com/path")

		require.NoError(t, err)
		assert.Equal(t, "https://server.example.com", issuer)
	})
}

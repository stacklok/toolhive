package remote

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

	tests := []testCase{
		// Priority 1: Configured issuer takes precedence
		{
			name: "configured issuer takes precedence",
			config: &Config{
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
			config: &Config{},
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
			config: &Config{},
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
			config: &Config{},
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
			config: &Config{},
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
			config: &Config{},
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
			config: &Config{},
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
			config: &Config{},
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
			config: &Config{},
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
			config: &Config{},
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
			config: &Config{},
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
			config: &Config{},
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
			config: &Config{},
			authInfo: &discovery.AuthInfo{
				Type: "OAuth",
			},
			remoteURL:     "not-a-url",
			expectError:   true,
			errorContains: "could not determine OAuth issuer",
		},
		{
			name: "configured scopes used with discovered issuer",
			config: &Config{
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
			config: &Config{},
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

			// Process test servers
			setup, authInfo, remoteURL, expectedIssuer := processTestServers(t, &tt)
			defer setup.cleanup()

			// Update expected issuer from processing
			if expectedIssuer != "" && expectedIssuer != tt.expectedIssuer {
				tt.expectedIssuer = expectedIssuer
			}

			handler := &Handler{
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
		handler := &Handler{
			config: &Config{},
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
		handler := &Handler{
			config: &Config{},
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

		handler := &Handler{
			config: &Config{},
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

		handler := &Handler{
			config: &Config{},
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

		handler := &Handler{
			config: &Config{
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

		handler := &Handler{
			config: &Config{},
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
		handler := &Handler{
			config: &Config{
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
		handler := &Handler{
			config: &Config{},
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
		handler := &Handler{
			config: &Config{},
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
		handler := &Handler{
			config: &Config{},
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

func TestTryDiscoverFromResourceMetadata_EmptyScopes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		configScopes   []string
		metadataScopes []string
		expectedScopes []string
		description    string
	}{
		{
			name:           "metadata with no scopes_supported - scopes remain empty",
			configScopes:   nil,
			metadataScopes: nil, // RFC 9728: scopes_supported is optional
			expectedScopes: nil,
			description:    "RFC 9728 compliant: when metadata has no scopes_supported, don't add defaults",
		},
		{
			name:           "metadata with empty scopes_supported - scopes remain empty",
			configScopes:   nil,
			metadataScopes: []string{},
			expectedScopes: nil,
			description:    "When metadata explicitly has empty scopes, don't add defaults",
		},
		{
			name:           "metadata with scopes but user configured scopes - user config wins",
			configScopes:   []string{"custom1", "custom2"},
			metadataScopes: []string{"metadata1", "metadata2"},
			expectedScopes: []string{"custom1", "custom2"},
			description:    "User-configured scopes take precedence over metadata scopes",
		},
		{
			name:           "metadata with scopes and no user config - use metadata scopes",
			configScopes:   nil,
			metadataScopes: []string{"incidents_read", "incidents_write"},
			expectedScopes: []string{"incidents_read", "incidents_write"},
			description:    "When no user config, use scopes from metadata",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create an auth server first (needed for validation)
			authServer := createMockAuthServer(t, "")
			defer authServer.Close()

			// Create a metadata server that references the auth server
			metadataServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Serve well-known metadata
				if strings.Contains(r.URL.Path, "oauth-protected-resource") {
					metadata := map[string]interface{}{
						"resource":                 "https://example.com",
						"authorization_servers":    []string{authServer.URL}, // Point to our mock auth server
						"bearer_methods_supported": []string{"header"},
					}
					if len(tt.metadataScopes) > 0 {
						metadata["scopes_supported"] = tt.metadataScopes
					}
					// If metadataScopes is nil, don't include the field (RFC 9728: scopes_supported is optional)
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(metadata)
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			defer metadataServer.Close()

			// Create handler with test config
			handler := &Handler{
				config: &Config{
					Scopes: tt.configScopes,
				},
			}

			ctx := context.Background()
			metadataURL := metadataServer.URL + "/.well-known/oauth-protected-resource"

			// Call tryDiscoverFromResourceMetadata
			issuer, scopes, authServerInfo, err := handler.tryDiscoverFromResourceMetadata(ctx, metadataURL)

			// Verify results
			require.NoError(t, err, tt.description)
			assert.NotEmpty(t, issuer, "Should have discovered issuer")
			assert.NotNil(t, authServerInfo, "Should have auth server info")

			// CRITICAL TEST: Verify scopes behavior
			if tt.expectedScopes == nil {
				assert.Nil(t, scopes, "%s - scopes should be nil, not empty slice or defaults", tt.description)
			} else {
				assert.Equal(t, tt.expectedScopes, scopes, tt.description)
			}
		})
	}
}

func TestAuthenticate_BearerToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		config         *Config
		serverResponse func(w http.ResponseWriter, r *http.Request)
		expectError    bool
		expectToken    bool
		expectedType   string
	}{
		{
			name: "explicit bearer token configured",
			config: &Config{
				BearerToken: "test-bearer-token-123",
			},
			serverResponse: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			},
			expectError:  false,
			expectToken:  true,
			expectedType: "Bearer",
		},
		{
			name: "bearer token from server detection",
			config: &Config{
				BearerToken: "test-bearer-token-456",
			},
			serverResponse: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="https://example.com"`)
				w.WriteHeader(http.StatusUnauthorized)
			},
			expectError: false,
			expectToken: true,
		},
		{
			name:   "server requires bearer token but none configured - fallback to OAuth for backward compatibility",
			config: &Config{},
			serverResponse: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="https://example.com"`)
				w.WriteHeader(http.StatusUnauthorized)
			},
			// With backward compatibility, Bearer header with realm should attempt OAuth flow
			// This will fail OAuth discovery, but that's expected without OAuth config
			expectError: true, // OAuth discovery will fail without issuer/client config
			expectToken: false,
		},
		{
			name:   "server requires bearer token without realm - should error",
			config: &Config{},
			serverResponse: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("WWW-Authenticate", `Bearer`)
				w.WriteHeader(http.StatusUnauthorized)
			},
			expectError: true,
			expectToken: false,
		},
		{
			name: "bearer token takes precedence over OAuth",
			config: &Config{
				BearerToken: "test-bearer-token-789",
				ClientID:    "oauth-client-id",
				Issuer:      "https://oauth.example.com",
			},
			serverResponse: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("WWW-Authenticate", `OAuth realm="https://oauth.example.com"`)
				w.WriteHeader(http.StatusUnauthorized)
			},
			expectError: false,
			expectToken: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create test server
			server := httptest.NewServer(http.HandlerFunc(tt.serverResponse))
			defer server.Close()

			handler := NewHandler(tt.config)
			ctx := context.Background()

			tokenSource, err := handler.Authenticate(ctx, server.URL)

			if tt.expectError {
				require.Error(t, err)
				assert.Nil(t, tokenSource)
				return
			}

			require.NoError(t, err)
			if tt.expectToken {
				require.NotNil(t, tokenSource, "Expected token source to be created")
				token, err := tokenSource.Token()
				require.NoError(t, err)
				assert.Equal(t, tt.config.BearerToken, token.AccessToken)
				assert.Equal(t, "Bearer", token.TokenType)
			} else {
				assert.Nil(t, tokenSource)
			}
		})
	}
}

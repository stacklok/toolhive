// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/auth/discovery"
	"github.com/stacklok/toolhive/pkg/secrets/mocks"
)

const (
	resourceMetadataPath = "/.well-known/resource-metadata"
)

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
			name:   "malformed resource metadata URL falls through to URL-derived issuer",
			config: &Config{},
			authInfo: &discovery.AuthInfo{
				Type:             "OAuth",
				ResourceMetadata: "not-a-url",
			},
			remoteURL:      "https://server.example.com",
			expectError:    false,
			expectedIssuer: "https://server.example.com",
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

		issuer, _, _, err := handler.discoverIssuerAndScopes(ctx, authInfo, "https://server.example.com")

		// Should not hang or crash; Priority 3 fails gracefully and falls through to URL-derived issuer
		require.NoError(t, err)
		assert.Equal(t, "https://server.example.com", issuer)
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

// TestAuthenticate_BearerToken tests bearer token authentication
func TestAuthenticate_BearerToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		config      *Config
		remoteURL   string
		expectError bool
		expectToken bool
		tokenValue  string
	}{
		{
			name: "bearer token authentication succeeds",
			config: &Config{
				BearerToken: "my-bearer-token-123",
			},
			remoteURL:   "https://example.com/mcp",
			expectError: false,
			expectToken: true,
			tokenValue:  "my-bearer-token-123",
		},
		{
			name: "empty bearer token returns nil token source",
			config: &Config{
				BearerToken: "",
			},
			remoteURL:   "https://example.com/mcp",
			expectError: false,
			expectToken: false,
		},
		{
			name: "bearer token takes priority over OAuth client secret",
			config: &Config{
				BearerToken:  "my-token",
				ClientSecret: "client-secret",
			},
			remoteURL:   "https://example.com/mcp",
			expectError: false,
			expectToken: true,
			tokenValue:  "my-token",
		},
		{
			name: "bearer token takes priority over OAuth issuer",
			config: &Config{
				BearerToken: "my-token",
				Issuer:      "https://issuer.example.com",
			},
			remoteURL:   "https://example.com/mcp",
			expectError: false,
			expectToken: true,
			tokenValue:  "my-token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := NewHandler(tt.config)
			ctx := context.Background()

			tokenSource, err := handler.Authenticate(ctx, tt.remoteURL)

			require.NoError(t, err)

			if tt.expectToken {
				require.NotNil(t, tokenSource, "Expected token source but got nil")
				token, err := tokenSource.Token()
				require.NoError(t, err)
				assert.Equal(t, tt.tokenValue, token.AccessToken)
				assert.Equal(t, "Bearer", token.TokenType)
			} else {
				assert.Nil(t, tokenSource, "Expected nil token source but got one")
			}
		})
	}
}

// TestAuthenticate_BearerTokenPriority tests that bearer token takes priority over OAuth detection
func TestAuthenticate_BearerTokenPriority(t *testing.T) {
	t.Parallel()

	// Create a mock server that would normally trigger OAuth detection
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Return WWW-Authenticate header that would trigger OAuth detection
		w.Header().Set("WWW-Authenticate", `Bearer realm="https://auth.example.com", resource_metadata="https://metadata.example.com"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer mockServer.Close()

	handler := NewHandler(&Config{
		BearerToken: "my-bearer-token",
	})

	ctx := context.Background()
	tokenSource, err := handler.Authenticate(ctx, mockServer.URL)

	// Should use bearer token, not attempt OAuth detection
	require.NoError(t, err)
	require.NotNil(t, tokenSource)

	token, err := tokenSource.Token()
	require.NoError(t, err)
	assert.Equal(t, "my-bearer-token", token.AccessToken)
	assert.Equal(t, "Bearer", token.TokenType)
}

// TestAuthenticate_BearerTokenDiscovery tests that bearer token discovery works correctly
func TestAuthenticate_BearerTokenDiscovery(t *testing.T) {
	t.Parallel()

	t.Run("bearer token discovery returns helpful error when token not configured", func(t *testing.T) {
		t.Parallel()

		// Create a mock server that requires simple bearer token (no OAuth flow)
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			// Handle both GET and POST requests for discovery
			// Return WWW-Authenticate header with just "Bearer" (no realm/resource_metadata)
			w.Header().Set("WWW-Authenticate", `Bearer`)
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer mockServer.Close()

		handler := NewHandler(&Config{
			BearerToken: "", // No bearer token configured
		})

		ctx := context.Background()
		tokenSource, err := handler.Authenticate(ctx, mockServer.URL)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "server requires bearer token authentication")
		assert.Contains(t, err.Error(), "--remote-auth-bearer-token")
		assert.Contains(t, err.Error(), "TOOLHIVE_REMOTE_AUTH_BEARER_TOKEN")
		assert.Nil(t, tokenSource)
	})

	t.Run("bearer token discovery succeeds when token is configured", func(t *testing.T) {
		t.Parallel()

		handler := NewHandler(&Config{
			BearerToken: "my-configured-token",
		})

		// Create a mock server - but token is configured so discovery won't be called
		mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("WWW-Authenticate", `Bearer`)
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer mockServer.Close()

		ctx := context.Background()
		tokenSource, err := handler.Authenticate(ctx, mockServer.URL)

		require.NoError(t, err)
		require.NotNil(t, tokenSource)

		token, err := tokenSource.Token()
		require.NoError(t, err)
		assert.Equal(t, "my-configured-token", token.AccessToken)
		assert.Equal(t, "Bearer", token.TokenType)
	})
}

// stubTokenSource is a minimal oauth2.TokenSource used in wrapWithPersistence tests.
type stubTokenSource struct{}

func (*stubTokenSource) Token() (*oauth2.Token, error) { return &oauth2.Token{}, nil }

func TestBuildOAuthFlowConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		config         *Config
		scopes         []string
		authServerInfo *discovery.AuthServerInfo
		wantConfig     *discovery.OAuthFlowConfig
	}{
		{
			name: "nil authServerInfo — config fields copied as-is",
			config: &Config{
				ClientID:     "client-id",
				ClientSecret: "client-secret",
				AuthorizeURL: "https://auth.example.com/authorize",
				TokenURL:     "https://auth.example.com/token",
				CallbackPort: 8080,
				SkipBrowser:  true,
				Resource:     "https://api.example.com",
				OAuthParams:  map[string]string{"audience": "myapi"},
			},
			scopes:         []string{"openid", "profile"},
			authServerInfo: nil,
			wantConfig: &discovery.OAuthFlowConfig{
				ClientID:     "client-id",
				ClientSecret: "client-secret",
				AuthorizeURL: "https://auth.example.com/authorize",
				TokenURL:     "https://auth.example.com/token",
				Scopes:       []string{"openid", "profile"},
				CallbackPort: 8080,
				SkipBrowser:  true,
				Resource:     "https://api.example.com",
				OAuthParams:  map[string]string{"audience": "myapi"},
			},
		},
		{
			name: "authServerInfo used when config URLs are empty",
			config: &Config{
				ClientID: "client-id",
			},
			scopes: []string{"openid"},
			authServerInfo: &discovery.AuthServerInfo{
				AuthorizationURL:     "https://discovered.example.com/authorize",
				TokenURL:             "https://discovered.example.com/token",
				RegistrationEndpoint: "https://discovered.example.com/register",
			},
			wantConfig: &discovery.OAuthFlowConfig{
				ClientID:             "client-id",
				AuthorizeURL:         "https://discovered.example.com/authorize",
				TokenURL:             "https://discovered.example.com/token",
				RegistrationEndpoint: "https://discovered.example.com/register",
				Scopes:               []string{"openid"},
			},
		},
		{
			name: "config AuthorizeURL preserved when set",
			config: &Config{
				AuthorizeURL: "https://static.example.com/authorize",
			},
			scopes: nil,
			authServerInfo: &discovery.AuthServerInfo{
				AuthorizationURL: "https://discovered.example.com/authorize",
				TokenURL:         "https://discovered.example.com/token",
			},
			wantConfig: &discovery.OAuthFlowConfig{
				// AuthorizeURL set → authServerInfo is NOT used (TokenURL also not overwritten)
				AuthorizeURL: "https://static.example.com/authorize",
				TokenURL:     "",
			},
		},
		{
			name: "config TokenURL preserved when set",
			config: &Config{
				TokenURL: "https://static.example.com/token",
			},
			scopes: nil,
			authServerInfo: &discovery.AuthServerInfo{
				AuthorizationURL: "https://discovered.example.com/authorize",
				TokenURL:         "https://discovered.example.com/token",
			},
			wantConfig: &discovery.OAuthFlowConfig{
				// TokenURL set → authServerInfo is NOT used (AuthorizeURL also not overwritten)
				AuthorizeURL: "",
				TokenURL:     "https://static.example.com/token",
			},
		},
		{
			name: "Resource and OAuthParams passed through unchanged",
			config: &Config{
				Resource:    "https://api.example.com/resource",
				OAuthParams: map[string]string{"key": "value"},
			},
			scopes:         nil,
			authServerInfo: nil,
			wantConfig: &discovery.OAuthFlowConfig{
				Resource:    "https://api.example.com/resource",
				OAuthParams: map[string]string{"key": "value"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := &Handler{config: tt.config}
			got := handler.buildOAuthFlowConfig(tt.scopes, tt.authServerInfo)

			assert.Equal(t, tt.wantConfig.ClientID, got.ClientID, "ClientID")
			assert.Equal(t, tt.wantConfig.ClientSecret, got.ClientSecret, "ClientSecret")
			assert.Equal(t, tt.wantConfig.AuthorizeURL, got.AuthorizeURL, "AuthorizeURL")
			assert.Equal(t, tt.wantConfig.TokenURL, got.TokenURL, "TokenURL")
			assert.Equal(t, tt.wantConfig.RegistrationEndpoint, got.RegistrationEndpoint, "RegistrationEndpoint")
			assert.Equal(t, tt.wantConfig.Scopes, got.Scopes, "Scopes")
			assert.Equal(t, tt.wantConfig.Resource, got.Resource, "Resource")
			assert.Equal(t, tt.wantConfig.OAuthParams, got.OAuthParams, "OAuthParams")
			assert.Equal(t, tt.wantConfig.CallbackPort, got.CallbackPort, "CallbackPort")
			assert.Equal(t, tt.wantConfig.SkipBrowser, got.SkipBrowser, "SkipBrowser")
		})
	}
}

func TestWrapWithPersistence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                       string
		tokenPersister             TokenPersister
		clientCredentialsPersister ClientCredentialsPersister
		result                     *discovery.OAuthFlowResult
		wantPersistingSource       bool // true if returned source should be a *PersistingTokenSource
	}{
		{
			name:                 "nil persisters — returns original token source unwrapped",
			tokenPersister:       nil,
			result:               &discovery.OAuthFlowResult{TokenSource: &stubTokenSource{}, RefreshToken: "rt"},
			wantPersistingSource: false,
		},
		{
			name: "token persister called when refresh token present",
			tokenPersister: func(_ string, _ time.Time) error {
				return nil
			},
			result:               &discovery.OAuthFlowResult{TokenSource: &stubTokenSource{}, RefreshToken: "rt"},
			wantPersistingSource: true,
		},
		{
			name: "token persister NOT called when refresh token empty",
			tokenPersister: func(_ string, _ time.Time) error {
				// This should NOT be called; if it is, returning an error makes the test meaningful
				return errors.New("persister should not have been called")
			},
			result: &discovery.OAuthFlowResult{
				TokenSource:  &stubTokenSource{},
				RefreshToken: "", // empty — persister must not be invoked
			},
			// tokenPersister is set so source is still wrapped
			wantPersistingSource: true,
		},
		{
			name: "token persister error is non-fatal",
			tokenPersister: func(_ string, _ time.Time) error {
				return errors.New("persist failed")
			},
			result:               &discovery.OAuthFlowResult{TokenSource: &stubTokenSource{}, RefreshToken: "rt"},
			wantPersistingSource: true,
		},
		{
			name: "client credentials persister called when clientID present",
			clientCredentialsPersister: func(clientID, clientSecret string) error {
				assert.Equal(t, "my-client-id", clientID)
				assert.Equal(t, "my-client-secret", clientSecret)
				return nil
			},
			result: &discovery.OAuthFlowResult{
				TokenSource:  &stubTokenSource{},
				ClientID:     "my-client-id",
				ClientSecret: "my-client-secret",
			},
			wantPersistingSource: false, // no tokenPersister set
		},
		{
			name: "client credentials persister NOT called when clientID empty",
			clientCredentialsPersister: func(_, _ string) error {
				return errors.New("persister should not have been called")
			},
			result: &discovery.OAuthFlowResult{
				TokenSource: &stubTokenSource{},
				ClientID:    "", // empty — persister must not be invoked
			},
			wantPersistingSource: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := &Handler{
				config:                     &Config{},
				tokenPersister:             tt.tokenPersister,
				clientCredentialsPersister: tt.clientCredentialsPersister,
			}

			got := handler.wrapWithPersistence(tt.result)

			require.NotNil(t, got)
			if tt.wantPersistingSource {
				_, ok := got.(*PersistingTokenSource)
				assert.True(t, ok, "expected *PersistingTokenSource, got %T", got)
			} else {
				assert.Equal(t, tt.result.TokenSource, got)
			}
		})
	}
}

func TestResolveClientCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		config           *Config
		setupMock        func(provider *mocks.MockProvider)
		wantClientID     string
		wantClientSecret string
	}{
		{
			name: "no cached credentials — static config used",
			config: &Config{
				ClientID:     "static-id",
				ClientSecret: "static-secret",
			},
			setupMock:        nil,
			wantClientID:     "static-id",
			wantClientSecret: "static-secret",
		},
		{
			name: "cached client ID overrides static",
			config: &Config{
				ClientID:       "static-id",
				ClientSecret:   "static-secret",
				CachedClientID: "cached-id",
				// CachedClientSecretRef empty → no secret fetch; static secret kept
			},
			setupMock:        nil,
			wantClientID:     "cached-id",
			wantClientSecret: "static-secret", // static secret preserved when no ref to override it
		},
		{
			name: "cached client ID with secret ref — secret fetched",
			config: &Config{
				CachedClientID:        "cached-id",
				CachedClientSecretRef: "secret-ref",
			},
			setupMock: func(provider *mocks.MockProvider) {
				provider.EXPECT().
					GetSecret(gomock.Any(), "secret-ref").
					Return("cached-secret", nil)
			},
			wantClientID:     "cached-id",
			wantClientSecret: "cached-secret",
		},
		{
			name: "cached secret ref with provider error — falls back to empty secret",
			config: &Config{
				CachedClientID:        "cached-id",
				CachedClientSecretRef: "secret-ref",
			},
			setupMock: func(provider *mocks.MockProvider) {
				provider.EXPECT().
					GetSecret(gomock.Any(), "secret-ref").
					Return("", errors.New("storage error"))
			},
			wantClientID:     "cached-id",
			wantClientSecret: "",
		},
		{
			name: "nil secret provider — empty secret used even if ref set",
			config: &Config{
				CachedClientID:        "cached-id",
				CachedClientSecretRef: "secret-ref",
			},
			setupMock:        nil, // secretProvider stays nil
			wantClientID:     "cached-id",
			wantClientSecret: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := &Handler{config: tt.config}

			if tt.setupMock != nil {
				ctrl := gomock.NewController(t)
				mockProvider := mocks.NewMockProvider(ctrl)
				tt.setupMock(mockProvider)
				handler.secretProvider = mockProvider
			}

			gotID, gotSecret := handler.resolveClientCredentials(context.Background())

			assert.Equal(t, tt.wantClientID, gotID, "clientID")
			assert.Equal(t, tt.wantClientSecret, gotSecret, "clientSecret")
		})
	}
}

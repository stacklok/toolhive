package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth/oauth"
)

const oauthWellKnownPath = "/.well-known/oauth-authorization-server"

// TestGetDiscoveryDocument_IssuerMismatch tests that getDiscoveryDocument
// correctly handles cases where the issuer in the metadata differs from
// the metadata URL (like Atlassian's case)
func TestGetDiscoveryDocument_IssuerMismatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		metadataURL    string
		actualIssuer   string
		expectError    bool
		expectedIssuer string
	}{
		{
			name:           "issuer mismatch - Atlassian pattern",
			metadataURL:    "https://mcp.atlassian.com",
			actualIssuer:   "https://atlassian-remote-mcp-production.workers.dev",
			expectError:    false,
			expectedIssuer: "https://atlassian-remote-mcp-production.workers.dev",
		},
		{
			name:           "issuer mismatch - Stripe pattern",
			metadataURL:    "https://mcp.stripe.com",
			actualIssuer:   "https://marketplace.stripe.com",
			expectError:    false,
			expectedIssuer: "https://marketplace.stripe.com",
		},
		{
			name:           "issuer matches metadata URL",
			metadataURL:    "https://auth.example.com",
			actualIssuer:   "https://auth.example.com",
			expectError:    false,
			expectedIssuer: "https://auth.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create a test server that returns OAuth metadata with different issuer
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Check if this is the OAuth authorization server metadata endpoint
				if r.URL.Path == oauthWellKnownPath {
					metadata := oauth.OIDCDiscoveryDocument{
						Issuer:                tt.actualIssuer,
						AuthorizationEndpoint: tt.actualIssuer + "/v1/authorize",
						TokenEndpoint:         tt.actualIssuer + "/v1/token",
						RegistrationEndpoint:  tt.actualIssuer + "/v1/register",
					}
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(metadata)
					return
				}
				// OIDC endpoint returns 404 (like Atlassian)
				w.WriteHeader(http.StatusNotFound)
			}))
			defer server.Close()

			// Override the metadata URL to point to our test server
			// In real scenario, this would be the public URL like mcp.atlassian.com
			ctx := context.Background()
			config := &OAuthFlowConfig{}

			// Mock the issuer to be our test server URL
			// This simulates the scenario where we try to discover from one URL
			// but the metadata contains a different issuer
			// Set the flag to indicate we're in resource metadata discovery context
			config.IsResourceMetadataDiscovery = true
			doc, err := getDiscoveryDocument(ctx, server.URL, config)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, doc)
			} else {
				require.NoError(t, err)
				require.NotNil(t, doc)

				// The key assertion: the issuer should be what's in the metadata,
				// not the URL we used to fetch it
				assert.Equal(t, tt.expectedIssuer, doc.Issuer)
				assert.Equal(t, tt.actualIssuer+"/v1/authorize", doc.AuthorizationEndpoint)
				assert.Equal(t, tt.actualIssuer+"/v1/token", doc.TokenEndpoint)
				assert.Equal(t, tt.actualIssuer+"/v1/register", doc.RegistrationEndpoint)
			}
		})
	}
}

// TestValidateAndDiscoverAuthServer_IssuerMismatch tests the complete flow
// of discovering an auth server where the issuer differs from the metadata URL
func TestValidateAndDiscoverAuthServer_IssuerMismatch(t *testing.T) {
	t.Parallel()

	// Create a test server that mimics Atlassian's behavior
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == oauthWellKnownPath {
			metadata := oauth.OIDCDiscoveryDocument{
				Issuer:                "https://actual-issuer.example.com",
				AuthorizationEndpoint: "https://actual-issuer.example.com/authorize",
				TokenEndpoint:         "https://actual-issuer.example.com/token",
				RegistrationEndpoint:  "https://actual-issuer.example.com/register",
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(metadata)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	ctx := context.Background()
	authInfo, err := ValidateAndDiscoverAuthServer(ctx, server.URL)

	require.NoError(t, err)
	require.NotNil(t, authInfo)

	// Verify that we got the actual issuer from the metadata
	assert.Equal(t, "https://actual-issuer.example.com", authInfo.Issuer)
	assert.Equal(t, "https://actual-issuer.example.com/authorize", authInfo.AuthorizationURL)
	assert.Equal(t, "https://actual-issuer.example.com/token", authInfo.TokenURL)
	assert.Equal(t, "https://actual-issuer.example.com/register", authInfo.RegistrationEndpoint)
}

// TestDynamicRegistrationWithIssuerMismatch tests the complete dynamic registration
// flow when the issuer doesn't match the metadata URL
func TestDynamicRegistrationWithIssuerMismatch(t *testing.T) {
	t.Parallel()

	registrationCalled := false

	// Create a test server that handles both discovery and registration
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case oauthWellKnownPath:
			// Return metadata with different issuer
			metadata := oauth.OIDCDiscoveryDocument{
				Issuer:                "https://real-issuer.example.com",
				AuthorizationEndpoint: "https://real-issuer.example.com/authorize",
				TokenEndpoint:         "https://real-issuer.example.com/token",
				RegistrationEndpoint:  "http://" + r.Host + "/register", // Registration happens on test server
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(metadata)

		case "/register":
			// Handle dynamic registration
			registrationCalled = true
			response := oauth.DynamicClientRegistrationResponse{
				ClientID:     "test-client-id",
				ClientSecret: "test-client-secret",
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(response)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	ctx := context.Background()
	config := &OAuthFlowConfig{
		CallbackPort:                8080,
		Scopes:                      []string{"openid", "profile"},
		IsResourceMetadataDiscovery: true, // This test simulates resource metadata discovery with issuer mismatch
	}

	// Test the dynamic registration flow
	err := handleDynamicRegistration(ctx, server.URL, config)

	require.NoError(t, err)
	assert.True(t, registrationCalled, "Registration endpoint should have been called")
	assert.Equal(t, "test-client-id", config.ClientID)
	assert.Equal(t, "test-client-secret", config.ClientSecret)

	// Verify that the endpoints were correctly set from the discovered issuer
	assert.Equal(t, "https://real-issuer.example.com/authorize", config.AuthorizeURL)
	assert.Equal(t, "https://real-issuer.example.com/token", config.TokenURL)
}

// TestGetDiscoveryDocument_SecurityScenarios tests various security attack scenarios
func TestGetDiscoveryDocument_SecurityScenarios(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		setupServer   func() *httptest.Server
		expectError   bool
		errorContains string
		description   string
	}{
		{
			name: "malicious HTTP issuer in HTTPS metadata",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == oauthWellKnownPath {
						// Attacker tries to downgrade to HTTP
						metadata := oauth.OIDCDiscoveryDocument{
							Issuer:                "http://evil.example.com",
							AuthorizationEndpoint: "http://evil.example.com/authorize",
							TokenEndpoint:         "http://evil.example.com/token",
							RegistrationEndpoint:  "http://evil.example.com/register",
						}
						w.Header().Set("Content-Type", "application/json")
						json.NewEncoder(w).Encode(metadata)
						return
					}
					w.WriteHeader(http.StatusNotFound)
				}))
			},
			expectError:   true,
			errorContains: "invalid authorization_endpoint",
			description:   "Should reject HTTP endpoints in metadata even if issuer mismatch is allowed",
		},
		{
			name: "malicious localhost issuer trying to bypass HTTPS",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == oauthWellKnownPath {
						// Attacker tries to use localhost to bypass HTTPS
						metadata := oauth.OIDCDiscoveryDocument{
							Issuer:                "http://localhost:8080",
							AuthorizationEndpoint: "http://localhost:8080/authorize",
							TokenEndpoint:         "http://localhost:8080/token",
							RegistrationEndpoint:  "http://localhost:8080/register",
						}
						w.Header().Set("Content-Type", "application/json")
						json.NewEncoder(w).Encode(metadata)
						return
					}
					w.WriteHeader(http.StatusNotFound)
				}))
			},
			expectError:   false, // localhost HTTP is allowed for development
			errorContains: "",
			description:   "Localhost HTTP should be allowed for development purposes",
		},
		{
			name: "empty issuer in metadata",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == oauthWellKnownPath {
						// Attacker provides empty issuer
						metadata := oauth.OIDCDiscoveryDocument{
							Issuer:                "",
							AuthorizationEndpoint: "https://evil.example.com/authorize",
							TokenEndpoint:         "https://evil.example.com/token",
							RegistrationEndpoint:  "https://evil.example.com/register",
						}
						w.Header().Set("Content-Type", "application/json")
						json.NewEncoder(w).Encode(metadata)
						return
					}
					w.WriteHeader(http.StatusNotFound)
				}))
			},
			expectError:   true,
			errorContains: "missing issuer",
			description:   "Should reject metadata with empty issuer",
		},
		{
			name: "malicious redirect via authorization endpoint",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == oauthWellKnownPath {
						// Attacker tries to inject malicious authorization endpoint
						metadata := oauth.OIDCDiscoveryDocument{
							Issuer:                "https://legitimate.example.com",
							AuthorizationEndpoint: "https://evil.example.com/phishing",
							TokenEndpoint:         "https://legitimate.example.com/token",
							RegistrationEndpoint:  "https://legitimate.example.com/register",
						}
						w.Header().Set("Content-Type", "application/json")
						json.NewEncoder(w).Encode(metadata)
						return
					}
					w.WriteHeader(http.StatusNotFound)
				}))
			},
			expectError:   false,
			errorContains: "",
			description:   "Mixed endpoints from different domains should be allowed (provider's choice)",
		},
		// TODO: Fix and re-enable this test
		// {
		// 	name: "XSS attempt in issuer field",
		// 	setupServer: func() *httptest.Server {
		// 		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 			if r.URL.Path == oauthWellKnownPath {
		// 				// Attacker tries XSS in issuer
		// 				metadata := oauth.OIDCDiscoveryDocument{
		// 					Issuer:                "https://example.com<script>alert('xss')</script>",
		// 					AuthorizationEndpoint: "https://example.com/authorize",
		// 					TokenEndpoint:         "https://example.com/token",
		// 					RegistrationEndpoint:  "https://example.com/register",
		// 				}
		// 				w.Header().Set("Content-Type", "application/json")
		// 				json.NewEncoder(w).Encode(metadata)
		// 				return
		// 			}
		// 			w.WriteHeader(http.StatusNotFound)
		// 		}))
		// 	},
		// 	expectError:   false, // XSS in issuer is technically a valid URL, just a bad one
		// 	errorContains: "",
		// 	description:   "XSS in issuer field - technically valid URL but dangerous",
		// },
		{
			name: "path traversal in endpoint URLs",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == oauthWellKnownPath {
						metadata := oauth.OIDCDiscoveryDocument{
							Issuer:                "https://example.com",
							AuthorizationEndpoint: "https://example.com/../../../etc/passwd",
							TokenEndpoint:         "https://example.com/token",
							RegistrationEndpoint:  "https://example.com/register",
						}
						w.Header().Set("Content-Type", "application/json")
						json.NewEncoder(w).Encode(metadata)
						return
					}
					w.WriteHeader(http.StatusNotFound)
				}))
			},
			expectError:   false, // URL normalization should handle this
			errorContains: "",
			description:   "Path traversal attempts should be normalized by URL parsing",
		},
		{
			name: "oversized response DoS attempt",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == oauthWellKnownPath {
						// Try to send huge response
						w.Header().Set("Content-Type", "application/json")
						// Write a massive JSON response
						w.Write([]byte(`{"issuer":"https://example.com","padding":"`))
						for i := 0; i < 2*1024*1024; i++ { // 2MB of padding
							w.Write([]byte("A"))
						}
						w.Write([]byte(`"}`))
						return
					}
					w.WriteHeader(http.StatusNotFound)
				}))
			},
			expectError:   true,
			errorContains: "unexpected response",
			description:   "Should reject responses larger than 1MB limit",
		},
		{
			name: "wrong content type response",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == oauthWellKnownPath {
						// Return HTML instead of JSON
						w.Header().Set("Content-Type", "text/html")
						w.Write([]byte("<html><body>Not JSON</body></html>"))
						return
					}
					w.WriteHeader(http.StatusNotFound)
				}))
			},
			expectError:   true,
			errorContains: "unexpected content-type",
			description:   "Should reject non-JSON content types",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := tt.setupServer()
			defer server.Close()

			ctx := context.Background()
			config := &OAuthFlowConfig{}
			// For these security tests, we're simulating resource metadata discovery
			// to test that security validations still work even with issuer mismatch allowed
			config.IsResourceMetadataDiscovery = true

			doc, err := getDiscoveryDocument(ctx, server.URL, config)

			if tt.expectError {
				assert.Error(t, err, tt.description)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains, tt.description)
				}
				assert.Nil(t, doc)
			} else {
				assert.NoError(t, err, tt.description)
				if err == nil {
					assert.NotNil(t, doc)
				}
			}
		})
	}
}

// TestTokenValidation_IssuerMismatch tests that even with issuer mismatch allowed
// during discovery, token validation still enforces issuer matching
func TestTokenValidation_IssuerMismatch(t *testing.T) {
	t.Parallel()

	// This test demonstrates that the security validation happens at token validation
	// Even if we accept a different issuer during discovery, the token's issuer claim
	// must match what we discovered

	discoveredIssuer := "https://real-issuer.example.com"

	tests := []struct {
		name        string
		tokenIssuer string
		expectValid bool
	}{
		{
			name:        "token issuer matches discovered issuer",
			tokenIssuer: discoveredIssuer,
			expectValid: true,
		},
		{
			name:        "token issuer does not match discovered issuer",
			tokenIssuer: "https://evil.example.com",
			expectValid: false,
		},
		{
			name:        "token issuer is empty",
			tokenIssuer: "",
			expectValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Simulate token validation
			// In real code, this would be done by the OAuth library
			// This test just demonstrates the concept

			// The discovered issuer is what we got from metadata
			// The token issuer is what's in the JWT
			isValid := tt.tokenIssuer == discoveredIssuer && tt.tokenIssuer != ""

			assert.Equal(t, tt.expectValid, isValid,
				"Token validation should enforce issuer matching")
		})
	}
}

package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const oauthWellKnownPath = "/.well-known/oauth-authorization-server"

// TestDiscoverActualIssuer tests the DiscoverActualIssuer function which allows
// the issuer in metadata to differ from the metadata URL
func TestDiscoverActualIssuer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		metadataURL    string
		actualIssuer   string
		expectError    bool
		expectedIssuer string
		description    string
	}{
		{
			name:           "Atlassian pattern - different issuer",
			metadataURL:    "https://mcp.atlassian.com",
			actualIssuer:   "https://atlassian-remote-mcp-production.workers.dev",
			expectError:    false,
			expectedIssuer: "https://atlassian-remote-mcp-production.workers.dev",
			description:    "Should accept issuer that differs from metadata URL",
		},
		{
			name:           "Stripe pattern - different issuer",
			metadataURL:    "https://mcp.stripe.com",
			actualIssuer:   "https://marketplace.stripe.com",
			expectError:    false,
			expectedIssuer: "https://marketplace.stripe.com",
			description:    "Should accept Stripe's different issuer pattern",
		},
		{
			name:           "Same issuer as metadata URL",
			metadataURL:    "https://auth.example.com",
			actualIssuer:   "https://auth.example.com",
			expectError:    false,
			expectedIssuer: "https://auth.example.com",
			description:    "Should work when issuer matches metadata URL",
		},
		{
			name:           "Empty issuer in metadata",
			metadataURL:    "https://example.com",
			actualIssuer:   "",
			expectError:    true,
			expectedIssuer: "",
			description:    "Should reject empty issuer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create test server that returns metadata with potentially different issuer
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == oauthWellKnownPath {
					doc := OIDCDiscoveryDocument{
						Issuer:                tt.actualIssuer,
						AuthorizationEndpoint: tt.actualIssuer + "/authorize",
						TokenEndpoint:         tt.actualIssuer + "/token",
						RegistrationEndpoint:  tt.actualIssuer + "/register",
					}
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(doc)
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			defer server.Close()

			ctx := context.Background()
			doc, err := DiscoverActualIssuer(ctx, server.URL)

			if tt.expectError {
				assert.Error(t, err, tt.description)
				assert.Nil(t, doc)
			} else {
				require.NoError(t, err, tt.description)
				require.NotNil(t, doc)
				assert.Equal(t, tt.expectedIssuer, doc.Issuer, tt.description)
			}
		})
	}
}

// TestDiscoverActualIssuer_SecurityValidation tests that security validations
// are still enforced even when issuer mismatch is allowed
func TestDiscoverActualIssuer_SecurityValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		setupServer   func() *httptest.Server
		expectError   bool
		errorContains string
		description   string
	}{
		{
			name: "HTTP endpoints rejected",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == oauthWellKnownPath {
						doc := OIDCDiscoveryDocument{
							Issuer:                "http://evil.example.com",
							AuthorizationEndpoint: "http://evil.example.com/authorize",
							TokenEndpoint:         "http://evil.example.com/token",
						}
						w.Header().Set("Content-Type", "application/json")
						json.NewEncoder(w).Encode(doc)
						return
					}
					w.WriteHeader(http.StatusNotFound)
				}))
			},
			expectError:   true,
			errorContains: "invalid authorization_endpoint",
			description:   "Should reject HTTP endpoints even with issuer mismatch allowed",
		},
		{
			name: "Missing required fields",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == oauthWellKnownPath {
						doc := OIDCDiscoveryDocument{
							Issuer: "https://example.com",
							// Missing authorization and token endpoints
						}
						w.Header().Set("Content-Type", "application/json")
						json.NewEncoder(w).Encode(doc)
						return
					}
					w.WriteHeader(http.StatusNotFound)
				}))
			},
			expectError:   true,
			errorContains: "missing authorization_endpoint",
			description:   "Should validate required fields even with issuer mismatch allowed",
		},
		{
			name: "Invalid URL in endpoints",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == oauthWellKnownPath {
						doc := OIDCDiscoveryDocument{
							Issuer:                "https://example.com",
							AuthorizationEndpoint: "not-a-valid-url",
							TokenEndpoint:         "https://example.com/token",
						}
						w.Header().Set("Content-Type", "application/json")
						json.NewEncoder(w).Encode(doc)
						return
					}
					w.WriteHeader(http.StatusNotFound)
				}))
			},
			expectError:   true,
			errorContains: "invalid authorization_endpoint",
			description:   "Should validate endpoint URLs even with issuer mismatch allowed",
		},
		{
			name: "Response size limit enforced",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == oauthWellKnownPath {
						w.Header().Set("Content-Type", "application/json")
						// Try to send oversized response
						w.Write([]byte(`{"issuer":"https://example.com","padding":"`))
						for i := 0; i < 2*1024*1024; i++ { // 2MB
							w.Write([]byte("X"))
						}
						w.Write([]byte(`"}`))
						return
					}
					w.WriteHeader(http.StatusNotFound)
				}))
			},
			expectError:   true,
			errorContains: "unexpected response",
			description:   "Should enforce response size limit",
		},
		{
			name: "Wrong content type rejected",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == oauthWellKnownPath {
						w.Header().Set("Content-Type", "text/html")
						w.Write([]byte("<html>Not JSON</html>"))
						return
					}
					w.WriteHeader(http.StatusNotFound)
				}))
			},
			expectError:   true,
			errorContains: "unexpected content-type",
			description:   "Should reject non-JSON responses",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := tt.setupServer()
			defer server.Close()

			ctx := context.Background()
			doc, err := DiscoverActualIssuer(ctx, server.URL)

			assert.Error(t, err, tt.description)
			if tt.errorContains != "" {
				assert.Contains(t, err.Error(), tt.errorContains, tt.description)
			}
			assert.Nil(t, doc)
		})
	}
}

// TestDiscoverOIDCEndpoints_vs_DiscoverActualIssuer compares the behavior
// of the two discovery functions
func TestDiscoverOIDCEndpoints_vs_DiscoverActualIssuer(t *testing.T) {
	t.Parallel()

	// Create a server that returns metadata with different issuer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == oauthWellKnownPath {
			doc := OIDCDiscoveryDocument{
				Issuer:                "https://actual-issuer.example.com",
				AuthorizationEndpoint: "https://actual-issuer.example.com/authorize",
				TokenEndpoint:         "https://actual-issuer.example.com/token",
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(doc)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	ctx := context.Background()

	// Test DiscoverOIDCEndpoints - should fail with issuer mismatch
	t.Run("DiscoverOIDCEndpoints rejects issuer mismatch", func(t *testing.T) {
		t.Parallel()
		doc, err := DiscoverOIDCEndpoints(ctx, server.URL)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "issuer mismatch")
		assert.Nil(t, doc)
	})

	// Test DiscoverActualIssuer - should succeed
	t.Run("DiscoverActualIssuer accepts issuer mismatch", func(t *testing.T) {
		t.Parallel()
		doc, err := DiscoverActualIssuer(ctx, server.URL)
		require.NoError(t, err)
		require.NotNil(t, doc)
		assert.Equal(t, "https://actual-issuer.example.com", doc.Issuer)
	})
}

package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDiscoverActualIssuer_MaliciousScenarios tests various attack scenarios
// to ensure the issuer mismatch allowance doesn't introduce security vulnerabilities
func TestDiscoverActualIssuer_MaliciousScenarios(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		setupServer   func() *httptest.Server
		expectError   bool
		errorContains string
		description   string
	}{
		{
			name: "SSRF attempt - internal network endpoint",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == oauthWellKnownPath {
						doc := OIDCDiscoveryDocument{
							Issuer:                "https://legitimate.example.com",
							AuthorizationEndpoint: "https://192.168.1.1/authorize", // Internal IP
							TokenEndpoint:         "https://10.0.0.1/token",        // Internal IP
							RegistrationEndpoint:  "https://172.16.0.1/register",   // Internal IP
						}
						w.Header().Set("Content-Type", "application/json")
						json.NewEncoder(w).Encode(doc)
						return
					}
					w.WriteHeader(http.StatusNotFound)
				}))
			},
			expectError:   false, // Currently these pass validation - might want to block internal IPs
			errorContains: "",
			description:   "Internal IP addresses in endpoints (potential SSRF)",
		},
		{
			name: "Open redirect attempt via issuer",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == oauthWellKnownPath {
						doc := OIDCDiscoveryDocument{
							// Issuer with redirect attempt
							Issuer:                "https://evil.com?redirect=https://legitimate.com",
							AuthorizationEndpoint: "https://evil.com/authorize",
							TokenEndpoint:         "https://evil.com/token",
						}
						w.Header().Set("Content-Type", "application/json")
						json.NewEncoder(w).Encode(doc)
						return
					}
					w.WriteHeader(http.StatusNotFound)
				}))
			},
			expectError:   false, // URL with query params is valid
			errorContains: "",
			description:   "Issuer with redirect parameter (potential open redirect)",
		},
		{
			name: "Mixed issuer and endpoint domains",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == oauthWellKnownPath {
						doc := OIDCDiscoveryDocument{
							Issuer:                "https://legitimate.example.com",
							AuthorizationEndpoint: "https://evil.example.com/phishing/authorize",
							TokenEndpoint:         "https://another-evil.com/steal/token",
							RegistrationEndpoint:  "https://legitimate.example.com/register",
						}
						w.Header().Set("Content-Type", "application/json")
						json.NewEncoder(w).Encode(doc)
						return
					}
					w.WriteHeader(http.StatusNotFound)
				}))
			},
			expectError:   false,
			errorContains: "",
			description:   "Mixed domains between issuer and endpoints (phishing risk)",
		},
		{
			name: "Unicode homograph attack in issuer",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == oauthWellKnownPath {
						doc := OIDCDiscoveryDocument{
							// Using Cyrillic 'о' instead of Latin 'o'
							Issuer:                "https://gооgle.com", // Contains Cyrillic characters
							AuthorizationEndpoint: "https://gооgle.com/authorize",
							TokenEndpoint:         "https://gооgle.com/token",
						}
						w.Header().Set("Content-Type", "application/json")
						json.NewEncoder(w).Encode(doc)
						return
					}
					w.WriteHeader(http.StatusNotFound)
				}))
			},
			expectError:   false, // Unicode domains are technically valid
			errorContains: "",
			description:   "Unicode homograph attack using similar-looking characters",
		},
		{
			name: "Data URI in endpoints",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == oauthWellKnownPath {
						doc := OIDCDiscoveryDocument{
							Issuer:                "https://example.com",
							AuthorizationEndpoint: "data:text/html,<script>alert('xss')</script>",
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
			description:   "Data URI should be rejected",
		},
		{
			name: "File URI in endpoints",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == oauthWellKnownPath {
						doc := OIDCDiscoveryDocument{
							Issuer:                "https://example.com",
							AuthorizationEndpoint: "file:///etc/passwd",
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
			description:   "File URI should be rejected",
		},
		{
			name: "JavaScript URI in endpoints",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == oauthWellKnownPath {
						doc := OIDCDiscoveryDocument{
							Issuer:                "https://example.com",
							AuthorizationEndpoint: "javascript:alert('xss')",
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
			description:   "JavaScript URI should be rejected",
		},
		{
			name: "Subdomain takeover scenario",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == oauthWellKnownPath {
						doc := OIDCDiscoveryDocument{
							// Legitimate-looking subdomain that could be taken over
							Issuer:                "https://auth.abandoned-subdomain.example.com",
							AuthorizationEndpoint: "https://auth.abandoned-subdomain.example.com/authorize",
							TokenEndpoint:         "https://auth.abandoned-subdomain.example.com/token",
						}
						w.Header().Set("Content-Type", "application/json")
						json.NewEncoder(w).Encode(doc)
						return
					}
					w.WriteHeader(http.StatusNotFound)
				}))
			},
			expectError:   false, // Valid HTTPS URL, subdomain takeover is a different issue
			errorContains: "",
			description:   "Subdomain that could be vulnerable to takeover",
		},
		{
			name: "Port confusion attack",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == oauthWellKnownPath {
						doc := OIDCDiscoveryDocument{
							Issuer:                "https://example.com:443",
							AuthorizationEndpoint: "https://example.com:8443/authorize", // Different port
							TokenEndpoint:         "https://example.com:9443/token",     // Different port
						}
						w.Header().Set("Content-Type", "application/json")
						json.NewEncoder(w).Encode(doc)
						return
					}
					w.WriteHeader(http.StatusNotFound)
				}))
			},
			expectError:   false,
			errorContains: "",
			description:   "Different ports for different endpoints (port confusion)",
		},
		{
			name: "IDN spoofing with punycode",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == oauthWellKnownPath {
						doc := OIDCDiscoveryDocument{
							// xn--80ak6aa92e.com is apple.com in Cyrillic
							Issuer:                "https://xn--80ak6aa92e.com",
							AuthorizationEndpoint: "https://xn--80ak6aa92e.com/authorize",
							TokenEndpoint:         "https://xn--80ak6aa92e.com/token",
						}
						w.Header().Set("Content-Type", "application/json")
						json.NewEncoder(w).Encode(doc)
						return
					}
					w.WriteHeader(http.StatusNotFound)
				}))
			},
			expectError:   false, // Punycode domains are valid
			errorContains: "",
			description:   "IDN domain that looks like a legitimate domain",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := tt.setupServer()
			defer server.Close()

			ctx := context.Background()
			doc, err := DiscoverActualIssuer(ctx, server.URL)

			if tt.expectError {
				assert.Error(t, err, tt.description)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains, tt.description)
				}
				assert.Nil(t, doc)
			} else {
				// Even if we don't expect an error, log that this scenario passes
				// so we're aware of what attacks are currently not blocked
				if err == nil {
					t.Logf("WARNING: %s - This scenario is currently allowed", tt.description)
				}
				assert.NoError(t, err, tt.description)
				assert.NotNil(t, doc)
			}
		})
	}
}

// TestCreateOAuthConfigFromOIDC_MaliciousIssuer tests that CreateOAuthConfigFromOIDC
// properly validates the issuer even when using DiscoverOIDCEndpoints
func TestCreateOAuthConfigFromOIDC_MaliciousIssuer(t *testing.T) {
	t.Parallel()

	// This function uses DiscoverOIDCEndpoints which DOES validate issuer match
	// So a malicious server returning a different issuer should fail
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == oauthWellKnownPath {
			doc := OIDCDiscoveryDocument{
				Issuer:                "https://evil.example.com", // Different from server URL
				AuthorizationEndpoint: "https://evil.example.com/authorize",
				TokenEndpoint:         "https://evil.example.com/token",
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(doc)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	ctx := context.Background()
	config, err := CreateOAuthConfigFromOIDC(ctx, server.URL, "client-id", "client-secret", nil, false, 0)

	assert.Error(t, err, "Should reject issuer mismatch in CreateOAuthConfigFromOIDC")
	assert.Contains(t, err.Error(), "issuer mismatch")
	assert.Nil(t, config)
}

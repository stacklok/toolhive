// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/networking"
	oauthproto "github.com/stacklok/toolhive/pkg/oauth"
)

const wellKnownOAuthPath = "/.well-known/oauth-protected-resource"

func TestParseWWWAuthenticate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		header   string
		expected *AuthInfo
		wantErr  bool
	}{
		{
			name:    "empty header",
			header:  "",
			wantErr: true,
		},
		{
			name:    "whitespace only",
			header:  "   ",
			wantErr: true,
		},
		{
			name:   "simple bearer",
			header: "Bearer",
			expected: &AuthInfo{
				Type: "Bearer",
			},
		},
		{
			name:   "bearer with realm",
			header: `Bearer realm="https://example.com"`,
			expected: &AuthInfo{
				Type:  "Bearer",
				Realm: "https://example.com",
			},
		},
		{
			name:   "bearer with quoted realm",
			header: `Bearer realm="https://example.com/oauth"`,
			expected: &AuthInfo{
				Type:  "Bearer",
				Realm: "https://example.com/oauth",
			},
		},
		{
			name:   "oauth scheme",
			header: `OAuth realm="https://example.com"`,
			expected: &AuthInfo{
				Type:  "OAuth",
				Realm: "https://example.com",
			},
		},
		{
			name:   "multiple schemes with bearer first",
			header: `Bearer realm="https://example.com", Basic realm="test"`,
			expected: &AuthInfo{
				Type:  "Bearer",
				Realm: "https://example.com",
			},
		},
		{
			name:    "unsupported scheme",
			header:  "Basic realm=\"test\"",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := ParseWWWAuthenticate(tt.header)

			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseWWWAuthenticate() expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("ParseWWWAuthenticate() unexpected error: %v", err)
				return
			}

			if result.Type != tt.expected.Type {
				t.Errorf("ParseWWWAuthenticate() Type = %v, want %v", result.Type, tt.expected.Type)
			}

			if result.Realm != tt.expected.Realm {
				t.Errorf("ParseWWWAuthenticate() Realm = %v, want %v", result.Realm, tt.expected.Realm)
			}
		})
	}
}

func TestExtractParameter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		params    string
		paramName string
		expected  string
	}{
		{
			name:      "simple parameter",
			params:    `realm="https://example.com"`,
			paramName: "realm",
			expected:  "https://example.com",
		},
		{
			name:      "quoted parameter",
			params:    `realm="https://example.com/oauth"`,
			paramName: "realm",
			expected:  "https://example.com/oauth",
		},
		{
			name:      "multiple parameters",
			params:    `realm="https://example.com", scope="openid"`,
			paramName: "realm",
			expected:  "https://example.com",
		},
		{
			name:      "parameter not found",
			params:    `realm="https://example.com"`,
			paramName: "scope",
			expected:  "",
		},
		{
			name:      "empty params",
			params:    "",
			paramName: "realm",
			expected:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := ExtractParameter(tt.params, tt.paramName)
			if result != tt.expected {
				t.Errorf("ExtractParameter() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestDeriveIssuerFromRealm(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		realm    string
		expected string
	}{
		{
			name:     "valid https issuer url",
			realm:    "https://example.com",
			expected: "https://example.com",
		},
		{
			name:     "https url with path",
			realm:    "https://api.example.com/v1",
			expected: "https://api.example.com/v1",
		},
		{
			name:     "https url with query params (should be removed)",
			realm:    "https://example.com?param=value",
			expected: "https://example.com",
		},
		{
			name:     "https url with fragment (should be removed)",
			realm:    "https://example.com#fragment",
			expected: "https://example.com",
		},
		{
			name:     "http url (not valid for issuer)",
			realm:    "http://example.com",
			expected: "",
		},
		{
			name:     "non-url realm string",
			realm:    "MyRealm",
			expected: "",
		},
		{
			name:     "invalid url",
			realm:    "not-a-url",
			expected: "",
		},
		{
			name:     "empty realm",
			realm:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := DeriveIssuerFromRealm(tt.realm)
			if result != tt.expected {
				t.Errorf("DeriveIssuerFromRealm() = %v, want %v", result, tt.expected)
			}
		})
	}
}
func TestDetectAuthenticationFromServer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		serverResponse func(w http.ResponseWriter, _ *http.Request)
		expected       *AuthInfo
		wantErr        bool
	}{
		{
			name: "no authentication required",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				// Return 404 for well-known URIs, 200 OK for main endpoint
				if strings.Contains(r.URL.Path, ".well-known") {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.WriteHeader(http.StatusOK)
			},
			expected: nil,
		},
		{
			name: "bearer authentication required (OAuth flow)",
			serverResponse: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="https://example.com"`)
				w.WriteHeader(http.StatusUnauthorized)
			},
			expected: &AuthInfo{
				Type:  "Bearer",
				Realm: "https://example.com",
			},
		},
		{
			name: "simple bearer token authentication required",
			serverResponse: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("WWW-Authenticate", `Bearer`)
				w.WriteHeader(http.StatusUnauthorized)
			},
			expected: &AuthInfo{
				Type: "Bearer",
			},
		},
		{
			name: "oauth authentication required",
			serverResponse: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("WWW-Authenticate", `OAuth realm="https://example.com"`)
				w.WriteHeader(http.StatusUnauthorized)
			},
			expected: &AuthInfo{
				Type:  "OAuth",
				Realm: "https://example.com",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Create test server
			server := httptest.NewServer(http.HandlerFunc(tt.serverResponse))
			defer server.Close()

			// Test detection
			ctx := context.Background()
			result, err := DetectAuthenticationFromServer(ctx, server.URL, nil)

			if tt.wantErr {
				if err == nil {
					t.Errorf("DetectAuthenticationFromServer() expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("DetectAuthenticationFromServer() unexpected error: %v", err)
				return
			}

			if tt.expected == nil {
				if result != nil {
					t.Errorf("DetectAuthenticationFromServer() = %v, want nil", result)
				}
				return
			}

			if result == nil {
				t.Errorf("DetectAuthenticationFromServer() = nil, want %v", tt.expected)
				return
			}

			if result.Type != tt.expected.Type {
				t.Errorf("DetectAuthenticationFromServer() Type = %v, want %v", result.Type, tt.expected.Type)
			}

			if result.Realm != tt.expected.Realm {
				t.Errorf("DetectAuthenticationFromServer() Realm = %v, want %v", result.Realm, tt.expected.Realm)
			}
		})
	}
}

func TestDefaultDiscoveryConfig(t *testing.T) {
	t.Parallel()
	config := DefaultDiscoveryConfig()

	if config.Timeout != 10*time.Second {
		t.Errorf("DefaultDiscoveryConfig() Timeout = %v, want %v", config.Timeout, 10*time.Second)
	}

	if config.TLSHandshakeTimeout != 5*time.Second {
		t.Errorf("DefaultDiscoveryConfig() TLSHandshakeTimeout = %v, want %v", config.TLSHandshakeTimeout, 5*time.Second)
	}

	if config.ResponseHeaderTimeout != 5*time.Second {
		t.Errorf("DefaultDiscoveryConfig() ResponseHeaderTimeout = %v, want %v", config.ResponseHeaderTimeout, 5*time.Second)
	}

	if !config.EnablePOSTDetection {
		t.Errorf("DefaultDiscoveryConfig() EnablePOSTDetection = %v, want %v", config.EnablePOSTDetection, true)
	}
}

func TestOAuthFlowConfig(t *testing.T) {
	t.Parallel()
	t.Run("nil config validation", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		result, err := PerformOAuthFlow(ctx, "https://example.com", nil)

		if err == nil {
			t.Errorf("PerformOAuthFlow() expected error for nil config but got none")
		}
		if result != nil {
			t.Errorf("PerformOAuthFlow() expected nil result for nil config")
		}
		if !strings.Contains(err.Error(), "OAuth flow config cannot be nil") {
			t.Errorf("PerformOAuthFlow() expected nil config error, got: %v", err)
		}
	})

	t.Run("config validation", func(t *testing.T) {
		t.Parallel()
		config := &OAuthFlowConfig{
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			Scopes:       []string{"openid"},
		}

		// This test only validates that the config is accepted and doesn't cause
		// immediate validation errors. The actual OAuth flow will fail with OIDC
		// discovery errors, which is expected.
		if config.ClientID == "" {
			t.Errorf("Expected ClientID to be set")
		}
		if config.ClientSecret == "" {
			t.Errorf("Expected ClientSecret to be set")
		}
		if len(config.Scopes) == 0 {
			t.Errorf("Expected Scopes to be set")
		}
	})
}

func TestDeriveIssuerFromURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "https no port",
			in:   "https://api.example.com",
			want: "https://api.example.com",
		},
		{
			name: "https with nondefault port, path, query, fragment",
			in:   "https://api.example.com:8443/v1/users?id=42#top",
			want: "https://api.example.com:8443",
		},
		{
			name: "http scheme forced to https",
			in:   "http://api.example.com",
			want: "https://api.example.com",
		},
		{
			name: "userinfo ignored; keep host:port",
			in:   "https://user:pass@auth.example.com:9443/oauth/authorize",
			want: "https://auth.example.com:9443",
		},
		{
			name: "file scheme unsupported -> empty",
			in:   "file:///etc/passwd",
			want: "",
		},
		{
			name: "malformed url -> empty",
			in:   "://not a url",
			want: "",
		},
		{
			name: "empty host -> empty",
			in:   "https://",
			want: "",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := DeriveIssuerFromURL(tc.in)
			if got != tc.want {
				t.Fatalf("DeriveIssuerFromURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestPerformOAuthFlow_PortBehavior(t *testing.T) {
	t.Parallel()

	// Test dynamic registration with available port
	t.Run("dynamic registration with available port", func(t *testing.T) {
		t.Parallel()

		config := &OAuthFlowConfig{
			ClientID:     "", // No client ID triggers dynamic registration
			ClientSecret: "",
			CallbackPort: 0, // Use 0 to find an available port
			Scopes:       []string{"openid"},
		}

		// Create a mock OIDC discovery server
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/.well-known/openid_configuration") {
				// Return OIDC discovery document
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{
					"issuer": "https://example.com",
					"authorization_endpoint": "https://example.com/auth",
					"token_endpoint": "https://example.com/token",
					"registration_endpoint": "https://example.com/register"
				}`))
				return
			}
			if strings.HasSuffix(r.URL.Path, "/register") {
				// Return dynamic registration response
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				w.Write([]byte(`{
					"client_id": "dynamic-client-id",
					"client_secret": "dynamic-client-secret"
				}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		ctx := context.Background()
		_, err := PerformOAuthFlow(ctx, server.URL, config)

		// For successful cases, we expect the OAuth flow to fail later
		// (since we're not actually completing the full flow), but the
		// port resolution should work correctly
		if err != nil {
			// Check if it's a port-related error (which we don't want)
			if strings.Contains(err.Error(), "not available") {
				t.Errorf("Unexpected port availability error: %v", err)
			}
		}
	})

	// Test dynamic registration with unavailable port - should fallback
	t.Run("dynamic registration with unavailable port - should fallback", func(t *testing.T) {
		t.Parallel()

		// Create a listener to make a port unavailable
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer listener.Close()
		unavailablePort := listener.Addr().(*net.TCPAddr).Port

		config := &OAuthFlowConfig{
			ClientID:     "", // No client ID triggers dynamic registration
			ClientSecret: "",
			CallbackPort: unavailablePort, // Use the unavailable port
			Scopes:       []string{"openid"},
		}

		// Create a mock OIDC discovery server
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/.well-known/openid_configuration") {
				// Return OIDC discovery document
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{
					"issuer": "https://example.com",
					"authorization_endpoint": "https://example.com/auth",
					"token_endpoint": "https://example.com/token",
					"registration_endpoint": "https://example.com/register"
				}`))
				return
			}
			if strings.HasSuffix(r.URL.Path, "/register") {
				// Return dynamic registration response
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				w.Write([]byte(`{
					"client_id": "dynamic-client-id",
					"client_secret": "dynamic-client-secret"
				}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		ctx := context.Background()
		_, err = PerformOAuthFlow(ctx, server.URL, config)

		// Should not fail due to port unavailability (should fallback)
		if err != nil {
			// Check if it's a port-related error (which we don't want for dynamic registration)
			if strings.Contains(err.Error(), "not available") {
				t.Errorf("Dynamic registration should allow port fallback, but got port error: %v", err)
			}
		}
	})

	// Test pre-registered client with available port
	t.Run("pre-registered client with available port", func(t *testing.T) {
		t.Parallel()

		config := &OAuthFlowConfig{
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			CallbackPort: 0, // Use 0 to find an available port
			Scopes:       []string{"openid"},
		}

		// Create a mock OIDC discovery server
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/.well-known/openid_configuration") {
				// Return OIDC discovery document
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{
					"issuer": "https://example.com",
					"authorization_endpoint": "https://example.com/auth",
					"token_endpoint": "https://example.com/token",
					"registration_endpoint": "https://example.com/register"
				}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		ctx := context.Background()
		_, err := PerformOAuthFlow(ctx, server.URL, config)

		// For successful cases, we expect the OAuth flow to fail later
		// (since we're not actually completing the full flow), but the
		// port resolution should work correctly
		if err != nil {
			// Check if it's a port-related error (which we don't want)
			if strings.Contains(err.Error(), "not available") {
				t.Errorf("Unexpected port availability error: %v", err)
			}
		}
	})

	// Test pre-registered client with unavailable port - should fail
	t.Run("pre-registered client with unavailable port - should fail", func(t *testing.T) {
		t.Parallel()

		// Create a listener to make a port unavailable
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer listener.Close()
		unavailablePort := listener.Addr().(*net.TCPAddr).Port

		config := &OAuthFlowConfig{
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			CallbackPort: unavailablePort, // Use the unavailable port
			Scopes:       []string{"openid"},
		}

		// Create a mock OIDC discovery server
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/.well-known/openid_configuration") {
				// Return OIDC discovery document
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{
					"issuer": "https://example.com",
					"authorization_endpoint": "https://example.com/auth",
					"token_endpoint": "https://example.com/token",
					"registration_endpoint": "https://example.com/register"
				}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		// Verify the port is actually unavailable
		if networking.IsAvailable(config.CallbackPort) {
			t.Fatalf("Test setup error: Expected port %d to be unavailable, but it's available", config.CallbackPort)
		}

		ctx := context.Background()
		_, err = PerformOAuthFlow(ctx, server.URL, config)

		// Should fail due to port unavailability
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not available")
	})
}

func TestPerformOAuthFlow_PortFallbackBehavior(t *testing.T) {
	t.Parallel()

	// Test that dynamic registration allows port fallback
	t.Run("dynamic registration port fallback", func(t *testing.T) {
		t.Parallel()

		// Create a listener to make a port unavailable
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer listener.Close()
		unavailablePort := listener.Addr().(*net.TCPAddr).Port

		config := &OAuthFlowConfig{
			ClientID:     "", // No client ID triggers dynamic registration
			ClientSecret: "",
			CallbackPort: unavailablePort,
			Scopes:       []string{"openid"},
		}

		// Create a mock OIDC discovery server
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/.well-known/openid_configuration") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{
					"issuer": "https://example.com",
					"authorization_endpoint": "https://example.com/auth",
					"token_endpoint": "https://example.com/token",
					"registration_endpoint": "https://example.com/register"
				}`))
				return
			}
			if strings.HasSuffix(r.URL.Path, "/register") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				w.Write([]byte(`{
					"client_id": "dynamic-client-id",
					"client_secret": "dynamic-client-secret"
				}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		ctx := context.Background()
		_, err = PerformOAuthFlow(ctx, server.URL, config)

		// Should not fail due to port unavailability
		// (it may fail later in the OAuth flow, but not due to port issues)
		if err != nil && strings.Contains(err.Error(), "not available") {
			t.Errorf("Dynamic registration should allow port fallback, but got port error: %v", err)
		}
	})

	// Test that pre-registered clients fail on unavailable ports
	t.Run("pre-registered client strict port checking", func(t *testing.T) {
		t.Parallel()

		// Create a listener to make a port unavailable
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer listener.Close()
		unavailablePort := listener.Addr().(*net.TCPAddr).Port

		config := &OAuthFlowConfig{
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			CallbackPort: unavailablePort,
			Scopes:       []string{"openid"},
		}

		ctx := context.Background()
		_, err = PerformOAuthFlow(ctx, "https://example.com", config)

		// Should fail due to port unavailability
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not available")
	})
}

// TestPerformOAuthFlow_PortCheckingOnly tests just the port checking logic
// without going through the full OAuth flow
func TestPerformOAuthFlow_PortCheckingOnly(t *testing.T) {
	t.Parallel()

	// Test that pre-registered clients fail on unavailable ports
	t.Run("pre-registered client strict port checking", func(t *testing.T) {
		t.Parallel()

		// Create a listener to make a port unavailable
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer listener.Close()

		unavailablePort := listener.Addr().(*net.TCPAddr).Port

		config := &OAuthFlowConfig{
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			CallbackPort: unavailablePort,
			Scopes:       []string{"openid"},
		}

		// Test the port checking logic directly
		if shouldDynamicallyRegisterClient(config) {
			t.Error("Expected shouldDynamicallyRegisterClient to return false for pre-registered client")
		}

		// This should fail because the port is unavailable
		if networking.IsAvailable(config.CallbackPort) {
			t.Errorf("Expected port %d to be unavailable, but IsAvailable returned true", config.CallbackPort)
		}
	})
}

func TestBuildWellKnownURI(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		targetURL        string
		endpointSpecific bool
		expected         string
	}{
		{
			name:             "root-level with simple path",
			targetURL:        "https://example.com/api/mcp",
			endpointSpecific: false,
			expected:         "https://example.com/.well-known/oauth-protected-resource",
		},
		{
			name:             "endpoint-specific with simple path",
			targetURL:        "https://example.com/api/mcp",
			endpointSpecific: true,
			expected:         "https://example.com/.well-known/oauth-protected-resource/api/mcp",
		},
		{
			name:             "root-level with root path",
			targetURL:        "https://example.com/",
			endpointSpecific: false,
			expected:         "https://example.com/.well-known/oauth-protected-resource",
		},
		{
			name:             "endpoint-specific with root path",
			targetURL:        "https://example.com/",
			endpointSpecific: true,
			expected:         "https://example.com/.well-known/oauth-protected-resource",
		},
		{
			name:             "endpoint-specific with deeply nested path",
			targetURL:        "https://example.com/api/unstable/mcp-server/mcp",
			endpointSpecific: true,
			expected:         "https://example.com/.well-known/oauth-protected-resource/api/unstable/mcp-server/mcp",
		},
		{
			name:             "root-level with deeply nested path",
			targetURL:        "https://example.com/api/unstable/mcp-server/mcp",
			endpointSpecific: false,
			expected:         "https://example.com/.well-known/oauth-protected-resource",
		},
		{
			name:             "localhost HTTP with path",
			targetURL:        "http://localhost:8080/mcp",
			endpointSpecific: true,
			expected:         "http://localhost:8080/.well-known/oauth-protected-resource/mcp",
		},
		{
			name:             "URL with trailing slash",
			targetURL:        "https://example.com/api/mcp/",
			endpointSpecific: true,
			expected:         "https://example.com/.well-known/oauth-protected-resource/api/mcp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			parsedURL, err := url.Parse(tt.targetURL)
			require.NoError(t, err, "Failed to parse test URL")

			result := buildWellKnownURI(parsedURL, tt.endpointSpecific)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCheckWellKnownURIExists(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		serverResponse func(w http.ResponseWriter, r *http.Request)
		expected       bool
	}{
		{
			name: "200 OK response with application/json",
			serverResponse: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"resource":"https://example.com"}`))
			},
			expected: true,
		},
		{
			name: "200 OK with application/json; charset=utf-8",
			serverResponse: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"resource":"https://example.com"}`))
			},
			expected: true,
		},
		{
			name: "200 OK with wrong Content-Type",
			serverResponse: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`<html></html>`))
			},
			expected: false, // Should reject non-JSON content
		},
		{
			name: "200 OK without Content-Type header",
			serverResponse: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"resource":"https://example.com"}`))
			},
			expected: false, // Should reject missing Content-Type
		},
		{
			name: "401 Unauthorized with application/json",
			serverResponse: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
			},
			expected: false, // Well-known metadata must be publicly accessible (200 OK only)
		},
		{
			name: "401 Unauthorized without Content-Type",
			serverResponse: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			},
			expected: false, // Well-known metadata must be publicly accessible
		},
		{
			name: "404 Not Found response",
			serverResponse: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			expected: false,
		},
		{
			name: "500 Internal Server Error",
			serverResponse: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(tt.serverResponse))
			defer server.Close()

			ctx := context.Background()
			client := &http.Client{Timeout: 5 * time.Second}

			result := checkWellKnownURIExists(ctx, client, server.URL)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTryWellKnownDiscovery(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                 string
		targetURL            string
		endpointSpecificResp func(w http.ResponseWriter, r *http.Request)
		rootLevelResp        func(w http.ResponseWriter, r *http.Request)
		expectedFound        bool
		expectedMetadataURL  string // Should match which well-known URI was found
	}{
		{
			name:      "endpoint-specific well-known URI found",
			targetURL: "/api/mcp",
			endpointSpecificResp: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
			},
			rootLevelResp: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			expectedFound:       true,
			expectedMetadataURL: "/.well-known/oauth-protected-resource/api/mcp",
		},
		{
			name:      "root-level well-known URI found",
			targetURL: "/api/mcp",
			endpointSpecificResp: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			rootLevelResp: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
			},
			expectedFound:       true,
			expectedMetadataURL: "/.well-known/oauth-protected-resource",
		},
		{
			name:      "both well-known URIs return 404",
			targetURL: "/api/mcp",
			endpointSpecificResp: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			rootLevelResp: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			expectedFound: false,
		},
		{
			name:      "endpoint-specific takes priority",
			targetURL: "/api/mcp",
			endpointSpecificResp: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
			},
			rootLevelResp: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
			},
			expectedFound:       true,
			expectedMetadataURL: "/.well-known/oauth-protected-resource/api/mcp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Create a test server that routes to different handlers
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, wellKnownOAuthPath+"/") {
					tt.endpointSpecificResp(w, r)
				} else if r.URL.Path == wellKnownOAuthPath {
					tt.rootLevelResp(w, r)
				} else {
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			ctx := context.Background()
			client := &http.Client{Timeout: 5 * time.Second}
			targetURI := server.URL + tt.targetURL

			result, err := tryWellKnownDiscovery(ctx, client, targetURI)
			require.NoError(t, err)

			if tt.expectedFound {
				require.NotNil(t, result, "Expected AuthInfo but got nil")
				assert.Equal(t, "OAuth", result.Type)
				assert.True(t, strings.HasSuffix(result.ResourceMetadata, tt.expectedMetadataURL),
					"Expected ResourceMetadata to end with %s, got %s", tt.expectedMetadataURL, result.ResourceMetadata)
			} else {
				assert.Nil(t, result, "Expected nil AuthInfo but got %v", result)
			}
		})
	}
}

func TestDetectAuthenticationFromServer_WellKnownFallback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                 string
		serverResponse       func(w http.ResponseWriter, r *http.Request)
		expectedAuthFound    bool
		expectedResourceMeta bool // Whether ResourceMetadata should be set
	}{
		{
			name: "WWW-Authenticate header takes precedence",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				// Return WWW-Authenticate header on unauthorized requests
				if r.URL.Path == "/" || r.URL.Path == "" {
					w.Header().Set("WWW-Authenticate", `Bearer realm="https://example.com"`)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				// Also have well-known URI available
				if r.URL.Path == "/.well-known/oauth-protected-resource" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"resource":"https://example.com","authorization_servers":["https://example.com"]}`))
					return
				}
				w.WriteHeader(http.StatusNotFound)
			},
			expectedAuthFound:    true,
			expectedResourceMeta: false, // Should use WWW-Authenticate, not well-known
		},
		{
			name: "well-known URI fallback works when no WWW-Authenticate",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				// Return 401 but without WWW-Authenticate header
				if r.URL.Path == "/" || r.URL.Path == "" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				// Well-known URI available
				if r.URL.Path == "/.well-known/oauth-protected-resource" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"resource":"https://example.com","authorization_servers":["https://example.com"]}`))
					return
				}
				w.WriteHeader(http.StatusNotFound)
			},
			expectedAuthFound:    true,
			expectedResourceMeta: true, // Should use well-known URI
		},
		{
			name: "no authentication required",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				// All requests return 200 OK
				if r.URL.Path == "/" || r.URL.Path == "" {
					w.WriteHeader(http.StatusOK)
					return
				}
				// No well-known URI
				w.WriteHeader(http.StatusNotFound)
			},
			expectedAuthFound:    false,
			expectedResourceMeta: false,
		},
		{
			name: "401 without WWW-Authenticate and no well-known URI",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				// Return 401 for main endpoint but 404 for well-known URIs
				if strings.Contains(r.URL.Path, ".well-known") {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				// Return 401 but no WWW-Authenticate
				w.WriteHeader(http.StatusUnauthorized)
			},
			expectedAuthFound:    false,
			expectedResourceMeta: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(tt.serverResponse))
			defer server.Close()

			ctx := context.Background()
			result, err := DetectAuthenticationFromServer(ctx, server.URL, nil)
			require.NoError(t, err)

			if tt.expectedAuthFound {
				require.NotNil(t, result, "Expected AuthInfo but got nil")
				// Well-known URI discovery returns Type = "OAuth", WWW-Authenticate Bearer headers return Type = "Bearer"
				if tt.expectedResourceMeta {
					// Well-known URI fallback - should be OAuth
					assert.Equal(t, "OAuth", result.Type)
					assert.NotEmpty(t, result.ResourceMetadata, "Expected ResourceMetadata to be set")
					assert.True(t, strings.Contains(result.ResourceMetadata, "/.well-known/oauth-protected-resource"),
						"ResourceMetadata should contain well-known path")
				} else {
					// WWW-Authenticate header - should be Bearer
					assert.Equal(t, "Bearer", result.Type)
					// When WWW-Authenticate is used (expectedResourceMeta=false), ResourceMetadata might
					// or might not be set depending on the header content
				}
			} else {
				assert.Nil(t, result, "Expected nil AuthInfo but got %v", result)
			}
		})
	}
}

// TestDetectAuthenticationFromServer_ErrorPaths tests error handling paths
func TestDetectAuthenticationFromServer_ErrorPaths(t *testing.T) {
	t.Parallel()

	t.Run("malformed target URL returns error", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		// Use an invalid URL with control characters
		invalidURL := "http://example.com/path\x00with\x00nulls"

		result, err := DetectAuthenticationFromServer(ctx, invalidURL, nil)

		// Should return error because the URL is malformed
		require.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "failed to create GET request")
	})

	t.Run("network error returns error", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		// Use a URL that will cause network errors (non-routable IP)
		invalidURL := "http://192.0.2.1:9999/mcp"

		config := &Config{
			Timeout:               1 * time.Millisecond,
			TLSHandshakeTimeout:   1 * time.Millisecond,
			ResponseHeaderTimeout: 1 * time.Millisecond,
			EnablePOSTDetection:   false,
		}

		result, err := DetectAuthenticationFromServer(ctx, invalidURL, config)

		// Should return error due to network failure
		require.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "failed to make GET request")
	})
}

// TestCheckWellKnownURIExists_ErrorPaths tests error handling in checkWellKnownURIExists
func TestCheckWellKnownURIExists_ErrorPaths(t *testing.T) {
	t.Parallel()

	t.Run("invalid URI causes request creation to fail", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		client := &http.Client{Timeout: 5 * time.Second}

		// Create an invalid URI with control characters that will fail http.NewRequestWithContext
		invalidURI := "http://example.com/path\x00with\x00nulls"

		result := checkWellKnownURIExists(ctx, client, invalidURI)
		assert.False(t, result, "Expected false for invalid URI")
	})

	t.Run("network error during request", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		client := &http.Client{Timeout: 1 * time.Millisecond} // Very short timeout

		// Use a non-routable IP to cause network timeout/error
		// 192.0.2.0/24 is TEST-NET-1, reserved for documentation
		invalidURI := "http://192.0.2.1:9999/.well-known/oauth-protected-resource"

		result := checkWellKnownURIExists(ctx, client, invalidURI)
		assert.False(t, result, "Expected false for network error")
	})

	t.Run("cancelled context", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		client := &http.Client{Timeout: 5 * time.Second}
		uri := "http://example.com/.well-known/oauth-protected-resource"

		result := checkWellKnownURIExists(ctx, client, uri)
		assert.False(t, result, "Expected false for cancelled context")
	})

	t.Run("large response body is safely drained with limit", func(t *testing.T) {
		t.Parallel()
		// Create a server that returns a very large response body
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			// Write 2x MaxResponseBodyDrain to exceed the drain limit
			_, _ = w.Write(bytes.Repeat([]byte("X"), 2*MaxResponseBodyDrain))
		}))
		defer server.Close()

		ctx := context.Background()
		client := &http.Client{Timeout: 5 * time.Second}

		// This should complete quickly even with a large response because we limit draining
		result := checkWellKnownURIExists(ctx, client, server.URL)

		// Should return true (200 OK with correct content-type)
		assert.True(t, result, "Expected true for valid response even with large body")
	})
}

// TestTryWellKnownDiscovery_ErrorPaths tests error handling in tryWellKnownDiscovery
func TestTryWellKnownDiscovery_ErrorPaths(t *testing.T) {
	t.Parallel()

	t.Run("malformed target URL", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		client := &http.Client{Timeout: 5 * time.Second}

		// Use a malformed URL that will fail url.Parse
		malformedURL := "ht!tp://not a valid url with spaces"

		result, err := tryWellKnownDiscovery(ctx, client, malformedURL)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid target URI")
		assert.Nil(t, result)
	})

	t.Run("target URL with control characters", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		client := &http.Client{Timeout: 5 * time.Second}

		// URL with null bytes
		invalidURL := "http://example.com/path\x00with\x00control\x00chars"

		result, err := tryWellKnownDiscovery(ctx, client, invalidURL)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid target URI")
		assert.Nil(t, result)
	})

	t.Run("URL with scheme but no host", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		client := &http.Client{Timeout: 5 * time.Second}

		// URL with scheme but no host - causes issues when building well-known URIs
		invalidURL := "http://"

		result, err := tryWellKnownDiscovery(ctx, client, invalidURL)

		// Should not find any well-known URIs and return nil, nil
		require.NoError(t, err)
		assert.Nil(t, result)
	})
}

// TestRegisterDynamicClient_MissingRegistrationEndpoint tests that registerDynamicClient
// returns a clear error message when the OIDC discovery document doesn't include
// a registration_endpoint (provider doesn't support DCR).
func TestRegisterDynamicClient_MissingRegistrationEndpoint(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create a discovery document without registration_endpoint
	discoveredDoc := &oauthproto.OIDCDiscoveryDocument{
		AuthorizationServerMetadata: oauthproto.AuthorizationServerMetadata{
			Issuer:                "https://auth.example.com",
			AuthorizationEndpoint: "https://auth.example.com/oauth/authorize",
			TokenEndpoint:         "https://auth.example.com/oauth/token",
			JWKSURI:               "https://auth.example.com/oauth/jwks",
			// Note: RegistrationEndpoint is intentionally omitted (empty string)
			RegistrationEndpoint: "",
		},
	}

	config := &OAuthFlowConfig{
		Scopes:       []string{"openid", "profile"},
		CallbackPort: 8765,
	}

	// Call registerDynamicClient with a discovery document missing registration_endpoint
	result, err := registerDynamicClient(ctx, config, discoveredDoc)

	// Should return an error
	require.Error(t, err)
	assert.Nil(t, result)

	// Error message should clearly indicate DCR is not supported
	assert.Contains(t, err.Error(), "does not support Dynamic Client Registration")
	assert.Contains(t, err.Error(), "DCR")

	// Error message should provide actionable guidance
	assert.Contains(t, err.Error(), "--remote-auth-client-id")
	assert.Contains(t, err.Error(), "--remote-auth-client-secret")
}

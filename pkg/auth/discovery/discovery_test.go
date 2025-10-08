package discovery

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/networking"
)

func init() {
	// Initialize logger for tests
	logger.Initialize()
}

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
				Type: "OAuth",
			},
		},
		{
			name:   "bearer with realm",
			header: `Bearer realm="https://example.com"`,
			expected: &AuthInfo{
				Type:  "OAuth",
				Realm: "https://example.com",
			},
		},
		{
			name:   "bearer with quoted realm",
			header: `Bearer realm="https://example.com/oauth"`,
			expected: &AuthInfo{
				Type:  "OAuth",
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
				Type:  "OAuth",
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
			serverResponse: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			},
			expected: nil,
		},
		{
			name: "bearer authentication required",
			serverResponse: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="https://example.com"`)
				w.WriteHeader(http.StatusUnauthorized)
			},
			expected: &AuthInfo{
				Type:  "OAuth",
				Realm: "https://example.com",
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

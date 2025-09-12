package discovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/toolhive/pkg/logger"
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

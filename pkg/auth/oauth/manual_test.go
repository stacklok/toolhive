package oauth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateOAuthConfigManual(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		clientID     string
		clientSecret string
		authURL      string
		tokenURL     string
		scopes       []string
		usePKCE      bool
		callbackPort int
		expectError  bool
		errorMsg     string
		validate     func(t *testing.T, config *Config)
	}{
		{
			name:         "valid config with all parameters",
			clientID:     "test-client",
			clientSecret: "test-secret",
			authURL:      "https://example.com/oauth/authorize",
			tokenURL:     "https://example.com/oauth/token",
			scopes:       []string{"read", "write"},
			usePKCE:      true,
			callbackPort: 8080,
			expectError:  false,
			validate: func(t *testing.T, config *Config) {
				t.Helper()
				assert.Equal(t, "test-client", config.ClientID)
				assert.Equal(t, "test-secret", config.ClientSecret)
				assert.Equal(t, "https://example.com/oauth/authorize", config.AuthURL)
				assert.Equal(t, "https://example.com/oauth/token", config.TokenURL)
				assert.Equal(t, []string{"read", "write"}, config.Scopes)
				assert.True(t, config.UsePKCE)
				assert.Equal(t, 8080, config.CallbackPort)
			},
		},
		{
			name:         "valid config without client secret (PKCE flow)",
			clientID:     "test-client",
			clientSecret: "",
			authURL:      "https://example.com/oauth/authorize",
			tokenURL:     "https://example.com/oauth/token",
			scopes:       []string{"read"},
			usePKCE:      true,
			callbackPort: 0,
			expectError:  false,
			validate: func(t *testing.T, config *Config) {
				t.Helper()
				assert.Equal(t, "test-client", config.ClientID)
				assert.Equal(t, "", config.ClientSecret)
				assert.Equal(t, []string{"read"}, config.Scopes)
				assert.True(t, config.UsePKCE)
				assert.Equal(t, 0, config.CallbackPort)
			},
		},
		{
			name:         "valid config with empty scopes (OAuth default)",
			clientID:     "test-client",
			clientSecret: "test-secret",
			authURL:      "https://example.com/oauth/authorize",
			tokenURL:     "https://example.com/oauth/token",
			scopes:       nil, // Should default to empty for OAuth
			usePKCE:      false,
			callbackPort: 8666,
			expectError:  false,
			validate: func(t *testing.T, config *Config) {
				t.Helper()
				assert.Equal(t, []string{}, config.Scopes)
				assert.False(t, config.UsePKCE)
			},
		},
		{
			name:         "localhost URLs allowed for development",
			clientID:     "test-client",
			clientSecret: "test-secret",
			authURL:      "http://localhost:8080/oauth/authorize",
			tokenURL:     "http://localhost:8080/oauth/token",
			scopes:       []string{"read"},
			usePKCE:      true,
			callbackPort: 8080,
			expectError:  false,
			validate: func(t *testing.T, config *Config) {
				t.Helper()
				assert.Equal(t, "http://localhost:8080/oauth/authorize", config.AuthURL)
				assert.Equal(t, "http://localhost:8080/oauth/token", config.TokenURL)
			},
		},
		{
			name:         "127.0.0.1 URLs allowed for development",
			clientID:     "test-client",
			clientSecret: "test-secret",
			authURL:      "http://127.0.0.1:8080/oauth/authorize",
			tokenURL:     "http://127.0.0.1:8080/oauth/token",
			scopes:       []string{"read"},
			usePKCE:      true,
			callbackPort: 8080,
			expectError:  false,
			validate: func(t *testing.T, config *Config) {
				t.Helper()
				assert.Equal(t, "http://127.0.0.1:8080/oauth/authorize", config.AuthURL)
				assert.Equal(t, "http://127.0.0.1:8080/oauth/token", config.TokenURL)
			},
		},
		{
			name:         "valid config with OAuth parameters",
			clientID:     "test-client",
			clientSecret: "test-secret",
			authURL:      "https://example.com/oauth/authorize",
			tokenURL:     "https://example.com/oauth/token",
			scopes:       []string{"read", "write"},
			usePKCE:      true,
			callbackPort: 8080,
			expectError:  false,
			validate: func(t *testing.T, config *Config) {
				t.Helper()
				assert.Equal(t, "test-client", config.ClientID)
				assert.Equal(t, "test-secret", config.ClientSecret)
				assert.Equal(t, "https://example.com/oauth/authorize", config.AuthURL)
				assert.Equal(t, "https://example.com/oauth/token", config.TokenURL)
				assert.Equal(t, []string{"read", "write"}, config.Scopes)
				assert.True(t, config.UsePKCE)
				assert.Equal(t, 8080, config.CallbackPort)
				assert.Equal(t, map[string]string{"prompt": "select_account", "response_mode": "query"}, config.OAuthParams)
			},
		},
		{
			name:         "GitHub OAuth configuration",
			clientID:     "github-client-id",
			clientSecret: "github-client-secret",
			authURL:      "https://github.com/login/oauth/authorize",
			tokenURL:     "https://github.com/login/oauth/access_token",
			scopes:       []string{"repo", "user:email"},
			usePKCE:      true,
			callbackPort: 8666,
			expectError:  false,
			validate: func(t *testing.T, config *Config) {
				t.Helper()
				assert.Equal(t, "github-client-id", config.ClientID)
				assert.Equal(t, "github-client-secret", config.ClientSecret)
				assert.Equal(t, "https://github.com/login/oauth/authorize", config.AuthURL)
				assert.Equal(t, "https://github.com/login/oauth/access_token", config.TokenURL)
				assert.Equal(t, []string{"repo", "user:email"}, config.Scopes)
				assert.True(t, config.UsePKCE)
			},
		},
		// Error cases
		{
			name:         "missing client ID",
			clientID:     "",
			clientSecret: "test-secret",
			authURL:      "https://example.com/oauth/authorize",
			tokenURL:     "https://example.com/oauth/token",
			scopes:       []string{"read"},
			usePKCE:      true,
			callbackPort: 8080,
			expectError:  true,
			errorMsg:     "client ID is required",
		},
		{
			name:         "missing authorization URL",
			clientID:     "test-client",
			clientSecret: "test-secret",
			authURL:      "",
			tokenURL:     "https://example.com/oauth/token",
			scopes:       []string{"read"},
			usePKCE:      true,
			callbackPort: 8080,
			expectError:  true,
			errorMsg:     "authorization URL is required",
		},
		{
			name:         "missing token URL",
			clientID:     "test-client",
			clientSecret: "test-secret",
			authURL:      "https://example.com/oauth/authorize",
			tokenURL:     "",
			scopes:       []string{"read"},
			usePKCE:      true,
			callbackPort: 8080,
			expectError:  true,
			errorMsg:     "token URL is required",
		},
		{
			name:         "invalid authorization URL",
			clientID:     "test-client",
			clientSecret: "test-secret",
			authURL:      "not-a-url",
			tokenURL:     "https://example.com/oauth/token",
			scopes:       []string{"read"},
			usePKCE:      true,
			callbackPort: 8080,
			expectError:  true,
			errorMsg:     "invalid authorization URL",
		},
		{
			name:         "invalid token URL",
			clientID:     "test-client",
			clientSecret: "test-secret",
			authURL:      "https://example.com/oauth/authorize",
			tokenURL:     "not-a-url",
			scopes:       []string{"read"},
			usePKCE:      true,
			callbackPort: 8080,
			expectError:  true,
			errorMsg:     "invalid token URL",
		},
		{
			name:         "non-HTTPS authorization URL (security check)",
			clientID:     "test-client",
			clientSecret: "test-secret",
			authURL:      "http://example.com/oauth/authorize",
			tokenURL:     "https://example.com/oauth/token",
			scopes:       []string{"read"},
			usePKCE:      true,
			callbackPort: 8080,
			expectError:  true,
			errorMsg:     "invalid authorization URL",
		},
		{
			name:         "non-HTTPS token URL (security check)",
			clientID:     "test-client",
			clientSecret: "test-secret",
			authURL:      "https://example.com/oauth/authorize",
			tokenURL:     "http://example.com/oauth/token",
			scopes:       []string{"read"},
			usePKCE:      true,
			callbackPort: 8080,
			expectError:  true,
			errorMsg:     "invalid token URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Prepare OAuth parameters for the specific test case
			var oauthParams map[string]string
			if tt.name == "valid config with OAuth parameters" {
				oauthParams = map[string]string{
					"prompt":        "select_account",
					"response_mode": "query",
				}
			}

			config, err := CreateOAuthConfigManual(
				tt.clientID,
				tt.clientSecret,
				tt.authURL,
				tt.tokenURL,
				tt.scopes,
				tt.usePKCE,
				tt.callbackPort,
				oauthParams,
			)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
				assert.Nil(t, config)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, config)

			// Common validations for all successful cases
			assert.Equal(t, tt.clientID, config.ClientID)
			assert.Equal(t, tt.clientSecret, config.ClientSecret)
			assert.Equal(t, tt.authURL, config.AuthURL)
			assert.Equal(t, tt.tokenURL, config.TokenURL)
			assert.Equal(t, tt.usePKCE, config.UsePKCE)
			assert.Equal(t, tt.callbackPort, config.CallbackPort)

			if tt.validate != nil {
				tt.validate(t, config)
			}
		})
	}
}

func TestCreateOAuthConfigManual_ScopeDefaultBehavior(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		scopes   []string
		expected []string
	}{
		{
			name:     "nil scopes should default to empty",
			scopes:   nil,
			expected: []string{},
		},
		{
			name:     "empty slice should remain empty",
			scopes:   []string{},
			expected: []string{},
		},
		{
			name:     "provided scopes should be preserved",
			scopes:   []string{"read", "write", "admin"},
			expected: []string{"read", "write", "admin"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config, err := CreateOAuthConfigManual(
				"test-client",
				"test-secret",
				"https://example.com/oauth/authorize",
				"https://example.com/oauth/token",
				tt.scopes,
				true,
				8080,
				nil, // No OAuth params for basic tests
			)

			require.NoError(t, err)
			require.NotNil(t, config)
			assert.Equal(t, tt.expected, config.Scopes)
		})
	}
}

func TestCreateOAuthConfigManual_PKCEBehavior(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		usePKCE  bool
		expected bool
	}{
		{
			name:     "PKCE enabled",
			usePKCE:  true,
			expected: true,
		},
		{
			name:     "PKCE disabled",
			usePKCE:  false,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config, err := CreateOAuthConfigManual(
				"test-client",
				"test-secret",
				"https://example.com/oauth/authorize",
				"https://example.com/oauth/token",
				[]string{"read"},
				tt.usePKCE,
				8080,
				nil, // No OAuth params for basic tests
			)

			require.NoError(t, err)
			require.NotNil(t, config)
			assert.Equal(t, tt.expected, config.UsePKCE)
		})
	}
}

func TestCreateOAuthConfigManual_CallbackPortBehavior(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		port     int
		expected int
	}{
		{
			name:     "default port (0 means auto-select)",
			port:     0,
			expected: 0,
		},
		{
			name:     "custom port",
			port:     9000,
			expected: 9000,
		},
		{
			name:     "standard OAuth port",
			port:     8666,
			expected: 8666,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config, err := CreateOAuthConfigManual(
				"test-client",
				"test-secret",
				"https://example.com/oauth/authorize",
				"https://example.com/oauth/token",
				[]string{"read"},
				true,
				tt.port,
				nil, // No OAuth params for basic tests
			)

			require.NoError(t, err)
			require.NotNil(t, config)
			assert.Equal(t, tt.expected, config.CallbackPort)
		})
	}
}

func TestCreateOAuthConfigManual_OAuthParamsBehavior(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		oauthParams map[string]string
		expected    map[string]string
	}{
		{
			name:        "nil OAuth params",
			oauthParams: nil,
			expected:    nil,
		},
		{
			name:        "empty OAuth params",
			oauthParams: map[string]string{},
			expected:    map[string]string{},
		},
		{
			name: "GitHub-style OAuth params",
			oauthParams: map[string]string{
				"prompt": "select_account",
			},
			expected: map[string]string{
				"prompt": "select_account",
			},
		},
		{
			name: "multiple OAuth params",
			oauthParams: map[string]string{
				"prompt":        "select_account",
				"response_mode": "query",
				"access_type":   "offline",
			},
			expected: map[string]string{
				"prompt":        "select_account",
				"response_mode": "query",
				"access_type":   "offline",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config, err := CreateOAuthConfigManual(
				"test-client",
				"test-secret",
				"https://example.com/oauth/authorize",
				"https://example.com/oauth/token",
				[]string{"read"},
				true,
				8080,
				tt.oauthParams,
			)

			require.NoError(t, err)
			require.NotNil(t, config)
			assert.Equal(t, tt.expected, config.OAuthParams)
		})
	}
}

package resolver

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// mockEnvReader is a test implementation of env.Reader
type mockEnvReader struct {
	values map[string]string
}

func (m *mockEnvReader) Getenv(key string) string {
	if m.values == nil {
		return ""
	}
	return m.values[key]
}

func TestNewCLIAuthResolver(t *testing.T) {
	t.Parallel()

	envReader := &mockEnvReader{}
	resolver := NewCLIAuthResolver(envReader, "/some/config/dir")

	require.NotNil(t, resolver)
	assert.Equal(t, "/some/config/dir", resolver.configDir)
	assert.Equal(t, envReader, resolver.envReader)
}

func TestCLIAuthResolver_ResolveExternalAuthConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		refName        string
		configContent  string
		envVars        map[string]string
		wantErr        bool
		errContains    string
		validateResult func(t *testing.T, strategy *authtypes.BackendAuthStrategy)
	}{
		{
			name:        "empty ref name returns error",
			refName:     "",
			wantErr:     true,
			errContains: "external auth config ref name is empty",
		},
		{
			name:        "config file not found returns error",
			refName:     "non-existent",
			wantErr:     true,
			errContains: "config file not found",
		},
		{
			name:    "invalid yaml returns error",
			refName: "invalid-yaml",
			configContent: `
this is not valid yaml: [
`,
			wantErr:     true,
			errContains: "failed to parse YAML",
		},
		{
			name:    "unknown type returns error",
			refName: "unknown-type",
			configContent: `
type: unknown_type
`,
			wantErr:     true,
			errContains: "unknown auth type",
		},
		{
			name:    "token exchange missing client_secret_env returns error",
			refName: "token-exchange-no-secret",
			configContent: `
type: token_exchange
token_exchange:
  token_url: https://auth.example.com/token
  client_id: my-client
`,
			wantErr:     true,
			errContains: "client_secret_env is required",
		},
		{
			name:    "token exchange missing env var returns error",
			refName: "token-exchange-missing-env",
			configContent: `
type: token_exchange
token_exchange:
  token_url: https://auth.example.com/token
  client_id: my-client
  client_secret_env: MISSING_SECRET_VAR
`,
			envVars:     map[string]string{},
			wantErr:     true,
			errContains: "environment variable \"MISSING_SECRET_VAR\" is not set",
		},
		{
			name:    "token exchange success",
			refName: "token-exchange-success",
			configContent: `
type: token_exchange
token_exchange:
  token_url: https://auth.example.com/token
  client_id: my-client
  client_secret_env: MY_CLIENT_SECRET
  audience: https://api.example.com
  scopes:
    - read
    - write
`,
			envVars: map[string]string{
				"MY_CLIENT_SECRET": "super-secret-value",
			},
			wantErr: false,
			validateResult: func(t *testing.T, strategy *authtypes.BackendAuthStrategy) {
				t.Helper()
				assert.Equal(t, authtypes.StrategyTypeTokenExchange, strategy.Type)
				require.NotNil(t, strategy.TokenExchange)
				assert.Equal(t, "https://auth.example.com/token", strategy.TokenExchange.TokenURL)
				assert.Equal(t, "my-client", strategy.TokenExchange.ClientID)
				assert.Equal(t, "super-secret-value", strategy.TokenExchange.ClientSecret)
				assert.Equal(t, "https://api.example.com", strategy.TokenExchange.Audience)
				assert.Equal(t, []string{"read", "write"}, strategy.TokenExchange.Scopes)
			},
		},
		{
			name:    "header injection missing header_value_env returns error",
			refName: "header-injection-no-env",
			configContent: `
type: header_injection
header_injection:
  header_name: X-API-Key
`,
			wantErr:     true,
			errContains: "header_value_env is required",
		},
		{
			name:    "header injection missing env var returns error",
			refName: "header-injection-missing-env",
			configContent: `
type: header_injection
header_injection:
  header_name: X-API-Key
  header_value_env: MISSING_API_KEY
`,
			envVars:     map[string]string{},
			wantErr:     true,
			errContains: "environment variable \"MISSING_API_KEY\" is not set",
		},
		{
			name:    "header injection success",
			refName: "header-injection-success",
			configContent: `
type: header_injection
header_injection:
  header_name: X-API-Key
  header_value_env: MY_API_KEY
`,
			envVars: map[string]string{
				"MY_API_KEY": "api-key-12345",
			},
			wantErr: false,
			validateResult: func(t *testing.T, strategy *authtypes.BackendAuthStrategy) {
				t.Helper()
				assert.Equal(t, authtypes.StrategyTypeHeaderInjection, strategy.Type)
				require.NotNil(t, strategy.HeaderInjection)
				assert.Equal(t, "X-API-Key", strategy.HeaderInjection.HeaderName)
				assert.Equal(t, "api-key-12345", strategy.HeaderInjection.HeaderValue)
			},
		},
		{
			name:    "token exchange nil config returns error",
			refName: "token-exchange-nil",
			configContent: `
type: token_exchange
`,
			wantErr:     true,
			errContains: "token_exchange config is nil",
		},
		{
			name:    "header injection nil config returns error",
			refName: "header-injection-nil",
			configContent: `
type: header_injection
`,
			wantErr:     true,
			errContains: "header_injection config is nil",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Create temp directory for config files
			tempDir := t.TempDir()

			// Write config file if content is provided
			if tc.configContent != "" {
				configPath := filepath.Join(tempDir, tc.refName+".yaml")
				err := os.WriteFile(configPath, []byte(tc.configContent), 0600)
				require.NoError(t, err)
			}

			// Create resolver with mock env reader
			envReader := &mockEnvReader{values: tc.envVars}
			resolver := NewCLIAuthResolver(envReader, tempDir)

			strategy, err := resolver.ResolveExternalAuthConfig(context.Background(), tc.refName)

			if tc.wantErr {
				require.Error(t, err)
				assert.Nil(t, strategy)
				if tc.errContains != "" {
					assert.Contains(t, err.Error(), tc.errContains)
				}
			} else {
				require.NoError(t, err)
				require.NotNil(t, strategy)
				if tc.validateResult != nil {
					tc.validateResult(t, strategy)
				}
			}
		})
	}
}

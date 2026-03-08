// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/permissions"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/remote"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

func TestValidateBulkOperationRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		request bulkOperationRequest
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid with names only",
			request: bulkOperationRequest{
				Names: []string{"workload1", "workload2"},
			},
			wantErr: false,
		},
		{
			name: "valid with group only",
			request: bulkOperationRequest{
				Group: "test-group",
			},
			wantErr: false,
		},
		{
			name: "invalid - both names and group",
			request: bulkOperationRequest{
				Names: []string{"workload1"},
				Group: "test-group",
			},
			wantErr: true,
			errMsg:  "cannot specify both names and group",
		},
		{
			name:    "invalid - neither names nor group",
			request: bulkOperationRequest{},
			wantErr: true,
			errMsg:  "must specify either names or group",
		},
		{
			name: "invalid - empty names array",
			request: bulkOperationRequest{
				Names: []string{},
			},
			wantErr: true,
			errMsg:  "must specify either names or group",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateBulkOperationRequest(tt.request)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestRunConfigToCreateRequest(t *testing.T) {
	t.Parallel()

	t.Run("basic conversion", func(t *testing.T) {
		t.Parallel()

		runConfig := &runner.RunConfig{
			Name:           "test-workload",
			Image:          "test-image:latest",
			Host:           "localhost",
			Port:           3000,
			CmdArgs:        []string{"arg1", "arg2"},
			TargetPort:     8080,
			EnvVars:        map[string]string{"ENV1": "value1"},
			Secrets:        []string{"secret1,target=/path1", "secret2,target=/path2"},
			Volumes:        []string{"/host:/container"},
			Transport:      types.TransportTypeSSE,
			Group:          "test-group",
			ProxyMode:      types.ProxyModeSSE,
			IsolateNetwork: true,
			ToolsFilter:    []string{"tool1", "tool2"},
		}

		result := runConfigToCreateRequest(runConfig)

		require.NotNil(t, result)
		assert.Equal(t, "test-workload", result.Name)
		assert.Equal(t, "test-image:latest", result.Image)
		assert.Equal(t, "localhost", result.Host)
		assert.Equal(t, []string{"arg1", "arg2"}, result.CmdArguments)
		assert.Equal(t, 8080, result.TargetPort)
		assert.Equal(t, 3000, result.ProxyPort)
		assert.Equal(t, map[string]string{"ENV1": "value1"}, result.EnvVars)
		require.Len(t, result.Secrets, 2)
		assert.Equal(t, "secret1", result.Secrets[0].Name)
		assert.Equal(t, "/path1", result.Secrets[0].Target)
		assert.Equal(t, "secret2", result.Secrets[1].Name)
		assert.Equal(t, "/path2", result.Secrets[1].Target)
		assert.Equal(t, []string{"/host:/container"}, result.Volumes)
		assert.Equal(t, "sse", result.Transport)
		assert.Equal(t, "test-group", result.Group)
		assert.Equal(t, "sse", result.ProxyMode)
		assert.True(t, result.NetworkIsolation)
		assert.Equal(t, []string{"tool1", "tool2"}, result.ToolsFilter)
	})

	t.Run("with plaintext header forward", func(t *testing.T) {
		t.Parallel()

		runConfig := &runner.RunConfig{
			Name: "test-workload",
			HeaderForward: &runner.HeaderForwardConfig{
				AddPlaintextHeaders: map[string]string{
					"X-Custom-Header": "custom-value",
					"X-Tenant-ID":     "tenant-123",
				},
			},
		}

		result := runConfigToCreateRequest(runConfig)

		require.NotNil(t, result)
		require.NotNil(t, result.HeaderForward)
		assert.Equal(t, map[string]string{
			"X-Custom-Header": "custom-value",
			"X-Tenant-ID":     "tenant-123",
		}, result.HeaderForward.AddPlaintextHeaders)
		assert.Nil(t, result.HeaderForward.AddHeadersFromSecret)
	})

	t.Run("with secret-backed header forward", func(t *testing.T) {
		t.Parallel()

		runConfig := &runner.RunConfig{
			Name: "test-workload",
			HeaderForward: &runner.HeaderForwardConfig{
				AddHeadersFromSecret: map[string]string{
					"Authorization": "api-key-secret",
					"X-API-Key":     "another-secret",
				},
			},
		}

		result := runConfigToCreateRequest(runConfig)

		require.NotNil(t, result)
		require.NotNil(t, result.HeaderForward)
		assert.Nil(t, result.HeaderForward.AddPlaintextHeaders)
		assert.Equal(t, map[string]string{
			"Authorization": "api-key-secret",
			"X-API-Key":     "another-secret",
		}, result.HeaderForward.AddHeadersFromSecret)
	})

	t.Run("with both plaintext and secret header forward", func(t *testing.T) {
		t.Parallel()

		runConfig := &runner.RunConfig{
			Name: "test-workload",
			HeaderForward: &runner.HeaderForwardConfig{
				AddPlaintextHeaders: map[string]string{
					"X-Tenant-ID": "tenant-123",
				},
				AddHeadersFromSecret: map[string]string{
					"Authorization": "api-key-secret",
				},
			},
		}

		result := runConfigToCreateRequest(runConfig)

		require.NotNil(t, result)
		require.NotNil(t, result.HeaderForward)
		assert.Equal(t, "tenant-123", result.HeaderForward.AddPlaintextHeaders["X-Tenant-ID"])
		assert.Equal(t, "api-key-secret", result.HeaderForward.AddHeadersFromSecret["Authorization"])
	})

	t.Run("with OIDC config", func(t *testing.T) {
		t.Parallel()

		runConfig := &runner.RunConfig{
			Name: "test-workload",
			OIDCConfig: &auth.TokenValidatorConfig{
				Issuer:           "https://oidc.example.com",
				Audience:         "test-audience",
				JWKSURL:          "https://oidc.example.com/jwks",
				IntrospectionURL: "https://oidc.example.com/introspect",
				ClientID:         "test-client",
				ClientSecret:     "test-secret",
			},
		}

		result := runConfigToCreateRequest(runConfig)

		require.NotNil(t, result)
		assert.Equal(t, "https://oidc.example.com", result.OIDC.Issuer)
		assert.Equal(t, "test-audience", result.OIDC.Audience)
		assert.Equal(t, "https://oidc.example.com/jwks", result.OIDC.JwksURL)
		assert.Equal(t, "https://oidc.example.com/introspect", result.OIDC.IntrospectionURL)
		assert.Equal(t, "test-client", result.OIDC.ClientID)
		assert.Equal(t, "test-secret", result.OIDC.ClientSecret)
	})

	t.Run("with remote OAuth config", func(t *testing.T) {
		t.Parallel()

		runConfig := &runner.RunConfig{
			Name: "test-workload",
			RemoteAuthConfig: &remote.Config{
				Issuer:       "https://oauth.example.com",
				AuthorizeURL: "https://oauth.example.com/auth",
				TokenURL:     "https://oauth.example.com/token",
				ClientID:     "test-client",
				ClientSecret: "oauth-client-secret,target=oauth_secret",
				Scopes:       []string{"read", "write"},
				UsePKCE:      true,
				Resource:     "https://mcp.example.com",
				OAuthParams:  map[string]string{"custom": "param"},
				CallbackPort: 8081,
			},
		}

		result := runConfigToCreateRequest(runConfig)

		require.NotNil(t, result)
		require.NotNil(t, result.OAuthConfig)
		assert.Equal(t, "https://oauth.example.com", result.OAuthConfig.Issuer)
		assert.Equal(t, "https://oauth.example.com/auth", result.OAuthConfig.AuthorizeURL)
		assert.Equal(t, "https://oauth.example.com/token", result.OAuthConfig.TokenURL)
		assert.Equal(t, "test-client", result.OAuthConfig.ClientID)
		assert.Equal(t, []string{"read", "write"}, result.OAuthConfig.Scopes)
		assert.True(t, result.OAuthConfig.UsePKCE)
		assert.Equal(t, "https://mcp.example.com", result.OAuthConfig.Resource)
		assert.Equal(t, map[string]string{"custom": "param"}, result.OAuthConfig.OAuthParams)
		assert.Equal(t, 8081, result.OAuthConfig.CallbackPort)

		// Verify that secret is parsed correctly from CLI format
		require.NotNil(t, result.OAuthConfig.ClientSecret)
		assert.Equal(t, "oauth-client-secret", result.OAuthConfig.ClientSecret.Name)
		assert.Equal(t, "oauth_secret", result.OAuthConfig.ClientSecret.Target)
	})

	t.Run("with remote OAuth config without secret key (CLI case)", func(t *testing.T) {
		t.Parallel()

		runConfig := &runner.RunConfig{
			Name: "test-workload",
			RemoteAuthConfig: &remote.Config{
				Issuer:       "https://oauth.example.com",
				AuthorizeURL: "https://oauth.example.com/auth",
				TokenURL:     "https://oauth.example.com/token",
				ClientID:     "test-client",
				ClientSecret: "actual-secret-value", // Plain text secret (CLI case)
				Scopes:       []string{"read", "write"},
				UsePKCE:      true,
				OAuthParams:  map[string]string{"custom": "param"},
				CallbackPort: 8081,
			},
		}

		result := runConfigToCreateRequest(runConfig)

		require.NotNil(t, result)
		require.NotNil(t, result.OAuthConfig)
		assert.Equal(t, "test-client", result.OAuthConfig.ClientID)
		assert.True(t, result.OAuthConfig.UsePKCE)

		// When no secret key is stored (CLI case), ClientSecret should be nil
		assert.Nil(t, result.OAuthConfig.ClientSecret)
	})

	t.Run("with remote OAuth config with bearer token", func(t *testing.T) {
		t.Parallel()

		runConfig := &runner.RunConfig{
			Name: "test-workload",
			RemoteAuthConfig: &remote.Config{
				Issuer:      "https://oauth.example.com",
				ClientID:    "test-client",
				BearerToken: "bearer-token-secret,target=bearer_token",
				Scopes:      []string{"read", "write"},
			},
		}

		result := runConfigToCreateRequest(runConfig)

		require.NotNil(t, result)
		require.NotNil(t, result.OAuthConfig)
		assert.Equal(t, "test-client", result.OAuthConfig.ClientID)

		// Verify that bearer token is parsed correctly from CLI format
		require.NotNil(t, result.OAuthConfig.BearerToken)
		assert.Equal(t, "bearer-token-secret", result.OAuthConfig.BearerToken.Name)
		assert.Equal(t, "bearer_token", result.OAuthConfig.BearerToken.Target)
	})

	t.Run("with remote OAuth config with bearer token without secret key (CLI case)", func(t *testing.T) {
		t.Parallel()

		runConfig := &runner.RunConfig{
			Name: "test-workload",
			RemoteAuthConfig: &remote.Config{
				Issuer:      "https://oauth.example.com",
				ClientID:    "test-client",
				BearerToken: "actual-bearer-token-value", // Plain text token (CLI case)
				Scopes:      []string{"read", "write"},
			},
		}

		result := runConfigToCreateRequest(runConfig)

		require.NotNil(t, result)
		require.NotNil(t, result.OAuthConfig)
		assert.Equal(t, "test-client", result.OAuthConfig.ClientID)

		// When no secret key is stored (CLI case), BearerToken should be nil
		assert.Nil(t, result.OAuthConfig.BearerToken)
	})

	t.Run("with permission profile", func(t *testing.T) {
		t.Parallel()

		profile := &permissions.Profile{
			Name: "test-profile",
		}

		runConfig := &runner.RunConfig{
			Name:              "test-workload",
			PermissionProfile: profile,
		}

		result := runConfigToCreateRequest(runConfig)

		require.NotNil(t, result)
		assert.Equal(t, profile, result.PermissionProfile)
	})

	t.Run("with invalid secrets", func(t *testing.T) {
		t.Parallel()

		runConfig := &runner.RunConfig{
			Name:    "test-workload",
			Secrets: []string{"invalid-secret-format", "another-invalid"},
		}

		result := runConfigToCreateRequest(runConfig)

		require.NotNil(t, result)
		// Invalid secrets should be ignored, resulting in empty secrets array
		assert.Empty(t, result.Secrets)
	})

	t.Run("with tools override", func(t *testing.T) {
		t.Parallel()

		runConfig := &runner.RunConfig{
			Name: "test-workload",
			ToolsOverride: map[string]runner.ToolOverride{
				"fetch": {
					Name:        "fetch_custom",
					Description: "Custom fetch description",
				},
				"read": {
					Name: "read_file",
				},
			},
		}

		result := runConfigToCreateRequest(runConfig)

		require.NotNil(t, result)
		require.NotNil(t, result.ToolsOverride)
		assert.Len(t, result.ToolsOverride, 2)
		assert.Equal(t, "fetch_custom", result.ToolsOverride["fetch"].Name)
		assert.Equal(t, "Custom fetch description", result.ToolsOverride["fetch"].Description)
		assert.Equal(t, "read_file", result.ToolsOverride["read"].Name)
		assert.Empty(t, result.ToolsOverride["read"].Description)
	})

	t.Run("nil runConfig", func(t *testing.T) {
		t.Parallel()

		result := runConfigToCreateRequest(nil)
		assert.Nil(t, result)
	})
}

func TestCreateRequestToRemoteAuthConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		clientSecret         *secrets.SecretParameter
		bearerToken          *secrets.SecretParameter
		expectedClientSecret string
		expectedBearerToken  string
	}{
		{
			name: "with bearer token only",
			bearerToken: &secrets.SecretParameter{
				Name:   "bearer-token-secret",
				Target: "bearer_token",
			},
			expectedClientSecret: "",
			expectedBearerToken:  "bearer-token-secret,target=bearer_token",
		},
		{
			name: "with bearer token and client secret",
			clientSecret: &secrets.SecretParameter{
				Name:   "oauth-client-secret",
				Target: "oauth_secret",
			},
			bearerToken: &secrets.SecretParameter{
				Name:   "bearer-token-secret",
				Target: "bearer_token",
			},
			expectedClientSecret: "oauth-client-secret,target=oauth_secret",
			expectedBearerToken:  "bearer-token-secret,target=bearer_token",
		},
		{
			name:                 "without bearer token or client secret",
			clientSecret:         nil,
			bearerToken:          nil,
			expectedClientSecret: "",
			expectedBearerToken:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := &createRequest{
				updateRequest: updateRequest{
					URL: "https://example.com/mcp",
					OAuthConfig: remoteOAuthConfig{
						ClientID:     "test-client",
						ClientSecret: tt.clientSecret,
						BearerToken:  tt.bearerToken,
						Scopes:       []string{"read", "write"},
					},
				},
			}

			result := createRequestToRemoteAuthConfig(context.Background(), req)

			require.NotNil(t, result)
			assert.Equal(t, "test-client", result.ClientID)
			assert.Equal(t, []string{"read", "write"}, result.Scopes)
			assert.Equal(t, tt.expectedClientSecret, result.ClientSecret)
			assert.Equal(t, tt.expectedBearerToken, result.BearerToken)
		})
	}
}

func TestValidateHeaderForwardConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		config    *headerForwardConfig
		wantErr   bool
		errSubstr string
	}{
		{
			name: "valid config with plaintext headers",
			config: &headerForwardConfig{
				AddPlaintextHeaders: map[string]string{
					"X-Custom-Header": "value",
					"X-Tenant-ID":     "tenant-123",
				},
			},
			wantErr: false,
		},
		{
			name: "valid config with secret headers",
			config: &headerForwardConfig{
				AddHeadersFromSecret: map[string]string{
					"X-API-Key":     "api-key-secret",
					"Authorization": "auth-secret",
				},
			},
			wantErr: false,
		},
		{
			name:    "nil config is valid",
			config:  nil,
			wantErr: false,
		},
		{
			name:    "empty config is valid",
			config:  &headerForwardConfig{},
			wantErr: false,
		},
		{
			name: "restricted header Host rejected in plaintext",
			config: &headerForwardConfig{
				AddPlaintextHeaders: map[string]string{
					"Host": "evil.com",
				},
			},
			wantErr:   true,
			errSubstr: "restricted",
		},
		{
			name: "restricted header Host rejected in secrets",
			config: &headerForwardConfig{
				AddHeadersFromSecret: map[string]string{
					"Host": "host-secret",
				},
			},
			wantErr:   true,
			errSubstr: "restricted",
		},
		{
			name: "restricted header Content-Length rejected",
			config: &headerForwardConfig{
				AddPlaintextHeaders: map[string]string{
					"Content-Length": "100",
				},
			},
			wantErr:   true,
			errSubstr: "restricted",
		},
		{
			name: "empty header name rejected in plaintext",
			config: &headerForwardConfig{
				AddPlaintextHeaders: map[string]string{
					"": "value",
				},
			},
			wantErr:   true,
			errSubstr: "empty",
		},
		{
			name: "empty header name rejected in secrets",
			config: &headerForwardConfig{
				AddHeadersFromSecret: map[string]string{
					"": "secret-name",
				},
			},
			wantErr:   true,
			errSubstr: "empty",
		},
		{
			name: "CRLF injection in header value rejected",
			config: &headerForwardConfig{
				AddPlaintextHeaders: map[string]string{
					"X-Custom": "value\r\nX-Injected: malicious",
				},
			},
			wantErr:   true,
			errSubstr: "invalid header value",
		},
		{
			name: "control character in header value rejected",
			config: &headerForwardConfig{
				AddPlaintextHeaders: map[string]string{
					"X-Custom": "value\x00with-null",
				},
			},
			wantErr:   true,
			errSubstr: "invalid header value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateHeaderForwardConfig(tt.config)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errSubstr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

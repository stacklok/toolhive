package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/permissions"
	"github.com/stacklok/toolhive/pkg/runner"
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
			RemoteAuthConfig: &runner.RemoteAuthConfig{
				Issuer:       "https://oauth.example.com",
				AuthorizeURL: "https://oauth.example.com/auth",
				TokenURL:     "https://oauth.example.com/token",
				ClientID:     "test-client",
				Scopes:       []string{"read", "write"},
				UsePKCE:      true,
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
		assert.Equal(t, map[string]string{"custom": "param"}, result.OAuthConfig.OAuthParams)
		assert.Equal(t, 8081, result.OAuthConfig.CallbackPort)
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

	t.Run("nil runConfig", func(t *testing.T) {
		t.Parallel()

		result := runConfigToCreateRequest(nil)
		assert.Nil(t, result)
	})
}

package export

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/permissions"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

func TestWriteK8sManifest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		config     *runner.RunConfig
		wantErr    bool
		validateFn func(t *testing.T, mcpServer *v1alpha1.MCPServer)
	}{
		{
			name: "basic stdio config",
			config: &runner.RunConfig{
				Image:         "ghcr.io/stacklok/mcp-server-github:latest",
				Name:          "github",
				BaseName:      "github",
				ContainerName: "thv-github",
				Transport:     types.TransportTypeStdio,
				ProxyMode:     types.ProxyModeSSE,
				Port:          8080,
				CmdArgs:       []string{"--verbose"},
			},
			validateFn: func(t *testing.T, mcpServer *v1alpha1.MCPServer) {
				t.Helper()
				assert.Equal(t, "toolhive.stacklok.com/v1alpha1", mcpServer.APIVersion)
				assert.Equal(t, "MCPServer", mcpServer.Kind)
				assert.Equal(t, "github", mcpServer.Name)
				assert.Equal(t, "ghcr.io/stacklok/mcp-server-github:latest", mcpServer.Spec.Image)
				assert.Equal(t, "stdio", mcpServer.Spec.Transport)
				assert.Equal(t, "sse", mcpServer.Spec.ProxyMode)
				assert.Equal(t, int32(8080), mcpServer.Spec.Port)
				assert.Equal(t, []string{"--verbose"}, mcpServer.Spec.Args)
			},
		},
		{
			name: "sse transport with target port",
			config: &runner.RunConfig{
				Image:      "ghcr.io/stacklok/mcp-server-fetch:latest",
				Name:       "fetch",
				BaseName:   "fetch",
				Transport:  types.TransportTypeSSE,
				Port:       8081,
				TargetPort: 3000,
			},
			validateFn: func(t *testing.T, mcpServer *v1alpha1.MCPServer) {
				t.Helper()
				assert.Equal(t, "sse", mcpServer.Spec.Transport)
				assert.Equal(t, int32(8081), mcpServer.Spec.Port)
				assert.Equal(t, int32(3000), mcpServer.Spec.TargetPort)
			},
		},
		{
			name: "config with environment variables",
			config: &runner.RunConfig{
				Image:     "ghcr.io/stacklok/mcp-server-github:latest",
				Name:      "github",
				BaseName:  "github",
				Transport: types.TransportTypeStdio,
				EnvVars: map[string]string{
					"GITHUB_TOKEN": "secret-token",
					"DEBUG":        "true",
				},
			},
			validateFn: func(t *testing.T, mcpServer *v1alpha1.MCPServer) {
				t.Helper()
				require.Len(t, mcpServer.Spec.Env, 2)
				envMap := make(map[string]string)
				for _, env := range mcpServer.Spec.Env {
					envMap[env.Name] = env.Value
				}
				assert.Equal(t, "secret-token", envMap["GITHUB_TOKEN"])
				assert.Equal(t, "true", envMap["DEBUG"])
			},
		},
		{
			name: "config with volumes",
			config: &runner.RunConfig{
				Image:     "ghcr.io/stacklok/mcp-server:latest",
				Name:      "test",
				BaseName:  "test",
				Transport: types.TransportTypeStdio,
				Volumes: []string{
					"/host/path:/container/path",
					"/readonly:/data:ro",
				},
			},
			validateFn: func(t *testing.T, mcpServer *v1alpha1.MCPServer) {
				t.Helper()
				require.Len(t, mcpServer.Spec.Volumes, 2)
				assert.Equal(t, "/host/path", mcpServer.Spec.Volumes[0].HostPath)
				assert.Equal(t, "/container/path", mcpServer.Spec.Volumes[0].MountPath)
				assert.False(t, mcpServer.Spec.Volumes[0].ReadOnly)
				assert.Equal(t, "/readonly", mcpServer.Spec.Volumes[1].HostPath)
				assert.Equal(t, "/data", mcpServer.Spec.Volumes[1].MountPath)
				assert.True(t, mcpServer.Spec.Volumes[1].ReadOnly)
			},
		},
		{
			name: "config with permission profile",
			config: &runner.RunConfig{
				Image:     "ghcr.io/stacklok/mcp-server:latest",
				Name:      "test",
				BaseName:  "test",
				Transport: types.TransportTypeStdio,
				PermissionProfile: &permissions.Profile{
					Read:  []permissions.MountDeclaration{"/data"},
					Write: []permissions.MountDeclaration{"/output"},
				},
			},
			validateFn: func(t *testing.T, mcpServer *v1alpha1.MCPServer) {
				t.Helper()
				require.NotNil(t, mcpServer.Spec.PermissionProfile)
				assert.Equal(t, v1alpha1.PermissionProfileTypeBuiltin, mcpServer.Spec.PermissionProfile.Type)
				assert.Equal(t, "none", mcpServer.Spec.PermissionProfile.Name)
			},
		},
		{
			name: "config with OIDC",
			config: &runner.RunConfig{
				Image:     "ghcr.io/stacklok/mcp-server:latest",
				Name:      "test",
				BaseName:  "test",
				Transport: types.TransportTypeStdio,
				OIDCConfig: &auth.TokenValidatorConfig{
					Issuer:   "https://accounts.google.com",
					Audience: "my-client-id",
					JWKSURL:  "https://accounts.google.com/.well-known/jwks.json",
				},
			},
			validateFn: func(t *testing.T, mcpServer *v1alpha1.MCPServer) {
				t.Helper()
				require.NotNil(t, mcpServer.Spec.OIDCConfig)
				assert.Equal(t, v1alpha1.OIDCConfigTypeInline, mcpServer.Spec.OIDCConfig.Type)
				require.NotNil(t, mcpServer.Spec.OIDCConfig.Inline)
				assert.Equal(t, "https://accounts.google.com", mcpServer.Spec.OIDCConfig.Inline.Issuer)
				assert.Equal(t, "my-client-id", mcpServer.Spec.OIDCConfig.Inline.Audience)
				assert.Equal(t, "https://accounts.google.com/.well-known/jwks.json", mcpServer.Spec.OIDCConfig.Inline.JWKSURL)
			},
		},
		{
			name: "config with authz",
			config: &runner.RunConfig{
				Image:     "ghcr.io/stacklok/mcp-server:latest",
				Name:      "test",
				BaseName:  "test",
				Transport: types.TransportTypeStdio,
				AuthzConfig: &authz.Config{
					Type: authz.ConfigTypeCedarV1,
					Cedar: &authz.CedarConfig{
						Policies: []string{
							"permit(principal, action, resource);",
						},
						EntitiesJSON: "[]",
					},
				},
			},
			validateFn: func(t *testing.T, mcpServer *v1alpha1.MCPServer) {
				t.Helper()
				require.NotNil(t, mcpServer.Spec.AuthzConfig)
				assert.Equal(t, v1alpha1.AuthzConfigTypeInline, mcpServer.Spec.AuthzConfig.Type)
				require.NotNil(t, mcpServer.Spec.AuthzConfig.Inline)
				require.Len(t, mcpServer.Spec.AuthzConfig.Inline.Policies, 1)
				assert.Equal(t, "permit(principal, action, resource);", mcpServer.Spec.AuthzConfig.Inline.Policies[0])
				assert.Equal(t, "[]", mcpServer.Spec.AuthzConfig.Inline.EntitiesJSON)
			},
		},
		{
			name: "config with audit",
			config: &runner.RunConfig{
				Image:     "ghcr.io/stacklok/mcp-server:latest",
				Name:      "test",
				BaseName:  "test",
				Transport: types.TransportTypeStdio,
				AuditConfig: &audit.Config{
					Component: "test-component",
				},
			},
			validateFn: func(t *testing.T, mcpServer *v1alpha1.MCPServer) {
				t.Helper()
				require.NotNil(t, mcpServer.Spec.Audit)
				assert.True(t, mcpServer.Spec.Audit.Enabled)
			},
		},
		{
			name: "config with telemetry",
			config: &runner.RunConfig{
				Image:     "ghcr.io/stacklok/mcp-server:latest",
				Name:      "test",
				BaseName:  "test",
				Transport: types.TransportTypeStdio,
				TelemetryConfig: &telemetry.Config{
					Endpoint:    "http://otel-collector:4318",
					ServiceName: "my-service",
					Insecure:    true,
				},
			},
			validateFn: func(t *testing.T, mcpServer *v1alpha1.MCPServer) {
				t.Helper()
				require.NotNil(t, mcpServer.Spec.Telemetry)
				require.NotNil(t, mcpServer.Spec.Telemetry.OpenTelemetry)
				assert.True(t, mcpServer.Spec.Telemetry.OpenTelemetry.Enabled)
				assert.Equal(t, "http://otel-collector:4318", mcpServer.Spec.Telemetry.OpenTelemetry.Endpoint)
				assert.Equal(t, "my-service", mcpServer.Spec.Telemetry.OpenTelemetry.ServiceName)
				assert.True(t, mcpServer.Spec.Telemetry.OpenTelemetry.Insecure)
			},
		},
		{
			name: "config with prometheus metrics",
			config: &runner.RunConfig{
				Image:     "ghcr.io/stacklok/mcp-server:latest",
				Name:      "test",
				BaseName:  "test",
				Transport: types.TransportTypeStdio,
				TelemetryConfig: &telemetry.Config{
					EnablePrometheusMetricsPath: true,
				},
			},
			validateFn: func(t *testing.T, mcpServer *v1alpha1.MCPServer) {
				t.Helper()
				require.NotNil(t, mcpServer.Spec.Telemetry)
				require.NotNil(t, mcpServer.Spec.Telemetry.Prometheus)
				assert.True(t, mcpServer.Spec.Telemetry.Prometheus.Enabled)
			},
		},
		{
			name: "config with tools filter",
			config: &runner.RunConfig{
				Image:       "ghcr.io/stacklok/mcp-server:latest",
				Name:        "test",
				BaseName:    "test",
				Transport:   types.TransportTypeStdio,
				ToolsFilter: []string{"tool1", "tool2"},
			},
			validateFn: func(t *testing.T, mcpServer *v1alpha1.MCPServer) {
				t.Helper()
				require.Len(t, mcpServer.Spec.ToolsFilter, 2)
				assert.Equal(t, "tool1", mcpServer.Spec.ToolsFilter[0])
				assert.Equal(t, "tool2", mcpServer.Spec.ToolsFilter[1])
			},
		},
		{
			name: "invalid volume format",
			config: &runner.RunConfig{
				Image:     "ghcr.io/stacklok/mcp-server:latest",
				Name:      "test",
				BaseName:  "test",
				Transport: types.TransportTypeStdio,
				Volumes: []string{
					"invalid",
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			err := WriteK8sManifest(tt.config, &buf)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.NotEmpty(t, buf.String())

			// Parse the YAML to validate structure
			var mcpServer v1alpha1.MCPServer
			err = yaml.Unmarshal(buf.Bytes(), &mcpServer)
			require.NoError(t, err)

			// Run custom validation
			if tt.validateFn != nil {
				tt.validateFn(t, &mcpServer)
			}
		})
	}
}

func TestParseVolumeString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		volStr  string
		index   int
		wantVol v1alpha1.Volume
		wantErr bool
	}{
		{
			name:   "basic volume",
			volStr: "/host/path:/container/path",
			index:  0,
			wantVol: v1alpha1.Volume{
				Name:      "volume-0",
				HostPath:  "/host/path",
				MountPath: "/container/path",
				ReadOnly:  false,
			},
		},
		{
			name:   "read-only volume",
			volStr: "/host/path:/container/path:ro",
			index:  1,
			wantVol: v1alpha1.Volume{
				Name:      "volume-1",
				HostPath:  "/host/path",
				MountPath: "/container/path",
				ReadOnly:  true,
			},
		},
		{
			name:    "invalid format - missing colon",
			volStr:  "/host/path",
			index:   0,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vol, err := parseVolumeString(tt.volStr, tt.index)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantVol, vol)
		})
	}
}

func TestSanitizeK8sName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple lowercase",
			input:    "test",
			expected: "test",
		},
		{
			name:     "uppercase to lowercase",
			input:    "TEST",
			expected: "test",
		},
		{
			name:     "with hyphens",
			input:    "test-server",
			expected: "test-server",
		},
		{
			name:     "with underscores",
			input:    "test_server",
			expected: "test-server",
		},
		{
			name:     "with special characters",
			input:    "test@server!",
			expected: "test-server",
		},
		{
			name:     "leading and trailing hyphens",
			input:    "-test-",
			expected: "test",
		},
		{
			name:     "multiple special characters",
			input:    "test___server",
			expected: "test---server",
		},
		{
			name:     "alphanumeric",
			input:    "test123",
			expected: "test123",
		},
		{
			name:     "long name over 253 chars",
			input:    strings.Repeat("a", 300),
			expected: strings.Repeat("a", 253),
		},
		{
			name:     "long name with trailing hyphen after truncation",
			input:    strings.Repeat("a", 252) + "-" + strings.Repeat("b", 50),
			expected: strings.Repeat("a", 252),
		},
		{
			name:     "container name format",
			input:    "thv-github",
			expected: "thv-github",
		},
		{
			name:     "image-based name",
			input:    "ghcr.io/stacklok/mcp-server-github",
			expected: "ghcr-io-stacklok-mcp-server-github",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := sanitizeK8sName(tt.input)
			assert.Equal(t, tt.expected, result)

			// Validate that result is a valid Kubernetes name
			assert.LessOrEqual(t, len(result), 253)
			assert.NotEmpty(t, result)
			assert.NotContains(t, result, "_")
			assert.NotContains(t, result, ".")
			if len(result) > 0 {
				assert.NotEqual(t, "-", string(result[0]))
				assert.NotEqual(t, "-", string(result[len(result)-1]))
			}
		})
	}
}

func TestRunConfigToMCPServer(t *testing.T) {
	t.Parallel()

	t.Run("uses base name for resource name", func(t *testing.T) {
		t.Parallel()

		config := &runner.RunConfig{
			Image:         "test:latest",
			BaseName:      "my-base-name",
			ContainerName: "thv-my-container",
			Name:          "my-name",
			Transport:     types.TransportTypeStdio,
		}

		mcpServer, err := runConfigToMCPServer(config)
		require.NoError(t, err)
		assert.Equal(t, "my-base-name", mcpServer.Name)
	})

	t.Run("falls back to container name", func(t *testing.T) {
		t.Parallel()

		config := &runner.RunConfig{
			Image:         "test:latest",
			ContainerName: "thv-my-container",
			Name:          "my-name",
			Transport:     types.TransportTypeStdio,
		}

		mcpServer, err := runConfigToMCPServer(config)
		require.NoError(t, err)
		assert.Equal(t, "thv-my-container", mcpServer.Name)
	})

	t.Run("falls back to name", func(t *testing.T) {
		t.Parallel()

		config := &runner.RunConfig{
			Image:     "test:latest",
			Name:      "my-name",
			Transport: types.TransportTypeStdio,
		}

		mcpServer, err := runConfigToMCPServer(config)
		require.NoError(t, err)
		assert.Equal(t, "my-name", mcpServer.Name)
	})

	t.Run("sanitizes name", func(t *testing.T) {
		t.Parallel()

		config := &runner.RunConfig{
			Image:     "test:latest",
			Name:      "My_Name_With_CAPS",
			Transport: types.TransportTypeStdio,
		}

		mcpServer, err := runConfigToMCPServer(config)
		require.NoError(t, err)
		assert.Equal(t, "my-name-with-caps", mcpServer.Name)
	})
}

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/runner"
	transporttypes "github.com/stacklok/toolhive/pkg/transport/types"
)

const (
	testImage               = "test-image:latest"
	stdioTransport          = "stdio"
	sseProxyMode            = "sse"
	streamableHTTPProxyMode = "streamable-http"
)

func createRunConfigTestScheme() *runtime.Scheme {
	testScheme := runtime.NewScheme()
	_ = corev1.AddToScheme(testScheme)
	_ = mcpv1alpha1.AddToScheme(testScheme)
	return testScheme
}

func createTestMCPServerWithConfig(name, namespace, image string, envVars []mcpv1alpha1.EnvVar) *mcpv1alpha1.MCPServer {
	return &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     image,
			Transport: stdioTransport,
			Port:      8080,
			Env:       envVars,
		},
	}
}

// TestCreateRunConfigFromMCPServer tests the conversion from MCPServer to RunConfig
func TestCreateRunConfigFromMCPServer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		mcpServer *mcpv1alpha1.MCPServer
		expected  func(t *testing.T, config *runner.RunConfig)
	}{
		{
			name: "basic conversion",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     testImage,
					Transport: stdioTransport,
					Port:      8080,
				},
			},
			//nolint:thelper // We want to see the error at the specific line
			expected: func(t *testing.T, config *runner.RunConfig) {
				assert.Equal(t, "test-server", config.Name)
				assert.Equal(t, "test-image:latest", config.Image)
				assert.Equal(t, transporttypes.TransportTypeStdio, config.Transport)
				assert.Equal(t, 8080, config.Port)
			},
		},
		{
			name: "with environment variables",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "env-server",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "env-image:latest",
					Transport: "sse",
					Port:      9090,
					Env: []mcpv1alpha1.EnvVar{
						{Name: "VAR1", Value: "value1"},
						{Name: "VAR2", Value: "value2"},
					},
				},
			},
			//nolint:thelper // We want to see the error at the specific line
			expected: func(t *testing.T, config *runner.RunConfig) {
				assert.Equal(t, "env-server", config.Name)
				// Check that user-provided env vars are present
				assert.Equal(t, "value1", config.EnvVars["VAR1"])
				assert.Equal(t, "value2", config.EnvVars["VAR2"])
				// Check that transport env var is set
				assert.Equal(t, "sse", config.EnvVars["MCP_TRANSPORT"])
			},
		},
		{
			name: "with volumes",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vol-server",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "vol-image:latest",
					Transport: "stdio",
					Port:      8080,
					Volumes: []mcpv1alpha1.Volume{
						{Name: "vol1", HostPath: "/host/path1", MountPath: "/mount/path1", ReadOnly: false},
						{Name: "vol2", HostPath: "/host/path2", MountPath: "/mount/path2", ReadOnly: true},
					},
				},
			},
			//nolint:thelper // We want to see the error at the specific line
			expected: func(t *testing.T, config *runner.RunConfig) {
				assert.Equal(t, "vol-server", config.Name)
				assert.Len(t, config.Volumes, 2)
				assert.Equal(t, "/host/path1:/mount/path1", config.Volumes[0])
				assert.Equal(t, "/host/path2:/mount/path2:ro", config.Volumes[1])
			},
		},
		{
			name: "with secrets",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "secret-server",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "secret-image:latest",
					Transport: "stdio",
					Port:      8080,
					Secrets: []mcpv1alpha1.SecretRef{
						{Name: "secret1", Key: "key1", TargetEnvName: "TARGET1"},
						{Name: "secret2", Key: "key2"}, // No target, should use key as target
					},
				},
			},
			//nolint:thelper // We want to see the error at the specific line
			expected: func(t *testing.T, config *runner.RunConfig) {
				assert.Equal(t, "secret-server", config.Name)
				// Secrets are NOT in the RunConfig for ConfigMap mode - handled via k8s pod patch
				// This avoids secrets provider errors in Kubernetes environment
				assert.Len(t, config.Secrets, 0)
				// For ConfigMap mode, K8s pod template patch is NOT in the runconfig
				// (it's passed via CLI flag instead to avoid redundancy)
				assert.Empty(t, config.K8sPodTemplatePatch)
			},
		},
		{
			name: "proxy mode specified",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "proxy-mode-server",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     testImage,
					Transport: stdioTransport,
					Port:      8080,
					ProxyMode: streamableHTTPProxyMode,
				},
			},
			//nolint:thelper // We want to see the error at the specific line
			expected: func(t *testing.T, config *runner.RunConfig) {
				assert.Equal(t, "proxy-mode-server", config.Name)
				assert.Equal(t, testImage, config.Image)
				assert.Equal(t, transporttypes.TransportTypeStdio, config.Transport)
				assert.Equal(t, 8080, config.Port)
				assert.Equal(t, transporttypes.ProxyModeStreamableHTTP, config.ProxyMode)
			},
		},
		{
			name: "proxy mode defaults to sse when not specified",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "default-proxy-mode-server",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     testImage,
					Transport: stdioTransport,
					Port:      8080,
					// ProxyMode not specified
				},
			},
			//nolint:thelper // We want to see the error at the specific line
			expected: func(t *testing.T, config *runner.RunConfig) {
				assert.Equal(t, "default-proxy-mode-server", config.Name)
				assert.Equal(t, testImage, config.Image)
				assert.Equal(t, transporttypes.TransportTypeStdio, config.Transport)
				assert.Equal(t, 8080, config.Port)
				assert.Equal(t, transporttypes.ProxyModeSSE, config.ProxyMode, "Should default to sse")
			},
		},
		{
			name: "comprehensive test with all fields",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "comprehensive-server",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:       "comprehensive:latest",
					Transport:   "streamable-http",
					Port:        9090,
					TargetPort:  8080,
					ProxyMode:   "streamable-http",
					Args:        []string{"--comprehensive", "--test"},
					ToolsFilter: []string{"tool1", "tool2"},
					Env: []mcpv1alpha1.EnvVar{
						{Name: "ENV1", Value: "value1"},
						{Name: "ENV2", Value: "value2"},
						{Name: "EMPTY_VALUE", Value: ""},
					},
					Volumes: []mcpv1alpha1.Volume{
						{Name: "vol1", HostPath: "/host/path1", MountPath: "/mount/path1", ReadOnly: false},
						{Name: "vol2", HostPath: "/host/path2", MountPath: "/mount/path2", ReadOnly: true},
					},
					Secrets: []mcpv1alpha1.SecretRef{
						{Name: "secret1", Key: "key1", TargetEnvName: "CUSTOM_TARGET"},
						{Name: "secret2", Key: "key2"}, // Uses key as target
					},
				},
			},
			//nolint:thelper // We want to see the error at the specific line
			expected: func(t *testing.T, config *runner.RunConfig) {
				assert.Equal(t, "comprehensive-server", config.Name)
				assert.Equal(t, "comprehensive:latest", config.Image)
				assert.Equal(t, transporttypes.TransportTypeStreamableHTTP, config.Transport)
				assert.Equal(t, 9090, config.Port)
				assert.Equal(t, 8080, config.TargetPort)
				assert.Equal(t, transporttypes.ProxyModeStreamableHTTP, config.ProxyMode)
				assert.Equal(t, []string{"--comprehensive", "--test"}, config.CmdArgs)
				assert.Equal(t, []string{"tool1", "tool2"}, config.ToolsFilter)
				assert.Len(t, config.EnvVars, 6) // NOTE: we should probably drop this
				assert.Equal(t, "value1", config.EnvVars["ENV1"])
				assert.Equal(t, "value2", config.EnvVars["ENV2"])
				assert.Equal(t, "", config.EnvVars["EMPTY_VALUE"])
				assert.Len(t, config.Volumes, 2)
				assert.Equal(t, "/host/path1:/mount/path1", config.Volumes[0])
				assert.Equal(t, "/host/path2:/mount/path2:ro", config.Volumes[1])
				// Secrets are NOT in the RunConfig for ConfigMap mode - handled via k8s pod patch
				// This avoids secrets provider errors in Kubernetes environment
				assert.Len(t, config.Secrets, 0)
				// For ConfigMap mode, K8s pod template patch is NOT in the runconfig
				// (it's passed via CLI flag instead to avoid redundancy)
				assert.Empty(t, config.K8sPodTemplatePatch)
			},
		},
		{
			name: "edge case: empty/nil slices",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "edge-server",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:       "edge:latest",
					Transport:   "stdio",
					Port:        8080,
					Args:        []string{},             // Empty slice
					ToolsFilter: nil,                    // Nil slice
					Env:         nil,                    // Nil slice
					Volumes:     []mcpv1alpha1.Volume{}, // Empty slice
					Secrets:     nil,                    // Nil slice
				},
			},
			//nolint:thelper // We want to see the error at the specific line
			expected: func(t *testing.T, config *runner.RunConfig) {
				assert.Equal(t, "edge-server", config.Name)
				assert.Equal(t, "edge:latest", config.Image)
				assert.Len(t, config.CmdArgs, 0)
				assert.Len(t, config.ToolsFilter, 0)
				assert.Len(t, config.EnvVars, 1)
				assert.Len(t, config.Volumes, 0)
				assert.Len(t, config.Secrets, 0)
			},
		},
		{
			name: "with telemetry configuration",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "telemetry-server",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     testImage,
					Transport: stdioTransport,
					Port:      8080,
					Telemetry: &mcpv1alpha1.TelemetryConfig{
						OpenTelemetry: &mcpv1alpha1.OpenTelemetryConfig{
							Enabled:     true,
							Endpoint:    "http://otel-collector:4317",
							ServiceName: "custom-service-name",
							Insecure:    true,
							Headers:     []string{"Authorization=Bearer token123", "X-API-Key=abc"},
							Tracing: &mcpv1alpha1.OpenTelemetryTracingConfig{
								Enabled:      true,
								SamplingRate: "0.25",
							},
							Metrics: &mcpv1alpha1.OpenTelemetryMetricsConfig{
								Enabled: true,
							},
						},
						Prometheus: &mcpv1alpha1.PrometheusConfig{
							Enabled: true,
						},
					},
				},
			},
			//nolint:thelper // We want to see the error at the specific line
			expected: func(t *testing.T, config *runner.RunConfig) {
				assert.Equal(t, "telemetry-server", config.Name)

				// Verify telemetry config is set
				assert.NotNil(t, config.TelemetryConfig)

				// Check OpenTelemetry settings (endpoint should have http:// prefix stripped)
				assert.Equal(t, "otel-collector:4317", config.TelemetryConfig.Endpoint)
				assert.Equal(t, "custom-service-name", config.TelemetryConfig.ServiceName)
				assert.True(t, config.TelemetryConfig.Insecure)
				assert.True(t, config.TelemetryConfig.TracingEnabled)
				assert.True(t, config.TelemetryConfig.MetricsEnabled)
				assert.Equal(t, 0.25, config.TelemetryConfig.SamplingRate)
				assert.Equal(t, map[string]string{"Authorization": "Bearer token123", "X-API-Key": "abc"}, config.TelemetryConfig.Headers)

				// Check Prometheus settings
				assert.True(t, config.TelemetryConfig.EnablePrometheusMetricsPath)
			},
		},
		{
			name: "with minimal telemetry configuration",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "minimal-telemetry-server",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     testImage,
					Transport: stdioTransport,
					Port:      8080,
					Telemetry: &mcpv1alpha1.TelemetryConfig{
						OpenTelemetry: &mcpv1alpha1.OpenTelemetryConfig{
							Enabled:  true,
							Endpoint: "https://secure-otel:4318",
							// ServiceName not specified - should default to MCPServer name
						},
					},
				},
			},
			//nolint:thelper // We want to see the error at the specific line
			expected: func(t *testing.T, config *runner.RunConfig) {
				assert.Equal(t, "minimal-telemetry-server", config.Name)

				// Verify telemetry config is set
				assert.NotNil(t, config.TelemetryConfig)

				// Check that service name defaults to MCPServer name
				assert.Equal(t, "minimal-telemetry-server", config.TelemetryConfig.ServiceName)
				assert.Equal(t, "secure-otel:4318", config.TelemetryConfig.Endpoint)
				assert.False(t, config.TelemetryConfig.Insecure)           // Default should be false
				assert.Equal(t, 0.05, config.TelemetryConfig.SamplingRate) // Default sampling rate
			},
		},
		{
			name: "with prometheus only telemetry",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "prometheus-only-server",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     testImage,
					Transport: stdioTransport,
					Port:      8080,
					Telemetry: &mcpv1alpha1.TelemetryConfig{
						Prometheus: &mcpv1alpha1.PrometheusConfig{
							Enabled: true,
						},
					},
				},
			},
			//nolint:thelper // We want to see the error at the specific line
			expected: func(t *testing.T, config *runner.RunConfig) {
				assert.Equal(t, "prometheus-only-server", config.Name)

				// Verify telemetry config is set
				assert.NotNil(t, config.TelemetryConfig)

				// Only Prometheus should be enabled
				assert.True(t, config.TelemetryConfig.EnablePrometheusMetricsPath)
				assert.False(t, config.TelemetryConfig.TracingEnabled)
				assert.False(t, config.TelemetryConfig.MetricsEnabled)
				assert.Equal(t, "", config.TelemetryConfig.Endpoint)
			},
		},
		{
			name: "with inline authorization configuration",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "authz-server",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     testImage,
					Transport: stdioTransport,
					Port:      8080,
					AuthzConfig: &mcpv1alpha1.AuthzConfigRef{
						Type: mcpv1alpha1.AuthzConfigTypeInline,
						Inline: &mcpv1alpha1.InlineAuthzConfig{
							Policies: []string{
								`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`,
								`permit(principal, action == Action::"get_prompt", resource == Prompt::"greeting");`,
							},
							EntitiesJSON: `[{"uid": {"type": "User", "id": "user1"}, "attrs": {}}]`,
						},
					},
				},
			},
			//nolint:thelper // We want to see the error at the specific line
			expected: func(t *testing.T, config *runner.RunConfig) {
				assert.Equal(t, "authz-server", config.Name)

				// Verify authorization config is set
				assert.NotNil(t, config.AuthzConfig)
				assert.Equal(t, "v1", config.AuthzConfig.Version)
				assert.Equal(t, authz.ConfigTypeCedarV1, config.AuthzConfig.Type)
				assert.NotNil(t, config.AuthzConfig.Cedar)

				// Check Cedar-specific configuration
				assert.Len(t, config.AuthzConfig.Cedar.Policies, 2)
				assert.Contains(t, config.AuthzConfig.Cedar.Policies, `permit(principal, action == Action::"call_tool", resource == Tool::"weather");`)
				assert.Contains(t, config.AuthzConfig.Cedar.Policies, `permit(principal, action == Action::"get_prompt", resource == Prompt::"greeting");`)
				assert.Equal(t, `[{"uid": {"type": "User", "id": "user1"}, "attrs": {}}]`, config.AuthzConfig.Cedar.EntitiesJSON)
			},
		},
		{
			name: "with configmap authorization configuration",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "authz-configmap-server",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     testImage,
					Transport: stdioTransport,
					Port:      8080,
					AuthzConfig: &mcpv1alpha1.AuthzConfigRef{
						Type: mcpv1alpha1.AuthzConfigTypeConfigMap,
						ConfigMap: &mcpv1alpha1.ConfigMapAuthzRef{
							Name: "test-authz-config",
							Key:  defaultAuthzKey,
						},
					},
				},
			},
			//nolint:thelper // We want to see the error at the specific line
			expected: func(t *testing.T, config *runner.RunConfig) {
				assert.Equal(t, "authz-configmap-server", config.Name)

				// For ConfigMap type, with new feature, authorization config is embedded in RunConfig
				require.NotNil(t, config.AuthzConfig)
				assert.Equal(t, "v1", config.AuthzConfig.Version)
				assert.Equal(t, authz.ConfigTypeCedarV1, config.AuthzConfig.Type)
				require.NotNil(t, config.AuthzConfig.Cedar)
				assert.Len(t, config.AuthzConfig.Cedar.Policies, 1)
				assert.Contains(t, config.AuthzConfig.Cedar.Policies[0], "call_tool")
				assert.Equal(t, "[]", config.AuthzConfig.Cedar.EntitiesJSON)
			},
		},
		{
			name: "with inline OIDC authentication configuration",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "oidc-server",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     testImage,
					Transport: stdioTransport,
					Port:      8080,
					OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:             "https://auth.example.com",
							Audience:           "toolhive-api",
							JWKSURL:            "https://auth.example.com/.well-known/jwks.json",
							IntrospectionURL:   "https://auth.example.com/oauth/introspect",
							ClientID:           "toolhive-client",
							ClientSecret:       "secret123",
							ThvCABundlePath:    "/etc/ssl/ca-bundle.pem",
							JWKSAuthTokenPath:  "/etc/auth/token",
							JWKSAllowPrivateIP: true,
						},
					},
				},
			},
			//nolint:thelper // We want to see the error at the specific line
			expected: func(t *testing.T, config *runner.RunConfig) {
				assert.Equal(t, "oidc-server", config.Name)
				// Verify OIDC config is set
				assert.NotNil(t, config.OIDCConfig)
				assert.Equal(t, "https://auth.example.com", config.OIDCConfig.Issuer)
				assert.Equal(t, "toolhive-api", config.OIDCConfig.Audience)
				assert.Equal(t, "https://auth.example.com/.well-known/jwks.json", config.OIDCConfig.JWKSURL)
				assert.Equal(t, "https://auth.example.com/oauth/introspect", config.OIDCConfig.IntrospectionURL)
				assert.Equal(t, "toolhive-client", config.OIDCConfig.ClientID)
				assert.Equal(t, "secret123", config.OIDCConfig.ClientSecret)
				// NOTE: CACertPath and AuthTokenFile are not currently mapped in WithOIDCConfig function
				// This is likely a bug that should be fixed separately
				assert.Equal(t, "", config.OIDCConfig.CACertPath)
				assert.Equal(t, "", config.OIDCConfig.AuthTokenFile)
				assert.True(t, config.OIDCConfig.AllowPrivateIP)
			},
		},
		// {
		// 	name: "with configmap OIDC authentication configuration",
		// 	mcpServer: &mcpv1alpha1.MCPServer{
		// 		ObjectMeta: metav1.ObjectMeta{
		// 			Name:      "oidc-configmap-server",
		// 			Namespace: "test-ns",
		// 		},
		// 		Spec: mcpv1alpha1.MCPServerSpec{
		// 			Image:     testImage,
		// 			Transport: stdioTransport,
		// 			Port:      8080,
		// 			OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
		// 				Type: mcpv1alpha1.OIDCConfigTypeConfigMap,
		// 				ConfigMap: &mcpv1alpha1.ConfigMapOIDCRef{
		// 					Name: "test-oidc-config",
		// 					Key:  "oidc.json",
		// 				},
		// 			},
		// 		},
		// 	},
		// 	//nolint:thelper // We want to see the error at the specific line
		// 	expected: func(t *testing.T, config *runner.RunConfig) {
		// 		assert.Equal(t, "oidc-configmap-server", config.Name)
		// 		// For ConfigMap type, OIDC config should not be set directly in RunConfig
		// 		// since it will be handled by proxyrunner when reading from ConfigMap
		// 		assert.Nil(t, config.OIDCConfig)
		// 	},
		// },
		{
			name: "with audit configuration enabled",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "audit-server",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     testImage,
					Transport: stdioTransport,
					Port:      8080,
					Audit: &mcpv1alpha1.AuditConfig{
						Enabled: true,
					},
				},
			},
			//nolint:thelper // We want to see the error at the specific line
			expected: func(t *testing.T, config *runner.RunConfig) {
				assert.Equal(t, "audit-server", config.Name)
				// Verify audit config is set
				assert.NotNil(t, config.AuditConfig)
			},
		},
		{
			name: "with audit configuration disabled",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "audit-disabled-server",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     testImage,
					Transport: stdioTransport,
					Port:      8080,
					Audit: &mcpv1alpha1.AuditConfig{
						Enabled: false,
					},
				},
			},
			//nolint:thelper // We want to see the error at the specific line
			expected: func(t *testing.T, config *runner.RunConfig) {
				assert.Equal(t, "audit-disabled-server", config.Name)
				// When audit is disabled, config should be nil
				assert.Nil(t, config.AuditConfig)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Build reconciler; if test uses ConfigMap-based authz, provide a fake client with that ConfigMap
			var r *MCPServerReconciler
			if tt.mcpServer != nil &&
				tt.mcpServer.Spec.AuthzConfig != nil &&
				tt.mcpServer.Spec.AuthzConfig.Type == mcpv1alpha1.AuthzConfigTypeConfigMap &&
				tt.mcpServer.Spec.AuthzConfig.ConfigMap != nil {

				scheme := createRunConfigTestScheme()

				// Prepare a ConfigMap with authorization configuration content
				cm := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      tt.mcpServer.Spec.AuthzConfig.ConfigMap.Name,
						Namespace: tt.mcpServer.Namespace,
					},
					Data: map[string]string{
						func() string {
							if k := tt.mcpServer.Spec.AuthzConfig.ConfigMap.Key; k != "" {
								return k
							}
							return defaultAuthzKey
						}(): `{
							"version": "v1",
							"type": "cedarv1",
							"cedar": {
								"policies": [
									"permit(principal, action == Action::\"call_tool\", resource == Tool::\"weather\");"
								],
								"entities_json": "[]"
							}
						}`,
					},
				}

				fakeClient := fake.NewClientBuilder().
					WithScheme(scheme).
					WithRuntimeObjects(cm).
					Build()

				r = &MCPServerReconciler{
					Client: fakeClient,
					Scheme: scheme,
				}
			} else {
				r = &MCPServerReconciler{}
			}

			result, err := r.createRunConfigFromMCPServer(tt.mcpServer)
			require.NoError(t, err)
			assert.NotNil(t, result)
			assert.Equal(t, runner.CurrentSchemaVersion, result.SchemaVersion)
			tt.expected(t, result)
		})
	}
}

// TestDeterministicConfigMapGeneration tests that the same MCPServer always generates identical ConfigMaps
func TestDeterministicConfigMapGeneration(t *testing.T) {
	t.Parallel()

	// Create a complex MCPServer with all possible fields to ensure comprehensive testing
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "deterministic-server",
			Namespace: "test-namespace",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:       "deterministic-test:v1.2.3",
			Transport:   "sse",
			Port:        9090,
			TargetPort:  8080,
			Args:        []string{"--arg1", "--arg2", "--complex-flag=value"},
			ToolsFilter: []string{"tool3", "tool1", "tool2"}, // Different order to test sorting
			Env: []mcpv1alpha1.EnvVar{
				{Name: "VAR_C", Value: "value_c"},
				{Name: "VAR_A", Value: "value_a"},
				{Name: "VAR_B", Value: "value_b"},
				{Name: "EMPTY_VAR", Value: ""},
			},
			Volumes: []mcpv1alpha1.Volume{
				{Name: "vol2", HostPath: "/host/path2", MountPath: "/container/path2", ReadOnly: true},
				{Name: "vol1", HostPath: "/host/path1", MountPath: "/container/path1", ReadOnly: false},
			},
			Secrets: []mcpv1alpha1.SecretRef{
				{Name: "secret2", Key: "key2", TargetEnvName: "CUSTOM_TARGET2"},
				{Name: "secret1", Key: "key1"}, // Uses key as target
			},
		},
	}

	reconciler := &MCPServerReconciler{}

	// Generate RunConfig and ConfigMap 10 times
	var configMaps []*corev1.ConfigMap
	var runConfigs []*runner.RunConfig
	var checksums []string

	for i := 0; i < 10; i++ {
		// Generate RunConfig from MCPServer
		runConfig, err := reconciler.createRunConfigFromMCPServer(mcpServer)
		require.NoError(t, err, "Run %d: Failed to create RunConfig", i+1)
		require.NotNil(t, runConfig, "Run %d: RunConfig should not be nil", i+1)

		// Serialize RunConfig to JSON
		runConfigJSON, err := json.MarshalIndent(runConfig, "", "  ")
		require.NoError(t, err, "Run %d: Failed to marshal RunConfig", i+1)

		// Create ConfigMap as the operator would
		configMapName := fmt.Sprintf("%s-runconfig", mcpServer.Name)
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      configMapName,
				Namespace: mcpServer.Namespace,
				Labels:    labelsForRunConfig(mcpServer.Name),
			},
			Data: map[string]string{
				"runconfig.json": string(runConfigJSON),
			},
		}

		// Compute and add checksum
		checksum := computeConfigMapChecksum(configMap)
		configMap.Annotations = map[string]string{
			"toolhive.stacklok.dev/content-checksum": checksum,
		}

		// Store results
		runConfigs = append(runConfigs, runConfig)
		configMaps = append(configMaps, configMap)
		checksums = append(checksums, checksum)
	}

	// Verify all RunConfigs are identical
	baseRunConfig := runConfigs[0]
	for i := 1; i < len(runConfigs); i++ {
		assert.True(t, reflect.DeepEqual(baseRunConfig, runConfigs[i]),
			"RunConfig %d differs from base RunConfig", i+1)
	}

	// Verify all ConfigMaps have identical content
	baseConfigMap := configMaps[0]
	baseJSON := baseConfigMap.Data["runconfig.json"]

	for i := 1; i < len(configMaps); i++ {
		currentJSON := configMaps[i].Data["runconfig.json"]
		assert.Equal(t, baseJSON, currentJSON,
			"ConfigMap %d JSON content differs from base", i+1)

		assert.Equal(t, baseConfigMap.Name, configMaps[i].Name,
			"ConfigMap %d name differs from base", i+1)
		assert.Equal(t, baseConfigMap.Namespace, configMaps[i].Namespace,
			"ConfigMap %d namespace differs from base", i+1)
		assert.True(t, reflect.DeepEqual(baseConfigMap.Labels, configMaps[i].Labels),
			"ConfigMap %d labels differ from base", i+1)
	}

	// Verify all checksums are identical
	baseChecksum := checksums[0]
	for i := 1; i < len(checksums); i++ {
		assert.Equal(t, baseChecksum, checksums[i],
			"Checksum %d differs from base checksum", i+1)
	}

	// Additional verification: manually check the RunConfig content makes sense
	assert.Equal(t, "deterministic-server", baseRunConfig.Name)
	assert.Equal(t, "deterministic-test:v1.2.3", baseRunConfig.Image)
	assert.Equal(t, transporttypes.TransportTypeSSE, baseRunConfig.Transport)
	assert.Equal(t, 9090, baseRunConfig.Port)
	assert.Equal(t, 8080, baseRunConfig.TargetPort)
	assert.Equal(t, []string{"--arg1", "--arg2", "--complex-flag=value"}, baseRunConfig.CmdArgs)
	assert.Equal(t, []string{"tool3", "tool1", "tool2"}, baseRunConfig.ToolsFilter)

	// Verify environment variables
	assert.Len(t, baseRunConfig.EnvVars, 7) // NOTE: we should probably drop this
	assert.Equal(t, "value_a", baseRunConfig.EnvVars["VAR_A"])
	assert.Equal(t, "value_b", baseRunConfig.EnvVars["VAR_B"])
	assert.Equal(t, "value_c", baseRunConfig.EnvVars["VAR_C"])
	assert.Equal(t, "", baseRunConfig.EnvVars["EMPTY_VAR"])

	// Verify volumes (should maintain order from MCPServer)
	assert.Len(t, baseRunConfig.Volumes, 2)
	assert.Equal(t, "/host/path2:/container/path2:ro", baseRunConfig.Volumes[0])
	assert.Equal(t, "/host/path1:/container/path1", baseRunConfig.Volumes[1])

	// Verify secrets are NOT in the RunConfig for ConfigMap mode - handled via k8s pod patch
	// This avoids secrets provider errors in Kubernetes environment
	assert.Len(t, baseRunConfig.Secrets, 0)

	t.Logf("✅ Deterministic test passed: Generated identical ConfigMaps 10 times")
	t.Logf("   Checksum: %s", baseChecksum)
	t.Logf("   ConfigMap size: %d bytes", len(baseJSON))
}

// TestEnsureRunConfigConfigMap tests the ConfigMap creation and update logic
func TestEnsureRunConfigConfigMap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		mcpServer       *mcpv1alpha1.MCPServer
		existingCM      *corev1.ConfigMap
		expectUpdate    bool
		expectError     bool
		validateContent func(*testing.T, *corev1.ConfigMap)
	}{
		{
			name:        "create new configmap",
			mcpServer:   createTestMCPServerWithConfig("new-server", "default", "test:v1", nil),
			existingCM:  nil,
			expectError: false,
			validateContent: func(t *testing.T, cm *corev1.ConfigMap) {
				t.Helper()
				assert.Equal(t, "new-server-runconfig", cm.Name)
				assert.Equal(t, "default", cm.Namespace)
				assert.Contains(t, cm.Data, "runconfig.json")
				assert.Contains(t, cm.Annotations, "toolhive.stacklok.dev/content-checksum")

				var runConfig runner.RunConfig
				err := json.Unmarshal([]byte(cm.Data["runconfig.json"]), &runConfig)
				require.NoError(t, err)
				assert.Equal(t, "new-server", runConfig.Name)
				assert.Equal(t, "test:v1", runConfig.Image)
			},
		},
		{
			name:      "update existing configmap with changed content",
			mcpServer: createTestMCPServerWithConfig("update-server", "default", "test:v2", nil),
			existingCM: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "update-server-runconfig",
					Namespace: "default",
					Labels:    labelsForRunConfig("update-server"),
					Annotations: map[string]string{
						"toolhive.stacklok.dev/content-checksum": "oldchecksum123",
					},
				},
				Data: map[string]string{
					"runconfig.json": `{"schemaVersion":"v1","name":"update-server","image":"test:v1","transport":"stdio","port":8080}`,
				},
			},
			expectUpdate: true,
			expectError:  false,
			validateContent: func(t *testing.T, cm *corev1.ConfigMap) {
				t.Helper()
				var runConfig runner.RunConfig
				err := json.Unmarshal([]byte(cm.Data["runconfig.json"]), &runConfig)
				require.NoError(t, err)
				assert.Equal(t, "test:v2", runConfig.Image)
				assert.NotEqual(t, "oldchecksum123", cm.Annotations["toolhive.stacklok.dev/content-checksum"])
				assert.NotEmpty(t, cm.Annotations["toolhive.stacklok.dev/content-checksum"])
			},
		},
		{
			name:      "no update when content unchanged",
			mcpServer: createTestMCPServerWithConfig("same-server", "default", "test:v1", nil),
			existingCM: func() *corev1.ConfigMap {
				// Create a ConfigMap with the same content that would be generated
				r := &MCPServerReconciler{}
				mcpServer := createTestMCPServerWithConfig("same-server", "default", "test:v1", nil)
				runConfig, err := r.createRunConfigFromMCPServer(mcpServer)
				if err != nil {
					panic(fmt.Sprintf("Failed to create RunConfig: %v", err))
				}
				runConfigJSON, _ := json.MarshalIndent(runConfig, "", "  ")

				configMap := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "same-server-runconfig",
						Namespace: "default",
						Labels:    labelsForRunConfig("same-server"),
					},
					Data: map[string]string{
						"runconfig.json": string(runConfigJSON),
					},
				}

				// Compute the actual checksum for this content
				checksum := computeConfigMapChecksum(configMap)
				configMap.Annotations = map[string]string{
					"toolhive.stacklok.dev/content-checksum": checksum,
				}

				return configMap
			}(),
			expectUpdate: false,
			expectError:  false,
			validateContent: func(t *testing.T, cm *corev1.ConfigMap) {
				t.Helper()
				// Should have a valid checksum for the content
				assert.NotEmpty(t, cm.Annotations["toolhive.stacklok.dev/content-checksum"])
			},
		},
		{
			name: "configmap with telemetry configuration",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "telemetry-test",
					Namespace: "toolhive-system",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "ghcr.io/example/server:v1.0.0",
					Transport: "stdio",
					Port:      8080,
					Telemetry: &mcpv1alpha1.TelemetryConfig{
						OpenTelemetry: &mcpv1alpha1.OpenTelemetryConfig{
							Enabled:     true,
							Endpoint:    "http://otel-collector:4317",
							ServiceName: "test-service",
							Headers:     []string{"Authorization=Bearer test-token"},
							Insecure:    true,
							Tracing: &mcpv1alpha1.OpenTelemetryTracingConfig{
								Enabled:      true,
								SamplingRate: "0.1",
							},
							Metrics: &mcpv1alpha1.OpenTelemetryMetricsConfig{
								Enabled: true,
							},
						},
						Prometheus: &mcpv1alpha1.PrometheusConfig{
							Enabled: true,
						},
					},
				},
			},
			existingCM:  nil,
			expectError: false,
			validateContent: func(t *testing.T, cm *corev1.ConfigMap) {
				t.Helper()
				assert.Equal(t, "telemetry-test-runconfig", cm.Name)
				assert.Equal(t, "toolhive-system", cm.Namespace)
				assert.Contains(t, cm.Data, "runconfig.json")

				// Parse and validate telemetry configuration in runconfig.json
				var runConfig runner.RunConfig
				err := json.Unmarshal([]byte(cm.Data["runconfig.json"]), &runConfig)
				require.NoError(t, err)

				// Verify basic fields
				assert.Equal(t, "telemetry-test", runConfig.Name)
				assert.Equal(t, "ghcr.io/example/server:v1.0.0", runConfig.Image)

				// Verify telemetry configuration is properly serialized
				assert.NotNil(t, runConfig.TelemetryConfig, "TelemetryConfig should be present in runconfig.json")

				// Check OpenTelemetry settings (endpoint should have http:// prefix stripped)
				assert.Equal(t, "otel-collector:4317", runConfig.TelemetryConfig.Endpoint)
				assert.Equal(t, "test-service", runConfig.TelemetryConfig.ServiceName)
				assert.True(t, runConfig.TelemetryConfig.Insecure)
				assert.True(t, runConfig.TelemetryConfig.TracingEnabled)
				assert.True(t, runConfig.TelemetryConfig.MetricsEnabled)
				assert.Equal(t, 0.1, runConfig.TelemetryConfig.SamplingRate)
				assert.Equal(t, map[string]string{"Authorization": "Bearer test-token"}, runConfig.TelemetryConfig.Headers)

				// Check Prometheus settings
				assert.True(t, runConfig.TelemetryConfig.EnablePrometheusMetricsPath)
			},
		},
		{
			name: "configmap with inline authorization configuration",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "authz-test",
					Namespace: "toolhive-system",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "ghcr.io/example/server:v1.0.0",
					Transport: "stdio",
					Port:      8080,
					AuthzConfig: &mcpv1alpha1.AuthzConfigRef{
						Type: mcpv1alpha1.AuthzConfigTypeInline,
						Inline: &mcpv1alpha1.InlineAuthzConfig{
							Policies: []string{
								`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`,
								`permit(principal, action == Action::"get_prompt", resource == Prompt::"greeting");`,
							},
							EntitiesJSON: `[{"uid": {"type": "User", "id": "user1"}, "attrs": {}}]`,
						},
					},
				},
			},
			existingCM:  nil,
			expectError: false,
			validateContent: func(t *testing.T, cm *corev1.ConfigMap) {
				t.Helper()
				assert.Equal(t, "authz-test-runconfig", cm.Name)
				assert.Equal(t, "toolhive-system", cm.Namespace)
				assert.Contains(t, cm.Data, "runconfig.json")

				// Parse and validate authorization configuration in runconfig.json
				var runConfig runner.RunConfig
				err := json.Unmarshal([]byte(cm.Data["runconfig.json"]), &runConfig)
				require.NoError(t, err)

				// Verify basic fields
				assert.Equal(t, "authz-test", runConfig.Name)
				assert.Equal(t, "ghcr.io/example/server:v1.0.0", runConfig.Image)

				// Verify authorization configuration is properly serialized
				assert.NotNil(t, runConfig.AuthzConfig, "AuthzConfig should be present in runconfig.json")
				assert.Equal(t, "v1", runConfig.AuthzConfig.Version)
				assert.Equal(t, authz.ConfigTypeCedarV1, runConfig.AuthzConfig.Type)
				assert.NotNil(t, runConfig.AuthzConfig.Cedar)

				// Check Cedar-specific configuration
				assert.Len(t, runConfig.AuthzConfig.Cedar.Policies, 2)
				assert.Contains(t, runConfig.AuthzConfig.Cedar.Policies, `permit(principal, action == Action::"call_tool", resource == Tool::"weather");`)
				assert.Contains(t, runConfig.AuthzConfig.Cedar.Policies, `permit(principal, action == Action::"get_prompt", resource == Prompt::"greeting");`)
				assert.Equal(t, `[{"uid": {"type": "User", "id": "user1"}, "attrs": {}}]`, runConfig.AuthzConfig.Cedar.EntitiesJSON)
			},
		},
		{
			name: "configmap with inline OIDC authentication configuration",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "oidc-test",
					Namespace: "toolhive-system",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "ghcr.io/example/server:v1.0.0",
					Transport: "stdio",
					Port:      8080,
					OIDCConfig: &mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:             "https://auth.example.com",
							Audience:           "toolhive-api",
							JWKSURL:            "https://auth.example.com/.well-known/jwks.json",
							IntrospectionURL:   "https://auth.example.com/oauth/introspect",
							ClientID:           "toolhive-client",
							ClientSecret:       "secret123",
							ThvCABundlePath:    "/etc/ssl/ca-bundle.pem",
							JWKSAuthTokenPath:  "/etc/auth/token",
							JWKSAllowPrivateIP: true,
						},
					},
				},
			},
			existingCM:  nil,
			expectError: false,
			validateContent: func(t *testing.T, cm *corev1.ConfigMap) {
				t.Helper()
				assert.Equal(t, "oidc-test-runconfig", cm.Name)
				assert.Equal(t, "toolhive-system", cm.Namespace)
				assert.Contains(t, cm.Data, "runconfig.json")
				// Parse and validate OIDC authentication configuration in runconfig.json
				var runConfig runner.RunConfig
				err := json.Unmarshal([]byte(cm.Data["runconfig.json"]), &runConfig)
				require.NoError(t, err)
				// Verify basic fields
				assert.Equal(t, "oidc-test", runConfig.Name)
				assert.Equal(t, "ghcr.io/example/server:v1.0.0", runConfig.Image)
				// Verify OIDC authentication configuration is properly serialized
				assert.NotNil(t, runConfig.OIDCConfig, "OIDCConfig should be present in runconfig.json")
				assert.Equal(t, "https://auth.example.com", runConfig.OIDCConfig.Issuer)
				assert.Equal(t, "toolhive-api", runConfig.OIDCConfig.Audience)
				assert.Equal(t, "https://auth.example.com/.well-known/jwks.json", runConfig.OIDCConfig.JWKSURL)
				assert.Equal(t, "https://auth.example.com/oauth/introspect", runConfig.OIDCConfig.IntrospectionURL)
				assert.Equal(t, "toolhive-client", runConfig.OIDCConfig.ClientID)
				assert.Equal(t, "secret123", runConfig.OIDCConfig.ClientSecret)
				// NOTE: CACertPath and AuthTokenFile are not currently mapped in WithOIDCConfig function
				assert.Equal(t, "", runConfig.OIDCConfig.CACertPath)
				assert.Equal(t, "", runConfig.OIDCConfig.AuthTokenFile)
				assert.True(t, runConfig.OIDCConfig.AllowPrivateIP)
			},
		},
		{
			name: "configmap with audit configuration enabled",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "audit-test",
					Namespace: "toolhive-system",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "ghcr.io/example/server:v1.0.0",
					Transport: "stdio",
					Port:      8080,
					Audit: &mcpv1alpha1.AuditConfig{
						Enabled: true,
					},
				},
			},
			existingCM:  nil,
			expectError: false,
			validateContent: func(t *testing.T, cm *corev1.ConfigMap) {
				t.Helper()
				assert.Equal(t, "audit-test-runconfig", cm.Name)
				assert.Equal(t, "toolhive-system", cm.Namespace)
				assert.Contains(t, cm.Data, "runconfig.json")
				// Parse and validate audit configuration in runconfig.json
				var runConfig runner.RunConfig
				err := json.Unmarshal([]byte(cm.Data["runconfig.json"]), &runConfig)
				require.NoError(t, err)
				// Verify basic fields
				assert.Equal(t, "audit-test", runConfig.Name)
				assert.Equal(t, "ghcr.io/example/server:v1.0.0", runConfig.Image)
				// Verify audit configuration is properly serialized
				assert.NotNil(t, runConfig.AuditConfig, "AuditConfig should be present in runconfig.json")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			testScheme := createRunConfigTestScheme()
			objects := []runtime.Object{tt.mcpServer}
			if tt.existingCM != nil {
				objects = append(objects, tt.existingCM)
			}
			fakeClient := fake.NewClientBuilder().WithScheme(testScheme).WithRuntimeObjects(objects...).Build()

			reconciler := &MCPServerReconciler{
				Client: fakeClient,
				Scheme: testScheme,
			}

			// Execute the method under test
			err := reconciler.ensureRunConfigConfigMap(context.TODO(), tt.mcpServer)
			if tt.expectError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)

			// Verify the ConfigMap exists
			configMapName := fmt.Sprintf("%s-runconfig", tt.mcpServer.Name)
			configMap := &corev1.ConfigMap{}
			err = fakeClient.Get(context.TODO(), types.NamespacedName{
				Name:      configMapName,
				Namespace: tt.mcpServer.Namespace,
			}, configMap)
			require.NoError(t, err)

			// Verify basic structure
			assert.Equal(t, configMapName, configMap.Name)
			assert.Equal(t, tt.mcpServer.Namespace, configMap.Namespace)
			assert.Equal(t, labelsForRunConfig(tt.mcpServer.Name), configMap.Labels)
			assert.Contains(t, configMap.Data, "runconfig.json")

			// Verify the RunConfig content is correct
			var runConfig runner.RunConfig
			err = json.Unmarshal([]byte(configMap.Data["runconfig.json"]), &runConfig)
			require.NoError(t, err)
			assert.Equal(t, tt.mcpServer.Name, runConfig.Name)
			assert.Equal(t, tt.mcpServer.Spec.Image, runConfig.Image)

			// Verify annotation behavior
			if tt.validateContent != nil {
				tt.validateContent(t, configMap)
			}
		})
	}

	// Additional test: ConfigMap-based Authz referenced externally should be embedded into runconfig.json
	t.Run("configmap with external authorization configuration", func(t *testing.T) {
		t.Parallel()
		testScheme := createRunConfigTestScheme()

		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "authz-cm-ext",
				Namespace: "toolhive-system",
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image:     "ghcr.io/example/server:v1.0.0",
				Transport: "stdio",
				Port:      8080,
				AuthzConfig: &mcpv1alpha1.AuthzConfigRef{
					Type: mcpv1alpha1.AuthzConfigTypeConfigMap,
					ConfigMap: &mcpv1alpha1.ConfigMapAuthzRef{
						Name: "ext-authz-config",
						Key:  "authz.json",
					},
				},
			},
		}

		authzCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "ext-authz-config",
				Namespace: "toolhive-system",
			},
			Data: map[string]string{
				"authz.json": `{
					"version": "v1",
					"type": "cedarv1",
					"cedar": {
						"policies": [
							"permit(principal, action == Action::\"call_tool\", resource == Tool::\"weather\");",
							"permit(principal, action == Action::\"get_prompt\", resource == Prompt::\"greeting\");"
						],
						"entities_json": "[{\"uid\": {\"type\": \"User\", \"id\": \"user1\"}, \"attrs\": {}}]"
					}
				}`,
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(testScheme).
			WithRuntimeObjects(mcpServer, authzCM).
			Build()

		reconciler := &MCPServerReconciler{
			Client: fakeClient,
			Scheme: testScheme,
		}

		err := reconciler.ensureRunConfigConfigMap(context.TODO(), mcpServer)
		require.NoError(t, err)

		// Fetch the generated runconfig ConfigMap
		configMapName := fmt.Sprintf("%s-runconfig", mcpServer.Name)
		configMap := &corev1.ConfigMap{}
		err = fakeClient.Get(context.TODO(), types.NamespacedName{
			Name:      configMapName,
			Namespace: mcpServer.Namespace,
		}, configMap)
		require.NoError(t, err)

		// Validate that authz config is embedded
		var runConfig runner.RunConfig
		err = json.Unmarshal([]byte(configMap.Data["runconfig.json"]), &runConfig)
		require.NoError(t, err)

		require.NotNil(t, runConfig.AuthzConfig)
		assert.Equal(t, "v1", runConfig.AuthzConfig.Version)
		assert.Equal(t, authz.ConfigTypeCedarV1, runConfig.AuthzConfig.Type)
		require.NotNil(t, runConfig.AuthzConfig.Cedar)
		assert.Len(t, runConfig.AuthzConfig.Cedar.Policies, 2)
		assert.Contains(t, runConfig.AuthzConfig.Cedar.Policies, `permit(principal, action == Action::"call_tool", resource == Tool::"weather");`)
		assert.Contains(t, runConfig.AuthzConfig.Cedar.Policies, `permit(principal, action == Action::"get_prompt", resource == Prompt::"greeting");`)
		assert.Equal(t, `[{"uid": {"type": "User", "id": "user1"}, "attrs": {}}]`, runConfig.AuthzConfig.Cedar.EntitiesJSON)
	})
}

// TestRunConfigContentEquals tests the content comparison logic
func TestRunConfigContentEquals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		current  *corev1.ConfigMap
		desired  *corev1.ConfigMap
		expected bool
	}{
		{
			name: "identical content with same checksum",
			current: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"key": "value"},
					Annotations: map[string]string{
						"other":                                  "annotation",
						"toolhive.stacklok.dev/content-checksum": "samechecksum123",
					},
				},
				Data: map[string]string{"runconfig.json": "content"},
			},
			desired: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"key": "value"},
					Annotations: map[string]string{
						"other":                                  "annotation",
						"toolhive.stacklok.dev/content-checksum": "samechecksum123",
					},
				},
				Data: map[string]string{"runconfig.json": "content"},
			},
			expected: true,
		},
		{
			name: "different data content",
			current: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"toolhive.stacklok.dev/content-checksum": "oldchecksum123",
					},
				},
				Data: map[string]string{"runconfig.json": "old-content"},
			},
			desired: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"toolhive.stacklok.dev/content-checksum": "newchecksum456",
					},
				},
				Data: map[string]string{"runconfig.json": "new-content"},
			},
			expected: false,
		},
		{
			name: "different labels",
			current: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"key": "old-value"},
					Annotations: map[string]string{
						"toolhive.stacklok.dev/content-checksum": "oldchecksum123",
					},
				},
				Data: map[string]string{"runconfig.json": "content"},
			},
			desired: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"key": "new-value"},
					Annotations: map[string]string{
						"toolhive.stacklok.dev/content-checksum": "newchecksum456",
					},
				},
				Data: map[string]string{"runconfig.json": "content"},
			},
			expected: false,
		},
		{
			name: "different non-checksum annotations",
			current: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"other":                                  "old-annotation",
						"toolhive.stacklok.dev/content-checksum": "oldchecksum123",
					},
				},
				Data: map[string]string{"runconfig.json": "content"},
			},
			desired: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"other":                                  "new-annotation",
						"toolhive.stacklok.dev/content-checksum": "newchecksum456",
					},
				},
				Data: map[string]string{"runconfig.json": "content"},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := &MCPServerReconciler{}
			result := r.runConfigContentEquals(tt.current, tt.desired)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestValidateRunConfig tests the validation logic
func TestValidateRunConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		config    *runner.RunConfig
		expectErr bool
		errMsg    string
	}{
		{
			name: "valid config",
			config: &runner.RunConfig{
				Name:      "valid-server",
				Image:     "test:latest",
				Transport: "stdio",
				Port:      8080,
			},
			expectErr: false,
		},
		{
			name:      "nil config",
			config:    nil,
			expectErr: true,
			errMsg:    "RunConfig cannot be nil",
		},
		{
			name: "missing image",
			config: &runner.RunConfig{
				Name:      "no-image",
				Transport: "stdio",
			},
			expectErr: true,
			errMsg:    "image is required",
		},
		{
			name: "missing name",
			config: &runner.RunConfig{
				Image:     "test:latest",
				Transport: "stdio",
			},
			expectErr: true,
			errMsg:    "name is required",
		},
		{
			name: "invalid transport",
			config: &runner.RunConfig{
				Name:      "invalid-transport",
				Image:     "test:latest",
				Transport: "invalid",
			},
			expectErr: true,
			errMsg:    "invalid transport type",
		},
		{
			name: "invalid environment variable key",
			config: &runner.RunConfig{
				Name:      "invalid-env",
				Image:     "test:latest",
				Transport: "stdio",
				EnvVars:   map[string]string{"INVALID=KEY": "value"},
			},
			expectErr: true,
			errMsg:    "invalid environment variable key",
		},
		{
			name: "invalid volume format",
			config: &runner.RunConfig{
				Name:      "invalid-vol",
				Image:     "test:latest",
				Transport: "stdio",
				Volumes:   []string{"invalid-format"},
			},
			expectErr: true,
			errMsg:    "invalid volume mount format",
		},
		{
			name: "invalid secret format",
			config: &runner.RunConfig{
				Name:      "invalid-secret",
				Image:     "test:latest",
				Transport: "stdio",
				Secrets:   []string{"invalid-format"},
			},
			expectErr: true,
			errMsg:    "invalid secret format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := &MCPServerReconciler{}
			err := r.validateRunConfig(t.Context(), tt.config)

			if tt.expectErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestLabelsForRunConfig tests the label generation
func TestLabelsForRunConfig(t *testing.T) {
	t.Parallel()
	expected := map[string]string{
		"toolhive.stacklok.io/component":  "run-config",
		"toolhive.stacklok.io/mcp-server": "test-server",
		"toolhive.stacklok.io/managed-by": "toolhive-operator",
	}

	result := labelsForRunConfig("test-server")
	assert.Equal(t, expected, result)
}

// TestComputeConfigMapChecksum tests the checksum computation
func TestComputeConfigMapChecksum(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name               string
		cm1                *corev1.ConfigMap
		cm2                *corev1.ConfigMap
		sameShouldChecksum bool
	}{
		{
			name: "identical configmaps have same checksum",
			cm1: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      map[string]string{"key": "value"},
					Annotations: map[string]string{"other": "annotation"},
				},
				Data: map[string]string{"runconfig.json": "content"},
			},
			cm2: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      map[string]string{"key": "value"},
					Annotations: map[string]string{"other": "annotation"},
				},
				Data: map[string]string{"runconfig.json": "content"},
			},
			sameShouldChecksum: true,
		},
		{
			name: "different data content produces different checksum",
			cm1: &corev1.ConfigMap{
				Data: map[string]string{"runconfig.json": "content1"},
			},
			cm2: &corev1.ConfigMap{
				Data: map[string]string{"runconfig.json": "content2"},
			},
			sameShouldChecksum: false,
		},
		{
			name: "different labels produce different checksum",
			cm1: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"key": "value1"},
				},
				Data: map[string]string{"runconfig.json": "content"},
			},
			cm2: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"key": "value2"},
				},
				Data: map[string]string{"runconfig.json": "content"},
			},
			sameShouldChecksum: false,
		},
		{
			name: "checksum annotation is ignored in computation",
			cm1: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"other":                                  "annotation",
						"toolhive.stacklok.dev/content-checksum": "checksum1",
					},
				},
				Data: map[string]string{"runconfig.json": "content"},
			},
			cm2: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"other":                                  "annotation",
						"toolhive.stacklok.dev/content-checksum": "checksum2",
					},
				},
				Data: map[string]string{"runconfig.json": "content"},
			},
			sameShouldChecksum: true, // Should be same because checksum annotation is ignored
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			checksum1 := computeConfigMapChecksum(tt.cm1)
			checksum2 := computeConfigMapChecksum(tt.cm2)

			assert.NotEmpty(t, checksum1)
			assert.NotEmpty(t, checksum2)

			if tt.sameShouldChecksum {
				assert.Equal(t, checksum1, checksum2)
			} else {
				assert.NotEqual(t, checksum1, checksum2)
			}
		})
	}
}

// TestEnsureRunConfigConfigMapCompleteFlow tests the complete flow from MCPServer changes to ConfigMap updates
func TestEnsureRunConfigConfigMapCompleteFlow(t *testing.T) {
	t.Parallel()
	testScheme := createRunConfigTestScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(testScheme).Build()
	reconciler := &MCPServerReconciler{
		Client: fakeClient,
		Scheme: testScheme,
	}

	// Step 1: Create initial MCPServer and ConfigMap
	mcpServer := createTestMCPServerWithConfig("flow-server", "flow-ns", "test:v1", []mcpv1alpha1.EnvVar{
		{Name: "ENV1", Value: "value1"},
	})

	err := reconciler.ensureRunConfigConfigMap(context.TODO(), mcpServer)
	require.NoError(t, err)

	// Verify initial ConfigMap
	configMapName := fmt.Sprintf("%s-runconfig", mcpServer.Name)
	configMap1 := &corev1.ConfigMap{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      configMapName,
		Namespace: mcpServer.Namespace,
	}, configMap1)
	require.NoError(t, err)

	initialChecksum := configMap1.Annotations["toolhive.stacklok.dev/content-checksum"]
	assert.NotEmpty(t, initialChecksum)

	// Verify initial content
	var initialRunConfig runner.RunConfig
	err = json.Unmarshal([]byte(configMap1.Data["runconfig.json"]), &initialRunConfig)
	require.NoError(t, err)
	assert.Equal(t, "test:v1", initialRunConfig.Image)
	assert.Len(t, initialRunConfig.EnvVars, 2) // NOTE: we should probably drop this
	assert.Equal(t, "value1", initialRunConfig.EnvVars["ENV1"])

	// Step 2: Update MCPServer with new environment variable
	// The checksum will automatically change when content changes

	mcpServer.Spec.Image = "test:v2"
	mcpServer.Spec.Env = []mcpv1alpha1.EnvVar{
		{Name: "ENV1", Value: "value1"},
		{Name: "ENV2", Value: "value2"},
	}

	err = reconciler.ensureRunConfigConfigMap(context.TODO(), mcpServer)
	require.NoError(t, err)

	// Verify ConfigMap was updated
	configMap2 := &corev1.ConfigMap{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      configMapName,
		Namespace: mcpServer.Namespace,
	}, configMap2)
	require.NoError(t, err)

	updatedChecksum := configMap2.Annotations["toolhive.stacklok.dev/content-checksum"]
	assert.NotEmpty(t, updatedChecksum)
	assert.NotEqual(t, initialChecksum, updatedChecksum, "Checksum should be updated when content changes")

	// Verify updated content
	var updatedRunConfig runner.RunConfig
	err = json.Unmarshal([]byte(configMap2.Data["runconfig.json"]), &updatedRunConfig)
	require.NoError(t, err)
	assert.Equal(t, "test:v2", updatedRunConfig.Image)
	assert.Len(t, updatedRunConfig.EnvVars, 3) // NOTE: we should probably drop this
	assert.Equal(t, "value1", updatedRunConfig.EnvVars["ENV1"])
	assert.Equal(t, "value2", updatedRunConfig.EnvVars["ENV2"])

	// Step 3: No-op update (same content)
	err = reconciler.ensureRunConfigConfigMap(context.TODO(), mcpServer)
	require.NoError(t, err)

	// Verify ConfigMap timestamp didn't change
	configMap3 := &corev1.ConfigMap{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      configMapName,
		Namespace: mcpServer.Namespace,
	}, configMap3)
	require.NoError(t, err)

	finalChecksum := configMap3.Annotations["toolhive.stacklok.dev/content-checksum"]
	assert.Equal(t, updatedChecksum, finalChecksum, "Checksum should not change for no-op update")
}

func TestMCPServerModificationScenarios(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		initialServer   func() *mcpv1alpha1.MCPServer
		modifyServer    func(*mcpv1alpha1.MCPServer)
		expectedChanges map[string]interface{}
	}{
		{
			name: "Transport change",
			initialServer: func() *mcpv1alpha1.MCPServer {
				return createTestMCPServerWithConfig("transport-test", "default", "test:v1", nil)
			},
			modifyServer: func(server *mcpv1alpha1.MCPServer) {
				server.Spec.Transport = "sse"
				server.Spec.Port = 9090
				server.Spec.TargetPort = 8080
			},
			expectedChanges: map[string]interface{}{
				"Transport": transporttypes.TransportTypeSSE,
				"Port":      9090,
			},
		},
		{
			name: "Args modification",
			initialServer: func() *mcpv1alpha1.MCPServer {
				server := createTestMCPServerWithConfig("args-test", "default", "test:v1", nil)
				server.Spec.Args = []string{"--initial", "--arg"}
				return server
			},
			modifyServer: func(server *mcpv1alpha1.MCPServer) {
				server.Spec.Args = []string{"--modified", "--different", "--args"}
			},
			expectedChanges: map[string]interface{}{
				"CmdArgs": []string{"--modified", "--different", "--args"},
			},
		},
		{
			name: "ToolsFilter change",
			initialServer: func() *mcpv1alpha1.MCPServer {
				server := createTestMCPServerWithConfig("tools-test", "default", "test:v1", nil)
				server.Spec.ToolsFilter = []string{"tool1", "tool2"}
				return server
			},
			modifyServer: func(server *mcpv1alpha1.MCPServer) {
				server.Spec.ToolsFilter = []string{"tool3", "tool4", "tool5"}
			},
			expectedChanges: map[string]interface{}{
				"ToolsFilter": []string{"tool3", "tool4", "tool5"},
			},
		},
		{
			name: "Volume changes",
			initialServer: func() *mcpv1alpha1.MCPServer {
				server := createTestMCPServerWithConfig("volume-test", "default", "test:v1", nil)
				server.Spec.Volumes = []mcpv1alpha1.Volume{
					{HostPath: "/host/path1", MountPath: "/container/path1"},
				}
				return server
			},
			modifyServer: func(server *mcpv1alpha1.MCPServer) {
				server.Spec.Volumes = []mcpv1alpha1.Volume{
					{HostPath: "/host/path1", MountPath: "/container/path1", ReadOnly: true},
					{HostPath: "/host/path2", MountPath: "/container/path2"},
				}
			},
			expectedChanges: map[string]interface{}{
				"Volumes": []string{"/host/path1:/container/path1:ro", "/host/path2:/container/path2"},
			},
		},
		{
			name: "Secret changes",
			initialServer: func() *mcpv1alpha1.MCPServer {
				server := createTestMCPServerWithConfig("secret-test", "default", "test:v1", nil)
				server.Spec.Secrets = []mcpv1alpha1.SecretRef{
					{Name: "secret1", Key: "key1"},
				}
				return server
			},
			modifyServer: func(server *mcpv1alpha1.MCPServer) {
				server.Spec.Secrets = []mcpv1alpha1.SecretRef{
					{Name: "secret1", Key: "key1", TargetEnvName: "CUSTOM_ENV1"},
					{Name: "secret2", Key: "key2"},
				}
			},
			expectedChanges: map[string]interface{}{
				// Secrets are NOT in the RunConfig for ConfigMap mode - handled via k8s pod patch
				// Since secrets don't affect runconfig content, no changes expected in runconfig
				"Secrets": ([]string)(nil),
			},
		},
		{
			name: "Proxy mode change",
			initialServer: func() *mcpv1alpha1.MCPServer {
				server := createTestMCPServerWithConfig("proxy-test", "default", "test:v1", nil)
				server.Spec.ProxyMode = sseProxyMode
				return server
			},
			modifyServer: func(server *mcpv1alpha1.MCPServer) {
				server.Spec.ProxyMode = streamableHTTPProxyMode
			},
			expectedChanges: map[string]interface{}{
				"ProxyMode": transporttypes.ProxyModeStreamableHTTP,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Setup - create a new scheme for each test to avoid concurrent access
			testScheme := createRunConfigTestScheme()

			fakeClient := fake.NewClientBuilder().WithScheme(testScheme).Build()
			reconciler := &MCPServerReconciler{
				Client: fakeClient,
				Scheme: testScheme,
			}

			// Create initial MCPServer and ConfigMap
			mcpServer := tt.initialServer()
			err := reconciler.ensureRunConfigConfigMap(context.TODO(), mcpServer)
			require.NoError(t, err)

			// Get initial ConfigMap
			configMapName := fmt.Sprintf("%s-runconfig", mcpServer.Name)
			initialConfigMap := &corev1.ConfigMap{}
			err = fakeClient.Get(context.TODO(), types.NamespacedName{
				Name:      configMapName,
				Namespace: mcpServer.Namespace,
			}, initialConfigMap)
			require.NoError(t, err)
			initialChecksum := initialConfigMap.Annotations["toolhive.stacklok.dev/content-checksum"]

			// Modify the MCPServer
			tt.modifyServer(mcpServer)

			// Ensure ConfigMap is updated
			err = reconciler.ensureRunConfigConfigMap(context.TODO(), mcpServer)
			require.NoError(t, err)

			// Verify ConfigMap was updated
			updatedConfigMap := &corev1.ConfigMap{}
			err = fakeClient.Get(context.TODO(), types.NamespacedName{
				Name:      configMapName,
				Namespace: mcpServer.Namespace,
			}, updatedConfigMap)
			require.NoError(t, err)

			// Verify checksum behavior based on test case
			updatedChecksum := updatedConfigMap.Annotations["toolhive.stacklok.dev/content-checksum"]
			if tt.name == "Secret changes" {
				// For secrets changes, checksum should NOT change since secrets are handled via k8s pod patch
				assert.Equal(t, initialChecksum, updatedChecksum, "Checksum should not change for secret changes (secrets handled via k8s pod patch)")
			} else {
				// For other changes, checksum should change
				assert.NotEqual(t, initialChecksum, updatedChecksum, "Checksum should change when content changes")
			}

			// Verify specific changes in RunConfig
			var updatedRunConfig runner.RunConfig
			err = json.Unmarshal([]byte(updatedConfigMap.Data["runconfig.json"]), &updatedRunConfig)
			require.NoError(t, err)

			// Check expected changes using reflection
			runConfigValue := reflect.ValueOf(updatedRunConfig)
			for fieldName, expectedValue := range tt.expectedChanges {
				field := runConfigValue.FieldByName(fieldName)
				require.True(t, field.IsValid(), "Field %s should exist in RunConfig", fieldName)

				actualValue := field.Interface()
				assert.Equal(t, expectedValue, actualValue, "Field %s should have expected value", fieldName)
			}
		})
	}
}

func TestEnsureRunConfigConfigMap_WithVaultInjection(t *testing.T) {
	t.Parallel()

	// Test that EnvFileDir is properly set when Vault Agent Injection is detected
	testCases := []struct {
		name           string
		mcpServer      *mcpv1alpha1.MCPServer
		expectedEnvDir string
	}{
		{
			name: "vault injection in PodTemplateSpec annotations",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vault-server",
					Namespace: "toolhive-system",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "ghcr.io/example/server:v1.0.0",
					Transport: "stdio",
					Port:      8080,
					PodTemplateSpec: &corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								"vault.hashicorp.com/agent-inject": "true",
								"vault.hashicorp.com/role":         "test-role",
							},
						},
					},
				},
			},
			expectedEnvDir: "/vault/secrets",
		},
		{
			name: "vault injection in ResourceOverrides annotations",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vault-override-server",
					Namespace: "toolhive-system",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "ghcr.io/example/server:v1.0.0",
					Transport: "stdio",
					Port:      8080,
					ResourceOverrides: &mcpv1alpha1.ResourceOverrides{
						ProxyDeployment: &mcpv1alpha1.ProxyDeploymentOverrides{
							PodTemplateMetadataOverrides: &mcpv1alpha1.ResourceMetadataOverrides{
								Annotations: map[string]string{
									"vault.hashicorp.com/agent-inject": "true",
									"vault.hashicorp.com/role":         "override-role",
								},
							},
						},
					},
				},
			},
			expectedEnvDir: "/vault/secrets",
		},
		{
			name: "no vault injection - should have empty EnvFileDir",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "no-vault-server",
					Namespace: "toolhive-system",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "ghcr.io/example/server:v1.0.0",
					Transport: "stdio",
					Port:      8080,
				},
			},
			expectedEnvDir: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			testScheme := createRunConfigTestScheme()
			fakeClient := fake.NewClientBuilder().
				WithScheme(testScheme).
				WithRuntimeObjects(tc.mcpServer).
				Build()

			reconciler := &MCPServerReconciler{
				Client: fakeClient,
				Scheme: testScheme,
			}

			// Execute the method under test
			err := reconciler.ensureRunConfigConfigMap(context.TODO(), tc.mcpServer)
			require.NoError(t, err)

			// Verify the ConfigMap exists
			configMapName := fmt.Sprintf("%s-runconfig", tc.mcpServer.Name)
			configMap := &corev1.ConfigMap{}
			err = fakeClient.Get(context.TODO(), types.NamespacedName{
				Name:      configMapName,
				Namespace: tc.mcpServer.Namespace,
			}, configMap)
			require.NoError(t, err)

			// Parse the RunConfig from the ConfigMap
			var runConfig runner.RunConfig
			err = json.Unmarshal([]byte(configMap.Data["runconfig.json"]), &runConfig)
			require.NoError(t, err)

			// Verify EnvFileDir is set correctly
			assert.Equal(t, tc.expectedEnvDir, runConfig.EnvFileDir, "EnvFileDir should match expected value")

			// Verify basic RunConfig fields
			assert.Equal(t, tc.mcpServer.Name, runConfig.Name)
			assert.Equal(t, tc.mcpServer.Spec.Image, runConfig.Image)
		})
	}
}

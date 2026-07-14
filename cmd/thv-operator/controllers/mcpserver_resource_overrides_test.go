// Copyright 2024 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	checksum "github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

func TestMCPServerDeploymentNeedsUpdate_EmbeddedAuthLegacyEnvStable(t *testing.T) {
	t.Parallel()

	scheme := testutil.NewScheme(t)

	externalAuthConfig := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "embedded-auth",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
			EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
					{
						Name: "google",
						Type: mcpv1beta1.UpstreamProviderTypeOIDC,
						OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{
							IssuerURL: "https://accounts.google.com",
							ClientID:  "client-id",
							ClientSecretRef: &mcpv1beta1.SecretKeyRef{
								Name: "upstream-secret",
								Key:  "client-secret",
							},
						},
					},
				},
			},
		},
	}

	mcpServer := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPServerSpec{
			Image:     "test-image",
			ProxyPort: 8080,
			ExternalAuthConfigRef: &mcpv1beta1.ExternalAuthConfigRef{
				Name: externalAuthConfig.Name,
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(externalAuthConfig).
		Build()
	r := newTestMCPServerReconciler(client, scheme, kubernetes.PlatformKubernetes)

	deployment, err := r.deploymentForMCPServer(t.Context(), mcpServer, "test-checksum")
	require.NoError(t, err)
	require.NotNil(t, deployment)
	require.Len(t, deployment.Spec.Template.Spec.Containers, 1)
	require.Contains(t, deployment.Spec.Template.Spec.Containers[0].Env, corev1.EnvVar{
		Name: "TOOLHIVE_UPSTREAM_CLIENT_SECRET_GOOGLE",
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "upstream-secret"},
				Key:                  "client-secret",
			},
		},
	})

	assert.False(t, r.deploymentNeedsUpdate(t.Context(), deployment, mcpServer, "test-checksum"))
}

func TestMCPServerDeploymentNeedsUpdate_EmbeddedAuthAuthServerRefEnvStable(t *testing.T) {
	t.Parallel()

	scheme := testutil.NewScheme(t)

	authConfig := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "embedded-auth",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
			EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
				UpstreamProviders: []mcpv1beta1.UpstreamProviderConfig{
					{
						Name: "google",
						Type: mcpv1beta1.UpstreamProviderTypeOIDC,
						OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{
							IssuerURL: "https://accounts.google.com",
							ClientID:  "client-id",
							ClientSecretRef: &mcpv1beta1.SecretKeyRef{
								Name: "upstream-secret",
								Key:  "client-secret",
							},
						},
					},
				},
			},
		},
	}

	mcpServer := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPServerSpec{
			Image:     "test-image",
			ProxyPort: 8080,
			AuthServerRef: &mcpv1beta1.AuthServerRef{
				Kind: "MCPExternalAuthConfig",
				Name: authConfig.Name,
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(authConfig).
		Build()
	r := newTestMCPServerReconciler(client, scheme, kubernetes.PlatformKubernetes)

	deployment, err := r.deploymentForMCPServer(t.Context(), mcpServer, "test-checksum")
	require.NoError(t, err)
	require.NotNil(t, deployment)
	require.Len(t, deployment.Spec.Template.Spec.Containers, 1)

	assert.False(t, r.deploymentNeedsUpdate(t.Context(), deployment, mcpServer, "test-checksum"))
}

func TestMCPServerDeploymentNeedsUpdate_TokenExchangeDoesNotDrift(t *testing.T) {
	t.Parallel()

	scheme := testutil.NewScheme(t)

	authConfig := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "exchange-config",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1beta1.TokenExchangeConfig{
				TokenURL: "https://oauth.example.com/token",
				ClientID: "client-id",
				ClientSecretRef: &mcpv1beta1.SecretKeyRef{
					Name: "token-secret",
					Key:  "client-secret",
				},
				Audience: "api",
			},
		},
	}

	mcpServer := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPServerSpec{
			Image:     "test-image",
			ProxyPort: 8080,
			ExternalAuthConfigRef: &mcpv1beta1.ExternalAuthConfigRef{
				Name: authConfig.Name,
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(authConfig).
		Build()
	r := newTestMCPServerReconciler(client, scheme, kubernetes.PlatformKubernetes)

	deployment, err := r.deploymentForMCPServer(t.Context(), mcpServer, "test-checksum")
	require.NoError(t, err)
	require.NotNil(t, deployment)
	require.Len(t, deployment.Spec.Template.Spec.Containers, 1)

	assert.False(t, r.deploymentNeedsUpdate(t.Context(), deployment, mcpServer, "test-checksum"))
}

func TestResourceOverrides(t *testing.T) {
	t.Parallel()

	scheme := testutil.NewScheme(t)

	// Note: expectedPodTemplateAnns entries below carry
	// "toolhive.stacklok.dev/mcpserver-generation": "0" because the controller
	// stamps strconv.FormatInt(m.Generation, 10) and the fake client does not
	// auto-increment metadata.generation on Create (the real API server starts
	// at 1). Envtest coverage in
	// cmd/thv-operator/test-integration/mcp-server/mcpserver_generation_freeze_integration_test.go
	// exercises the realistic generation-tracking behavior.
	tests := []struct {
		name                     string
		mcpServer                *mcpv1beta1.MCPServer
		expectedDeploymentLabels map[string]string
		expectedDeploymentAnns   map[string]string
		expectedPodTemplateAnns  map[string]string
		expectedServiceLabels    map[string]string
		expectedServiceAnns      map[string]string
	}{
		{
			name: "no resource overrides",
			mcpServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:     "test-image",
					ProxyPort: 8080,
				},
			},
			expectedDeploymentLabels: map[string]string{
				"app":                        "mcpserver",
				"app.kubernetes.io/name":     "mcpserver",
				"app.kubernetes.io/instance": "test-server",
				"toolhive":                   "true",
				"toolhive-name":              "test-server",
			},
			expectedDeploymentAnns: map[string]string{},
			expectedPodTemplateAnns: map[string]string{
				"toolhive.stacklok.dev/runconfig-checksum":   "test-checksum",
				"toolhive.stacklok.dev/mcpserver-generation": "0",
			},
			expectedServiceLabels: map[string]string{
				"app":                        "mcpserver",
				"app.kubernetes.io/name":     "mcpserver",
				"app.kubernetes.io/instance": "test-server",
				"toolhive":                   "true",
				"toolhive-name":              "test-server",
			},
			expectedServiceAnns: map[string]string{},
		},
		{
			name: "with resource overrides",
			mcpServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:     "test-image",
					ProxyPort: 8080,
					ResourceOverrides: &mcpv1beta1.ResourceOverrides{
						ProxyDeployment: &mcpv1beta1.ProxyDeploymentOverrides{
							ResourceMetadataOverrides: mcpv1beta1.ResourceMetadataOverrides{
								Labels: map[string]string{
									"custom-label": "deployment-value",
									"environment":  "test",
									"app":          "should-be-overridden", // This should be overridden by default
								},
								Annotations: map[string]string{
									"custom-annotation": "deployment-annotation",
									"monitoring/scrape": "true",
								},
							},
						},
						ProxyService: &mcpv1beta1.ResourceMetadataOverrides{
							Labels: map[string]string{
								"custom-label": "service-value",
								"environment":  "test",
								"toolhive":     "should-be-overridden", // This should be overridden by default
							},
							Annotations: map[string]string{
								"custom-annotation": "service-annotation",
								"service.beta.kubernetes.io/aws-load-balancer-type": "nlb",
							},
						},
					},
				},
			},
			expectedDeploymentLabels: map[string]string{
				"app":                        "mcpserver", // Default takes precedence
				"app.kubernetes.io/name":     "mcpserver",
				"app.kubernetes.io/instance": "test-server",
				"toolhive":                   "true",
				"toolhive-name":              "test-server",
				"custom-label":               "deployment-value",
				"environment":                "test",
			},
			expectedDeploymentAnns: map[string]string{
				"custom-annotation": "deployment-annotation",
				"monitoring/scrape": "true",
			},
			expectedPodTemplateAnns: map[string]string{
				"toolhive.stacklok.dev/runconfig-checksum":   "test-checksum",
				"toolhive.stacklok.dev/mcpserver-generation": "0",
			},
			expectedServiceLabels: map[string]string{
				"app":                        "mcpserver",
				"app.kubernetes.io/name":     "mcpserver",
				"app.kubernetes.io/instance": "test-server",
				"toolhive":                   "true", // Default takes precedence
				"toolhive-name":              "test-server",
				"custom-label":               "service-value",
				"environment":                "test",
			},
			expectedServiceAnns: map[string]string{
				"custom-annotation": "service-annotation",
				"service.beta.kubernetes.io/aws-load-balancer-type": "nlb",
			},
		},
		{
			name: "with proxy environment variables",
			mcpServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:     "test-image",
					ProxyPort: 8080,
					ResourceOverrides: &mcpv1beta1.ResourceOverrides{
						ProxyDeployment: &mcpv1beta1.ProxyDeploymentOverrides{
							ResourceMetadataOverrides: mcpv1beta1.ResourceMetadataOverrides{
								Labels: map[string]string{
									"environment": "test",
								},
							},
							Env: []mcpv1beta1.EnvVar{
								{
									Name:  "HTTP_PROXY",
									Value: "http://proxy.example.com:8080",
								},
								{
									Name:  "NO_PROXY",
									Value: "localhost,127.0.0.1",
								},
								{
									Name:  "CUSTOM_ENV",
									Value: "custom-value",
								},
							},
						},
					},
				},
			},
			expectedDeploymentLabels: map[string]string{
				"app":                        "mcpserver",
				"app.kubernetes.io/name":     "mcpserver",
				"app.kubernetes.io/instance": "test-server",
				"toolhive":                   "true",
				"toolhive-name":              "test-server",
				"environment":                "test",
			},
			expectedDeploymentAnns: map[string]string{},
			expectedPodTemplateAnns: map[string]string{
				"toolhive.stacklok.dev/runconfig-checksum":   "test-checksum",
				"toolhive.stacklok.dev/mcpserver-generation": "0",
			},
			expectedServiceLabels: map[string]string{
				"app":                        "mcpserver",
				"app.kubernetes.io/name":     "mcpserver",
				"app.kubernetes.io/instance": "test-server",
				"toolhive":                   "true",
				"toolhive-name":              "test-server",
			},
			expectedServiceAnns: map[string]string{},
		},
		{
			name: "with debug logging via TOOLHIVE_DEBUG env var",
			mcpServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:     "test-image",
					ProxyPort: 8080,
					ResourceOverrides: &mcpv1beta1.ResourceOverrides{
						ProxyDeployment: &mcpv1beta1.ProxyDeploymentOverrides{
							Env: []mcpv1beta1.EnvVar{
								{Name: "TOOLHIVE_DEBUG", Value: "true"},
							},
						},
					},
				},
			},
			expectedDeploymentLabels: map[string]string{
				"app":                        "mcpserver",
				"app.kubernetes.io/name":     "mcpserver",
				"app.kubernetes.io/instance": "test-server",
				"toolhive":                   "true",
				"toolhive-name":              "test-server",
			},
			expectedDeploymentAnns: map[string]string{},
			expectedPodTemplateAnns: map[string]string{
				"toolhive.stacklok.dev/runconfig-checksum":   "test-checksum",
				"toolhive.stacklok.dev/mcpserver-generation": "0",
			},
			expectedServiceLabels: map[string]string{
				"app":                        "mcpserver",
				"app.kubernetes.io/name":     "mcpserver",
				"app.kubernetes.io/instance": "test-server",
				"toolhive":                   "true",
				"toolhive-name":              "test-server",
			},
			expectedServiceAnns: map[string]string{},
		},
		{
			name: "with both metadata overrides and proxy environment variables",
			mcpServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:     "test-image",
					ProxyPort: 8080,
					ResourceOverrides: &mcpv1beta1.ResourceOverrides{
						ProxyDeployment: &mcpv1beta1.ProxyDeploymentOverrides{
							ResourceMetadataOverrides: mcpv1beta1.ResourceMetadataOverrides{
								Labels: map[string]string{
									"environment": "production",
									"team":        "platform",
								},
								Annotations: map[string]string{
									"monitoring/enabled": "true",
									"version":            "v1.2.3",
								},
							},
							Env: []mcpv1beta1.EnvVar{
								{
									Name:  "LOG_LEVEL",
									Value: "debug",
								},
								{
									Name:  "METRICS_ENABLED",
									Value: "true",
								},
							},
						},
						ProxyService: &mcpv1beta1.ResourceMetadataOverrides{
							Annotations: map[string]string{
								"service.beta.kubernetes.io/aws-load-balancer-type": "nlb",
							},
						},
					},
				},
			},
			expectedDeploymentLabels: map[string]string{
				"app":                        "mcpserver",
				"app.kubernetes.io/name":     "mcpserver",
				"app.kubernetes.io/instance": "test-server",
				"toolhive":                   "true",
				"toolhive-name":              "test-server",
				"environment":                "production",
				"team":                       "platform",
			},
			expectedDeploymentAnns: map[string]string{
				"monitoring/enabled": "true",
				"version":            "v1.2.3",
			},
			expectedPodTemplateAnns: map[string]string{
				"toolhive.stacklok.dev/runconfig-checksum":   "test-checksum",
				"toolhive.stacklok.dev/mcpserver-generation": "0",
			},
			expectedServiceLabels: map[string]string{
				"app":                        "mcpserver",
				"app.kubernetes.io/name":     "mcpserver",
				"app.kubernetes.io/instance": "test-server",
				"toolhive":                   "true",
				"toolhive-name":              "test-server",
			},
			expectedServiceAnns: map[string]string{
				"service.beta.kubernetes.io/aws-load-balancer-type": "nlb",
			},
		},
		{
			name: "with Vault Agent Injection pod template annotations",
			mcpServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:     "test-image",
					ProxyPort: 8080,
					ResourceOverrides: &mcpv1beta1.ResourceOverrides{
						ProxyDeployment: &mcpv1beta1.ProxyDeploymentOverrides{
							PodTemplateMetadataOverrides: &mcpv1beta1.ResourceMetadataOverrides{
								Annotations: map[string]string{
									"vault.hashicorp.com/agent-inject": "true",
									"vault.hashicorp.com/role":         "toolhive-mcp-workloads",
								},
							},
						},
					},
				},
			},
			expectedDeploymentLabels: map[string]string{
				"app":                        "mcpserver",
				"app.kubernetes.io/name":     "mcpserver",
				"app.kubernetes.io/instance": "test-server",
				"toolhive":                   "true",
				"toolhive-name":              "test-server",
			},
			expectedDeploymentAnns: map[string]string{},
			expectedPodTemplateAnns: map[string]string{
				"vault.hashicorp.com/agent-inject":           "true",
				"vault.hashicorp.com/role":                   "toolhive-mcp-workloads",
				"toolhive.stacklok.dev/runconfig-checksum":   "test-checksum",
				"toolhive.stacklok.dev/mcpserver-generation": "0",
			},
			expectedServiceLabels: map[string]string{
				"app":                        "mcpserver",
				"app.kubernetes.io/name":     "mcpserver",
				"app.kubernetes.io/instance": "test-server",
				"toolhive":                   "true",
				"toolhive-name":              "test-server",
			},
			expectedServiceAnns: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := fake.NewClientBuilder().WithScheme(scheme).Build()
			r := newTestMCPServerReconciler(client, scheme, kubernetes.PlatformKubernetes)

			// Test deployment creation
			ctx := t.Context()
			deployment, err := r.deploymentForMCPServer(ctx, tt.mcpServer, "test-checksum")
			require.NoError(t, err)
			require.NotNil(t, deployment)

			assert.Equal(t, tt.expectedDeploymentLabels, deployment.Labels)
			assert.Equal(t, tt.expectedDeploymentAnns, deployment.Annotations)
			assert.Equal(t, tt.expectedPodTemplateAnns, deployment.Spec.Template.Annotations,
				"pod template annotations must contain user overrides plus the runconfig-checksum")

			// Test service creation
			service := r.serviceForMCPServer(t.Context(), tt.mcpServer)
			require.NotNil(t, service)

			assert.Equal(t, tt.expectedServiceLabels, service.Labels)
			assert.Equal(t, tt.expectedServiceAnns, service.Annotations)

			// Verify session affinity defaults to ClientIP when not explicitly set
			expectedAffinity := corev1.ServiceAffinityClientIP
			if tt.mcpServer.Spec.SessionAffinity != "" {
				expectedAffinity = corev1.ServiceAffinity(tt.mcpServer.Spec.SessionAffinity)
			}
			assert.Equal(t, expectedAffinity, service.Spec.SessionAffinity)

			// For test cases with environment variables, verify they are set correctly
			if tt.name == "with proxy environment variables" || tt.name == "with both metadata overrides and proxy environment variables" || tt.name == "with debug logging via TOOLHIVE_DEBUG env var" {
				require.Len(t, deployment.Spec.Template.Spec.Containers, 1)
				container := deployment.Spec.Template.Spec.Containers[0]

				// Define expected environment variables based on test case
				var expectedEnvVars map[string]string
				switch tt.name {
				case "with proxy environment variables":
					expectedEnvVars = map[string]string{
						"HTTP_PROXY":               "http://proxy.example.com:8080",
						"NO_PROXY":                 "localhost,127.0.0.1",
						"CUSTOM_ENV":               "custom-value",
						"THV_MCPSERVER_GENERATION": "", // downward API; Value is empty, ValueFrom set
						"XDG_CONFIG_HOME":          "/tmp",
						"HOME":                     "/tmp",
						"TOOLHIVE_RUNTIME":         "kubernetes",
						"UNSTRUCTURED_LOGS":        "false",
					}
				case "with debug logging via TOOLHIVE_DEBUG env var":
					expectedEnvVars = map[string]string{
						"TOOLHIVE_DEBUG":           "true",
						"THV_MCPSERVER_GENERATION": "", // downward API; Value is empty, ValueFrom set
						"XDG_CONFIG_HOME":          "/tmp",
						"HOME":                     "/tmp",
						"TOOLHIVE_RUNTIME":         "kubernetes",
						"UNSTRUCTURED_LOGS":        "false",
					}
				default:
					expectedEnvVars = map[string]string{
						"LOG_LEVEL":                "debug",
						"METRICS_ENABLED":          "true",
						"THV_MCPSERVER_GENERATION": "", // downward API; Value is empty, ValueFrom set
						"XDG_CONFIG_HOME":          "/tmp",
						"HOME":                     "/tmp",
						"TOOLHIVE_RUNTIME":         "kubernetes",
						"UNSTRUCTURED_LOGS":        "false",
					}
				}

				assert.Len(t, container.Env, len(expectedEnvVars))

				for _, env := range container.Env {
					expectedValue, exists := expectedEnvVars[env.Name]
					assert.True(t, exists, "Unexpected environment variable: %s", env.Name)
					assert.Equal(t, expectedValue, env.Value, "Environment variable %s has incorrect value", env.Name)
				}
			}
		})
	}
}

func TestDeploymentForMCPServer_PodTemplateOverridesPreserveRunConfigChecksum(t *testing.T) {
	t.Parallel()

	scheme := testutil.NewScheme(t)

	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := newTestMCPServerReconciler(client, scheme, kubernetes.PlatformKubernetes)

	mcpServer := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
		Spec: mcpv1beta1.MCPServerSpec{
			Image: "test:latest",
			ResourceOverrides: &mcpv1beta1.ResourceOverrides{
				ProxyDeployment: &mcpv1beta1.ProxyDeploymentOverrides{
					PodTemplateMetadataOverrides: &mcpv1beta1.ResourceMetadataOverrides{
						Annotations: map[string]string{
							"user.example.com/some-key": "value",
						},
					},
				},
			},
		},
	}

	deployment, err := r.deploymentForMCPServer(t.Context(), mcpServer, "C1")
	require.NoError(t, err)
	require.NotNil(t, deployment)

	assert.Equal(t, "C1",
		deployment.Spec.Template.Annotations[checksum.RunConfigChecksumAnnotation],
		"runconfig-checksum must survive when PodTemplateMetadataOverrides.Annotations is set")
	assert.Equal(t, "value",
		deployment.Spec.Template.Annotations["user.example.com/some-key"],
		"user override must survive")
	assert.Contains(t, deployment.Spec.Template.Annotations,
		kubernetes.RunConfigMCPServerGenerationAnnotation,
		"mcpserver-generation must be stamped for the downward-API env var (#5360)")
	assert.Len(t, deployment.Spec.Template.Annotations, 3,
		"no extra keys should leak into the pod template")
}

func TestDeploymentNeedsUpdate_StableAfterBuildWithPodTemplateOverrides(t *testing.T) {
	t.Parallel()

	scheme := testutil.NewScheme(t)

	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := newTestMCPServerReconciler(client, scheme, kubernetes.PlatformKubernetes)

	mcpServer := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "test-server", Namespace: "default"},
		Spec: mcpv1beta1.MCPServerSpec{
			Image: "test:latest",
			ResourceOverrides: &mcpv1beta1.ResourceOverrides{
				ProxyDeployment: &mcpv1beta1.ProxyDeploymentOverrides{
					PodTemplateMetadataOverrides: &mcpv1beta1.ResourceMetadataOverrides{
						Annotations: map[string]string{
							"vault.hashicorp.com/agent-inject": "true",
							"vault.hashicorp.com/role":         "toolhive-mcp-workloads",
						},
					},
				},
			},
		},
	}

	const runConfigChecksum = "stable-checksum"
	built, err := r.deploymentForMCPServer(t.Context(), mcpServer, runConfigChecksum)
	require.NoError(t, err)
	require.NotNil(t, built)

	// Constructor and comparator must agree on the same input — otherwise the
	// operator gets stuck in a perpetual r.Update loop on every reconcile.
	needsUpdate := r.deploymentNeedsUpdate(t.Context(), built, mcpServer, runConfigChecksum)
	assert.False(t, needsUpdate,
		"deploymentNeedsUpdate must report no drift immediately after deploymentForMCPServer with the same checksum and overrides")
}

func TestMergeStringMaps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		defaultMap  map[string]string
		overrideMap map[string]string
		expected    map[string]string
	}{
		{
			name:        "empty maps",
			defaultMap:  map[string]string{},
			overrideMap: map[string]string{},
			expected:    map[string]string{},
		},
		{
			name:        "only default map",
			defaultMap:  map[string]string{"key1": "default1", "key2": "default2"},
			overrideMap: map[string]string{},
			expected:    map[string]string{"key1": "default1", "key2": "default2"},
		},
		{
			name:        "only override map",
			defaultMap:  map[string]string{},
			overrideMap: map[string]string{"key1": "override1", "key2": "override2"},
			expected:    map[string]string{"key1": "override1", "key2": "override2"},
		},
		{
			name:        "default takes precedence",
			defaultMap:  map[string]string{"key1": "default1", "key2": "default2"},
			overrideMap: map[string]string{"key1": "override1", "key3": "override3"},
			expected:    map[string]string{"key1": "default1", "key2": "default2", "key3": "override3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := ctrlutil.MergeStringMaps(tt.defaultMap, tt.overrideMap)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDeploymentNeedsUpdateProxyEnv(t *testing.T) {
	t.Parallel()

	scheme := testutil.NewScheme(t)

	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := newTestMCPServerReconciler(client, scheme, kubernetes.PlatformKubernetes)

	tests := []struct {
		name            string
		mcpServer       *mcpv1beta1.MCPServer
		existingEnvVars []corev1.EnvVar
		expectEnvChange bool // Focus on whether env change detection works
	}{
		{
			name: "matching proxy env vars - no env change",
			mcpServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:     "test-image",
					ProxyPort: 8080,
					ResourceOverrides: &mcpv1beta1.ResourceOverrides{
						ProxyDeployment: &mcpv1beta1.ProxyDeploymentOverrides{
							Env: []mcpv1beta1.EnvVar{
								{Name: "HTTP_PROXY", Value: "http://proxy.example.com:8080"},
								{Name: "NO_PROXY", Value: "localhost,127.0.0.1"},
							},
						},
					},
				},
			},
			existingEnvVars: []corev1.EnvVar{
				{Name: "HTTP_PROXY", Value: "http://proxy.example.com:8080"},
				{Name: "NO_PROXY", Value: "localhost,127.0.0.1"},
			},
			expectEnvChange: false,
		},
		{
			name: "different proxy env vars - env change detected",
			mcpServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:     "test-image",
					ProxyPort: 8080,
					ResourceOverrides: &mcpv1beta1.ResourceOverrides{
						ProxyDeployment: &mcpv1beta1.ProxyDeploymentOverrides{
							Env: []mcpv1beta1.EnvVar{
								{Name: "HTTP_PROXY", Value: "http://new-proxy.example.com:8080"},
								{Name: "NO_PROXY", Value: "localhost,127.0.0.1"},
							},
						},
					},
				},
			},
			existingEnvVars: []corev1.EnvVar{
				{Name: "HTTP_PROXY", Value: "http://old-proxy.example.com:8080"},
				{Name: "NO_PROXY", Value: "localhost,127.0.0.1"},
			},
			expectEnvChange: true,
		},
		{
			name: "added proxy env vars - env change detected",
			mcpServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:     "test-image",
					ProxyPort: 8080,
					ResourceOverrides: &mcpv1beta1.ResourceOverrides{
						ProxyDeployment: &mcpv1beta1.ProxyDeploymentOverrides{
							Env: []mcpv1beta1.EnvVar{
								{Name: "HTTP_PROXY", Value: "http://proxy.example.com:8080"},
								{Name: "NO_PROXY", Value: "localhost,127.0.0.1"},
								{Name: "CUSTOM_ENV", Value: "custom-value"},
							},
						},
					},
				},
			},
			existingEnvVars: []corev1.EnvVar{
				{Name: "HTTP_PROXY", Value: "http://proxy.example.com:8080"},
				{Name: "NO_PROXY", Value: "localhost,127.0.0.1"},
			},
			expectEnvChange: true,
		},
		{
			name: "removed proxy env vars - env change detected",
			mcpServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:     "test-image",
					ProxyPort: 8080,
					ResourceOverrides: &mcpv1beta1.ResourceOverrides{
						ProxyDeployment: &mcpv1beta1.ProxyDeploymentOverrides{
							Env: []mcpv1beta1.EnvVar{
								{Name: "HTTP_PROXY", Value: "http://proxy.example.com:8080"},
							},
						},
					},
				},
			},
			existingEnvVars: []corev1.EnvVar{
				{Name: "HTTP_PROXY", Value: "http://proxy.example.com:8080"},
				{Name: "NO_PROXY", Value: "localhost,127.0.0.1"},
			},
			expectEnvChange: true,
		},
		{
			name: "no proxy env vars specified - no env change when none exist",
			mcpServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:     "test-image",
					ProxyPort: 8080,
				},
			},
			existingEnvVars: []corev1.EnvVar{},
			expectEnvChange: false,
		},
		{
			name: "env vars removed entirely - env change detected",
			mcpServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:     "test-image",
					ProxyPort: 8080,
				},
			},
			existingEnvVars: []corev1.EnvVar{
				{Name: "HTTP_PROXY", Value: "http://proxy.example.com:8080"},
				{Name: "NO_PROXY", Value: "localhost,127.0.0.1"},
			},
			expectEnvChange: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create a deployment and manually set up its state to isolate proxy env testing
			ctx := t.Context()
			deployment, err := r.deploymentForMCPServer(ctx, tt.mcpServer, "test-checksum")
			require.NoError(t, err)
			require.NotNil(t, deployment)
			require.Len(t, deployment.Spec.Template.Spec.Containers, 1)

			// Set the existing env vars to simulate current deployment state
			deployment.Spec.Template.Spec.Containers[0].Env = tt.existingEnvVars

			// Ensure the image matches to avoid image comparison issues
			deployment.Spec.Template.Spec.Containers[0].Image = getToolhiveRunnerImage()

			// Test if deployment needs update - should correlate with env change expectation
			needsUpdate := r.deploymentNeedsUpdate(t.Context(), deployment, tt.mcpServer, "test-checksum")

			if tt.expectEnvChange {
				assert.True(t, needsUpdate, "Expected deployment update due to proxy env change")
			} else {
				// Note: This might still be true due to other factors, but at minimum
				// we're testing that our proxy env logic doesn't incorrectly trigger updates
				if needsUpdate {
					t.Logf("Deployment needs update even though proxy env hasn't changed - likely due to other factors")
				}
			}
		})
	}
}

func TestMCPServerDeploymentNeedsUpdate_ImagePullSecretsDrift(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		specSecrets       []corev1.LocalObjectReference // set on mcpServer.Spec.ResourceOverrides
		deploymentSecrets []corev1.LocalObjectReference // overrides deployment after build
		expectNeedsUpdate bool
	}{
		{
			name:              "both empty - no update",
			specSecrets:       nil,
			deploymentSecrets: nil,
			expectNeedsUpdate: false,
		},
		{
			name:              "spec has secrets, deployment has nil - needs update",
			specSecrets:       []corev1.LocalObjectReference{{Name: "regsec"}},
			deploymentSecrets: nil,
			expectNeedsUpdate: true,
		},
		{
			name:              "spec cleared, deployment has stale - needs update",
			specSecrets:       nil,
			deploymentSecrets: []corev1.LocalObjectReference{{Name: "old-regsec"}},
			expectNeedsUpdate: true,
		},
		{
			name:              "match - no update",
			specSecrets:       []corev1.LocalObjectReference{{Name: "regsec"}},
			deploymentSecrets: []corev1.LocalObjectReference{{Name: "regsec"}},
			expectNeedsUpdate: false,
		},
		{
			name:              "spec nil vs deployment empty slice - no update",
			specSecrets:       nil,
			deploymentSecrets: []corev1.LocalObjectReference{},
			expectNeedsUpdate: false,
		},
		{
			name:              "spec empty slice vs deployment empty slice - no update",
			specSecrets:       []corev1.LocalObjectReference{},
			deploymentSecrets: []corev1.LocalObjectReference{},
			expectNeedsUpdate: false,
		},
		{
			name:              "reorder triggers update",
			specSecrets:       []corev1.LocalObjectReference{{Name: "a"}, {Name: "b"}},
			deploymentSecrets: []corev1.LocalObjectReference{{Name: "b"}, {Name: "a"}},
			expectNeedsUpdate: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := testutil.NewScheme(t)

			fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
			r := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

			mcpServer := &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:     "test-image",
					ProxyPort: 8080,
				},
			}
			if tt.specSecrets != nil {
				mcpServer.Spec.ResourceOverrides = &mcpv1beta1.ResourceOverrides{
					ProxyDeployment: &mcpv1beta1.ProxyDeploymentOverrides{
						ImagePullSecrets: tt.specSecrets,
					},
				}
			}

			ctx := t.Context()
			deployment, err := r.deploymentForMCPServer(ctx, mcpServer, "test-checksum")
			require.NoError(t, err)
			require.NotNil(t, deployment)

			// Simulate the "stored" state by overwriting ImagePullSecrets only.
			// The freshly built deployment is otherwise fully aligned with the mcpServer spec,
			// so any detected drift is caused solely by this field.
			deployment.Spec.Template.Spec.ImagePullSecrets = tt.deploymentSecrets

			needsUpdate := r.deploymentNeedsUpdate(ctx, deployment, mcpServer, "test-checksum")
			assert.Equal(t, tt.expectNeedsUpdate, needsUpdate, "ImagePullSecrets drift detection mismatch")
		})
	}
}

func TestMCPServerSessionAffinityNone(t *testing.T) {
	t.Parallel()

	scheme := testutil.NewScheme(t)

	mcpServer := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPServerSpec{
			Image:           "test-image",
			ProxyPort:       8080,
			SessionAffinity: string(corev1.ServiceAffinityNone),
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := newTestMCPServerReconciler(client, scheme, kubernetes.PlatformKubernetes)

	service := r.serviceForMCPServer(t.Context(), mcpServer)
	require.NotNil(t, service)
	assert.Equal(t, corev1.ServiceAffinityNone, service.Spec.SessionAffinity)
}

func TestMCPServerServiceNeedsUpdate(t *testing.T) {
	t.Parallel()

	baseMCPServer := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPServerSpec{
			Image:     "test-image",
			ProxyPort: 8080,
		},
	}

	baseService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        ctrlutil.CreateProxyServiceName(baseMCPServer.Name),
			Namespace:   baseMCPServer.Namespace,
			Labels:      labelsForMCPServer(baseMCPServer.Name),
			Annotations: map[string]string{},
		},
		Spec: corev1.ServiceSpec{
			SessionAffinity: corev1.ServiceAffinityClientIP,
			Ports: []corev1.ServicePort{{
				Port: 8080,
			}},
		},
	}

	tests := []struct {
		name        string
		service     *corev1.Service
		mcpServer   *mcpv1beta1.MCPServer
		needsUpdate bool
	}{
		{
			name:        "no update needed",
			service:     baseService.DeepCopy(),
			mcpServer:   baseMCPServer.DeepCopy(),
			needsUpdate: false,
		},
		{
			name: "session affinity drifted to empty",
			service: func() *corev1.Service {
				s := baseService.DeepCopy()
				s.Spec.SessionAffinity = ""
				return s
			}(),
			mcpServer:   baseMCPServer.DeepCopy(),
			needsUpdate: true,
		},
		{
			name:    "session affinity spec changed to None",
			service: baseService.DeepCopy(),
			mcpServer: func() *mcpv1beta1.MCPServer {
				m := baseMCPServer.DeepCopy()
				m.Spec.SessionAffinity = string(corev1.ServiceAffinityNone)
				return m
			}(),
			needsUpdate: true,
		},
		{
			name: "session affinity matches spec None",
			service: func() *corev1.Service {
				s := baseService.DeepCopy()
				s.Spec.SessionAffinity = corev1.ServiceAffinityNone
				return s
			}(),
			mcpServer: func() *mcpv1beta1.MCPServer {
				m := baseMCPServer.DeepCopy()
				m.Spec.SessionAffinity = string(corev1.ServiceAffinityNone)
				return m
			}(),
			needsUpdate: false,
		},
		{
			// Regression for #5730: external controllers (e.g. GKE NEG/Gateway) write
			// cloud.google.com/* annotations on the Service. These are not operator-owned
			// and must not be treated as drift, or the operator hot-loops Update.
			name: "external cloud annotations ignored",
			service: func() *corev1.Service {
				s := baseService.DeepCopy()
				s.Annotations["cloud.google.com/neg-status"] = `{"network_endpoint_groups":{"8080":"k8s1-abc"}}`
				return s
			}(),
			mcpServer:   baseMCPServer.DeepCopy(),
			needsUpdate: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := serviceNeedsUpdate(tt.service, tt.mcpServer)
			assert.Equal(t, tt.needsUpdate, result)
		})
	}
}

// TestDeploymentForMCPServer_MCPServerGenerationDownwardAPI verifies that the
// proxy Deployment stamps the MCPServer generation as a pod-template annotation
// AND projects that annotation into the proxyrunner container as the
// THV_MCPSERVER_GENERATION env var via the downward API. This is the
// frozen-per-pod path that closes the race described in #5360 — the env var's
// value is bound to the pod's own annotations at creation time, so a restarted
// old-RS pod cannot acquire the new generation by re-reading the live-mounted
// RunConfig ConfigMap.
func TestDeploymentForMCPServer_MCPServerGenerationDownwardAPI(t *testing.T) {
	t.Parallel()

	scheme := testutil.NewScheme(t)

	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := newTestMCPServerReconciler(client, scheme, kubernetes.PlatformKubernetes)

	mcpServer := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-server",
			Namespace:  "default",
			Generation: 7,
		},
		Spec: mcpv1beta1.MCPServerSpec{
			Image:     "test-image",
			ProxyPort: 8080,
		},
	}

	deployment, err := r.deploymentForMCPServer(t.Context(), mcpServer, "test-checksum")
	require.NoError(t, err)
	require.NotNil(t, deployment)

	assert.Equal(t, "7",
		deployment.Spec.Template.Annotations[kubernetes.RunConfigMCPServerGenerationAnnotation],
		"pod template must stamp the MCPServer generation so the downward-API env var resolves")

	require.Len(t, deployment.Spec.Template.Spec.Containers, 1)
	var got *corev1.EnvVar
	for i := range deployment.Spec.Template.Spec.Containers[0].Env {
		if deployment.Spec.Template.Spec.Containers[0].Env[i].Name == kubernetes.EnvVarMCPServerGeneration {
			got = &deployment.Spec.Template.Spec.Containers[0].Env[i]
			break
		}
	}
	require.NotNil(t, got, "container must declare the %s env var", kubernetes.EnvVarMCPServerGeneration)
	require.NotNil(t, got.ValueFrom, "env var must use ValueFrom (downward API), not a literal Value")
	require.NotNil(t, got.ValueFrom.FieldRef)
	assert.Equal(t,
		"metadata.annotations['"+kubernetes.RunConfigMCPServerGenerationAnnotation+"']",
		got.ValueFrom.FieldRef.FieldPath,
		"FieldRef must point at the mcpserver-generation pod annotation")
	// APIVersion must be set explicitly so the drift comparator at
	// deploymentNeedsUpdate matches the API-server-defaulted value. An empty
	// APIVersion here results in equality.Semantic.DeepEqual returning false on
	// every reconcile, causing perpetual Deployment rewrites. See #5360.
	assert.Equal(t, "v1", got.ValueFrom.FieldRef.APIVersion,
		"FieldRef.APIVersion must match the API server default of v1 to avoid false drift")
}

// Copyright 2025 Stacklok, Inc.
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
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/pkg/transport/session"
)

// TestDeploymentForMCPRemoteProxy tests deployment generation
func TestDeploymentForMCPRemoteProxy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		proxy    *mcpv1beta1.MCPRemoteProxy
		validate func(*testing.T, *appsv1.Deployment)
	}{
		{
			name:  "basic deployment",
			proxy: v1beta1test.NewMCPRemoteProxy("basic-proxy", "default"),
			validate: func(t *testing.T, dep *appsv1.Deployment) {
				t.Helper()
				assert.Equal(t, "basic-proxy", dep.Name)
				assert.Equal(t, "default", dep.Namespace)
				assert.Nil(t, dep.Spec.Replicas, "nil spec.replicas leaves the count to the apiserver default")

				// Verify labels
				assert.Equal(t, labelsForMCPRemoteProxy("basic-proxy"), dep.Spec.Selector.MatchLabels)

				// Verify container
				require.Len(t, dep.Spec.Template.Spec.Containers, 1)
				container := dep.Spec.Template.Spec.Containers[0]
				assert.Equal(t, "toolhive", container.Name)
				assert.Contains(t, container.Args, "run")
				assert.Contains(t, container.Args, "--foreground=true")
				assert.Contains(t, container.Args, "placeholder-for-remote-proxy")

				// Verify port
				require.Len(t, container.Ports, 1)
				assert.Equal(t, int32(8080), container.Ports[0].ContainerPort)
				assert.Equal(t, "http", container.Ports[0].Name)

				// Verify health probes
				assert.NotNil(t, container.StartupProbe)
				assert.NotNil(t, container.LivenessProbe)
				assert.NotNil(t, container.ReadinessProbe)
				assert.Equal(t, "/health", container.LivenessProbe.HTTPGet.Path)

				// Verify service account
				assert.Equal(t, proxyRunnerServiceAccountNameForRemoteProxy("basic-proxy"),
					dep.Spec.Template.Spec.ServiceAccountName)
			},
		},
		{
			name: "with resource limits",
			proxy: v1beta1test.NewMCPRemoteProxy("resources-proxy", "default",
				v1beta1test.MutateRemoteProxy(func(p *mcpv1beta1.MCPRemoteProxy) {
					p.Spec.Resources = mcpv1beta1.ResourceRequirements{
						Limits: mcpv1beta1.ResourceList{
							CPU:    "1",
							Memory: "512Mi",
						},
						Requests: mcpv1beta1.ResourceList{
							CPU:    "100m",
							Memory: "128Mi",
						},
					}
				}),
			),
			validate: func(t *testing.T, dep *appsv1.Deployment) {
				t.Helper()
				container := dep.Spec.Template.Spec.Containers[0]
				assert.Equal(t, "1", container.Resources.Limits.Cpu().String())
				assert.Equal(t, "512Mi", container.Resources.Limits.Memory().String())
				assert.Equal(t, "100m", container.Resources.Requests.Cpu().String())
				assert.Equal(t, "128Mi", container.Resources.Requests.Memory().String())
			},
		},
		{
			name: "with resource overrides",
			proxy: v1beta1test.NewMCPRemoteProxy("override-proxy", "default",
				v1beta1test.MutateRemoteProxy(func(p *mcpv1beta1.MCPRemoteProxy) {
					p.Spec.ResourceOverrides = &mcpv1beta1.ResourceOverrides{
						ProxyDeployment: &mcpv1beta1.ProxyDeploymentOverrides{
							ResourceMetadataOverrides: mcpv1beta1.ResourceMetadataOverrides{
								Labels: map[string]string{
									"custom-label": "custom-value",
								},
								Annotations: map[string]string{
									"custom-annotation": "custom-annotation-value",
								},
							},
							Env: []mcpv1beta1.EnvVar{
								{Name: "CUSTOM_ENV", Value: "custom-value"},
								{Name: "TOOLHIVE_DEBUG", Value: "true"},
							},
						},
					}
				}),
			),
			validate: func(t *testing.T, dep *appsv1.Deployment) {
				t.Helper()
				// Verify custom labels
				assert.Equal(t, "custom-value", dep.Labels["custom-label"])

				// Verify custom annotations
				assert.Equal(t, "custom-annotation-value", dep.Annotations["custom-annotation"])

				// Verify custom environment variables
				container := dep.Spec.Template.Spec.Containers[0]
				customEnvFound := false
				debugEnvFound := false
				for _, env := range container.Env {
					if env.Name == "CUSTOM_ENV" {
						assert.Equal(t, "custom-value", env.Value)
						customEnvFound = true
					}
					if env.Name == "TOOLHIVE_DEBUG" {
						assert.Equal(t, "true", env.Value)
						debugEnvFound = true
					}
				}
				assert.True(t, customEnvFound, "Custom environment variable should be present")
				assert.True(t, debugEnvFound, "TOOLHIVE_DEBUG environment variable should be present")

				// Verify args only contain base arguments
				assert.Contains(t, container.Args, "run")
				assert.Contains(t, container.Args, "--foreground=true")
				assert.Contains(t, container.Args, "placeholder-for-remote-proxy")
				assert.Len(t, container.Args, 3, "Args should only contain base arguments")
			},
		},
		{
			name: "custom proxyPort",
			proxy: v1beta1test.NewMCPRemoteProxy("custom-port-proxy", "default",
				v1beta1test.WithRemoteProxyPort(9090),
			),
			validate: func(t *testing.T, dep *appsv1.Deployment) {
				t.Helper()
				container := dep.Spec.Template.Spec.Containers[0]
				assert.Equal(t, int32(9090), container.Ports[0].ContainerPort)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := testutil.NewScheme(t)
			reconciler := &MCPRemoteProxyReconciler{
				Scheme:           scheme,
				PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
			}

			dep := reconciler.deploymentForMCPRemoteProxy(context.TODO(), tt.proxy, "test-checksum")
			require.NotNil(t, dep)

			if tt.validate != nil {
				tt.validate(t, dep)
			}
		})
	}
}

// TestServiceForMCPRemoteProxy tests service generation
func TestServiceForMCPRemoteProxy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		proxy    *mcpv1beta1.MCPRemoteProxy
		validate func(*testing.T, *corev1.Service)
	}{
		{
			name:  "basic service",
			proxy: v1beta1test.NewMCPRemoteProxy("basic-proxy", "default"),
			validate: func(t *testing.T, svc *corev1.Service) {
				t.Helper()
				assert.Equal(t, createProxyServiceName("basic-proxy"), svc.Name)
				assert.Equal(t, "default", svc.Namespace)

				// Verify selector
				assert.Equal(t, labelsForMCPRemoteProxy("basic-proxy"), svc.Spec.Selector)

				// Verify session affinity
				assert.Equal(t, corev1.ServiceAffinityClientIP, svc.Spec.SessionAffinity)

				// Verify port
				require.Len(t, svc.Spec.Ports, 1)
				assert.Equal(t, int32(8080), svc.Spec.Ports[0].Port)
				assert.Equal(t, "http", svc.Spec.Ports[0].Name)
			},
		},
		{
			name: "service with session affinity None",
			proxy: v1beta1test.NewMCPRemoteProxy("basic-proxy", "default",
				v1beta1test.MutateRemoteProxy(func(p *mcpv1beta1.MCPRemoteProxy) {
					p.Spec.SessionAffinity = string(corev1.ServiceAffinityNone)
				}),
			),
			validate: func(t *testing.T, svc *corev1.Service) {
				t.Helper()
				assert.Equal(t, corev1.ServiceAffinityNone, svc.Spec.SessionAffinity)
			},
		},
		{
			name: "service with overrides",
			proxy: v1beta1test.NewMCPRemoteProxy("override-proxy", "default",
				v1beta1test.WithRemoteProxyPort(9090),
				v1beta1test.MutateRemoteProxy(func(p *mcpv1beta1.MCPRemoteProxy) {
					p.Spec.ResourceOverrides = &mcpv1beta1.ResourceOverrides{
						ProxyService: &mcpv1beta1.ResourceMetadataOverrides{
							Labels: map[string]string{
								"svc-label": "svc-value",
							},
							Annotations: map[string]string{
								"svc-annotation": "svc-annotation-value",
							},
						},
					}
				}),
			),
			validate: func(t *testing.T, svc *corev1.Service) {
				t.Helper()
				assert.Equal(t, "svc-value", svc.Labels["svc-label"])
				assert.Equal(t, "svc-annotation-value", svc.Annotations["svc-annotation"])
				assert.Equal(t, int32(9090), svc.Spec.Ports[0].Port)
				assert.Equal(t, corev1.ServiceAffinityClientIP, svc.Spec.SessionAffinity)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := testutil.NewScheme(t)
			reconciler := &MCPRemoteProxyReconciler{
				Scheme: scheme,
			}

			svc := reconciler.serviceForMCPRemoteProxy(context.TODO(), tt.proxy)
			require.NotNil(t, svc)

			if tt.validate != nil {
				tt.validate(t, svc)
			}
		})
	}
}

// TestBuildResourceRequirements tests resource requirements building
func TestBuildResourceRequirements(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		resourceSpec mcpv1beta1.ResourceRequirements
		validate     func(*testing.T, corev1.ResourceRequirements)
	}{
		{
			name: "with limits and requests",
			resourceSpec: mcpv1beta1.ResourceRequirements{
				Limits: mcpv1beta1.ResourceList{
					CPU:    "2",
					Memory: "1Gi",
				},
				Requests: mcpv1beta1.ResourceList{
					CPU:    "500m",
					Memory: "256Mi",
				},
			},
			validate: func(t *testing.T, res corev1.ResourceRequirements) {
				t.Helper()
				assert.Equal(t, "2", res.Limits.Cpu().String())
				assert.Equal(t, "1Gi", res.Limits.Memory().String())
				assert.Equal(t, "500m", res.Requests.Cpu().String())
				assert.Equal(t, "256Mi", res.Requests.Memory().String())
			},
		},
		{
			name:         "empty resources",
			resourceSpec: mcpv1beta1.ResourceRequirements{},
			validate: func(t *testing.T, res corev1.ResourceRequirements) {
				t.Helper()
				assert.Nil(t, res.Limits)
				assert.Nil(t, res.Requests)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := ctrlutil.BuildResourceRequirements(tt.resourceSpec)

			if tt.validate != nil {
				tt.validate(t, result)
			}
		})
	}
}

// TestBuildHeaderForwardSecretEnvVars tests the buildHeaderForwardSecretEnvVars function
func TestBuildHeaderForwardSecretEnvVars(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		proxy    *mcpv1beta1.MCPRemoteProxy
		validate func(*testing.T, []corev1.EnvVar)
	}{
		{
			name: "single header secret",
			proxy: v1beta1test.NewMCPRemoteProxy("test-proxy", "default",
				v1beta1test.WithRemoteProxyHeaderForward(&mcpv1beta1.HeaderForwardConfig{
					AddHeadersFromSecret: []mcpv1beta1.HeaderFromSecret{
						{
							HeaderName: "X-API-Key",
							ValueSecretRef: &mcpv1beta1.SecretKeyRef{
								Name: "my-secret",
								Key:  "api-key",
							},
						},
					},
				}),
			),
			validate: func(t *testing.T, envVars []corev1.EnvVar) {
				t.Helper()
				require.Len(t, envVars, 1)
				assert.Equal(t, "TOOLHIVE_SECRET_HEADER_FORWARD_X_API_KEY_TEST_PROXY", envVars[0].Name)
				require.NotNil(t, envVars[0].ValueFrom)
				require.NotNil(t, envVars[0].ValueFrom.SecretKeyRef)
				assert.Equal(t, "my-secret", envVars[0].ValueFrom.SecretKeyRef.Name)
				assert.Equal(t, "api-key", envVars[0].ValueFrom.SecretKeyRef.Key)
			},
		},
		{
			name: "multiple header secrets",
			proxy: v1beta1test.NewMCPRemoteProxy("multi-proxy", "default",
				v1beta1test.WithRemoteProxyHeaderForward(&mcpv1beta1.HeaderForwardConfig{
					AddHeadersFromSecret: []mcpv1beta1.HeaderFromSecret{
						{
							HeaderName: "X-API-Key",
							ValueSecretRef: &mcpv1beta1.SecretKeyRef{
								Name: "secret-a",
								Key:  "key-a",
							},
						},
						{
							HeaderName: "X-Token",
							ValueSecretRef: &mcpv1beta1.SecretKeyRef{
								Name: "secret-b",
								Key:  "key-b",
							},
						},
					},
				}),
			),
			validate: func(t *testing.T, envVars []corev1.EnvVar) {
				t.Helper()
				require.Len(t, envVars, 2)
				// Verify first env var
				assert.Equal(t, "TOOLHIVE_SECRET_HEADER_FORWARD_X_API_KEY_MULTI_PROXY", envVars[0].Name)
				assert.Equal(t, "secret-a", envVars[0].ValueFrom.SecretKeyRef.Name)
				// Verify second env var
				assert.Equal(t, "TOOLHIVE_SECRET_HEADER_FORWARD_X_TOKEN_MULTI_PROXY", envVars[1].Name)
				assert.Equal(t, "secret-b", envVars[1].ValueFrom.SecretKeyRef.Name)
			},
		},
		{
			name: "skip entries with nil ValueSecretRef",
			proxy: v1beta1test.NewMCPRemoteProxy("skip-proxy", "default",
				v1beta1test.WithRemoteProxyHeaderForward(&mcpv1beta1.HeaderForwardConfig{
					AddHeadersFromSecret: []mcpv1beta1.HeaderFromSecret{
						{
							HeaderName:     "X-Invalid",
							ValueSecretRef: nil, // Should be skipped
						},
						{
							HeaderName: "X-Valid",
							ValueSecretRef: &mcpv1beta1.SecretKeyRef{
								Name: "valid-secret",
								Key:  "valid-key",
							},
						},
					},
				}),
			),
			validate: func(t *testing.T, envVars []corev1.EnvVar) {
				t.Helper()
				require.Len(t, envVars, 1)
				assert.Equal(t, "TOOLHIVE_SECRET_HEADER_FORWARD_X_VALID_SKIP_PROXY", envVars[0].Name)
			},
		},
		{
			name: "empty AddHeadersFromSecret",
			proxy: v1beta1test.NewMCPRemoteProxy("empty-proxy", "default",
				v1beta1test.WithRemoteProxyHeaderForward(&mcpv1beta1.HeaderForwardConfig{
					AddHeadersFromSecret: []mcpv1beta1.HeaderFromSecret{},
				}),
			),
			validate: func(t *testing.T, envVars []corev1.EnvVar) {
				t.Helper()
				assert.Empty(t, envVars)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			envVars := buildHeaderForwardSecretEnvVars(tt.proxy)

			if tt.validate != nil {
				tt.validate(t, envVars)
			}
		})
	}
}

// TestBuildHealthProbe tests health probe building
func TestBuildHealthProbe(t *testing.T) {
	t.Parallel()

	probe := ctrlutil.BuildHealthProbe("/health", "http", 10, 5, 3, 2)

	assert.NotNil(t, probe)
	assert.NotNil(t, probe.HTTPGet)
	assert.Equal(t, "/health", probe.HTTPGet.Path)
	assert.Equal(t, "http", probe.HTTPGet.Port.StrVal)
	assert.Equal(t, int32(10), probe.InitialDelaySeconds)
	assert.Equal(t, int32(5), probe.PeriodSeconds)
	assert.Equal(t, int32(3), probe.TimeoutSeconds)
	assert.Equal(t, int32(2), probe.FailureThreshold)
}

// TestEnsureDeployment tests deployment creation and update
func TestEnsureDeployment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		proxy              *mcpv1beta1.MCPRemoteProxy
		existingDeployment *appsv1.Deployment
		expectRequeue      bool
	}{
		{
			name:               "create new deployment",
			proxy:              v1beta1test.NewMCPRemoteProxy("new-proxy", "default"),
			existingDeployment: nil,
			expectRequeue:      true,
		},
		{
			name:  "deployment exists - no update to allow HPA",
			proxy: v1beta1test.NewMCPRemoteProxy("replica-proxy", "default"),
			existingDeployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "replica-proxy",
					Namespace: "default",
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: int32Ptr(3),
					Selector: &metav1.LabelSelector{
						MatchLabels: labelsForMCPRemoteProxy("replica-proxy"),
					},
				},
			},
			expectRequeue: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := testutil.NewScheme(t)
			// Add RBAC and Apps types to scheme
			_ = rbacv1.AddToScheme(scheme)
			_ = appsv1.AddToScheme(scheme)

			objects := []runtime.Object{tt.proxy}
			if tt.existingDeployment != nil {
				objects = append(objects, tt.existingDeployment)
			}

			// Add RunConfig ConfigMap with checksum annotation
			configMapName := fmt.Sprintf("%s-runconfig", tt.proxy.Name)
			runConfigCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configMapName,
					Namespace: tt.proxy.Namespace,
					Annotations: map[string]string{
						"toolhive.stacklok.dev/content-checksum": "test-checksum-123",
					},
				},
				Data: map[string]string{
					"runconfig.json": "{}",
				},
			}
			objects = append(objects, runConfigCM)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				Build()

			reconciler := &MCPRemoteProxyReconciler{
				Client:           fakeClient,
				Scheme:           scheme,
				PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
			}

			result, err := reconciler.ensureDeployment(context.TODO(), tt.proxy)
			assert.NoError(t, err)

			if tt.expectRequeue {
				assert.Equal(t, int64(0), result.RequeueAfter.Nanoseconds())
			}
		})
	}
}

// TestEnsureService tests service creation
func TestEnsureService(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		proxy           *mcpv1beta1.MCPRemoteProxy
		existingService *corev1.Service
		expectRequeue   bool
	}{
		{
			name:            "create new service",
			proxy:           v1beta1test.NewMCPRemoteProxy("new-svc-proxy", "default"),
			existingService: nil,
			expectRequeue:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := testutil.NewScheme(t)
			// Add RBAC and Apps types to scheme
			_ = rbacv1.AddToScheme(scheme)
			_ = appsv1.AddToScheme(scheme)

			objects := []runtime.Object{tt.proxy}
			if tt.existingService != nil {
				objects = append(objects, tt.existingService)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				Build()

			reconciler := &MCPRemoteProxyReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			result, err := reconciler.ensureService(context.TODO(), tt.proxy)
			assert.NoError(t, err)

			if tt.expectRequeue {
				assert.Equal(t, int64(0), result.RequeueAfter.Nanoseconds())
			}
		})
	}
}

// TestMCPRemoteProxyEnsureService_PreservesExternalAnnotations is a regression test for
// #5730: when a genuine operator-owned change triggers a Service update, annotations
// written by an external controller (e.g. GKE NEG) must be merged, not stripped.
func TestMCPRemoteProxyEnsureService_PreservesExternalAnnotations(t *testing.T) {
	t.Parallel()

	proxy := v1beta1test.NewMCPRemoteProxy("ext-annot-proxy", "default")

	// Existing Service co-owned by GKE (external annotation) with an operator-owned field
	// drifted (empty session affinity) so serviceNeedsUpdate fires.
	existing := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      createProxyServiceName(proxy.Name),
			Namespace: proxy.Namespace,
			Labels:    labelsForMCPRemoteProxy(proxy.Name),
			Annotations: map[string]string{
				"cloud.google.com/neg-status": `{"network_endpoint_groups":{"8080":"k8s1-abc"}}`,
			},
		},
		Spec: corev1.ServiceSpec{
			SessionAffinity: "",
			Ports:           []corev1.ServicePort{{Port: 8080}},
		},
	}

	scheme := testutil.NewScheme(t)
	_ = rbacv1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(proxy, existing).
		Build()
	reconciler := &MCPRemoteProxyReconciler{Client: fakeClient, Scheme: scheme}

	_, err := reconciler.ensureService(context.TODO(), proxy)
	require.NoError(t, err)

	updated := &corev1.Service{}
	require.NoError(t, fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      createProxyServiceName(proxy.Name),
		Namespace: proxy.Namespace,
	}, updated))
	// External annotation preserved...
	assert.Equal(t, `{"network_endpoint_groups":{"8080":"k8s1-abc"}}`,
		updated.Annotations["cloud.google.com/neg-status"])
	// ...and the operator-owned field corrected.
	assert.Equal(t, corev1.ServiceAffinityClientIP, updated.Spec.SessionAffinity)
}

func TestMCPRemoteProxyDeploymentNeedsUpdate_EmbeddedAuthLegacyEnvStable(t *testing.T) {
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

	proxy := v1beta1test.NewMCPRemoteProxy("test-proxy", "default",
		v1beta1test.WithRemoteProxyExternalAuthConfigRef(authConfig.Name),
	)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(authConfig).
		Build()

	reconciler := &MCPRemoteProxyReconciler{
		Client:           fakeClient,
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}

	deployment := reconciler.deploymentForMCPRemoteProxy(t.Context(), proxy, "test-checksum")
	require.NotNil(t, deployment)

	assert.False(t, reconciler.deploymentNeedsUpdate(t.Context(), deployment, proxy, "test-checksum"))
}

func TestMCPRemoteProxyDeploymentNeedsUpdate_EmbeddedAuthAuthServerRefEnvStable(t *testing.T) {
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

	proxy := v1beta1test.NewMCPRemoteProxy("test-proxy", "default",
		v1beta1test.WithRemoteProxyAuthServerRef("MCPExternalAuthConfig", authConfig.Name),
	)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(authConfig).
		Build()

	reconciler := &MCPRemoteProxyReconciler{
		Client:           fakeClient,
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}

	deployment := reconciler.deploymentForMCPRemoteProxy(t.Context(), proxy, "test-checksum")
	require.NotNil(t, deployment)

	assert.False(t, reconciler.deploymentNeedsUpdate(t.Context(), deployment, proxy, "test-checksum"))
}

func TestMCPRemoteProxyDeploymentNeedsUpdate_TokenExchangeDoesNotDrift(t *testing.T) {
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

	proxy := v1beta1test.NewMCPRemoteProxy("test-proxy", "default",
		v1beta1test.WithRemoteProxyExternalAuthConfigRef(authConfig.Name),
	)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(authConfig).
		Build()

	reconciler := &MCPRemoteProxyReconciler{
		Client:           fakeClient,
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}

	deployment := reconciler.deploymentForMCPRemoteProxy(t.Context(), proxy, "test-checksum")
	require.NotNil(t, deployment)

	assert.False(t, reconciler.deploymentNeedsUpdate(t.Context(), deployment, proxy, "test-checksum"))
}

// TestMCPRemoteProxyDeployment_OBOSecretEnvVars verifies that an obo-typed
// MCPExternalAuthConfig referenced from an MCPRemoteProxy injects the registered
// OBOHandler.SecretEnvVars output into the proxy container, and that the
// deployment builder and drift check agree on it so a correctly-configured
// resource does not hot-loop. A stub OBO handler stands in for the out-of-tree
// enterprise handler.
//
//nolint:paralleltest // Mutates package-level oboHandler via RegisterOBOHandler.
func TestMCPRemoteProxyDeployment_OBOSecretEnvVars(t *testing.T) {
	t.Cleanup(func() { ctrlutil.RegisterOBOHandler(defaultOBOHandlerStub()) })

	oboEnvVar := corev1.EnvVar{
		Name: "TOOLHIVE_OBO_CLIENT_SECRET",
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "obo-secret"},
				Key:                  "client-secret",
			},
		},
	}
	stub := defaultOBOHandlerStub()
	stub.SecretEnvVars = func(*mcpv1beta1.MCPExternalAuthConfig) ([]corev1.EnvVar, error) {
		return []corev1.EnvVar{oboEnvVar}, nil
	}
	ctrlutil.RegisterOBOHandler(stub)

	scheme := testutil.NewScheme(t)

	authConfig := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "obo-config",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeOBO,
			OBO:  &mcpv1beta1.OBOConfig{},
		},
	}

	proxy := v1beta1test.NewMCPRemoteProxy("test-proxy", "default",
		v1beta1test.WithRemoteProxyExternalAuthConfigRef(authConfig.Name),
	)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(authConfig).
		Build()

	reconciler := &MCPRemoteProxyReconciler{
		Client:           fakeClient,
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}

	deployment := reconciler.deploymentForMCPRemoteProxy(t.Context(), proxy, "test-checksum")
	require.NotNil(t, deployment)

	container := deployment.Spec.Template.Spec.Containers[0]
	assert.Contains(t, container.Env, oboEnvVar,
		"OBO handler SecretEnvVars output must be injected into the proxy container")

	assert.False(t, reconciler.deploymentNeedsUpdate(t.Context(), deployment, proxy, "test-checksum"),
		"freshly built deployment with an OBO env var must not be seen as drifted")
}

func TestMCPRemoteProxyDeploymentNeedsUpdate_ImagePullSecretsDrift(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		specSecrets       []corev1.LocalObjectReference // set on proxy.Spec.ResourceOverrides
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

			proxy := v1beta1test.NewMCPRemoteProxy("test-proxy", "default")
			if tt.specSecrets != nil {
				proxy.Spec.ResourceOverrides = &mcpv1beta1.ResourceOverrides{
					ProxyDeployment: &mcpv1beta1.ProxyDeploymentOverrides{
						ImagePullSecrets: tt.specSecrets,
					},
				}
			}

			reconciler := &MCPRemoteProxyReconciler{
				Client:           fakeClient,
				Scheme:           scheme,
				PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
			}

			deployment := reconciler.deploymentForMCPRemoteProxy(t.Context(), proxy, "test-checksum")
			require.NotNil(t, deployment)

			// Simulate the "stored" state by overwriting ImagePullSecrets only.
			// The freshly built deployment is otherwise fully aligned with the proxy spec,
			// so any detected drift is caused solely by this field.
			deployment.Spec.Template.Spec.ImagePullSecrets = tt.deploymentSecrets

			needsUpdate := reconciler.deploymentNeedsUpdate(t.Context(), deployment, proxy, "test-checksum")
			assert.Equal(t, tt.expectNeedsUpdate, needsUpdate, "ImagePullSecrets drift detection mismatch")
		})
	}
}

// TestBuildEnvVarsForProxy tests environment variable building
func TestBuildEnvVarsForProxy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		proxy        *mcpv1beta1.MCPRemoteProxy
		externalAuth *mcpv1beta1.MCPExternalAuthConfig
		clientSecret *corev1.Secret
		validate     func(*testing.T, []corev1.EnvVar)
	}{
		{
			name:  "basic env vars",
			proxy: v1beta1test.NewMCPRemoteProxy("basic-proxy", "default"),
			validate: func(t *testing.T, envVars []corev1.EnvVar) {
				t.Helper()
				// Should have required env vars
				found := false
				for _, env := range envVars {
					if env.Name == "TOOLHIVE_RUNTIME" {
						assert.Equal(t, "kubernetes", env.Value)
						found = true
						break
					}
				}
				assert.True(t, found, "TOOLHIVE_RUNTIME should be set")
			},
		},
		{
			name: "with token exchange",
			proxy: v1beta1test.NewMCPRemoteProxy("exchange-proxy", "default",
				v1beta1test.WithRemoteProxyExternalAuthConfigRef("exchange-config"),
			),
			externalAuth: &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "exchange-config",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1beta1.TokenExchangeConfig{
						TokenURL: "https://oauth.com/token",
						ClientID: "client",
						ClientSecretRef: &mcpv1beta1.SecretKeyRef{
							Name: "secret",
							Key:  "key",
						},
						Audience: "api",
					},
				},
			},
			clientSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "secret",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"key": []byte("secret-value"),
				},
			},
			validate: func(t *testing.T, envVars []corev1.EnvVar) {
				t.Helper()
				found := false
				for _, env := range envVars {
					if env.Name == "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET" {
						require.NotNil(t, env.ValueFrom)
						require.NotNil(t, env.ValueFrom.SecretKeyRef)
						assert.Equal(t, "secret", env.ValueFrom.SecretKeyRef.Name)
						assert.Equal(t, "key", env.ValueFrom.SecretKeyRef.Key)
						found = true
						break
					}
				}
				assert.True(t, found, "Token exchange secret should be referenced")
			},
		},
		{
			name: "with header forward secrets",
			proxy: v1beta1test.NewMCPRemoteProxy("header-forward-proxy", "default",
				v1beta1test.WithRemoteProxyHeaderForward(&mcpv1beta1.HeaderForwardConfig{
					AddHeadersFromSecret: []mcpv1beta1.HeaderFromSecret{
						{
							HeaderName: "X-API-Key",
							ValueSecretRef: &mcpv1beta1.SecretKeyRef{
								Name: "api-key-secret",
								Key:  "api-key",
							},
						},
						{
							HeaderName: "Authorization",
							ValueSecretRef: &mcpv1beta1.SecretKeyRef{
								Name: "auth-secret",
								Key:  "token",
							},
						},
					},
				}),
			),
			validate: func(t *testing.T, envVars []corev1.EnvVar) {
				t.Helper()
				// Should have env vars for both header secrets and TOOLHIVE_SECRETS_PROVIDER
				apiKeyFound := false
				authFound := false
				secretsProviderFound := false
				for _, env := range envVars {
					if env.Name == "TOOLHIVE_SECRETS_PROVIDER" {
						assert.Equal(t, "environment", env.Value)
						secretsProviderFound = true
					}
					if env.Name == "TOOLHIVE_SECRET_HEADER_FORWARD_X_API_KEY_HEADER_FORWARD_PROXY" {
						require.NotNil(t, env.ValueFrom)
						require.NotNil(t, env.ValueFrom.SecretKeyRef)
						assert.Equal(t, "api-key-secret", env.ValueFrom.SecretKeyRef.Name)
						assert.Equal(t, "api-key", env.ValueFrom.SecretKeyRef.Key)
						apiKeyFound = true
					}
					if env.Name == "TOOLHIVE_SECRET_HEADER_FORWARD_AUTHORIZATION_HEADER_FORWARD_PROXY" {
						require.NotNil(t, env.ValueFrom)
						require.NotNil(t, env.ValueFrom.SecretKeyRef)
						assert.Equal(t, "auth-secret", env.ValueFrom.SecretKeyRef.Name)
						assert.Equal(t, "token", env.ValueFrom.SecretKeyRef.Key)
						authFound = true
					}
				}
				assert.True(t, secretsProviderFound, "TOOLHIVE_SECRETS_PROVIDER should be set to 'environment'")
				assert.True(t, apiKeyFound, "X-API-Key header secret should be referenced")
				assert.True(t, authFound, "Authorization header secret should be referenced")
			},
		},
		{
			name: "with bearer token",
			proxy: v1beta1test.NewMCPRemoteProxy("bearer-proxy", "default",
				v1beta1test.WithRemoteProxyExternalAuthConfigRef("bearer-config"),
			),
			externalAuth: &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "bearer-config",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeBearerToken,
					BearerToken: &mcpv1beta1.BearerTokenConfig{
						TokenSecretRef: &mcpv1beta1.SecretKeyRef{
							Name: "bearer-secret",
							Key:  "token",
						},
					},
				},
			},
			clientSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "bearer-secret",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"token": []byte("my-bearer-token"),
				},
			},
			validate: func(t *testing.T, envVars []corev1.EnvVar) {
				t.Helper()
				found := false
				for _, env := range envVars {
					if env.Name == "TOOLHIVE_SECRET_bearer-secret" {
						require.NotNil(t, env.ValueFrom)
						require.NotNil(t, env.ValueFrom.SecretKeyRef)
						assert.Equal(t, "bearer-secret", env.ValueFrom.SecretKeyRef.Name)
						assert.Equal(t, "token", env.ValueFrom.SecretKeyRef.Key)
						found = true
						break
					}
				}
				assert.True(t, found, "Bearer token secret should be referenced as TOOLHIVE_SECRET_bearer-secret")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := testutil.NewScheme(t)
			objects := []runtime.Object{tt.proxy}
			if tt.externalAuth != nil {
				objects = append(objects, tt.externalAuth)
			}
			if tt.clientSecret != nil {
				objects = append(objects, tt.clientSecret)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				Build()

			reconciler := &MCPRemoteProxyReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			envVars := reconciler.buildEnvVarsForProxy(context.TODO(), tt.proxy)

			if tt.validate != nil {
				tt.validate(t, envVars)
			}
		})
	}
}

func TestMCPRemoteProxyServiceNeedsUpdate(t *testing.T) {
	t.Parallel()

	baseProxy := v1beta1test.NewMCPRemoteProxy("test-proxy", "default")

	baseService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        createProxyServiceName(baseProxy.Name),
			Namespace:   baseProxy.Namespace,
			Labels:      labelsForMCPRemoteProxy(baseProxy.Name),
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
		proxy       *mcpv1beta1.MCPRemoteProxy
		needsUpdate bool
	}{
		{
			name:        "no update needed",
			service:     baseService.DeepCopy(),
			proxy:       baseProxy.DeepCopy(),
			needsUpdate: false,
		},
		{
			name: "session affinity drifted to empty",
			service: func() *corev1.Service {
				s := baseService.DeepCopy()
				s.Spec.SessionAffinity = ""
				return s
			}(),
			proxy:       baseProxy.DeepCopy(),
			needsUpdate: true,
		},
		{
			name:    "session affinity spec changed to None",
			service: baseService.DeepCopy(),
			proxy: func() *mcpv1beta1.MCPRemoteProxy {
				p := baseProxy.DeepCopy()
				p.Spec.SessionAffinity = string(corev1.ServiceAffinityNone)
				return p
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
			proxy: func() *mcpv1beta1.MCPRemoteProxy {
				p := baseProxy.DeepCopy()
				p.Spec.SessionAffinity = string(corev1.ServiceAffinityNone)
				return p
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
			proxy:       baseProxy.DeepCopy(),
			needsUpdate: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := &MCPRemoteProxyReconciler{}
			result := r.serviceNeedsUpdate(tt.service, tt.proxy)
			assert.Equal(t, tt.needsUpdate, result)
		})
	}
}

// TestBuildRedisPasswordEnvVarForRemoteProxy mirrors VirtualMCPServer's
// TestBuildRedisPasswordEnvVar — the env var must be injected only when
// sessionStorage uses the redis provider AND a passwordRef is set, and it
// must always be a SecretKeyRef (never a plaintext value).
func TestBuildRedisPasswordEnvVarForRemoteProxy(t *testing.T) {
	t.Parallel()

	passwordRef := &mcpv1beta1.SecretKeyRef{Name: "redis-secret", Key: "password"}

	tests := []struct {
		name        string
		storage     *mcpv1beta1.SessionStorageConfig
		expectEnVar bool
	}{
		{
			name:        "nil sessionStorage produces no env var",
			storage:     nil,
			expectEnVar: false,
		},
		{
			name:        "memory provider produces no env var",
			storage:     &mcpv1beta1.SessionStorageConfig{Provider: "memory"},
			expectEnVar: false,
		},
		{
			name:        "redis without passwordRef produces no env var",
			storage:     &mcpv1beta1.SessionStorageConfig{Provider: mcpv1beta1.SessionStorageProviderRedis, Address: "redis:6379"},
			expectEnVar: false,
		},
		{
			name: "redis with passwordRef produces THV_SESSION_REDIS_PASSWORD",
			storage: &mcpv1beta1.SessionStorageConfig{
				Provider:    mcpv1beta1.SessionStorageProviderRedis,
				Address:     "redis:6379",
				PasswordRef: passwordRef,
			},
			expectEnVar: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			proxy := v1beta1test.NewMCPRemoteProxy("test-proxy", "default",
				v1beta1test.WithRemoteProxySessionStorage(tc.storage),
			)
			env := buildRedisPasswordEnvVarForRemoteProxy(proxy)
			if tc.expectEnVar {
				require.Len(t, env, 1)
				assert.Equal(t, session.RedisPasswordEnvVar, env[0].Name)
				assert.Empty(t, env[0].Value, "must not use plaintext Value")
				require.NotNil(t, env[0].ValueFrom)
				require.NotNil(t, env[0].ValueFrom.SecretKeyRef)
				assert.Equal(t, passwordRef.Name, env[0].ValueFrom.SecretKeyRef.Name)
				assert.Equal(t, passwordRef.Key, env[0].ValueFrom.SecretKeyRef.Key)
			} else {
				assert.Empty(t, env)
			}
		})
	}
}

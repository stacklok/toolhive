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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
)

// TestDeploymentForMCPRemoteProxy tests deployment generation
func TestDeploymentForMCPRemoteProxy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		proxy    *mcpv1alpha1.MCPRemoteProxy
		validate func(*testing.T, *appsv1.Deployment)
	}{
		{
			name: "basic deployment",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "basic-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					Port:      8080,
				},
			},
			validate: func(t *testing.T, dep *appsv1.Deployment) {
				t.Helper()
				assert.Equal(t, "basic-proxy", dep.Name)
				assert.Equal(t, "default", dep.Namespace)
				assert.Equal(t, int32(1), *dep.Spec.Replicas)

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
				assert.NotNil(t, container.LivenessProbe)
				assert.NotNil(t, container.ReadinessProbe)
				assert.Equal(t, "/health", container.LivenessProbe.HTTPGet.Path)

				// Verify service account
				assert.Equal(t, proxyRunnerServiceAccountNameForRemoteProxy("basic-proxy"),
					dep.Spec.Template.Spec.ServiceAccountName)

				// Verify default resources are applied when none are specified
				assert.Equal(t, "50m", container.Resources.Requests.Cpu().String())
				assert.Equal(t, "64Mi", container.Resources.Requests.Memory().String())
				assert.Equal(t, "200m", container.Resources.Limits.Cpu().String())
				assert.Equal(t, "256Mi", container.Resources.Limits.Memory().String())
			},
		},
		{
			name: "with resource limits",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "resources-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					Port:      8080,
					Resources: mcpv1alpha1.ResourceRequirements{
						Limits: mcpv1alpha1.ResourceList{
							CPU:    "1",
							Memory: "512Mi",
						},
						Requests: mcpv1alpha1.ResourceList{
							CPU:    "100m",
							Memory: "128Mi",
						},
					},
				},
			},
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
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "override-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					Port:      8080,
					ResourceOverrides: &mcpv1alpha1.ResourceOverrides{
						ProxyDeployment: &mcpv1alpha1.ProxyDeploymentOverrides{
							ResourceMetadataOverrides: mcpv1alpha1.ResourceMetadataOverrides{
								Labels: map[string]string{
									"custom-label": "custom-value",
								},
								Annotations: map[string]string{
									"custom-annotation": "custom-annotation-value",
								},
							},
							Env: []mcpv1alpha1.EnvVar{
								{Name: "CUSTOM_ENV", Value: "custom-value"},
								{Name: "TOOLHIVE_DEBUG", Value: "true"},
							},
						},
					},
				},
			},
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := createRunConfigTestScheme()
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
		proxy    *mcpv1alpha1.MCPRemoteProxy
		validate func(*testing.T, *corev1.Service)
	}{
		{
			name: "basic service",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "basic-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					Port:      8080,
				},
			},
			validate: func(t *testing.T, svc *corev1.Service) {
				t.Helper()
				assert.Equal(t, createProxyServiceName("basic-proxy"), svc.Name)
				assert.Equal(t, "default", svc.Namespace)

				// Verify selector
				assert.Equal(t, labelsForMCPRemoteProxy("basic-proxy"), svc.Spec.Selector)

				// Verify port
				require.Len(t, svc.Spec.Ports, 1)
				assert.Equal(t, int32(8080), svc.Spec.Ports[0].Port)
				assert.Equal(t, "http", svc.Spec.Ports[0].Name)
			},
		},
		{
			name: "service with overrides",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "override-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					Port:      9090,
					ResourceOverrides: &mcpv1alpha1.ResourceOverrides{
						ProxyService: &mcpv1alpha1.ResourceMetadataOverrides{
							Labels: map[string]string{
								"svc-label": "svc-value",
							},
							Annotations: map[string]string{
								"svc-annotation": "svc-annotation-value",
							},
						},
					},
				},
			},
			validate: func(t *testing.T, svc *corev1.Service) {
				t.Helper()
				assert.Equal(t, "svc-value", svc.Labels["svc-label"])
				assert.Equal(t, "svc-annotation-value", svc.Annotations["svc-annotation"])
				assert.Equal(t, int32(9090), svc.Spec.Ports[0].Port)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := createRunConfigTestScheme()
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
		resourceSpec mcpv1alpha1.ResourceRequirements
		validate     func(*testing.T, corev1.ResourceRequirements)
	}{
		{
			name: "with limits and requests",
			resourceSpec: mcpv1alpha1.ResourceRequirements{
				Limits: mcpv1alpha1.ResourceList{
					CPU:    "2",
					Memory: "1Gi",
				},
				Requests: mcpv1alpha1.ResourceList{
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
			resourceSpec: mcpv1alpha1.ResourceRequirements{},
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

// TestResourceRequirementsForRemoteProxy tests resource requirements for remote proxy
func TestResourceRequirementsForRemoteProxy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		proxy    *mcpv1alpha1.MCPRemoteProxy
		validate func(*testing.T, corev1.ResourceRequirements)
	}{
		{
			name: "no user resources - uses defaults",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					Resources: mcpv1alpha1.ResourceRequirements{},
				},
			},
			validate: func(t *testing.T, res corev1.ResourceRequirements) {
				t.Helper()
				assert.Equal(t, "50m", res.Requests.Cpu().String())
				assert.Equal(t, "64Mi", res.Requests.Memory().String())
				assert.Equal(t, "200m", res.Limits.Cpu().String())
				assert.Equal(t, "256Mi", res.Limits.Memory().String())
			},
		},
		{
			name: "user resources override defaults",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					Resources: mcpv1alpha1.ResourceRequirements{
						Limits: mcpv1alpha1.ResourceList{
							CPU:    "1",
							Memory: "512Mi",
						},
						Requests: mcpv1alpha1.ResourceList{
							CPU:    "100m",
							Memory: "128Mi",
						},
					},
				},
			},
			validate: func(t *testing.T, res corev1.ResourceRequirements) {
				t.Helper()
				// User values should override defaults
				assert.Equal(t, "100m", res.Requests.Cpu().String())
				assert.Equal(t, "128Mi", res.Requests.Memory().String())
				assert.Equal(t, "1", res.Limits.Cpu().String())
				assert.Equal(t, "512Mi", res.Limits.Memory().String())
			},
		},
		{
			name: "partial user resources - only CPU specified",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					Resources: mcpv1alpha1.ResourceRequirements{
						Limits: mcpv1alpha1.ResourceList{
							CPU: "500m",
						},
						Requests: mcpv1alpha1.ResourceList{
							CPU: "100m",
						},
					},
				},
			},
			validate: func(t *testing.T, res corev1.ResourceRequirements) {
				t.Helper()
				// CPU should be user-provided
				assert.Equal(t, "100m", res.Requests.Cpu().String())
				assert.Equal(t, "500m", res.Limits.Cpu().String())
				// Memory should use defaults
				assert.Equal(t, "64Mi", res.Requests.Memory().String())
				assert.Equal(t, "256Mi", res.Limits.Memory().String())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := resourceRequirementsForRemoteProxy(tt.proxy)

			if tt.validate != nil {
				tt.validate(t, result)
			}
		})
	}
}

// TestBuildDefaultProxyRunnerResourceRequirements tests default resource requirements
func TestBuildDefaultProxyRunnerResourceRequirements(t *testing.T) {
	t.Parallel()

	res := ctrlutil.BuildDefaultProxyRunnerResourceRequirements()

	assert.Equal(t, "50m", res.Requests.Cpu().String())
	assert.Equal(t, "64Mi", res.Requests.Memory().String())
	assert.Equal(t, "200m", res.Limits.Cpu().String())
	assert.Equal(t, "256Mi", res.Limits.Memory().String())
}

// TestMergeResourceRequirements tests resource requirements merging
func TestMergeResourceRequirements(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		defaultRes    corev1.ResourceRequirements
		userRes       corev1.ResourceRequirements
		validate      func(*testing.T, corev1.ResourceRequirements)
	}{
		{
			name: "user values override defaults",
			defaultRes: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("50m"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("200m"),
					corev1.ResourceMemory: resource.MustParse("256Mi"),
				},
			},
			userRes: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("100m"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("500m"),
				},
			},
			validate: func(t *testing.T, res corev1.ResourceRequirements) {
				t.Helper()
				// CPU should be user-provided
				assert.Equal(t, "100m", res.Requests.Cpu().String())
				assert.Equal(t, "500m", res.Limits.Cpu().String())
				// Memory should be from defaults
				assert.Equal(t, "64Mi", res.Requests.Memory().String())
				assert.Equal(t, "256Mi", res.Limits.Memory().String())
			},
		},
		{
			name: "empty user resources - returns defaults",
			defaultRes: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("50m"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("200m"),
					corev1.ResourceMemory: resource.MustParse("256Mi"),
				},
			},
			userRes: corev1.ResourceRequirements{},
			validate: func(t *testing.T, res corev1.ResourceRequirements) {
				t.Helper()
				assert.Equal(t, "50m", res.Requests.Cpu().String())
				assert.Equal(t, "64Mi", res.Requests.Memory().String())
				assert.Equal(t, "200m", res.Limits.Cpu().String())
				assert.Equal(t, "256Mi", res.Limits.Memory().String())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := ctrlutil.MergeResourceRequirements(tt.defaultRes, tt.userRes)

			if tt.validate != nil {
				tt.validate(t, result)
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
		proxy              *mcpv1alpha1.MCPRemoteProxy
		existingDeployment *appsv1.Deployment
		expectRequeue      bool
	}{
		{
			name: "create new deployment",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "new-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					Port:      8080,
				},
			},
			existingDeployment: nil,
			expectRequeue:      true,
		},
		{
			name: "deployment exists - no update to allow HPA",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "replica-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					Port:      8080,
				},
			},
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

			scheme := createRunConfigTestScheme()
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
		proxy           *mcpv1alpha1.MCPRemoteProxy
		existingService *corev1.Service
		expectRequeue   bool
	}{
		{
			name: "create new service",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "new-svc-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					Port:      8080,
				},
			},
			existingService: nil,
			expectRequeue:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := createRunConfigTestScheme()
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

// TestBuildEnvVarsForProxy tests environment variable building
func TestBuildEnvVarsForProxy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		proxy        *mcpv1alpha1.MCPRemoteProxy
		externalAuth *mcpv1alpha1.MCPExternalAuthConfig
		clientSecret *corev1.Secret
		validate     func(*testing.T, []corev1.EnvVar)
	}{
		{
			name: "basic env vars",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "basic-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
				},
			},
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
			name: "with telemetry",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "telemetry-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					Telemetry: &mcpv1alpha1.TelemetryConfig{
						OpenTelemetry: &mcpv1alpha1.OpenTelemetryConfig{
							Enabled:     true,
							ServiceName: "my-proxy",
						},
					},
				},
			},
			validate: func(t *testing.T, envVars []corev1.EnvVar) {
				t.Helper()
				found := false
				for _, env := range envVars {
					if env.Name == "OTEL_RESOURCE_ATTRIBUTES" {
						assert.Contains(t, env.Value, "service.name=my-proxy")
						found = true
						break
					}
				}
				assert.True(t, found, "OTEL_RESOURCE_ATTRIBUTES should be set")
			},
		},
		{
			name: "with token exchange",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "exchange-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "exchange-config",
					},
				},
			},
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "exchange-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://oauth.com/token",
						ClientID: "client",
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := createRunConfigTestScheme()
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

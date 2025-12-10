package controllers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
)

func TestMCPServerReconciler_deploymentNeedsUpdate_Resources(t *testing.T) {
	t.Parallel()

	// Register scheme
	s := scheme.Scheme
	_ = mcpv1alpha1.AddToScheme(s)

	// Create fake client
	fakeClient := fake.NewClientBuilder().WithScheme(s).Build()

	reconciler := &MCPServerReconciler{
		Client: fakeClient,
	}

	tests := []struct {
		name               string
		existingDeployment *appsv1.Deployment
		mcpServer          *mcpv1alpha1.MCPServer
		expectNeedsUpdate  bool
		description        string
	}{
		{
			name: "no update needed - default resources match",
			existingDeployment: createDeploymentWithResources(
				"test-server",
				// Proxy runner resources (default)
				ctrlutil.BuildDefaultProxyRunnerResourceRequirements(),
				// MCP server resources in pod patch (default)
				ctrlutil.BuildDefaultResourceRequirements(),
			),
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "test:latest",
					Transport: "stdio",
					// No resources specified, so defaults will be used
				},
			},
			expectNeedsUpdate: false,
			description:       "When resources match defaults, no update is needed",
		},
		{
			name: "update needed - MCP server resources changed",
			existingDeployment: createDeploymentWithResources(
				"test-server",
				ctrlutil.BuildDefaultProxyRunnerResourceRequirements(),
				ctrlutil.BuildDefaultResourceRequirements(),
			),
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "test:latest",
					Transport: "stdio",
					Resources: mcpv1alpha1.ResourceRequirements{
						Limits: mcpv1alpha1.ResourceList{
							CPU:    "1000m",
							Memory: "1Gi",
						},
					},
				},
			},
			expectNeedsUpdate: true,
			description:       "When MCP server resources change, update is needed",
		},
		{
			name: "update needed - proxy runner resources changed",
			existingDeployment: createDeploymentWithResources(
				"test-server",
				ctrlutil.BuildDefaultProxyRunnerResourceRequirements(),
				ctrlutil.BuildDefaultResourceRequirements(),
			),
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "test:latest",
					Transport: "stdio",
					ProxyRunnerResources: mcpv1alpha1.ResourceRequirements{
						Limits: mcpv1alpha1.ResourceList{
							CPU:    "300m",
							Memory: "256Mi",
						},
					},
				},
			},
			expectNeedsUpdate: true,
			description:       "When proxy runner resources change, update is needed",
		},
		{
			name: "no update needed - custom resources match",
			existingDeployment: createDeploymentWithResources(
				"test-server",
				// Proxy runner with custom resources
				corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("300m"),
						corev1.ResourceMemory: resource.MustParse("256Mi"),
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
				},
				// MCP server with custom resources
				corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
				},
			),
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "test:latest",
					Transport: "stdio",
					Resources: mcpv1alpha1.ResourceRequirements{
						Limits: mcpv1alpha1.ResourceList{
							CPU:    "2000m",
							Memory: "2Gi",
						},
						Requests: mcpv1alpha1.ResourceList{
							CPU:    "1000m",
							Memory: "1Gi",
						},
					},
					ProxyRunnerResources: mcpv1alpha1.ResourceRequirements{
						Limits: mcpv1alpha1.ResourceList{
							CPU:    "300m",
							Memory: "256Mi",
						},
						Requests: mcpv1alpha1.ResourceList{
							CPU:    "100m",
							Memory: "128Mi",
						},
					},
				},
			},
			expectNeedsUpdate: false,
			description:       "When both proxy and MCP resources match, no update is needed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			needsUpdate := reconciler.deploymentNeedsUpdate(ctx, tt.existingDeployment, tt.mcpServer, "test-checksum")

			assert.Equal(t, tt.expectNeedsUpdate, needsUpdate, tt.description)
		})
	}
}

// createDeploymentWithResources creates a deployment with specified proxy and MCP server resources
func createDeploymentWithResources(name string, proxyResources, mcpResources corev1.ResourceRequirements) *appsv1.Deployment {
	// Create pod template spec with service account and MCP container resources
	// This matches what deploymentForMCPServer creates via PodTemplateSpecBuilder
	// Note: Image is NOT included in the patch - only resources, secrets, and service account
	defaultSA := mcpServerServiceAccountName(name)
	podTemplate := &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			ServiceAccountName: defaultSA,
			Containers: []corev1.Container{
				{
					Name:      mcpContainerName,
					Resources: mcpResources,
				},
			},
		},
	}

	// Marshal pod template to JSON for the --k8s-pod-patch argument
	podTemplateJSON, _ := json.Marshal(podTemplate)

	// Create labels for the deployment (matches labelsForMCPServer)
	labels := map[string]string{
		"app":                        "mcpserver",
		"app.kubernetes.io/name":     "mcpserver",
		"app.kubernetes.io/instance": name,
		"toolhive":                   "true",
		"toolhive-name":              name,
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
					Annotations: map[string]string{
						"toolhive.stacklok.dev/runconfig-checksum": "test-checksum",
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: ctrlutil.ProxyRunnerServiceAccountName(name),
					Containers: []corev1.Container{
						{
							Name:      "proxy-runner",
							Image:     "ghcr.io/stacklok/toolhive/proxyrunner:latest",
							Resources: proxyResources,
							Args: []string{
								"run",
								"--k8s-pod-patch=" + string(podTemplateJSON),
								"test:latest",
							},
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 8080,
									Name:          "http",
									Protocol:      corev1.ProtocolTCP,
								},
							},
							Env: []corev1.EnvVar{
								{Name: "XDG_CONFIG_HOME", Value: "/tmp"},
								{Name: "HOME", Value: "/tmp"},
								{Name: "TOOLHIVE_RUNTIME", Value: "kubernetes"},
								{Name: "UNSTRUCTURED_LOGS", Value: "false"},
							},
						},
					},
				},
			},
		},
	}
}

func TestMCPServerReconciler_deploymentNeedsUpdate_EdgeCases(t *testing.T) {
	t.Parallel()

	reconciler := &MCPServerReconciler{}
	ctx := context.Background()

	t.Run("nil deployment returns true", func(t *testing.T) {
		t.Parallel()
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
			Spec:       mcpv1alpha1.MCPServerSpec{Image: "test:latest", Transport: "stdio"},
		}
		require.True(t, reconciler.deploymentNeedsUpdate(ctx, nil, mcpServer, "checksum"))
	})

	t.Run("nil mcpServer returns true", func(t *testing.T) {
		t.Parallel()
		deployment := createDeploymentWithResources(
			"test",
			ctrlutil.BuildDefaultProxyRunnerResourceRequirements(),
			ctrlutil.BuildDefaultResourceRequirements(),
		)
		require.True(t, reconciler.deploymentNeedsUpdate(ctx, deployment, nil, "checksum"))
	})
}

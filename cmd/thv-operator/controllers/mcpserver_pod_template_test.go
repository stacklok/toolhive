package controllers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestDeploymentForMCPServerWithPodTemplateSpec(t *testing.T) {
	t.Parallel()
	// Create a test MCPServer with a PodTemplateSpec
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mcp-server",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "stdio",
			Port:      8080,
			PodTemplateSpec: &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Tolerations: []corev1.Toleration{
						{
							Key:      "dedicated",
							Operator: "Equal",
							Value:    "mcp-servers",
							Effect:   "NoSchedule",
						},
					},
					NodeSelector: map[string]string{
						"kubernetes.io/os": "linux",
						"node-type":        "mcp-server",
					},
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: boolPtr(true),
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
					Containers: []corev1.Container{
						{
							Name: "mcp",
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: boolPtr(false),
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"ALL"},
								},
								RunAsUser: int64Ptr(1000),
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("500m"),
									corev1.ResourceMemory: resource.MustParse("512Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("128Mi"),
								},
							},
						},
					},
				},
			},
		},
	}

	// Create a new scheme for this test to avoid race conditions
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	s.AddKnownTypes(mcpv1alpha1.GroupVersion, &mcpv1alpha1.MCPServer{})
	s.AddKnownTypes(mcpv1alpha1.GroupVersion, &mcpv1alpha1.MCPServerList{})

	// Create a reconciler with the scheme
	r := &MCPServerReconciler{
		Scheme: s,
	}

	// Call deploymentForMCPServer
	ctx := context.Background()
	deployment := r.deploymentForMCPServer(ctx, mcpServer)
	require.NotNil(t, deployment, "Deployment should not be nil")

	// Verify that ConfigMap-based configuration is used instead of --k8s-pod-patch
	configMapArgFound := false
	expectedConfigMapArg := "--from-configmap=default/test-mcp-server-runconfig"
	for _, arg := range deployment.Spec.Template.Spec.Containers[0].Args {
		if arg == expectedConfigMapArg {
			configMapArgFound = true
			break
		}
	}
	assert.True(t, configMapArgFound, "Should use --from-configmap argument for pod template configuration")

	// Now validate that the RunConfig created contains the correct pod template configuration
	runConfig, err := r.createRunConfigFromMCPServer(mcpServer)
	require.NoError(t, err, "Should be able to create RunConfig from MCPServer")
	require.NotNil(t, runConfig, "RunConfig should not be nil")

	// Verify that the K8sPodTemplatePatch field contains the pod template configuration
	require.NotEmpty(t, runConfig.K8sPodTemplatePatch, "K8sPodTemplatePatch should contain pod template configuration")

	// Parse the K8sPodTemplatePatch JSON to verify it contains expected configuration
	var podTemplatePatch corev1.PodTemplateSpec
	err = json.Unmarshal([]byte(runConfig.K8sPodTemplatePatch), &podTemplatePatch)
	require.NoError(t, err, "K8sPodTemplatePatch should be valid JSON")

	// Extract the pod spec for validation
	podPatch := podTemplatePatch.Spec

	// Validate tolerations
	require.Len(t, podPatch.Tolerations, 1, "Should have one toleration")
	toleration := podPatch.Tolerations[0]
	assert.Equal(t, "dedicated", toleration.Key)
	assert.Equal(t, corev1.TolerationOperator("Equal"), toleration.Operator)
	assert.Equal(t, "mcp-servers", toleration.Value)
	assert.Equal(t, corev1.TaintEffect("NoSchedule"), toleration.Effect)

	// Validate node selector
	require.NotNil(t, podPatch.NodeSelector, "NodeSelector should not be nil")
	assert.Equal(t, "linux", podPatch.NodeSelector["kubernetes.io/os"])
	assert.Equal(t, "mcp-server", podPatch.NodeSelector["node-type"])

	// Validate pod security context
	require.NotNil(t, podPatch.SecurityContext, "Pod SecurityContext should not be nil")
	assert.True(t, *podPatch.SecurityContext.RunAsNonRoot, "Pod RunAsNonRoot should be true")
	require.NotNil(t, podPatch.SecurityContext.SeccompProfile, "SeccompProfile should not be nil")
	assert.Equal(t, corev1.SeccompProfileTypeRuntimeDefault, podPatch.SecurityContext.SeccompProfile.Type)

	// Validate container configuration
	require.Len(t, podPatch.Containers, 1, "Should have one container specification")
	container := podPatch.Containers[0]
	assert.Equal(t, "mcp", container.Name)

	// Validate container security context
	require.NotNil(t, container.SecurityContext, "Container SecurityContext should not be nil")
	assert.False(t, *container.SecurityContext.AllowPrivilegeEscalation, "Container AllowPrivilegeEscalation should be false")
	require.NotNil(t, container.SecurityContext.Capabilities, "Capabilities should not be nil")
	assert.Contains(t, container.SecurityContext.Capabilities.Drop, corev1.Capability("ALL"), "Should drop ALL capabilities")
	assert.Equal(t, int64(1000), *container.SecurityContext.RunAsUser, "Container RunAsUser should be 1000")

	// Validate container resources
	assert.NotNil(t, container.Resources.Limits, "Resource limits should be set")
	assert.Equal(t, resource.MustParse("500m"), container.Resources.Limits[corev1.ResourceCPU])
	assert.Equal(t, resource.MustParse("512Mi"), container.Resources.Limits[corev1.ResourceMemory])

	assert.NotNil(t, container.Resources.Requests, "Resource requests should be set")
	assert.Equal(t, resource.MustParse("100m"), container.Resources.Requests[corev1.ResourceCPU])
	assert.Equal(t, resource.MustParse("128Mi"), container.Resources.Requests[corev1.ResourceMemory])
}

func TestProxyRunnerSecurityContext(t *testing.T) {
	t.Parallel()

	// Create a test MCPServer
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mcp-server-env",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "stdio",
			Port:      8080,
		},
	}

	// Create a new scheme for this test to avoid race conditions
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	s.AddKnownTypes(mcpv1alpha1.GroupVersion, &mcpv1alpha1.MCPServer{})
	s.AddKnownTypes(mcpv1alpha1.GroupVersion, &mcpv1alpha1.MCPServerList{})

	// Create a reconciler with the scheme
	r := &MCPServerReconciler{
		Scheme: s,
	}

	// Generate the deployment
	ctx := context.Background()
	deployment := r.deploymentForMCPServer(ctx, mcpServer)
	require.NotNil(t, deployment, "Deployment should not be nil")

	// Check that the ProxyRunner's pod and container security context are set
	proxyRunnerPodSecurityContext := deployment.Spec.Template.Spec.SecurityContext
	require.NotNil(t, proxyRunnerPodSecurityContext, "ProxyRunner pod security context should not be nil")
	assert.True(t, *proxyRunnerPodSecurityContext.RunAsNonRoot, "ProxyRunner pod RunAsNonRoot should be true")
	assert.Equal(t, int64(1000), *proxyRunnerPodSecurityContext.RunAsUser, "ProxyRunner pod RunAsUser should be 1000")
	assert.Equal(t, int64(1000), *proxyRunnerPodSecurityContext.RunAsGroup, "ProxyRunner pod RunAsGroup should be 1000")
	assert.Equal(t, int64(1000), *proxyRunnerPodSecurityContext.FSGroup, "ProxyRunner pod FSGroup should be 1000")

	proxyRunnerContainerSecurityContext := deployment.Spec.Template.Spec.Containers[0].SecurityContext
	require.NotNil(t, proxyRunnerContainerSecurityContext, "ProxyRunner container security context should not be nil")
	assert.False(t, *proxyRunnerContainerSecurityContext.Privileged, "ProxyRunner container Privileged should be false")
	assert.True(t, *proxyRunnerContainerSecurityContext.RunAsNonRoot, "ProxyRunner container RunAsNonRoot should be true")
	assert.Equal(t, int64(1000), *proxyRunnerContainerSecurityContext.RunAsUser, "ProxyRunner container RunAsUser should be 1000")
	assert.Equal(t, int64(1000), *proxyRunnerContainerSecurityContext.RunAsGroup, "ProxyRunner container RunAsGroup should be 1000")
	assert.False(t, *proxyRunnerContainerSecurityContext.AllowPrivilegeEscalation, "ProxyRunner container AllowPrivilegeEscalation should be false")
}

func TestProxyRunnerStructuredLogsEnvVar(t *testing.T) {
	t.Parallel()

	// Create a test MCPServer
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mcp-server-logs",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "stdio",
			Port:      8080,
		},
	}

	// Create a new scheme for this test to avoid race conditions
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	s.AddKnownTypes(mcpv1alpha1.GroupVersion, &mcpv1alpha1.MCPServer{})
	s.AddKnownTypes(mcpv1alpha1.GroupVersion, &mcpv1alpha1.MCPServerList{})

	// Create a reconciler with the scheme
	r := &MCPServerReconciler{
		Scheme: s,
	}

	// Create the deployment
	ctx := context.Background()
	deployment := r.deploymentForMCPServer(ctx, mcpServer)
	require.NotNil(t, deployment, "Deployment should not be nil")

	// Check that the proxy runner container has the UNSTRUCTURED_LOGS environment variable set to false
	container := deployment.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "toolhive", container.Name, "Container should be named 'toolhive'")

	// Find the UNSTRUCTURED_LOGS environment variable
	unstructuredLogsFound := false
	for _, env := range container.Env {
		if env.Name == "UNSTRUCTURED_LOGS" {
			unstructuredLogsFound = true
			assert.Equal(t, "false", env.Value, "UNSTRUCTURED_LOGS should be set to false for structured JSON logging")
			break
		}
	}
	assert.True(t, unstructuredLogsFound, "UNSTRUCTURED_LOGS environment variable should be set")
}

// Helper functions
func boolPtr(b bool) *bool {
	return &b
}

func int64Ptr(i int64) *int64 {
	return &i
}

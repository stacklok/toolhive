package controllers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

func TestDeploymentForMCPServerWithPodTemplateSpec(t *testing.T) {
	t.Parallel()
	// Create a test MCPServer with a PodTemplateSpec
	podTemplateSpec := &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "mcp",
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: boolPtr(false),
						RunAsUser:                int64Ptr(1000),
						Capabilities: &corev1.Capabilities{
							Drop: []corev1.Capability{"ALL"},
						},
					},
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("500m"),
							corev1.ResourceMemory: resource.MustParse("256Mi"),
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
					},
				},
			},
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
		},
	}

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mcp-server",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:           "test-image:latest",
			Transport:       "stdio",
			Port:            8080,
			PodTemplateSpec: podTemplateSpecToRawExtension(t, podTemplateSpec),
		},
	}

	// Create a new scheme for this test to avoid race conditions
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	s.AddKnownTypes(mcpv1alpha1.GroupVersion, &mcpv1alpha1.MCPServer{})
	s.AddKnownTypes(mcpv1alpha1.GroupVersion, &mcpv1alpha1.MCPServerList{})

	// Create a reconciler with the scheme
	r := newTestMCPServerReconciler(nil, s, kubernetes.PlatformKubernetes)

	// Call deploymentForMCPServer
	ctx := context.Background()
	deployment := r.deploymentForMCPServer(ctx, mcpServer, "test-checksum")
	require.NotNil(t, deployment, "Deployment should not be nil")

	// Check if the pod template patch is included in the args
	podTemplatePatchFound := false
	for _, arg := range deployment.Spec.Template.Spec.Containers[0].Args {
		if len(arg) > 16 && arg[:16] == "--k8s-pod-patch=" {
			podTemplatePatchFound = true

			// Verify the pod template patch contains the expected values
			patchJSON := arg[16:]
			var podTemplateSpec corev1.PodTemplateSpec
			err := json.Unmarshal([]byte(patchJSON), &podTemplateSpec)
			require.NoError(t, err, "Should be able to unmarshal pod template patch")

			// Check tolerations
			require.Len(t, podTemplateSpec.Spec.Tolerations, 1, "Should have one toleration")
			assert.Equal(t, "dedicated", podTemplateSpec.Spec.Tolerations[0].Key, "Toleration key should match")
			assert.Equal(t, "Equal", string(podTemplateSpec.Spec.Tolerations[0].Operator), "Toleration operator should match")
			assert.Equal(t, "mcp-servers", podTemplateSpec.Spec.Tolerations[0].Value, "Toleration value should match")
			assert.Equal(t, "NoSchedule", string(podTemplateSpec.Spec.Tolerations[0].Effect), "Toleration effect should match")

			// Check node selector
			require.NotNil(t, podTemplateSpec.Spec.NodeSelector, "NodeSelector should not be nil")
			assert.Equal(t, "linux", podTemplateSpec.Spec.NodeSelector["kubernetes.io/os"], "NodeSelector OS should match")
			assert.Equal(t, "mcp-server", podTemplateSpec.Spec.NodeSelector["node-type"], "NodeSelector node-type should match")

			// Check security context
			require.NotNil(t, podTemplateSpec.Spec.SecurityContext, "SecurityContext should not be nil")
			assert.True(t, *podTemplateSpec.Spec.SecurityContext.RunAsNonRoot, "RunAsNonRoot should be true")
			assert.Equal(t, corev1.SeccompProfileTypeRuntimeDefault, podTemplateSpec.Spec.SecurityContext.SeccompProfile.Type, "SeccompProfile type should match")

			// Check containers
			require.Len(t, podTemplateSpec.Spec.Containers, 1, "Should have one container")
			mcpContainer := podTemplateSpec.Spec.Containers[0]
			assert.Equal(t, "mcp", mcpContainer.Name, "Container name should be mcp")

			// Check container security context
			require.NotNil(t, mcpContainer.SecurityContext, "Container SecurityContext should not be nil")
			assert.False(t, *mcpContainer.SecurityContext.AllowPrivilegeEscalation, "AllowPrivilegeEscalation should be false")
			require.NotNil(t, mcpContainer.SecurityContext.Capabilities, "Capabilities should not be nil")
			assert.Contains(t, mcpContainer.SecurityContext.Capabilities.Drop, corev1.Capability("ALL"), "Should drop ALL capabilities")
			assert.Equal(t, int64(1000), *mcpContainer.SecurityContext.RunAsUser, "RunAsUser should be 1000")

			// Check container resources
			cpuLimit := mcpContainer.Resources.Limits[corev1.ResourceCPU]
			memoryLimit := mcpContainer.Resources.Limits[corev1.ResourceMemory]
			cpuRequest := mcpContainer.Resources.Requests[corev1.ResourceCPU]
			memoryRequest := mcpContainer.Resources.Requests[corev1.ResourceMemory]

			assert.Equal(t, "500m", cpuLimit.String(), "CPU limit should match")
			assert.Equal(t, "256Mi", memoryLimit.String(), "Memory limit should match")
			assert.Equal(t, "100m", cpuRequest.String(), "CPU request should match")
			assert.Equal(t, "128Mi", memoryRequest.String(), "Memory request should match")

			break
		}
	}
	assert.True(t, podTemplatePatchFound, "Pod template patch should be included in the args")
}

func TestDeploymentForMCPServerSecretsProviderEnv(t *testing.T) {
	t.Parallel()
	// Create a test MCPServer
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mcp-server",
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
	r := newTestMCPServerReconciler(nil, s, kubernetes.PlatformKubernetes)

	// Call deploymentForMCPServer
	ctx := context.Background()
	deployment := r.deploymentForMCPServer(ctx, mcpServer, "test-checksum")
	require.NotNil(t, deployment, "Deployment should not be nil")
}

func TestDeploymentForMCPServerWithSecrets(t *testing.T) {
	t.Parallel()
	// Create a test MCPServer with secrets and custom service account
	customSA := "custom-mcp-sa"
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mcp-server-secrets",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:          "test-image:latest",
			Transport:      "stdio",
			Port:           8080,
			ServiceAccount: &customSA,
			Secrets: []mcpv1alpha1.SecretRef{
				{
					Name:          "github-token",
					Key:           "token",
					TargetEnvName: "GITHUB_PERSONAL_ACCESS_TOKEN",
				},
				{
					Name: "api-key",
					Key:  "key",
					// No TargetEnvName, should default to Key
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
	r := newTestMCPServerReconciler(nil, s, kubernetes.PlatformKubernetes)

	// Call deploymentForMCPServer
	ctx := context.Background()
	deployment := r.deploymentForMCPServer(ctx, mcpServer, "test-checksum")
	require.NotNil(t, deployment, "Deployment should not be nil")

	// Check that secrets are injected via pod template patch
	container := deployment.Spec.Template.Spec.Containers[0]

	// Find the pod template patch in the container args
	var podTemplatePatch string
	podTemplatePatchFound := false
	for _, arg := range container.Args {
		if strings.HasPrefix(arg, "--k8s-pod-patch=") {
			podTemplatePatchFound = true
			podTemplatePatch = arg[16:] // Remove "--k8s-pod-patch=" prefix
			break
		}
	}

	assert.True(t, podTemplatePatchFound, "Pod template patch should be present in args")

	// Parse and verify the pod template patch contains secret environment variables and service account
	var podTemplateSpec corev1.PodTemplateSpec
	err := json.Unmarshal([]byte(podTemplatePatch), &podTemplateSpec)
	require.NoError(t, err, "Should be able to unmarshal pod template patch")

	// Verify the service account is set in the pod template patch
	assert.Equal(t, customSA, podTemplateSpec.Spec.ServiceAccountName,
		"ServiceAccountName should be set in pod template patch")

	// Find the mcp container in the patch
	var mcpContainer *corev1.Container
	for i, container := range podTemplateSpec.Spec.Containers {
		if container.Name == "mcp" {
			mcpContainer = &podTemplateSpec.Spec.Containers[i]
			break
		}
	}

	require.NotNil(t, mcpContainer, "mcp container should be present in pod template patch")
	require.Len(t, mcpContainer.Env, 2, "mcp container should have 2 environment variables")

	// Check for GITHUB_PERSONAL_ACCESS_TOKEN
	githubTokenEnvFound := false
	apiKeyEnvFound := false

	for _, env := range mcpContainer.Env {
		if env.Name == "GITHUB_PERSONAL_ACCESS_TOKEN" {
			githubTokenEnvFound = true
			require.NotNil(t, env.ValueFrom, "ValueFrom should not be nil for secret env var")
			require.NotNil(t, env.ValueFrom.SecretKeyRef, "SecretKeyRef should not be nil")
			assert.Equal(t, "github-token", env.ValueFrom.SecretKeyRef.Name, "Secret name should match")
			assert.Equal(t, "token", env.ValueFrom.SecretKeyRef.Key, "Secret key should match")
		}
		if env.Name == "key" {
			apiKeyEnvFound = true
			require.NotNil(t, env.ValueFrom, "ValueFrom should not be nil for secret env var")
			require.NotNil(t, env.ValueFrom.SecretKeyRef, "SecretKeyRef should not be nil")
			assert.Equal(t, "api-key", env.ValueFrom.SecretKeyRef.Name, "Secret name should match")
			assert.Equal(t, "key", env.ValueFrom.SecretKeyRef.Key, "Secret key should match")
		}
	}

	assert.True(t, githubTokenEnvFound, "GITHUB_PERSONAL_ACCESS_TOKEN environment variable should be present in pod template patch")
	assert.True(t, apiKeyEnvFound, "key environment variable should be present in pod template patch")

	// Verify that no secret CLI arguments are present in the container args
	for _, arg := range container.Args {
		assert.NotContains(t, arg, "--secret=", "No secret CLI arguments should be present")
	}
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
	r := newTestMCPServerReconciler(nil, s, kubernetes.PlatformKubernetes)

	// Generate the deployment
	ctx := context.Background()
	deployment := r.deploymentForMCPServer(ctx, mcpServer, "test-checksum")
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
	r := newTestMCPServerReconciler(nil, s, kubernetes.PlatformKubernetes)

	// Create the deployment
	ctx := context.Background()
	deployment := r.deploymentForMCPServer(ctx, mcpServer, "test-checksum")
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

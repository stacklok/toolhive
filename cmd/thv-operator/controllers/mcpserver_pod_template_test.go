package controllers

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	// Register the scheme
	s := scheme.Scheme
	s.AddKnownTypes(mcpv1alpha1.GroupVersion, &mcpv1alpha1.MCPServer{})
	s.AddKnownTypes(mcpv1alpha1.GroupVersion, &mcpv1alpha1.MCPServerList{})

	// Create a reconciler with the scheme
	r := &MCPServerReconciler{
		Scheme: s,
	}

	// Call deploymentForMCPServer
	deployment := r.deploymentForMCPServer(mcpServer)
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
			require.Len(t, podTemplateSpec.Spec.Tolerations, 1, "Should have 1 toleration")
			assert.Equal(t, "dedicated", podTemplateSpec.Spec.Tolerations[0].Key)
			assert.Equal(t, "Equal", string(podTemplateSpec.Spec.Tolerations[0].Operator))
			assert.Equal(t, "mcp-servers", podTemplateSpec.Spec.Tolerations[0].Value)
			assert.Equal(t, "NoSchedule", string(podTemplateSpec.Spec.Tolerations[0].Effect))

			// Check node selector
			assert.Equal(t, "linux", podTemplateSpec.Spec.NodeSelector["kubernetes.io/os"])
			assert.Equal(t, "mcp-server", podTemplateSpec.Spec.NodeSelector["node-type"])

			// Check security context
			require.NotNil(t, podTemplateSpec.Spec.SecurityContext, "Pod security context should not be nil")
			assert.True(t, *podTemplateSpec.Spec.SecurityContext.RunAsNonRoot)
			assert.Equal(t, corev1.SeccompProfileTypeRuntimeDefault, podTemplateSpec.Spec.SecurityContext.SeccompProfile.Type)

			// Check container security context
			require.Len(t, podTemplateSpec.Spec.Containers, 1, "Should have 1 container")
			container := podTemplateSpec.Spec.Containers[0]
			assert.Equal(t, "mcp", container.Name)
			require.NotNil(t, container.SecurityContext, "Container security context should not be nil")
			assert.False(t, *container.SecurityContext.AllowPrivilegeEscalation)
			assert.Equal(t, int64(1000), *container.SecurityContext.RunAsUser)
			require.NotNil(t, container.SecurityContext.Capabilities, "Container capabilities should not be nil")
			assert.Contains(t, container.SecurityContext.Capabilities.Drop, corev1.Capability("ALL"))

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

	// Register the scheme
	s := scheme.Scheme
	s.AddKnownTypes(mcpv1alpha1.GroupVersion, &mcpv1alpha1.MCPServer{})
	s.AddKnownTypes(mcpv1alpha1.GroupVersion, &mcpv1alpha1.MCPServerList{})

	// Create a reconciler with the scheme
	r := &MCPServerReconciler{
		Scheme: s,
	}

	// Call deploymentForMCPServer
	deployment := r.deploymentForMCPServer(mcpServer)
	require.NotNil(t, deployment, "Deployment should not be nil")

	// Check that the TOOLHIVE_SECRETS_PROVIDER environment variable is set to "none"
	container := deployment.Spec.Template.Spec.Containers[0]
	secretsProviderEnvFound := false
	for _, env := range container.Env {
		if env.Name == "TOOLHIVE_SECRETS_PROVIDER" {
			secretsProviderEnvFound = true
			assert.Equal(t, "none", env.Value, "TOOLHIVE_SECRETS_PROVIDER should be set to 'none'")
			break
		}
	}
	assert.True(t, secretsProviderEnvFound, "TOOLHIVE_SECRETS_PROVIDER environment variable should be present")
}

// Helper functions
func boolPtr(b bool) *bool {
	return &b
}

func int64Ptr(i int64) *int64 {
	return &i
}

package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestDeploymentForMCPServerWithSSETransport(t *testing.T) {
	tests := []struct {
		name             string
		transport        string
		expectTargetHost bool
	}{
		{
			name:             "SSE transport should include target-host",
			transport:        "sse",
			expectTargetHost: true,
		},
		{
			name:             "stdio transport should not include target-host",
			transport:        "stdio",
			expectTargetHost: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test MCPServer
			mcpServer := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-mcp-server",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "test-image:latest",
					Transport: tt.transport,
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

			// Check container args
			require.Len(t, deployment.Spec.Template.Spec.Containers, 1, "Should have exactly one container")
			container := deployment.Spec.Template.Spec.Containers[0]

			// Verify basic args are present
			expectedArgs := []string{
				"--port=8080",
				"--name=test-mcp-server",
				"--transport=" + tt.transport,
				"--host=0.0.0.0", // default proxy host
			}

			for _, expectedArg := range expectedArgs {
				assert.Contains(t, container.Args, expectedArg, "Should contain expected arg: %s", expectedArg)
			}

			// Check for target-host arg
			expectedTargetHost := "mcp-test-mcp-server-proxy.test-namespace.svc.cluster.local"
			targetHostArg := "--target-host=" + expectedTargetHost

			if tt.expectTargetHost {
				assert.Contains(t, container.Args, targetHostArg, "SSE transport should include target-host arg")
			} else {
				assert.NotContains(t, container.Args, targetHostArg, "stdio transport should not include target-host arg")
				// Also check that no target-host arg is present at all
				for _, arg := range container.Args {
					assert.NotContains(t, arg, "--target-host=", "stdio transport should not have any target-host arg")
				}
			}

			// Verify the image is included in args
			assert.Contains(t, container.Args, "test-image:latest", "Should contain the MCP server image")
		})
	}
}

func TestCreateServiceName(t *testing.T) {
	tests := []struct {
		name           string
		mcpServerName  string
		expectedResult string
	}{
		{
			name:           "simple name",
			mcpServerName:  "mkp",
			expectedResult: "mcp-mkp-proxy",
		},
		{
			name:           "name with hyphens",
			mcpServerName:  "my-mcp-server",
			expectedResult: "mcp-my-mcp-server-proxy",
		},
		{
			name:           "single character name",
			mcpServerName:  "a",
			expectedResult: "mcp-a-proxy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := createServiceName(tt.mcpServerName)
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestDeploymentNeedsUpdateWithSSETransport(t *testing.T) {
	// Create a test MCPServer with SSE transport
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mcp-server",
			Namespace: "test-namespace",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "sse",
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

	// Create the expected deployment
	expectedDeployment := r.deploymentForMCPServer(mcpServer)
	require.NotNil(t, expectedDeployment, "Expected deployment should not be nil")

	t.Run("deployment with correct target-host should not need update", func(t *testing.T) {
		// Create a deployment that matches the expected one
		currentDeployment := expectedDeployment.DeepCopy()

		// Should not need update
		needsUpdate := deploymentNeedsUpdate(currentDeployment, mcpServer)
		assert.False(t, needsUpdate, "Deployment with correct target-host should not need update")
	})

	t.Run("deployment with missing target-host should need update", func(t *testing.T) {
		// Create a deployment without the target-host arg
		currentDeployment := expectedDeployment.DeepCopy()

		// Remove the target-host arg
		container := &currentDeployment.Spec.Template.Spec.Containers[0]
		newArgs := []string{}
		for _, arg := range container.Args {
			if !contains(arg, "--target-host=") {
				newArgs = append(newArgs, arg)
			}
		}
		container.Args = newArgs

		// Should need update
		needsUpdate := deploymentNeedsUpdate(currentDeployment, mcpServer)
		assert.True(t, needsUpdate, "Deployment with missing target-host should need update")
	})

	t.Run("deployment with wrong target-host should need update", func(t *testing.T) {
		// Create a deployment with wrong target-host
		currentDeployment := expectedDeployment.DeepCopy()

		// Replace the target-host arg with a wrong one
		container := &currentDeployment.Spec.Template.Spec.Containers[0]
		for i, arg := range container.Args {
			if contains(arg, "--target-host=") {
				container.Args[i] = "--target-host=wrong-host.wrong-namespace.svc.cluster.local"
				break
			}
		}

		// Should need update
		needsUpdate := deploymentNeedsUpdate(currentDeployment, mcpServer)
		assert.True(t, needsUpdate, "Deployment with wrong target-host should need update")
	})

	t.Run("stdio transport deployment should not check target-host", func(t *testing.T) {
		// Change to stdio transport
		stdioMCPServer := mcpServer.DeepCopy()
		stdioMCPServer.Spec.Transport = "stdio"

		// Create deployment for stdio transport
		stdioDeployment := r.deploymentForMCPServer(stdioMCPServer)
		require.NotNil(t, stdioDeployment, "stdio deployment should not be nil")

		// Should not need update (no target-host check for stdio)
		needsUpdate := deploymentNeedsUpdate(stdioDeployment, stdioMCPServer)
		assert.False(t, needsUpdate, "stdio transport deployment should not need update")
	})
}

func TestSSETransportWithDifferentNamespaces(t *testing.T) {
	tests := []struct {
		name               string
		namespace          string
		mcpName            string
		expectedTargetHost string
	}{
		{
			name:               "default namespace",
			namespace:          "default",
			mcpName:            "test-mcp",
			expectedTargetHost: "mcp-test-mcp-proxy.default.svc.cluster.local",
		},
		{
			name:               "toolhive-system namespace",
			namespace:          "toolhive-system",
			mcpName:            "mkp",
			expectedTargetHost: "mcp-mkp-proxy.toolhive-system.svc.cluster.local",
		},
		{
			name:               "custom namespace",
			namespace:          "my-custom-namespace",
			mcpName:            "fetch-server",
			expectedTargetHost: "mcp-fetch-server-proxy.my-custom-namespace.svc.cluster.local",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test MCPServer with SSE transport
			mcpServer := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      tt.mcpName,
					Namespace: tt.namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "test-image:latest",
					Transport: "sse",
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

			// Check that the target-host is correctly set
			container := deployment.Spec.Template.Spec.Containers[0]
			expectedTargetHostArg := "--target-host=" + tt.expectedTargetHost
			assert.Contains(t, container.Args, expectedTargetHostArg, "Should contain correct target-host for namespace %s", tt.namespace)
		})
	}
}

// Helper function to check if a string contains a substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && s[:len(substr)] == substr
}

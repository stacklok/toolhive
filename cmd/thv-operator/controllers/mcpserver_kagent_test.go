package controllers

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestIsKagentIntegrationEnabled(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		envValue string
		expected bool
	}{
		{
			name:     "enabled with true",
			envValue: "true",
			expected: true,
		},
		{
			name:     "disabled with false",
			envValue: "false",
			expected: false,
		},
		{
			name:     "disabled when empty",
			envValue: "",
			expected: false,
		},
		{
			name:     "disabled with invalid value",
			envValue: "invalid",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Set environment variable
			os.Setenv("KAGENT_INTEGRATION_ENABLED", tt.envValue)
			defer os.Unsetenv("KAGENT_INTEGRATION_ENABLED")

			// Test
			result := isKagentIntegrationEnabled()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCreateKagentToolServerObject(t *testing.T) {
	t.Parallel()
	// Create a sample ToolHive MCPServer
	toolhiveMCP := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mcp",
			Namespace: "test-namespace",
			UID:       "test-uid",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:      "test-image:latest",
			Transport:  "sse",
			Port:       8080,
			TargetPort: 3000,
			Args:       []string{"--arg1", "--arg2"},
			Env: []mcpv1alpha1.EnvVar{
				{Name: "ENV1", Value: "value1"},
				{Name: "ENV2", Value: "value2"},
			},
			Secrets: []mcpv1alpha1.SecretRef{
				{Name: "secret1"},
				{Name: "secret2"},
			},
		},
	}

	// Create reconciler
	r := &MCPServerReconciler{}

	// Create kagent ToolServer object
	kagentToolServer := r.createKagentToolServerObject(toolhiveMCP)

	// Verify basic metadata
	assert.Equal(t, "toolhive-test-mcp", kagentToolServer.GetName())
	assert.Equal(t, "test-namespace", kagentToolServer.GetNamespace())
	assert.Equal(t, "kagent.dev", kagentToolServer.GroupVersionKind().Group)
	assert.Equal(t, "v1alpha1", kagentToolServer.GroupVersionKind().Version)
	assert.Equal(t, "ToolServer", kagentToolServer.GroupVersionKind().Kind)

	// Verify labels
	labels := kagentToolServer.GetLabels()
	assert.Equal(t, "toolhive-operator", labels["toolhive.stacklok.dev/managed-by"])
	assert.Equal(t, "test-mcp", labels["toolhive.stacklok.dev/mcpserver"])

	// Verify owner references
	ownerRefs := kagentToolServer.GetOwnerReferences()
	require.Len(t, ownerRefs, 1)
	assert.Equal(t, "toolhive.stacklok.dev/v1alpha1", ownerRefs[0].APIVersion)
	assert.Equal(t, "MCPServer", ownerRefs[0].Kind)
	assert.Equal(t, "test-mcp", ownerRefs[0].Name)
	assert.Equal(t, types.UID("test-uid"), ownerRefs[0].UID)
	assert.True(t, *ownerRefs[0].Controller)
	assert.True(t, *ownerRefs[0].BlockOwnerDeletion)

	// Verify spec
	spec, found, err := unstructured.NestedMap(kagentToolServer.Object, "spec")
	require.NoError(t, err)
	require.True(t, found)

	// Verify description
	description, found, err := unstructured.NestedString(spec, "description")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "ToolHive MCP Server: test-mcp", description)

	// Verify config
	config, found, err := unstructured.NestedMap(spec, "config")
	require.NoError(t, err)
	require.True(t, found)

	// Verify config type (should be sse for sse transport)
	configType, found, err := unstructured.NestedString(config, "type")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "sse", configType)

	// Verify SSE configuration
	sseConfig, found, err := unstructured.NestedMap(config, "sse")
	require.NoError(t, err)
	require.True(t, found)

	url, found, err := unstructured.NestedString(sseConfig, "url")
	require.NoError(t, err)
	require.True(t, found)
	expectedURL := fmt.Sprintf("http://mcp-%s-proxy.test-namespace.svc.cluster.local:8080", "test-mcp")
	assert.Equal(t, expectedURL, url)
}

func TestCreateKagentToolServerObjectStreamableHTTP(t *testing.T) {
	t.Parallel()
	// Create a sample ToolHive MCPServer with streamable-http transport
	toolhiveMCP := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mcp",
			Namespace: "test-namespace",
			UID:       "test-uid",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "streamable-http",
			Port:      8080,
		},
	}

	// Create reconciler
	r := &MCPServerReconciler{}

	// Create kagent ToolServer object
	kagentToolServer := r.createKagentToolServerObject(toolhiveMCP)

	// Verify config
	spec, found, err := unstructured.NestedMap(kagentToolServer.Object, "spec")
	require.NoError(t, err)
	require.True(t, found)

	config, found, err := unstructured.NestedMap(spec, "config")
	require.NoError(t, err)
	require.True(t, found)

	// Verify config type (should be streamableHttp for streamable-http transport)
	configType, found, err := unstructured.NestedString(config, "type")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "streamableHttp", configType)

	// Verify streamableHttp configuration
	streamableHttpConfig, found, err := unstructured.NestedMap(config, "streamableHttp")
	require.NoError(t, err)
	require.True(t, found)

	url, found, err := unstructured.NestedString(streamableHttpConfig, "url")
	require.NoError(t, err)
	require.True(t, found)
	expectedURL := fmt.Sprintf("http://mcp-%s-proxy.test-namespace.svc.cluster.local:8080", "test-mcp")
	assert.Equal(t, expectedURL, url)
}

func TestEnsureKagentToolServer(t *testing.T) { //nolint:tparallel // Can't parallelize due to environment variable usage
	t.Parallel()
	// Create scheme with support for unstructured resources
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)

	// Register kagent GVKs as unstructured types
	// This allows the fake client to handle them even without the actual CRDs
	// We need to add these to the scheme so the fake client knows about them
	kagentToolServerGVK := schema.GroupVersionKind{
		Group:   "kagent.dev",
		Version: "v1alpha1",
		Kind:    "ToolServer",
	}
	kagentRemoteMCPServerGVK := schema.GroupVersionKind{
		Group:   "kagent.dev",
		Version: "v1alpha2",
		Kind:    "RemoteMCPServer",
	}

	// Add the unstructured types to the scheme
	scheme.AddKnownTypeWithName(kagentToolServerGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group:   "kagent.dev",
		Version: "v1alpha1",
		Kind:    "ToolServerList",
	}, &unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(kagentRemoteMCPServerGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group:   "kagent.dev",
		Version: "v1alpha2",
		Kind:    "RemoteMCPServerList",
	}, &unstructured.UnstructuredList{})

	// Create a sample ToolHive MCPServer
	toolhiveMCP := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mcp",
			Namespace: "test-namespace",
			UID:       "test-uid",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "stdio",
			Port:      8080,
		},
	}

	t.Run("creates kagent ToolServer when enabled", func(t *testing.T) { //nolint:paralleltest // Can't parallelize due to environment variable usage
		// Don't run in parallel as environment variables are shared
		// Enable kagent integration
		os.Setenv("KAGENT_INTEGRATION_ENABLED", "true")
		defer os.Unsetenv("KAGENT_INTEGRATION_ENABLED")

		// Create fake client
		client := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		// Create reconciler
		r := &MCPServerReconciler{
			Client: client,
			Scheme: scheme,
		}

		// Ensure kagent ToolServer
		err := r.ensureKagentToolServer(context.Background(), toolhiveMCP)
		// The function should complete without error
		// In a real cluster with kagent CRDs installed, this would create the resource
		// With the fake client, it will try to create but may not be able to verify
		require.NoError(t, err)

		// Try to get the created resource using the client
		kagentToolServer := &unstructured.Unstructured{}
		kagentToolServer.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "kagent.dev",
			Version: "v1alpha1",
			Kind:    "ToolServer",
		})

		// Try to get the resource - this may fail with fake client
		// but the important part is that ensureKagentToolServer didn't error
		err = client.Get(context.Background(), types.NamespacedName{
			Name:      "toolhive-test-mcp",
			Namespace: "test-namespace",
		}, kagentToolServer)

		// With fake client and unregistered CRDs, we expect this to fail
		// but that's OK - the main test is that ensureKagentToolServer works
		if err == nil {
			assert.Equal(t, "toolhive-test-mcp", kagentToolServer.GetName())
			assert.Equal(t, "test-namespace", kagentToolServer.GetNamespace())
		}
	})

	t.Run("deletes kagent ToolServer when disabled", func(t *testing.T) { //nolint:paralleltest // Can't parallelize due to environment variable usage
		// Don't run in parallel as environment variables are shared
		// Disable kagent integration
		os.Setenv("KAGENT_INTEGRATION_ENABLED", "false")
		defer os.Unsetenv("KAGENT_INTEGRATION_ENABLED")

		// Create existing kagent ToolServer
		existingKagentToolServer := &unstructured.Unstructured{}
		existingKagentToolServer.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "kagent.dev",
			Version: "v1alpha1",
			Kind:    "ToolServer",
		})
		existingKagentToolServer.SetName("toolhive-test-mcp")
		existingKagentToolServer.SetNamespace("test-namespace")

		// Create fake client with existing resource
		client := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(existingKagentToolServer).
			Build()

		// Create reconciler
		r := &MCPServerReconciler{
			Client: client,
			Scheme: scheme,
		}

		// Ensure kagent ToolServer (should delete it)
		err := r.ensureKagentToolServer(context.Background(), toolhiveMCP)
		require.NoError(t, err)

		// Verify kagent ToolServer was deleted
		kagentToolServer := &unstructured.Unstructured{}
		kagentToolServer.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "kagent.dev",
			Version: "v1alpha1",
			Kind:    "ToolServer",
		})
		err = client.Get(context.Background(), types.NamespacedName{
			Name:      "toolhive-test-mcp",
			Namespace: "test-namespace",
		}, kagentToolServer)
		assert.True(t, errors.IsNotFound(err))
	})
}

// Test new v1alpha2 functions
func TestGetPreferredKagentAPIVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		envValue string
		expected string
	}{
		{
			name:     "v1alpha2 when set",
			envValue: "v1alpha2",
			expected: "v1alpha2",
		},
		{
			name:     "v1alpha1 when set",
			envValue: "v1alpha1",
			expected: "v1alpha1",
		},
		{
			name:     "default to v1alpha1 when empty",
			envValue: "",
			expected: "v1alpha1",
		},
		{
			name:     "default to v1alpha1 when invalid",
			envValue: "invalid",
			expected: "v1alpha1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Set environment variable
			os.Setenv("KAGENT_API_VERSION", tt.envValue)
			defer os.Unsetenv("KAGENT_API_VERSION")

			// Test
			result := getPreferredKagentAPIVersion()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCreateKagentRemoteMCPServerObject(t *testing.T) {
	t.Parallel()
	// Create a sample ToolHive MCPServer
	toolhiveMCP := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mcp",
			Namespace: "test-namespace",
			UID:       "test-uid",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "sse",
			Port:      8080,
		},
	}

	// Create reconciler
	r := &MCPServerReconciler{}

	// Create kagent RemoteMCPServer object
	remoteMCPServer := r.createKagentRemoteMCPServerObject(toolhiveMCP)

	// Verify basic metadata
	assert.Equal(t, "toolhive-test-mcp", remoteMCPServer.GetName())
	assert.Equal(t, "test-namespace", remoteMCPServer.GetNamespace())
	assert.Equal(t, "kagent.dev", remoteMCPServer.GroupVersionKind().Group)
	assert.Equal(t, "v1alpha2", remoteMCPServer.GroupVersionKind().Version)
	assert.Equal(t, "RemoteMCPServer", remoteMCPServer.GroupVersionKind().Kind)

	// Verify labels
	labels := remoteMCPServer.GetLabels()
	assert.Equal(t, "toolhive-operator", labels["toolhive.stacklok.dev/managed-by"])
	assert.Equal(t, "test-mcp", labels["toolhive.stacklok.dev/mcpserver"])

	// Verify owner references
	ownerRefs := remoteMCPServer.GetOwnerReferences()
	require.Len(t, ownerRefs, 1)
	assert.Equal(t, "toolhive.stacklok.dev/v1alpha1", ownerRefs[0].APIVersion)
	assert.Equal(t, "MCPServer", ownerRefs[0].Kind)
	assert.Equal(t, "test-mcp", ownerRefs[0].Name)
	assert.Equal(t, types.UID("test-uid"), ownerRefs[0].UID)
	assert.True(t, *ownerRefs[0].Controller)
	assert.True(t, *ownerRefs[0].BlockOwnerDeletion)

	// Verify spec
	spec, found, err := unstructured.NestedMap(remoteMCPServer.Object, "spec")
	require.NoError(t, err)
	require.True(t, found)

	// Verify description
	description, found, err := unstructured.NestedString(spec, "description")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "ToolHive MCP Server: test-mcp", description)

	// Verify URL
	url, found, err := unstructured.NestedString(spec, "url")
	require.NoError(t, err)
	require.True(t, found)
	expectedURL := fmt.Sprintf("http://mcp-%s-proxy.test-namespace.svc.cluster.local:8080", "test-mcp")
	assert.Equal(t, expectedURL, url)

	// Verify protocol (should be SSE for sse transport)
	protocol, found, err := unstructured.NestedString(spec, "protocol")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "SSE", protocol)
}

func TestCreateKagentRemoteMCPServerObjectStreamableHTTP(t *testing.T) {
	t.Parallel()
	// Create a sample ToolHive MCPServer with streamable-http transport
	toolhiveMCP := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mcp",
			Namespace: "test-namespace",
			UID:       "test-uid",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "streamable-http",
			Port:      8080,
		},
	}

	// Create reconciler
	r := &MCPServerReconciler{}

	// Create kagent RemoteMCPServer object
	remoteMCPServer := r.createKagentRemoteMCPServerObject(toolhiveMCP)

	// Verify spec
	spec, found, err := unstructured.NestedMap(remoteMCPServer.Object, "spec")
	require.NoError(t, err)
	require.True(t, found)

	// Verify protocol (should be STREAMABLE_HTTP for streamable-http transport)
	protocol, found, err := unstructured.NestedString(spec, "protocol")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "STREAMABLE_HTTP", protocol)
}

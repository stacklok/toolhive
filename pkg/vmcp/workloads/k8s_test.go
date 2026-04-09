// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package workloads

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

const testNamespace = "test-namespace"

// setupTestClient creates a fake Kubernetes client with the CRD schemes registered
func setupTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		Build()
}

func TestDiscoverAuth_TokenExchange(t *testing.T) {
	t.Parallel()

	namespace := testNamespace

	// Create test secret
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-client-secret",
			Namespace: namespace,
		},
		Data: map[string][]byte{
			"client-secret": []byte("my-secret-value"),
		},
	}

	// Create MCPExternalAuthConfig with token exchange
	authConfig := &mcpv1alpha1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-token-exchange",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
				TokenURL: "https://auth.example.com/token",
				ClientID: "test-client",
				ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
					Name: "test-client-secret",
					Key:  "client-secret",
				},
				Audience:         "https://api.example.com",
				Scopes:           []string{"read", "write"},
				SubjectTokenType: "access_token",
			},
		},
	}

	// Create MCPServer that references the auth config
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "streamable-http",
			ProxyPort: 8080,
			ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
				Name: "test-token-exchange",
			},
		},
		Status: mcpv1alpha1.MCPServerStatus{
			Phase: mcpv1alpha1.MCPServerPhaseReady,
			URL:   "http://localhost:8080",
		},
	}

	k8sClient := setupTestClient(t, secret, authConfig, mcpServer)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace)

	ctx := context.Background()
	backend, err := discoverer.GetWorkloadAsVMCPBackend(ctx, TypedWorkload{
		Name: "test-server",
		Type: WorkloadTypeMCPServer,
	})

	require.NoError(t, err)
	require.NotNil(t, backend)

	// Verify backend has auth populated
	assert.Equal(t, "token_exchange", backend.AuthConfig.Type)
	assert.NotNil(t, backend.AuthConfig)

	// Verify typed fields contain expected values
	assert.NotNil(t, backend.AuthConfig.TokenExchange)
	assert.Equal(t, "https://auth.example.com/token", backend.AuthConfig.TokenExchange.TokenURL)
	assert.Equal(t, "test-client", backend.AuthConfig.TokenExchange.ClientID)
	assert.Equal(t, "my-secret-value", backend.AuthConfig.TokenExchange.ClientSecret)
	assert.Equal(t, "https://api.example.com", backend.AuthConfig.TokenExchange.Audience)
	assert.Equal(t, []string{"read", "write"}, backend.AuthConfig.TokenExchange.Scopes)
	assert.Equal(t, "urn:ietf:params:oauth:token-type:access_token", backend.AuthConfig.TokenExchange.SubjectTokenType)
}

func TestDiscoverAuth_HeaderInjection(t *testing.T) {
	t.Parallel()

	namespace := testNamespace

	// Create test secret
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "api-key-secret",
			Namespace: namespace,
		},
		Data: map[string][]byte{
			"api-key": []byte("my-api-key-value"),
		},
	}

	// Create MCPExternalAuthConfig with header injection
	authConfig := &mcpv1alpha1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-header-injection",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeHeaderInjection,
			HeaderInjection: &mcpv1alpha1.HeaderInjectionConfig{
				HeaderName: "X-API-Key",
				ValueSecretRef: &mcpv1alpha1.SecretKeyRef{
					Name: "api-key-secret",
					Key:  "api-key",
				},
			},
		},
	}

	// Create MCPServer that references the auth config
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "streamable-http",
			ProxyPort: 8080,
			ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
				Name: "test-header-injection",
			},
		},
		Status: mcpv1alpha1.MCPServerStatus{
			Phase: mcpv1alpha1.MCPServerPhaseReady,
			URL:   "http://localhost:8080",
		},
	}

	k8sClient := setupTestClient(t, secret, authConfig, mcpServer)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace)

	ctx := context.Background()
	backend, err := discoverer.GetWorkloadAsVMCPBackend(ctx, TypedWorkload{
		Name: "test-server",
		Type: WorkloadTypeMCPServer,
	})

	require.NoError(t, err)
	require.NotNil(t, backend)

	// Verify backend has auth populated
	assert.Equal(t, "header_injection", backend.AuthConfig.Type)
	assert.NotNil(t, backend.AuthConfig)

	// Verify typed fields contain expected values
	assert.NotNil(t, backend.AuthConfig.HeaderInjection)
	assert.Equal(t, "X-API-Key", backend.AuthConfig.HeaderInjection.HeaderName)
	assert.Equal(t, "my-api-key-value", backend.AuthConfig.HeaderInjection.HeaderValue)
	// Env var reference should be removed after secret resolution
	assert.Empty(t, backend.AuthConfig.HeaderInjection.HeaderValueEnv)
}

func TestDiscoverAuth_NoAuthConfig(t *testing.T) {
	t.Parallel()

	namespace := testNamespace

	// Create MCPServer without auth config reference
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "streamable-http",
			ProxyPort: 8080,
		},
		Status: mcpv1alpha1.MCPServerStatus{
			Phase: mcpv1alpha1.MCPServerPhaseReady,
			URL:   "http://localhost:8080",
		},
	}

	k8sClient := setupTestClient(t, mcpServer)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace)

	ctx := context.Background()
	backend, err := discoverer.GetWorkloadAsVMCPBackend(ctx, TypedWorkload{
		Name: "test-server",
		Type: WorkloadTypeMCPServer,
	})

	require.NoError(t, err)
	require.NotNil(t, backend)

	// Verify backend has no auth
	assert.Nil(t, backend.AuthConfig)
}

func TestDiscoverAuth_AuthConfigNotFound(t *testing.T) {
	t.Parallel()

	namespace := testNamespace

	// Create MCPServer that references non-existent auth config
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "streamable-http",
			ProxyPort: 8080,
			ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
				Name: "non-existent-auth-config",
			},
		},
		Status: mcpv1alpha1.MCPServerStatus{
			Phase: mcpv1alpha1.MCPServerPhaseReady,
			URL:   "http://localhost:8080",
		},
	}

	k8sClient := setupTestClient(t, mcpServer)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace)

	ctx := context.Background()
	backend, err := discoverer.GetWorkloadAsVMCPBackend(ctx, TypedWorkload{
		Name: "test-server",
		Type: WorkloadTypeMCPServer,
	})

	// Should return nil backend when auth config is referenced but not found
	// This is security-critical: fail closed rather than allowing unauthorized access
	require.NoError(t, err)
	require.Nil(t, backend, "Should return nil backend when auth config is missing")
}

func TestDiscoverAuth_SecretNotFound(t *testing.T) {
	t.Parallel()

	namespace := testNamespace

	// Create MCPExternalAuthConfig with token exchange but secret doesn't exist
	authConfig := &mcpv1alpha1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-token-exchange",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
				TokenURL: "https://auth.example.com/token",
				ClientID: "test-client",
				ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
					Name: "non-existent-secret",
					Key:  "client-secret",
				},
			},
		},
	}

	// Create MCPServer that references the auth config
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "streamable-http",
			ProxyPort: 8080,
			ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
				Name: "test-token-exchange",
			},
		},
		Status: mcpv1alpha1.MCPServerStatus{
			Phase: mcpv1alpha1.MCPServerPhaseReady,
			URL:   "http://localhost:8080",
		},
	}

	k8sClient := setupTestClient(t, authConfig, mcpServer)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace)

	ctx := context.Background()
	backend, err := discoverer.GetWorkloadAsVMCPBackend(ctx, TypedWorkload{
		Name: "test-server",
		Type: WorkloadTypeMCPServer,
	})

	// Should return nil backend when secret is missing
	// This is security-critical: fail closed rather than allowing unauthorized access
	require.NoError(t, err)
	require.Nil(t, backend, "Should return nil backend when secret is missing")
}

func TestMCPServerToBackend_BasicFields(t *testing.T) {
	t.Parallel()

	namespace := testNamespace

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "streamable-http",
			ProxyPort: 8080,
		},
		Status: mcpv1alpha1.MCPServerStatus{
			Phase: mcpv1alpha1.MCPServerPhaseReady,
			URL:   "http://localhost:8080",
		},
	}

	k8sClient := setupTestClient(t, mcpServer)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace).(*k8sDiscoverer)

	ctx := context.Background()
	backend := discoverer.mcpServerToBackend(ctx, mcpServer)

	require.NotNil(t, backend)

	assert.Equal(t, "test-server", backend.ID)
	assert.Equal(t, "test-server", backend.Name)
	assert.Equal(t, "http://localhost:8080", backend.BaseURL)
	assert.Equal(t, "streamable-http", backend.TransportType)
	assert.Equal(t, vmcp.BackendHealthy, backend.HealthStatus)
	assert.Equal(t, "mcp", backend.Metadata["tool_type"])
	assert.Equal(t, "mcp_server", backend.Metadata["workload_type"])
	assert.Equal(t, string(mcpv1alpha1.MCPServerPhaseReady), backend.Metadata["workload_status"])
	assert.Equal(t, namespace, backend.Metadata["namespace"])
}

func TestMCPServerToBackend_StdioTransport(t *testing.T) {
	t.Parallel()

	namespace := testNamespace

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "stdio",
			ProxyMode: "sse", // Explicit proxy mode
			ProxyPort: 8080,
		},
		Status: mcpv1alpha1.MCPServerStatus{
			Phase: mcpv1alpha1.MCPServerPhaseReady,
			URL:   "http://localhost:8080",
		},
	}

	k8sClient := setupTestClient(t, mcpServer)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace).(*k8sDiscoverer)

	ctx := context.Background()
	backend := discoverer.mcpServerToBackend(ctx, mcpServer)

	require.NotNil(t, backend)

	// For stdio transport with explicit proxy mode, should use the proxy mode
	assert.Equal(t, "sse", backend.TransportType)
}

func TestMCPServerToBackend_WithAnnotations(t *testing.T) {
	t.Parallel()

	namespace := testNamespace

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: namespace,
			Annotations: map[string]string{
				"custom-annotation":         "custom-value",
				"kubectl.kubernetes.io/foo": "should-be-filtered",
			},
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "streamable-http",
			ProxyPort: 8080,
		},
		Status: mcpv1alpha1.MCPServerStatus{
			Phase: mcpv1alpha1.MCPServerPhaseReady,
			URL:   "http://localhost:8080",
		},
	}

	k8sClient := setupTestClient(t, mcpServer)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace).(*k8sDiscoverer)

	ctx := context.Background()
	backend := discoverer.mcpServerToBackend(ctx, mcpServer)

	require.NotNil(t, backend)

	// Custom annotation should be in metadata
	assert.Equal(t, "custom-value", backend.Metadata["custom-annotation"])
	// Standard k8s annotation should be filtered out
	assert.NotContains(t, backend.Metadata, "kubectl.kubernetes.io/foo")
}

func TestListWorkloadsInGroup(t *testing.T) {
	t.Parallel()

	namespace := testNamespace

	// Create multiple MCPServers in different groups
	server1 := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "server1",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "streamable-http",
			GroupRef:  "group-a",
		},
	}

	server2 := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "server2",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "streamable-http",
			GroupRef:  "group-a",
		},
	}

	server3 := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "server3",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "streamable-http",
			GroupRef:  "group-b",
		},
	}

	k8sClient := setupTestClient(t, server1, server2, server3)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace)

	ctx := context.Background()
	workloadList, err := discoverer.ListWorkloadsInGroup(ctx, "group-a")

	require.NoError(t, err)
	assert.Len(t, workloadList, 2)
	assert.Contains(t, workloadList, TypedWorkload{
		Name: "server1",
		Type: WorkloadTypeMCPServer,
	})
	assert.Contains(t, workloadList, TypedWorkload{
		Name: "server2",
		Type: WorkloadTypeMCPServer,
	})
	assert.NotContains(t, workloadList, TypedWorkload{
		Name: "server3",
		Type: WorkloadTypeMCPServer,
	})
}

func TestListWorkloadsInGroup_MCPRemoteProxies(t *testing.T) {
	t.Parallel()

	namespace := testNamespace

	// Create multiple MCPRemoteProxies in different groups
	proxy1 := &mcpv1alpha1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "proxy1",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPRemoteProxySpec{
			GroupRef: "group-a",
		},
	}

	proxy2 := &mcpv1alpha1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "proxy2",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPRemoteProxySpec{
			GroupRef: "group-a",
		},
	}

	proxy3 := &mcpv1alpha1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "proxy3",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPRemoteProxySpec{
			GroupRef: "group-b",
		},
	}

	k8sClient := setupTestClient(t, proxy1, proxy2, proxy3)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace)

	ctx := context.Background()
	workloadList, err := discoverer.ListWorkloadsInGroup(ctx, "group-a")

	require.NoError(t, err)
	assert.Len(t, workloadList, 2)
	assert.Contains(t, workloadList, TypedWorkload{
		Name: "proxy1",
		Type: WorkloadTypeMCPRemoteProxy,
	})
	assert.Contains(t, workloadList, TypedWorkload{
		Name: "proxy2",
		Type: WorkloadTypeMCPRemoteProxy,
	})
	assert.NotContains(t, workloadList, TypedWorkload{
		Name: "proxy3",
		Type: WorkloadTypeMCPRemoteProxy,
	})
}

func TestListWorkloadsInGroup_MixedWorkloads(t *testing.T) {
	t.Parallel()

	namespace := testNamespace

	// Create MCPServers
	server1 := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "server1",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "streamable-http",
			GroupRef:  "group-a",
		},
	}

	server2 := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "server2",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "streamable-http",
			GroupRef:  "group-b", // Different group
		},
	}

	// Create MCPRemoteProxies
	proxy1 := &mcpv1alpha1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "proxy1",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPRemoteProxySpec{
			GroupRef: "group-a",
		},
	}

	proxy2 := &mcpv1alpha1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "proxy2",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPRemoteProxySpec{
			GroupRef: "group-a",
		},
	}

	k8sClient := setupTestClient(t, server1, server2, proxy1, proxy2)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace)

	ctx := context.Background()
	workloadList, err := discoverer.ListWorkloadsInGroup(ctx, "group-a")

	require.NoError(t, err)
	assert.Len(t, workloadList, 3) // 1 server + 2 proxies

	// Verify MCPServer is included with correct type
	assert.Contains(t, workloadList, TypedWorkload{
		Name: "server1",
		Type: WorkloadTypeMCPServer,
	})

	// Verify MCPRemoteProxies are included with correct type
	assert.Contains(t, workloadList, TypedWorkload{
		Name: "proxy1",
		Type: WorkloadTypeMCPRemoteProxy,
	})
	assert.Contains(t, workloadList, TypedWorkload{
		Name: "proxy2",
		Type: WorkloadTypeMCPRemoteProxy,
	})

	// Verify server from different group is not included
	assert.NotContains(t, workloadList, TypedWorkload{
		Name: "server2",
		Type: WorkloadTypeMCPServer,
	})
}

func TestMCPServerToBackend_EmptyStatusURL(t *testing.T) {
	t.Parallel()

	namespace := testNamespace

	// MCPServer is Running with transport and port, but Status.URL is empty
	// (the controller hasn't reconciled the Service yet).
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pending-server",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "streamable-http",
			ProxyPort: 8080,
		},
		Status: mcpv1alpha1.MCPServerStatus{
			Phase: mcpv1alpha1.MCPServerPhaseReady,
			// URL intentionally empty — not yet assigned by the operator
		},
	}

	k8sClient := setupTestClient(t, mcpServer)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace)

	ctx := context.Background()
	backend, err := discoverer.GetWorkloadAsVMCPBackend(ctx, TypedWorkload{
		Name: "pending-server",
		Type: WorkloadTypeMCPServer,
	})

	// Backend should be skipped (nil) because Status.URL is empty.
	// Previously the code fell back to a localhost URL which pointed to the
	// wrong target inside K8s pods.
	require.NoError(t, err)
	require.Nil(t, backend, "should return nil backend when Status.URL is empty")
}

func TestMCPRemoteProxyToBackend_EmptyStatusURL(t *testing.T) {
	t.Parallel()

	namespace := testNamespace

	// MCPRemoteProxy is Ready with transport, but Status.URL is empty.
	proxy := &mcpv1alpha1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pending-proxy",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPRemoteProxySpec{
			RemoteURL: "https://remote-mcp.example.com",
			Transport: "streamable-http",
		},
		Status: mcpv1alpha1.MCPRemoteProxyStatus{
			Phase: mcpv1alpha1.MCPRemoteProxyPhaseReady,
			// URL intentionally empty — not yet assigned by the operator
		},
	}

	k8sClient := setupTestClient(t, proxy)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace)

	ctx := context.Background()
	backend, err := discoverer.GetWorkloadAsVMCPBackend(ctx, TypedWorkload{
		Name: "pending-proxy",
		Type: WorkloadTypeMCPRemoteProxy,
	})

	// Backend should be skipped (nil) because Status.URL is empty.
	require.NoError(t, err)
	require.Nil(t, backend, "should return nil backend when Status.URL is empty")
}

func TestMCPRemoteProxyToBackend_BasicFields(t *testing.T) {
	t.Parallel()

	namespace := testNamespace

	proxy := &mcpv1alpha1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-proxy",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPRemoteProxySpec{
			RemoteURL: "https://remote-mcp.example.com",
			Transport: "streamable-http",
		},
		Status: mcpv1alpha1.MCPRemoteProxyStatus{
			Phase: mcpv1alpha1.MCPRemoteProxyPhaseReady,
			URL:   "http://proxy-service:8080",
		},
	}

	k8sClient := setupTestClient(t, proxy)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace).(*k8sDiscoverer)

	ctx := context.Background()
	backend := discoverer.mcpRemoteProxyToBackend(ctx, proxy)

	require.NotNil(t, backend)

	assert.Equal(t, "test-proxy", backend.ID)
	assert.Equal(t, "test-proxy", backend.Name)
	assert.Equal(t, "http://proxy-service:8080", backend.BaseURL)
	assert.Equal(t, "streamable-http", backend.TransportType)
	assert.Equal(t, vmcp.BackendHealthy, backend.HealthStatus)
	assert.Equal(t, "mcp", backend.Metadata["tool_type"])
	assert.Equal(t, "remote_proxy", backend.Metadata["workload_type"])
	assert.Equal(t, string(mcpv1alpha1.MCPRemoteProxyPhaseReady), backend.Metadata["workload_status"])
	assert.Equal(t, namespace, backend.Metadata["namespace"])
}

func TestMCPRemoteProxyToBackend_WithAnnotations(t *testing.T) {
	t.Parallel()

	namespace := testNamespace

	proxy := &mcpv1alpha1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-proxy",
			Namespace: namespace,
			Annotations: map[string]string{
				"custom-annotation":         "custom-value",
				"kubectl.kubernetes.io/foo": "should-be-filtered",
			},
		},
		Spec: mcpv1alpha1.MCPRemoteProxySpec{
			RemoteURL: "https://remote-mcp.example.com",
			Transport: "streamable-http",
		},
		Status: mcpv1alpha1.MCPRemoteProxyStatus{
			Phase: mcpv1alpha1.MCPRemoteProxyPhaseReady,
			URL:   "http://proxy-service:8080",
		},
	}

	k8sClient := setupTestClient(t, proxy)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace).(*k8sDiscoverer)

	ctx := context.Background()
	backend := discoverer.mcpRemoteProxyToBackend(ctx, proxy)

	require.NotNil(t, backend)

	// Custom annotation should be in metadata
	assert.Equal(t, "custom-value", backend.Metadata["custom-annotation"])
	// Standard k8s annotation should be filtered out
	assert.NotContains(t, backend.Metadata, "kubectl.kubernetes.io/foo")
}

func TestMCPRemoteProxyToBackend_HealthStatusMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		phase          mcpv1alpha1.MCPRemoteProxyPhase
		expectedHealth vmcp.BackendHealthStatus
	}{
		{
			name:           "Ready phase maps to Healthy",
			phase:          mcpv1alpha1.MCPRemoteProxyPhaseReady,
			expectedHealth: vmcp.BackendHealthy,
		},
		{
			name:           "Failed phase maps to Unhealthy",
			phase:          mcpv1alpha1.MCPRemoteProxyPhaseFailed,
			expectedHealth: vmcp.BackendUnhealthy,
		},
		{
			name:           "Pending phase maps to Unknown",
			phase:          mcpv1alpha1.MCPRemoteProxyPhasePending,
			expectedHealth: vmcp.BackendUnknown,
		},
		{
			name:           "Terminating phase maps to Unhealthy",
			phase:          mcpv1alpha1.MCPRemoteProxyPhaseTerminating,
			expectedHealth: vmcp.BackendUnhealthy,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			namespace := testNamespace

			proxy := &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-proxy",
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://remote-mcp.example.com",
					Transport: "streamable-http",
				},
				Status: mcpv1alpha1.MCPRemoteProxyStatus{
					Phase: tt.phase,
					URL:   "http://proxy-service:8080",
				},
			}

			k8sClient := setupTestClient(t, proxy)
			discoverer := NewK8SDiscovererWithClient(k8sClient, namespace).(*k8sDiscoverer)

			ctx := context.Background()
			backend := discoverer.mcpRemoteProxyToBackend(ctx, proxy)

			require.NotNil(t, backend)
			assert.Equal(t, tt.expectedHealth, backend.HealthStatus)
		})
	}
}

func TestGetWorkloadAsVMCPBackend_MCPRemoteProxy(t *testing.T) {
	t.Parallel()

	namespace := testNamespace

	proxy := &mcpv1alpha1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-proxy",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPRemoteProxySpec{
			RemoteURL: "https://remote-mcp.example.com",
			Transport: "streamable-http",
		},
		Status: mcpv1alpha1.MCPRemoteProxyStatus{
			Phase: mcpv1alpha1.MCPRemoteProxyPhaseReady,
			URL:   "http://proxy-service:8080",
		},
	}

	k8sClient := setupTestClient(t, proxy)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace)

	ctx := context.Background()
	backend, err := discoverer.GetWorkloadAsVMCPBackend(ctx, TypedWorkload{
		Name: "test-proxy",
		Type: WorkloadTypeMCPRemoteProxy,
	})

	require.NoError(t, err)
	require.NotNil(t, backend)

	assert.Equal(t, "test-proxy", backend.ID)
	assert.Equal(t, "http://proxy-service:8080", backend.BaseURL)
}

func TestDiscoverAuth_MCPRemoteProxy_TokenExchange(t *testing.T) {
	t.Parallel()

	namespace := testNamespace

	// Create test secret
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-client-secret",
			Namespace: namespace,
		},
		Data: map[string][]byte{
			"client-secret": []byte("my-secret-value"),
		},
	}

	// Create MCPExternalAuthConfig with token exchange
	authConfig := &mcpv1alpha1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-token-exchange",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
				TokenURL: "https://auth.example.com/token",
				ClientID: "test-client",
				ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
					Name: "test-client-secret",
					Key:  "client-secret",
				},
				Audience:         "https://api.example.com",
				Scopes:           []string{"read", "write"},
				SubjectTokenType: "access_token",
			},
		},
	}

	// Create MCPRemoteProxy that references the auth config
	proxy := &mcpv1alpha1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-proxy",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPRemoteProxySpec{
			RemoteURL: "https://remote-mcp.example.com",
			Transport: "streamable-http",
			ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
				Name: "test-token-exchange",
			},
		},
		Status: mcpv1alpha1.MCPRemoteProxyStatus{
			Phase: mcpv1alpha1.MCPRemoteProxyPhaseReady,
			URL:   "http://proxy-service:8080",
		},
	}

	k8sClient := setupTestClient(t, secret, authConfig, proxy)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace)

	ctx := context.Background()
	backend, err := discoverer.GetWorkloadAsVMCPBackend(ctx, TypedWorkload{
		Name: "test-proxy",
		Type: WorkloadTypeMCPRemoteProxy,
	})

	require.NoError(t, err)
	require.NotNil(t, backend)

	// Verify backend has auth populated
	assert.Equal(t, "token_exchange", backend.AuthConfig.Type)
	assert.NotNil(t, backend.AuthConfig)

	// Verify typed fields contain expected values
	assert.NotNil(t, backend.AuthConfig.TokenExchange)
	assert.Equal(t, "https://auth.example.com/token", backend.AuthConfig.TokenExchange.TokenURL)
	assert.Equal(t, "test-client", backend.AuthConfig.TokenExchange.ClientID)
	assert.Equal(t, "my-secret-value", backend.AuthConfig.TokenExchange.ClientSecret)
}

func TestListWorkloadsInGroup_MCPServerEntries(t *testing.T) {
	t.Parallel()

	namespace := testNamespace

	entry1 := &mcpv1alpha1.MCPServerEntry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "entry1",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerEntrySpec{
			RemoteURL: "https://mcp1.example.com",
			Transport: "streamable-http",
			GroupRef:  "group-a",
		},
	}

	entry2 := &mcpv1alpha1.MCPServerEntry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "entry2",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerEntrySpec{
			RemoteURL: "https://mcp2.example.com",
			Transport: "sse",
			GroupRef:  "group-a",
		},
	}

	entry3 := &mcpv1alpha1.MCPServerEntry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "entry3",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerEntrySpec{
			RemoteURL: "https://mcp3.example.com",
			Transport: "streamable-http",
			GroupRef:  "group-b",
		},
	}

	k8sClient := setupTestClient(t, entry1, entry2, entry3)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace)

	workloadList, err := discoverer.ListWorkloadsInGroup(t.Context(), "group-a")

	require.NoError(t, err)
	assert.Len(t, workloadList, 2)
	assert.Contains(t, workloadList, TypedWorkload{
		Name: "entry1",
		Type: WorkloadTypeMCPServerEntry,
	})
	assert.Contains(t, workloadList, TypedWorkload{
		Name: "entry2",
		Type: WorkloadTypeMCPServerEntry,
	})
	assert.NotContains(t, workloadList, TypedWorkload{
		Name: "entry3",
		Type: WorkloadTypeMCPServerEntry,
	})
}

func TestListWorkloadsInGroup_AllWorkloadTypes(t *testing.T) {
	t.Parallel()

	namespace := testNamespace

	server := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "server1",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "streamable-http",
			GroupRef:  "group-a",
		},
	}

	proxy := &mcpv1alpha1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "proxy1",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPRemoteProxySpec{
			GroupRef: "group-a",
		},
	}

	entry := &mcpv1alpha1.MCPServerEntry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "entry1",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerEntrySpec{
			RemoteURL: "https://mcp.example.com",
			Transport: "streamable-http",
			GroupRef:  "group-a",
		},
	}

	k8sClient := setupTestClient(t, server, proxy, entry)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace)

	workloadList, err := discoverer.ListWorkloadsInGroup(t.Context(), "group-a")

	require.NoError(t, err)
	assert.Len(t, workloadList, 3)
	assert.Contains(t, workloadList, TypedWorkload{Name: "server1", Type: WorkloadTypeMCPServer})
	assert.Contains(t, workloadList, TypedWorkload{Name: "proxy1", Type: WorkloadTypeMCPRemoteProxy})
	assert.Contains(t, workloadList, TypedWorkload{Name: "entry1", Type: WorkloadTypeMCPServerEntry})
}

func TestGetWorkloadAsVMCPBackend_MCPServerEntry(t *testing.T) {
	t.Parallel()

	namespace := testNamespace

	entry := &mcpv1alpha1.MCPServerEntry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-entry",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerEntrySpec{
			RemoteURL: "https://mcp.example.com/v1",
			Transport: "streamable-http",
			GroupRef:  "group-a",
		},
		Status: mcpv1alpha1.MCPServerEntryStatus{
			Phase: mcpv1alpha1.MCPServerEntryPhaseValid,
		},
	}

	k8sClient := setupTestClient(t, entry)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace)

	backend, err := discoverer.GetWorkloadAsVMCPBackend(t.Context(), TypedWorkload{
		Name: "test-entry",
		Type: WorkloadTypeMCPServerEntry,
	})

	require.NoError(t, err)
	require.NotNil(t, backend)

	assert.Equal(t, "test-entry", backend.ID)
	assert.Equal(t, "https://mcp.example.com/v1", backend.BaseURL)
}

func TestGetWorkloadAsVMCPBackend_MCPServerEntry_NotFound(t *testing.T) {
	t.Parallel()

	namespace := testNamespace

	k8sClient := setupTestClient(t)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace)

	_, err := discoverer.GetWorkloadAsVMCPBackend(t.Context(), TypedWorkload{
		Name: "non-existent-entry",
		Type: WorkloadTypeMCPServerEntry,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestMCPServerEntryToBackend_BasicFields(t *testing.T) {
	t.Parallel()

	namespace := testNamespace

	entry := &mcpv1alpha1.MCPServerEntry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-entry",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerEntrySpec{
			RemoteURL: "https://mcp.example.com/v1",
			Transport: "streamable-http",
			GroupRef:  "group-a",
		},
		Status: mcpv1alpha1.MCPServerEntryStatus{
			Phase: mcpv1alpha1.MCPServerEntryPhaseValid,
		},
	}

	k8sClient := setupTestClient(t, entry)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace).(*k8sDiscoverer)

	backend := discoverer.mcpServerEntryToBackend(t.Context(), entry)

	require.NotNil(t, backend)

	// Key difference from MCPServer/MCPRemoteProxy: BaseURL comes from Spec.RemoteURL, not Status.URL
	assert.Equal(t, "test-entry", backend.ID)
	assert.Equal(t, "test-entry", backend.Name)
	assert.Equal(t, "https://mcp.example.com/v1", backend.BaseURL)
	assert.Equal(t, "streamable-http", backend.TransportType)
	assert.Equal(t, vmcp.BackendHealthy, backend.HealthStatus)
	assert.Equal(t, "mcp", backend.Metadata["tool_type"])
	assert.Equal(t, "server_entry", backend.Metadata["workload_type"])
	assert.Equal(t, string(mcpv1alpha1.MCPServerEntryPhaseValid), backend.Metadata["workload_status"])
	assert.Equal(t, "https://mcp.example.com/v1", backend.Metadata["remote_url"])
	assert.Equal(t, namespace, backend.Metadata["namespace"])
}

func TestMCPServerEntryToBackend_SSETransport(t *testing.T) {
	t.Parallel()

	namespace := testNamespace

	entry := &mcpv1alpha1.MCPServerEntry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sse-entry",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerEntrySpec{
			RemoteURL: "https://mcp.example.com/sse",
			Transport: "sse",
			GroupRef:  "group-a",
		},
		Status: mcpv1alpha1.MCPServerEntryStatus{
			Phase: mcpv1alpha1.MCPServerEntryPhaseValid,
		},
	}

	k8sClient := setupTestClient(t, entry)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace).(*k8sDiscoverer)

	backend := discoverer.mcpServerEntryToBackend(t.Context(), entry)

	require.NotNil(t, backend)
	assert.Equal(t, "sse", backend.TransportType)
}

func TestMCPServerEntryToBackend_WithAnnotations(t *testing.T) {
	t.Parallel()

	namespace := testNamespace

	entry := &mcpv1alpha1.MCPServerEntry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "annotated-entry",
			Namespace: namespace,
			Annotations: map[string]string{
				"custom-annotation":         "custom-value",
				"kubectl.kubernetes.io/foo": "should-be-filtered",
			},
		},
		Spec: mcpv1alpha1.MCPServerEntrySpec{
			RemoteURL: "https://mcp.example.com/v1",
			Transport: "streamable-http",
			GroupRef:  "group-a",
		},
		Status: mcpv1alpha1.MCPServerEntryStatus{
			Phase: mcpv1alpha1.MCPServerEntryPhaseValid,
		},
	}

	k8sClient := setupTestClient(t, entry)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace).(*k8sDiscoverer)

	backend := discoverer.mcpServerEntryToBackend(t.Context(), entry)

	require.NotNil(t, backend)
	assert.Equal(t, "custom-value", backend.Metadata["custom-annotation"])
	assert.NotContains(t, backend.Metadata, "kubectl.kubernetes.io/foo")
}

func TestMCPServerEntryToBackend_EmptyRemoteURL(t *testing.T) {
	t.Parallel()

	namespace := testNamespace

	entry := &mcpv1alpha1.MCPServerEntry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "empty-url-entry",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerEntrySpec{
			RemoteURL: "",
			Transport: "streamable-http",
			GroupRef:  "group-a",
		},
		Status: mcpv1alpha1.MCPServerEntryStatus{
			Phase: mcpv1alpha1.MCPServerEntryPhaseValid,
		},
	}

	k8sClient := setupTestClient(t, entry)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace)

	backend, err := discoverer.GetWorkloadAsVMCPBackend(t.Context(), TypedWorkload{
		Name: "empty-url-entry",
		Type: WorkloadTypeMCPServerEntry,
	})

	// Backend should be skipped (nil) because RemoteURL is empty
	require.NoError(t, err)
	require.Nil(t, backend, "should return nil backend when RemoteURL is empty")
}

func TestMCPServerEntryPhaseToHealth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		phase          mcpv1alpha1.MCPServerEntryPhase
		expectedHealth vmcp.BackendHealthStatus
	}{
		{
			name:           "Valid phase maps to Healthy",
			phase:          mcpv1alpha1.MCPServerEntryPhaseValid,
			expectedHealth: vmcp.BackendHealthy,
		},
		{
			name:           "Failed phase maps to Unhealthy",
			phase:          mcpv1alpha1.MCPServerEntryPhaseFailed,
			expectedHealth: vmcp.BackendUnhealthy,
		},
		{
			name:           "Pending phase maps to Unknown",
			phase:          mcpv1alpha1.MCPServerEntryPhasePending,
			expectedHealth: vmcp.BackendUnknown,
		},
		{
			name:           "Unknown phase maps to Unknown",
			phase:          mcpv1alpha1.MCPServerEntryPhase("SomeUnknownPhase"),
			expectedHealth: vmcp.BackendUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expectedHealth, mapMCPServerEntryPhaseToHealth(tt.phase))
		})
	}
}

func TestMCPServerEntryToBackend_HealthStatusMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		phase          mcpv1alpha1.MCPServerEntryPhase
		expectedHealth vmcp.BackendHealthStatus
	}{
		{
			name:           "Valid phase maps to Healthy",
			phase:          mcpv1alpha1.MCPServerEntryPhaseValid,
			expectedHealth: vmcp.BackendHealthy,
		},
		{
			name:           "Failed phase maps to Unhealthy",
			phase:          mcpv1alpha1.MCPServerEntryPhaseFailed,
			expectedHealth: vmcp.BackendUnhealthy,
		},
		{
			name:           "Pending phase maps to Unknown",
			phase:          mcpv1alpha1.MCPServerEntryPhasePending,
			expectedHealth: vmcp.BackendUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			namespace := testNamespace

			entry := &mcpv1alpha1.MCPServerEntry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-entry",
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerEntrySpec{
					RemoteURL: "https://mcp.example.com",
					Transport: "streamable-http",
					GroupRef:  "group-a",
				},
				Status: mcpv1alpha1.MCPServerEntryStatus{
					Phase: tt.phase,
				},
			}

			k8sClient := setupTestClient(t, entry)
			discoverer := NewK8SDiscovererWithClient(k8sClient, namespace).(*k8sDiscoverer)

			backend := discoverer.mcpServerEntryToBackend(t.Context(), entry)

			require.NotNil(t, backend)
			assert.Equal(t, tt.expectedHealth, backend.HealthStatus)
		})
	}
}

func TestDiscoverAuth_MCPServerEntry_NoAuthConfig(t *testing.T) {
	t.Parallel()

	namespace := testNamespace

	entry := &mcpv1alpha1.MCPServerEntry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "no-auth-entry",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerEntrySpec{
			RemoteURL: "https://mcp.example.com",
			Transport: "streamable-http",
			GroupRef:  "group-a",
		},
		Status: mcpv1alpha1.MCPServerEntryStatus{
			Phase: mcpv1alpha1.MCPServerEntryPhaseValid,
		},
	}

	k8sClient := setupTestClient(t, entry)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace)

	backend, err := discoverer.GetWorkloadAsVMCPBackend(t.Context(), TypedWorkload{
		Name: "no-auth-entry",
		Type: WorkloadTypeMCPServerEntry,
	})

	require.NoError(t, err)
	require.NotNil(t, backend)
	assert.Nil(t, backend.AuthConfig)
}

func TestDiscoverAuth_MCPServerEntry_AuthConfigNotFound(t *testing.T) {
	t.Parallel()

	namespace := testNamespace

	entry := &mcpv1alpha1.MCPServerEntry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "auth-missing-entry",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerEntrySpec{
			RemoteURL: "https://mcp.example.com",
			Transport: "streamable-http",
			GroupRef:  "group-a",
			ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
				Name: "non-existent-auth-config",
			},
		},
		Status: mcpv1alpha1.MCPServerEntryStatus{
			Phase: mcpv1alpha1.MCPServerEntryPhaseValid,
		},
	}

	k8sClient := setupTestClient(t, entry)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace)

	backend, err := discoverer.GetWorkloadAsVMCPBackend(t.Context(), TypedWorkload{
		Name: "auth-missing-entry",
		Type: WorkloadTypeMCPServerEntry,
	})

	// Should return nil backend when auth config is referenced but not found
	// Security-critical: fail closed rather than allowing unauthorized access
	require.NoError(t, err)
	require.Nil(t, backend, "Should return nil backend when auth config is missing")
}

func TestDiscoverAuth_MCPServerEntry_TokenExchange(t *testing.T) {
	t.Parallel()

	namespace := testNamespace

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "entry-client-secret",
			Namespace: namespace,
		},
		Data: map[string][]byte{
			"client-secret": []byte("entry-secret-value"),
		},
	}

	authConfig := &mcpv1alpha1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "entry-token-exchange",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
				TokenURL: "https://auth.example.com/token",
				ClientID: "entry-client",
				ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
					Name: "entry-client-secret",
					Key:  "client-secret",
				},
				Audience:         "https://api.example.com",
				Scopes:           []string{"read"},
				SubjectTokenType: "access_token",
			},
		},
	}

	entry := &mcpv1alpha1.MCPServerEntry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "auth-entry",
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerEntrySpec{
			RemoteURL: "https://mcp.example.com",
			Transport: "streamable-http",
			GroupRef:  "group-a",
			ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
				Name: "entry-token-exchange",
			},
		},
		Status: mcpv1alpha1.MCPServerEntryStatus{
			Phase: mcpv1alpha1.MCPServerEntryPhaseValid,
		},
	}

	k8sClient := setupTestClient(t, secret, authConfig, entry)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace)

	backend, err := discoverer.GetWorkloadAsVMCPBackend(t.Context(), TypedWorkload{
		Name: "auth-entry",
		Type: WorkloadTypeMCPServerEntry,
	})

	require.NoError(t, err)
	require.NotNil(t, backend)

	assert.Equal(t, "token_exchange", backend.AuthConfig.Type)
	assert.NotNil(t, backend.AuthConfig.TokenExchange)
	assert.Equal(t, "https://auth.example.com/token", backend.AuthConfig.TokenExchange.TokenURL)
	assert.Equal(t, "entry-client", backend.AuthConfig.TokenExchange.ClientID)
	assert.Equal(t, "entry-secret-value", backend.AuthConfig.TokenExchange.ClientSecret)
}

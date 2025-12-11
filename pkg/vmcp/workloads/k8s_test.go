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
			Phase: mcpv1alpha1.MCPServerPhaseRunning,
			URL:   "http://localhost:8080",
		},
	}

	k8sClient := setupTestClient(t, secret, authConfig, mcpServer)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace)

	ctx := context.Background()
	backend, err := discoverer.GetWorkloadAsVMCPBackend(ctx, "test-server")

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
			Phase: mcpv1alpha1.MCPServerPhaseRunning,
			URL:   "http://localhost:8080",
		},
	}

	k8sClient := setupTestClient(t, secret, authConfig, mcpServer)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace)

	ctx := context.Background()
	backend, err := discoverer.GetWorkloadAsVMCPBackend(ctx, "test-server")

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
			Phase: mcpv1alpha1.MCPServerPhaseRunning,
			URL:   "http://localhost:8080",
		},
	}

	k8sClient := setupTestClient(t, mcpServer)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace)

	ctx := context.Background()
	backend, err := discoverer.GetWorkloadAsVMCPBackend(ctx, "test-server")

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
			Phase: mcpv1alpha1.MCPServerPhaseRunning,
			URL:   "http://localhost:8080",
		},
	}

	k8sClient := setupTestClient(t, mcpServer)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace)

	ctx := context.Background()
	backend, err := discoverer.GetWorkloadAsVMCPBackend(ctx, "test-server")

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
			Phase: mcpv1alpha1.MCPServerPhaseRunning,
			URL:   "http://localhost:8080",
		},
	}

	k8sClient := setupTestClient(t, authConfig, mcpServer)
	discoverer := NewK8SDiscovererWithClient(k8sClient, namespace)

	ctx := context.Background()
	backend, err := discoverer.GetWorkloadAsVMCPBackend(ctx, "test-server")

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
			Phase: mcpv1alpha1.MCPServerPhaseRunning,
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
	assert.Equal(t, string(mcpv1alpha1.MCPServerPhaseRunning), backend.Metadata["workload_status"])
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
			Phase: mcpv1alpha1.MCPServerPhaseRunning,
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
			Phase: mcpv1alpha1.MCPServerPhaseRunning,
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
	workloads, err := discoverer.ListWorkloadsInGroup(ctx, "group-a")

	require.NoError(t, err)
	assert.Len(t, workloads, 2)
	assert.Contains(t, workloads, "server1")
	assert.Contains(t, workloads, "server2")
	assert.NotContains(t, workloads, "server3")
}

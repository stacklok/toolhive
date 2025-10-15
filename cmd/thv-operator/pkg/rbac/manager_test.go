package rbac

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// testContext provides a test environment for RBAC manager tests
type testContext struct {
	mcpServer              *mcpv1alpha1.MCPServer
	client                 client.Client
	manager                Manager
	proxyRunnerNameForRBAC string
	mcpServerSAName        string
}

// setupTest creates a new test context with all necessary components
func setupTest(name, namespace string) *testContext {
	mcpServer := createTestMCPServer(name, namespace)
	testScheme := createTestScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(mcpServer).Build()

	manager := NewManager(Config{
		Client:           fakeClient,
		Scheme:           testScheme,
		DefaultRBACRules: nil, // Use default rules
	})

	return &testContext{
		mcpServer:              mcpServer,
		client:                 fakeClient,
		manager:                manager,
		proxyRunnerNameForRBAC: fmt.Sprintf("%s-proxy-runner", name),
		mcpServerSAName:        fmt.Sprintf("%s-sa", name),
	}
}

// Helper methods for assertions
func (tc *testContext) assertServiceAccountExists(t *testing.T, name string) {
	t.Helper()
	sa := &corev1.ServiceAccount{}
	err := tc.client.Get(context.TODO(), types.NamespacedName{
		Name:      name,
		Namespace: tc.mcpServer.Namespace,
	}, sa)
	require.NoError(t, err)
	assert.Equal(t, name, sa.Name)
	assert.Equal(t, tc.mcpServer.Namespace, sa.Namespace)

	// Check owner reference
	assert.Len(t, sa.OwnerReferences, 1)
	assert.Equal(t, "MCPServer", sa.OwnerReferences[0].Kind)
	assert.Equal(t, tc.mcpServer.Name, sa.OwnerReferences[0].Name)
}

func (tc *testContext) assertRoleExists(t *testing.T) {
	t.Helper()
	role := &rbacv1.Role{}
	err := tc.client.Get(context.TODO(), types.NamespacedName{
		Name:      tc.proxyRunnerNameForRBAC,
		Namespace: tc.mcpServer.Namespace,
	}, role)
	require.NoError(t, err)
	assert.Equal(t, tc.proxyRunnerNameForRBAC, role.Name)
	assert.Equal(t, tc.mcpServer.Namespace, role.Namespace)
	assert.Equal(t, GetDefaultRBACRules(), role.Rules)

	// Check owner reference
	assert.Len(t, role.OwnerReferences, 1)
	assert.Equal(t, "MCPServer", role.OwnerReferences[0].Kind)
	assert.Equal(t, tc.mcpServer.Name, role.OwnerReferences[0].Name)
}

func (tc *testContext) assertRoleBindingExists(t *testing.T) {
	t.Helper()
	rb := &rbacv1.RoleBinding{}
	err := tc.client.Get(context.TODO(), types.NamespacedName{
		Name:      tc.proxyRunnerNameForRBAC,
		Namespace: tc.mcpServer.Namespace,
	}, rb)
	require.NoError(t, err)
	assert.Equal(t, tc.proxyRunnerNameForRBAC, rb.Name)
	assert.Equal(t, tc.mcpServer.Namespace, rb.Namespace)

	expectedRoleRef := rbacv1.RoleRef{
		APIGroup: "rbac.authorization.k8s.io",
		Kind:     "Role",
		Name:     tc.proxyRunnerNameForRBAC,
	}
	assert.Equal(t, expectedRoleRef, rb.RoleRef)

	expectedSubjects := []rbacv1.Subject{
		{
			Kind:      "ServiceAccount",
			Name:      tc.proxyRunnerNameForRBAC,
			Namespace: tc.mcpServer.Namespace,
		},
	}
	assert.Equal(t, expectedSubjects, rb.Subjects)

	// Check owner reference
	assert.Len(t, rb.OwnerReferences, 1)
	assert.Equal(t, "MCPServer", rb.OwnerReferences[0].Kind)
	assert.Equal(t, tc.mcpServer.Name, rb.OwnerReferences[0].Name)
}

func (tc *testContext) assertAllRBACResourcesExist(t *testing.T) {
	t.Helper()
	tc.assertServiceAccountExists(t, tc.proxyRunnerNameForRBAC)
	tc.assertRoleExists(t)
	tc.assertRoleBindingExists(t)
	// Also check MCP server SA if not using custom SA
	if tc.mcpServer.Spec.ServiceAccount == nil {
		tc.assertServiceAccountExists(t, tc.mcpServerSAName)
	}
}

func (tc *testContext) assertServiceAccountNotExists(t *testing.T, name string) {
	t.Helper()
	sa := &corev1.ServiceAccount{}
	err := tc.client.Get(context.TODO(), types.NamespacedName{
		Name:      name,
		Namespace: tc.mcpServer.Namespace,
	}, sa)
	assert.True(t, client.IgnoreNotFound(err) == nil)
}

// Test functions
func TestManager_EnsureRBACResources_Creation(t *testing.T) {
	t.Parallel()
	tc := setupTest("test-server", "default")

	err := tc.manager.EnsureRBACResources(context.TODO(), tc.mcpServer)
	require.NoError(t, err)

	tc.assertAllRBACResourcesExist(t)
}

func TestManager_EnsureRBACResources_WithCustomServiceAccount(t *testing.T) {
	t.Parallel()
	tc := setupTest("test-server-custom-sa", "default")
	customSA := "custom-service-account"
	tc.mcpServer.Spec.ServiceAccount = &customSA

	err := tc.manager.EnsureRBACResources(context.TODO(), tc.mcpServer)
	require.NoError(t, err)

	// Should create proxy runner resources but not MCP server SA
	tc.assertServiceAccountExists(t, tc.proxyRunnerNameForRBAC)
	tc.assertRoleExists(t)
	tc.assertRoleBindingExists(t)
	tc.assertServiceAccountNotExists(t, tc.mcpServerSAName)
}

func TestManager_EnsureRBACResources_UpdateRole(t *testing.T) {
	t.Parallel()
	tc := setupTest("test-server-update", "default")

	// Create existing role with different rules
	existingRole := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tc.proxyRunnerNameForRBAC,
			Namespace: tc.mcpServer.Namespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"get"},
			},
		},
	}
	err := tc.client.Create(context.TODO(), existingRole)
	require.NoError(t, err)

	err = tc.manager.EnsureRBACResources(context.TODO(), tc.mcpServer)
	require.NoError(t, err)

	// Role should be updated with default rules
	tc.assertRoleExists(t)
}

func TestManager_EnsureRBACResources_UpdateRoleBinding(t *testing.T) {
	t.Parallel()
	tc := setupTest("test-server-rb-update", "default")

	// Create existing role binding with different subjects
	existingRB := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tc.proxyRunnerNameForRBAC,
			Namespace: tc.mcpServer.Namespace,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     "different-role",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "different-sa",
				Namespace: tc.mcpServer.Namespace,
			},
		},
	}
	err := tc.client.Create(context.TODO(), existingRB)
	require.NoError(t, err)

	err = tc.manager.EnsureRBACResources(context.TODO(), tc.mcpServer)
	require.NoError(t, err)

	// RoleBinding should be updated
	tc.assertRoleBindingExists(t)
}

func TestManager_EnsureRBACResources_Idempotency(t *testing.T) {
	t.Parallel()
	tc := setupTest("test-server-idempotent", "default")

	// Run multiple times
	for i := 0; i < 3; i++ {
		err := tc.manager.EnsureRBACResources(context.TODO(), tc.mcpServer)
		require.NoError(t, err, "Iteration %d failed", i)
	}

	tc.assertAllRBACResourcesExist(t)
}

func TestManager_GetProxyRunnerServiceAccountName(t *testing.T) {
	t.Parallel()
	manager := NewManager(Config{})

	testCases := []struct {
		mcpServerName string
		expected      string
	}{
		{"test-server", "test-server-proxy-runner"},
		{"mcp-server-123", "mcp-server-123-proxy-runner"},
		{"my-server", "my-server-proxy-runner"},
	}

	for _, tc := range testCases {
		t.Run(tc.mcpServerName, func(t *testing.T) {
			actual := manager.GetProxyRunnerServiceAccountName(tc.mcpServerName)
			assert.Equal(t, tc.expected, actual)
		})
	}
}

func TestManager_GetMCPServerServiceAccountName(t *testing.T) {
	t.Parallel()
	manager := NewManager(Config{})

	testCases := []struct {
		mcpServerName string
		expected      string
	}{
		{"test-server", "test-server-sa"},
		{"mcp-server-123", "mcp-server-123-sa"},
		{"my-server", "my-server-sa"},
	}

	for _, tc := range testCases {
		t.Run(tc.mcpServerName, func(t *testing.T) {
			actual := manager.GetMCPServerServiceAccountName(tc.mcpServerName)
			assert.Equal(t, tc.expected, actual)
		})
	}
}

func TestManager_CustomRBACRules(t *testing.T) {
	t.Parallel()
	customRules := []rbacv1.PolicyRule{
		{
			APIGroups: []string{"custom.io"},
			Resources: []string{"customresources"},
			Verbs:     []string{"get", "list"},
		},
	}

	mcpServer := createTestMCPServer("test-server", "default")
	testScheme := createTestScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(mcpServer).Build()

	manager := NewManager(Config{
		Client:           fakeClient,
		Scheme:           testScheme,
		DefaultRBACRules: customRules,
	})

	err := manager.EnsureRBACResources(context.TODO(), mcpServer)
	require.NoError(t, err)

	// Check that custom rules were applied
	role := &rbacv1.Role{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "test-server-proxy-runner",
		Namespace: "default",
	}, role)
	require.NoError(t, err)
	assert.Equal(t, customRules, role.Rules)
}

func TestManager_MultipleNamespaces(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		namespace string
	}{
		{"server1", "namespace1"},
		{"server2", "namespace2"},
		{"server3", "default"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name+"-"+testCase.namespace, func(t *testing.T) {
			t.Parallel()
			tc := setupTest(testCase.name, testCase.namespace)

			err := tc.manager.EnsureRBACResources(context.TODO(), tc.mcpServer)
			require.NoError(t, err)

			tc.assertAllRBACResourcesExist(t)
		})
	}
}

// Helper functions
func createTestMCPServer(name, namespace string) *mcpv1alpha1.MCPServer {
	return &mcpv1alpha1.MCPServer{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "toolhive.stacklok.dev/v1alpha1",
			Kind:       "MCPServer",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID("test-uid-" + name),
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "stdio",
			Port:      8080,
		},
	}
}

func createTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = mcpv1alpha1.AddToScheme(s)
	return s
}

package controllers

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

type testContext struct {
	mcpServer              *mcpv1alpha1.MCPServer
	client                 client.Client
	reconciler             *MCPServerReconciler
	proxyRunnerNameForRBAC string
}

func setupTest(name, namespace string) *testContext {
	mcpServer := createTestMCPServer(name, namespace)
	testScheme := createTestScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(testScheme).Build()
	proxyRunnerNameForRBAC := fmt.Sprintf("%s-proxy-runner", name)
	return &testContext{
		mcpServer: mcpServer,
		client:    fakeClient,
		reconciler: &MCPServerReconciler{
			Client: fakeClient,
			Scheme: testScheme,
		},
		proxyRunnerNameForRBAC: proxyRunnerNameForRBAC,
	}
}

func (tc *testContext) ensureRBACResources() error {
	return tc.reconciler.ensureRBACResources(context.TODO(), tc.mcpServer)
}

func (tc *testContext) assertServiceAccountExists(t *testing.T) {
	t.Helper()
	sa := &corev1.ServiceAccount{}
	err := tc.client.Get(context.TODO(), types.NamespacedName{
		Name:      tc.proxyRunnerNameForRBAC,
		Namespace: tc.mcpServer.Namespace,
	}, sa)
	require.NoError(t, err)
	assert.Equal(t, tc.proxyRunnerNameForRBAC, sa.Name)
	assert.Equal(t, tc.mcpServer.Namespace, sa.Namespace)
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
	assert.Equal(t, defaultRBACRules, role.Rules)
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
}

func (tc *testContext) assertAllRBACResourcesExist(t *testing.T) {
	t.Helper()
	tc.assertServiceAccountExists(t)
	tc.assertRoleExists(t)
	tc.assertRoleBindingExists(t)
}

func TestEnsureRBACResources_ServiceAccount_Creation(t *testing.T) {
	tc := setupTest("test-server", "default")

	err := tc.ensureRBACResources()
	require.NoError(t, err)

	tc.assertServiceAccountExists(t)
}

func TestEnsureRBACResources_ServiceAccount_Update(t *testing.T) {
	tc := setupTest("test-server-sa-update", "default")

	existingSA := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tc.proxyRunnerNameForRBAC,
			Namespace: tc.mcpServer.Namespace,
			Labels:    map[string]string{"old": "label"},
		},
	}
	err := tc.client.Create(context.TODO(), existingSA)
	require.NoError(t, err)

	err = tc.ensureRBACResources()
	require.NoError(t, err)

	tc.assertServiceAccountExists(t)
}

func TestEnsureRBACResources_Role_Creation(t *testing.T) {
	tc := setupTest("test-server", "default")

	err := tc.ensureRBACResources()
	require.NoError(t, err)

	tc.assertRoleExists(t)
}

func TestEnsureRBACResources_Role_Update(t *testing.T) {
	tc := setupTest("test-server-role-update", "default")

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

	err = tc.ensureRBACResources()
	require.NoError(t, err)

	tc.assertRoleExists(t)
}

func TestEnsureRBACResources_RoleBinding_Creation(t *testing.T) {
	tc := setupTest("test-server", "default")

	err := tc.ensureRBACResources()
	require.NoError(t, err)

	tc.assertRoleBindingExists(t)
}

func TestEnsureRBACResources_RoleBinding_Update(t *testing.T) {
	tc := setupTest("test-server-rb-update", "default")

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

	err = tc.ensureRBACResources()
	require.NoError(t, err)

	tc.assertRoleBindingExists(t)
}

func TestEnsureRBACResources_MultipleNamespaces(t *testing.T) {
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
			tc := setupTest(testCase.name, testCase.namespace)

			err := tc.ensureRBACResources()
			require.NoError(t, err)

			tc.assertAllRBACResourcesExist(t)
		})
	}
}

func TestEnsureRBACResources_ResourceNames(t *testing.T) {
	testCases := []string{
		"simple-server",
		"mcp-server-test",
		"server123",
	}

	for _, serverName := range testCases {
		t.Run(serverName, func(t *testing.T) {
			tc := setupTest(serverName, "default")

			err := tc.ensureRBACResources()
			require.NoError(t, err)

			tc.assertAllRBACResourcesExist(t)
		})
	}
}

func TestEnsureRBACResources_NoChangesNeeded(t *testing.T) {
	tc := setupTest("test-server-no-changes", "default")

	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tc.proxyRunnerNameForRBAC,
			Namespace: tc.mcpServer.Namespace,
		},
	}
	err := tc.client.Create(context.TODO(), sa)
	require.NoError(t, err)

	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tc.proxyRunnerNameForRBAC,
			Namespace: tc.mcpServer.Namespace,
		},
		Rules: defaultRBACRules,
	}
	err = tc.client.Create(context.TODO(), role)
	require.NoError(t, err)

	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tc.proxyRunnerNameForRBAC,
			Namespace: tc.mcpServer.Namespace,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     tc.proxyRunnerNameForRBAC,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      tc.proxyRunnerNameForRBAC,
				Namespace: tc.mcpServer.Namespace,
			},
		},
	}
	err = tc.client.Create(context.TODO(), rb)
	require.NoError(t, err)

	err = tc.ensureRBACResources()
	require.NoError(t, err)

	tc.assertAllRBACResourcesExist(t)
}

func TestEnsureRBACResources_Idempotency(t *testing.T) {
	tc := setupTest("test-server-idempotency", "default")

	for i := 0; i < 3; i++ {
		err := tc.ensureRBACResources()
		require.NoError(t, err, "Iteration %d failed", i)
	}

	tc.assertAllRBACResourcesExist(t)
}

func createTestMCPServer(name, namespace string) *mcpv1alpha1.MCPServer {
	return &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
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

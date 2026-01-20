package controllers

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func createTestVirtualMCPServer() *mcpv1alpha1.VirtualMCPServer {
	return &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vmcp",
			Namespace: "test-namespace",
			UID:       types.UID("test-uid"),
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
				Type: "anonymous",
			},
		},
	}
}

func createTestSchemeForVMCP() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)
	return scheme
}

func TestEnsureRBACResourcesForVirtualMCPServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		vmcp          *mcpv1alpha1.VirtualMCPServer
		setupClient   func(*testing.T) client.Client
		expectedError string
		validate      func(*testing.T, client.Client, *mcpv1alpha1.VirtualMCPServer)
	}{
		{
			name: "creates all RBAC resources when none exist",
			vmcp: createTestVirtualMCPServer(),
			setupClient: func(t *testing.T) client.Client {
				t.Helper()
				return fake.NewClientBuilder().WithScheme(createTestSchemeForVMCP()).Build()
			},
			validate: func(t *testing.T, c client.Client, vmcp *mcpv1alpha1.VirtualMCPServer) {
				t.Helper()
				ctx := context.Background()
				resourceName := vmcpServiceAccountName(vmcp.Name)

				// Verify ServiceAccount
				sa := &corev1.ServiceAccount{}
				err := c.Get(ctx, types.NamespacedName{Name: resourceName, Namespace: vmcp.Namespace}, sa)
				require.NoError(t, err)
				require.Len(t, sa.OwnerReferences, 1)
				assert.Equal(t, vmcp.Name, sa.OwnerReferences[0].Name)
				assert.Equal(t, labelsForVirtualMCPServerRBAC(vmcp, resourceName), sa.Labels)

				// Verify Role
				role := &rbacv1.Role{}
				err = c.Get(ctx, types.NamespacedName{Name: resourceName, Namespace: vmcp.Namespace}, role)
				require.NoError(t, err)
				assert.Equal(t, vmcpRBACRules, role.Rules)
				require.Len(t, role.OwnerReferences, 1)
				assert.Equal(t, vmcp.Name, role.OwnerReferences[0].Name)
				assert.Equal(t, labelsForVirtualMCPServerRBAC(vmcp, resourceName), role.Labels)

				// Verify RoleBinding
				rb := &rbacv1.RoleBinding{}
				err = c.Get(ctx, types.NamespacedName{Name: resourceName, Namespace: vmcp.Namespace}, rb)
				require.NoError(t, err)
				assert.Equal(t, resourceName, rb.RoleRef.Name)
				assert.Equal(t, "Role", rb.RoleRef.Kind)
				require.Len(t, rb.Subjects, 1)
				assert.Equal(t, resourceName, rb.Subjects[0].Name)
				require.Len(t, rb.OwnerReferences, 1)
				assert.Equal(t, vmcp.Name, rb.OwnerReferences[0].Name)
				assert.Equal(t, labelsForVirtualMCPServerRBAC(vmcp, resourceName), rb.Labels)
			},
		},
		{
			name: "is idempotent with existing resources",
			vmcp: createTestVirtualMCPServer(),
			setupClient: func(t *testing.T) client.Client {
				t.Helper()
				vmcp := createTestVirtualMCPServer()
				resourceName := vmcpServiceAccountName(vmcp.Name)
				return fake.NewClientBuilder().
					WithScheme(createTestSchemeForVMCP()).
					WithObjects(
						&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: vmcp.Namespace}},
						&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: vmcp.Namespace}, Rules: vmcpRBACRules},
						&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: vmcp.Namespace}, RoleRef: rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: resourceName}},
					).Build()
			},
			validate: func(t *testing.T, c client.Client, vmcp *mcpv1alpha1.VirtualMCPServer) {
				t.Helper()
				ctx := context.Background()
				resourceName := vmcpServiceAccountName(vmcp.Name)
				role := &rbacv1.Role{}
				require.NoError(t, c.Get(ctx, types.NamespacedName{Name: resourceName, Namespace: vmcp.Namespace}, role))
			},
		},
		{
			name: "updates RBAC rules when they change",
			vmcp: createTestVirtualMCPServer(),
			setupClient: func(t *testing.T) client.Client {
				t.Helper()
				vmcp := createTestVirtualMCPServer()
				resourceName := vmcpServiceAccountName(vmcp.Name)
				oldRules := []rbacv1.PolicyRule{
					{
						APIGroups: []string{""},
						Resources: []string{"configmaps"},
						Verbs:     []string{"get"},
					},
				}
				return fake.NewClientBuilder().
					WithScheme(createTestSchemeForVMCP()).
					WithObjects(
						&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: vmcp.Namespace}},
						&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: vmcp.Namespace}, Rules: oldRules},
						&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: vmcp.Namespace}, RoleRef: rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: resourceName}},
					).Build()
			},
			validate: func(t *testing.T, c client.Client, vmcp *mcpv1alpha1.VirtualMCPServer) {
				t.Helper()
				ctx := context.Background()
				resourceName := vmcpServiceAccountName(vmcp.Name)
				role := &rbacv1.Role{}
				require.NoError(t, c.Get(ctx, types.NamespacedName{Name: resourceName, Namespace: vmcp.Namespace}, role))
				// Verify that the rules have been updated to match vmcpRBACRules
				assert.Equal(t, vmcpRBACRules, role.Rules)
			},
		},
		{
			name: "returns error when ServiceAccount creation fails",
			vmcp: createTestVirtualMCPServer(),
			setupClient: func(t *testing.T) client.Client {
				t.Helper()
				return fake.NewClientBuilder().
					WithScheme(createTestSchemeForVMCP()).
					WithInterceptorFuncs(interceptor.Funcs{
						Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
							if _, ok := obj.(*corev1.ServiceAccount); ok {
								return errors.New("simulated failure")
							}
							return c.Create(ctx, obj, opts...)
						},
					}).Build()
			},
			expectedError: "failed to ensure service account",
		},
		{
			name: "returns error when Role creation fails",
			vmcp: createTestVirtualMCPServer(),
			setupClient: func(t *testing.T) client.Client {
				t.Helper()
				return fake.NewClientBuilder().
					WithScheme(createTestSchemeForVMCP()).
					WithInterceptorFuncs(interceptor.Funcs{
						Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
							if _, ok := obj.(*rbacv1.Role); ok {
								return errors.New("simulated failure")
							}
							return c.Create(ctx, obj, opts...)
						},
					}).Build()
			},
			expectedError: "failed to ensure role",
		},
		{
			name: "returns error when RoleBinding creation fails",
			vmcp: createTestVirtualMCPServer(),
			setupClient: func(t *testing.T) client.Client {
				t.Helper()
				return fake.NewClientBuilder().
					WithScheme(createTestSchemeForVMCP()).
					WithInterceptorFuncs(interceptor.Funcs{
						Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
							if _, ok := obj.(*rbacv1.RoleBinding); ok {
								return errors.New("simulated failure")
							}
							return c.Create(ctx, obj, opts...)
						},
					}).Build()
			},
			expectedError: "failed to ensure role binding",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := tt.setupClient(t)
			r := &VirtualMCPServerReconciler{
				Client: c,
				Scheme: createTestSchemeForVMCP(),
			}

			err := r.ensureRBACResourcesForVirtualMCPServer(context.Background(), tt.vmcp)

			if tt.expectedError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
			} else {
				require.NoError(t, err)
				if tt.validate != nil {
					tt.validate(t, c, tt.vmcp)
				}
			}
		})
	}
}

func TestVmcpRBACRules(t *testing.T) {
	t.Parallel()

	require.Len(t, vmcpRBACRules, 3)

	// Core resources (ConfigMaps and Secrets)
	assert.ElementsMatch(t, []string{""}, vmcpRBACRules[0].APIGroups)
	assert.ElementsMatch(t, []string{"configmaps", "secrets"}, vmcpRBACRules[0].Resources)
	assert.ElementsMatch(t, []string{"get", "list", "watch"}, vmcpRBACRules[0].Verbs)

	// ToolHive resources for backend discovery
	assert.ElementsMatch(t, []string{"toolhive.stacklok.dev"}, vmcpRBACRules[1].APIGroups)
	assert.ElementsMatch(t, []string{"mcpgroups", "mcpservers", "mcpremoteproxies", "mcpexternalauthconfigs", "mcptoolconfigs"}, vmcpRBACRules[1].Resources)
	assert.ElementsMatch(t, []string{"get", "list", "watch"}, vmcpRBACRules[1].Verbs)

	// Status update permissions
	assert.ElementsMatch(t, []string{"toolhive.stacklok.dev"}, vmcpRBACRules[2].APIGroups)
	assert.ElementsMatch(t, []string{"virtualmcpservers/status"}, vmcpRBACRules[2].Resources)
	assert.ElementsMatch(t, []string{"update", "patch"}, vmcpRBACRules[2].Verbs)
}

func TestLabelsForVirtualMCPServerRBAC(t *testing.T) {
	t.Parallel()

	vmcp := createTestVirtualMCPServer()
	resourceName := vmcpServiceAccountName(vmcp.Name)
	labels := labelsForVirtualMCPServerRBAC(vmcp, resourceName)

	expectedLabels := map[string]string{
		"app.kubernetes.io/name":       "virtualmcpserver-rbac",
		"app.kubernetes.io/instance":   resourceName,
		"app.kubernetes.io/component":  "rbac",
		"app.kubernetes.io/part-of":    "toolhive",
		"app.kubernetes.io/managed-by": "toolhive-operator",
		"toolhive.stacklok.dev/vmcp":   vmcp.Name,
	}

	assert.Equal(t, expectedLabels, labels)
}

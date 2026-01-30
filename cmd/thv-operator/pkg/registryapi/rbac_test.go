// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registryapi

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

func createTestMCPRegistry() *mcpv1alpha1.MCPRegistry {
	return &mcpv1alpha1.MCPRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-registry",
			Namespace: "test-namespace",
			UID:       types.UID("test-uid"),
		},
		Spec: mcpv1alpha1.MCPRegistrySpec{
			Registries: []mcpv1alpha1.MCPRegistryConfig{
				{
					Name:   "default",
					Format: mcpv1alpha1.RegistryFormatToolHive,
					ConfigMapRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "test-configmap"},
						Key:                  "registry.json",
					},
				},
			},
		},
	}
}

func createTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)
	return scheme
}

func TestEnsureRBACResources(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		mcpRegistry   *mcpv1alpha1.MCPRegistry
		setupClient   func(*testing.T) client.Client
		expectedError string
		validate      func(*testing.T, client.Client, *mcpv1alpha1.MCPRegistry)
	}{
		{
			name:        "creates all RBAC resources when none exist",
			mcpRegistry: createTestMCPRegistry(),
			setupClient: func(t *testing.T) client.Client {
				t.Helper()
				return fake.NewClientBuilder().WithScheme(createTestScheme()).Build()
			},
			validate: func(t *testing.T, c client.Client, mcpRegistry *mcpv1alpha1.MCPRegistry) {
				t.Helper()
				ctx := context.Background()
				resourceName := mcpRegistry.Name + "-registry-api"

				// Verify ServiceAccount
				sa := &corev1.ServiceAccount{}
				err := c.Get(ctx, types.NamespacedName{Name: resourceName, Namespace: mcpRegistry.Namespace}, sa)
				require.NoError(t, err)
				require.Len(t, sa.OwnerReferences, 1)
				assert.Equal(t, mcpRegistry.Name, sa.OwnerReferences[0].Name)

				// Verify Role
				role := &rbacv1.Role{}
				err = c.Get(ctx, types.NamespacedName{Name: resourceName, Namespace: mcpRegistry.Namespace}, role)
				require.NoError(t, err)
				assert.Equal(t, registryAPIRBACRules, role.Rules)
				require.Len(t, role.OwnerReferences, 1)
				assert.Equal(t, mcpRegistry.Name, role.OwnerReferences[0].Name)

				// Verify RoleBinding
				rb := &rbacv1.RoleBinding{}
				err = c.Get(ctx, types.NamespacedName{Name: resourceName, Namespace: mcpRegistry.Namespace}, rb)
				require.NoError(t, err)
				assert.Equal(t, resourceName, rb.RoleRef.Name)
				assert.Equal(t, "Role", rb.RoleRef.Kind)
				require.Len(t, rb.Subjects, 1)
				assert.Equal(t, resourceName, rb.Subjects[0].Name)
				require.Len(t, rb.OwnerReferences, 1)
				assert.Equal(t, mcpRegistry.Name, rb.OwnerReferences[0].Name)
			},
		},
		{
			name:        "is idempotent with existing resources",
			mcpRegistry: createTestMCPRegistry(),
			setupClient: func(t *testing.T) client.Client {
				t.Helper()
				mcpRegistry := createTestMCPRegistry()
				resourceName := mcpRegistry.Name + "-registry-api"
				return fake.NewClientBuilder().
					WithScheme(createTestScheme()).
					WithObjects(
						&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: mcpRegistry.Namespace}},
						&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: mcpRegistry.Namespace}, Rules: registryAPIRBACRules},
						&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: mcpRegistry.Namespace}, RoleRef: rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: resourceName}},
					).Build()
			},
			validate: func(t *testing.T, c client.Client, mcpRegistry *mcpv1alpha1.MCPRegistry) {
				t.Helper()
				ctx := context.Background()
				resourceName := mcpRegistry.Name + "-registry-api"
				role := &rbacv1.Role{}
				require.NoError(t, c.Get(ctx, types.NamespacedName{Name: resourceName, Namespace: mcpRegistry.Namespace}, role))
			},
		},
		{
			name:        "returns error when ServiceAccount creation fails",
			mcpRegistry: createTestMCPRegistry(),
			setupClient: func(t *testing.T) client.Client {
				t.Helper()
				return fake.NewClientBuilder().
					WithScheme(createTestScheme()).
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
			name:        "returns error when Role creation fails",
			mcpRegistry: createTestMCPRegistry(),
			setupClient: func(t *testing.T) client.Client {
				t.Helper()
				return fake.NewClientBuilder().
					WithScheme(createTestScheme()).
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
			name:        "returns error when RoleBinding creation fails",
			mcpRegistry: createTestMCPRegistry(),
			setupClient: func(t *testing.T) client.Client {
				t.Helper()
				return fake.NewClientBuilder().
					WithScheme(createTestScheme()).
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
			m := &manager{client: c, scheme: createTestScheme()}

			err := m.ensureRBACResources(context.Background(), tt.mcpRegistry)

			if tt.expectedError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
			} else {
				require.NoError(t, err)
				if tt.validate != nil {
					tt.validate(t, c, tt.mcpRegistry)
				}
			}
		})
	}
}

func TestRegistryAPIRBACRules(t *testing.T) {
	t.Parallel()

	require.Len(t, registryAPIRBACRules, 6)

	// ToolHive resources (MCP discovery)
	assert.ElementsMatch(t, []string{"toolhive.stacklok.dev"}, registryAPIRBACRules[0].APIGroups)
	assert.ElementsMatch(t, []string{"mcpservers", "mcpremoteproxies", "virtualmcpservers"}, registryAPIRBACRules[0].Resources)
	assert.ElementsMatch(t, []string{"get", "list", "watch"}, registryAPIRBACRules[0].Verbs)

	// Core services
	assert.ElementsMatch(t, []string{""}, registryAPIRBACRules[1].APIGroups)
	assert.ElementsMatch(t, []string{"services"}, registryAPIRBACRules[1].Resources)
	assert.ElementsMatch(t, []string{"get", "list", "watch"}, registryAPIRBACRules[1].Verbs)

	// Gateway API
	assert.ElementsMatch(t, []string{"gateway.networking.k8s.io"}, registryAPIRBACRules[2].APIGroups)
	assert.ElementsMatch(t, []string{"httproutes", "gateways"}, registryAPIRBACRules[2].Resources)
	assert.ElementsMatch(t, []string{"get", "list", "watch"}, registryAPIRBACRules[2].Verbs)

	// Leader election - ConfigMaps
	assert.ElementsMatch(t, []string{""}, registryAPIRBACRules[3].APIGroups)
	assert.ElementsMatch(t, []string{"configmaps"}, registryAPIRBACRules[3].Resources)
	assert.ElementsMatch(t, []string{"get", "list", "watch", "create", "update", "patch", "delete"}, registryAPIRBACRules[3].Verbs)

	// Leader election - Leases
	assert.ElementsMatch(t, []string{"coordination.k8s.io"}, registryAPIRBACRules[4].APIGroups)
	assert.ElementsMatch(t, []string{"leases"}, registryAPIRBACRules[4].Resources)
	assert.ElementsMatch(t, []string{"get", "list", "watch", "create", "update", "patch", "delete"}, registryAPIRBACRules[4].Verbs)

	// Leader election - Events
	assert.ElementsMatch(t, []string{""}, registryAPIRBACRules[5].APIGroups)
	assert.ElementsMatch(t, []string{"events"}, registryAPIRBACRules[5].Resources)
	assert.ElementsMatch(t, []string{"create", "patch"}, registryAPIRBACRules[5].Verbs)
}

package rbac

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// mockClient is a mock implementation of client.Client for testing error scenarios
type mockClient struct {
	client.Client
	getError    error
	createError error
	updateError error
	deleteError error
	// Control which resource type should fail
	failResourceType string
	// Track calls for verification
	getCalls    int
	createCalls int
	updateCalls int
	deleteCalls int
	// Custom function overrides
	GetFunc    func(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error
	CreateFunc func(ctx context.Context, obj client.Object, opts ...client.CreateOption) error
}

func (m *mockClient) Get(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
	m.getCalls++

	// Use custom function if provided
	if m.GetFunc != nil {
		return m.GetFunc(ctx, key, obj, opts...)
	}

	// Return specific error if configured
	if m.getError != nil {
		// Check if we should fail for this specific resource type
		if m.failResourceType == "" || m.shouldFailForResource(obj) {
			return m.getError
		}
	}

	// Default to NotFound to trigger create flow
	return apierrors.NewNotFound(schema.GroupResource{}, key.Name)
}

func (m *mockClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	m.createCalls++

	// Use custom function if provided
	if m.CreateFunc != nil {
		return m.CreateFunc(ctx, obj, opts...)
	}

	if m.createError != nil {
		if m.failResourceType == "" || m.shouldFailForResource(obj) {
			return m.createError
		}
	}
	return nil
}

func (m *mockClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	m.updateCalls++

	if m.updateError != nil {
		if m.failResourceType == "" || m.shouldFailForResource(obj) {
			return m.updateError
		}
	}
	return nil
}

func (m *mockClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	m.deleteCalls++

	if m.deleteError != nil {
		if m.failResourceType == "" || m.shouldFailForResource(obj) {
			return m.deleteError
		}
	}
	return nil
}

func (m *mockClient) shouldFailForResource(obj client.Object) bool {
	switch obj.(type) {
	case *rbacv1.Role:
		return m.failResourceType == "Role"
	case *corev1.ServiceAccount:
		return m.failResourceType == "ServiceAccount"
	case *rbacv1.RoleBinding:
		return m.failResourceType == "RoleBinding"
	default:
		return false
	}
}

// Test error during Get operation (non-NotFound error)
func TestManager_EnsureRBACResources_GetError(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name             string
		failResourceType string
		getError         error
		expectedError    string
	}{
		{
			name:             "Role Get fails",
			failResourceType: "Role",
			getError:         errors.New("network error"),
			expectedError:    "failed to get Role: network error",
		},
		{
			name:             "ServiceAccount Get fails",
			failResourceType: "ServiceAccount",
			getError:         errors.New("permission denied"),
			expectedError:    "failed to get ServiceAccount: permission denied",
		},
		{
			name:             "RoleBinding Get fails",
			failResourceType: "RoleBinding",
			getError:         errors.New("timeout"),
			expectedError:    "failed to get RoleBinding: timeout",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mockClient := &mockClient{
				getError:         tc.getError,
				failResourceType: tc.failResourceType,
			}

			manager := &manager{
				client:           mockClient,
				scheme:           createTestScheme(),
				defaultRBACRules: GetDefaultRBACRules(),
			}

			mcpServer := createTestMCPServer("test-server", "default")
			err := manager.EnsureRBACResources(context.TODO(), mcpServer)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.expectedError)
			assert.Greater(t, mockClient.getCalls, 0, "Get should have been called")
		})
	}
}

// Test error during Create operation
func TestManager_EnsureRBACResources_CreateError(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name             string
		failResourceType string
		createError      error
		expectedError    string
	}{
		{
			name:             "Role Create fails",
			failResourceType: "Role",
			createError:      errors.New("quota exceeded"),
			expectedError:    "failed to create Role: quota exceeded",
		},
		{
			name:             "ServiceAccount Create fails",
			failResourceType: "ServiceAccount",
			createError:      errors.New("invalid name"),
			expectedError:    "failed to create ServiceAccount: invalid name",
		},
		{
			name:             "RoleBinding Create fails",
			failResourceType: "RoleBinding",
			createError:      errors.New("conflict"),
			expectedError:    "failed to create RoleBinding: conflict",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mockClient := &mockClient{
				createError:      tc.createError,
				failResourceType: tc.failResourceType,
			}

			manager := &manager{
				client:           mockClient,
				scheme:           createTestScheme(),
				defaultRBACRules: GetDefaultRBACRules(),
			}

			mcpServer := createTestMCPServer("test-server", "default")
			err := manager.EnsureRBACResources(context.TODO(), mcpServer)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.expectedError)
			assert.Greater(t, mockClient.createCalls, 0, "Create should have been called")
		})
	}
}

// Test error during Update operation
func TestManager_EnsureRBACResources_UpdateError(t *testing.T) {
	t.Parallel()

	// For update tests, we need a mock that returns existing resources
	mockClientForUpdate := &mockClient{
		updateError: errors.New("update failed"),
	}

	// Override Get to return an existing resource that needs updating
	mockClientForUpdate.GetFunc = func(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
		// Populate the object to simulate it exists but needs updating
		switch v := obj.(type) {
		case *rbacv1.Role:
			v.ObjectMeta = metav1.ObjectMeta{
				Name:      key.Name,
				Namespace: key.Namespace,
			}
			// Set different rules to trigger update
			v.Rules = []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"pods"},
					Verbs:     []string{"get"},
				},
			}
		case *rbacv1.RoleBinding:
			v.ObjectMeta = metav1.ObjectMeta{
				Name:      key.Name,
				Namespace: key.Namespace,
			}
			// Set different roleRef to trigger update
			v.RoleRef = rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "Role",
				Name:     "different-role",
			}
		case *corev1.ServiceAccount:
			v.ObjectMeta = metav1.ObjectMeta{
				Name:      key.Name,
				Namespace: key.Namespace,
			}
		}
		return nil
	}

	manager := &manager{
		client:           mockClientForUpdate,
		scheme:           createTestScheme(),
		defaultRBACRules: GetDefaultRBACRules(),
	}

	mcpServer := createTestMCPServer("test-server", "default")
	err := manager.EnsureRBACResources(context.TODO(), mcpServer)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to update")
	assert.Greater(t, mockClientForUpdate.updateCalls, 0, "Update should have been called")
}


// Test multiple errors during EnsureRBACResources
func TestManager_EnsureRBACResources_MultipleErrors(t *testing.T) {
	t.Parallel()

	// First resource creation fails, should stop early
	mockClient := &mockClient{
		createError:      errors.New("first resource fails"),
		failResourceType: "Role", // Fail on the first resource type
	}

	manager := &manager{
		client:           mockClient,
		scheme:           createTestScheme(),
		defaultRBACRules: GetDefaultRBACRules(),
	}

	mcpServer := createTestMCPServer("test-server", "default")
	err := manager.EnsureRBACResources(context.TODO(), mcpServer)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to ensure proxy runner Role")
	// Should stop after first failure
	assert.Equal(t, 1, mockClient.createCalls, "Should stop after first create failure")
}

// Test with custom service account (should skip MCP server SA creation)
func TestManager_EnsureRBACResources_CustomServiceAccount_Error(t *testing.T) {
	t.Parallel()

	// Should only try to create 3 resources (Role, SA for proxy, RoleBinding)
	// and not the 4th (MCP server SA)
	callCount := 0
	mockClient := &mockClient{}
	mockClient.CreateFunc = func(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
		callCount++
		if callCount == 3 { // Fail on the third resource (RoleBinding)
			return errors.New("rolebinding creation failed")
		}
		return nil
	}
	mockClient.GetFunc = func(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
		return apierrors.NewNotFound(schema.GroupResource{}, key.Name)
	}

	manager := &manager{
		client:           mockClient,
		scheme:           createTestScheme(),
		defaultRBACRules: GetDefaultRBACRules(),
	}

	mcpServer := createTestMCPServer("test-server", "default")
	customSA := "custom-sa"
	mcpServer.Spec.ServiceAccount = &customSA

	err := manager.EnsureRBACResources(context.TODO(), mcpServer)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to ensure proxy runner RoleBinding")
	assert.Equal(t, 3, callCount, "Should only try to create 3 resources when custom SA is specified")
}

// Test concurrent calls (simulate race conditions)
func TestManager_EnsureRBACResources_ConcurrentCalls(t *testing.T) {
	t.Parallel()

	// This test ensures the manager handles concurrent calls gracefully
	mockClient := &mockClient{
		// Simulate occasional conflicts
		createError: apierrors.NewAlreadyExists(schema.GroupResource{}, "test"),
	}

	manager := &manager{
		client:           mockClient,
		scheme:           createTestScheme(),
		defaultRBACRules: GetDefaultRBACRules(),
	}

	mcpServer := createTestMCPServer("test-server", "default")

	// Run multiple goroutines trying to create the same resources
	done := make(chan error, 5)
	for i := 0; i < 5; i++ {
		go func() {
			done <- manager.EnsureRBACResources(context.TODO(), mcpServer)
		}()
	}

	// Collect results
	var errors []error
	for i := 0; i < 5; i++ {
		if err := <-done; err != nil {
			errors = append(errors, err)
		}
	}

	// All calls should have encountered the "already exists" error
	assert.Len(t, errors, 5, "All concurrent calls should have failed with already exists")
	for _, err := range errors {
		assert.Contains(t, err.Error(), "failed")
	}
}

// Test with nil MCPServer - should panic
func TestManager_EnsureRBACResources_NilMCPServer(t *testing.T) {
	t.Parallel()

	manager := NewManager(Config{
		Client: &mockClient{},
		Scheme: createTestScheme(),
	})

	// This should panic when trying to access mcpServer.Name
	assert.Panics(t, func() {
		_ = manager.EnsureRBACResources(context.TODO(), nil)
	}, "Should panic when MCPServer is nil")
}

// Test with empty MCPServer name - resources will have prefixes only
func TestManager_EnsureRBACResources_EmptyName(t *testing.T) {
	t.Parallel()

	createCount := 0
	mockClient := &mockClient{}
	mockClient.CreateFunc = func(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
		createCount++
		// Resources will have names like "-proxy-runner" and "-sa" which are valid
		// Check that the names have the expected suffixes
		name := obj.GetName()
		assert.True(t, name == "-proxy-runner" || name == "-sa",
			"Unexpected resource name: %s", name)
		return nil
	}

	manager := &manager{
		client:           mockClient,
		scheme:           createTestScheme(),
		defaultRBACRules: GetDefaultRBACRules(),
	}

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "", // Empty name
			Namespace: "default",
		},
	}

	err := manager.EnsureRBACResources(context.TODO(), mcpServer)

	// Should succeed with empty name - resources will have suffix-only names
	assert.NoError(t, err)
	assert.Equal(t, 4, createCount, "Should create all 4 resources even with empty name")
}

// Test with invalid namespace
func TestManager_EnsureRBACResources_InvalidNamespace(t *testing.T) {
	t.Parallel()

	mockClient := &mockClient{
		createError: errors.New("namespace does not exist"),
	}

	manager := &manager{
		client:           mockClient,
		scheme:           createTestScheme(),
		defaultRBACRules: GetDefaultRBACRules(),
	}

	mcpServer := createTestMCPServer("test-server", "non-existent-namespace")
	err := manager.EnsureRBACResources(context.TODO(), mcpServer)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "namespace does not exist")
}
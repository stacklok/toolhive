package groups

import (
	"context"
	goerr "errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/logger"
)

func init() {
	// Initialize logger for tests
	logger.Initialize()
}

// createTestScheme creates a test scheme with required types
func createTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(mcpv1alpha1.AddToScheme(scheme))
	return scheme
}

// createTestCRDManager creates a CRD manager with a fake client for testing
func createTestCRDManager(objs ...client.Object) (*crdManager, client.Client) {
	scheme := createTestScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	manager := NewCRDManager(fakeClient, "default").(*crdManager)
	return manager, fakeClient
}

func TestCRDManager_Create(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		groupName   string
		setupObjs   []client.Object
		expectError bool
		errorType   error
		errorMsg    string
	}{
		{
			name:        "successful creation",
			groupName:   "testgroup",
			setupObjs:   []client.Object{},
			expectError: false,
		},
		{
			name:      "group already exists",
			groupName: "existinggroup",
			setupObjs: []client.Object{
				&mcpv1alpha1.MCPGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "existinggroup",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPGroupSpec{},
				},
			},
			expectError: true,
			errorType:   ErrGroupAlreadyExists,
			errorMsg:    "already exists",
		},
		{
			name:        "invalid name - uppercase",
			groupName:   "MyGroup",
			setupObjs:   []client.Object{},
			expectError: true,
			errorType:   ErrInvalidGroupName,
			errorMsg:    "must be lowercase",
		},
		{
			name:        "invalid name - empty",
			groupName:   "",
			setupObjs:   []client.Object{},
			expectError: true,
			errorType:   ErrInvalidGroupName,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager, _ := createTestCRDManager(tt.setupObjs...)
			ctx := context.Background()

			err := manager.Create(ctx, tt.groupName)

			if tt.expectError {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.errorType)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)

				// Verify the group was created
				group, err := manager.Get(ctx, tt.groupName)
				require.NoError(t, err)
				assert.Equal(t, tt.groupName, group.Name)
				assert.Empty(t, group.RegisteredClients)
			}
		})
	}
}

func TestCRDManager_Get(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		groupName   string
		setupObjs   []client.Object
		expectError bool
		errorType   error
		expected    *Group
	}{
		{
			name:      "successful get",
			groupName: "testgroup",
			setupObjs: []client.Object{
				&mcpv1alpha1.MCPGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "testgroup",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPGroupSpec{},
				},
			},
			expectError: false,
			expected: &Group{
				Name:              "testgroup",
				RegisteredClients: []string{},
			},
		},
		{
			name:        "group not found",
			groupName:   "nonexistent",
			setupObjs:   []client.Object{},
			expectError: true,
			errorType:   ErrGroupNotFound,
		},
		{
			name:      "group with no registered clients",
			groupName: "emptygroup",
			setupObjs: []client.Object{
				&mcpv1alpha1.MCPGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "emptygroup",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPGroupSpec{},
				},
			},
			expectError: false,
			expected: &Group{
				Name:              "emptygroup",
				RegisteredClients: []string{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager, _ := createTestCRDManager(tt.setupObjs...)
			ctx := context.Background()

			group, err := manager.Get(ctx, tt.groupName)

			if tt.expectError {
				require.Error(t, err)
				if goerr.Is(err, tt.errorType) {
					assert.ErrorIs(t, err, tt.errorType)
				}
				assert.Nil(t, group)
			} else {
				require.NoError(t, err)
				require.NotNil(t, group)
				assert.Equal(t, tt.expected.Name, group.Name)
				assert.Equal(t, tt.expected.RegisteredClients, group.RegisteredClients)
			}
		})
	}
}

func TestCRDManager_List(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupObjs []client.Object
		expected  []*Group
	}{
		{
			name:      "empty list",
			setupObjs: []client.Object{},
			expected:  []*Group{},
		},
		{
			name: "list multiple groups",
			setupObjs: []client.Object{
				&mcpv1alpha1.MCPGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "group1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPGroupSpec{},
				},
				&mcpv1alpha1.MCPGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "group2",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPGroupSpec{},
				},
				&mcpv1alpha1.MCPGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "agroup",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPGroupSpec{},
				},
			},
			expected: []*Group{
				{
					Name:              "agroup",
					RegisteredClients: []string{},
				},
				{
					Name:              "group1",
					RegisteredClients: []string{},
				},
				{
					Name:              "group2",
					RegisteredClients: []string{},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager, _ := createTestCRDManager(tt.setupObjs...)
			ctx := context.Background()

			groups, err := manager.List(ctx)

			require.NoError(t, err)
			require.Len(t, groups, len(tt.expected))

			for i, expected := range tt.expected {
				assert.Equal(t, expected.Name, groups[i].Name)
				assert.Equal(t, expected.RegisteredClients, groups[i].RegisteredClients)
			}
		})
	}
}

func TestCRDManager_Delete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		groupName   string
		setupObjs   []client.Object
		expectError bool
		errorType   error
	}{
		{
			name:      "successful deletion",
			groupName: "testgroup",
			setupObjs: []client.Object{
				&mcpv1alpha1.MCPGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "testgroup",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPGroupSpec{},
				},
			},
			expectError: false,
		},
		{
			name:        "group not found",
			groupName:   "nonexistent",
			setupObjs:   []client.Object{},
			expectError: true,
			errorType:   ErrGroupNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager, fakeClient := createTestCRDManager(tt.setupObjs...)
			ctx := context.Background()

			err := manager.Delete(ctx, tt.groupName)

			if tt.expectError {
				require.Error(t, err)
				if goerr.Is(err, tt.errorType) {
					assert.ErrorIs(t, err, tt.errorType)
				}
			} else {
				assert.NoError(t, err)

				// Verify the group was deleted
				mcpGroup := &mcpv1alpha1.MCPGroup{}
				err := fakeClient.Get(ctx, client.ObjectKey{
					Name:      tt.groupName,
					Namespace: "default",
				}, mcpGroup)
				assert.True(t, errors.IsNotFound(err), "Group should be deleted")
			}
		})
	}
}

func TestCRDManager_Exists(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		groupName   string
		setupObjs   []client.Object
		expected    bool
		expectError bool
	}{
		{
			name:      "group exists",
			groupName: "testgroup",
			setupObjs: []client.Object{
				&mcpv1alpha1.MCPGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "testgroup",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPGroupSpec{},
				},
			},
			expected:    true,
			expectError: false,
		},
		{
			name:        "group does not exist",
			groupName:   "nonexistent",
			setupObjs:   []client.Object{},
			expected:    false,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager, _ := createTestCRDManager(tt.setupObjs...)
			ctx := context.Background()

			exists, err := manager.Exists(ctx, tt.groupName)

			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, exists)
			}
		})
	}
}

func TestCRDManager_RegisterClients(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		groupNames  []string
		clientNames []string
		setupObjs   []client.Object
		expectError bool
	}{
		{
			name:        "register clients to single group (no-op)",
			groupNames:  []string{"testgroup"},
			clientNames: []string{"client1", "client2"},
			setupObjs: []client.Object{
				&mcpv1alpha1.MCPGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "testgroup",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPGroupSpec{},
				},
			},
			expectError: false,
		},
		{
			name:        "register clients to multiple groups (no-op)",
			groupNames:  []string{"group1", "group2"},
			clientNames: []string{"client1"},
			setupObjs: []client.Object{
				&mcpv1alpha1.MCPGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "group1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPGroupSpec{},
				},
				&mcpv1alpha1.MCPGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "group2",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPGroupSpec{},
				},
			},
			expectError: false,
		},
		{
			name:        "register clients with non-existent groups (no-op)",
			groupNames:  []string{"nonexistent"},
			clientNames: []string{"client1"},
			setupObjs:   []client.Object{},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager, _ := createTestCRDManager(tt.setupObjs...)
			ctx := context.Background()

			err := manager.RegisterClients(ctx, tt.groupNames, tt.clientNames)

			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCRDManager_UnregisterClients(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		groupNames  []string
		clientNames []string
		setupObjs   []client.Object
		expectError bool
	}{
		{
			name:        "unregister clients from single group (no-op)",
			groupNames:  []string{"testgroup"},
			clientNames: []string{"client1"},
			setupObjs: []client.Object{
				&mcpv1alpha1.MCPGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "testgroup",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPGroupSpec{},
				},
			},
			expectError: false,
		},
		{
			name:        "unregister multiple clients (no-op)",
			groupNames:  []string{"testgroup"},
			clientNames: []string{"client1", "client2"},
			setupObjs: []client.Object{
				&mcpv1alpha1.MCPGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "testgroup",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPGroupSpec{},
				},
			},
			expectError: false,
		},
		{
			name:        "unregister from multiple groups (no-op)",
			groupNames:  []string{"group1", "group2"},
			clientNames: []string{"client1"},
			setupObjs: []client.Object{
				&mcpv1alpha1.MCPGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "group1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPGroupSpec{},
				},
				&mcpv1alpha1.MCPGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "group2",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPGroupSpec{},
				},
			},
			expectError: false,
		},
		{
			name:        "unregister with non-existent groups (no-op)",
			groupNames:  []string{"nonexistent"},
			clientNames: []string{"client1"},
			setupObjs:   []client.Object{},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager, _ := createTestCRDManager(tt.setupObjs...)
			ctx := context.Background()

			err := manager.UnregisterClients(ctx, tt.groupNames, tt.clientNames)

			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestMCPGroupToGroup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		mcpGroup *mcpv1alpha1.MCPGroup
		expected *Group
	}{
		{
			name: "basic group conversion",
			mcpGroup: &mcpv1alpha1.MCPGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testgroup",
				},
				Spec: mcpv1alpha1.MCPGroupSpec{},
			},
			expected: &Group{
				Name:              "testgroup",
				RegisteredClients: []string{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := mcpGroupToGroup(tt.mcpGroup)
			assert.Equal(t, tt.expected.Name, result.Name)
			assert.Equal(t, tt.expected.RegisteredClients, result.RegisteredClients)
		})
	}
}

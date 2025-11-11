package workloads

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/workloads/k8s"
)

const (
	defaultNamespace = "default"
	testWorkload1    = "workload1"
)

// mockClient is a mock implementation of client.Client for testing
type mockClient struct {
	client.Client
	getFunc    func(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error
	listFunc   func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error
	updateFunc func(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error
}

func (m *mockClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if m.getFunc != nil {
		return m.getFunc(ctx, key, obj, opts...)
	}
	return k8serrors.NewNotFound(schema.GroupResource{}, key.Name)
}

func (m *mockClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if m.listFunc != nil {
		return m.listFunc(ctx, list, opts...)
	}
	return nil
}

func (m *mockClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if m.updateFunc != nil {
		return m.updateFunc(ctx, obj, opts...)
	}
	return nil
}

func TestNewK8SManager(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		k8sClient client.Client
		namespace string
		wantError bool
	}{
		{
			name:      "successful creation",
			k8sClient: &mockClient{},
			namespace: defaultNamespace,
			wantError: false,
		},
		{
			name:      "empty namespace",
			k8sClient: &mockClient{},
			namespace: "",
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager, err := NewK8SManager(tt.k8sClient, tt.namespace)

			if tt.wantError {
				require.Error(t, err)
				assert.Nil(t, manager)
			} else {
				require.NoError(t, err)
				require.NotNil(t, manager)

				k8sMgr, ok := manager.(*k8sManager)
				require.True(t, ok)
				assert.Equal(t, tt.k8sClient, k8sMgr.k8sClient)
				assert.Equal(t, tt.namespace, k8sMgr.namespace)
			}
		})
	}
}

func TestK8SManager_GetWorkload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		workloadName string
		setupMock    func(*mockClient)
		wantError    bool
		errorMsg     string
		expected     k8s.Workload
	}{
		{
			name:         "successful get",
			workloadName: "test-workload",
			setupMock: func(mc *mockClient) {
				mc.getFunc = func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
					if mcpServer, ok := obj.(*mcpv1alpha1.MCPServer); ok {
						mcpServer.Name = "test-workload"
						mcpServer.Namespace = defaultNamespace
						mcpServer.Status.Phase = mcpv1alpha1.MCPServerPhaseRunning
						mcpServer.Spec.Transport = "streamable-http"
						mcpServer.Spec.ProxyPort = 8080
						mcpServer.Annotations = map[string]string{
							"group": "test-group",
						}
					}
					return nil
				}
			},
			wantError: false,
			expected: k8s.Workload{
				Name:      "test-workload",
				Namespace: defaultNamespace,
				Phase:     mcpv1alpha1.MCPServerPhaseRunning,
				URL:       "http://127.0.0.1:8080/mcp", // URL is generated from spec
				Labels: map[string]string{
					"group": "test-group",
				},
			},
		},
		{
			name:         "workload not found",
			workloadName: "non-existent",
			setupMock: func(mc *mockClient) {
				mc.getFunc = func(_ context.Context, key client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
					return k8serrors.NewNotFound(schema.GroupResource{Resource: "mcpservers"}, key.Name)
				}
			},
			wantError: true,
			errorMsg:  "MCPServer non-existent not found",
		},
		{
			name:         "get error",
			workloadName: "error-workload",
			setupMock: func(mc *mockClient) {
				mc.getFunc = func(_ context.Context, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
					return k8serrors.NewInternalError(errors.New("internal error"))
				}
			},
			wantError: true,
			errorMsg:  "failed to get MCPServer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mockClient := &mockClient{}
			tt.setupMock(mockClient)

			manager := &k8sManager{
				k8sClient: mockClient,
				namespace: defaultNamespace,
			}

			ctx := context.Background()
			result, err := manager.GetWorkload(ctx, tt.workloadName)

			if tt.wantError {
				require.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected.Name, result.Name)
				assert.Equal(t, tt.expected.Phase, result.Phase)
				assert.Equal(t, tt.expected.URL, result.URL)
			}
		})
	}
}

func TestK8SManager_DoesWorkloadExist(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		workloadName string
		setupMock    func(*mockClient)
		expected     bool
		wantError    bool
	}{
		{
			name:         "workload exists",
			workloadName: "existing-workload",
			setupMock: func(mc *mockClient) {
				mc.getFunc = func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
					if mcpServer, ok := obj.(*mcpv1alpha1.MCPServer); ok {
						mcpServer.Name = "existing-workload"
					}
					return nil
				}
			},
			expected:  true,
			wantError: false,
		},
		{
			name:         "workload does not exist",
			workloadName: "non-existent",
			setupMock: func(mc *mockClient) {
				mc.getFunc = func(_ context.Context, key client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
					return k8serrors.NewNotFound(schema.GroupResource{Resource: "mcpservers"}, key.Name)
				}
			},
			expected:  false,
			wantError: false,
		},
		{
			name:         "get error",
			workloadName: "error-workload",
			setupMock: func(mc *mockClient) {
				mc.getFunc = func(_ context.Context, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
					return k8serrors.NewInternalError(errors.New("internal error"))
				}
			},
			expected:  false,
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mockClient := &mockClient{}
			tt.setupMock(mockClient)

			manager := &k8sManager{
				k8sClient: mockClient,
				namespace: defaultNamespace,
			}

			ctx := context.Background()
			result, err := manager.DoesWorkloadExist(ctx, tt.workloadName)

			if tt.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestK8SManager_ListWorkloadsInGroup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		groupName string
		setupMock func(*mockClient)
		expected  []string
		wantError bool
		errorMsg  string
	}{
		{
			name:      "successful list",
			groupName: "test-group",
			setupMock: func(mc *mockClient) {
				mc.listFunc = func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					if mcpServerList, ok := list.(*mcpv1alpha1.MCPServerList); ok {
						mcpServerList.Items = []mcpv1alpha1.MCPServer{
							{
								ObjectMeta: metav1.ObjectMeta{
									Name: testWorkload1,
								},
								Spec: mcpv1alpha1.MCPServerSpec{
									GroupRef: "test-group",
								},
							},
							{
								ObjectMeta: metav1.ObjectMeta{
									Name: "workload2",
								},
								Spec: mcpv1alpha1.MCPServerSpec{
									GroupRef: "test-group",
								},
							},
							{
								ObjectMeta: metav1.ObjectMeta{
									Name: "workload3",
								},
								Spec: mcpv1alpha1.MCPServerSpec{
									GroupRef: "other-group",
								},
							},
						}
					}
					return nil
				}
			},
			expected:  []string{testWorkload1, "workload2"},
			wantError: false,
		},
		{
			name:      "empty group",
			groupName: "empty-group",
			setupMock: func(mc *mockClient) {
				mc.listFunc = func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					if mcpServerList, ok := list.(*mcpv1alpha1.MCPServerList); ok {
						mcpServerList.Items = []mcpv1alpha1.MCPServer{}
					}
					return nil
				}
			},
			expected:  []string{},
			wantError: false,
		},
		{
			name:      "list error",
			groupName: "test-group",
			setupMock: func(mc *mockClient) {
				mc.listFunc = func(_ context.Context, _ client.ObjectList, _ ...client.ListOption) error {
					return k8serrors.NewInternalError(errors.New("internal error"))
				}
			},
			expected:  nil,
			wantError: true,
			errorMsg:  "failed to list MCPServers",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mockClient := &mockClient{}
			tt.setupMock(mockClient)

			manager := &k8sManager{
				k8sClient: mockClient,
				namespace: defaultNamespace,
			}

			ctx := context.Background()
			result, err := manager.ListWorkloadsInGroup(ctx, tt.groupName)

			if tt.wantError {
				require.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				require.NoError(t, err)
				assert.ElementsMatch(t, tt.expected, result)
			}
		})
	}
}

func TestK8SManager_ListWorkloads(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		listAll      bool
		labelFilters []string
		setupMock    func(*mockClient)
		expected     int
		wantError    bool
		errorMsg     string
	}{
		{
			name:    "successful list all",
			listAll: true,
			setupMock: func(mc *mockClient) {
				mc.listFunc = func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					if mcpServerList, ok := list.(*mcpv1alpha1.MCPServerList); ok {
						mcpServerList.Items = []mcpv1alpha1.MCPServer{
							{
								ObjectMeta: metav1.ObjectMeta{Name: testWorkload1},
								Status:     mcpv1alpha1.MCPServerStatus{Phase: mcpv1alpha1.MCPServerPhaseRunning},
							},
							{
								ObjectMeta: metav1.ObjectMeta{Name: "workload2"},
								Status:     mcpv1alpha1.MCPServerStatus{Phase: mcpv1alpha1.MCPServerPhaseTerminating},
							},
						}
					}
					return nil
				}
			},
			expected:  2,
			wantError: false,
		},
		{
			name:         "list with label filters",
			listAll:      true,
			labelFilters: []string{"env=prod"},
			setupMock: func(mc *mockClient) {
				mc.listFunc = func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					if mcpServerList, ok := list.(*mcpv1alpha1.MCPServerList); ok {
						mcpServerList.Items = []mcpv1alpha1.MCPServer{
							{
								ObjectMeta: metav1.ObjectMeta{
									Name:   testWorkload1,
									Labels: map[string]string{"env": "prod"},
								},
								Status: mcpv1alpha1.MCPServerStatus{Phase: mcpv1alpha1.MCPServerPhaseRunning},
							},
						}
					}
					return nil
				}
			},
			expected:  1,
			wantError: false,
		},
		{
			name:         "invalid label filter",
			listAll:      true,
			labelFilters: []string{"invalid-filter"},
			setupMock: func(*mockClient) {
				// No list call expected due to filter parsing error
			},
			expected:  0,
			wantError: true,
			errorMsg:  "failed to parse label filters",
		},
		{
			name:    "list error",
			listAll: true,
			setupMock: func(mc *mockClient) {
				mc.listFunc = func(_ context.Context, _ client.ObjectList, _ ...client.ListOption) error {
					return k8serrors.NewInternalError(errors.New("internal error"))
				}
			},
			expected:  0,
			wantError: true,
			errorMsg:  "failed to list MCPServers",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mockClient := &mockClient{}
			tt.setupMock(mockClient)

			manager := &k8sManager{
				k8sClient: mockClient,
				namespace: defaultNamespace,
			}

			ctx := context.Background()
			result, err := manager.ListWorkloads(ctx, tt.listAll, tt.labelFilters...)

			if tt.wantError {
				require.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				require.NoError(t, err)
				assert.Len(t, result, tt.expected)
			}
		})
	}
}

func TestK8SManager_MoveToGroup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		workloadNames []string
		groupFrom     string
		groupTo       string
		setupMock     func(*mockClient)
		wantError     bool
		errorMsg      string
	}{
		{
			name:          "successful move",
			workloadNames: []string{testWorkload1},
			groupFrom:     "old-group",
			groupTo:       "new-group",
			setupMock: func(mc *mockClient) {
				mc.getFunc = func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
					if mcpServer, ok := obj.(*mcpv1alpha1.MCPServer); ok {
						mcpServer.Name = testWorkload1
						mcpServer.Namespace = defaultNamespace
						mcpServer.Spec.GroupRef = "old-group"
					}
					return nil
				}
				mc.updateFunc = func(_ context.Context, _ client.Object, _ ...client.UpdateOption) error {
					return nil
				}
			},
			wantError: false,
		},
		{
			name:          "workload not found",
			workloadNames: []string{"non-existent"},
			groupFrom:     "old-group",
			groupTo:       "new-group",
			setupMock: func(mc *mockClient) {
				mc.getFunc = func(_ context.Context, key client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
					return k8serrors.NewNotFound(schema.GroupResource{Resource: "mcpservers"}, key.Name)
				}
			},
			wantError: true,
			errorMsg:  "MCPServer",
		},
		{
			name:          "workload in different group",
			workloadNames: []string{testWorkload1},
			groupFrom:     "old-group",
			groupTo:       "new-group",
			setupMock: func(mc *mockClient) {
				mc.getFunc = func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
					if mcpServer, ok := obj.(*mcpv1alpha1.MCPServer); ok {
						mcpServer.Name = testWorkload1
						mcpServer.Namespace = defaultNamespace
						mcpServer.Spec.GroupRef = "different-group"
					}
					return nil
				}
			},
			wantError: true, // Returns error when group doesn't match
			errorMsg:  "is not in group",
		},
		{
			name:          "update error",
			workloadNames: []string{testWorkload1},
			groupFrom:     "old-group",
			groupTo:       "new-group",
			setupMock: func(mc *mockClient) {
				mc.getFunc = func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
					if mcpServer, ok := obj.(*mcpv1alpha1.MCPServer); ok {
						mcpServer.Name = testWorkload1
						mcpServer.Namespace = defaultNamespace
						mcpServer.Spec.GroupRef = "old-group"
					}
					return nil
				}
				mc.updateFunc = func(_ context.Context, _ client.Object, _ ...client.UpdateOption) error {
					return k8serrors.NewInternalError(errors.New("update failed"))
				}
			},
			wantError: true,
			errorMsg:  "failed to update MCPServer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mockClient := &mockClient{}
			tt.setupMock(mockClient)

			manager := &k8sManager{
				k8sClient: mockClient,
				namespace: defaultNamespace,
			}

			ctx := context.Background()
			err := manager.MoveToGroup(ctx, tt.workloadNames, tt.groupFrom, tt.groupTo)

			if tt.wantError {
				require.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestK8SManager_NoOpMethods(t *testing.T) {
	t.Parallel()

	mockClient := &mockClient{}
	manager := &k8sManager{
		k8sClient: mockClient,
		namespace: defaultNamespace,
	}

	ctx := context.Background()

	t.Run("GetLogs returns error", func(t *testing.T) {
		t.Parallel()
		logs, err := manager.GetLogs(ctx, testWorkload1, false)
		require.Error(t, err)
		assert.Empty(t, logs)
		assert.Contains(t, err.Error(), "not fully implemented")
	})

	t.Run("GetProxyLogs returns error", func(t *testing.T) {
		t.Parallel()
		logs, err := manager.GetProxyLogs(ctx, testWorkload1)
		require.Error(t, err)
		assert.Empty(t, logs)
		assert.Contains(t, err.Error(), "not fully implemented")
	})
}

func TestK8SManager_mcpServerToK8SWorkload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mcpServer *mcpv1alpha1.MCPServer
		expected  k8s.Workload
	}{
		{
			name: "running workload with HTTP transport",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-workload",
					Namespace: defaultNamespace,
					Annotations: map[string]string{
						"group": "test-group",
						"env":   "prod",
					},
				},
				Status: mcpv1alpha1.MCPServerStatus{
					Phase: mcpv1alpha1.MCPServerPhaseRunning,
					URL:   "http://localhost:8080",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Transport: "streamable-http",
					ProxyPort: 8080,
				},
			},
			expected: k8s.Workload{
				Name:      "test-workload",
				Namespace: defaultNamespace,
				Phase:     mcpv1alpha1.MCPServerPhaseRunning,
				URL:       "http://localhost:8080",
				Labels:    map[string]string{"group": "test-group", "env": "prod"},
			},
		},
		{
			name: "terminating workload",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "terminating-workload",
					Namespace: defaultNamespace,
				},
				Status: mcpv1alpha1.MCPServerStatus{
					Phase: mcpv1alpha1.MCPServerPhaseTerminating,
				},
			},
			expected: k8s.Workload{
				Name:      "terminating-workload",
				Namespace: defaultNamespace,
				Phase:     mcpv1alpha1.MCPServerPhaseTerminating,
			},
		},
		{
			name: "failed workload",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "failed-workload",
					Namespace: defaultNamespace,
				},
				Status: mcpv1alpha1.MCPServerStatus{
					Phase: mcpv1alpha1.MCPServerPhaseFailed,
				},
			},
			expected: k8s.Workload{
				Name:      "failed-workload",
				Namespace: defaultNamespace,
				Phase:     mcpv1alpha1.MCPServerPhaseFailed,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager := &k8sManager{
				namespace: defaultNamespace,
			}

			result, err := manager.mcpServerToK8SWorkload(tt.mcpServer)
			require.NoError(t, err)

			assert.Equal(t, tt.expected.Name, result.Name)
			assert.Equal(t, tt.expected.Namespace, result.Namespace)
			assert.Equal(t, tt.expected.Phase, result.Phase)
			assert.Equal(t, tt.expected.URL, result.URL)
			if tt.expected.Labels != nil {
				assert.Equal(t, tt.expected.Labels, result.Labels)
			}
		})
	}
}

package server

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	runtime "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	registrymocks "github.com/stacklok/toolhive/pkg/registry/mocks"
	regtypes "github.com/stacklok/toolhive/pkg/registry/registry"
	"github.com/stacklok/toolhive/pkg/workloads"
	workloadsmocks "github.com/stacklok/toolhive/pkg/workloads/mocks"
)

func TestHandler_SearchRegistry_WithMocks(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	t.Cleanup(func() { ctrl.Finish() })

	tests := []struct {
		name        string
		query       string
		mockServers []regtypes.ServerMetadata
		setupMocks  func(*registrymocks.MockProvider)
		wantErr     bool
		checkResult func(*testing.T, *mcp.CallToolResult)
	}{
		{
			name:  "successful search with results",
			query: "test",
			mockServers: []regtypes.ServerMetadata{
				&regtypes.ImageMetadata{
					BaseServerMetadata: regtypes.BaseServerMetadata{
						Name:        "test-server",
						Description: "Test server description",
						Transport:   "sse",
						Tools:       []string{"tool1", "tool2"},
						Tags:        []string{"tag1", "tag2"},
					},
					Image: "test/image:latest",
				},
				&regtypes.ImageMetadata{
					BaseServerMetadata: regtypes.BaseServerMetadata{
						Name:        "another-test",
						Description: "Another test server",
						Transport:   "stdio",
					},
					Image: "test/another:v1",
				},
			},
			setupMocks: func(m *registrymocks.MockProvider) {
				m.EXPECT().
					SearchServers("test").
					Return([]regtypes.ServerMetadata{
						&regtypes.ImageMetadata{
							BaseServerMetadata: regtypes.BaseServerMetadata{
								Name:        "test-server",
								Description: "Test server description",
								Transport:   "sse",
								Tools:       []string{"tool1", "tool2"},
								Tags:        []string{"tag1", "tag2"},
							},
							Image: "test/image:latest",
						},
						&regtypes.ImageMetadata{
							BaseServerMetadata: regtypes.BaseServerMetadata{
								Name:        "another-test",
								Description: "Another test server",
								Transport:   "stdio",
							},
							Image: "test/another:v1",
						},
					}, nil)
			},
			wantErr: false,
			checkResult: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				assert.NotNil(t, result)
				assert.False(t, result.IsError)
			},
		},
		{
			name:        "empty search results",
			query:       "nonexistent",
			mockServers: []regtypes.ServerMetadata{},
			setupMocks: func(m *registrymocks.MockProvider) {
				m.EXPECT().
					SearchServers("nonexistent").
					Return([]regtypes.ServerMetadata{}, nil)
			},
			wantErr: false,
			checkResult: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				assert.NotNil(t, result)
				assert.False(t, result.IsError)
			},
		},
		{
			name:  "search error",
			query: "error",
			setupMocks: func(m *registrymocks.MockProvider) {
				m.EXPECT().
					SearchServers("error").
					Return(nil, assert.AnError)
			},
			wantErr: false, // Handler returns error as tool result, not actual error
			checkResult: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				assert.NotNil(t, result)
				assert.True(t, result.IsError)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mockRegistry := registrymocks.NewMockProvider(ctrl)
			mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)

			if tt.setupMocks != nil {
				tt.setupMocks(mockRegistry)
			}

			handler := &Handler{
				ctx:              context.Background(),
				workloadManager:  mockWorkloadManager,
				registryProvider: mockRegistry,
			}

			request := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Name: "search_registry",
					Arguments: map[string]interface{}{
						"query": tt.query,
					},
				},
			}

			result, err := handler.SearchRegistry(context.Background(), request)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.checkResult != nil {
					tt.checkResult(t, result)
				}
			}
		})
	}
}

func TestHandler_ListServers_WithMocks(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	t.Cleanup(func() { ctrl.Finish() })

	tests := []struct {
		name        string
		workloads   []core.Workload
		setupMocks  func(*workloadsmocks.MockManager)
		wantErr     bool
		checkResult func(*testing.T, *mcp.CallToolResult)
	}{
		{
			name: "list multiple workloads",
			workloads: []core.Workload{
				{
					Name:   "server1",
					Status: runtime.WorkloadStatusRunning,
					Port:   8080,
					Labels: map[string]string{
						"toolhive.server": "test-server",
					},
				},
				{
					Name:   "server2",
					Status: runtime.WorkloadStatusStopped,
					Labels: map[string]string{
						"toolhive.server": "another-server",
					},
				},
			},
			setupMocks: func(m *workloadsmocks.MockManager) {
				m.EXPECT().
					ListWorkloads(gomock.Any(), true).
					Return([]core.Workload{
						{
							Name:   "server1",
							Status: runtime.WorkloadStatusRunning,
							Port:   8080,
							Labels: map[string]string{
								"toolhive.server": "test-server",
							},
						},
						{
							Name:   "server2",
							Status: runtime.WorkloadStatusStopped,
							Labels: map[string]string{
								"toolhive.server": "another-server",
							},
						},
					}, nil)
			},
			wantErr: false,
			checkResult: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				assert.NotNil(t, result)
				assert.False(t, result.IsError)
			},
		},
		{
			name: "empty workload list",
			setupMocks: func(m *workloadsmocks.MockManager) {
				m.EXPECT().
					ListWorkloads(gomock.Any(), true).
					Return([]core.Workload{}, nil)
			},
			wantErr: false,
			checkResult: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				assert.NotNil(t, result)
				assert.False(t, result.IsError)
			},
		},
		{
			name: "list error",
			setupMocks: func(m *workloadsmocks.MockManager) {
				m.EXPECT().
					ListWorkloads(gomock.Any(), true).
					Return(nil, assert.AnError)
			},
			wantErr: false,
			checkResult: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				assert.NotNil(t, result)
				assert.True(t, result.IsError)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mockRegistry := registrymocks.NewMockProvider(ctrl)
			mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)

			if tt.setupMocks != nil {
				tt.setupMocks(mockWorkloadManager)
			}

			handler := &Handler{
				ctx:              context.Background(),
				workloadManager:  mockWorkloadManager,
				registryProvider: mockRegistry,
			}

			result, err := handler.ListServers(context.Background(), mcp.CallToolRequest{})

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.checkResult != nil {
					tt.checkResult(t, result)
				}
			}
		})
	}
}

func TestHandler_StopServer_WithMocks(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	t.Cleanup(func() { ctrl.Finish() })

	tests := []struct {
		name        string
		serverName  string
		setupMocks  func(*workloadsmocks.MockManager)
		wantErr     bool
		checkResult func(*testing.T, *mcp.CallToolResult)
	}{
		{
			name:       "successful stop",
			serverName: "test-server",
			setupMocks: func(m *workloadsmocks.MockManager) {
				complete := func() error { return nil }
				m.EXPECT().
					StopWorkloads(gomock.Any(), []string{"test-server"}).
					Return(workloads.CompletionFunc(complete), nil)
			},
			wantErr: false,
			checkResult: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				assert.NotNil(t, result)
				assert.False(t, result.IsError)
			},
		},
		{
			name:       "stop error",
			serverName: "test-server",
			setupMocks: func(m *workloadsmocks.MockManager) {
				m.EXPECT().
					StopWorkloads(gomock.Any(), []string{"test-server"}).
					Return(nil, assert.AnError)
			},
			wantErr: false,
			checkResult: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				assert.NotNil(t, result)
				assert.True(t, result.IsError)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mockRegistry := registrymocks.NewMockProvider(ctrl)
			mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)

			if tt.setupMocks != nil {
				tt.setupMocks(mockWorkloadManager)
			}

			handler := &Handler{
				ctx:              context.Background(),
				workloadManager:  mockWorkloadManager,
				registryProvider: mockRegistry,
			}

			request := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Name: "stop_server",
					Arguments: map[string]interface{}{
						"name": tt.serverName,
					},
				},
			}

			result, err := handler.StopServer(context.Background(), request)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.checkResult != nil {
					tt.checkResult(t, result)
				}
			}
		})
	}
}

func TestHandler_RemoveServer_WithMocks(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	t.Cleanup(func() { ctrl.Finish() })

	tests := []struct {
		name        string
		serverName  string
		setupMocks  func(*workloadsmocks.MockManager)
		wantErr     bool
		checkResult func(*testing.T, *mcp.CallToolResult)
	}{
		{
			name:       "successful remove",
			serverName: "test-server",
			setupMocks: func(m *workloadsmocks.MockManager) {
				complete := func() error { return nil }
				m.EXPECT().
					DeleteWorkloads(gomock.Any(), []string{"test-server"}).
					Return(workloads.CompletionFunc(complete), nil)
			},
			wantErr: false,
			checkResult: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				assert.NotNil(t, result)
				assert.False(t, result.IsError)
			},
		},
		{
			name:       "remove error",
			serverName: "test-server",
			setupMocks: func(m *workloadsmocks.MockManager) {
				m.EXPECT().
					DeleteWorkloads(gomock.Any(), []string{"test-server"}).
					Return(nil, assert.AnError)
			},
			wantErr: false,
			checkResult: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				assert.NotNil(t, result)
				assert.True(t, result.IsError)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mockRegistry := registrymocks.NewMockProvider(ctrl)
			mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)

			if tt.setupMocks != nil {
				tt.setupMocks(mockWorkloadManager)
			}

			handler := &Handler{
				ctx:              context.Background(),
				workloadManager:  mockWorkloadManager,
				registryProvider: mockRegistry,
			}

			request := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Name: "remove_server",
					Arguments: map[string]interface{}{
						"name": tt.serverName,
					},
				},
			}

			result, err := handler.RemoveServer(context.Background(), request)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.checkResult != nil {
					tt.checkResult(t, result)
				}
			}
		})
	}
}

func TestHandler_GetServerLogs_WithMocks(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	t.Cleanup(func() { ctrl.Finish() })

	tests := []struct {
		name        string
		serverName  string
		logs        string
		setupMocks  func(*workloadsmocks.MockManager)
		wantErr     bool
		checkResult func(*testing.T, *mcp.CallToolResult)
	}{
		{
			name:       "successful get logs",
			serverName: "test-server",
			logs:       "2024-01-01 12:00:00 Server started\n2024-01-01 12:00:01 Listening on port 8080",
			setupMocks: func(m *workloadsmocks.MockManager) {
				m.EXPECT().
					GetLogs(gomock.Any(), "test-server", false, 0).
					Return("2024-01-01 12:00:00 Server started\n2024-01-01 12:00:01 Listening on port 8080", nil)
			},
			wantErr: false,
			checkResult: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				assert.NotNil(t, result)
				assert.False(t, result.IsError)
				// When using NewToolResultText, the content is a text result
				assert.NotEmpty(t, result.Content)
			},
		},
		{
			name:       "server not found",
			serverName: "nonexistent",
			setupMocks: func(m *workloadsmocks.MockManager) {
				m.EXPECT().
					GetLogs(gomock.Any(), "nonexistent", false, 0).
					Return("", assert.AnError)
			},
			wantErr: false,
			checkResult: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				assert.NotNil(t, result)
				assert.True(t, result.IsError)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mockRegistry := registrymocks.NewMockProvider(ctrl)
			mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)

			if tt.setupMocks != nil {
				tt.setupMocks(mockWorkloadManager)
			}

			handler := &Handler{
				ctx:              context.Background(),
				workloadManager:  mockWorkloadManager,
				registryProvider: mockRegistry,
			}

			request := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Name: "get_server_logs",
					Arguments: map[string]interface{}{
						"name": tt.serverName,
					},
				},
			}

			result, err := handler.GetServerLogs(context.Background(), request)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.checkResult != nil {
					tt.checkResult(t, result)
				}
			}
		})
	}
}

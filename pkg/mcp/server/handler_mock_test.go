// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"testing"

	v0 "github.com/modelcontextprotocol/registry/pkg/api/v0"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	runtime "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/workloads"
	workloadsmocks "github.com/stacklok/toolhive/pkg/workloads/mocks"
)

// testSource is a Source implementation that returns preconfigured servers.
type testSource struct {
	servers []*v0.ServerJSON
}

func (s *testSource) Load(_ context.Context) (*registry.LoadResult, error) {
	return &registry.LoadResult{Servers: s.servers}, nil
}

// newTestStore creates a Store backed by the given servers for testing.
func newTestStore(servers []*v0.ServerJSON) *registry.Store {
	store := registry.NewStore("test")
	store.AddLocalRegistry("test", &testSource{servers: servers})
	return store
}

// emptyStore returns a Store with no servers for tests that don't need registry data.
func emptyStore() *registry.Store {
	return registry.NewStore("test")
}

func TestHandler_SearchRegistry_WithMocks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		query       string
		servers     []*v0.ServerJSON
		wantErr     bool
		checkResult func(*testing.T, *mcp.CallToolResult)
	}{
		{
			name:  "successful search with results",
			query: "test",
			servers: []*v0.ServerJSON{
				{
					Name:        "test-server",
					Description: "Test server description",
				},
				{
					Name:        "another-test",
					Description: "Another test server",
				},
			},
			wantErr: false,
			checkResult: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				assert.NotNil(t, result)
				assert.False(t, result.IsError)
			},
		},
		{
			name:    "empty search results",
			query:   "nonexistent",
			servers: nil,
			wantErr: false,
			checkResult: func(t *testing.T, result *mcp.CallToolResult) {
				t.Helper()
				assert.NotNil(t, result)
				assert.False(t, result.IsError)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := &Handler{
				ctx:             context.Background(),
				workloadManager: workloadsmocks.NewMockManager(gomock.NewController(t)),
				registryStore:   newTestStore(tt.servers),
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
		setupMocks  func(*workloadsmocks.MockManager)
		wantErr     bool
		checkResult func(*testing.T, *mcp.CallToolResult)
	}{
		{
			name: "list multiple workloads",
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
			mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)

			if tt.setupMocks != nil {
				tt.setupMocks(mockWorkloadManager)
			}

			handler := &Handler{
				ctx:             context.Background(),
				workloadManager: mockWorkloadManager,
				registryStore:   emptyStore(),
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
			mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)

			if tt.setupMocks != nil {
				tt.setupMocks(mockWorkloadManager)
			}

			handler := &Handler{
				ctx:             context.Background(),
				workloadManager: mockWorkloadManager,
				registryStore:   emptyStore(),
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
			mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)

			if tt.setupMocks != nil {
				tt.setupMocks(mockWorkloadManager)
			}

			handler := &Handler{
				ctx:             context.Background(),
				workloadManager: mockWorkloadManager,
				registryStore:   emptyStore(),
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
		setupMocks  func(*workloadsmocks.MockManager)
		wantErr     bool
		checkResult func(*testing.T, *mcp.CallToolResult)
	}{
		{
			name:       "successful get logs",
			serverName: "test-server",
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
			mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)

			if tt.setupMocks != nil {
				tt.setupMocks(mockWorkloadManager)
			}

			handler := &Handler{
				ctx:             context.Background(),
				workloadManager: mockWorkloadManager,
				registryStore:   emptyStore(),
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

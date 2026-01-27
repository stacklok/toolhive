// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package aggregator

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/groups/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/workloads"
	discoverermocks "github.com/stacklok/toolhive/pkg/vmcp/workloads/mocks"
)

const testGroupName = "test-group"

func TestBackendDiscoverer_Discover(t *testing.T) {
	t.Parallel()

	t.Run("successful discovery with multiple backends", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		backend1 := &vmcp.Backend{
			ID:            "workload1",
			Name:          "workload1",
			BaseURL:       "http://localhost:8080/mcp",
			TransportType: "streamable-http",
			HealthStatus:  vmcp.BackendHealthy,
			Metadata: map[string]string{
				"workload_status": "running",
				"env":             "prod",
			},
		}
		backend2 := &vmcp.Backend{
			ID:            "workload2",
			Name:          "workload2",
			BaseURL:       "http://localhost:8081/mcp",
			TransportType: "sse",
			HealthStatus:  vmcp.BackendHealthy,
			Metadata: map[string]string{
				"workload_status": "running",
			},
		}

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloadDiscoverer.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]workloads.TypedWorkload{
				{
					Name: "workload1",
					Type: workloads.WorkloadTypeMCPServer,
				},
				{
					Name: "workload2",
					Type: workloads.WorkloadTypeMCPServer,
				},
			}, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(
			gomock.Any(),
			workloads.TypedWorkload{
				Name: "workload1",
				Type: workloads.WorkloadTypeMCPServer,
			},
		).Return(backend1, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(
			gomock.Any(),
			workloads.TypedWorkload{
				Name: "workload2",
				Type: workloads.WorkloadTypeMCPServer,
			},
		).Return(backend2, nil)

		discoverer := NewUnifiedBackendDiscoverer(mockWorkloadDiscoverer, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 2)
		assert.Equal(t, "workload1", backends[0].ID)
		assert.Equal(t, "http://localhost:8080/mcp", backends[0].BaseURL)
		assert.Equal(t, vmcp.BackendHealthy, backends[0].HealthStatus)
		assert.Equal(t, "prod", backends[0].Metadata["env"])
		assert.Equal(t, "workload2", backends[1].ID)
		assert.Equal(t, "sse", backends[1].TransportType)
	})

	t.Run("discovers workloads with different health statuses", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		healthyBackend := &vmcp.Backend{
			ID:            "healthy-workload",
			Name:          "healthy-workload",
			BaseURL:       "http://localhost:8080/mcp",
			TransportType: "streamable-http",
			HealthStatus:  vmcp.BackendHealthy,
			Metadata:      map[string]string{"workload_status": "running"},
		}
		unhealthyBackend := &vmcp.Backend{
			ID:            "unhealthy-workload",
			Name:          "unhealthy-workload",
			BaseURL:       "http://localhost:8081/mcp",
			TransportType: "sse",
			HealthStatus:  vmcp.BackendUnhealthy,
			Metadata:      map[string]string{"workload_status": "stopped"},
		}

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloadDiscoverer.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]workloads.TypedWorkload{
				{
					Name: "healthy-workload",
					Type: workloads.WorkloadTypeMCPServer,
				},
				{
					Name: "unhealthy-workload",
					Type: workloads.WorkloadTypeMCPServer,
				},
			}, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), workloads.TypedWorkload{Name: "healthy-workload", Type: workloads.WorkloadTypeMCPServer}).Return(healthyBackend, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), workloads.TypedWorkload{Name: "unhealthy-workload", Type: workloads.WorkloadTypeMCPServer}).Return(unhealthyBackend, nil)

		discoverer := NewUnifiedBackendDiscoverer(mockWorkloadDiscoverer, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 2)
		assert.Equal(t, "healthy-workload", backends[0].ID)
		assert.Equal(t, vmcp.BackendHealthy, backends[0].HealthStatus)
		assert.Equal(t, "unhealthy-workload", backends[1].ID)
		assert.Equal(t, vmcp.BackendUnhealthy, backends[1].HealthStatus)
		assert.Equal(t, "stopped", backends[1].Metadata["workload_status"])
	})

	t.Run("filters out workloads without URL", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		backendWithURL := &vmcp.Backend{
			ID:            "workload1",
			Name:          "workload1",
			BaseURL:       "http://localhost:8080/mcp",
			TransportType: "streamable-http",
			HealthStatus:  vmcp.BackendHealthy,
			Metadata:      map[string]string{},
		}

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloadDiscoverer.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]workloads.TypedWorkload{
				{
					Name: "workload1",
					Type: workloads.WorkloadTypeMCPServer,
				},
				{
					Name: "workload2",
					Type: workloads.WorkloadTypeMCPServer,
				},
			}, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), workloads.TypedWorkload{Name: "workload1", Type: workloads.WorkloadTypeMCPServer}).Return(backendWithURL, nil)
		// workload2 has no URL, so GetWorkload returns nil
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(
			gomock.Any(),
			workloads.TypedWorkload{
				Name: "workload2",
				Type: workloads.WorkloadTypeMCPServer,
			},
		).Return(nil, nil)

		discoverer := NewUnifiedBackendDiscoverer(mockWorkloadDiscoverer, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 1)
		assert.Equal(t, "workload1", backends[0].ID)
	})

	t.Run("returns empty list when all workloads lack URLs", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloadDiscoverer.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]workloads.TypedWorkload{
				{
					Name: "workload1",
					Type: workloads.WorkloadTypeMCPServer,
				},
				{
					Name: "workload2",
					Type: workloads.WorkloadTypeMCPServer,
				},
			}, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(
			gomock.Any(),
			workloads.TypedWorkload{
				Name: "workload1",
				Type: workloads.WorkloadTypeMCPServer,
			},
		).Return(nil, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(
			gomock.Any(),
			workloads.TypedWorkload{
				Name: "workload2",
				Type: workloads.WorkloadTypeMCPServer,
			},
		).Return(nil, nil)

		discoverer := NewUnifiedBackendDiscoverer(mockWorkloadDiscoverer, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		assert.Empty(t, backends)
	})

	t.Run("returns error when group does not exist", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		mockGroups.EXPECT().Exists(gomock.Any(), "nonexistent-group").Return(false, nil)

		discoverer := NewUnifiedBackendDiscoverer(mockWorkloadDiscoverer, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), "nonexistent-group")

		require.Error(t, err)
		assert.Nil(t, backends)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("returns error when group check fails", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(false, errors.New("database error"))

		discoverer := NewUnifiedBackendDiscoverer(mockWorkloadDiscoverer, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.Error(t, err)
		assert.Nil(t, backends)
		assert.Contains(t, err.Error(), "failed to check if group exists")
	})

	t.Run("returns empty list when group is empty", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		mockGroups.EXPECT().Exists(gomock.Any(), "empty-group").Return(true, nil)
		mockWorkloadDiscoverer.EXPECT().ListWorkloadsInGroup(
			gomock.Any(), "empty-group",
		).Return([]workloads.TypedWorkload{}, nil)

		discoverer := NewUnifiedBackendDiscoverer(mockWorkloadDiscoverer, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), "empty-group")

		require.NoError(t, err)
		assert.Empty(t, backends)
	})

	t.Run("gracefully handles workload get failures", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		goodBackend := &vmcp.Backend{
			ID:            "good-workload",
			Name:          "good-workload",
			BaseURL:       "http://localhost:8080/mcp",
			TransportType: "streamable-http",
			HealthStatus:  vmcp.BackendHealthy,
			Metadata:      map[string]string{},
		}

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloadDiscoverer.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]workloads.TypedWorkload{
				{
					Name: "good-workload",
					Type: workloads.WorkloadTypeMCPServer,
				},
				{
					Name: "failing-workload",
					Type: workloads.WorkloadTypeMCPServer,
				},
			}, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(
			gomock.Any(),
			workloads.TypedWorkload{
				Name: "good-workload",
				Type: workloads.WorkloadTypeMCPServer,
			},
		).Return(goodBackend, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(
			gomock.Any(),
			workloads.TypedWorkload{
				Name: "failing-workload",
				Type: workloads.WorkloadTypeMCPServer,
			},
		).Return(nil, errors.New("workload query failed"))

		discoverer := NewUnifiedBackendDiscoverer(mockWorkloadDiscoverer, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 1)
		assert.Equal(t, "good-workload", backends[0].ID)
	})

	t.Run("returns error when list workloads fails", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloadDiscoverer.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return(nil, errors.New("failed to list workloads"))

		discoverer := NewUnifiedBackendDiscoverer(mockWorkloadDiscoverer, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.Error(t, err)
		assert.Nil(t, backends)
		assert.Contains(t, err.Error(), "failed to list workloads in group")
	})

	t.Run("applies authentication configuration", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		backend := &vmcp.Backend{
			ID:            "workload1",
			Name:          "workload1",
			BaseURL:       "http://localhost:8080/mcp",
			TransportType: "streamable-http",
			HealthStatus:  vmcp.BackendHealthy,
			Metadata:      map[string]string{},
		}

		authConfig := &config.OutgoingAuthConfig{
			Backends: map[string]*authtypes.BackendAuthStrategy{
				"workload1": {
					Type: "header_injection",
					HeaderInjection: &authtypes.HeaderInjectionConfig{
						HeaderName:  "Authorization",
						HeaderValue: "test-token",
					},
				},
			},
		}

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloadDiscoverer.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]workloads.TypedWorkload{
				{
					Name: "workload1",
					Type: workloads.WorkloadTypeMCPServer,
				},
			}, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(
			gomock.Any(),
			workloads.TypedWorkload{
				Name: "workload1",
				Type: workloads.WorkloadTypeMCPServer,
			},
		).Return(backend, nil)

		discoverer := NewUnifiedBackendDiscoverer(mockWorkloadDiscoverer, mockGroups, authConfig)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 1)
		assert.Equal(t, "header_injection", backends[0].AuthConfig.Type)
		assert.Equal(t, "test-token", backends[0].AuthConfig.HeaderInjection.HeaderValue)
	})

	t.Run("successful discovery with MCPRemoteProxy backends", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		proxy1 := &vmcp.Backend{
			ID:            "proxy1",
			Name:          "proxy1",
			BaseURL:       "http://proxy1-service:8080",
			TransportType: "streamable-http",
			HealthStatus:  vmcp.BackendHealthy,
			Metadata: map[string]string{
				"tool_type":       "mcp",
				"workload_status": "Ready",
			},
		}
		proxy2 := &vmcp.Backend{
			ID:            "proxy2",
			Name:          "proxy2",
			BaseURL:       "http://proxy2-service:8080",
			TransportType: "sse",
			HealthStatus:  vmcp.BackendHealthy,
			Metadata: map[string]string{
				"tool_type":       "mcp",
				"workload_status": "Ready",
			},
		}

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloadDiscoverer.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]workloads.TypedWorkload{
				{
					Name: "proxy1",
					Type: workloads.WorkloadTypeMCPRemoteProxy,
				},
				{
					Name: "proxy2",
					Type: workloads.WorkloadTypeMCPRemoteProxy,
				},
			}, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(
			gomock.Any(),
			workloads.TypedWorkload{
				Name: "proxy1",
				Type: workloads.WorkloadTypeMCPRemoteProxy,
			},
		).Return(proxy1, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(
			gomock.Any(),
			workloads.TypedWorkload{
				Name: "proxy2",
				Type: workloads.WorkloadTypeMCPRemoteProxy,
			},
		).Return(proxy2, nil)

		discoverer := NewUnifiedBackendDiscoverer(mockWorkloadDiscoverer, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 2)
		assert.Equal(t, "proxy1", backends[0].ID)
		assert.Equal(t, "http://proxy1-service:8080", backends[0].BaseURL)
		assert.Equal(t, vmcp.BackendHealthy, backends[0].HealthStatus)
		assert.Equal(t, "proxy2", backends[1].ID)
		assert.Equal(t, "sse", backends[1].TransportType)
	})

	t.Run("successful discovery with mixed workload types", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		server := &vmcp.Backend{
			ID:            "server1",
			Name:          "server1",
			BaseURL:       "http://server1:8080/mcp",
			TransportType: "streamable-http",
			HealthStatus:  vmcp.BackendHealthy,
			Metadata: map[string]string{
				"tool_type":       "github",
				"workload_status": "Running",
			},
		}
		proxy := &vmcp.Backend{
			ID:            "proxy1",
			Name:          "proxy1",
			BaseURL:       "http://proxy1-service:8080",
			TransportType: "sse",
			HealthStatus:  vmcp.BackendHealthy,
			Metadata: map[string]string{
				"tool_type":       "mcp",
				"workload_status": "Ready",
			},
		}

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloadDiscoverer.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]workloads.TypedWorkload{
				{
					Name: "server1",
					Type: workloads.WorkloadTypeMCPServer,
				},
				{
					Name: "proxy1",
					Type: workloads.WorkloadTypeMCPRemoteProxy,
				},
			}, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(
			gomock.Any(),
			workloads.TypedWorkload{
				Name: "server1",
				Type: workloads.WorkloadTypeMCPServer,
			},
		).Return(server, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(
			gomock.Any(),
			workloads.TypedWorkload{
				Name: "proxy1",
				Type: workloads.WorkloadTypeMCPRemoteProxy,
			},
		).Return(proxy, nil)

		discoverer := NewUnifiedBackendDiscoverer(mockWorkloadDiscoverer, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 2)

		// Backends are sorted alphabetically by name
		// proxy1 comes before server1 alphabetically
		assert.Equal(t, "proxy1", backends[0].ID)
		assert.Equal(t, "sse", backends[0].TransportType)
		assert.Equal(t, "mcp", backends[0].Metadata["tool_type"])

		assert.Equal(t, "server1", backends[1].ID)
		assert.Equal(t, "streamable-http", backends[1].TransportType)
		assert.Equal(t, "github", backends[1].Metadata["tool_type"])
	})

	t.Run("applies authentication to MCPRemoteProxy backends", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		proxy := &vmcp.Backend{
			ID:            "proxy1",
			Name:          "proxy1",
			BaseURL:       "http://proxy1-service:8080",
			TransportType: "streamable-http",
			HealthStatus:  vmcp.BackendHealthy,
			Metadata:      map[string]string{},
		}

		authConfig := &config.OutgoingAuthConfig{
			Backends: map[string]*authtypes.BackendAuthStrategy{
				"proxy1": {
					Type: "token_exchange",
					TokenExchange: &authtypes.TokenExchangeConfig{
						TokenURL: "https://auth.example.com/token",
						ClientID: "test-client",
					},
				},
			},
		}

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloadDiscoverer.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]workloads.TypedWorkload{
				{
					Name: "proxy1",
					Type: workloads.WorkloadTypeMCPRemoteProxy,
				},
			}, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(
			gomock.Any(),
			workloads.TypedWorkload{
				Name: "proxy1",
				Type: workloads.WorkloadTypeMCPRemoteProxy,
			},
		).Return(proxy, nil)

		discoverer := NewUnifiedBackendDiscoverer(mockWorkloadDiscoverer, mockGroups, authConfig)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 1)
		assert.Equal(t, "token_exchange", backends[0].AuthConfig.Type)
		assert.Equal(t, "https://auth.example.com/token", backends[0].AuthConfig.TokenExchange.TokenURL)
	})

	t.Run("gracefully handles MCPRemoteProxy workload get failures", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		goodProxy := &vmcp.Backend{
			ID:            "good-proxy",
			Name:          "good-proxy",
			BaseURL:       "http://proxy-service:8080",
			TransportType: "streamable-http",
			HealthStatus:  vmcp.BackendHealthy,
			Metadata:      map[string]string{},
		}

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloadDiscoverer.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]workloads.TypedWorkload{
				{
					Name: "good-proxy",
					Type: workloads.WorkloadTypeMCPRemoteProxy,
				},
				{
					Name: "failing-proxy",
					Type: workloads.WorkloadTypeMCPRemoteProxy,
				},
			}, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(
			gomock.Any(),
			workloads.TypedWorkload{
				Name: "good-proxy",
				Type: workloads.WorkloadTypeMCPRemoteProxy,
			},
		).Return(goodProxy, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(
			gomock.Any(),
			workloads.TypedWorkload{
				Name: "failing-proxy",
				Type: workloads.WorkloadTypeMCPRemoteProxy,
			},
		).Return(nil, errors.New("proxy query failed"))

		discoverer := NewUnifiedBackendDiscoverer(mockWorkloadDiscoverer, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 1)
		assert.Equal(t, "good-proxy", backends[0].ID)
	})
}

// TestCLIWorkloadDiscoverer tests the CLI workload discoverer implementation
// to ensure it correctly converts CLI workloads to backends.
func TestCLIWorkloadDiscoverer(t *testing.T) {
	t.Parallel()

	t.Run("converts CLI workload to backend correctly", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockManager := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		backend := &vmcp.Backend{
			ID:           "workload1",
			Name:         "workload1",
			BaseURL:      "http://localhost:8080/mcp",
			HealthStatus: vmcp.BackendHealthy,
			Metadata:     map[string]string{"env": "prod"},
		}

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockManager.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]workloads.TypedWorkload{
				{
					Name: "workload1",
					Type: workloads.WorkloadTypeMCPServer,
				},
			}, nil)
		mockManager.EXPECT().GetWorkloadAsVMCPBackend(
			gomock.Any(),
			workloads.TypedWorkload{
				Name: "workload1",
				Type: workloads.WorkloadTypeMCPServer,
			},
		).Return(backend, nil)

		discoverer := NewUnifiedBackendDiscoverer(mockManager, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 1)
		assert.Equal(t, "workload1", backends[0].ID)
		assert.Equal(t, "http://localhost:8080/mcp", backends[0].BaseURL)
		assert.Equal(t, vmcp.BackendHealthy, backends[0].HealthStatus)
		assert.Equal(t, "prod", backends[0].Metadata["env"])
	})

	t.Run("maps CLI workload statuses to health correctly", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		runningBackend := &vmcp.Backend{
			ID:           "running-workload",
			Name:         "running-workload",
			BaseURL:      "http://localhost:8080/mcp",
			HealthStatus: vmcp.BackendHealthy,
		}
		stoppedBackend := &vmcp.Backend{
			ID:           "stopped-workload",
			Name:         "stopped-workload",
			BaseURL:      "http://localhost:8081/mcp",
			HealthStatus: vmcp.BackendUnhealthy,
		}

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockDiscoverer.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]workloads.TypedWorkload{
				{
					Name: "running-workload",
					Type: workloads.WorkloadTypeMCPServer,
				},
				{
					Name: "stopped-workload",
					Type: workloads.WorkloadTypeMCPServer,
				},
			}, nil)
		// The discoverer iterates through all workloads in order
		mockDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(
			gomock.Any(),
			workloads.TypedWorkload{
				Name: "running-workload",
				Type: workloads.WorkloadTypeMCPServer,
			},
		).Return(runningBackend, nil)
		mockDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(
			gomock.Any(),
			workloads.TypedWorkload{
				Name: "stopped-workload",
				Type: workloads.WorkloadTypeMCPServer,
			},
		).Return(stoppedBackend, nil)

		discoverer := NewUnifiedBackendDiscoverer(mockDiscoverer, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 2)
		// Sort backends by name to ensure consistent ordering for assertions
		if backends[0].Name > backends[1].Name {
			backends[0], backends[1] = backends[1], backends[0]
		}
		// Find the correct backend by name
		var running, stopped *vmcp.Backend
		for i := range backends {
			if backends[i].Name == "running-workload" {
				running = &backends[i]
			}
			if backends[i].Name == "stopped-workload" {
				stopped = &backends[i]
			}
		}
		require.NotNil(t, running, "running-workload should be found")
		require.NotNil(t, stopped, "stopped-workload should be found")
		assert.Equal(t, vmcp.BackendHealthy, running.HealthStatus)
		assert.Equal(t, vmcp.BackendUnhealthy, stopped.HealthStatus)
	})
}

func TestBackendDiscoverer_applyAuthConfigToBackend(t *testing.T) {
	t.Parallel()

	t.Run("discovered mode with discovered auth", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		authConfig := &config.OutgoingAuthConfig{
			Source: "discovered",
			Backends: map[string]*authtypes.BackendAuthStrategy{
				"backend1": {
					Type: "header_injection",
					HeaderInjection: &authtypes.HeaderInjectionConfig{
						HeaderName:  "Authorization",
						HeaderValue: "config-token",
					},
				},
			},
		}

		discoverer := &backendDiscoverer{
			workloadsManager: mockWorkloadDiscoverer,
			groupsManager:    mockGroups,
			authConfig:       authConfig,
		}

		backend := &vmcp.Backend{
			ID:   "backend1",
			Name: "backend1",
			AuthConfig: &authtypes.BackendAuthStrategy{
				Type: "token_exchange",
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "https://auth.example.com/token",
				},
			},
		}

		discoverer.applyAuthConfigToBackend(backend, "backend1")

		// In discovered mode, discovered auth should be preserved
		assert.Equal(t, "token_exchange", backend.AuthConfig.Type)
		assert.Equal(t, "https://auth.example.com/token", backend.AuthConfig.TokenExchange.TokenURL)
	})

	t.Run("discovered mode without discovered auth falls back to config", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		authConfig := &config.OutgoingAuthConfig{
			Source: "discovered",
			Backends: map[string]*authtypes.BackendAuthStrategy{
				"backend1": {
					Type: "header_injection",
					HeaderInjection: &authtypes.HeaderInjectionConfig{
						HeaderName:  "Authorization",
						HeaderValue: "config-token",
					},
				},
			},
		}

		discoverer := &backendDiscoverer{
			workloadsManager: mockWorkloadDiscoverer,
			groupsManager:    mockGroups,
			authConfig:       authConfig,
		}

		backend := &vmcp.Backend{
			ID:   "backend1",
			Name: "backend1",
			// No AuthStrategy set - no discovered auth
		}

		discoverer.applyAuthConfigToBackend(backend, "backend1")

		// Should fall back to config-based auth
		assert.Equal(t, "header_injection", backend.AuthConfig.Type)
		assert.Equal(t, "config-token", backend.AuthConfig.HeaderInjection.HeaderValue)
	})

	t.Run("inline mode ignores discovered auth", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		authConfig := &config.OutgoingAuthConfig{
			Source: "inline",
			Backends: map[string]*authtypes.BackendAuthStrategy{
				"backend1": {
					Type: "header_injection",
					HeaderInjection: &authtypes.HeaderInjectionConfig{
						HeaderName:  "Authorization",
						HeaderValue: "inline-token",
					},
				},
			},
		}

		discoverer := &backendDiscoverer{
			workloadsManager: mockWorkloadDiscoverer,
			groupsManager:    mockGroups,
			authConfig:       authConfig,
		}

		backend := &vmcp.Backend{
			ID:   "backend1",
			Name: "backend1",
			AuthConfig: &authtypes.BackendAuthStrategy{
				Type: "token_exchange",
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "https://auth.example.com/token",
				},
			},
		}

		discoverer.applyAuthConfigToBackend(backend, "backend1")

		// In inline mode, config-based auth should replace discovered auth
		assert.Equal(t, "header_injection", backend.AuthConfig.Type)
		assert.Equal(t, "inline-token", backend.AuthConfig.HeaderInjection.HeaderValue)
	})

	t.Run("empty source mode ignores discovered auth", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		authConfig := &config.OutgoingAuthConfig{
			Source: "", // Empty source
			Backends: map[string]*authtypes.BackendAuthStrategy{
				"backend1": {
					Type: "header_injection",
					HeaderInjection: &authtypes.HeaderInjectionConfig{
						HeaderName:  "Authorization",
						HeaderValue: "config-token",
					},
				},
			},
		}

		discoverer := &backendDiscoverer{
			workloadsManager: mockWorkloadDiscoverer,
			groupsManager:    mockGroups,
			authConfig:       authConfig,
		}

		backend := &vmcp.Backend{
			ID:   "backend1",
			Name: "backend1",
			AuthConfig: &authtypes.BackendAuthStrategy{
				Type: "token_exchange",
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "https://auth.example.com/token",
				},
			},
		}

		discoverer.applyAuthConfigToBackend(backend, "backend1")

		// Empty source should behave like inline mode
		assert.Equal(t, "header_injection", backend.AuthConfig.Type)
		assert.Equal(t, "config-token", backend.AuthConfig.HeaderInjection.HeaderValue)
	})

	t.Run("unknown source mode defaults to config-based auth", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		authConfig := &config.OutgoingAuthConfig{
			Source: "unknown-mode",
			Backends: map[string]*authtypes.BackendAuthStrategy{
				"backend1": {
					Type: "header_injection",
					HeaderInjection: &authtypes.HeaderInjectionConfig{
						HeaderName:  "Authorization",
						HeaderValue: "fallback-token",
					},
				},
			},
		}

		discoverer := &backendDiscoverer{
			workloadsManager: mockWorkloadDiscoverer,
			groupsManager:    mockGroups,
			authConfig:       authConfig,
		}

		backend := &vmcp.Backend{
			ID:   "backend1",
			Name: "backend1",
			AuthConfig: &authtypes.BackendAuthStrategy{
				Type: "token_exchange",
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "https://auth.example.com/token",
				},
			},
		}

		discoverer.applyAuthConfigToBackend(backend, "backend1")

		// Unknown source should fall back to config-based auth for safety
		assert.Equal(t, "header_injection", backend.AuthConfig.Type)
		assert.Equal(t, "fallback-token", backend.AuthConfig.HeaderInjection.HeaderValue)
	})

	t.Run("nil auth config does nothing", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		discoverer := &backendDiscoverer{
			workloadsManager: mockWorkloadDiscoverer,
			groupsManager:    mockGroups,
			authConfig:       nil, // No auth config
		}

		backend := &vmcp.Backend{
			ID:   "backend1",
			Name: "backend1",
			AuthConfig: &authtypes.BackendAuthStrategy{
				Type: "token_exchange",
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "https://auth.example.com/token",
				},
			},
		}

		discoverer.applyAuthConfigToBackend(backend, "backend1")

		// With nil auth config, backend should remain unchanged
		assert.Equal(t, "token_exchange", backend.AuthConfig.Type)
		assert.Equal(t, "https://auth.example.com/token", backend.AuthConfig.TokenExchange.TokenURL)
	})

	t.Run("no config for backend in inline mode leaves backend unchanged", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		authConfig := &config.OutgoingAuthConfig{
			Source: "inline",
			Backends: map[string]*authtypes.BackendAuthStrategy{
				"other-backend": {
					Type: "header_injection",
					HeaderInjection: &authtypes.HeaderInjectionConfig{
						HeaderName:  "Authorization",
						HeaderValue: "other-token",
					},
				},
			},
		}

		discoverer := &backendDiscoverer{
			workloadsManager: mockWorkloadDiscoverer,
			groupsManager:    mockGroups,
			authConfig:       authConfig,
		}

		backend := &vmcp.Backend{
			ID:   "backend1",
			Name: "backend1",
			AuthConfig: &authtypes.BackendAuthStrategy{
				Type: "token_exchange",
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "https://auth.example.com/token",
				},
			},
		}

		discoverer.applyAuthConfigToBackend(backend, "backend1")

		// In inline mode with no config for this backend, discovered auth is cleared
		// but no new auth is applied (ResolveForBackend returns empty)
		assert.Equal(t, "token_exchange", backend.AuthConfig.Type)
		assert.Equal(t, "https://auth.example.com/token", backend.AuthConfig.TokenExchange.TokenURL)
	})

	t.Run("discovered mode with header injection auth", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		authConfig := &config.OutgoingAuthConfig{
			Source:   "discovered",
			Backends: map[string]*authtypes.BackendAuthStrategy{},
		}

		discoverer := &backendDiscoverer{
			workloadsManager: mockWorkloadDiscoverer,
			groupsManager:    mockGroups,
			authConfig:       authConfig,
		}

		backend := &vmcp.Backend{
			ID:   "backend1",
			Name: "backend1",
			AuthConfig: &authtypes.BackendAuthStrategy{
				Type: "header_injection",
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "X-API-Key",
					HeaderValue: "secret-key-123",
				},
			},
		}

		discoverer.applyAuthConfigToBackend(backend, "backend1")

		// In discovered mode, header injection auth should be preserved
		assert.Equal(t, "header_injection", backend.AuthConfig.Type)
		assert.Equal(t, "X-API-Key", backend.AuthConfig.HeaderInjection.HeaderName)
		assert.Equal(t, "secret-key-123", backend.AuthConfig.HeaderInjection.HeaderValue)
	})

	t.Run("discovered mode falls back to default config when no auth discovered", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		authConfig := &config.OutgoingAuthConfig{
			Source: "discovered",
			Default: &authtypes.BackendAuthStrategy{
				Type: "header_injection",
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "Authorization",
					HeaderValue: "default-fallback-token",
				},
			},
		}

		discoverer := &backendDiscoverer{
			workloadsManager: mockWorkloadDiscoverer,
			groupsManager:    mockGroups,
			authConfig:       authConfig,
		}

		backend := &vmcp.Backend{
			ID:   "backend1",
			Name: "backend1",
			// No discovered auth (AuthStrategy is empty)
		}

		discoverer.applyAuthConfigToBackend(backend, "backend1")

		// In discovered mode with no discovered auth, should fall back to default config
		assert.Equal(t, "header_injection", backend.AuthConfig.Type)
		assert.Equal(t, "default-fallback-token", backend.AuthConfig.HeaderInjection.HeaderValue)
	})
}

// TestStaticBackendDiscoverer_EmptyBackendList verifies that when a static discoverer
// is created with an empty backend list, it gracefully returns an empty list instead of
// panicking due to nil groupsManager (regression test for nil pointer dereference).
func TestStaticBackendDiscoverer_EmptyBackendList(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Create a static discoverer with empty backend list (not nil, but zero length)
	// This simulates the edge case where staticBackends was set but is empty
	discoverer := NewUnifiedBackendDiscovererWithStaticBackends(
		[]config.StaticBackendConfig{}, // Empty slice, not nil
		nil,                            // No auth config
		"test-group",
	)

	// This should return empty list without panicking
	// Previously would panic when falling through to dynamic mode with nil groupsManager
	backends, err := discoverer.Discover(ctx, "test-group")

	require.NoError(t, err)
	assert.Empty(t, backends)
}

// TestStaticBackendDiscoverer_MetadataGroupOverride verifies that the "group" metadata key
// is always overridden with the groupRef value, even if user provides a different value.
func TestStaticBackendDiscoverer_MetadataGroupOverride(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		staticBackends    []config.StaticBackendConfig
		groupRef          string
		expectedGroupVals []string
	}{
		{
			name: "user-provided group metadata is overridden",
			staticBackends: []config.StaticBackendConfig{
				{
					Name:      "backend1",
					URL:       "http://backend1:8080",
					Transport: "sse",
					Metadata: map[string]string{
						"group": "wrong-group", // User provided conflicting value
						"env":   "prod",
					},
				},
			},
			groupRef:          "correct-group",
			expectedGroupVals: []string{"correct-group"},
		},
		{
			name: "group metadata added when not present",
			staticBackends: []config.StaticBackendConfig{
				{
					Name:      "backend2",
					URL:       "http://backend2:8080",
					Transport: "streamable-http",
					Metadata: map[string]string{
						"env": "dev",
					},
				},
			},
			groupRef:          "test-group",
			expectedGroupVals: []string{"test-group"},
		},
		{
			name: "group metadata added when metadata is nil",
			staticBackends: []config.StaticBackendConfig{
				{
					Name:      "backend3",
					URL:       "http://backend3:8080",
					Transport: "sse",
					Metadata:  nil, // No metadata at all
				},
			},
			groupRef:          "my-group",
			expectedGroupVals: []string{"my-group"},
		},
		{
			name: "multiple backends all get correct group",
			staticBackends: []config.StaticBackendConfig{
				{
					Name:      "backend1",
					URL:       "http://backend1:8080",
					Transport: "sse",
					Metadata:  map[string]string{"group": "wrong1"},
				},
				{
					Name:      "backend2",
					URL:       "http://backend2:8080",
					Transport: "streamable-http",
					Metadata:  map[string]string{"env": "prod"},
				},
			},
			groupRef:          "shared-group",
			expectedGroupVals: []string{"shared-group", "shared-group"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()

			discoverer := NewUnifiedBackendDiscovererWithStaticBackends(
				tt.staticBackends,
				nil, // No auth config needed for this test
				tt.groupRef,
			)

			backends, err := discoverer.Discover(ctx, tt.groupRef)
			require.NoError(t, err)

			// Verify we got the expected number of backends
			assert.Len(t, backends, len(tt.expectedGroupVals))

			// Verify each backend has the correct group metadata
			for i, backend := range backends {
				assert.NotNil(t, backend.Metadata, "Backend %d should have metadata", i)
				assert.Equal(t, tt.expectedGroupVals[i], backend.Metadata["group"],
					"Backend %d should have correct group metadata", i)

				// Verify other metadata is preserved
				if tt.staticBackends[i].Metadata != nil {
					for k, v := range tt.staticBackends[i].Metadata {
						if k != "group" {
							assert.Equal(t, v, backend.Metadata[k],
								"Backend %d should preserve non-group metadata key %s", i, k)
						}
					}
				}
			}
		})
	}
}

// TestBackendDiscoverer_Discover_DeterministicOrdering tests that Discover returns backends
// in a deterministic order (sorted alphabetically by name) regardless of input order.
// This prevents non-deterministic ConfigMap content that would cause unnecessary
// deployment rollouts (pod cycling). See: https://github.com/stacklok/toolhive/issues/3448
func TestBackendDiscoverer_Discover_DeterministicOrdering(t *testing.T) {
	t.Parallel()

	// Test with multiple different input orders to ensure output is always sorted
	testCases := []struct {
		name           string
		staticBackends []config.StaticBackendConfig
	}{
		{
			name: "reverse alphabetical order (zebra, middle, alpha)",
			staticBackends: []config.StaticBackendConfig{
				{Name: "zebra-backend", URL: "http://zebra:8080", Transport: "sse"},
				{Name: "middle-backend", URL: "http://middle:8080", Transport: "streamable-http"},
				{Name: "alpha-backend", URL: "http://alpha:8080", Transport: "sse"},
			},
		},
		{
			name: "alphabetical order (alpha, middle, zebra)",
			staticBackends: []config.StaticBackendConfig{
				{Name: "alpha-backend", URL: "http://alpha:8080", Transport: "sse"},
				{Name: "middle-backend", URL: "http://middle:8080", Transport: "streamable-http"},
				{Name: "zebra-backend", URL: "http://zebra:8080", Transport: "sse"},
			},
		},
		{
			name: "random order (middle, zebra, alpha)",
			staticBackends: []config.StaticBackendConfig{
				{Name: "middle-backend", URL: "http://middle:8080", Transport: "streamable-http"},
				{Name: "zebra-backend", URL: "http://zebra:8080", Transport: "sse"},
				{Name: "alpha-backend", URL: "http://alpha:8080", Transport: "sse"},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()

			discoverer := NewUnifiedBackendDiscovererWithStaticBackends(
				tc.staticBackends,
				nil, // No auth config needed for this test
				"test-group",
			)

			backends, err := discoverer.Discover(ctx, "test-group")
			require.NoError(t, err)

			// Output should ALWAYS be alphabetically sorted regardless of input order
			require.Len(t, backends, 3, "should include all valid backends")
			assert.Equal(t, "alpha-backend", backends[0].Name,
				"first backend should be alpha-backend (alphabetically first)")
			assert.Equal(t, "middle-backend", backends[1].Name,
				"second backend should be middle-backend (alphabetically second)")
			assert.Equal(t, "zebra-backend", backends[2].Name,
				"third backend should be zebra-backend (alphabetically third)")
		})
	}
}

// TestBackendDiscoverer_Discover_DeterministicOrdering_DynamicMode tests that Discover
// returns backends in deterministic order when using dynamic mode (K8s API discovery).
func TestBackendDiscoverer_Discover_DeterministicOrdering_DynamicMode(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
	mockGroups := mocks.NewMockManager(ctrl)

	// Create backends in non-alphabetical order to test sorting
	backend1 := &vmcp.Backend{
		ID:            "zebra-backend",
		Name:          "zebra-backend",
		BaseURL:       "http://zebra:8080/mcp",
		TransportType: "sse",
		HealthStatus:  vmcp.BackendHealthy,
	}
	backend2 := &vmcp.Backend{
		ID:            "alpha-backend",
		Name:          "alpha-backend",
		BaseURL:       "http://alpha:8080/mcp",
		TransportType: "streamable-http",
		HealthStatus:  vmcp.BackendHealthy,
	}
	backend3 := &vmcp.Backend{
		ID:            "middle-backend",
		Name:          "middle-backend",
		BaseURL:       "http://middle:8080/mcp",
		TransportType: "sse",
		HealthStatus:  vmcp.BackendHealthy,
	}

	mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
	// Return workloads in non-alphabetical order (zebra, alpha, middle)
	mockWorkloadDiscoverer.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
		Return([]workloads.TypedWorkload{
			{Name: "zebra-backend", Type: workloads.WorkloadTypeMCPServer},
			{Name: "alpha-backend", Type: workloads.WorkloadTypeMCPServer},
			{Name: "middle-backend", Type: workloads.WorkloadTypeMCPServer},
		}, nil)
	mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(
		gomock.Any(),
		workloads.TypedWorkload{Name: "zebra-backend", Type: workloads.WorkloadTypeMCPServer},
	).Return(backend1, nil)
	mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(
		gomock.Any(),
		workloads.TypedWorkload{Name: "alpha-backend", Type: workloads.WorkloadTypeMCPServer},
	).Return(backend2, nil)
	mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(
		gomock.Any(),
		workloads.TypedWorkload{Name: "middle-backend", Type: workloads.WorkloadTypeMCPServer},
	).Return(backend3, nil)

	discoverer := NewUnifiedBackendDiscoverer(mockWorkloadDiscoverer, mockGroups, nil)
	backends, err := discoverer.Discover(context.Background(), testGroupName)

	require.NoError(t, err)
	require.Len(t, backends, 3)

	// Backends should be sorted alphabetically by name
	assert.Equal(t, "alpha-backend", backends[0].Name,
		"first backend should be alpha-backend (alphabetically first)")
	assert.Equal(t, "middle-backend", backends[1].Name,
		"second backend should be middle-backend (alphabetically second)")
	assert.Equal(t, "zebra-backend", backends[2].Name,
		"third backend should be zebra-backend (alphabetically third)")
}

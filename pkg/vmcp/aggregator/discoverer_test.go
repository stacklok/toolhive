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
				"tool_type":       "github",
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
				"tool_type":       "jira",
				"workload_status": "running",
			},
		}

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloadDiscoverer.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]string{"workload1", "workload2"}, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), "workload1").Return(backend1, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), "workload2").Return(backend2, nil)

		discoverer := NewUnifiedBackendDiscoverer(mockWorkloadDiscoverer, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 2)
		assert.Equal(t, "workload1", backends[0].ID)
		assert.Equal(t, "http://localhost:8080/mcp", backends[0].BaseURL)
		assert.Equal(t, vmcp.BackendHealthy, backends[0].HealthStatus)
		assert.Equal(t, "github", backends[0].Metadata["tool_type"])
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
			Return([]string{"healthy-workload", "unhealthy-workload"}, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), "healthy-workload").Return(healthyBackend, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), "unhealthy-workload").Return(unhealthyBackend, nil)

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
			Return([]string{"workload1", "workload2"}, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), "workload1").Return(backendWithURL, nil)
		// workload2 has no URL, so GetWorkload returns nil
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), "workload2").Return(nil, nil)

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
			Return([]string{"workload1", "workload2"}, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), "workload1").Return(nil, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), "workload2").Return(nil, nil)

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
		mockWorkloadDiscoverer.EXPECT().ListWorkloadsInGroup(gomock.Any(), "empty-group").Return([]string{}, nil)

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
			Return([]string{"good-workload", "failing-workload"}, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), "good-workload").Return(goodBackend, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), "failing-workload").
			Return(nil, errors.New("workload query failed"))

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
					Type: authtypes.StrategyTypeHeaderInjection,
					HeaderInjection: &authtypes.HeaderInjectionConfig{
						HeaderName:  "X-API-Key",
						HeaderValue: "test-token",
					},
				},
			},
		}

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockWorkloadDiscoverer.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]string{"workload1"}, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), "workload1").Return(backend, nil)

		discoverer := NewUnifiedBackendDiscoverer(mockWorkloadDiscoverer, mockGroups, authConfig)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 1)
		require.NotNil(t, backends[0].AuthConfig)
		assert.Equal(t, authtypes.StrategyTypeHeaderInjection, backends[0].AuthConfig.Type)
		require.NotNil(t, backends[0].AuthConfig.HeaderInjection)
		assert.Equal(t, "test-token", backends[0].AuthConfig.HeaderInjection.HeaderValue)
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
			Metadata:     map[string]string{"tool_type": "github", "env": "prod"},
		}

		mockGroups.EXPECT().Exists(gomock.Any(), testGroupName).Return(true, nil)
		mockManager.EXPECT().ListWorkloadsInGroup(gomock.Any(), testGroupName).
			Return([]string{"workload1"}, nil)
		mockManager.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), "workload1").Return(backend, nil)

		discoverer := NewUnifiedBackendDiscoverer(mockManager, mockGroups, nil)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 1)
		assert.Equal(t, "workload1", backends[0].ID)
		assert.Equal(t, "http://localhost:8080/mcp", backends[0].BaseURL)
		assert.Equal(t, vmcp.BackendHealthy, backends[0].HealthStatus)
		assert.Equal(t, "github", backends[0].Metadata["tool_type"])
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
			Return([]string{"running-workload", "stopped-workload"}, nil)
		// The discoverer iterates through all workloads in order
		mockDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), "running-workload").Return(runningBackend, nil)
		mockDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), "stopped-workload").Return(stoppedBackend, nil)

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
					Type: authtypes.StrategyTypeHeaderInjection,
					HeaderInjection: &authtypes.HeaderInjectionConfig{
						HeaderName:  "X-API-Key",
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
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "https://auth.example.com/token",
				},
			},
		}

		discoverer.applyAuthConfigToBackend(backend, "backend1")

		// In discovered mode, discovered auth should be preserved
		require.NotNil(t, backend.AuthConfig)
		assert.Equal(t, authtypes.StrategyTypeTokenExchange, backend.AuthConfig.Type)
		require.NotNil(t, backend.AuthConfig.TokenExchange)
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
					Type: authtypes.StrategyTypeHeaderInjection,
					HeaderInjection: &authtypes.HeaderInjectionConfig{
						HeaderName:  "X-API-Key",
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
			// No AuthConfig set - no discovered auth
		}

		discoverer.applyAuthConfigToBackend(backend, "backend1")

		// Should fall back to config-based auth
		require.NotNil(t, backend.AuthConfig)
		assert.Equal(t, authtypes.StrategyTypeHeaderInjection, backend.AuthConfig.Type)
		require.NotNil(t, backend.AuthConfig.HeaderInjection)
		assert.Equal(t, "config-token", backend.AuthConfig.HeaderInjection.HeaderValue)
	})

	t.Run("mixed mode with explicit config override", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		authConfig := &config.OutgoingAuthConfig{
			Source: "mixed",
			Backends: map[string]*authtypes.BackendAuthStrategy{
				"backend1": {
					Type: authtypes.StrategyTypeHeaderInjection,
					HeaderInjection: &authtypes.HeaderInjectionConfig{
						HeaderName:  "X-API-Key",
						HeaderValue: "override-token",
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
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "https://auth.example.com/token",
				},
			},
		}

		discoverer.applyAuthConfigToBackend(backend, "backend1")

		// In mixed mode with explicit config, config should override discovered auth
		require.NotNil(t, backend.AuthConfig)
		assert.Equal(t, authtypes.StrategyTypeHeaderInjection, backend.AuthConfig.Type)
		require.NotNil(t, backend.AuthConfig.HeaderInjection)
		assert.Equal(t, "override-token", backend.AuthConfig.HeaderInjection.HeaderValue)
	})

	t.Run("mixed mode without explicit config uses discovered auth", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		authConfig := &config.OutgoingAuthConfig{
			Source: "mixed",
			Backends: map[string]*authtypes.BackendAuthStrategy{
				"other-backend": {
					Type: authtypes.StrategyTypeHeaderInjection,
					HeaderInjection: &authtypes.HeaderInjectionConfig{
						HeaderName:  "X-API-Key",
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
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "https://auth.example.com/token",
				},
			},
		}

		discoverer.applyAuthConfigToBackend(backend, "backend1")

		// In mixed mode without explicit config, discovered auth should be preserved
		require.NotNil(t, backend.AuthConfig)
		assert.Equal(t, authtypes.StrategyTypeTokenExchange, backend.AuthConfig.Type)
		require.NotNil(t, backend.AuthConfig.TokenExchange)
		assert.Equal(t, "https://auth.example.com/token", backend.AuthConfig.TokenExchange.TokenURL)
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
					Type: authtypes.StrategyTypeHeaderInjection,
					HeaderInjection: &authtypes.HeaderInjectionConfig{
						HeaderName:  "X-API-Key",
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
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "https://auth.example.com/token",
				},
			},
		}

		discoverer.applyAuthConfigToBackend(backend, "backend1")

		// In inline mode, config-based auth should replace discovered auth
		require.NotNil(t, backend.AuthConfig)
		assert.Equal(t, authtypes.StrategyTypeHeaderInjection, backend.AuthConfig.Type)
		require.NotNil(t, backend.AuthConfig.HeaderInjection)
		assert.Equal(t, "inline-token", backend.AuthConfig.HeaderInjection.HeaderValue)
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
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "https://auth.example.com/token",
				},
			},
		}

		discoverer.applyAuthConfigToBackend(backend, "backend1")

		// With nil auth config, backend should remain unchanged
		require.NotNil(t, backend.AuthConfig)
		assert.Equal(t, authtypes.StrategyTypeTokenExchange, backend.AuthConfig.Type)
		require.NotNil(t, backend.AuthConfig.TokenExchange)
		assert.Equal(t, "https://auth.example.com/token", backend.AuthConfig.TokenExchange.TokenURL)
	})

	t.Run("default auth config is applied when backend not in Backends map", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := discoverermocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		authConfig := &config.OutgoingAuthConfig{
			Source:   "inline",
			Backends: map[string]*authtypes.BackendAuthStrategy{},
			Default: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "X-Default-Key",
					HeaderValue: "default-token",
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
		}

		discoverer.applyAuthConfigToBackend(backend, "backend1")

		// Default auth should be applied
		require.NotNil(t, backend.AuthConfig)
		assert.Equal(t, authtypes.StrategyTypeHeaderInjection, backend.AuthConfig.Type)
		require.NotNil(t, backend.AuthConfig.HeaderInjection)
		assert.Equal(t, "default-token", backend.AuthConfig.HeaderInjection.HeaderValue)
	})
}

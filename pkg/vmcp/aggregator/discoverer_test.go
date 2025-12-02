package aggregator

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/adrg/xdg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/groups/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp"
	resolvermocks "github.com/stacklok/toolhive/pkg/vmcp/auth/resolver/mocks"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
	workloadsmocks "github.com/stacklok/toolhive/pkg/vmcp/workloads/mocks"
)

const testGroupName = "test-group"

func TestBackendDiscoverer_Discover(t *testing.T) {
	t.Parallel()

	t.Run("successful discovery with multiple backends", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := workloadsmocks.NewMockDiscoverer(ctrl)
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

		mockWorkloadDiscoverer := workloadsmocks.NewMockDiscoverer(ctrl)
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

		mockWorkloadDiscoverer := workloadsmocks.NewMockDiscoverer(ctrl)
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

		mockWorkloadDiscoverer := workloadsmocks.NewMockDiscoverer(ctrl)
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

		mockWorkloadDiscoverer := workloadsmocks.NewMockDiscoverer(ctrl)
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

		mockWorkloadDiscoverer := workloadsmocks.NewMockDiscoverer(ctrl)
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

		mockWorkloadDiscoverer := workloadsmocks.NewMockDiscoverer(ctrl)
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

		mockWorkloadDiscoverer := workloadsmocks.NewMockDiscoverer(ctrl)
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

		mockWorkloadDiscoverer := workloadsmocks.NewMockDiscoverer(ctrl)
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

		mockWorkloadDiscoverer := workloadsmocks.NewMockDiscoverer(ctrl)
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
			Return([]string{"workload1"}, nil)
		mockWorkloadDiscoverer.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), "workload1").Return(backend, nil)

		discoverer := NewUnifiedBackendDiscoverer(mockWorkloadDiscoverer, mockGroups, authConfig)
		backends, err := discoverer.Discover(context.Background(), testGroupName)

		require.NoError(t, err)
		require.Len(t, backends, 1)
		assert.Equal(t, "header_injection", backends[0].AuthConfig.Type)
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

		mockManager := workloadsmocks.NewMockDiscoverer(ctrl)
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

		mockDiscoverer := workloadsmocks.NewMockDiscoverer(ctrl)
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

		mockWorkloadDiscoverer := workloadsmocks.NewMockDiscoverer(ctrl)
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

		err := discoverer.applyAuthConfigToBackend(context.Background(), backend, "backend1")
		require.NoError(t, err)

		// In discovered mode, discovered auth should be preserved
		assert.Equal(t, "token_exchange", backend.AuthConfig.Type)
		assert.Equal(t, "https://auth.example.com/token", backend.AuthConfig.TokenExchange.TokenURL)
	})

	t.Run("discovered mode without discovered auth falls back to config", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := workloadsmocks.NewMockDiscoverer(ctrl)
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

		err := discoverer.applyAuthConfigToBackend(context.Background(), backend, "backend1")
		require.NoError(t, err)

		// Should fall back to config-based auth
		assert.Equal(t, "header_injection", backend.AuthConfig.Type)
		assert.Equal(t, "config-token", backend.AuthConfig.HeaderInjection.HeaderValue)
	})

	t.Run("inline mode ignores discovered auth", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := workloadsmocks.NewMockDiscoverer(ctrl)
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

		err := discoverer.applyAuthConfigToBackend(context.Background(), backend, "backend1")
		require.NoError(t, err)

		// In inline mode, config-based auth should replace discovered auth
		assert.Equal(t, "header_injection", backend.AuthConfig.Type)
		assert.Equal(t, "inline-token", backend.AuthConfig.HeaderInjection.HeaderValue)
	})

	t.Run("empty source mode ignores discovered auth", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := workloadsmocks.NewMockDiscoverer(ctrl)
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

		err := discoverer.applyAuthConfigToBackend(context.Background(), backend, "backend1")
		require.NoError(t, err)

		// Empty source should behave like inline mode
		assert.Equal(t, "header_injection", backend.AuthConfig.Type)
		assert.Equal(t, "config-token", backend.AuthConfig.HeaderInjection.HeaderValue)
	})

	t.Run("unknown source mode defaults to config-based auth", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := workloadsmocks.NewMockDiscoverer(ctrl)
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

		err := discoverer.applyAuthConfigToBackend(context.Background(), backend, "backend1")
		require.NoError(t, err)

		// Unknown source should fall back to config-based auth for safety
		assert.Equal(t, "header_injection", backend.AuthConfig.Type)
		assert.Equal(t, "fallback-token", backend.AuthConfig.HeaderInjection.HeaderValue)
	})

	t.Run("nil auth config does nothing", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := workloadsmocks.NewMockDiscoverer(ctrl)
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

		err := discoverer.applyAuthConfigToBackend(context.Background(), backend, "backend1")
		require.NoError(t, err)

		// With nil auth config, backend should remain unchanged
		assert.Equal(t, "token_exchange", backend.AuthConfig.Type)
		assert.Equal(t, "https://auth.example.com/token", backend.AuthConfig.TokenExchange.TokenURL)
	})

	t.Run("no config for backend in inline mode leaves backend unchanged", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := workloadsmocks.NewMockDiscoverer(ctrl)
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

		err := discoverer.applyAuthConfigToBackend(context.Background(), backend, "backend1")
		require.NoError(t, err)

		// In inline mode with no config for this backend, discovered auth is cleared
		// but no new auth is applied (ResolveForBackend returns empty)
		assert.Equal(t, "token_exchange", backend.AuthConfig.Type)
		assert.Equal(t, "https://auth.example.com/token", backend.AuthConfig.TokenExchange.TokenURL)
	})

	t.Run("discovered mode with header injection auth", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := workloadsmocks.NewMockDiscoverer(ctrl)
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

		err := discoverer.applyAuthConfigToBackend(context.Background(), backend, "backend1")
		require.NoError(t, err)

		// In discovered mode, header injection auth should be preserved
		assert.Equal(t, "header_injection", backend.AuthConfig.Type)
		assert.Equal(t, "X-API-Key", backend.AuthConfig.HeaderInjection.HeaderName)
		assert.Equal(t, "secret-key-123", backend.AuthConfig.HeaderInjection.HeaderValue)
	})

	t.Run("discovered mode falls back to default config when no auth discovered", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloadDiscoverer := workloadsmocks.NewMockDiscoverer(ctrl)
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

		err := discoverer.applyAuthConfigToBackend(context.Background(), backend, "backend1")
		require.NoError(t, err)

		// In discovered mode with no discovered auth, should fall back to default config
		assert.Equal(t, "header_injection", backend.AuthConfig.Type)
		assert.Equal(t, "default-fallback-token", backend.AuthConfig.HeaderInjection.HeaderValue)
	})
}

func TestBackendDiscoverer_resolveExternalAuthConfigRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		authConfig     *authtypes.BackendAuthStrategy
		setupResolver  func(*resolvermocks.MockAuthResolver)
		hasResolver    bool
		wantErr        bool
		errContains    string
		validateResult func(*testing.T, *authtypes.BackendAuthStrategy)
	}{
		{
			name:       "nil auth config returns nil",
			authConfig: nil,
			wantErr:    false,
			validateResult: func(t *testing.T, result *authtypes.BackendAuthStrategy) {
				t.Helper()
				assert.Nil(t, result)
			},
		},
		{
			name: "non-external_auth_config_ref passes through",
			authConfig: &authtypes.BackendAuthStrategy{
				Type: "header_injection",
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "Authorization",
					HeaderValue: "Bearer token",
				},
			},
			wantErr: false,
			validateResult: func(t *testing.T, result *authtypes.BackendAuthStrategy) {
				t.Helper()
				assert.Equal(t, "header_injection", result.Type)
			},
		},
		{
			name: "external_auth_config_ref without resolver returns error",
			authConfig: &authtypes.BackendAuthStrategy{
				Type:                      authtypes.StrategyTypeExternalAuthConfigRef,
				ExternalAuthConfigRefName: "my-config",
			},
			hasResolver: false,
			wantErr:     true,
			errContains: "auth resolver not initialized",
		},
		{
			name: "external_auth_config_ref resolution success",
			authConfig: &authtypes.BackendAuthStrategy{
				Type:                      authtypes.StrategyTypeExternalAuthConfigRef,
				ExternalAuthConfigRefName: "my-config",
			},
			hasResolver: true,
			setupResolver: func(m *resolvermocks.MockAuthResolver) {
				m.EXPECT().ResolveExternalAuthConfig(gomock.Any(), "my-config").
					Return(&authtypes.BackendAuthStrategy{
						Type: "token_exchange",
						TokenExchange: &authtypes.TokenExchangeConfig{
							TokenURL: "https://auth.example.com/token",
						},
					}, nil)
			},
			wantErr: false,
			validateResult: func(t *testing.T, result *authtypes.BackendAuthStrategy) {
				t.Helper()
				assert.Equal(t, "token_exchange", result.Type)
				assert.Equal(t, "https://auth.example.com/token", result.TokenExchange.TokenURL)
			},
		},
		{
			name: "external_auth_config_ref resolution failure returns error",
			authConfig: &authtypes.BackendAuthStrategy{
				Type:                      authtypes.StrategyTypeExternalAuthConfigRef,
				ExternalAuthConfigRefName: "missing-config",
			},
			hasResolver: true,
			setupResolver: func(m *resolvermocks.MockAuthResolver) {
				m.EXPECT().ResolveExternalAuthConfig(gomock.Any(), "missing-config").
					Return(nil, errors.New("not found"))
			},
			wantErr:     true,
			errContains: "failed to resolve external auth config",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			t.Cleanup(ctrl.Finish)

			discoverer := &backendDiscoverer{
				workloadsManager: workloadsmocks.NewMockDiscoverer(ctrl),
				groupsManager:    mocks.NewMockManager(ctrl),
			}

			if tt.hasResolver {
				mockResolver := resolvermocks.NewMockAuthResolver(ctrl)
				if tt.setupResolver != nil {
					tt.setupResolver(mockResolver)
				}
				discoverer.authResolver = mockResolver
			}

			result, err := discoverer.resolveExternalAuthConfigRef(context.Background(), tt.authConfig, "backend1")

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}

			require.NoError(t, err)
			if tt.validateResult != nil {
				tt.validateResult(t, result)
			}
		})
	}
}

func TestNewK8SBackendDiscovererWithClient(t *testing.T) {
	t.Parallel()

	t.Run("creates discoverer with workload discoverer and auth resolver", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		// Create a fake K8s client with proper scheme
		scheme := runtime.NewScheme()
		require.NoError(t, clientgoscheme.AddToScheme(scheme))
		require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

		mockGroups := mocks.NewMockManager(ctrl)
		authConfig := &config.OutgoingAuthConfig{
			Default: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "Authorization",
					HeaderValue: "Bearer test",
				},
			},
		}

		discoverer := NewK8SBackendDiscovererWithClient(
			fakeClient,
			"test-namespace",
			mockGroups,
			authConfig,
		)

		require.NotNil(t, discoverer)

		// Verify internal composition
		bd, ok := discoverer.(*backendDiscoverer)
		require.True(t, ok, "discoverer should be *backendDiscoverer")
		assert.NotNil(t, bd.workloadsManager, "workloadsManager should be set")
		assert.NotNil(t, bd.authResolver, "authResolver should be set")
		assert.Equal(t, mockGroups, bd.groupsManager, "groupsManager should match")
		assert.Equal(t, authConfig, bd.authConfig, "authConfig should match")
	})

	t.Run("creates discoverer without auth config", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		scheme := runtime.NewScheme()
		require.NoError(t, clientgoscheme.AddToScheme(scheme))
		require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

		mockGroups := mocks.NewMockManager(ctrl)

		discoverer := NewK8SBackendDiscovererWithClient(
			fakeClient,
			"test-namespace",
			mockGroups,
			nil, // No auth config
		)

		require.NotNil(t, discoverer)

		bd, ok := discoverer.(*backendDiscoverer)
		require.True(t, ok)
		assert.Nil(t, bd.authConfig)
		// Auth resolver should still be set even without auth config
		assert.NotNil(t, bd.authResolver)
	})
}

func TestWithAuthResolver(t *testing.T) {
	t.Parallel()

	t.Run("sets auth resolver on discoverer", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockResolver := resolvermocks.NewMockAuthResolver(ctrl)
		mockWorkloads := workloadsmocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		discoverer := NewUnifiedBackendDiscoverer(
			mockWorkloads,
			mockGroups,
			nil,
			WithAuthResolver(mockResolver),
		)

		bd, ok := discoverer.(*backendDiscoverer)
		require.True(t, ok)
		assert.Equal(t, mockResolver, bd.authResolver)
	})

	t.Run("without option leaves auth resolver nil", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockWorkloads := workloadsmocks.NewMockDiscoverer(ctrl)
		mockGroups := mocks.NewMockManager(ctrl)

		discoverer := NewUnifiedBackendDiscoverer(
			mockWorkloads,
			mockGroups,
			nil,
		)

		bd, ok := discoverer.(*backendDiscoverer)
		require.True(t, ok)
		assert.Nil(t, bd.authResolver)
	})
}

// mockEnvReader is a test implementation of env.Reader
type mockEnvReader struct {
	values map[string]string
}

func (m *mockEnvReader) Getenv(key string) string {
	if m.values == nil {
		return ""
	}
	return m.values[key]
}

func TestGetCLIAuthConfigDir(t *testing.T) {
	t.Parallel()

	t.Run("uses environment variable when set", func(t *testing.T) {
		t.Parallel()
		envReader := &mockEnvReader{values: map[string]string{
			CLIAuthConfigDirEnv: "/custom/auth/dir",
		}}

		dir := getCLIAuthConfigDir(envReader)
		assert.Equal(t, "/custom/auth/dir", dir)
	})

	t.Run("falls back to XDG default when env var not set", func(t *testing.T) {
		t.Parallel()
		envReader := &mockEnvReader{values: map[string]string{}}

		dir := getCLIAuthConfigDir(envReader)
		expected := filepath.Join(xdg.ConfigHome, "toolhive", "vmcp", "auth-configs")
		assert.Equal(t, expected, dir)
	})

	t.Run("trims whitespace from env var", func(t *testing.T) {
		t.Parallel()
		envReader := &mockEnvReader{values: map[string]string{
			CLIAuthConfigDirEnv: "  /custom/path  ",
		}}

		dir := getCLIAuthConfigDir(envReader)
		assert.Equal(t, "/custom/path", dir)
	})

	t.Run("whitespace-only env var uses default", func(t *testing.T) {
		t.Parallel()
		envReader := &mockEnvReader{values: map[string]string{
			CLIAuthConfigDirEnv: "   ",
		}}

		dir := getCLIAuthConfigDir(envReader)
		expected := filepath.Join(xdg.ConfigHome, "toolhive", "vmcp", "auth-configs")
		assert.Equal(t, expected, dir)
	})
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	discoveryMocks "github.com/stacklok/toolhive/pkg/vmcp/discovery/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
)

// StatusResponse mirrors the server's status response structure for test deserialization.
type StatusResponse struct {
	Backends []BackendStatus `json:"backends"`
	Healthy  bool            `json:"healthy"`
	Version  string          `json:"version"`
	GroupRef string          `json:"group_ref"`
}

// BackendStatus mirrors the server's backend status structure for test deserialization.
type BackendStatus struct {
	Name      string `json:"name"`
	Health    string `json:"health"`
	Transport string `json:"transport"`
	AuthType  string `json:"auth_type,omitempty"`
}

// createTestServerWithBackends creates a test server instance with custom backends
// and no health monitoring. It is a convenience wrapper around createTestServerWithHealthMonitor.
func createTestServerWithBackends(t *testing.T, backends []vmcp.Backend, groupRef string) *server.Server {
	t.Helper()
	return createTestServerWithHealthMonitor(t, backends,
		health.MonitorConfig{}, // zero value → no health monitor started
		nil,                    // no mock expectations needed
		groupRef,
	)
}

func TestStatusEndpoint_HTTPBehavior(t *testing.T) {
	t.Parallel()

	t.Run("POST returns 405", func(t *testing.T) {
		t.Parallel()
		srv := createTestServerWithBackends(t, []vmcp.Backend{}, "")

		resp, err := http.Post("http://"+srv.Address()+"/status", "application/json", nil)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
	})

	t.Run("GET returns 200 with correct Content-Type", func(t *testing.T) {
		t.Parallel()
		srv := createTestServerWithBackends(t, []vmcp.Backend{}, "")

		resp, err := http.Get("http://" + srv.Address() + "/status")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	})
}

func TestStatusEndpoint_HealthLogic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		backends        []vmcp.Backend
		expectedHealthy bool
	}{
		{"no backends", []vmcp.Backend{}, false},
		{"single healthy", []vmcp.Backend{{ID: "b1", Name: "h", HealthStatus: vmcp.BackendHealthy}}, true},
		{"single unhealthy", []vmcp.Backend{{ID: "b1", Name: "u", HealthStatus: vmcp.BackendUnhealthy}}, false},
		{"mixed health", []vmcp.Backend{
			{ID: "b1", Name: "h", HealthStatus: vmcp.BackendHealthy},
			{ID: "b2", Name: "u", HealthStatus: vmcp.BackendUnhealthy},
		}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := createTestServerWithBackends(t, tc.backends, "")

			resp, err := http.Get("http://" + srv.Address() + "/status")
			require.NoError(t, err)
			defer resp.Body.Close()

			var status StatusResponse
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&status))

			assert.Equal(t, tc.expectedHealthy, status.Healthy)
			assert.Len(t, status.Backends, len(tc.backends))
		})
	}
}

func TestStatusEndpoint_AuthTypeMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		authConfig *authtypes.BackendAuthStrategy
		expected   string
	}{
		{"nil config", nil, authtypes.StrategyTypeUnauthenticated},
		{"non-nil config", &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeTokenExchange}, authtypes.StrategyTypeTokenExchange},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			backends := []vmcp.Backend{{
				ID: "b1", Name: "test", HealthStatus: vmcp.BackendHealthy,
				AuthConfig: tc.authConfig,
			}}
			srv := createTestServerWithBackends(t, backends, "")

			resp, err := http.Get("http://" + srv.Address() + "/status")
			require.NoError(t, err)
			defer resp.Body.Close()

			var status StatusResponse
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&status))

			require.Len(t, status.Backends, 1)
			assert.Equal(t, tc.expected, status.Backends[0].AuthType)
		})
	}
}

func TestStatusEndpoint_GroupRef(t *testing.T) {
	t.Parallel()

	srv := createTestServerWithBackends(t, []vmcp.Backend{}, "namespace/my-group")

	resp, err := http.Get("http://" + srv.Address() + "/status")
	require.NoError(t, err)
	defer resp.Body.Close()

	var status StatusResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&status))
	assert.Equal(t, "namespace/my-group", status.GroupRef)
}

func TestStatusEndpoint_BackendFieldMapping(t *testing.T) {
	t.Parallel()

	backends := []vmcp.Backend{{
		ID: "backend-id", Name: "my-backend", BaseURL: "https://api.example.com:9090/mcp",
		TransportType: "streamable-http", HealthStatus: vmcp.BackendHealthy,
		AuthConfig: &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeTokenExchange},
	}}
	srv := createTestServerWithBackends(t, backends, "test-group")

	resp, err := http.Get("http://" + srv.Address() + "/status")
	require.NoError(t, err)
	defer resp.Body.Close()

	var status StatusResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&status))

	// Verify all response fields
	assert.NotEmpty(t, status.Version)
	assert.Equal(t, "test-group", status.GroupRef)
	assert.True(t, status.Healthy)

	// Verify backend field mapping
	require.Len(t, status.Backends, 1)
	b := status.Backends[0]
	assert.Equal(t, "my-backend", b.Name)
	assert.Equal(t, "healthy", b.Health)
	assert.Equal(t, "streamable-http", b.Transport)
	assert.Equal(t, authtypes.StrategyTypeTokenExchange, b.AuthType)
}

// createTestServerWithHealthMonitor creates a test server with health monitoring enabled.
// setupMock configures mock expectations on the backend client (e.g. ListCapabilities responses for health checks).
// groupRef is set in the server config (empty string is fine for tests that don't need it).
func createTestServerWithHealthMonitor(
	t *testing.T,
	backends []vmcp.Backend,
	monitorCfg health.MonitorConfig,
	setupMock func(mockClient *mocks.MockBackendClient),
	groupRef string,
) *server.Server {
	t.Helper()

	// ctrl.Finish must run last so that all mock calls have already stopped.
	// t.Cleanup is LIFO, so register it first — it will execute third.
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
	rt := router.NewDefaultRouter()

	if setupMock != nil {
		setupMock(mockBackendClient)
	}

	port := networking.FindAvailable()
	require.NotZero(t, port, "Failed to find available port")

	mockDiscoveryMgr.EXPECT().
		Discover(gomock.Any(), gomock.Any()).
		Return(&aggregator.AggregatedCapabilities{
			Tools:     []vmcp.Tool{},
			Resources: []vmcp.Resource{},
			Prompts:   []vmcp.Prompt{},
			RoutingTable: &vmcp.RoutingTable{
				Tools:     make(map[string]*vmcp.BackendTarget),
				Resources: make(map[string]*vmcp.BackendTarget),
				Prompts:   make(map[string]*vmcp.BackendTarget),
			},
			Metadata: &aggregator.AggregationMetadata{},
		}, nil).
		AnyTimes()
	mockDiscoveryMgr.EXPECT().Stop().AnyTimes()

	ctx, cancel := context.WithCancel(t.Context())

	var healthMonCfg *health.MonitorConfig
	if (monitorCfg != health.MonitorConfig{}) {
		healthMonCfg = &monitorCfg
	}
	srv, err := server.New(ctx, &server.Config{
		Name:                "test-vmcp",
		Version:             "1.0.0",
		Host:                "127.0.0.1",
		Port:                port,
		GroupRef:            groupRef,
		HealthMonitorConfig: healthMonCfg,
		SessionFactory:      newNoopMockFactory(t),
	}, rt, mockBackendClient, mockDiscoveryMgr, vmcp.NewImmutableRegistry(backends), nil)
	require.NoError(t, err)

	type startResult struct {
		err error
	}
	done := make(chan startResult, 1)
	go func() {
		done <- startResult{err: srv.Start(ctx)}
	}()

	// Cleanup order (LIFO):
	//   1. cancel()  — stops the server and health monitor goroutines
	//   2. <-done    — waits for srv.Start (and all goroutines) to return
	//   3. ctrl.Finish — validates mock expectations after all calls have stopped
	t.Cleanup(func() {
		result := <-done
		if result.err != nil && !errors.Is(result.err, context.Canceled) {
			t.Errorf("server exited with unexpected error: %v", result.err)
		}
	})
	t.Cleanup(cancel)

	select {
	case <-srv.Ready():
	case result := <-done:
		t.Fatalf("server exited before becoming ready: %v", result.err)
	case <-time.After(5 * time.Second):
		t.Fatalf("Server did not become ready within 5s (address: %s)", srv.Address())
	}

	return srv
}

// queryStatus fetches and decodes /status from the given server.
func queryStatus(t *testing.T, srv *server.Server) StatusResponse {
	t.Helper()
	resp, err := http.Get("http://" + srv.Address() + "/status")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "unexpected HTTP status from /status")
	var status StatusResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&status))
	return status
}

// TestStatusEndpoint_ReflectsLiveHealthMonitor_Unhealthy verifies the fix for
// https://github.com/stacklok/toolhive/issues/4103: /status must report the
// same health state as the live health monitor, not the stale registry value.
//
// Without the fix, a backend registered as "healthy" would always appear healthy
// in /status even after the health monitor had marked it unhealthy.
func TestStatusEndpoint_ReflectsLiveHealthMonitor_Unhealthy(t *testing.T) {
	t.Parallel()

	// Backend starts as "healthy" in the registry – this is the stale value
	// that the old code would always return from /status.
	backends := []vmcp.Backend{{
		ID:            "b1",
		Name:          "test-backend",
		TransportType: "streamable-http",
		HealthStatus:  vmcp.BackendHealthy,
	}}

	monitorCfg := health.MonitorConfig{
		CheckInterval:      5 * time.Millisecond,
		UnhealthyThreshold: 1, // one failure → unhealthy immediately
		Timeout:            time.Second,
	}

	srv := createTestServerWithHealthMonitor(t, backends, monitorCfg, func(mockClient *mocks.MockBackendClient) {
		// All health checks fail – the monitor should mark the backend unhealthy.
		mockClient.EXPECT().
			ListCapabilities(gomock.Any(), gomock.Any()).
			Return(nil, errors.New("connection refused")).
			AnyTimes()
	}, "")

	// Poll /status until the live monitor state propagates. If the fix is
	// absent the backend stays "healthy" forever and the assertion times out.
	require.Eventually(t, func() bool {
		status := queryStatus(t, srv)
		if len(status.Backends) == 0 {
			return false
		}
		return status.Backends[0].Health == string(vmcp.BackendUnhealthy)
	}, 5*time.Second, 20*time.Millisecond, "expected /status to report backend as unhealthy")

	// The overall server health flag must also be false when no backend is healthy.
	status := queryStatus(t, srv)
	assert.False(t, status.Healthy)
	require.Len(t, status.Backends, 1)
	assert.Equal(t, string(vmcp.BackendUnhealthy), status.Backends[0].Health)
}

// TestStatusEndpoint_ReflectsLiveHealthMonitor_Healthy confirms that /status
// correctly reports a backend as healthy when the health monitor records success.
func TestStatusEndpoint_ReflectsLiveHealthMonitor_Healthy(t *testing.T) {
	t.Parallel()

	backends := []vmcp.Backend{{
		ID:            "b1",
		Name:          "test-backend",
		TransportType: "streamable-http",
		HealthStatus:  vmcp.BackendUnknown, // registry starts with unknown
	}}

	monitorCfg := health.MonitorConfig{
		CheckInterval:      5 * time.Millisecond,
		UnhealthyThreshold: 3,
		Timeout:            time.Second,
	}

	srv := createTestServerWithHealthMonitor(t, backends, monitorCfg, func(mockClient *mocks.MockBackendClient) {
		// Health checks succeed – the monitor should mark the backend healthy.
		mockClient.EXPECT().
			ListCapabilities(gomock.Any(), gomock.Any()).
			Return(&vmcp.CapabilityList{}, nil).
			AnyTimes()
	}, "")

	// Poll until the healthy state from the monitor appears in /status.
	require.Eventually(t, func() bool {
		status := queryStatus(t, srv)
		if len(status.Backends) == 0 {
			return false
		}
		return status.Backends[0].Health == string(vmcp.BackendHealthy)
	}, 5*time.Second, 20*time.Millisecond, "expected /status to report backend as healthy")

	status := queryStatus(t, srv)
	assert.True(t, status.Healthy)
	require.Len(t, status.Backends, 1)
	assert.Equal(t, string(vmcp.BackendHealthy), status.Backends[0].Health)
}

// TestStatusEndpoint_FallsBackToRegistry_WhenMonitorDisabled confirms the
// no-monitor path is unchanged: health status comes from the registry.
func TestStatusEndpoint_FallsBackToRegistry_WhenMonitorDisabled(t *testing.T) {
	t.Parallel()

	backends := []vmcp.Backend{
		{ID: "b1", Name: "healthy-backend", HealthStatus: vmcp.BackendHealthy},
		{ID: "b2", Name: "unhealthy-backend", HealthStatus: vmcp.BackendUnhealthy},
	}

	// createTestServerWithBackends does NOT configure a health monitor.
	srv := createTestServerWithBackends(t, backends, "")

	status := queryStatus(t, srv)

	require.Len(t, status.Backends, 2)
	healthByName := make(map[string]string)
	for _, b := range status.Backends {
		healthByName[b.Name] = b.Health
	}
	assert.Equal(t, string(vmcp.BackendHealthy), healthByName["healthy-backend"])
	assert.Equal(t, string(vmcp.BackendUnhealthy), healthByName["unhealthy-backend"])
}

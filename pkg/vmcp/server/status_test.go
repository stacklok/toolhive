package server_test

import (
	"context"
	"encoding/json"
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

// createTestServerWithBackends creates a test server instance with custom backends.
func createTestServerWithBackends(t *testing.T, backends []vmcp.Backend, groupRef string) *server.Server {
	t.Helper()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
	rt := router.NewDefaultRouter()

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

	srv, err := server.New(ctx, &server.Config{
		Name:     "test-vmcp",
		Version:  "1.0.0",
		Host:     "127.0.0.1",
		Port:     port,
		GroupRef: groupRef,
	}, rt, mockBackendClient, mockDiscoveryMgr, vmcp.NewImmutableRegistry(backends), nil)
	require.NoError(t, err)

	t.Cleanup(cancel)
	errCh := make(chan error, 1)
	go func() {
		if err := srv.Start(ctx); err != nil {
			errCh <- err
		}
	}()

	select {
	case <-srv.Ready():
	case err := <-errCh:
		t.Fatalf("Server failed to start: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatalf("Server did not become ready within 5s (address: %s)", srv.Address())
	}

	time.Sleep(10 * time.Millisecond)
	return srv
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

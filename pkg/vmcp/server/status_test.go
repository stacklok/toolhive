// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	discoveryMocks "github.com/stacklok/toolhive/pkg/vmcp/discovery/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// StatusResponse mirrors the server's status response structure for test deserialization.
type StatusResponse struct {
	Backends []BackendStatus `json:"backends"`
	Sessions *SessionsStatus `json:"sessions,omitempty"`
	Healthy  bool            `json:"healthy"`
	Version  string          `json:"version"`
	GroupRef string          `json:"group_ref"`
}

// SessionsStatus mirrors the server's sessions status structure for test deserialization.
type SessionsStatus struct {
	ActiveCount  int                     `json:"active_count"`
	BackendUsage map[string]BackendUsage `json:"backend_usage"`
}

// BackendUsage mirrors the server's backend usage structure for test deserialization.
// This provides operational visibility without exposing session identifiers.
type BackendUsage struct {
	SessionCount int `json:"session_count"` // Number of sessions using this backend
	HealthyCount int `json:"healthy_count"` // Number of sessions with healthy connections
	FailedCount  int `json:"failed_count"`  // Number of sessions with failed connections
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

func TestStatusEndpoint_SessionsNotIncludedWhenV2Disabled(t *testing.T) {
	t.Parallel()

	// Default server configuration has SessionManagementV2 = false
	backends := []vmcp.Backend{{
		ID: "b1", Name: "test-backend", HealthStatus: vmcp.BackendHealthy,
	}}
	srv := createTestServerWithBackends(t, backends, "")

	resp, err := http.Get("http://" + srv.Address() + "/status")
	require.NoError(t, err)
	defer resp.Body.Close()

	var status StatusResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&status))

	// Sessions field should be nil when v2 is disabled
	assert.Nil(t, status.Sessions, "Sessions should not be present when SessionManagementV2 is disabled")
	assert.Len(t, status.Backends, 1)
	assert.True(t, status.Healthy)
}

func TestStatusEndpoint_SessionsIncludedWhenV2Enabled(t *testing.T) {
	t.Parallel()

	// This test uses the sessionmanager package directly rather than mocking
	// to verify the full integration of backend usage statistics in /status.
	// Since we cannot easily create real MultiSessions without starting backend
	// servers, we verify the empty case (no active sessions) and the structure.

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

	backends := []vmcp.Backend{
		{ID: "backend1", Name: "backend1", HealthStatus: vmcp.BackendHealthy},
	}

	// Create a real session factory (required for SessionManagementV2)
	authReg := vmcpauth.NewDefaultOutgoingAuthRegistry()
	require.NoError(t, authReg.RegisterStrategy(
		authtypes.StrategyTypeUnauthenticated,
		strategies.NewUnauthenticatedStrategy(),
	))
	factory := vmcpsession.NewSessionFactory(authReg)

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	srv, err := server.New(ctx, &server.Config{
		Name:                "test-vmcp-v2",
		Version:             "1.0.0",
		Host:                "127.0.0.1",
		Port:                port,
		GroupRef:            "test-group",
		SessionTTL:          5 * time.Minute,
		SessionManagementV2: true,
		SessionFactory:      factory,
	}, rt, mockBackendClient, mockDiscoveryMgr, vmcp.NewImmutableRegistry(backends), nil)
	require.NoError(t, err)

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
		t.Fatalf("Server did not become ready within 5s")
	}

	time.Sleep(10 * time.Millisecond)

	// Make request to /status endpoint
	resp, err := http.Get("http://" + srv.Address() + "/status")
	require.NoError(t, err)
	defer resp.Body.Close()

	var status StatusResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&status))

	// Verify sessions section is included (even if empty)
	require.NotNil(t, status.Sessions, "Sessions should be present when SessionManagementV2 is enabled")
	assert.Equal(t, 0, status.Sessions.ActiveCount, "Should report 0 active sessions (no sessions created yet)")
	assert.NotNil(t, status.Sessions.BackendUsage, "BackendUsage should be non-nil map")
	assert.Empty(t, status.Sessions.BackendUsage, "BackendUsage should be empty (no sessions created yet)")

	// Verify the response structure doesn't expose session IDs
	resp2, err := http.Get("http://" + srv.Address() + "/status")
	require.NoError(t, err)
	defer resp2.Body.Close()

	var rawResponse map[string]interface{}
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&rawResponse))

	sessions, ok := rawResponse["sessions"].(map[string]interface{})
	require.True(t, ok, "sessions should be a map")

	// Ensure no session_id field exists
	_, hasSessionID := sessions["session_id"]
	assert.False(t, hasSessionID, "Should not expose session_id field")

	// Ensure no sessions array with individual session data
	_, hasSessions := sessions["sessions"]
	assert.False(t, hasSessions, "Should not have sessions array with individual session data")

	// Should only have active_count and backend_usage
	assert.Contains(t, sessions, "active_count")
	assert.Contains(t, sessions, "backend_usage")

	// Verify backend_usage is a map, not an array
	backendUsage, ok := sessions["backend_usage"].(map[string]interface{})
	require.True(t, ok, "backend_usage should be a map")
	assert.Empty(t, backendUsage, "backend_usage should be empty (no sessions yet)")
}

func TestStatusEndpoint_NoCredentialsExposed(t *testing.T) {
	t.Parallel()

	backends := []vmcp.Backend{{
		ID:           "b1",
		Name:         "secure-backend",
		BaseURL:      "https://secret-internal-url.example.com:9090/mcp",
		HealthStatus: vmcp.BackendHealthy,
		AuthConfig: &authtypes.BackendAuthStrategy{
			Type: authtypes.StrategyTypeTokenExchange,
		},
	}}
	srv := createTestServerWithBackends(t, backends, "")

	resp, err := http.Get("http://" + srv.Address() + "/status")
	require.NoError(t, err)
	defer resp.Body.Close()

	// Read raw response body to check what's exposed
	var rawResponse map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&rawResponse))

	// Verify no sensitive fields are exposed
	respBackends, ok := rawResponse["backends"].([]interface{})
	require.True(t, ok)
	require.Len(t, respBackends, 1)

	backend := respBackends[0].(map[string]interface{})

	// Should NOT expose: BaseURL, credentials, tokens, internal URLs
	_, hasBaseURL := backend["base_url"]
	_, hasURL := backend["url"]
	_, hasToken := backend["token"]
	_, hasCredentials := backend["credentials"]

	assert.False(t, hasBaseURL, "BaseURL should not be exposed")
	assert.False(t, hasURL, "URL should not be exposed")
	assert.False(t, hasToken, "Token should not be exposed")
	assert.False(t, hasCredentials, "Credentials should not be exposed")

	// Should expose: Name, Health, Transport, AuthType (safe metadata)
	assert.Contains(t, backend, "name")
	assert.Contains(t, backend, "health")
	assert.Contains(t, backend, "transport")
	assert.Contains(t, backend, "auth_type")
}

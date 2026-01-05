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
	discoveryMocks "github.com/stacklok/toolhive/pkg/vmcp/discovery/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
)

// mockK8sManager is a mock implementation of the K8sManager interface
type mockK8sManager struct {
	cacheSynced bool
}

func (m *mockK8sManager) WaitForCacheSync(_ context.Context) bool {
	return m.cacheSynced
}

// ReadinessResponse mirrors the server's readiness response structure for test deserialization.
type ReadinessResponse struct {
	Status string `json:"status"`
	Mode   string `json:"mode"`
	Reason string `json:"reason,omitempty"`
}

func TestReadinessEndpoint_StaticMode(t *testing.T) {
	t.Parallel()

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

	// Create server without K8sManager (static mode)
	srv, err := server.New(ctx, &server.Config{
		Name:       "test-vmcp",
		Version:    "1.0.0",
		Host:       "127.0.0.1",
		Port:       port,
		K8sManager: nil, // Static mode
	}, rt, mockBackendClient, mockDiscoveryMgr, vmcp.NewImmutableRegistry([]vmcp.Backend{}), nil)
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
		t.Fatalf("Server did not become ready within 5s")
	}

	time.Sleep(10 * time.Millisecond)

	// Test /readyz endpoint in static mode
	resp, err := http.Get("http://" + srv.Address() + "/readyz")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode, "Static mode should always return 200 OK")

	var readiness ReadinessResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&readiness))
	assert.Equal(t, "ready", readiness.Status)
	assert.Equal(t, "static", readiness.Mode)
}

func TestReadinessEndpoint_DynamicMode_CacheSynced(t *testing.T) {
	t.Parallel()

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

	// Create mock K8s manager with cache synced
	mockMgr := &mockK8sManager{cacheSynced: true}

	srv, err := server.New(ctx, &server.Config{
		Name:       "test-vmcp",
		Version:    "1.0.0",
		Host:       "127.0.0.1",
		Port:       port,
		K8sManager: mockMgr, // Dynamic mode with synced cache
	}, rt, mockBackendClient, mockDiscoveryMgr, vmcp.NewDynamicRegistry([]vmcp.Backend{}), nil)
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
		t.Fatalf("Server did not become ready within 5s")
	}

	time.Sleep(10 * time.Millisecond)

	// Test /readyz endpoint in dynamic mode with synced cache
	resp, err := http.Get("http://" + srv.Address() + "/readyz")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode, "Dynamic mode with synced cache should return 200 OK")

	var readiness ReadinessResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&readiness))
	assert.Equal(t, "ready", readiness.Status)
	assert.Equal(t, "dynamic", readiness.Mode)
}

func TestReadinessEndpoint_DynamicMode_CacheNotSynced(t *testing.T) {
	t.Parallel()

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

	// Create mock K8s manager with cache NOT synced
	mockMgr := &mockK8sManager{cacheSynced: false}

	srv, err := server.New(ctx, &server.Config{
		Name:       "test-vmcp",
		Version:    "1.0.0",
		Host:       "127.0.0.1",
		Port:       port,
		K8sManager: mockMgr, // Dynamic mode with unsynced cache
	}, rt, mockBackendClient, mockDiscoveryMgr, vmcp.NewDynamicRegistry([]vmcp.Backend{}), nil)
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
		t.Fatalf("Server did not become ready within 5s")
	}

	time.Sleep(10 * time.Millisecond)

	// Test /readyz endpoint in dynamic mode with unsynced cache
	resp, err := http.Get("http://" + srv.Address() + "/readyz")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, "Dynamic mode with unsynced cache should return 503")

	var readiness ReadinessResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&readiness))
	assert.Equal(t, "not_ready", readiness.Status)
	assert.Equal(t, "dynamic", readiness.Mode)
	assert.Equal(t, "cache_sync_pending", readiness.Reason)
}

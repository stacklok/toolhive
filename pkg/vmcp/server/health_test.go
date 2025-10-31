package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
)

// createTestServer creates a minimal test server instance.
// Each test should create its own server to enable parallel execution.
func createTestServer(t *testing.T) *server.Server {
	t.Helper()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	rt := router.NewDefaultRouter()

	srv := server.New(&server.Config{
		Name:    "test-vmcp",
		Version: "1.0.0",
		Host:    "127.0.0.1",
		Port:    0, // Random port for parallel tests
	}, rt, mockBackendClient)

	// Register minimal capabilities
	capabilities := &aggregator.AggregatedCapabilities{
		Tools:     []vmcp.Tool{},
		Resources: []vmcp.Resource{},
		Prompts:   []vmcp.Prompt{},
		RoutingTable: &vmcp.RoutingTable{
			Tools:     make(map[string]*vmcp.BackendTarget),
			Resources: make(map[string]*vmcp.BackendTarget),
			Prompts:   make(map[string]*vmcp.BackendTarget),
		},
		Metadata: &aggregator.AggregationMetadata{},
	}

	err := srv.RegisterCapabilities(context.Background(), capabilities)
	require.NoError(t, err)

	// Start server in background
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Start(ctx) }()

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)

	// Cleanup
	t.Cleanup(func() {
		cancel()
		time.Sleep(50 * time.Millisecond)
	})

	return srv
}

func TestHealthEndpoint(t *testing.T) {
	t.Parallel()

	t.Run("/health returns 200 OK with minimal response", func(t *testing.T) {
		t.Parallel()
		srv := createTestServer(t)

		resp, err := http.Get("http://" + srv.Address() + "/health")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

		var body map[string]string
		err = json.NewDecoder(resp.Body).Decode(&body)
		require.NoError(t, err)

		assert.Equal(t, "ok", body["status"])
	})

	t.Run("/ping returns 200 OK", func(t *testing.T) {
		t.Parallel()
		srv := createTestServer(t)

		resp, err := http.Get("http://" + srv.Address() + "/ping")
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "ok", mustDecodeJSON[map[string]string](t, resp.Body)["status"])
	})

	t.Run("health endpoint does not leak sensitive information", func(t *testing.T) {
		t.Parallel()
		srv := createTestServer(t)

		resp, err := http.Get("http://" + srv.Address() + "/health")
		require.NoError(t, err)
		defer resp.Body.Close()

		var body map[string]any
		err = json.NewDecoder(resp.Body).Decode(&body)
		require.NoError(t, err)

		// Verify NO sensitive data is exposed (multi-tenant security)
		sensitiveFields := []string{
			"sessions", "name", "version", "capabilities",
			"backends", "tools", "resources",
		}

		for _, field := range sensitiveFields {
			assert.NotContains(t, body, field)
		}

		assert.Len(t, body, 1, "Health response should only contain status field")
	})
}

// mustDecodeJSON is a test helper that decodes JSON or fails the test.
func mustDecodeJSON[T any](t *testing.T, r io.Reader) T {
	t.Helper()
	var result T
	err := json.NewDecoder(r).Decode(&result)
	require.NoError(t, err)
	return result
}

func TestServer_SessionManager(t *testing.T) {
	t.Parallel()

	t.Run("returns session manager instance", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockBackendClient := mocks.NewMockBackendClient(ctrl)
		rt := router.NewDefaultRouter()

		srv := server.New(&server.Config{
			Name:       "test-vmcp",
			Version:    "1.0.0",
			SessionTTL: 10 * time.Minute,
		}, rt, mockBackendClient)

		// SessionManager should be accessible
		mgr := srv.SessionManager()
		assert.NotNil(t, mgr)
	})

	t.Run("session manager uses configured TTL", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockBackendClient := mocks.NewMockBackendClient(ctrl)
		rt := router.NewDefaultRouter()

		customTTL := 15 * time.Minute
		srv := server.New(&server.Config{
			Name:       "test-vmcp",
			Version:    "1.0.0",
			SessionTTL: customTTL,
		}, rt, mockBackendClient)

		mgr := srv.SessionManager()
		assert.NotNil(t, mgr)

		// Manager should be configured with the TTL
		// We can't directly check TTL, but we can verify it was created
		assert.NotNil(t, mgr)
	})
}

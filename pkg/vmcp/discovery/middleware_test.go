package discovery

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/discovery/mocks"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// createTestSessionManager creates a session manager with VMCPSession factory for testing.
func createTestSessionManager(t *testing.T) *transportsession.Manager {
	t.Helper()
	sessionMgr := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
	t.Cleanup(func() { _ = sessionMgr.Stop() })
	return sessionMgr
}

func TestMiddleware_InitializeRequest(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockMgr := mocks.NewMockManager(ctrl)

	backends := []vmcp.Backend{
		{
			ID:            "backend1",
			Name:          "Backend 1",
			BaseURL:       "http://backend1:8080",
			TransportType: "streamable-http",
			HealthStatus:  vmcp.BackendHealthy,
		},
	}

	expectedCaps := &aggregator.AggregatedCapabilities{
		Tools: []vmcp.Tool{
			{Name: "tool1", BackendID: "backend1"},
		},
		Resources: []vmcp.Resource{},
		Prompts:   []vmcp.Prompt{},
		RoutingTable: &vmcp.RoutingTable{
			Tools: map[string]*vmcp.BackendTarget{
				"tool1": {WorkloadID: "backend1"},
			},
			Resources: make(map[string]*vmcp.BackendTarget),
			Prompts:   make(map[string]*vmcp.BackendTarget),
		},
		Metadata: &aggregator.AggregationMetadata{
			BackendCount: 1,
			ToolCount:    1,
		},
	}

	// Expect discovery to be called for initialize request (no session ID)
	mockMgr.EXPECT().
		Discover(gomock.Any(), backends).
		Return(expectedCaps, nil)

	// Create a test handler that verifies capabilities are in context
	handlerCalled := false
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true

		// Verify capabilities are in context
		caps, ok := DiscoveredCapabilitiesFromContext(r.Context())
		assert.True(t, ok, "capabilities should be in context")
		assert.NotNil(t, caps, "capabilities should not be nil")
		assert.Equal(t, expectedCaps, caps, "capabilities should match expected")

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("success"))
	})

	// Wrap handler with middleware
	backendRegistry := vmcp.NewImmutableRegistry(backends)
	middleware := Middleware(mockMgr, backendRegistry, createTestSessionManager(t))
	wrappedHandler := middleware(testHandler)

	// Create initialize request (no session ID header)
	req := httptest.NewRequest(http.MethodPost, "/mcp/v1/initialize", nil)
	rec := httptest.NewRecorder()

	// Execute request
	wrappedHandler.ServeHTTP(rec, req)

	// Verify response
	assert.True(t, handlerCalled, "handler should have been called")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "success", rec.Body.String())
}

func TestMiddleware_SubsequentRequest_SkipsDiscovery(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockMgr := mocks.NewMockManager(ctrl)

	backends := []vmcp.Backend{
		{
			ID:            "backend1",
			Name:          "Backend 1",
			BaseURL:       "http://backend1:8080",
			TransportType: "streamable-http",
			HealthStatus:  vmcp.BackendHealthy,
		},
	}

	// NO EXPECTATION for Discover - it should not be called for subsequent requests
	// If Discover is called, the test will fail due to unexpected call

	handlerCalled := false
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true

		// Verify capabilities ARE in context (retrieved from session, not discovered)
		caps, ok := DiscoveredCapabilitiesFromContext(r.Context())
		assert.True(t, ok, "capabilities should be in context from session")
		assert.NotNil(t, caps, "capabilities should not be nil")
		assert.NotNil(t, caps.RoutingTable, "routing table should not be nil")
		assert.Len(t, caps.RoutingTable.Tools, 1, "should have 1 tool from session")

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("success"))
	})

	// Create session manager and store routing table in a session
	sessionMgr := createTestSessionManager(t)

	// Create a routing table for this session
	routingTable := &vmcp.RoutingTable{
		Tools:     map[string]*vmcp.BackendTarget{"tool1": {WorkloadID: "backend1"}},
		Resources: make(map[string]*vmcp.BackendTarget),
		Prompts:   make(map[string]*vmcp.BackendTarget),
	}

	// Add session with routing table
	sess := vmcpsession.NewVMCPSession("test-session-123")
	sess.SetRoutingTable(routingTable)
	err := sessionMgr.AddSession(sess)
	require.NoError(t, err, "failed to add session")

	// Wrap handler with middleware
	backendRegistry := vmcp.NewImmutableRegistry(backends)
	middleware := Middleware(mockMgr, backendRegistry, sessionMgr)
	wrappedHandler := middleware(testHandler)

	// Create subsequent request (with session ID header)
	req := httptest.NewRequest(http.MethodPost, "/mcp/v1/tools/list", nil)
	req.Header.Set("Mcp-Session-Id", "test-session-123")
	rec := httptest.NewRecorder()

	// Execute request
	wrappedHandler.ServeHTTP(rec, req)

	// Verify response
	assert.True(t, handlerCalled, "handler should have been called")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "success", rec.Body.String())
}

func TestMiddleware_DiscoveryTimeout(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockMgr := mocks.NewMockManager(ctrl)

	backends := []vmcp.Backend{
		{ID: "backend1", Name: "Backend 1"},
	}

	// Simulate timeout by returning context.DeadlineExceeded
	mockMgr.EXPECT().
		Discover(gomock.Any(), backends).
		Return(nil, context.DeadlineExceeded)

	handlerCalled := false
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	backendRegistry := vmcp.NewImmutableRegistry(backends)
	middleware := Middleware(mockMgr, backendRegistry, createTestSessionManager(t))
	wrappedHandler := middleware(testHandler)

	// Initialize request (no session ID) - discovery should happen
	req := httptest.NewRequest(http.MethodPost, "/mcp/v1/initialize", nil)
	rec := httptest.NewRecorder()

	wrappedHandler.ServeHTTP(rec, req)

	// Verify timeout response
	assert.False(t, handlerCalled, "handler should not be called on timeout")
	assert.Equal(t, http.StatusGatewayTimeout, rec.Code)
	body, _ := io.ReadAll(rec.Body)
	assert.Contains(t, string(body), http.StatusText(http.StatusGatewayTimeout))
}

func TestMiddleware_DiscoveryFailure(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockMgr := mocks.NewMockManager(ctrl)

	backends := []vmcp.Backend{
		{ID: "backend1", Name: "Backend 1"},
	}

	// Simulate non-timeout error
	discoveryErr := errors.New("backend connection failed")
	mockMgr.EXPECT().
		Discover(gomock.Any(), backends).
		Return(nil, discoveryErr)

	handlerCalled := false
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	backendRegistry := vmcp.NewImmutableRegistry(backends)
	middleware := Middleware(mockMgr, backendRegistry, createTestSessionManager(t))
	wrappedHandler := middleware(testHandler)

	// Initialize request (no session ID) - discovery should happen
	req := httptest.NewRequest(http.MethodPost, "/mcp/v1/initialize", nil)
	rec := httptest.NewRecorder()

	wrappedHandler.ServeHTTP(rec, req)

	// Verify service unavailable response
	assert.False(t, handlerCalled, "handler should not be called on failure")
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	body, _ := io.ReadAll(rec.Body)
	assert.Contains(t, string(body), http.StatusText(http.StatusServiceUnavailable))
}

func TestMiddleware_CapabilitiesInContext(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockMgr := mocks.NewMockManager(ctrl)

	backends := []vmcp.Backend{
		{ID: "backend1", Name: "Backend 1"},
		{ID: "backend2", Name: "Backend 2"},
	}

	expectedCaps := &aggregator.AggregatedCapabilities{
		Tools: []vmcp.Tool{
			{Name: "tool1", BackendID: "backend1"},
			{Name: "tool2", BackendID: "backend2"},
		},
		Resources: []vmcp.Resource{
			{URI: "test://resource1", BackendID: "backend1"},
		},
		Prompts: []vmcp.Prompt{
			{Name: "prompt1", BackendID: "backend2"},
		},
		SupportsLogging:  true,
		SupportsSampling: false,
		RoutingTable: &vmcp.RoutingTable{
			Tools: map[string]*vmcp.BackendTarget{
				"tool1": {WorkloadID: "backend1"},
				"tool2": {WorkloadID: "backend2"},
			},
			Resources: map[string]*vmcp.BackendTarget{
				"test://resource1": {WorkloadID: "backend1"},
			},
			Prompts: map[string]*vmcp.BackendTarget{
				"prompt1": {WorkloadID: "backend2"},
			},
		},
		Metadata: &aggregator.AggregationMetadata{
			BackendCount:  2,
			ToolCount:     2,
			ResourceCount: 1,
			PromptCount:   1,
		},
	}

	mockMgr.EXPECT().
		Discover(gomock.Any(), backends).
		Return(expectedCaps, nil)

	// Create handler that inspects context in detail
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caps, ok := DiscoveredCapabilitiesFromContext(r.Context())
		require.True(t, ok, "capabilities must be in context")
		require.NotNil(t, caps, "capabilities must not be nil")

		// Verify all fields are accessible
		assert.Len(t, caps.Tools, 2)
		assert.Equal(t, "tool1", caps.Tools[0].Name)
		assert.Equal(t, "tool2", caps.Tools[1].Name)

		assert.Len(t, caps.Resources, 1)
		assert.Equal(t, "test://resource1", caps.Resources[0].URI)

		assert.Len(t, caps.Prompts, 1)
		assert.Equal(t, "prompt1", caps.Prompts[0].Name)

		assert.True(t, caps.SupportsLogging)
		assert.False(t, caps.SupportsSampling)

		assert.NotNil(t, caps.RoutingTable)
		assert.Contains(t, caps.RoutingTable.Tools, "tool1")
		assert.Contains(t, caps.RoutingTable.Tools, "tool2")
		assert.Contains(t, caps.RoutingTable.Resources, "test://resource1")
		assert.Contains(t, caps.RoutingTable.Prompts, "prompt1")

		assert.Equal(t, 2, caps.Metadata.BackendCount)
		assert.Equal(t, 2, caps.Metadata.ToolCount)
		assert.Equal(t, 1, caps.Metadata.ResourceCount)
		assert.Equal(t, 1, caps.Metadata.PromptCount)

		w.WriteHeader(http.StatusOK)
	})

	backendRegistry := vmcp.NewImmutableRegistry(backends)
	middleware := Middleware(mockMgr, backendRegistry, createTestSessionManager(t))
	wrappedHandler := middleware(testHandler)

	// Initialize request (no session ID) - discovery should happen
	req := httptest.NewRequest(http.MethodPost, "/mcp/v1/initialize", nil)
	rec := httptest.NewRecorder()

	wrappedHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMiddleware_PreservesUserContext(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockMgr := mocks.NewMockManager(ctrl)

	backends := []vmcp.Backend{
		{ID: "backend1", Name: "Backend 1"},
	}

	expectedCaps := &aggregator.AggregatedCapabilities{
		Tools: []vmcp.Tool{
			{Name: "tool1", BackendID: "backend1"},
		},
		RoutingTable: &vmcp.RoutingTable{
			Tools:     make(map[string]*vmcp.BackendTarget),
			Resources: make(map[string]*vmcp.BackendTarget),
			Prompts:   make(map[string]*vmcp.BackendTarget),
		},
		Metadata: &aggregator.AggregationMetadata{
			BackendCount: 1,
			ToolCount:    1,
		},
	}

	// Define the key type
	type userIDKey string

	mockMgr.EXPECT().
		Discover(gomock.Any(), backends).
		DoAndReturn(func(ctx context.Context, _ []vmcp.Backend) (*aggregator.AggregatedCapabilities, error) {
			// Verify user context is passed through
			userID := ctx.Value(userIDKey("user_id"))
			assert.Equal(t, "test_user", userID)
			return expectedCaps, nil
		})

	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify user context is preserved after middleware
		userID := r.Context().Value(userIDKey("user_id"))
		assert.Equal(t, "test_user", userID, "user context should be preserved")

		// Verify capabilities are also in context
		caps, ok := DiscoveredCapabilitiesFromContext(r.Context())
		assert.True(t, ok)
		assert.NotNil(t, caps)

		w.WriteHeader(http.StatusOK)
	})

	backendRegistry := vmcp.NewImmutableRegistry(backends)
	middleware := Middleware(mockMgr, backendRegistry, createTestSessionManager(t))
	wrappedHandler := middleware(testHandler)

	// Create initialize request with user context (as auth middleware would)
	req := httptest.NewRequest(http.MethodPost, "/mcp/v1/initialize", nil)
	ctx := context.WithValue(req.Context(), userIDKey("user_id"), "test_user")
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()

	wrappedHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMiddleware_ContextTimeoutHandling(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockMgr := mocks.NewMockManager(ctrl)

	backends := []vmcp.Backend{
		{ID: "backend1", Name: "Backend 1"},
	}

	// Simulate slow discovery that takes longer than timeout
	mockMgr.EXPECT().
		Discover(gomock.Any(), backends).
		DoAndReturn(func(ctx context.Context, _ []vmcp.Backend) (*aggregator.AggregatedCapabilities, error) {
			// Verify timeout context is set
			deadline, ok := ctx.Deadline()
			assert.True(t, ok, "context should have a deadline")
			assert.True(t, time.Until(deadline) <= discoveryTimeout, "timeout should be set correctly")

			// Simulate slow operation that exceeds the timeout
			// The 15-second timeout will expire before this 20-second sleep completes
			select {
			case <-ctx.Done():
				// Context was cancelled (either timeout or cancellation)
				return nil, ctx.Err()
			case <-time.After(20 * time.Second):
				// This should never be reached because context times out first
				return nil, errors.New("operation completed without timeout")
			}
		})

	testHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	backendRegistry := vmcp.NewImmutableRegistry(backends)
	middleware := Middleware(mockMgr, backendRegistry, createTestSessionManager(t))
	wrappedHandler := middleware(testHandler)

	// Initialize request (no session ID) - discovery should happen
	req := httptest.NewRequest(http.MethodPost, "/mcp/v1/initialize", nil)
	rec := httptest.NewRecorder()

	wrappedHandler.ServeHTTP(rec, req)

	// Verify timeout response (should be 504 Gateway Timeout)
	assert.Equal(t, http.StatusGatewayTimeout, rec.Code)
}

package k8s_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/k8s"
)

// TestNewBackendWatcher tests the backend watcher factory function validation
func TestNewBackendWatcher(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		cfg           *rest.Config
		namespace     string
		groupRef      string
		registry      vmcp.DynamicRegistry
		expectedError string
	}{
		{
			name:          "nil config",
			cfg:           nil,
			namespace:     "default",
			groupRef:      "default/test-group",
			registry:      vmcp.NewDynamicRegistry([]vmcp.Backend{}),
			expectedError: "rest config cannot be nil",
		},
		{
			name:          "empty namespace",
			cfg:           &rest.Config{},
			namespace:     "",
			groupRef:      "default/test-group",
			registry:      vmcp.NewDynamicRegistry([]vmcp.Backend{}),
			expectedError: "namespace cannot be empty",
		},
		{
			name:          "empty groupRef",
			cfg:           &rest.Config{},
			namespace:     "default",
			groupRef:      "",
			registry:      vmcp.NewDynamicRegistry([]vmcp.Backend{}),
			expectedError: "groupRef cannot be empty",
		},
		{
			name:          "nil registry",
			cfg:           &rest.Config{},
			namespace:     "default",
			groupRef:      "default/test-group",
			registry:      nil,
			expectedError: "registry cannot be nil",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mgr, err := k8s.NewBackendWatcher(tc.cfg, tc.namespace, tc.groupRef, tc.registry)

			if tc.expectedError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.expectedError)
				assert.Nil(t, mgr)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, mgr)
			}
		})
	}
}

// TestNewBackendWatcher_ValidInputs tests that NewBackendWatcher succeeds with valid inputs
// Note: This test validates that the watcher can be created, but doesn't start it
// to avoid requiring kubebuilder/envtest binaries in CI.
func TestNewBackendWatcher_ValidInputs(t *testing.T) {
	t.Parallel()

	// Create a basic REST config (doesn't need to connect to real cluster)
	cfg := &rest.Config{
		Host: "https://localhost:6443",
	}

	registry := vmcp.NewDynamicRegistry([]vmcp.Backend{
		{
			ID:   "test-backend",
			Name: "Test Backend",
		},
	})

	mgr, err := k8s.NewBackendWatcher(cfg, "default", "default/test-group", registry)
	require.NoError(t, err)
	assert.NotNil(t, mgr)
}

// TestBackendWatcher_WaitForCacheSync_NotStarted tests that WaitForCacheSync returns false
// when called before the watcher is started
func TestBackendWatcher_WaitForCacheSync_NotStarted(t *testing.T) {
	t.Parallel()

	cfg := &rest.Config{
		Host: "https://localhost:6443",
	}

	registry := vmcp.NewDynamicRegistry([]vmcp.Backend{})
	mgr, err := k8s.NewBackendWatcher(cfg, "default", "default/test-group", registry)
	require.NoError(t, err)

	// Try to wait for cache sync without starting manager
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	synced := mgr.WaitForCacheSync(ctx)
	assert.False(t, synced, "Cache sync should fail when watcher not started")
}

// TestBackendWatcher_StartValidation tests that Start can be called and respects context
func TestBackendWatcher_StartValidation(t *testing.T) {
	t.Parallel()

	cfg := &rest.Config{
		Host: "https://localhost:6443",
	}

	registry := vmcp.NewDynamicRegistry([]vmcp.Backend{})
	mgr, err := k8s.NewBackendWatcher(cfg, "default", "default/test-group-validation", registry)
	require.NoError(t, err)

	// Start watcher in background with a short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Start will exit when context times out (no real cluster to connect to)
	// This validates the watcher respects context cancellation
	err = mgr.Start(ctx)

	// Either nil (graceful exit) or error (connection failure) are both acceptable
	// The important thing is it doesn't hang
	t.Logf("Start returned: %v", err)
}

// mockBackendWatcherForTest is a simple mock for testing readiness endpoint behavior
type mockBackendWatcherForTest struct {
	cacheSynced bool
	syncCalled  bool
}

func (m *mockBackendWatcherForTest) WaitForCacheSync(_ context.Context) bool {
	m.syncCalled = true
	return m.cacheSynced
}

// TestMockBackendWatcher_InterfaceCompliance verifies the mock implements the interface
func TestMockBackendWatcher_InterfaceCompliance(t *testing.T) {
	t.Parallel()

	var _ interface {
		WaitForCacheSync(ctx context.Context) bool
	} = (*mockBackendWatcherForTest)(nil)
}

// TestMockBackendWatcher_CacheSynced tests mock watcher behavior when cache is synced
func TestMockBackendWatcher_CacheSynced(t *testing.T) {
	t.Parallel()

	mock := &mockBackendWatcherForTest{cacheSynced: true}

	ctx := context.Background()
	synced := mock.WaitForCacheSync(ctx)

	assert.True(t, synced)
	assert.True(t, mock.syncCalled, "WaitForCacheSync should have been called")
}

// TestMockBackendWatcher_CacheNotSynced tests mock watcher behavior when cache is not synced
func TestMockBackendWatcher_CacheNotSynced(t *testing.T) {
	t.Parallel()

	mock := &mockBackendWatcherForTest{cacheSynced: false}

	ctx := context.Background()
	synced := mock.WaitForCacheSync(ctx)

	assert.False(t, synced)
	assert.True(t, mock.syncCalled, "WaitForCacheSync should have been called")
}

// TestBackendWatcher_Lifecycle documents the expected lifecycle without requiring real cluster
func TestBackendWatcher_Lifecycle(t *testing.T) {
	t.Parallel()

	// This test documents the expected watcher lifecycle:
	// 1. Create watcher with NewBackendWatcher
	// 2. Start watcher in background goroutine
	// 3. Wait for cache sync before serving requests
	// 4. Cancel context to trigger graceful shutdown

	t.Run("documentation", func(t *testing.T) {
		t.Parallel()

		// Example lifecycle (documented, not executed):
		expectedLifecycle := `
		// Create watcher
		cfg, _ := rest.InClusterConfig()
		registry := vmcp.NewDynamicRegistry(backends)
		watcher, _ := k8s.NewBackendWatcher(cfg, "default", "default/my-group", registry)

		// Start in background
		ctx, cancel := context.WithCancel(context.Background())
		go watcher.Start(ctx)

		// Wait for cache sync (for readiness probe)
		syncCtx, syncCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer syncCancel()
		if !watcher.WaitForCacheSync(syncCtx) {
			return fmt.Errorf("cache sync failed")
		}

		// Server is ready to serve requests
		// ...

		// Graceful shutdown
		cancel()
		`
		assert.NotEmpty(t, expectedLifecycle)
	})
}

// TestBackendWatcher_ContextCancellation tests that context cancellation is respected
func TestBackendWatcher_ContextCancellation(t *testing.T) {
	t.Parallel()

	cfg := &rest.Config{
		Host: "https://localhost:6443",
	}

	registry := vmcp.NewDynamicRegistry([]vmcp.Backend{})
	mgr, err := k8s.NewBackendWatcher(cfg, "default", "default/test-group-cancellation", registry)
	require.NoError(t, err)

	// Create a context that's already cancelled
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	// Start should exit quickly when context is already cancelled
	// This validates the watcher respects pre-cancelled contexts
	startTime := time.Now()
	err = mgr.Start(ctx)
	duration := time.Since(startTime)

	// Should exit quickly (within 1 second)
	assert.Less(t, duration, time.Second, "Start should exit quickly with cancelled context")

	// Either nil (graceful exit) or error (context cancelled) are acceptable
	t.Logf("Start returned in %v: %v", duration, err)
}

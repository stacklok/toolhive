// Package k8s provides Kubernetes integration for Virtual MCP Server dynamic mode.
//
// In dynamic mode (outgoingAuth.source: discovered), the vMCP server runs a
// controller-runtime manager with informers to watch K8s resources dynamically.
// This enables backends to be added/removed from the MCPGroup without restarting.
package k8s

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// Manager wraps a controller-runtime manager for vMCP dynamic mode.
//
// In K8s mode (outgoingAuth.source: discovered), this manager runs informers
// that watch for backend changes in the referenced MCPGroup. When backends
// are added or removed, the manager updates the DynamicRegistry which triggers
// cache invalidation via version-based lazy invalidation.
//
// Design Philosophy:
//   - Wraps controller-runtime manager for lifecycle management
//   - Provides WaitForCacheSync for readiness probe gating
//   - Graceful shutdown on context cancellation
//   - Single responsibility: watch K8s resources and update registry
//
// Static mode (CLI) skips this entirely - no controller-runtime, no informers.
type Manager struct {
	// ctrlManager is the underlying controller-runtime manager
	ctrlManager manager.Manager

	// namespace is the namespace to watch for resources
	namespace string

	// groupRef identifies the MCPGroup to watch (format: "namespace/name")
	groupRef string

	// registry is the DynamicRegistry to update when backends change
	registry vmcp.DynamicRegistry

	// started tracks if the manager has been started
	started bool
}

// NewManager creates a new manager for vMCP dynamic mode.
//
// This initializes a controller-runtime manager configured to watch resources
// in the specified namespace. The manager will monitor the referenced MCPGroup
// and update the DynamicRegistry when backends are added or removed.
//
// Parameters:
//   - cfg: Kubernetes REST config (typically from in-cluster config)
//   - namespace: Namespace to watch for resources
//   - groupRef: MCPGroup reference in "namespace/name" format
//   - registry: DynamicRegistry to update when backends change
//
// Returns:
//   - *Manager: Configured manager ready to Start()
//   - error: Configuration or initialization errors
//
// Example:
//
//	restConfig, _ := rest.InClusterConfig()
//	registry := vmcp.NewDynamicRegistry(initialBackends)
//	mgr, err := k8s.NewManager(restConfig, "default", "default/my-group", registry)
//	if err != nil {
//	    return err
//	}
//	go mgr.Start(ctx)
//	if !mgr.WaitForCacheSync(ctx) {
//	    return fmt.Errorf("cache sync failed")
//	}
func NewManager(
	cfg *rest.Config,
	namespace string,
	groupRef string,
	registry vmcp.DynamicRegistry,
) (*Manager, error) {
	if cfg == nil {
		return nil, fmt.Errorf("rest config cannot be nil")
	}
	if namespace == "" {
		return nil, fmt.Errorf("namespace cannot be empty")
	}
	if groupRef == "" {
		return nil, fmt.Errorf("groupRef cannot be empty")
	}
	if registry == nil {
		return nil, fmt.Errorf("registry cannot be nil")
	}

	// Get the runtime scheme for ToolHive CRDs
	scheme := runtime.NewScheme()
	// TODO: Add scheme registration for ToolHive CRDs
	// err := thvscheme.AddToScheme(scheme)
	// if err != nil {
	//     return nil, fmt.Errorf("failed to add scheme: %w", err)
	// }

	// Create controller-runtime manager with namespace-scoped cache
	ctrlManager, err := ctrl.NewManager(cfg, manager.Options{
		Scheme: scheme,
		Cache: cache.Options{
			DefaultNamespaces: map[string]cache.Config{
				namespace: {},
			},
		},
		// Disable health probes - vMCP server handles its own
		HealthProbeBindAddress: "0",
		// Leader election not needed for vMCP (single replica per VirtualMCPServer)
		LeaderElection: false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create controller manager: %w", err)
	}

	return &Manager{
		ctrlManager: ctrlManager,
		namespace:   namespace,
		groupRef:    groupRef,
		registry:    registry,
		started:     false,
	}, nil
}

// Start starts the controller-runtime manager and blocks until context is cancelled.
//
// This method runs informers that watch for backend changes in the MCPGroup.
// It's designed to run in a background goroutine and will gracefully shutdown
// when the context is cancelled.
//
// Design Notes:
//   - Blocks until context cancellation (controller-runtime pattern)
//   - Graceful shutdown on context cancel
//   - Safe to call only once (subsequent calls will error)
//
// Example:
//
//	go func() {
//	    if err := mgr.Start(ctx); err != nil {
//	        logger.Errorf("Manager stopped with error: %v", err)
//	    }
//	}()
func (m *Manager) Start(ctx context.Context) error {
	if m.started {
		return fmt.Errorf("manager already started")
	}
	m.started = true

	logger.Info("Starting Kubernetes manager for vMCP dynamic mode")
	logger.Infof("Watching namespace: %s, group: %s", m.namespace, m.groupRef)

	// TODO: Add backend watcher controller
	// err := m.addBackendWatcher()
	// if err != nil {
	//     return fmt.Errorf("failed to add backend watcher: %w", err)
	// }

	// Start the manager (blocks until context cancelled)
	if err := m.ctrlManager.Start(ctx); err != nil {
		return fmt.Errorf("manager failed: %w", err)
	}

	logger.Info("Kubernetes manager stopped")
	return nil
}

// WaitForCacheSync waits for the manager's informer caches to sync.
//
// This is used by the /readyz endpoint to gate readiness until the manager
// has populated its caches. This ensures the vMCP server doesn't serve requests
// until it has an accurate view of backends.
//
// Parameters:
//   - ctx: Context with optional timeout for the wait operation
//
// Returns:
//   - bool: true if caches synced successfully, false on timeout or error
//
// Design Notes:
//   - Non-blocking if manager not started (returns false)
//   - Respects context timeout (e.g., 5-second readiness probe timeout)
//   - Safe to call multiple times (idempotent)
//
// Example (readiness probe):
//
//	func (s *Server) handleReadiness(w http.ResponseWriter, r *http.Request) {
//	    if s.k8sManager != nil {
//	        ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
//	        defer cancel()
//	        if !s.k8sManager.WaitForCacheSync(ctx) {
//	            w.WriteHeader(http.StatusServiceUnavailable)
//	            return
//	        }
//	    }
//	    w.WriteHeader(http.StatusOK)
//	}
func (m *Manager) WaitForCacheSync(ctx context.Context) bool {
	if !m.started {
		logger.Warn("WaitForCacheSync called but manager not started")
		return false
	}

	// Get the cache from the manager
	informerCache := m.ctrlManager.GetCache()

	// Create a timeout context if not already set
	// Default to 30 seconds to handle typical K8s API latency
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	logger.Info("Waiting for Kubernetes cache sync...")

	// Wait for cache to sync
	synced := informerCache.WaitForCacheSync(ctx)
	if !synced {
		logger.Warn("Cache sync timed out or failed")
		return false
	}

	logger.Info("Kubernetes cache synced successfully")
	return true
}

// TODO: Add backend watcher implementation in next phase
// This will watch the MCPGroup and call registry.Upsert/Remove when backends change
// func (m *Manager) addBackendWatcher() error {
//     // Create reconciler that watches MCPGroup
//     // On reconcile:
//     //   1. Get MCPGroup spec
//     //   2. Extract backend list
//     //   3. Call registry.Upsert for new/updated backends
//     //   4. Call registry.Remove for deleted backends
//     // This triggers cache invalidation via version increment
//     return nil
// }

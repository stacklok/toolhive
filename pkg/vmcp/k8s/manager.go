// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package k8s provides Kubernetes integration for Virtual MCP Server dynamic mode.
//
// In dynamic mode (outgoingAuth.source: discovered), the vMCP server runs a
// controller-runtime manager with informers to watch K8s resources dynamically.
// This enables backends to be added/removed from the MCPGroup without restarting.
package k8s

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
	"github.com/stacklok/toolhive/pkg/vmcp/workloads"
)

var (
	// setLoggerOnce ensures the controller-runtime logger is set exactly once
	// to avoid race conditions when multiple BackendWatcher instances are created
	setLoggerOnce sync.Once
)

// BackendWatcher wraps a controller-runtime manager for vMCP dynamic mode.
//
// In K8s mode (outgoingAuth.source: discovered), this watcher runs informers
// that watch for backend changes in the referenced MCPGroup. When backends
// are added or removed, the watcher updates the DynamicRegistry which triggers
// cache invalidation via version-based lazy invalidation.
//
// Design Philosophy:
//   - Wraps controller-runtime manager for lifecycle management
//   - Provides WaitForCacheSync for readiness probe gating
//   - Graceful shutdown on context cancellation
//   - Single responsibility: watch K8s resources and update registry
//
// Static mode (CLI) skips this entirely - no controller-runtime, no informers.
type BackendWatcher struct {
	// ctrlManager is the underlying controller-runtime manager
	ctrlManager manager.Manager

	// namespace is the namespace to watch for resources
	namespace string

	// groupRef identifies the MCPGroup to watch (format: "namespace/name")
	groupRef string

	// registry is the DynamicRegistry to update when backends change
	registry vmcp.DynamicRegistry

	// healthMonitor is the optional health monitor to notify of backend changes.
	// Nil if health monitoring is disabled.
	healthMonitor *health.Monitor

	// mu protects the started field for thread-safe access
	mu sync.Mutex

	// started tracks if the watcher has been started (protected by mu)
	started bool
}

// NewBackendWatcher creates a new backend watcher for vMCP dynamic mode.
//
// This initializes a controller-runtime manager configured to watch resources
// in the specified namespace. The watcher will monitor the referenced MCPGroup
// and update the DynamicRegistry when backends are added or removed.
//
// Parameters:
//   - cfg: Kubernetes REST config (typically from in-cluster config)
//   - namespace: Namespace to watch for resources
//   - groupRef: MCPGroup reference in "namespace/name" format
//   - registry: DynamicRegistry to update when backends change
//   - healthMonitor: Optional health monitor to notify of backend changes (nil if disabled)
//
// Returns:
//   - *BackendWatcher: Configured watcher ready to Start()
//   - error: Configuration or initialization errors
//
// Example:
//
//	restConfig, _ := rest.InClusterConfig()
//	registry := vmcp.NewDynamicRegistry(initialBackends)
//	watcher, err := k8s.NewBackendWatcher(restConfig, "default", "default/my-group", registry, healthMonitor)
//	if err != nil {
//	    return err
//	}
//	go watcher.Start(ctx)
//	if !watcher.WaitForCacheSync(ctx) {
//	    return fmt.Errorf("cache sync failed")
//	}
func NewBackendWatcher(
	cfg *rest.Config,
	namespace string,
	groupRef string,
	registry vmcp.DynamicRegistry,
	healthMonitor *health.Monitor,
) (*BackendWatcher, error) {
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

	// Set controller-runtime logger to use ToolHive's structured logger
	// Use sync.Once to avoid race conditions in tests where multiple
	// BackendWatcher instances are created concurrently
	setLoggerOnce.Do(func() {
		ctrl.SetLogger(logger.NewLogr())
	})

	// Create runtime scheme and register ToolHive CRDs + core Kubernetes types
	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to register ToolHive CRDs to scheme: %w", err)
	}

	// Register core Kubernetes types (Secrets, ConfigMaps, etc.) needed by discoverer
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to register core Kubernetes types to scheme: %w", err)
	}

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

	return &BackendWatcher{
		ctrlManager:   ctrlManager,
		namespace:     namespace,
		groupRef:      groupRef,
		registry:      registry,
		healthMonitor: healthMonitor,
		started:       false,
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
//	    if err := watcher.Start(ctx); err != nil {
//	        logger.Errorf("BackendWatcher stopped with error: %v", err)
//	    }
//	}()
func (w *BackendWatcher) Start(ctx context.Context) error {
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return fmt.Errorf("watcher already started")
	}
	w.started = true
	w.mu.Unlock()

	logger.Info("Starting Kubernetes backend watcher for vMCP dynamic mode")
	logger.Infof("Watching namespace: %s, group: %s", w.namespace, w.groupRef)

	// Register backend watch controller to reconcile MCPServer/MCPRemoteProxy changes
	err := w.addBackendWatchController()
	if err != nil {
		return fmt.Errorf("failed to add backend watch controller: %w", err)
	}

	// Start the manager (blocks until context cancelled)
	if err := w.ctrlManager.Start(ctx); err != nil {
		return fmt.Errorf("watcher failed: %w", err)
	}

	logger.Info("Kubernetes backend watcher stopped")
	return nil
}

// WaitForCacheSync waits for the watcher's informer caches to sync.
//
// This is used by the /readyz endpoint to gate readiness until the watcher
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
//   - Non-blocking if watcher not started (returns false)
//   - Respects context timeout (e.g., 5-second readiness probe timeout)
//   - Safe to call multiple times (idempotent)
//
// Example (readiness probe):
//
//	func (s *Server) handleReadiness(w http.ResponseWriter, r *http.Request) {
//	    if s.backendWatcher != nil {
//	        ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
//	        defer cancel()
//	        if !s.backendWatcher.WaitForCacheSync(ctx) {
//	            w.WriteHeader(http.StatusServiceUnavailable)
//	            return
//	        }
//	    }
//	    w.WriteHeader(http.StatusOK)
//	}
func (w *BackendWatcher) WaitForCacheSync(ctx context.Context) bool {
	w.mu.Lock()
	started := w.started
	w.mu.Unlock()

	if !started {
		logger.Warn("WaitForCacheSync called but watcher not started")
		return false
	}

	// Get the cache from the manager
	informerCache := w.ctrlManager.GetCache()

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

// SetHealthMonitor sets the health monitor for the watcher.
// This must be called before Start() if health monitoring support is desired.
// This method exists because the health monitor is created inside Server.New(),
// which happens after the BackendWatcher is instantiated.
func (w *BackendWatcher) SetHealthMonitor(monitor *health.Monitor) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.started {
		logger.Warn("Cannot set health monitor on already-started watcher")
		return
	}

	w.healthMonitor = monitor
	logger.Debug("Health monitor configured for BackendWatcher")
}

// addBackendWatchController registers the BackendReconciler with the controller manager.
//
// This method creates and registers a reconciler that watches MCPServer and MCPRemoteProxy
// resources in the configured namespace, filtering by groupRef to only process backends
// belonging to this vMCP server's MCPGroup.
//
// When backends are added, updated, or removed, the reconciler:
//  1. Converts K8s resources to vmcp.Backend structs
//  2. Calls registry.Upsert() for new/updated backends
//  3. Calls registry.Remove() for deleted backends
//
// This triggers version-based cache invalidation in the DynamicRegistry, ensuring
// the discovery manager detects changes and invalidates cached capabilities.
//
// Returns:
//   - nil: Reconciler registered successfully
//   - error: Failed to create discoverer or register reconciler
func (w *BackendWatcher) addBackendWatchController() error {
	// Create K8s discoverer for backend conversion
	// This reuses the existing workloads package conversion logic
	discoverer := workloads.NewK8SDiscovererWithClient(
		w.ctrlManager.GetClient(),
		w.namespace,
	)

	// Create backend reconciler with references to namespace, groupRef, registry, and health monitor
	reconciler := &BackendReconciler{
		Client:        w.ctrlManager.GetClient(),
		Namespace:     w.namespace,
		GroupRef:      w.groupRef,
		Registry:      w.registry,
		Discoverer:    discoverer,
		HealthMonitor: w.healthMonitor,
	}

	// Register reconciler with manager
	// This sets up watches on MCPServer, MCPRemoteProxy, and ExternalAuthConfig
	if err := reconciler.SetupWithManager(w.ctrlManager); err != nil {
		return fmt.Errorf("failed to setup backend reconciler: %w", err)
	}

	logger.Info("Backend watch controller registered successfully")
	return nil
}

// Package k8s provides Kubernetes integration for Virtual MCP Server dynamic mode.
package k8s

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/workloads"
)

// BackendReconciler watches MCPServers and MCPRemoteProxies, converting them to
// vmcp.Backend and updating the DynamicRegistry when backends change.
//
// This reconciler is specifically designed for vMCP dynamic mode where backends
// can be added/removed without restarting the vMCP server. It filters backends
// by groupRef to only process workloads belonging to the configured MCPGroup.
//
// Design Philosophy:
//   - Reuses existing conversion logic from workloads.Discoverer.GetWorkloadAsVMCPBackend()
//   - Filters workloads by groupRef before conversion (security + performance)
//   - Handles both MCPServer and MCPRemoteProxy resources
//   - Updates DynamicRegistry which triggers version-based cache invalidation
//   - Watches ExternalAuthConfig for auth changes (critical security path)
//   - Does NOT watch Secrets directly (performance optimization)
//
// Reconciliation Flow:
//  1. Fetch resource (try MCPServer, then MCPRemoteProxy)
//  2. If not found (deleted) → Remove from registry
//  3. If groupRef doesn't match → Remove from registry (moved to different group)
//  4. Convert to vmcp.Backend using discoverer
//  5. If conversion fails or returns nil (auth failed) → Remove from registry
//  6. Upsert backend to registry (triggers version increment + cache invalidation)
type BackendReconciler struct {
	client.Client

	// Namespace is the namespace to watch for resources (matches BackendWatcher)
	Namespace string

	// GroupRef is the MCPGroup name to filter workloads (format: "group-name")
	GroupRef string

	// Registry is the DynamicRegistry to update when backends change
	Registry vmcp.DynamicRegistry

	// Discoverer converts K8s resources to vmcp.Backend (reuses existing code)
	Discoverer workloads.Discoverer
}

// Reconcile handles MCPServer and MCPRemoteProxy events, updating the DynamicRegistry.
//
// This method is called by controller-runtime whenever:
//   - A watched resource (MCPServer, MCPRemoteProxy, ExternalAuthConfig) changes
//   - An event handler maps a resource change to this reconcile request
//
// The reconciler filters by groupRef to only process backends belonging to the
// configured MCPGroup, ensuring security isolation between vMCP servers.
//
// Returns:
//   - ctrl.Result{}, nil: Reconciliation succeeded, no requeue needed
//   - ctrl.Result{}, err: Reconciliation failed, controller-runtime will requeue
//
//nolint:gocyclo // Reconciler complexity is inherent to handling multiple resource types and error cases
func (r *BackendReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	// Try to fetch as MCPServer first
	mcpServer := &mcpv1alpha1.MCPServer{}
	errServer := r.Get(ctx, req.NamespacedName, mcpServer)

	// Try to fetch as MCPRemoteProxy
	mcpRemoteProxy := &mcpv1alpha1.MCPRemoteProxy{}
	errProxy := r.Get(ctx, req.NamespacedName, mcpRemoteProxy)

	// Determine which resource type we're reconciling
	var (
		isServer        bool
		isProxy         bool
		currentGroupRef string
	)

	if errServer == nil {
		isServer = true
		currentGroupRef = mcpServer.Spec.GroupRef
	} else if errProxy == nil {
		isProxy = true
		currentGroupRef = mcpRemoteProxy.Spec.GroupRef
	} else if errors.IsNotFound(errServer) && errors.IsNotFound(errProxy) {
		// Resource deleted - remove from registry
		backendID := fmt.Sprintf("%s/%s", req.Namespace, req.Name)
		ctxLogger.Info("Resource deleted, removing from registry", "backendID", backendID)

		if err := r.Registry.Remove(backendID); err != nil {
			ctxLogger.Error(err, "Failed to remove backend from registry")
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	} else {
		// Unexpected error fetching resource
		if errServer != nil {
			ctxLogger.Error(errServer, "Failed to get MCPServer")
			return ctrl.Result{}, errServer
		}
		ctxLogger.Error(errProxy, "Failed to get MCPRemoteProxy")
		return ctrl.Result{}, errProxy
	}

	// GroupRef filtering: Only process backends belonging to our MCPGroup
	// This provides security isolation between vMCP servers
	if currentGroupRef != r.GroupRef {
		// Backend no longer belongs to this group - remove from registry
		backendID := fmt.Sprintf("%s/%s", req.Namespace, req.Name)
		ctxLogger.V(1).Info(
			"Resource does not match groupRef, removing from registry",
			"backendID", backendID,
			"resourceGroupRef", currentGroupRef,
			"watcherGroupRef", r.GroupRef,
		)

		if err := r.Registry.Remove(backendID); err != nil {
			ctxLogger.Error(err, "Failed to remove backend from registry after groupRef mismatch")
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	}

	// Convert resource to vmcp.Backend using existing discoverer
	// The discoverer handles auth resolution, URL discovery, and all backend conversion logic
	var workload workloads.TypedWorkload
	if isServer {
		workload = workloads.TypedWorkload{
			Name: mcpServer.Name,
			Type: workloads.WorkloadTypeMCPServer,
		}
	} else if isProxy {
		workload = workloads.TypedWorkload{
			Name: mcpRemoteProxy.Name,
			Type: workloads.WorkloadTypeMCPRemoteProxy,
		}
	}

	backend, err := r.Discoverer.GetWorkloadAsVMCPBackend(ctx, workload)
	if err != nil {
		ctxLogger.Error(err, "Failed to convert workload to backend", "workload", workload.Name)
		// Remove from registry if conversion fails (could be auth failure)
		backendID := fmt.Sprintf("%s/%s", req.Namespace, req.Name)
		if removeErr := r.Registry.Remove(backendID); removeErr != nil {
			ctxLogger.Error(removeErr, "Failed to remove backend after conversion error")
		}
		return ctrl.Result{}, err
	}

	// backend is nil if auth resolution failed or workload not accessible
	// This is a security-critical check - we MUST NOT add backends without valid auth
	if backend == nil {
		backendID := fmt.Sprintf("%s/%s", req.Namespace, req.Name)
		ctxLogger.Info(
			"Backend conversion returned nil (auth failure or no URL), removing from registry",
			"backendID", backendID,
		)
		if err := r.Registry.Remove(backendID); err != nil {
			ctxLogger.Error(err, "Failed to remove backend after nil conversion")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Upsert backend to registry (triggers version increment + cache invalidation)
	// The DynamicRegistry's version-based invalidation ensures the discovery manager
	// detects this change and invalidates cached capabilities on next access
	if err := r.Registry.Upsert(*backend); err != nil {
		ctxLogger.Error(err, "Failed to upsert backend to registry", "backendID", backend.ID)
		return ctrl.Result{}, err
	}

	ctxLogger.Info(
		"Successfully reconciled backend",
		"backendID", backend.ID,
		"registryVersion", r.Registry.Version(),
	)

	return ctrl.Result{}, nil
}

// SetupWithManager registers the BackendReconciler with the controller manager.
//
// This method configures the reconciler to watch:
//   - MCPServers (primary resource, watched via For())
//   - MCPRemoteProxies (mapped via event handler with groupRef filter)
//   - MCPExternalAuthConfigs (mapped to servers/proxies that reference them)
//
// The reconciler does NOT watch Secrets directly for performance reasons.
// Secrets change frequently for unrelated reasons (TLS certs, app configs, etc.).
// Auth updates will trigger via ExternalAuthConfig changes or pod restarts.
//
// Watch Design:
//  1. For(&MCPServer{}) - Primary resource, all changes trigger reconciliation
//  2. Watches(&MCPRemoteProxy{}) - Secondary resource, filtered by groupRef
//  3. Watches(&ExternalAuthConfig{}) - Maps to servers/proxies that reference it
//
// All watches are scoped to the reconciler's namespace (configured in BackendWatcher).
func (r *BackendReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Event handler for ExternalAuthConfig changes
	// Maps ExternalAuthConfig → MCPServers/MCPRemoteProxies that reference it
	externalAuthConfigHandler := handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []reconcile.Request {
			authConfig, ok := obj.(*mcpv1alpha1.MCPExternalAuthConfig)
			if !ok {
				return nil
			}

			var requests []reconcile.Request

			// Find MCPServers referencing this ExternalAuthConfig
			mcpServerList := &mcpv1alpha1.MCPServerList{}
			if err := r.List(ctx, mcpServerList, client.InNamespace(r.Namespace)); err != nil {
				log.FromContext(ctx).Error(err, "Failed to list MCPServers for ExternalAuthConfig watch")
				return nil
			}

			for _, server := range mcpServerList.Items {
				// Only reconcile if server matches our groupRef AND references this auth config
				if server.Spec.GroupRef != r.GroupRef {
					continue
				}

				if server.Spec.ExternalAuthConfigRef != nil &&
					server.Spec.ExternalAuthConfigRef.Name == authConfig.Name {
					requests = append(requests, reconcile.Request{
						NamespacedName: types.NamespacedName{
							Name:      server.Name,
							Namespace: server.Namespace,
						},
					})
				}
			}

			// Find MCPRemoteProxies referencing this ExternalAuthConfig
			proxyList := &mcpv1alpha1.MCPRemoteProxyList{}
			if err := r.List(ctx, proxyList, client.InNamespace(r.Namespace)); err != nil {
				log.FromContext(ctx).Error(err, "Failed to list MCPRemoteProxies for ExternalAuthConfig watch")
				return nil
			}

			for _, proxy := range proxyList.Items {
				// Only reconcile if proxy matches our groupRef AND references this auth config
				if proxy.Spec.GroupRef != r.GroupRef {
					continue
				}

				if proxy.Spec.ExternalAuthConfigRef != nil &&
					proxy.Spec.ExternalAuthConfigRef.Name == authConfig.Name {
					requests = append(requests, reconcile.Request{
						NamespacedName: types.NamespacedName{
							Name:      proxy.Name,
							Namespace: proxy.Namespace,
						},
					})
				}
			}

			return requests
		},
	)

	// Event handler for MCPRemoteProxy changes
	// Maps MCPRemoteProxy events → reconcile requests with groupRef filter
	proxyHandler := handler.EnqueueRequestsFromMapFunc(
		func(_ context.Context, obj client.Object) []reconcile.Request {
			proxy, ok := obj.(*mcpv1alpha1.MCPRemoteProxy)
			if !ok {
				return nil
			}

			// Only reconcile if matches groupRef (security + performance)
			if proxy.Spec.GroupRef != r.GroupRef {
				return nil
			}

			return []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{
						Name:      proxy.Name,
						Namespace: proxy.Namespace,
					},
				},
			}
		},
	)

	return ctrl.NewControllerManagedBy(mgr).
		Named("backend-reconciler").
		For(&mcpv1alpha1.MCPServer{}).                                            // Primary watch on MCPServer
		Watches(&mcpv1alpha1.MCPRemoteProxy{}, proxyHandler).                     // Secondary watch on MCPRemoteProxy
		Watches(&mcpv1alpha1.MCPExternalAuthConfig{}, externalAuthConfigHandler). // Watch auth configs
		Complete(r)
}

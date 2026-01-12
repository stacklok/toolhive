// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package k8s provides Kubernetes integration for Virtual MCP Server dynamic mode.
package k8s

import (
	"context"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
	"github.com/stacklok/toolhive/pkg/vmcp/workloads"
)

// BackendReconciler watches MCPServers and MCPRemoteProxies, converting them to
// vmcp.Backend and updating the DynamicRegistry when backends change.
//
// This reconciler is specifically designed for vMCP dynamic mode where backends
// can be added/removed without restarting the vMCP server. It filters backends
// by groupRef to only process workloads belonging to the configured MCPGroup.
//
// Namespace Scoping:
//   - Each BackendWatcher (and its reconciler) is scoped to a SINGLE namespace
//   - The controller-runtime manager is configured with DefaultNamespaces (single namespace)
//   - Backend IDs use name-only format (no namespace prefix) because namespace collisions are impossible
//   - This matches how the discoverer stores backends (ID = resource.Name)
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

	// HealthMonitor is the optional health monitor to notify of backend changes.
	// When set, the reconciler will call AddBackend/RemoveBackend to enable
	// dynamic health monitoring for backends discovered at runtime.
	// Nil if health monitoring is disabled.
	HealthMonitor *health.Monitor
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
func (r *BackendReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	// Fetch backend resource and determine type
	resourceInfo, err := r.fetchBackendResource(ctx, req.NamespacedName)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Resource deleted - remove from registry
	if resourceInfo == nil {
		return r.removeBackendFromRegistry(ctx, req.Name, "Resource deleted")
	}

	// GroupRef filtering: Only process backends belonging to our MCPGroup
	if resourceInfo.GroupRef != r.GroupRef {
		ctxLogger.V(1).Info(
			"Resource does not match groupRef, removing from registry",
			"backendID", req.Name,
			"resourceGroupRef", resourceInfo.GroupRef,
			"watcherGroupRef", r.GroupRef,
		)
		return r.removeBackendFromRegistry(ctx, req.Name, "GroupRef mismatch")
	}

	// Convert resource to vmcp.Backend and upsert to registry
	return r.convertAndUpsertBackend(ctx, req.Name, resourceInfo)
}

// backendResourceInfo holds information about a fetched backend resource
type backendResourceInfo struct {
	Name     string
	GroupRef string
	Type     workloads.WorkloadType
}

// fetchBackendResource attempts to fetch a resource as MCPServer or MCPRemoteProxy.
//
// Returns:
//   - (*backendResourceInfo, nil) if resource exists (MCPServer or MCPRemoteProxy)
//   - (nil, nil) if both resources are NotFound (resource deleted)
//   - (nil, error) if API error occurs (returns first non-NotFound error)
func (r *BackendReconciler) fetchBackendResource(
	ctx context.Context,
	namespacedName types.NamespacedName,
) (*backendResourceInfo, error) {
	ctxLogger := log.FromContext(ctx)

	// Try to fetch as MCPServer first
	mcpServer := &mcpv1alpha1.MCPServer{}
	errServer := r.Get(ctx, namespacedName, mcpServer)

	if errServer == nil {
		return &backendResourceInfo{
			Name:     mcpServer.Name,
			GroupRef: mcpServer.Spec.GroupRef,
			Type:     workloads.WorkloadTypeMCPServer,
		}, nil
	}

	// Try to fetch as MCPRemoteProxy
	mcpRemoteProxy := &mcpv1alpha1.MCPRemoteProxy{}
	errProxy := r.Get(ctx, namespacedName, mcpRemoteProxy)

	if errProxy == nil {
		return &backendResourceInfo{
			Name:     mcpRemoteProxy.Name,
			GroupRef: mcpRemoteProxy.Spec.GroupRef,
			Type:     workloads.WorkloadTypeMCPRemoteProxy,
		}, nil
	}

	// Both resources not found - resource deleted
	if errors.IsNotFound(errServer) && errors.IsNotFound(errProxy) {
		return nil, nil
	}

	// Return first non-NotFound error (prioritize real API errors)
	if errServer != nil && !errors.IsNotFound(errServer) {
		ctxLogger.Error(errServer, "Failed to get MCPServer")
		return nil, errServer
	}
	if errProxy != nil && !errors.IsNotFound(errProxy) {
		ctxLogger.Error(errProxy, "Failed to get MCPRemoteProxy")
		return nil, errProxy
	}

	// One is NotFound, the other is nil - should not happen in practice
	// Handle gracefully by treating as deleted
	return nil, nil
}

// removeBackendFromRegistry removes a backend from the registry with consistent logging.
// Safe to use name-only ID because BackendWatcher is namespace-scoped.
func (r *BackendReconciler) removeBackendFromRegistry(ctx context.Context, backendID, reason string) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)
	ctxLogger.Info("Removing backend from registry", "backendID", backendID, "reason", reason)

	if err := r.Registry.Remove(backendID); err != nil {
		ctxLogger.Error(err, "Failed to remove backend from registry")
		return ctrl.Result{}, err
	}

	// Notify health monitor if enabled
	if r.HealthMonitor != nil {
		if err := r.HealthMonitor.RemoveBackend(backendID); err != nil {
			ctxLogger.Error(err, "Failed to remove backend from health monitor")
			// Continue - registry removal succeeded, health monitor failure is not critical
		}
	}

	return ctrl.Result{}, nil
}

// convertAndUpsertBackend converts a backend resource to vmcp.Backend and upserts to registry.
func (r *BackendReconciler) convertAndUpsertBackend(
	ctx context.Context,
	backendID string,
	resourceInfo *backendResourceInfo,
) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	// Build TypedWorkload for discoverer
	workload := workloads.TypedWorkload{
		Name: resourceInfo.Name,
		Type: resourceInfo.Type,
	}

	// Convert to vmcp.Backend using discoverer (handles auth resolution, URL discovery)
	backend, err := r.Discoverer.GetWorkloadAsVMCPBackend(ctx, workload)
	if err != nil {
		ctxLogger.Error(err, "Failed to convert workload to backend", "workload", workload.Name)
		// Remove from registry if conversion fails (could be auth failure)
		// Ignore removal errors and return the original conversion error for requeue
		if removeErr := r.Registry.Remove(backendID); removeErr != nil {
			ctxLogger.Error(removeErr, "Failed to remove backend after conversion error")
		}
		return ctrl.Result{}, err
	}

	// backend is nil if auth resolution failed or workload not accessible
	// This is a security-critical check - we MUST NOT add backends without valid auth
	if backend == nil {
		ctxLogger.Info("Backend conversion returned nil (auth failure or no URL)", "backendID", backendID)
		return r.removeBackendFromRegistry(ctx, backendID, "Auth failure or no URL")
	}

	// Upsert backend to registry (triggers version increment + cache invalidation)
	if err := r.Registry.Upsert(*backend); err != nil {
		ctxLogger.Error(err, "Failed to upsert backend to registry", "backendID", backend.ID)
		return ctrl.Result{}, err
	}

	// Notify health monitor if enabled
	if r.HealthMonitor != nil {
		if err := r.HealthMonitor.AddBackend(*backend); err != nil {
			ctxLogger.Error(err, "Failed to add backend to health monitor", "backendID", backend.ID)
			// Continue - registry upsert succeeded, health monitor failure is not critical
		}
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
//   - MCPServers (secondary watch via Watches() with groupRef filtering)
//   - MCPRemoteProxies (mapped via event handler with groupRef filter)
//   - MCPExternalAuthConfigs (mapped to servers/proxies that reference them)
//
// Note: We use Watches() instead of For() for MCPServer because MCPServerReconciler
// is already the primary controller. Using For() in multiple controllers causes
// reconciliation conflicts and race conditions.
//
// The reconciler does NOT watch Secrets directly for performance reasons.
// Secrets change frequently for unrelated reasons (TLS certs, app configs, etc.).
// Auth updates will trigger via ExternalAuthConfig changes or pod restarts.
//
// Watch Design:
//  1. Watches(&MCPServer{}) - Secondary watch with groupRef filter
//  2. Watches(&MCPRemoteProxy{}) - Secondary watch with groupRef filter
//  3. Watches(&ExternalAuthConfig{}) - Maps to servers/proxies that reference it
//
// All watches are scoped to the reconciler's namespace (configured in BackendWatcher).
//
//nolint:gocyclo // Event handlers and watch setup require multiple conditional paths
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

	// Event handler for MCPServer changes
	// Maps MCPServer events → reconcile requests with groupRef filter
	serverHandler := handler.EnqueueRequestsFromMapFunc(
		func(_ context.Context, obj client.Object) []reconcile.Request {
			server, ok := obj.(*mcpv1alpha1.MCPServer)
			if !ok {
				return nil
			}

			// Only reconcile if matches groupRef (security + performance)
			if server.Spec.GroupRef != r.GroupRef {
				return nil
			}

			return []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{
						Name:      server.Name,
						Namespace: server.Namespace,
					},
				},
			}
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

	controllerName := "backend-reconciler-" + r.GroupRef
	return ctrl.NewControllerManagedBy(mgr).
		Named(controllerName).
		Watches(&mcpv1alpha1.MCPServer{}, serverHandler).                         // Watch MCPServer as secondary controller
		Watches(&mcpv1alpha1.MCPRemoteProxy{}, proxyHandler).                     // Watch MCPRemoteProxy
		Watches(&mcpv1alpha1.MCPExternalAuthConfig{}, externalAuthConfigHandler). // Watch auth configs
		Complete(r)
}

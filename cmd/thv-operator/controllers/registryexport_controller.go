// Package controllers provides Kubernetes controllers for the ToolHive operator.
package controllers

import (
	"context"
	"fmt"

	upstreamv0 "github.com/modelcontextprotocol/registry/pkg/api/v0"
	"github.com/modelcontextprotocol/registry/pkg/model"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/registryexport"
	"github.com/stacklok/toolhive/pkg/env"
)

// RegistryExportReconciler reconciles MCP resources to export them to the registry.
// It watches MCPServer, MCPRemoteProxy, and VirtualMCPServer resources for the
// registry-url annotation and aggregates them into a ConfigMap per namespace.
type RegistryExportReconciler struct {
	client.Client
	Generator    *registryexport.Generator
	ConfigMapMgr *registryexport.ConfigMapManager
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpservers,verbs=get;list;watch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpremoteproxies,verbs=get;list;watch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=virtualmcpservers,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile handles reconciliation for registry export.
// It is triggered by changes to any MCP resource in a namespace and aggregates
// all annotated resources into a single ConfigMap.
func (r *RegistryExportReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)
	ctxLogger.Info("Reconciling registry export", "namespace", req.Namespace)

	// Collect all exportable resources in the namespace
	entries, err := r.collectExportableResources(ctx, req.Namespace)
	if err != nil {
		ctxLogger.Error(err, "Failed to collect exportable resources")
		return ctrl.Result{}, err
	}

	// If no resources to export, delete the ConfigMap if it exists
	if len(entries) == 0 {
		ctxLogger.Info("No exportable resources found, deleting ConfigMap if exists")
		if err := r.ConfigMapMgr.DeleteConfigMap(ctx, req.Namespace); err != nil {
			ctxLogger.Error(err, "Failed to delete ConfigMap")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Build and update the registry ConfigMap
	registry := r.Generator.BuildUpstreamRegistry(entries)
	if err := r.ConfigMapMgr.UpsertConfigMap(ctx, req.Namespace, registry); err != nil {
		ctxLogger.Error(err, "Failed to upsert ConfigMap")
		return ctrl.Result{}, err
	}

	ctxLogger.Info("Successfully reconciled registry export", "entries", len(entries))
	return ctrl.Result{}, nil
}

// collectExportableResources collects all MCP resources with registry export annotations.
func (r *RegistryExportReconciler) collectExportableResources(
	ctx context.Context,
	namespace string,
) ([]upstreamv0.ServerJSON, error) {
	var entries []upstreamv0.ServerJSON

	// Collect from MCPServers
	mcpServers, err := r.collectMCPServers(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to collect MCPServers: %w", err)
	}
	entries = append(entries, mcpServers...)

	// Collect from MCPRemoteProxies
	mcpProxies, err := r.collectMCPRemoteProxies(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to collect MCPRemoteProxies: %w", err)
	}
	entries = append(entries, mcpProxies...)

	// Collect from VirtualMCPServers
	vmcpServers, err := r.collectVirtualMCPServers(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to collect VirtualMCPServers: %w", err)
	}
	entries = append(entries, vmcpServers...)

	return entries, nil
}

// collectMCPServers collects registry entries from MCPServer resources.
func (r *RegistryExportReconciler) collectMCPServers(
	ctx context.Context,
	namespace string,
) ([]upstreamv0.ServerJSON, error) {
	ctxLogger := log.FromContext(ctx)

	var servers mcpv1alpha1.MCPServerList
	if err := r.List(ctx, &servers, client.InNamespace(namespace)); err != nil {
		return nil, err
	}

	var entries []upstreamv0.ServerJSON
	for i := range servers.Items {
		server := &servers.Items[i]
		if !registryexport.HasRegistryExportAnnotation(server) {
			continue
		}

		// Determine external-facing transport for registry
		// If Transport is stdio, use ProxyMode (default: streamable-http)
		// Otherwise use Transport directly
		transport := server.Spec.Transport
		if transport == "" || transport == "stdio" {
			transport = server.Spec.ProxyMode
			if transport == "" {
				transport = model.TransportTypeStreamableHTTP
			}
		}

		entry, err := r.Generator.GenerateServerEntry(registryexport.ExportableResource{
			Object:    server,
			Transport: transport,
		})
		if err != nil {
			ctxLogger.Error(err, "Failed to generate entry for MCPServer", "name", server.Name)
			continue
		}
		if entry != nil {
			entries = append(entries, *entry)
		}
	}

	return entries, nil
}

// collectMCPRemoteProxies collects registry entries from MCPRemoteProxy resources.
func (r *RegistryExportReconciler) collectMCPRemoteProxies(
	ctx context.Context,
	namespace string,
) ([]upstreamv0.ServerJSON, error) {
	ctxLogger := log.FromContext(ctx)

	var proxies mcpv1alpha1.MCPRemoteProxyList
	if err := r.List(ctx, &proxies, client.InNamespace(namespace)); err != nil {
		return nil, err
	}

	var entries []upstreamv0.ServerJSON
	for i := range proxies.Items {
		proxy := &proxies.Items[i]
		if !registryexport.HasRegistryExportAnnotation(proxy) {
			continue
		}

		transport := proxy.Spec.Transport
		if transport == "" {
			transport = model.TransportTypeStreamableHTTP
		}

		entry, err := r.Generator.GenerateServerEntry(registryexport.ExportableResource{
			Object:    proxy,
			Transport: transport,
		})
		if err != nil {
			ctxLogger.Error(err, "Failed to generate entry for MCPRemoteProxy", "name", proxy.Name)
			continue
		}
		if entry != nil {
			entries = append(entries, *entry)
		}
	}

	return entries, nil
}

// collectVirtualMCPServers collects registry entries from VirtualMCPServer resources.
func (r *RegistryExportReconciler) collectVirtualMCPServers(
	ctx context.Context,
	namespace string,
) ([]upstreamv0.ServerJSON, error) {
	ctxLogger := log.FromContext(ctx)

	var vmcpServers mcpv1alpha1.VirtualMCPServerList
	if err := r.List(ctx, &vmcpServers, client.InNamespace(namespace)); err != nil {
		return nil, err
	}

	var entries []upstreamv0.ServerJSON
	for i := range vmcpServers.Items {
		vmcp := &vmcpServers.Items[i]
		if !registryexport.HasRegistryExportAnnotation(vmcp) {
			continue
		}

		// VirtualMCPServer uses streamable-http by default
		entry, err := r.Generator.GenerateServerEntry(registryexport.ExportableResource{
			Object:    vmcp,
			Transport: "streamable-http",
		})
		if err != nil {
			ctxLogger.Error(err, "Failed to generate entry for VirtualMCPServer", "name", vmcp.Name)
			continue
		}
		if entry != nil {
			entries = append(entries, *entry)
		}
	}

	return entries, nil
}

// mapMCPResourceToNamespace maps any MCP resource to a reconcile request for its namespace.
// This ensures that any change to an MCP resource triggers a full namespace reconciliation.
// We always trigger reconciliation to handle both annotation additions and removals.
func (*RegistryExportReconciler) mapMCPResourceToNamespace(
	_ context.Context,
	obj client.Object,
) []reconcile.Request {
	return []reconcile.Request{{
		NamespacedName: client.ObjectKey{
			Namespace: obj.GetNamespace(),
			Name:      obj.GetNamespace(),
		},
	}}
}

// SetupWithManager sets up the controller with the Manager.
func (r *RegistryExportReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Predicate to filter only registry export ConfigMaps
	registryExportConfigMapPredicate := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		labels := obj.GetLabels()
		return labels != nil && labels[registryexport.LabelRegistryExport] == registryexport.LabelRegistryExportValue
	})

	return ctrl.NewControllerManagedBy(mgr).
		// Watch only registry export ConfigMaps as the primary resource
		For(&corev1.ConfigMap{}, builder.WithPredicates(registryExportConfigMapPredicate)).
		Watches(
			&mcpv1alpha1.MCPServer{},
			handler.EnqueueRequestsFromMapFunc(r.mapMCPResourceToNamespace),
		).
		Watches(
			&mcpv1alpha1.MCPRemoteProxy{},
			handler.EnqueueRequestsFromMapFunc(r.mapMCPResourceToNamespace),
		).
		Watches(
			&mcpv1alpha1.VirtualMCPServer{},
			handler.EnqueueRequestsFromMapFunc(r.mapMCPResourceToNamespace),
		).
		Complete(r)
}

// IsRegistryExportEnabled checks if the registry export feature is enabled via environment variable.
func IsRegistryExportEnabled() bool {
	return IsRegistryExportEnabledWithEnv(&env.OSReader{})
}

// IsRegistryExportEnabledWithEnv checks if the registry export feature is enabled
// using a custom environment reader for testing.
func IsRegistryExportEnabledWithEnv(envReader env.Reader) bool {
	val := envReader.Getenv(registryexport.EnvEnableRegistryExport)
	return val == "true" || val == "1" || val == "yes"
}

// NewRegistryExportReconciler creates a new RegistryExportReconciler.
func NewRegistryExportReconciler(c client.Client) *RegistryExportReconciler {
	return &RegistryExportReconciler{
		Client:       c,
		Generator:    registryexport.NewGenerator(),
		ConfigMapMgr: registryexport.NewConfigMapManager(c),
	}
}

package controllers

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

const (
	// ToolConfigFinalizerName is the name of the finalizer for MCPToolConfig
	ToolConfigFinalizerName = "toolhive.stacklok.dev/toolconfig-finalizer"

	// finalizerRequeueDelay is the delay before requeuing after adding a finalizer
	finalizerRequeueDelay = 500 * time.Millisecond
)

// ToolConfigReconciler reconciles a MCPToolConfig object
type ToolConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcptoolconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcptoolconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcptoolconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpservers,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *ToolConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the MCPToolConfig instance
	toolConfig := &mcpv1alpha1.MCPToolConfig{}
	err := r.Get(ctx, req.NamespacedName, toolConfig)
	if err != nil {
		if errors.IsNotFound(err) {
			// Object not found, could have been deleted after reconcile request.
			// Return and don't requeue
			logger.Info("MCPToolConfig resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		logger.Error(err, "Failed to get MCPToolConfig")
		return ctrl.Result{}, err
	}

	// Check if the MCPToolConfig is being deleted
	if !toolConfig.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, toolConfig)
	}

	// Add finalizer if it doesn't exist
	if !controllerutil.ContainsFinalizer(toolConfig, ToolConfigFinalizerName) {
		controllerutil.AddFinalizer(toolConfig, ToolConfigFinalizerName)
		if err := r.Update(ctx, toolConfig); err != nil {
			logger.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
		// Requeue to continue processing after finalizer is added
		return ctrl.Result{RequeueAfter: finalizerRequeueDelay}, nil
	}

	// Calculate the hash of the current configuration
	configHash := r.calculateConfigHash(toolConfig.Spec)

	// Check if the hash has changed
	if toolConfig.Status.ConfigHash != configHash {
		logger.Info("MCPToolConfig configuration changed", "oldHash", toolConfig.Status.ConfigHash, "newHash", configHash)

		// Update the status with the new hash
		toolConfig.Status.ConfigHash = configHash
		toolConfig.Status.ObservedGeneration = toolConfig.Generation

		// Find all MCPServers that reference this MCPToolConfig
		referencingServers, err := r.findReferencingMCPServers(ctx, toolConfig)
		if err != nil {
			logger.Error(err, "Failed to find referencing MCPServers")
			return ctrl.Result{}, fmt.Errorf("failed to find referencing MCPServers: %w", err)
		}

		// Update the status with the list of referencing servers
		serverNames := make([]string, 0, len(referencingServers))
		for _, server := range referencingServers {
			serverNames = append(serverNames, server.Name)
		}
		toolConfig.Status.ReferencingServers = serverNames

		// Update the MCPToolConfig status
		if err := r.Status().Update(ctx, toolConfig); err != nil {
			logger.Error(err, "Failed to update MCPToolConfig status")
			return ctrl.Result{}, err
		}

		// Trigger reconciliation of all referencing MCPServers
		for _, server := range referencingServers {
			logger.Info("Triggering reconciliation of MCPServer due to MCPToolConfig change",
				"mcpserver", server.Name, "toolconfig", toolConfig.Name)

			// Add an annotation to the MCPServer to trigger reconciliation
			if server.Annotations == nil {
				server.Annotations = make(map[string]string)
			}
			server.Annotations["toolhive.stacklok.dev/toolconfig-hash"] = configHash

			if err := r.Update(ctx, &server); err != nil {
				logger.Error(err, "Failed to update MCPServer annotation", "mcpserver", server.Name)
				// Continue with other servers even if one fails
			}
		}
	}

	return ctrl.Result{}, nil
}

// calculateConfigHash calculates a hash of the MCPToolConfig spec using Kubernetes utilities
func (*ToolConfigReconciler) calculateConfigHash(spec mcpv1alpha1.MCPToolConfigSpec) string {
	return CalculateConfigHash(spec)
}

// handleDeletion handles the deletion of a MCPToolConfig
func (r *ToolConfigReconciler) handleDeletion(ctx context.Context, toolConfig *mcpv1alpha1.MCPToolConfig) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if controllerutil.ContainsFinalizer(toolConfig, ToolConfigFinalizerName) {
		// Check if any MCPServers are still referencing this MCPToolConfig
		referencingServers, err := r.findReferencingMCPServers(ctx, toolConfig)
		if err != nil {
			logger.Error(err, "Failed to find referencing MCPServers during deletion")
			return ctrl.Result{}, err
		}

		if len(referencingServers) > 0 {
			// Cannot delete - still referenced
			serverNames := make([]string, 0, len(referencingServers))
			for _, server := range referencingServers {
				serverNames = append(serverNames, server.Name)
			}
			logger.Info("Cannot delete MCPToolConfig - still referenced by MCPServers",
				"toolconfig", toolConfig.Name, "referencingServers", serverNames)

			// Update status to show it's still referenced
			toolConfig.Status.ReferencingServers = serverNames
			if err := r.Status().Update(ctx, toolConfig); err != nil {
				logger.Error(err, "Failed to update MCPToolConfig status during deletion")
			}

			// Return an error to prevent deletion
			return ctrl.Result{}, fmt.Errorf("MCPToolConfig %s is still referenced by MCPServers: %v",
				toolConfig.Name, serverNames)
		}

		// No references, safe to remove finalizer and allow deletion
		controllerutil.RemoveFinalizer(toolConfig, ToolConfigFinalizerName)
		if err := r.Update(ctx, toolConfig); err != nil {
			logger.Error(err, "Failed to remove finalizer")
			return ctrl.Result{}, err
		}
		logger.Info("Removed finalizer from MCPToolConfig", "toolconfig", toolConfig.Name)
	}

	return ctrl.Result{}, nil
}

// findReferencingMCPServers finds all MCPServers that reference the given MCPToolConfig
func (r *ToolConfigReconciler) findReferencingMCPServers(
	ctx context.Context,
	toolConfig *mcpv1alpha1.MCPToolConfig,
) ([]mcpv1alpha1.MCPServer, error) {
	return FindReferencingMCPServers(ctx, r.Client, toolConfig.Namespace, toolConfig.Name,
		func(server *mcpv1alpha1.MCPServer) *string {
			if server.Spec.ToolConfigRef != nil {
				return &server.Spec.ToolConfigRef.Name
			}
			return nil
		})
}

// SetupWithManager sets up the controller with the Manager.
func (r *ToolConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Create a handler that maps MCPToolConfig changes to MCPServer reconciliation requests
	toolConfigHandler := handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []reconcile.Request {
			toolConfig, ok := obj.(*mcpv1alpha1.MCPToolConfig)
			if !ok {
				return nil
			}

			// Find all MCPServers that reference this MCPToolConfig
			mcpServers, err := r.findReferencingMCPServers(ctx, toolConfig)
			if err != nil {
				log.FromContext(ctx).Error(err, "Failed to find referencing MCPServers")
				return nil
			}

			// Create reconcile requests for each referencing MCPServer
			requests := make([]reconcile.Request, 0, len(mcpServers))
			for _, server := range mcpServers {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      server.Name,
						Namespace: server.Namespace,
					},
				})
			}

			return requests
		},
	)

	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.MCPToolConfig{}).
		// Watch for MCPServers and reconcile the MCPToolConfig when they change
		Watches(&mcpv1alpha1.MCPServer{}, toolConfigHandler).
		Complete(r)
}

// GetToolConfigForMCPServer retrieves the MCPToolConfig referenced by an MCPServer
func GetToolConfigForMCPServer(
	ctx context.Context,
	c client.Client,
	mcpServer *mcpv1alpha1.MCPServer,
) (*mcpv1alpha1.MCPToolConfig, error) {
	if mcpServer.Spec.ToolConfigRef == nil {
		// We throw an error because in this case you assume there is a ToolConfig
		// but there isn't one referenced.
		return nil, fmt.Errorf("MCPServer %s does not reference a MCPToolConfig", mcpServer.Name)
	}

	toolConfig := &mcpv1alpha1.MCPToolConfig{}
	err := c.Get(ctx, types.NamespacedName{
		Name:      mcpServer.Spec.ToolConfigRef.Name,
		Namespace: mcpServer.Namespace, // Same namespace as MCPServer
	}, toolConfig)

	if err != nil {
		if errors.IsNotFound(err) {
			return nil, fmt.Errorf("MCPToolConfig %s not found in namespace %s",
				mcpServer.Spec.ToolConfigRef.Name, mcpServer.Namespace)
		}
		return nil, fmt.Errorf("failed to get MCPToolConfig: %w", err)
	}

	return toolConfig, nil
}

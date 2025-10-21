package controllers

import (
	"context"
	"sort"
	"time"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// MCPGroupFinalizerName is the name of the finalizer for MCPGroup
	MCPGroupFinalizerName = "toolhive.stacklok.dev/mcpgroup-finalizer"
)

type MCPGroupReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpgroups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpgroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpgroups/finalizers,verbs=update
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpservers,verbs=get;list;watch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpservers/status,verbs=get;update;patch

// Reconcile is part of the main kubernetes reconciliation loop
// which aims to move the current state of the cluster closer to the desired state.
func (r *MCPGroupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)
	ctxLogger.Info("Reconciling MCPGroup", "mcpgroup", req.NamespacedName)

	// Fetch the MCPGroup instance
	mcpGroup := &mcpv1alpha1.MCPGroup{}
	err := r.Get(ctx, req.NamespacedName, mcpGroup)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Return and don't requeue
			ctxLogger.Info("MCPGroup resource not found. Ignoring since object must be deleted.")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		ctxLogger.Error(err, "Failed to get MCPGroup", "mcpgroup", req.NamespacedName)
		return ctrl.Result{}, err
	}

	// Check if the MCPGroup is being deleted
	if !mcpGroup.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, mcpGroup)
	}

	// Add finalizer if it doesn't exist
	if !controllerutil.ContainsFinalizer(mcpGroup, MCPGroupFinalizerName) {
		controllerutil.AddFinalizer(mcpGroup, MCPGroupFinalizerName)
		if err := r.Update(ctx, mcpGroup); err != nil {
			ctxLogger.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
		// Requeue to continue processing after finalizer is added
		return ctrl.Result{RequeueAfter: 500 * time.Millisecond}, nil
	}

	// Find MCPServers that reference this MCPGroup
	mcpServers, err := r.findReferencingMCPServers(ctx, mcpGroup)
	if err != nil {
		ctxLogger.Error(err, "Failed to list MCPServers")
		mcpGroup.Status.Phase = mcpv1alpha1.MCPGroupPhaseFailed
		meta.SetStatusCondition(&mcpGroup.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionTypeMCPServersChecked,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1alpha1.ConditionReasonListMCPServersFailed,
			Message:            "Failed to list MCPServers in namespace",
			ObservedGeneration: mcpGroup.Generation,
		})
		mcpGroup.Status.ServerCount = 0
		mcpGroup.Status.Servers = nil
		// Update the MCPGroup status to reflect the failure
		if updateErr := r.Status().Update(ctx, mcpGroup); updateErr != nil {
			if errors.IsConflict(err) {
				// Requeue to retry with fresh data
				return ctrl.Result{Requeue: true}, nil
			}
			ctxLogger.Error(updateErr, "Failed to update MCPGroup status after list failure")
		}
		return ctrl.Result{}, nil
	}

	meta.SetStatusCondition(&mcpGroup.Status.Conditions, metav1.Condition{
		Type:               mcpv1alpha1.ConditionTypeMCPServersChecked,
		Status:             metav1.ConditionTrue,
		Reason:             mcpv1alpha1.ConditionReasonListMCPServersSucceeded,
		Message:            "Successfully listed MCPServers in namespace",
		ObservedGeneration: mcpGroup.Generation,
	})

	// Set MCPGroup status fields
	mcpGroup.Status.ServerCount = len(mcpServers)
	if len(mcpServers) == 0 {
		mcpGroup.Status.Servers = []string{}
	} else {
		// If there are servers, extract their names
		mcpGroup.Status.Servers = make([]string, len(mcpServers))
		for i, server := range mcpServers {
			mcpGroup.Status.Servers[i] = server.Name
		}
		sort.Strings(mcpGroup.Status.Servers)
	}

	mcpGroup.Status.Phase = mcpv1alpha1.MCPGroupPhaseReady

	// Update the MCPGroup status
	if err := r.Status().Update(ctx, mcpGroup); err != nil {
		if errors.IsConflict(err) {
			// Requeue to retry with fresh data
			return ctrl.Result{Requeue: true}, nil
		}
		ctxLogger.Error(err, "Failed to update MCPGroup status")
		return ctrl.Result{}, err
	}

	ctxLogger.Info("Successfully reconciled MCPGroup", "serverCount", mcpGroup.Status.ServerCount)
	return ctrl.Result{}, nil
}

// handleDeletion handles the deletion of an MCPGroup
func (r *MCPGroupReconciler) handleDeletion(ctx context.Context, mcpGroup *mcpv1alpha1.MCPGroup) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	if controllerutil.ContainsFinalizer(mcpGroup, MCPGroupFinalizerName) {
		// Find all MCPServers that reference this group
		referencingServers, err := r.findReferencingMCPServers(ctx, mcpGroup)
		if err != nil {
			ctxLogger.Error(err, "Failed to find referencing MCPServers during deletion")
			return ctrl.Result{}, err
		}

		// Update conditions on all referencing MCPServers to indicate the group is being deleted
		if len(referencingServers) > 0 {
			ctxLogger.Info("Updating conditions on referencing MCPServers", "count", len(referencingServers))
			if err := r.updateReferencingServersOnDeletion(ctx, referencingServers, mcpGroup.Name); err != nil {
				ctxLogger.Error(err, "Failed to update referencing MCPServers")
				// Continue with deletion even if update fails - this is best effort
			}
		}

		// Remove the finalizer to allow deletion
		controllerutil.RemoveFinalizer(mcpGroup, MCPGroupFinalizerName)
		if err := r.Update(ctx, mcpGroup); err != nil {
			if errors.IsConflict(err) {
				// Requeue to retry with fresh data
				return ctrl.Result{Requeue: true}, nil
			}
			ctxLogger.Error(err, "Failed to remove finalizer")
			return ctrl.Result{}, err
		}
		ctxLogger.Info("Removed finalizer from MCPGroup", "mcpgroup", mcpGroup.Name)
	}

	return ctrl.Result{}, nil
}

// findReferencingMCPServers finds all MCPServers that reference the given MCPGroup
func (r *MCPGroupReconciler) findReferencingMCPServers(ctx context.Context, mcpGroup *mcpv1alpha1.MCPGroup) ([]mcpv1alpha1.MCPServer, error) {
	mcpServerList := &mcpv1alpha1.MCPServerList{}
	listOpts := []client.ListOption{
		client.InNamespace(mcpGroup.Namespace),
		client.MatchingFields{"spec.groupRef": mcpGroup.Name},
	}
	if err := r.List(ctx, mcpServerList, listOpts...); err != nil {
		return nil, err
	}

	return mcpServerList.Items, nil
}

// updateReferencingServersOnDeletion updates the conditions on MCPServers to indicate their group is being deleted
func (r *MCPGroupReconciler) updateReferencingServersOnDeletion(ctx context.Context, servers []mcpv1alpha1.MCPServer, groupName string) error {
	ctxLogger := log.FromContext(ctx)

	for _, server := range servers {
		// Update the condition to indicate the group is being deleted
		meta.SetStatusCondition(&server.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionGroupRefValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1alpha1.ConditionReasonGroupRefNotFound,
			Message:            "Referenced MCPGroup is being deleted",
			ObservedGeneration: server.Generation,
		})

		// Update the server status
		if err := r.Status().Update(ctx, &server); err != nil {
			ctxLogger.Error(err, "Failed to update MCPServer condition during group deletion",
				"mcpserver", server.Name, "mcpgroup", groupName)
			// Continue with other servers even if one fails
			continue
		}
		ctxLogger.Info("Updated MCPServer condition for group deletion",
			"mcpserver", server.Name, "mcpgroup", groupName)
	}

	return nil
}

func (r *MCPGroupReconciler) findMCPGroupForMCPServer(ctx context.Context, obj client.Object) []ctrl.Request {
	ctxLogger := log.FromContext(ctx)

	// Get the MCPServer object
	mcpServer, ok := obj.(*mcpv1alpha1.MCPServer)
	if !ok {
		ctxLogger.Error(nil, "Object is not an MCPServer", "object", obj.GetName())
		return []ctrl.Request{}
	}
	if mcpServer.Spec.GroupRef == "" {
		// No MCPGroup reference, nothing to do
		return []ctrl.Request{}
	}

	// Find which MCPGroup this MCPServer belongs to
	ctxLogger.Info("Finding MCPGroup for MCPServer", "namespace", obj.GetNamespace(), "mcpserver", obj.GetName(), "groupRef", mcpServer.Spec.GroupRef)
	group := &mcpv1alpha1.MCPGroup{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: obj.GetNamespace(), Name: mcpServer.Spec.GroupRef}, group); err != nil {
		ctxLogger.Error(err, "Failed to get MCPGroup for MCPServer", "namespace", obj.GetNamespace(), "name", mcpServer.Spec.GroupRef)
		return []ctrl.Request{}
	}
	return []ctrl.Request{
		{
			NamespacedName: types.NamespacedName{
				Namespace: obj.GetNamespace(),
				Name:      group.Name,
			},
		},
	}
}

func (r *MCPGroupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.MCPGroup{}).
		Watches(
			&mcpv1alpha1.MCPServer{}, handler.EnqueueRequestsFromMapFunc(r.findMCPGroupForMCPServer),
		).
		Complete(r)
}

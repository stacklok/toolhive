package controllers

import (
	"context"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type MCPGroupReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpgroups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpgroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpgroups/finalizers,verbs=update
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpservers,verbs=get;list;watch

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

	// List all MCPServers in the same namespace
	mcpServerList := &mcpv1alpha1.MCPServerList{}
	if err := r.List(ctx, mcpServerList, client.InNamespace(req.Namespace)); err != nil {
		ctxLogger.Error(err, "Failed to list MCPServers")
		mcpGroup.Status.Phase = mcpv1alpha1.MCPGroupPhaseFailed
		meta.SetStatusCondition(&mcpGroup.Status.Conditions, metav1.Condition{
			Type:    mcpv1alpha1.ConditionTypeMCPServersChecked,
			Status:  metav1.ConditionFalse,
			Reason:  mcpv1alpha1.ConditionReasonListMCPServersFailed,
			Message: "Failed to list MCPServers in namespace",
		})
		mcpGroup.Status.ServerCount = 0
		mcpGroup.Status.Servers = nil
		// Update the MCPGroup status to reflect the failure
		if updateErr := r.Status().Update(ctx, mcpGroup); updateErr != nil {
			ctxLogger.Error(updateErr, "Failed to update MCPGroup status after list failure")
		}
		return ctrl.Result{}, nil
	} else {
		meta.SetStatusCondition(&mcpGroup.Status.Conditions, metav1.Condition{
			Type:    mcpv1alpha1.ConditionTypeMCPServersChecked,
			Status:  metav1.ConditionTrue,
			Reason:  mcpv1alpha1.ConditionReasonListMCPServersSucceeded,
			Message: "Successfully listed MCPServers in namespace",
		})
	}

	// Filter servers that belong to this group
	filteredServers := []mcpv1alpha1.MCPServer{}
	for _, server := range mcpServerList.Items {
		if server.Spec.GroupRef == mcpGroup.Name {
			filteredServers = append(filteredServers, server)
		}
	}

	// Set server count and names
	mcpGroup.Status.ServerCount = len(filteredServers)
	if len(filteredServers) == 0 {
		// Ensure servers is an empty slice, not nil
		mcpGroup.Status.Servers = []string{}
	} else {
		mcpGroup.Status.Servers = make([]string, len(filteredServers))
		for i, server := range filteredServers {
			mcpGroup.Status.Servers[i] = server.Name
		}
	}

	// Set status conditions
	mcpGroup.Status.Phase = mcpv1alpha1.MCPGroupPhaseReady

	// Update the MCPGroup status
	if err := r.Status().Update(ctx, mcpGroup); err != nil {
		ctxLogger.Error(err, "Failed to update MCPGroup status")
		return ctrl.Result{}, err
	}

	ctxLogger.Info("Successfully reconciled MCPGroup", "serverCount", mcpGroup.Status.ServerCount)
	return ctrl.Result{}, nil
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

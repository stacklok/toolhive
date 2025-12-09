package controllers

import (
	"context"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

const (
	// MCPGroupFinalizerName is the name of the finalizer for MCPGroup
	MCPGroupFinalizerName = "toolhive.stacklok.dev/mcpgroup-finalizer"
)

// MCPGroupReconciler reconciles a MCPGroup object
type MCPGroupReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpgroups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpgroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpgroups/finalizers,verbs=update
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpservers,verbs=get;list;watch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpservers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpremoteproxies,verbs=get;list;watch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpremoteproxies/status,verbs=get;update;patch

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

	// Find and update status for MCPServers and MCPRemoteProxies
	result, err := r.updateGroupMemberStatus(ctx, mcpGroup)
	if err != nil || result.RequeueAfter > 0 {
		return result, err
	}

	ctxLogger.Info("Successfully reconciled MCPGroup",
		"serverCount", mcpGroup.Status.ServerCount,
		"remoteProxyCount", mcpGroup.Status.RemoteProxyCount)
	return ctrl.Result{}, nil
}

// updateGroupMemberStatus finds MCPServers and MCPRemoteProxies referencing the group
// and updates the MCPGroup status accordingly.
func (r *MCPGroupReconciler) updateGroupMemberStatus(
	ctx context.Context,
	mcpGroup *mcpv1alpha1.MCPGroup,
) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	// Find MCPServers that reference this MCPGroup
	mcpServers, err := r.findReferencingMCPServers(ctx, mcpGroup)
	if err != nil {
		return r.handleListFailure(ctx, mcpGroup, err, "MCPServers")
	}

	// Find MCPRemoteProxies that reference this MCPGroup
	mcpRemoteProxies, err := r.findReferencingMCPRemoteProxies(ctx, mcpGroup)
	if err != nil {
		return r.handleListFailure(ctx, mcpGroup, err, "MCPRemoteProxies")
	}

	meta.SetStatusCondition(&mcpGroup.Status.Conditions, metav1.Condition{
		Type:               mcpv1alpha1.ConditionTypeMCPServersChecked,
		Status:             metav1.ConditionTrue,
		Reason:             mcpv1alpha1.ConditionReasonListMCPServersSucceeded,
		Message:            "Successfully listed MCPServers and MCPRemoteProxies in namespace",
		ObservedGeneration: mcpGroup.Generation,
	})

	// Set MCPGroup status fields for MCPServers
	r.populateServerStatus(mcpGroup, mcpServers)

	// Set MCPGroup status fields for MCPRemoteProxies
	r.populateRemoteProxyStatus(mcpGroup, mcpRemoteProxies)

	mcpGroup.Status.Phase = mcpv1alpha1.MCPGroupPhaseReady

	// Update the MCPGroup status
	if err := r.Status().Update(ctx, mcpGroup); err != nil {
		if errors.IsConflict(err) {
			return ctrl.Result{RequeueAfter: 500 * time.Millisecond}, nil
		}
		ctxLogger.Error(err, "Failed to update MCPGroup status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// handleListFailure handles the case when listing MCPServers or MCPRemoteProxies fails.
func (r *MCPGroupReconciler) handleListFailure(
	ctx context.Context,
	mcpGroup *mcpv1alpha1.MCPGroup,
	listErr error,
	resourceType string,
) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)
	ctxLogger.Error(listErr, "Failed to list "+resourceType)

	mcpGroup.Status.Phase = mcpv1alpha1.MCPGroupPhaseFailed
	meta.SetStatusCondition(&mcpGroup.Status.Conditions, metav1.Condition{
		Type:               mcpv1alpha1.ConditionTypeMCPServersChecked,
		Status:             metav1.ConditionFalse,
		Reason:             mcpv1alpha1.ConditionReasonListMCPServersFailed,
		Message:            "Failed to list " + resourceType + " in namespace",
		ObservedGeneration: mcpGroup.Generation,
	})

	if resourceType == "MCPServers" {
		mcpGroup.Status.ServerCount = 0
		mcpGroup.Status.Servers = nil
	} else {
		mcpGroup.Status.RemoteProxyCount = 0
		mcpGroup.Status.RemoteProxies = nil
	}

	if updateErr := r.Status().Update(ctx, mcpGroup); updateErr != nil {
		if errors.IsConflict(updateErr) {
			return ctrl.Result{RequeueAfter: 500 * time.Millisecond}, nil
		}
		ctxLogger.Error(updateErr, "Failed to update MCPGroup status after list failure")
	}
	return ctrl.Result{}, nil
}

// populateServerStatus populates the MCPGroup status with MCPServer information.
func (*MCPGroupReconciler) populateServerStatus(
	mcpGroup *mcpv1alpha1.MCPGroup,
	mcpServers []mcpv1alpha1.MCPServer,
) {
	mcpGroup.Status.ServerCount = len(mcpServers)
	if len(mcpServers) == 0 {
		mcpGroup.Status.Servers = []string{}
		return
	}
	mcpGroup.Status.Servers = make([]string, len(mcpServers))
	for i, server := range mcpServers {
		mcpGroup.Status.Servers[i] = server.Name
	}
	sort.Strings(mcpGroup.Status.Servers)
}

// populateRemoteProxyStatus populates the MCPGroup status with MCPRemoteProxy information.
func (*MCPGroupReconciler) populateRemoteProxyStatus(
	mcpGroup *mcpv1alpha1.MCPGroup,
	mcpRemoteProxies []mcpv1alpha1.MCPRemoteProxy,
) {
	mcpGroup.Status.RemoteProxyCount = len(mcpRemoteProxies)
	if len(mcpRemoteProxies) == 0 {
		mcpGroup.Status.RemoteProxies = []string{}
		return
	}
	mcpGroup.Status.RemoteProxies = make([]string, len(mcpRemoteProxies))
	for i, proxy := range mcpRemoteProxies {
		mcpGroup.Status.RemoteProxies[i] = proxy.Name
	}
	sort.Strings(mcpGroup.Status.RemoteProxies)
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
			r.updateReferencingServersOnDeletion(ctx, referencingServers, mcpGroup.Name)
		}

		// Find all MCPRemoteProxies that reference this group
		referencingProxies, err := r.findReferencingMCPRemoteProxies(ctx, mcpGroup)
		if err != nil {
			ctxLogger.Error(err, "Failed to find referencing MCPRemoteProxies during deletion")
			return ctrl.Result{}, err
		}

		// Update conditions on all referencing MCPRemoteProxies to indicate the group is being deleted
		if len(referencingProxies) > 0 {
			ctxLogger.Info("Updating conditions on referencing MCPRemoteProxies", "count", len(referencingProxies))
			r.updateReferencingRemoteProxiesOnDeletion(ctx, referencingProxies, mcpGroup.Name)
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
func (r *MCPGroupReconciler) findReferencingMCPServers(
	ctx context.Context, mcpGroup *mcpv1alpha1.MCPGroup) ([]mcpv1alpha1.MCPServer, error) {

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

// findReferencingMCPRemoteProxies finds all MCPRemoteProxies that reference the given MCPGroup
func (r *MCPGroupReconciler) findReferencingMCPRemoteProxies(
	ctx context.Context, mcpGroup *mcpv1alpha1.MCPGroup) ([]mcpv1alpha1.MCPRemoteProxy, error) {

	mcpRemoteProxyList := &mcpv1alpha1.MCPRemoteProxyList{}
	listOpts := []client.ListOption{
		client.InNamespace(mcpGroup.Namespace),
		client.MatchingFields{"spec.groupRef": mcpGroup.Name},
	}
	if err := r.List(ctx, mcpRemoteProxyList, listOpts...); err != nil {
		return nil, err
	}

	return mcpRemoteProxyList.Items, nil
}

// updateReferencingServersOnDeletion updates the conditions on MCPServers to indicate their group is being deleted
func (r *MCPGroupReconciler) updateReferencingServersOnDeletion(
	ctx context.Context, servers []mcpv1alpha1.MCPServer, groupName string) {
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
}

// updateReferencingRemoteProxiesOnDeletion updates the conditions on MCPRemoteProxies to indicate their group is being deleted
func (r *MCPGroupReconciler) updateReferencingRemoteProxiesOnDeletion(
	ctx context.Context, proxies []mcpv1alpha1.MCPRemoteProxy, groupName string) {
	ctxLogger := log.FromContext(ctx)

	for _, proxy := range proxies {
		// Update the condition to indicate the group is being deleted
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionTypeMCPRemoteProxyGroupRefValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1alpha1.ConditionReasonMCPRemoteProxyGroupRefNotFound,
			Message:            "Referenced MCPGroup is being deleted",
			ObservedGeneration: proxy.Generation,
		})

		// Update the proxy status
		if err := r.Status().Update(ctx, &proxy); err != nil {
			ctxLogger.Error(err, "Failed to update MCPRemoteProxy condition during group deletion",
				"mcpremoteproxy", proxy.Name, "mcpgroup", groupName)
			// Continue with other proxies even if one fails
			continue
		}
		ctxLogger.Info("Updated MCPRemoteProxy condition for group deletion",
			"mcpremoteproxy", proxy.Name, "mcpgroup", groupName)
	}
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
	ctxLogger.Info(
		"Finding MCPGroup for MCPServer",
		"namespace",
		obj.GetNamespace(),
		"mcpserver",
		obj.GetName(),
		"groupRef",
		mcpServer.Spec.GroupRef)
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

func (r *MCPGroupReconciler) findMCPGroupForMCPRemoteProxy(ctx context.Context, obj client.Object) []ctrl.Request {
	ctxLogger := log.FromContext(ctx)

	// Get the MCPRemoteProxy object
	mcpRemoteProxy, ok := obj.(*mcpv1alpha1.MCPRemoteProxy)
	if !ok {
		ctxLogger.Error(nil, "Object is not an MCPRemoteProxy", "object", obj.GetName())
		return []ctrl.Request{}
	}
	if mcpRemoteProxy.Spec.GroupRef == "" {
		// No MCPGroup reference, nothing to do
		return []ctrl.Request{}
	}

	// Find which MCPGroup this MCPRemoteProxy belongs to
	ctxLogger.Info(
		"Finding MCPGroup for MCPRemoteProxy",
		"namespace",
		obj.GetNamespace(),
		"mcpremoteproxy",
		obj.GetName(),
		"groupRef",
		mcpRemoteProxy.Spec.GroupRef)
	group := &mcpv1alpha1.MCPGroup{}
	groupKey := types.NamespacedName{Namespace: obj.GetNamespace(), Name: mcpRemoteProxy.Spec.GroupRef}
	if err := r.Get(ctx, groupKey, group); err != nil {
		ctxLogger.Error(err, "Failed to get MCPGroup for MCPRemoteProxy",
			"namespace", obj.GetNamespace(), "name", mcpRemoteProxy.Spec.GroupRef)
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

// SetupWithManager sets up the controller with the Manager.
func (r *MCPGroupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.MCPGroup{}).
		Watches(
			&mcpv1alpha1.MCPServer{}, handler.EnqueueRequestsFromMapFunc(r.findMCPGroupForMCPServer),
		).
		Watches(
			&mcpv1alpha1.MCPRemoteProxy{}, handler.EnqueueRequestsFromMapFunc(r.findMCPGroupForMCPRemoteProxy),
		).
		Complete(r)
}

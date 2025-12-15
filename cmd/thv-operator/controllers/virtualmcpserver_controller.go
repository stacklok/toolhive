// Package controllers contains the reconciliation logic for the VirtualMCPServer custom resource.
// It handles the creation, update, and deletion of Virtual MCP Servers in Kubernetes.
package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/virtualmcpserverstatus"
	"github.com/stacklok/toolhive/pkg/groups"
	vmcptypes "github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/converters"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/workloads"
)

const (
	// OutgoingAuthSourceDiscovered indicates that auth configs should be automatically discovered from MCPServers
	OutgoingAuthSourceDiscovered = "discovered"
	// OutgoingAuthSourceInline indicates that auth configs should be explicitly specified
	OutgoingAuthSourceInline = "inline"
)

// VirtualMCPServerReconciler reconciles a VirtualMCPServer object
//
// Resource Cleanup Strategy:
// VirtualMCPServer does NOT use finalizers because all managed resources have owner references
// set via controllerutil.SetControllerReference. Kubernetes automatically cascade-deletes
// owned resources when the VirtualMCPServer is deleted. Managed resources include:
//   - Deployment (owned)
//   - Service (owned)
//   - ConfigMap for vmcp config (owned)
//   - ServiceAccount, Role, RoleBinding via ctrlutil.EnsureRBACResource (owned)
//
// This differs from MCPServer which uses finalizers to explicitly delete resources that
// may not have owner references (StatefulSet, headless Service, RunConfig ConfigMap).
type VirtualMCPServerReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Recorder         record.EventRecorder
	PlatformDetector *ctrlutil.SharedPlatformDetector
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=virtualmcpservers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=virtualmcpservers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpgroups,verbs=get;list;watch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpservers,verbs=get;list;watch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpexternalauthconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcptoolconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=virtualmcpcompositetooldefinitions,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups="",resources=services,verbs=create;delete;get;list;patch;update;watch;apply
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=roles,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=rolebindings,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=create;delete;get;list;patch;update;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *VirtualMCPServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	// Fetch the VirtualMCPServer instance
	vmcp := &mcpv1alpha1.VirtualMCPServer{}
	err := r.Get(ctx, req.NamespacedName, vmcp)
	if err != nil {
		if errors.IsNotFound(err) {
			ctxLogger.Info("VirtualMCPServer resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		ctxLogger.Error(err, "Failed to get VirtualMCPServer")
		return ctrl.Result{}, err
	}

	// Create status manager for batched updates
	statusManager := virtualmcpserverstatus.NewStatusManager(vmcp)

	// Validate PodTemplateSpec early - before other validations
	if !r.validateAndUpdatePodTemplateStatus(ctx, vmcp, statusManager) {
		// Invalid PodTemplateSpec - apply status updates and return without error to avoid infinite retries
		// The user must fix the spec and the next reconciliation will retry
		if err := r.applyStatusUpdates(ctx, vmcp, statusManager); err != nil {
			ctxLogger.Error(err, "Failed to apply status updates after PodTemplateSpec validation error")
		}
		return ctrl.Result{}, nil
	}

	// Validate GroupRef
	if err := r.validateGroupRef(ctx, vmcp, statusManager); err != nil {
		// Apply status changes before returning error
		if err := r.applyStatusUpdates(ctx, vmcp, statusManager); err != nil {
			ctxLogger.Error(err, "Failed to apply status updates after GroupRef validation error")
		}
		return ctrl.Result{}, err
	}

	// Validate CompositeToolRefs
	if err := r.validateCompositeToolRefs(ctx, vmcp, statusManager); err != nil {
		// Apply status changes before returning error
		if err := r.applyStatusUpdates(ctx, vmcp, statusManager); err != nil {
			ctxLogger.Error(err, "Failed to apply status updates after CompositeToolRefs validation error")
		}
		return ctrl.Result{}, err
	}

	// Ensure all resources
	if err := r.ensureAllResources(ctx, vmcp, statusManager); err != nil {
		// Apply status changes before returning error
		if err := r.applyStatusUpdates(ctx, vmcp, statusManager); err != nil {
			ctxLogger.Error(err, "Failed to apply status updates after resource reconciliation error")
		}
		return ctrl.Result{}, err
	}

	// Discover backends from the MCPGroup
	discoveredBackends, err := r.discoverBackends(ctx, vmcp)
	if err != nil {
		ctxLogger.Error(err, "Failed to discover backends")
		// Don't fail reconciliation if backend discovery fails, but log the error
		statusManager.SetCondition(
			mcpv1alpha1.ConditionTypeVirtualMCPServerBackendsDiscovered,
			mcpv1alpha1.ConditionReasonVirtualMCPServerBackendDiscoveryFailed,
			fmt.Sprintf("Failed to discover backends: %v", err),
			metav1.ConditionFalse,
		)
		statusManager.SetObservedGeneration(vmcp.Generation)
	} else {
		statusManager.SetDiscoveredBackends(discoveredBackends)
		statusManager.SetCondition(
			mcpv1alpha1.ConditionTypeVirtualMCPServerBackendsDiscovered,
			mcpv1alpha1.ConditionReasonVirtualMCPServerBackendsDiscoveredSuccessfully,
			fmt.Sprintf("Discovered %d backends", len(discoveredBackends)),
			metav1.ConditionTrue,
		)
		statusManager.SetObservedGeneration(vmcp.Generation)
		ctxLogger.Info("Discovered backends", "count", len(discoveredBackends))
	}

	// Fetch the latest version before updating status to ensure we use the current Generation
	latestVMCP := &mcpv1alpha1.VirtualMCPServer{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      vmcp.Name,
		Namespace: vmcp.Namespace,
	}, latestVMCP); err != nil {
		ctxLogger.Error(err, "Failed to get latest VirtualMCPServer before status update")
		return ctrl.Result{}, err
	}

	// Update status based on pod health using the latest Generation
	if err := r.updateVirtualMCPServerStatus(ctx, latestVMCP, statusManager); err != nil {
		ctxLogger.Error(err, "Failed to update VirtualMCPServer status")
		return ctrl.Result{}, err
	}

	// Apply all collected status changes in a single batch update
	if err := r.applyStatusUpdates(ctx, latestVMCP, statusManager); err != nil {
		ctxLogger.Error(err, "Failed to apply final status updates")
		return ctrl.Result{}, err
	}

	// Reconciliation complete - rely on event-driven reconciliation
	// Kubernetes will automatically trigger reconcile when:
	// - VirtualMCPServer spec changes
	// - Referenced resources (MCPGroup, Secrets) change
	// - Owned resources (Deployment, Service) status changes
	// - vmcp pods emit events about backend health
	return ctrl.Result{}, nil
}

// applyStatusUpdates applies all collected status changes in a single batch update.
// This implements the StatusCollector pattern to reduce API calls and prevent update conflicts.
func (r *VirtualMCPServerReconciler) applyStatusUpdates(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	statusManager virtualmcpserverstatus.StatusManager,
) error {
	ctxLogger := log.FromContext(ctx)

	// Fetch the latest version to avoid conflicts
	latest := &mcpv1alpha1.VirtualMCPServer{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      vmcp.Name,
		Namespace: vmcp.Namespace,
	}, latest); err != nil {
		return fmt.Errorf("failed to get latest VirtualMCPServer: %w", err)
	}

	// Apply collected changes to the latest status
	hasUpdates := statusManager.UpdateStatus(ctx, &latest.Status)

	// Only update if there are changes
	if hasUpdates {
		if err := r.Status().Update(ctx, latest); err != nil {
			// Handle conflicts by returning error to trigger requeue
			if errors.IsConflict(err) {
				ctxLogger.V(1).Info("Conflict updating status, will requeue")
				return err
			}
			return fmt.Errorf("failed to update VirtualMCPServer status: %w", err)
		}
		ctxLogger.V(1).Info("Successfully applied batched status updates")
	}

	return nil
}

// validateGroupRef validates that the referenced MCPGroup exists and is ready
func (r *VirtualMCPServerReconciler) validateGroupRef(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	statusManager virtualmcpserverstatus.StatusManager,
) error {
	ctxLogger := log.FromContext(ctx)

	// Validate GroupRef exists
	mcpGroup := &mcpv1alpha1.MCPGroup{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      vmcp.Spec.GroupRef.Name,
		Namespace: vmcp.Namespace,
	}, mcpGroup)

	if errors.IsNotFound(err) {
		message := fmt.Sprintf("Referenced MCPGroup %s not found", vmcp.Spec.GroupRef.Name)
		statusManager.SetPhase(mcpv1alpha1.VirtualMCPServerPhaseFailed)
		statusManager.SetMessage(message)
		statusManager.SetGroupRefValidatedCondition(
			mcpv1alpha1.ConditionReasonVirtualMCPServerGroupRefNotFound,
			message,
			metav1.ConditionFalse,
		)
		statusManager.SetObservedGeneration(vmcp.Generation)
		return err
	} else if err != nil {
		ctxLogger.Error(err, "Failed to get MCPGroup")
		return err
	}

	// Check if MCPGroup is ready
	if mcpGroup.Status.Phase != mcpv1alpha1.MCPGroupPhaseReady {
		message := fmt.Sprintf("Referenced MCPGroup %s is not ready (phase: %s)",
			vmcp.Spec.GroupRef.Name, mcpGroup.Status.Phase)
		statusManager.SetPhase(mcpv1alpha1.VirtualMCPServerPhasePending)
		statusManager.SetMessage(message)
		statusManager.SetGroupRefValidatedCondition(
			mcpv1alpha1.ConditionReasonVirtualMCPServerGroupRefNotReady,
			message,
			metav1.ConditionFalse,
		)
		statusManager.SetObservedGeneration(vmcp.Generation)
		// Requeue to check again later
		return fmt.Errorf("MCPGroup %s is not ready", vmcp.Spec.GroupRef.Name)
	}

	// GroupRef is valid and ready
	statusManager.SetGroupRefValidatedCondition(
		mcpv1alpha1.ConditionReasonVirtualMCPServerGroupRefValid,
		fmt.Sprintf("MCPGroup %s is valid and ready", vmcp.Spec.GroupRef.Name),
		metav1.ConditionTrue,
	)
	statusManager.SetObservedGeneration(vmcp.Generation)

	return nil
}

// validateCompositeToolRefs validates that all referenced VirtualMCPCompositeToolDefinition resources exist
func (r *VirtualMCPServerReconciler) validateCompositeToolRefs(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	statusManager virtualmcpserverstatus.StatusManager,
) error {
	ctxLogger := log.FromContext(ctx)

	// If no composite tool refs, nothing to validate
	if len(vmcp.Spec.CompositeToolRefs) == 0 {
		// Set condition to indicate validation passed (no refs to validate)
		statusManager.SetCompositeToolRefsValidatedCondition(
			mcpv1alpha1.ConditionReasonCompositeToolRefsValid,
			"No composite tool references to validate",
			metav1.ConditionTrue,
		)
		return nil
	}

	// Validate each referenced composite tool definition exists
	for _, ref := range vmcp.Spec.CompositeToolRefs {
		compositeToolDef := &mcpv1alpha1.VirtualMCPCompositeToolDefinition{}
		err := r.Get(ctx, types.NamespacedName{
			Name:      ref.Name,
			Namespace: vmcp.Namespace,
		}, compositeToolDef)

		if errors.IsNotFound(err) {
			message := fmt.Sprintf("Referenced VirtualMCPCompositeToolDefinition %s not found", ref.Name)
			statusManager.SetPhase(mcpv1alpha1.VirtualMCPServerPhaseFailed)
			statusManager.SetMessage(message)
			statusManager.SetCompositeToolRefsValidatedCondition(
				mcpv1alpha1.ConditionReasonCompositeToolRefNotFound,
				message,
				metav1.ConditionFalse,
			)
			return err
		} else if err != nil {
			ctxLogger.Error(err, "Failed to get VirtualMCPCompositeToolDefinition", "name", ref.Name)
			return err
		}

		// Check that the composite tool definition is validated and valid
		if compositeToolDef.Status.ValidationStatus == mcpv1alpha1.ValidationStatusInvalid {
			message := fmt.Sprintf("Referenced VirtualMCPCompositeToolDefinition %s is invalid", ref.Name)
			if len(compositeToolDef.Status.ValidationErrors) > 0 {
				message = fmt.Sprintf("%s: %s", message, strings.Join(compositeToolDef.Status.ValidationErrors, "; "))
			}
			statusManager.SetPhase(mcpv1alpha1.VirtualMCPServerPhaseFailed)
			statusManager.SetMessage(message)
			statusManager.SetCompositeToolRefsValidatedCondition(
				mcpv1alpha1.ConditionReasonCompositeToolRefInvalid,
				message,
				metav1.ConditionFalse,
			)
			return fmt.Errorf("referenced VirtualMCPCompositeToolDefinition %s is invalid", ref.Name)
		}

		// If ValidationStatus is Unknown, we still allow it (validation might be in progress)
		// but log a warning
		if compositeToolDef.Status.ValidationStatus == mcpv1alpha1.ValidationStatusUnknown {
			ctxLogger.V(1).Info("Referenced composite tool definition validation status is Unknown, proceeding",
				"name", ref.Name, "namespace", vmcp.Namespace)
		}
	}

	// All composite tool refs are valid
	statusManager.SetCompositeToolRefsValidatedCondition(
		mcpv1alpha1.ConditionReasonCompositeToolRefsValid,
		fmt.Sprintf("All %d composite tool references are valid", len(vmcp.Spec.CompositeToolRefs)),
		metav1.ConditionTrue,
	)

	return nil
}

// validateAndUpdatePodTemplateStatus validates the PodTemplateSpec and uses StatusManager to collect
// status changes. Returns true if validation passes, false otherwise.
// The caller is responsible for applying status updates via applyStatusUpdates().
func (r *VirtualMCPServerReconciler) validateAndUpdatePodTemplateStatus(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	statusManager virtualmcpserverstatus.StatusManager,
) bool {
	ctxLogger := log.FromContext(ctx)

	// Only validate if PodTemplateSpec is provided
	if vmcp.Spec.PodTemplateSpec == nil || vmcp.Spec.PodTemplateSpec.Raw == nil {
		// No PodTemplateSpec provided, validation passes
		return true
	}

	_, err := ctrlutil.NewPodTemplateSpecBuilder(vmcp.Spec.PodTemplateSpec, "vmcp")
	if err != nil {
		// Record event for invalid PodTemplateSpec
		if r.Recorder != nil {
			r.Recorder.Eventf(vmcp, corev1.EventTypeWarning, "InvalidPodTemplateSpec",
				"Failed to parse PodTemplateSpec: %v. Deployment blocked until PodTemplateSpec is fixed.", err)
		}

		// Use StatusManager to collect status changes
		statusManager.SetPhase(mcpv1alpha1.VirtualMCPServerPhaseFailed)
		statusManager.SetMessage(fmt.Sprintf("Invalid PodTemplateSpec: %v", err))
		statusManager.SetCondition(
			mcpv1alpha1.ConditionTypeVirtualMCPServerPodTemplateSpecValid,
			mcpv1alpha1.ConditionReasonVirtualMCPServerPodTemplateSpecInvalid,
			fmt.Sprintf("Failed to parse PodTemplateSpec: %v. Deployment blocked until fixed.", err),
			metav1.ConditionFalse,
		)
		statusManager.SetObservedGeneration(vmcp.Generation)

		ctxLogger.Error(err, "PodTemplateSpec validation failed")
		return false
	}

	// Use StatusManager to collect status changes for valid PodTemplateSpec
	statusManager.SetCondition(
		mcpv1alpha1.ConditionTypeVirtualMCPServerPodTemplateSpecValid,
		mcpv1alpha1.ConditionReasonVirtualMCPServerPodTemplateSpecValid,
		"PodTemplateSpec is valid",
		metav1.ConditionTrue,
	)
	statusManager.SetObservedGeneration(vmcp.Generation)

	return true
}

// ensureAllResources ensures all Kubernetes resources for the VirtualMCPServer
func (r *VirtualMCPServerReconciler) ensureAllResources(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	statusManager virtualmcpserverstatus.StatusManager,
) error {
	ctxLogger := log.FromContext(ctx)

	// Validate secret references before creating resources
	// This catches configuration errors early, providing faster feedback than waiting for pod startup failures
	if err := r.validateSecretReferences(ctx, vmcp); err != nil {
		ctxLogger.Error(err, "Secret validation failed")
		// Set AuthConfigured condition to False
		statusManager.SetAuthConfiguredCondition(
			mcpv1alpha1.ConditionReasonAuthInvalid,
			fmt.Sprintf("Authentication configuration is invalid: %v", err),
			metav1.ConditionFalse,
		)
		statusManager.SetObservedGeneration(vmcp.Generation)
		// Record event for secret validation failure
		if r.Recorder != nil {
			r.Recorder.Eventf(vmcp, corev1.EventTypeWarning, "SecretValidationFailed",
				"Secret validation failed: %v", err)
		}
		return err
	}

	// Authentication secrets validated successfully
	statusManager.SetAuthConfiguredCondition(
		mcpv1alpha1.ConditionReasonAuthValid,
		"Authentication configuration is valid",
		metav1.ConditionTrue,
	)
	statusManager.SetObservedGeneration(vmcp.Generation)

	// List workloads once and pass to functions that need them
	// This ensures consistency - all functions use the same workload list
	// rather than listing at different times which could yield different results
	workloadDiscoverer := workloads.NewK8SDiscovererWithClient(r.Client, vmcp.Namespace)
	workloadNames, err := workloadDiscoverer.ListWorkloadsInGroup(ctx, vmcp.Spec.GroupRef.Name)
	if err != nil {
		ctxLogger.Error(err, "Failed to list workloads in group")
		return fmt.Errorf("failed to list workloads in group: %w", err)
	}

	// Ensure RBAC resources
	if err := r.ensureRBACResources(ctx, vmcp); err != nil {
		ctxLogger.Error(err, "Failed to ensure RBAC resources")
		return err
	}

	// Ensure vmcp Config ConfigMap
	if err := r.ensureVmcpConfigConfigMap(ctx, vmcp, workloadNames); err != nil {
		ctxLogger.Error(err, "Failed to ensure vmcp Config ConfigMap")
		return err
	}

	// Ensure Deployment
	if result, err := r.ensureDeployment(ctx, vmcp, workloadNames); err != nil {
		return err
	} else if result.RequeueAfter > 0 {
		return nil
	}

	// Ensure Service
	if result, err := r.ensureService(ctx, vmcp); err != nil {
		return err
	} else if result.RequeueAfter > 0 {
		return nil
	}

	// Update service URL in status
	r.ensureServiceURL(vmcp, statusManager)
	return nil
}

// ensureRBACResources ensures that the RBAC resources are in place for the VirtualMCPServer
func (r *VirtualMCPServerReconciler) ensureRBACResources(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) error {
	serviceAccountName := vmcpServiceAccountName(vmcp.Name)

	// Ensure Role with minimal permissions
	if err := ctrlutil.EnsureRBACResource(ctx, r.Client, r.Scheme, vmcp, "Role", func() client.Object {
		return &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      serviceAccountName,
				Namespace: vmcp.Namespace,
			},
			Rules: vmcpRBACRules,
		}
	}); err != nil {
		return err
	}

	// Ensure ServiceAccount
	if err := ctrlutil.EnsureRBACResource(ctx, r.Client, r.Scheme, vmcp, "ServiceAccount", func() client.Object {
		return &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      serviceAccountName,
				Namespace: vmcp.Namespace,
			},
		}
	}); err != nil {
		return err
	}

	// Ensure RoleBinding
	return ctrlutil.EnsureRBACResource(ctx, r.Client, r.Scheme, vmcp, "RoleBinding", func() client.Object {
		return &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      serviceAccountName,
				Namespace: vmcp.Namespace,
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "Role",
				Name:     serviceAccountName,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      serviceAccountName,
					Namespace: vmcp.Namespace,
				},
			},
		}
	})
}

// getVmcpConfigChecksum fetches the vmcp Config ConfigMap checksum annotation.
// This is used to trigger deployment rollouts when the configuration changes.
//
// Note: VirtualMCPServer uses a custom ConfigMap naming pattern ("{name}-vmcp-config")
// instead of the standard "{name}-runconfig" pattern, so it cannot use the shared
// checksum.RunConfigChecksumFetcher. However, it follows the same validation logic
// and uses the same annotation constant for consistency.
func (r *VirtualMCPServerReconciler) getVmcpConfigChecksum(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) (string, error) {
	if vmcp == nil {
		return "", fmt.Errorf("vmcp cannot be nil")
	}

	configMapName := vmcpConfigMapName(vmcp.Name)
	configMap := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      configMapName,
		Namespace: vmcp.Namespace,
	}, configMap)

	if err != nil {
		// Preserve error type for IsNotFound checks
		return "", fmt.Errorf("failed to get vmcp Config ConfigMap %s/%s: %w",
			vmcp.Namespace, configMapName, err)
	}

	// Use the standard checksum annotation constant for consistency
	checksumValue, ok := configMap.Annotations[checksum.ContentChecksumAnnotation]
	if !ok {
		return "", fmt.Errorf("vmcp Config ConfigMap %s/%s missing %s annotation",
			vmcp.Namespace, configMapName, checksum.ContentChecksumAnnotation)
	}

	if checksumValue == "" {
		return "", fmt.Errorf("vmcp Config ConfigMap %s/%s has empty %s annotation",
			vmcp.Namespace, configMapName, checksum.ContentChecksumAnnotation)
	}

	return checksumValue, nil
}

// ensureDeployment ensures the Deployment exists and is up to date
//
//nolint:unparam // ctrl.Result needed for ConfigMap not found case (RequeueAfter)
func (r *VirtualMCPServerReconciler) ensureDeployment(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	typedWorkloads []workloads.TypedWorkload,
) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	// Fetch vmcp Config ConfigMap checksum to include in pod template annotations
	vmcpConfigChecksum, err := r.getVmcpConfigChecksum(ctx, vmcp)
	if err != nil {
		if errors.IsNotFound(err) {
			ctxLogger.Info("vmcp Config ConfigMap not found yet, will retry",
				"vmcp", vmcp.Name, "namespace", vmcp.Namespace)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		ctxLogger.Error(err, "Failed to get vmcp Config checksum")
		return ctrl.Result{}, err
	}

	deployment := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: vmcp.Name, Namespace: vmcp.Namespace}, deployment)

	if errors.IsNotFound(err) {
		dep := r.deploymentForVirtualMCPServer(ctx, vmcp, vmcpConfigChecksum, typedWorkloads)
		if dep == nil {
			return ctrl.Result{}, fmt.Errorf("failed to create Deployment object")
		}
		ctxLogger.Info("Creating a new Deployment", "Deployment.Namespace", dep.Namespace, "Deployment.Name", dep.Name)
		if err := r.Create(ctx, dep); err != nil {
			ctxLogger.Error(err, "Failed to create new Deployment")
			// Record event for deployment creation failure
			if r.Recorder != nil {
				r.Recorder.Eventf(vmcp, corev1.EventTypeWarning, "DeploymentCreationFailed",
					"Failed to create Deployment: %v", err)
			}
			return ctrl.Result{}, err
		}
		// Record event for successful deployment creation
		if r.Recorder != nil {
			r.Recorder.Event(vmcp, corev1.EventTypeNormal, "DeploymentCreated",
				"Deployment created successfully")
		}
		// Return empty result to continue with rest of reconciliation (Service, status update, etc.)
		// Kubernetes will automatically requeue when Deployment status changes
		return ctrl.Result{}, nil
	} else if err != nil {
		ctxLogger.Error(err, "Failed to get Deployment")
		return ctrl.Result{}, err
	}

	// Deployment exists - check if it needs to be updated
	// deploymentNeedsUpdate performs a detailed comparison to avoid unnecessary updates
	if r.deploymentNeedsUpdate(ctx, deployment, vmcp, vmcpConfigChecksum, typedWorkloads) {
		newDeployment := r.deploymentForVirtualMCPServer(ctx, vmcp, vmcpConfigChecksum, typedWorkloads)
		if newDeployment == nil {
			return ctrl.Result{}, fmt.Errorf("failed to create updated Deployment object")
		}

		// Selective field update strategy:
		// - Update Spec.Template: Contains container spec, volumes, pod metadata (triggers rollout)
		// - Update Labels: For label selectors and queries
		// - Update Annotations: For metadata and tooling
		// - Preserve Spec.Replicas: Allows HPA/VPA to manage scaling independently
		// - Preserve ResourceVersion, UID: Required for optimistic concurrency control
		//
		// Note: If update conflicts occur due to concurrent modifications, the reconcile
		// loop will retry automatically. Kubernetes' optimistic locking prevents data loss.
		deployment.Spec.Template = newDeployment.Spec.Template
		deployment.Labels = newDeployment.Labels
		deployment.Annotations = newDeployment.Annotations

		ctxLogger.Info("Updating Deployment", "Deployment.Namespace", deployment.Namespace, "Deployment.Name", deployment.Name)
		if err := r.Update(ctx, deployment); err != nil {
			ctxLogger.Error(err, "Failed to update Deployment")
			// Record event for deployment update failure
			if r.Recorder != nil {
				r.Recorder.Eventf(vmcp, corev1.EventTypeWarning, "DeploymentUpdateFailed",
					"Failed to update Deployment: %v", err)
			}
			// Return error to trigger reconcile retry (handles transient failures and conflicts)
			return ctrl.Result{}, err
		}
		// Record event for successful deployment update (config change triggers rollout)
		if r.Recorder != nil {
			r.Recorder.Event(vmcp, corev1.EventTypeNormal, "DeploymentUpdated",
				"Deployment updated, rolling out new configuration")
		}
		// Return empty result to continue with rest of reconciliation
		// Deployment rollout will be monitored when Kubernetes triggers subsequent reconciles
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

// ensureService ensures the Service exists and is up to date
//
//nolint:unparam // ctrl.Result kept for consistency with ensureDeployment signature
func (r *VirtualMCPServerReconciler) ensureService(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	serviceName := vmcpServiceName(vmcp.Name)
	service := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: serviceName, Namespace: vmcp.Namespace}, service)

	if errors.IsNotFound(err) {
		svc := r.serviceForVirtualMCPServer(ctx, vmcp)
		if svc == nil {
			return ctrl.Result{}, fmt.Errorf("failed to create Service object")
		}
		ctxLogger.Info("Creating a new Service", "Service.Namespace", svc.Namespace, "Service.Name", svc.Name)
		if err := r.Create(ctx, svc); err != nil {
			ctxLogger.Error(err, "Failed to create new Service")
			// Record event for service creation failure
			if r.Recorder != nil {
				r.Recorder.Eventf(vmcp, corev1.EventTypeWarning, "ServiceCreationFailed",
					"Failed to create Service: %v", err)
			}
			return ctrl.Result{}, err
		}
		// Record event for successful service creation
		if r.Recorder != nil {
			r.Recorder.Eventf(vmcp, corev1.EventTypeNormal, "ServiceCreated",
				"Service %s created successfully", serviceName)
		}
		// Return empty result to continue with rest of reconciliation
		return ctrl.Result{}, nil
	} else if err != nil {
		ctxLogger.Error(err, "Failed to get Service")
		return ctrl.Result{}, err
	}

	// Service exists - check if it needs to be updated
	// serviceNeedsUpdate compares ports, type, labels, and annotations
	if r.serviceNeedsUpdate(service, vmcp) {
		newService := r.serviceForVirtualMCPServer(ctx, vmcp)
		if newService == nil {
			return ctrl.Result{}, fmt.Errorf("failed to create updated Service object")
		}

		// Selective field update strategy for Service:
		// - Update Spec.Ports: Modify exposed ports
		// - Update Spec.Type: Change service type (ClusterIP, NodePort, LoadBalancer)
		// - Update Labels: For selectors and queries
		// - Update Annotations: For metadata and tooling
		// - Preserve Spec.ClusterIP: Immutable field, cannot be changed
		// - Preserve Spec.HealthCheckNodePort: Set by cloud provider for LoadBalancer
		// - Preserve ResourceVersion, UID: Required for optimistic concurrency control
		service.Spec.Ports = newService.Spec.Ports
		service.Spec.Type = newService.Spec.Type
		service.Labels = newService.Labels
		service.Annotations = newService.Annotations

		ctxLogger.Info("Updating Service", "Service.Namespace", service.Namespace, "Service.Name", service.Name)
		if err := r.Update(ctx, service); err != nil {
			ctxLogger.Error(err, "Failed to update Service")
			return ctrl.Result{}, err
		}
		// Return empty result to continue with rest of reconciliation
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

// ensureServiceURL ensures the service URL is set in the status
func (*VirtualMCPServerReconciler) ensureServiceURL(
	vmcp *mcpv1alpha1.VirtualMCPServer,
	statusManager virtualmcpserverstatus.StatusManager,
) {
	if vmcp.Status.URL == "" {
		url := createVmcpServiceURL(vmcp.Name, vmcp.Namespace, vmcpDefaultPort)
		statusManager.SetURL(url)
	}
}

// deploymentNeedsUpdate checks if the deployment needs to be updated
func (r *VirtualMCPServerReconciler) deploymentNeedsUpdate(
	ctx context.Context,
	deployment *appsv1.Deployment,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	vmcpConfigChecksum string,
	typedWorkloads []workloads.TypedWorkload,
) bool {
	if deployment == nil || vmcp == nil {
		return true
	}

	if len(deployment.Spec.Template.Spec.Containers) == 0 {
		return true
	}

	if r.containerNeedsUpdate(ctx, deployment, vmcp, typedWorkloads) {
		return true
	}

	if r.deploymentMetadataNeedsUpdate(deployment, vmcp) {
		return true
	}

	if r.podTemplateMetadataNeedsUpdate(deployment, vmcp, vmcpConfigChecksum) {
		return true
	}

	if r.podTemplateSpecNeedsUpdate(ctx, deployment, vmcp, typedWorkloads) {
		return true
	}

	return false
}

// containerNeedsUpdate checks if the container specification has changed
func (r *VirtualMCPServerReconciler) containerNeedsUpdate(
	ctx context.Context,
	deployment *appsv1.Deployment,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	typedWorkloads []workloads.TypedWorkload,
) bool {
	if deployment == nil || vmcp == nil || len(deployment.Spec.Template.Spec.Containers) == 0 {
		return true
	}

	container := deployment.Spec.Template.Spec.Containers[0]

	// Check if vmcp image has changed
	expectedImage := getVmcpImage()
	if container.Image != expectedImage {
		return true
	}

	// Check if port has changed
	if len(container.Ports) > 0 && container.Ports[0].ContainerPort != vmcpDefaultPort {
		return true
	}

	// Check if container args have changed (includes --debug flag from logLevel)
	expectedArgs := r.buildContainerArgsForVmcp(vmcp)
	if !reflect.DeepEqual(container.Args, expectedArgs) {
		return true
	}

	// Check if environment variables have changed
	expectedEnv := r.buildEnvVarsForVmcp(ctx, vmcp, typedWorkloads)
	if !reflect.DeepEqual(container.Env, expectedEnv) {
		return true
	}

	// Check if service account has changed
	expectedServiceAccountName := vmcpServiceAccountName(vmcp.Name)
	currentServiceAccountName := deployment.Spec.Template.Spec.ServiceAccountName
	if currentServiceAccountName != "" && currentServiceAccountName != expectedServiceAccountName {
		return true
	}

	return false
}

// deploymentMetadataNeedsUpdate checks if deployment-level metadata has changed
func (*VirtualMCPServerReconciler) deploymentMetadataNeedsUpdate(
	deployment *appsv1.Deployment,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) bool {
	if deployment == nil || vmcp == nil {
		return true
	}

	expectedLabels := labelsForVirtualMCPServer(vmcp.Name)
	expectedAnnotations := make(map[string]string)

	// TODO: Add support for ResourceOverrides if needed in the future

	// Check that all expected labels are present with correct values
	// (Allows Kubernetes-managed labels to exist without triggering updates)
	for key, expectedValue := range expectedLabels {
		if actualValue, exists := deployment.Labels[key]; !exists || actualValue != expectedValue {
			return true
		}
	}

	// Check that all expected annotations are present with correct values
	// (Allows Kubernetes-managed annotations like deployment.kubernetes.io/revision to exist)
	for key, expectedValue := range expectedAnnotations {
		if actualValue, exists := deployment.Annotations[key]; !exists || actualValue != expectedValue {
			return true
		}
	}

	return false
}

// podTemplateMetadataNeedsUpdate checks if pod template metadata has changed
func (r *VirtualMCPServerReconciler) podTemplateMetadataNeedsUpdate(
	deployment *appsv1.Deployment,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	vmcpConfigChecksum string,
) bool {
	if deployment == nil || vmcp == nil {
		return true
	}

	expectedPodTemplateLabels, expectedPodTemplateAnnotations := r.buildPodTemplateMetadata(
		labelsForVirtualMCPServer(vmcp.Name), vmcp, vmcpConfigChecksum,
	)

	if !maps.Equal(deployment.Spec.Template.Labels, expectedPodTemplateLabels) {
		return true
	}

	if !maps.Equal(deployment.Spec.Template.Annotations, expectedPodTemplateAnnotations) {
		return true
	}

	return false
}

// podTemplateSpecNeedsUpdate checks if the user-provided PodTemplateSpec has changed
// This method compares the current deployment against a freshly generated deployment
// that includes the PodTemplateSpec customizations.
func (r *VirtualMCPServerReconciler) podTemplateSpecNeedsUpdate(
	ctx context.Context,
	deployment *appsv1.Deployment,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	typedWorkloads []workloads.TypedWorkload,
) bool {
	if deployment == nil || vmcp == nil {
		return true
	}

	// If no PodTemplateSpec is provided, no update needed
	if vmcp.Spec.PodTemplateSpec == nil || vmcp.Spec.PodTemplateSpec.Raw == nil {
		return false
	}

	// Get the vmcp config checksum
	vmcpConfigChecksum, err := r.getVmcpConfigChecksum(ctx, vmcp)
	if err != nil {
		// If we can't get the checksum, assume update is needed
		return true
	}

	// Generate a fresh deployment with PodTemplateSpec applied
	expectedDeployment := r.deploymentForVirtualMCPServer(ctx, vmcp, vmcpConfigChecksum, typedWorkloads)
	if expectedDeployment == nil {
		// If we can't generate expected deployment, assume update is needed
		return true
	}

	// Compare the pod template specs
	currentJSON, err := json.Marshal(deployment.Spec.Template)
	if err != nil {
		return true
	}

	expectedJSON, err := json.Marshal(expectedDeployment.Spec.Template)
	if err != nil {
		return true
	}

	// If the JSON representations differ, an update is needed
	return string(currentJSON) != string(expectedJSON)
}

// serviceNeedsUpdate checks if the service needs to be updated
func (*VirtualMCPServerReconciler) serviceNeedsUpdate(
	service *corev1.Service,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) bool {
	if service == nil || vmcp == nil {
		return true
	}

	// Check if port has changed
	if len(service.Spec.Ports) > 0 && service.Spec.Ports[0].Port != vmcpDefaultPort {
		return true
	}

	// Check if service type has changed
	expectedServiceType := corev1.ServiceTypeClusterIP
	if vmcp.Spec.ServiceType != "" {
		expectedServiceType = corev1.ServiceType(vmcp.Spec.ServiceType)
	}
	if service.Spec.Type != expectedServiceType {
		return true
	}

	// Check if service metadata has changed
	expectedLabels := labelsForVirtualMCPServer(vmcp.Name)
	expectedAnnotations := make(map[string]string)

	// TODO: Add support for ResourceOverrides if needed in the future

	if !maps.Equal(service.Labels, expectedLabels) {
		return true
	}

	if !maps.Equal(service.Annotations, expectedAnnotations) {
		return true
	}

	return false
}

// updateVirtualMCPServerStatus updates the status of the VirtualMCPServer based on pod and backend health.
//
// Status Update Pattern and Conflict Handling:
//
// This controller follows the status update pattern established by MCPGroup controller in this codebase.
// Status updates occur at multiple points during reconciliation:
//
//  1. Early Error States: Status updates happen immediately when validation or discovery fails
//     (e.g., GroupRef not found, GroupRef not ready, backend discovery failed)
//
// 2. Mid-Reconciliation: Status fields like URL are set when resources are created
//
// 3. Final Status: This function performs the comprehensive final status update by:
//   - Listing all pods for the deployment
//   - Checking backend health status
//   - Computing overall phase (Ready, Degraded, Pending, Failed)
//   - Setting appropriate conditions
//   - Updating ObservedGeneration to track which spec version was reconciled
//
// Conflict Handling Strategy:
// All Status().Update() calls now include explicit conflict detection using errors.IsConflict().
// When conflicts occur:
// - The error is returned to the controller runtime
// - Controller runtime automatically requeues the reconciliation
// - Next reconcile loop will GET the latest resource version and retry
//
// This implements Kubernetes' optimistic concurrency control pattern and prevents lost updates
// when multiple controllers or processes modify the same resource. The MCPGroup controller
// demonstrates this pattern is the established best practice in this codebase.
//
// Why Not a Separate Status Reconciler?
// This codebase does not use separate status-only reconcile loops. Status and spec reconciliation
// happen in the same loop, which is appropriate for this use case because:
// - Status depends on spec reconciliation (need deployment/service to exist first)
// - Status updates are not frequent enough to warrant separate reconciliation
// - Single reconcile loop is simpler and matches existing codebase patterns

// statusDecision encapsulates the status update decision to reduce branching and repetition
type statusDecision struct {
	phase          mcpv1alpha1.VirtualMCPServerPhase
	message        string
	reason         string
	conditionMsg   string
	conditionState metav1.ConditionStatus
}

// countBackendHealth counts ready and unhealthy backends
func countBackendHealth(ctx context.Context, backends []mcpv1alpha1.DiscoveredBackend) (ready, unhealthy int) {
	ctxLogger := log.FromContext(ctx)

	for _, backend := range backends {
		switch backend.Status {
		case mcpv1alpha1.BackendStatusReady:
			ready++
		case mcpv1alpha1.BackendStatusUnavailable,
			mcpv1alpha1.BackendStatusDegraded,
			mcpv1alpha1.BackendStatusUnknown:
			unhealthy++
		default:
			ctxLogger.V(1).Info("Unexpected backend status, treating as unhealthy",
				"backend", backend.Name, "status", backend.Status)
			unhealthy++
		}
	}
	return ready, unhealthy
}

// determineStatusFromBackends evaluates backend health to determine status
func (*VirtualMCPServerReconciler) determineStatusFromBackends(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) statusDecision {
	ctxLogger := log.FromContext(ctx)

	ready, unhealthy := countBackendHealth(ctx, vmcp.Status.DiscoveredBackends)
	total := ready + unhealthy

	// All backends unhealthy
	if ready == 0 && unhealthy > 0 {
		return statusDecision{
			phase:          mcpv1alpha1.VirtualMCPServerPhaseDegraded,
			message:        fmt.Sprintf("Virtual MCP server is running but all %d backends are unhealthy", unhealthy),
			reason:         "BackendsUnavailable",
			conditionMsg:   "All backends are unhealthy",
			conditionState: metav1.ConditionFalse,
		}
	}

	// Some backends unhealthy
	if unhealthy > 0 {
		return statusDecision{
			phase:          mcpv1alpha1.VirtualMCPServerPhaseDegraded,
			message:        fmt.Sprintf("Virtual MCP server is running with %d/%d backends available", ready, total),
			reason:         "BackendsDegraded",
			conditionMsg:   "Some backends are unhealthy",
			conditionState: metav1.ConditionFalse,
		}
	}

	// All backends ready
	if ready > 0 {
		return statusDecision{
			phase:          mcpv1alpha1.VirtualMCPServerPhaseReady,
			message:        "Virtual MCP server is running",
			reason:         "DeploymentReady",
			conditionMsg:   "Deployment is ready",
			conditionState: metav1.ConditionTrue,
		}
	}

	// Edge case: backends exist but none counted
	ctxLogger.V(1).Info("No backends were counted, treating as degraded",
		"discoveredBackendsCount", len(vmcp.Status.DiscoveredBackends))
	return statusDecision{
		phase:          mcpv1alpha1.VirtualMCPServerPhaseDegraded,
		message:        "Virtual MCP server is running but backend status cannot be determined",
		reason:         "BackendsUnknown",
		conditionMsg:   "Backend status unknown",
		conditionState: metav1.ConditionFalse,
	}
}

// determineStatusFromPods determines the appropriate status based on pod states.
// The 'ready' parameter counts pods that have passed their readiness probes (PodReady condition is True),
// not just pods in Running phase. This ensures the VirtualMCPServer is only marked Ready when
// the underlying pods are actually ready to serve traffic.
func (r *VirtualMCPServerReconciler) determineStatusFromPods(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	ready, pending, failed int,
) statusDecision {
	// Handle non-ready states first (early returns reduce nesting)
	if ready == 0 {
		if failed > 0 {
			return statusDecision{
				phase:          mcpv1alpha1.VirtualMCPServerPhaseFailed,
				message:        "Virtual MCP server failed to start",
				reason:         "DeploymentFailed",
				conditionMsg:   "Deployment failed",
				conditionState: metav1.ConditionFalse,
			}
		}
		// pending > 0 or no pods at all
		msg := "Virtual MCP server is starting"
		if pending == 0 {
			msg = "No pods found for Virtual MCP server"
		}
		return statusDecision{
			phase:          mcpv1alpha1.VirtualMCPServerPhasePending,
			message:        msg,
			reason:         "DeploymentNotReady",
			conditionMsg:   "Deployment is not yet ready",
			conditionState: metav1.ConditionFalse,
		}
	}

	// Pods are ready (passed readiness probes) - check backend health if backends exist
	if len(vmcp.Status.DiscoveredBackends) == 0 {
		// No backends discovered yet - pods ready is sufficient for Ready
		return statusDecision{
			phase:          mcpv1alpha1.VirtualMCPServerPhaseReady,
			message:        "Virtual MCP server is running",
			reason:         "DeploymentReady",
			conditionMsg:   "Deployment is ready",
			conditionState: metav1.ConditionTrue,
		}
	}

	// Backends exist - determine health status
	return r.determineStatusFromBackends(ctx, vmcp)
}

func (r *VirtualMCPServerReconciler) updateVirtualMCPServerStatus(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	statusManager virtualmcpserverstatus.StatusManager,
) error {
	// List the pods for this VirtualMCPServer's deployment
	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(vmcp.Namespace),
		client.MatchingLabels(labelsForVirtualMCPServer(vmcp.Name)),
	}
	if err := r.List(ctx, podList, listOpts...); err != nil {
		return err
	}

	// Count pod states based on actual readiness, not just phase.
	// A pod in Running phase may not be ready to serve traffic if it hasn't
	// passed its readiness probe yet. We must check the PodReady condition.
	var ready, pending, failed int
	for _, pod := range podList.Items {
		// Check for terminal failure states first
		if pod.Status.Phase == corev1.PodFailed {
			failed++
			continue
		}

		// Check if pod is actually ready to serve traffic (passed readiness probes)
		// This is the authoritative signal that the pod can handle requests
		isPodReady := false
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				isPodReady = true
				break
			}
		}

		if isPodReady {
			ready++
		} else {
			// Pod exists but isn't ready yet (still starting, or readiness probe failing)
			pending++
		}
	}

	// Determine status in one place (no branching/repetition)
	decision := r.determineStatusFromPods(ctx, vmcp, ready, pending, failed)

	// Apply all status updates at once
	statusManager.SetPhase(decision.phase)
	statusManager.SetMessage(decision.message)
	statusManager.SetReadyCondition(decision.reason, decision.conditionMsg, decision.conditionState)
	statusManager.SetObservedGeneration(vmcp.Generation)

	return nil
}

// labelsForVirtualMCPServer returns the labels for selecting the resources belonging to the given VirtualMCPServer CR name
func labelsForVirtualMCPServer(name string) map[string]string {
	return map[string]string{
		"app":                        "virtualmcpserver",
		"app.kubernetes.io/name":     "virtualmcpserver",
		"app.kubernetes.io/instance": name,
		"toolhive":                   "true",
		"toolhive-name":              name,
	}
}

// vmcpServiceAccountName returns the service account name for the vmcp server
// Uses "-vmcp" suffix to avoid conflicts with MCPServer or MCPRemoteProxy resources of the same name.
// This allows VirtualMCPServer, MCPServer, and MCPRemoteProxy to coexist in the same namespace
// with the same base name (e.g., "foo-vmcp", "foo-proxy-runner", "foo-remote-proxy-runner").
func vmcpServiceAccountName(vmcpName string) string {
	return fmt.Sprintf("%s-vmcp", vmcpName)
}

// vmcpServiceName generates the service name for a VirtualMCPServer
// Uses "vmcp-" prefix to distinguish from MCPServer's "mcp-{name}-proxy" pattern.
// This allows VirtualMCPServer and MCPServer to coexist with the same base name.
//
// Design Note: Each controller has its own service naming functions rather than using a shared utility
// because naming conventions are intentionally different to prevent conflicts:
// - MCPServer: "mcp-{name}-proxy"
// - MCPRemoteProxy: "mcp-{name}-remote-proxy"
// - VirtualMCPServer: "vmcp-{name}"
//
// This pattern is controller-specific by design. Moving to controllerutil would not add value since
// there's no shared logic - just different prefixes/suffixes for each resource type.
func vmcpServiceName(vmcpName string) string {
	return fmt.Sprintf("vmcp-%s", vmcpName)
}

// vmcpConfigMapName generates the ConfigMap name for a VirtualMCPServer's vmcp configuration
// Uses "-vmcp-config" suffix pattern.
func vmcpConfigMapName(vmcpName string) string {
	return fmt.Sprintf("%s-vmcp-config", vmcpName)
}

// createVmcpServiceURL generates the full cluster-local service URL for a VirtualMCPServer
// While the URL pattern (http://{service}.{namespace}.svc.cluster.local:{port}) is standard,
// each controller has different service naming requirements (see vmcpServiceName comment).
func createVmcpServiceURL(vmcpName, namespace string, port int32) string {
	serviceName := vmcpServiceName(vmcpName)
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", serviceName, namespace, port)
}

// convertExternalAuthConfigToStrategy converts an MCPExternalAuthConfig to a BackendAuthStrategy.
// This uses the converter registry to support all auth types (token exchange, header injection, etc.).
// For ConfigMap mode (inline), secrets are referenced as environment variables that will be
// mounted in the deployment. Each ExternalAuthConfig gets a unique env var name to avoid conflicts.
func (*VirtualMCPServerReconciler) convertExternalAuthConfigToStrategy(
	externalAuthConfig *mcpv1alpha1.MCPExternalAuthConfig,
) (*authtypes.BackendAuthStrategy, error) {
	// Use the converter registry to convert to typed strategy
	registry := converters.DefaultRegistry()
	converter, err := registry.GetConverter(externalAuthConfig.Spec.Type)
	if err != nil {
		return nil, err
	}

	// Convert to typed BackendAuthStrategy (this will use env var references for secrets)
	strategy, err := converter.ConvertToStrategy(externalAuthConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to convert external auth config to strategy: %w", err)
	}

	// Set unique env var names per ExternalAuthConfig to avoid conflicts
	// when multiple configs of the same type reference different secrets
	if strategy.TokenExchange != nil &&
		externalAuthConfig.Spec.TokenExchange != nil &&
		externalAuthConfig.Spec.TokenExchange.ClientSecretRef != nil {
		strategy.TokenExchange.ClientSecretEnv = ctrlutil.GenerateUniqueTokenExchangeEnvVarName(externalAuthConfig.Name)
	}
	if strategy.HeaderInjection != nil &&
		externalAuthConfig.Spec.HeaderInjection != nil &&
		externalAuthConfig.Spec.HeaderInjection.ValueSecretRef != nil {
		strategy.HeaderInjection.HeaderValueEnv = ctrlutil.GenerateUniqueHeaderInjectionEnvVarName(externalAuthConfig.Name)
	}

	return strategy, nil
}

// convertBackendAuthConfigToVMCP converts a BackendAuthConfig from CRD to vmcp config.
func (r *VirtualMCPServerReconciler) convertBackendAuthConfigToVMCP(
	ctx context.Context,
	namespace string,
	crdConfig *mcpv1alpha1.BackendAuthConfig,
) (*authtypes.BackendAuthStrategy, error) {
	// For type="discovered", return a minimal strategy (will be populated by discovery)
	if crdConfig.Type == mcpv1alpha1.BackendAuthTypeDiscovered {
		return &authtypes.BackendAuthStrategy{
			Type: crdConfig.Type,
		}, nil
	}

	// For type="external_auth_config_ref", fetch and convert the referenced config
	if crdConfig.ExternalAuthConfigRef != nil {
		// Fetch the MCPExternalAuthConfig and convert it
		externalAuthConfig, err := ctrlutil.GetExternalAuthConfigByName(
			ctx, r.Client, namespace, crdConfig.ExternalAuthConfigRef.Name)
		if err != nil {
			return nil, fmt.Errorf("failed to get MCPExternalAuthConfig %s: %w", crdConfig.ExternalAuthConfigRef.Name, err)
		}

		// Convert the external auth config to strategy
		return r.convertExternalAuthConfigToStrategy(externalAuthConfig)
	}

	// Fallback: return minimal strategy
	return &authtypes.BackendAuthStrategy{
		Type: crdConfig.Type,
	}, nil
}

// listMCPServersAsMap lists all MCPServers in the namespace and returns a map by name.
func (r *VirtualMCPServerReconciler) listMCPServersAsMap(
	ctx context.Context,
	namespace string,
) (map[string]*mcpv1alpha1.MCPServer, error) {
	mcpServerList := &mcpv1alpha1.MCPServerList{}
	if err := r.List(ctx, mcpServerList, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	mcpServerMap := make(map[string]*mcpv1alpha1.MCPServer, len(mcpServerList.Items))
	for i := range mcpServerList.Items {
		mcpServerMap[mcpServerList.Items[i].Name] = &mcpServerList.Items[i]
	}
	return mcpServerMap, nil
}

// listMCPRemoteProxiesAsMap lists all MCPRemoteProxies in the namespace and returns a map by name.
func (r *VirtualMCPServerReconciler) listMCPRemoteProxiesAsMap(
	ctx context.Context,
	namespace string,
) (map[string]*mcpv1alpha1.MCPRemoteProxy, error) {
	mcpRemoteProxyList := &mcpv1alpha1.MCPRemoteProxyList{}
	if err := r.List(ctx, mcpRemoteProxyList, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	mcpRemoteProxyMap := make(map[string]*mcpv1alpha1.MCPRemoteProxy, len(mcpRemoteProxyList.Items))
	for i := range mcpRemoteProxyList.Items {
		mcpRemoteProxyMap[mcpRemoteProxyList.Items[i].Name] = &mcpRemoteProxyList.Items[i]
	}
	return mcpRemoteProxyMap, nil
}

// discovers ExternalAuthConfig from workloads and adds them to the outgoing config
func (r *VirtualMCPServerReconciler) discoverExternalAuthConfigs(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	typedWorkloads []workloads.TypedWorkload,
	outgoing *vmcpconfig.OutgoingAuthConfig,
) {
	ctxLogger := log.FromContext(ctx)

	mcpServerMap, err := r.listMCPServersAsMap(ctx, vmcp.Namespace)
	if err != nil {
		ctxLogger.Error(err, "Failed to list MCPServers")
		return
	}

	mcpRemoteProxyMap, err := r.listMCPRemoteProxiesAsMap(ctx, vmcp.Namespace)
	if err != nil {
		ctxLogger.Error(err, "Failed to list MCPRemoteProxies")
		return
	}

	for _, workloadInfo := range typedWorkloads {
		externalAuthConfigName := r.getExternalAuthConfigNameFromWorkload(
			workloadInfo, mcpServerMap, mcpRemoteProxyMap)
		if externalAuthConfigName == "" {
			continue
		}

		// Fetch the MCPExternalAuthConfig
		externalAuthConfig, err := ctrlutil.GetExternalAuthConfigByName(
			ctx, r.Client, vmcp.Namespace, externalAuthConfigName)
		if err != nil {
			ctxLogger.V(1).Info("Failed to get MCPExternalAuthConfig for backend, skipping",
				"backend", workloadInfo.Name,
				"externalAuthConfig", externalAuthConfigName,
				"error", err)
			continue
		}

		// Convert MCPExternalAuthConfig to BackendAuthStrategy
		strategy, err := r.convertExternalAuthConfigToStrategy(externalAuthConfig)
		if err != nil {
			ctxLogger.V(1).Info("Failed to convert MCPExternalAuthConfig to strategy, skipping",
				"backend", workloadInfo.Name,
				"externalAuthConfig", externalAuthConfig.Name,
				"error", err)
			continue
		}

		// Only add if not already overridden in inline config
		if vmcp.Spec.OutgoingAuth == nil || vmcp.Spec.OutgoingAuth.Backends == nil {
			outgoing.Backends[workloadInfo.Name] = strategy
		} else if _, exists := vmcp.Spec.OutgoingAuth.Backends[workloadInfo.Name]; !exists {
			// Only add discovered config if not explicitly overridden
			outgoing.Backends[workloadInfo.Name] = strategy
		}
	}
}

// getExternalAuthConfigNameFromWorkload extracts the ExternalAuthConfigRef name from a workload.
func (*VirtualMCPServerReconciler) getExternalAuthConfigNameFromWorkload(
	workloadInfo workloads.TypedWorkload,
	mcpServerMap map[string]*mcpv1alpha1.MCPServer,
	mcpRemoteProxyMap map[string]*mcpv1alpha1.MCPRemoteProxy,
) string {
	switch workloadInfo.Type {
	case workloads.WorkloadTypeMCPServer:
		mcpServer, found := mcpServerMap[workloadInfo.Name]
		if !found || mcpServer.Spec.ExternalAuthConfigRef == nil {
			return ""
		}
		return mcpServer.Spec.ExternalAuthConfigRef.Name

	case workloads.WorkloadTypeMCPRemoteProxy:
		mcpRemoteProxy, found := mcpRemoteProxyMap[workloadInfo.Name]
		if !found || mcpRemoteProxy.Spec.ExternalAuthConfigRef == nil {
			return ""
		}
		return mcpRemoteProxy.Spec.ExternalAuthConfigRef.Name

	default:
		return ""
	}
}

// buildOutgoingAuthConfig builds an OutgoingAuthConfig from the VirtualMCPServer spec,
// discovering ExternalAuthConfig from MCPServers when source is "discovered".
func (r *VirtualMCPServerReconciler) buildOutgoingAuthConfig(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	typedWorkloads []workloads.TypedWorkload,
) (*vmcpconfig.OutgoingAuthConfig, error) {
	// Determine source - default to "discovered" if not specified
	source := OutgoingAuthSourceDiscovered
	if vmcp.Spec.OutgoingAuth != nil && vmcp.Spec.OutgoingAuth.Source != "" {
		source = vmcp.Spec.OutgoingAuth.Source
	}

	outgoing := &vmcpconfig.OutgoingAuthConfig{
		Source:   source,
		Backends: make(map[string]*authtypes.BackendAuthStrategy),
	}

	// Convert Default if specified
	if vmcp.Spec.OutgoingAuth != nil && vmcp.Spec.OutgoingAuth.Default != nil {
		defaultStrategy, err := r.convertBackendAuthConfigToVMCP(ctx, vmcp.Namespace, vmcp.Spec.OutgoingAuth.Default)
		if err != nil {
			return nil, fmt.Errorf("failed to convert default auth config: %w", err)
		}
		outgoing.Default = defaultStrategy
	}

	// Discover ExternalAuthConfig from MCPServers if source is "discovered"
	if source == OutgoingAuthSourceDiscovered {
		r.discoverExternalAuthConfigs(ctx, vmcp, typedWorkloads, outgoing)
	}

	// Apply inline overrides (works for all source modes)
	if vmcp.Spec.OutgoingAuth != nil && vmcp.Spec.OutgoingAuth.Backends != nil {
		for backendName, backendAuth := range vmcp.Spec.OutgoingAuth.Backends {
			strategy, err := r.convertBackendAuthConfigToVMCP(ctx, vmcp.Namespace, &backendAuth)
			if err != nil {
				return nil, fmt.Errorf("failed to convert backend auth config for %s: %w", backendName, err)
			}
			outgoing.Backends[backendName] = strategy
		}
	}

	return outgoing, nil
}

// discoverBackends discovers all MCPServers in the referenced MCPGroup and returns
// a list of DiscoveredBackend objects with their current status.
// This reuses the existing workload discovery code from pkg/vmcp/workloads.
//
//nolint:gocyclo
func (r *VirtualMCPServerReconciler) discoverBackends(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) ([]mcpv1alpha1.DiscoveredBackend, error) {
	ctxLogger := log.FromContext(ctx)

	// Create groups manager using the controller's client and VirtualMCPServer's namespace
	groupsManager := groups.NewCRDManager(r.Client, vmcp.Namespace)

	// Create K8S workload discoverer for the VirtualMCPServer's namespace
	workloadDiscoverer := workloads.NewK8SDiscovererWithClient(r.Client, vmcp.Namespace)

	// Get all workloads in the group
	typedWorkloads, err := workloadDiscoverer.ListWorkloadsInGroup(ctx, vmcp.Spec.GroupRef.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to list workloads in group: %w", err)
	}

	// Build outgoing auth config only if OutgoingAuth is explicitly configured
	// This allows the aggregator to apply auth config to backends based on source mode
	var authConfig *vmcpconfig.OutgoingAuthConfig
	if vmcp.Spec.OutgoingAuth != nil {
		var err error
		authConfig, err = r.buildOutgoingAuthConfig(ctx, vmcp, typedWorkloads)
		if err != nil {
			ctxLogger.V(1).Info("Failed to build outgoing auth config, continuing without auth",
				"error", err)
			// Continue without auth config rather than failing
			authConfig = nil
		}
	}

	// Use the aggregator's unified backend discoverer to reuse discovery logic
	backendDiscoverer := aggregator.NewUnifiedBackendDiscoverer(workloadDiscoverer, groupsManager, authConfig)

	// Discover backends using the aggregator
	backends, err := backendDiscoverer.Discover(ctx, vmcp.Spec.GroupRef.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to discover backends: %w", err)
	}

	// Create a map of discovered backend names for quick lookup
	discoveredBackendMap := make(map[string]*vmcptypes.Backend, len(backends))
	for i := range backends {
		discoveredBackendMap[backends[i].Name] = &backends[i]
	}

	// Build maps of MCPServers and MCPRemoteProxies for efficient lookup
	mcpServerMap, err := r.listMCPServersAsMap(ctx, vmcp.Namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to list MCPServers: %w", err)
	}

	mcpRemoteProxyMap, err := r.listMCPRemoteProxiesAsMap(ctx, vmcp.Namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to list MCPRemoteProxies: %w", err)
	}

	discoveredBackends := make([]mcpv1alpha1.DiscoveredBackend, 0, len(typedWorkloads))
	now := metav1.Now()

	// Convert vmcp.Backend to DiscoveredBackend for all workloads in the group
	for _, workloadInfo := range typedWorkloads {
		backend, found := discoveredBackendMap[workloadInfo.Name]
		if !found {
			// Workload exists but is not accessible (no URL or error)
			discoveredBackends = append(discoveredBackends, mcpv1alpha1.DiscoveredBackend{
				Name:            workloadInfo.Name,
				Status:          mcpv1alpha1.BackendStatusUnavailable,
				LastHealthCheck: now,
			})
			continue
		}

		// Convert vmcp.Backend to DiscoveredBackend
		// Map health status from BackendHealthStatus to string
		var backendStatus string
		switch backend.HealthStatus {
		case vmcptypes.BackendHealthy:
			backendStatus = mcpv1alpha1.BackendStatusReady
		case vmcptypes.BackendUnhealthy, vmcptypes.BackendUnauthenticated:
			backendStatus = mcpv1alpha1.BackendStatusUnavailable
		case vmcptypes.BackendDegraded:
			backendStatus = mcpv1alpha1.BackendStatusDegraded
		case vmcptypes.BackendUnknown:
			backendStatus = mcpv1alpha1.BackendStatusUnknown
		default:
			backendStatus = mcpv1alpha1.BackendStatusUnknown
		}

		// Extract auth config reference and check workload phase based on workload type
		// Using pre-fetched maps instead of individual Get calls
		authConfigRef := ""
		authType := ""
		switch workloadInfo.Type {
		case workloads.WorkloadTypeMCPServer:
			if mcpServer, found := mcpServerMap[workloadInfo.Name]; found {
				if mcpServer.Spec.ExternalAuthConfigRef != nil {
					authConfigRef = mcpServer.Spec.ExternalAuthConfigRef.Name
					authType = mcpv1alpha1.BackendAuthTypeExternalAuthConfigRef
				}
				// Override backend status based on MCPServer phase for non-ready states
				if mcpServer.Status.Phase == mcpv1alpha1.MCPServerPhasePending ||
					mcpServer.Status.Phase == mcpv1alpha1.MCPServerPhaseFailed ||
					mcpServer.Status.Phase == mcpv1alpha1.MCPServerPhaseTerminating {
					backendStatus = mcpv1alpha1.BackendStatusUnavailable
					ctxLogger.V(1).Info("Backend MCPServer not ready, marking as unavailable",
						"name", workloadInfo.Name,
						"phase", mcpServer.Status.Phase)
				}
			}
		case workloads.WorkloadTypeMCPRemoteProxy:
			if mcpRemoteProxy, found := mcpRemoteProxyMap[workloadInfo.Name]; found {
				if mcpRemoteProxy.Spec.ExternalAuthConfigRef != nil {
					authConfigRef = mcpRemoteProxy.Spec.ExternalAuthConfigRef.Name
					authType = mcpv1alpha1.BackendAuthTypeExternalAuthConfigRef
				}
				// Override backend status based on MCPRemoteProxy phase for non-ready states
				if mcpRemoteProxy.Status.Phase == mcpv1alpha1.MCPRemoteProxyPhasePending ||
					mcpRemoteProxy.Status.Phase == mcpv1alpha1.MCPRemoteProxyPhaseFailed ||
					mcpRemoteProxy.Status.Phase == mcpv1alpha1.MCPRemoteProxyPhaseTerminating {
					backendStatus = mcpv1alpha1.BackendStatusUnavailable
					ctxLogger.V(1).Info("Backend MCPRemoteProxy not ready, marking as unavailable",
						"name", workloadInfo.Name,
						"phase", mcpRemoteProxy.Status.Phase)
				}
			}
		}

		discoveredBackend := mcpv1alpha1.DiscoveredBackend{
			Name:            backend.Name,
			AuthConfigRef:   authConfigRef,
			AuthType:        authType,
			Status:          backendStatus,
			LastHealthCheck: now,
			URL:             backend.BaseURL,
		}

		discoveredBackends = append(discoveredBackends, discoveredBackend)
		ctxLogger.V(1).Info("Discovered backend",
			"name", backend.Name,
			"status", backendStatus,
			"url", backend.BaseURL,
			"authConfigRef", authConfigRef)
	}

	// Query vmcp health status and update backend statuses if health monitoring is enabled
	// This provides real MCP health check results instead of just Pod/Phase status
	if vmcp.Status.URL != "" {
		healthStatus := r.queryVMCPHealthStatus(ctx, vmcp.Status.URL)
		if healthStatus != nil {
			ctxLogger.V(1).Info("Updating backend status from vmcp health checks",
				"vmcp_url", vmcp.Status.URL,
				"backend_count", len(healthStatus))

			for i := range discoveredBackends {
				backend := &discoveredBackends[i]
				if healthInfo, found := healthStatus[backend.Name]; found {
					// Map vmcp health status to CRD backend status
					// vmcp statuses: healthy, unhealthy, degraded, unknown
					// CRD statuses: ready, unavailable, degraded, unknown
					var newStatus string
					switch healthInfo.Status {
					case "healthy":
						newStatus = mcpv1alpha1.BackendStatusReady
					case "unhealthy":
						newStatus = mcpv1alpha1.BackendStatusUnavailable
					case "degraded":
						newStatus = mcpv1alpha1.BackendStatusDegraded
					case "unknown":
						newStatus = mcpv1alpha1.BackendStatusUnknown
					default:
						// Keep existing status if health status is unexpected
						continue
					}

					// Update status if changed
					if newStatus != backend.Status {
						ctxLogger.V(1).Info("Backend health check updated status",
							"name", backend.Name,
							"old_status", backend.Status,
							"new_status", newStatus,
							"health_status", healthInfo.Status)
						backend.Status = newStatus
					}

					// Update LastHealthCheck with actual health check timestamp from vmcp
					if !healthInfo.LastCheckTime.IsZero() {
						backend.LastHealthCheck = metav1.NewTime(healthInfo.LastCheckTime)
					}
				}
			}
		} else {
			ctxLogger.V(1).Info("Health monitoring not enabled or failed to query vmcp health endpoint",
				"vmcp_url", vmcp.Status.URL)
		}
	}

	return discoveredBackends, nil
}

// SetupWithManager sets up the controller with the Manager
func (r *VirtualMCPServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.VirtualMCPServer{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Watches(&mcpv1alpha1.MCPGroup{}, handler.EnqueueRequestsFromMapFunc(r.mapMCPGroupToVirtualMCPServer)).
		Watches(&mcpv1alpha1.MCPServer{}, handler.EnqueueRequestsFromMapFunc(r.mapMCPServerToVirtualMCPServer)).
		Watches(&mcpv1alpha1.MCPRemoteProxy{}, handler.EnqueueRequestsFromMapFunc(r.mapMCPRemoteProxyToVirtualMCPServer)).
		Watches(&mcpv1alpha1.MCPExternalAuthConfig{}, handler.EnqueueRequestsFromMapFunc(r.mapExternalAuthConfigToVirtualMCPServer)).
		Watches(&mcpv1alpha1.MCPToolConfig{}, handler.EnqueueRequestsFromMapFunc(r.mapToolConfigToVirtualMCPServer)).
		Watches(
			&mcpv1alpha1.VirtualMCPCompositeToolDefinition{},
			handler.EnqueueRequestsFromMapFunc(r.mapCompositeToolDefinitionToVirtualMCPServer),
		).
		Complete(r)
}

// mapMCPGroupToVirtualMCPServer maps MCPGroup changes to VirtualMCPServer reconciliation requests
func (r *VirtualMCPServerReconciler) mapMCPGroupToVirtualMCPServer(ctx context.Context, obj client.Object) []reconcile.Request {
	mcpGroup, ok := obj.(*mcpv1alpha1.MCPGroup)
	if !ok {
		return nil
	}

	vmcpList := &mcpv1alpha1.VirtualMCPServerList{}
	if err := r.List(ctx, vmcpList, client.InNamespace(mcpGroup.Namespace)); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list VirtualMCPServers for MCPGroup watch")
		return nil
	}

	var requests []reconcile.Request
	for _, vmcp := range vmcpList.Items {
		if vmcp.Spec.GroupRef.Name == mcpGroup.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      vmcp.Name,
					Namespace: vmcp.Namespace,
				},
			})
		}
	}

	return requests
}

// mapMCPServerToVirtualMCPServer maps MCPServer changes to VirtualMCPServer reconciliation requests.
// This function implements an optimization to only reconcile VirtualMCPServers that are actually
// affected by the MCPServer change, rather than reconciling all VirtualMCPServers in the namespace.
//
// The optimization works by:
// 1. Finding all MCPGroups that include the changed MCPServer (via Status.Servers)
// 2. Finding all VirtualMCPServers that reference those MCPGroups
// 3. Only reconciling those specific VirtualMCPServers
//
// This significantly reduces unnecessary reconciliations in large clusters with many VirtualMCPServers.
func (r *VirtualMCPServerReconciler) mapMCPServerToVirtualMCPServer(ctx context.Context, obj client.Object) []reconcile.Request {
	mcpServer, ok := obj.(*mcpv1alpha1.MCPServer)
	if !ok {
		return nil
	}

	ctxLogger := log.FromContext(ctx)

	// Step 1: Find all MCPGroups that include this MCPServer
	// MCPGroups track their member servers in Status.Servers (populated by MCPGroup controller)
	mcpGroupList := &mcpv1alpha1.MCPGroupList{}
	if err := r.List(ctx, mcpGroupList, client.InNamespace(mcpServer.Namespace)); err != nil {
		ctxLogger.Error(err, "Failed to list MCPGroups for MCPServer watch")
		return nil
	}

	// Track which MCPGroups include this MCPServer
	affectedGroups := make(map[string]bool)
	for _, group := range mcpGroupList.Items {
		// Check if this MCPServer is in the group's server list
		for _, serverName := range group.Status.Servers {
			if serverName == mcpServer.Name {
				affectedGroups[group.Name] = true
				ctxLogger.V(1).Info("MCPServer is member of MCPGroup",
					"mcpServer", mcpServer.Name,
					"mcpGroup", group.Name)
				break // No need to check other servers in this group
			}
		}
	}

	// If no groups include this MCPServer, no VirtualMCPServers need reconciliation
	if len(affectedGroups) == 0 {
		ctxLogger.V(1).Info("MCPServer not a member of any MCPGroup, skipping VirtualMCPServer reconciliation",
			"mcpServer", mcpServer.Name)
		return nil
	}

	// Step 2: Find VirtualMCPServers that reference the affected MCPGroups
	vmcpList := &mcpv1alpha1.VirtualMCPServerList{}
	if err := r.List(ctx, vmcpList, client.InNamespace(mcpServer.Namespace)); err != nil {
		ctxLogger.Error(err, "Failed to list VirtualMCPServers for MCPServer watch")
		return nil
	}

	var requests []reconcile.Request
	for _, vmcp := range vmcpList.Items {
		// Only reconcile if this VirtualMCPServer references an affected MCPGroup
		if affectedGroups[vmcp.Spec.GroupRef.Name] {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      vmcp.Name,
					Namespace: vmcp.Namespace,
				},
			})
			ctxLogger.V(1).Info("Queuing VirtualMCPServer for reconciliation due to MCPServer change",
				"virtualMCPServer", vmcp.Name,
				"mcpGroup", vmcp.Spec.GroupRef.Name,
				"mcpServer", mcpServer.Name)
		}
	}

	ctxLogger.V(1).Info("Mapped MCPServer to VirtualMCPServers",
		"mcpServer", mcpServer.Name,
		"affectedGroups", len(affectedGroups),
		"virtualMCPServers", len(requests))

	return requests
}

// mapMCPRemoteProxyToVirtualMCPServer maps MCPRemoteProxy changes to VirtualMCPServer reconciliation requests.
// This function implements the same optimization as mapMCPServerToVirtualMCPServer to only reconcile
// VirtualMCPServers that are actually affected by the MCPRemoteProxy change.
//
// The optimization works by:
// 1. Finding all MCPGroups that include the changed MCPRemoteProxy (via Status.RemoteProxies)
// 2. Finding all VirtualMCPServers that reference those MCPGroups
// 3. Only reconciling those specific VirtualMCPServers
func (r *VirtualMCPServerReconciler) mapMCPRemoteProxyToVirtualMCPServer(
	ctx context.Context,
	obj client.Object,
) []reconcile.Request {
	mcpRemoteProxy, ok := obj.(*mcpv1alpha1.MCPRemoteProxy)
	if !ok {
		return nil
	}

	ctxLogger := log.FromContext(ctx)

	// Step 1: Find all MCPGroups that include this MCPRemoteProxy
	// MCPGroups track their member remote proxies in Status.RemoteProxies (populated by MCPGroup controller)
	mcpGroupList := &mcpv1alpha1.MCPGroupList{}
	if err := r.List(ctx, mcpGroupList, client.InNamespace(mcpRemoteProxy.Namespace)); err != nil {
		ctxLogger.Error(err, "Failed to list MCPGroups for MCPRemoteProxy watch")
		return nil
	}

	// Track which MCPGroups include this MCPRemoteProxy
	affectedGroups := make(map[string]bool)
	for _, group := range mcpGroupList.Items {
		// Check if this MCPRemoteProxy is in the group's remote proxy list
		for _, proxyName := range group.Status.RemoteProxies {
			if proxyName == mcpRemoteProxy.Name {
				affectedGroups[group.Name] = true
				ctxLogger.V(1).Info("MCPRemoteProxy is member of MCPGroup",
					"mcpRemoteProxy", mcpRemoteProxy.Name,
					"mcpGroup", group.Name)
				break // No need to check other proxies in this group
			}
		}
	}

	// If no groups include this MCPRemoteProxy, no VirtualMCPServers need reconciliation
	if len(affectedGroups) == 0 {
		ctxLogger.V(1).Info("MCPRemoteProxy not a member of any MCPGroup, skipping VirtualMCPServer reconciliation",
			"mcpRemoteProxy", mcpRemoteProxy.Name)
		return nil
	}

	// Step 2: Find VirtualMCPServers that reference the affected MCPGroups
	vmcpList := &mcpv1alpha1.VirtualMCPServerList{}
	if err := r.List(ctx, vmcpList, client.InNamespace(mcpRemoteProxy.Namespace)); err != nil {
		ctxLogger.Error(err, "Failed to list VirtualMCPServers for MCPRemoteProxy watch")
		return nil
	}

	var requests []reconcile.Request
	for _, vmcp := range vmcpList.Items {
		// Only reconcile if this VirtualMCPServer references an affected MCPGroup
		if affectedGroups[vmcp.Spec.GroupRef.Name] {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      vmcp.Name,
					Namespace: vmcp.Namespace,
				},
			})
			ctxLogger.V(1).Info("Queuing VirtualMCPServer for reconciliation due to MCPRemoteProxy change",
				"virtualMCPServer", vmcp.Name,
				"mcpGroup", vmcp.Spec.GroupRef.Name,
				"mcpRemoteProxy", mcpRemoteProxy.Name)
		}
	}

	ctxLogger.V(1).Info("Mapped MCPRemoteProxy to VirtualMCPServers",
		"mcpRemoteProxy", mcpRemoteProxy.Name,
		"affectedGroups", len(affectedGroups),
		"virtualMCPServers", len(requests))

	return requests
}

// mapExternalAuthConfigToVirtualMCPServer maps MCPExternalAuthConfig changes to VirtualMCPServer reconciliation requests
func (r *VirtualMCPServerReconciler) mapExternalAuthConfigToVirtualMCPServer(
	ctx context.Context,
	obj client.Object,
) []reconcile.Request {
	externalAuthConfig, ok := obj.(*mcpv1alpha1.MCPExternalAuthConfig)
	if !ok {
		return nil
	}

	vmcpList := &mcpv1alpha1.VirtualMCPServerList{}
	if err := r.List(ctx, vmcpList, client.InNamespace(externalAuthConfig.Namespace)); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list VirtualMCPServers for MCPExternalAuthConfig watch")
		return nil
	}

	var requests []reconcile.Request
	for _, vmcp := range vmcpList.Items {
		// Only reconcile VirtualMCPServers that actually reference this ExternalAuthConfig
		// This includes both inline references and discovered references (via MCPServers)
		if r.vmcpReferencesExternalAuthConfig(ctx, &vmcp, externalAuthConfig.Name) {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      vmcp.Name,
					Namespace: vmcp.Namespace,
				},
			})
		}
	}

	return requests
}

// mapToolConfigToVirtualMCPServer maps MCPToolConfig changes to VirtualMCPServer reconciliation requests
func (r *VirtualMCPServerReconciler) mapToolConfigToVirtualMCPServer(ctx context.Context, obj client.Object) []reconcile.Request {
	toolConfig, ok := obj.(*mcpv1alpha1.MCPToolConfig)
	if !ok {
		return nil
	}

	vmcpList := &mcpv1alpha1.VirtualMCPServerList{}
	if err := r.List(ctx, vmcpList, client.InNamespace(toolConfig.Namespace)); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list VirtualMCPServers for MCPToolConfig watch")
		return nil
	}

	var requests []reconcile.Request
	for _, vmcp := range vmcpList.Items {
		if r.vmcpReferencesToolConfig(&vmcp, toolConfig.Name) {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      vmcp.Name,
					Namespace: vmcp.Namespace,
				},
			})
		}
	}

	return requests
}

// vmcpReferencesToolConfig checks if a VirtualMCPServer references the given MCPToolConfig
func (*VirtualMCPServerReconciler) vmcpReferencesToolConfig(vmcp *mcpv1alpha1.VirtualMCPServer, toolConfigName string) bool {
	if vmcp.Spec.Aggregation == nil || len(vmcp.Spec.Aggregation.Tools) == 0 {
		return false
	}

	for _, tc := range vmcp.Spec.Aggregation.Tools {
		if tc.ToolConfigRef != nil && tc.ToolConfigRef.Name == toolConfigName {
			return true
		}
	}

	return false
}

// vmcpReferencesExternalAuthConfig checks if a VirtualMCPServer references the given MCPExternalAuthConfig.
// It checks both inline references (in outgoingAuth spec) and discovered references (via MCPServers in the group).
func (r *VirtualMCPServerReconciler) vmcpReferencesExternalAuthConfig(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	authConfigName string,
) bool {
	if vmcp.Spec.OutgoingAuth == nil {
		return false
	}

	// Check inline references in outgoing auth configuration
	// Check default backend auth configuration
	if vmcp.Spec.OutgoingAuth.Default != nil &&
		vmcp.Spec.OutgoingAuth.Default.ExternalAuthConfigRef != nil &&
		vmcp.Spec.OutgoingAuth.Default.ExternalAuthConfigRef.Name == authConfigName {
		return true
	}

	// Check per-backend auth configurations
	for _, backendAuth := range vmcp.Spec.OutgoingAuth.Backends {
		if backendAuth.ExternalAuthConfigRef != nil &&
			backendAuth.ExternalAuthConfigRef.Name == authConfigName {
			return true
		}
	}

	// Check discovered references when source is "discovered"
	// When using discovered mode, auth configs are referenced through MCPServers, not inline
	if vmcp.Spec.OutgoingAuth.Source == OutgoingAuthSourceDiscovered {
		if r.mcpGroupBackendsReferenceExternalAuthConfig(ctx, vmcp, authConfigName) {
			return true
		}
	}

	return false
}

// mcpGroupBackendsReferenceExternalAuthConfig checks if any MCPServers or MCPRemoteProxies
// in the VirtualMCPServer's group reference the given MCPExternalAuthConfig
func (r *VirtualMCPServerReconciler) mcpGroupBackendsReferenceExternalAuthConfig(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	authConfigName string,
) bool {
	ctxLogger := log.FromContext(ctx)

	// Get the MCPGroup to verify it exists
	mcpGroup := &mcpv1alpha1.MCPGroup{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      vmcp.Spec.GroupRef.Name,
		Namespace: vmcp.Namespace,
	}, mcpGroup)
	if err != nil {
		// If we can't get the group, we can't determine if it references the auth config
		// Return false to avoid false positives
		ctxLogger.Error(err, "Failed to get MCPGroup for ExternalAuthConfig reference check",
			"group", vmcp.Spec.GroupRef.Name,
			"vmcp", vmcp.Name)
		return false
	}

	listOpts := []client.ListOption{
		client.InNamespace(vmcp.Namespace),
		client.MatchingFields{"spec.groupRef": mcpGroup.Name},
	}

	// List all MCPServers in the group using field selector (same as MCPGroup controller)
	mcpServerList := &mcpv1alpha1.MCPServerList{}
	err = r.List(ctx, mcpServerList, listOpts...)
	if err != nil {
		ctxLogger.Error(err, "Failed to list MCPServers for ExternalAuthConfig reference check",
			"group", mcpGroup.Name)
		return false
	}

	// Check if any MCPServer references the ExternalAuthConfig
	for _, mcpServer := range mcpServerList.Items {
		if mcpServer.Spec.ExternalAuthConfigRef != nil &&
			mcpServer.Spec.ExternalAuthConfigRef.Name == authConfigName {
			return true
		}
	}

	// List all MCPRemoteProxies in the group
	mcpRemoteProxyList := &mcpv1alpha1.MCPRemoteProxyList{}
	err = r.List(ctx, mcpRemoteProxyList, listOpts...)
	if err != nil {
		ctxLogger.Error(err, "Failed to list MCPRemoteProxies for ExternalAuthConfig reference check",
			"group", mcpGroup.Name)
		return false
	}

	// Check if any MCPRemoteProxy references the ExternalAuthConfig
	for _, mcpRemoteProxy := range mcpRemoteProxyList.Items {
		if mcpRemoteProxy.Spec.ExternalAuthConfigRef != nil &&
			mcpRemoteProxy.Spec.ExternalAuthConfigRef.Name == authConfigName {
			return true
		}
	}

	return false
}

// mapCompositeToolDefinitionToVirtualMCPServer maps VirtualMCPCompositeToolDefinition changes to
// VirtualMCPServer reconciliation requests
func (r *VirtualMCPServerReconciler) mapCompositeToolDefinitionToVirtualMCPServer(
	ctx context.Context,
	obj client.Object,
) []reconcile.Request {
	compositeToolDef, ok := obj.(*mcpv1alpha1.VirtualMCPCompositeToolDefinition)
	if !ok {
		return nil
	}

	vmcpList := &mcpv1alpha1.VirtualMCPServerList{}
	if err := r.List(ctx, vmcpList, client.InNamespace(compositeToolDef.Namespace)); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list VirtualMCPServers for VirtualMCPCompositeToolDefinition watch")
		return nil
	}

	var requests []reconcile.Request
	for _, vmcp := range vmcpList.Items {
		if r.vmcpReferencesCompositeToolDefinition(&vmcp, compositeToolDef.Name) {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      vmcp.Name,
					Namespace: vmcp.Namespace,
				},
			})
		}
	}

	return requests
}

// vmcpReferencesCompositeToolDefinition checks if a VirtualMCPServer references the given VirtualMCPCompositeToolDefinition
func (*VirtualMCPServerReconciler) vmcpReferencesCompositeToolDefinition(
	vmcp *mcpv1alpha1.VirtualMCPServer,
	compositeToolDefName string,
) bool {
	if len(vmcp.Spec.CompositeToolRefs) == 0 {
		return false
	}

	for _, ref := range vmcp.Spec.CompositeToolRefs {
		if ref.Name == compositeToolDefName {
			return true
		}
	}

	return false
}

// BackendHealthInfo contains health information for a single backend
type BackendHealthInfo struct {
	Status        string
	LastCheckTime time.Time
}

// BackendHealthStatusResponse represents the health status response from the vmcp health API
type BackendHealthStatusResponse struct {
	Backends []struct {
		BackendID           string    `json:"backendId"`
		Status              string    `json:"status"`
		ConsecutiveFailures int       `json:"consecutiveFailures"`
		LastCheckTime       time.Time `json:"lastCheckTime"`
		LastError           string    `json:"lastError,omitempty"`
		LastTransitionTime  time.Time `json:"lastTransitionTime"`
	} `json:"backends"`
}

// queryVMCPHealthStatus queries the vmcp health endpoint and returns backend health information.
// Returns nil if health monitoring is not enabled or if there's an error.
func (*VirtualMCPServerReconciler) queryVMCPHealthStatus(
	ctx context.Context,
	vmcpURL string,
) map[string]*BackendHealthInfo {
	ctxLogger := log.FromContext(ctx)

	// Construct health endpoint URL
	healthURL := fmt.Sprintf("%s/api/backends/health", vmcpURL)

	// Create HTTP client with timeout
	httpClient := &http.Client{
		Timeout: 5 * time.Second,
	}

	// Create and execute request
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		ctxLogger.V(1).Error(err, "Failed to create health check request", "url", healthURL)
		return nil
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		ctxLogger.V(1).Error(err, "Failed to query vmcp health endpoint", "url", healthURL)
		return nil
	}
	defer resp.Body.Close()

	// Check status code
	if resp.StatusCode == http.StatusServiceUnavailable {
		// Health monitoring is not enabled on the vmcp server
		ctxLogger.V(1).Info("Health monitoring not enabled on vmcp server", "url", healthURL)
		return nil
	}

	if resp.StatusCode != http.StatusOK {
		ctxLogger.V(1).Info("Unexpected status code from vmcp health endpoint",
			"url", healthURL,
			"status_code", resp.StatusCode)
		return nil
	}

	// Parse response
	var healthResp BackendHealthStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&healthResp); err != nil {
		ctxLogger.V(1).Error(err, "Failed to decode health response", "url", healthURL)
		return nil
	}

	// Convert to map of backendID -> health info
	healthStatus := make(map[string]*BackendHealthInfo)
	for _, backend := range healthResp.Backends {
		healthStatus[backend.BackendID] = &BackendHealthInfo{
			Status:        backend.Status,
			LastCheckTime: backend.LastCheckTime,
		}
	}

	ctxLogger.V(1).Info("Retrieved health status from vmcp server",
		"url", healthURL,
		"backend_count", len(healthStatus))

	return healthStatus
}

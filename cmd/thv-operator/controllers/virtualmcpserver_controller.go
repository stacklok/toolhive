// Package controllers contains the reconciliation logic for the VirtualMCPServer custom resource.
// It handles the creation, update, and deletion of Virtual MCP Servers in Kubernetes.
package controllers

import (
	"context"
	"fmt"
	"maps"
	"reflect"
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
	"github.com/stacklok/toolhive/pkg/vmcp/workloads"
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

	// Validate GroupRef
	if err := r.validateGroupRef(ctx, vmcp, statusManager); err != nil {
		// Apply status changes before returning error
		if err := r.applyStatusUpdates(ctx, vmcp, statusManager); err != nil {
			ctxLogger.Error(err, "Failed to apply status updates after GroupRef validation error")
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
			"BackendsDiscovered",
			"BackendDiscoveryFailed",
			fmt.Sprintf("Failed to discover backends: %v", err),
			metav1.ConditionFalse,
		)
	} else {
		statusManager.SetDiscoveredBackends(discoveredBackends)
		statusManager.SetCondition(
			"BackendsDiscovered",
			"BackendsDiscoveredSuccessfully",
			fmt.Sprintf("Discovered %d backends", len(discoveredBackends)),
			metav1.ConditionTrue,
		)
		ctxLogger.Info("Discovered backends", "count", len(discoveredBackends))
	}

	// Update status based on pod health
	if err := r.updateVirtualMCPServerStatus(ctx, vmcp, statusManager); err != nil {
		ctxLogger.Error(err, "Failed to update VirtualMCPServer status")
		return ctrl.Result{}, err
	}

	// Apply all collected status changes in a single batch update
	if err := r.applyStatusUpdates(ctx, vmcp, statusManager); err != nil {
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
		// Requeue to check again later
		return fmt.Errorf("MCPGroup %s is not ready", vmcp.Spec.GroupRef.Name)
	}

	// GroupRef is valid and ready
	statusManager.SetGroupRefValidatedCondition(
		mcpv1alpha1.ConditionReasonVirtualMCPServerGroupRefValid,
		fmt.Sprintf("MCPGroup %s is valid and ready", vmcp.Spec.GroupRef.Name),
		metav1.ConditionTrue,
	)

	return nil
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

	// Ensure RBAC resources
	if err := r.ensureRBACResources(ctx, vmcp); err != nil {
		ctxLogger.Error(err, "Failed to ensure RBAC resources")
		return err
	}

	// Ensure vmcp Config ConfigMap
	if err := r.ensureVmcpConfigConfigMap(ctx, vmcp); err != nil {
		ctxLogger.Error(err, "Failed to ensure vmcp Config ConfigMap")
		return err
	}

	// Ensure Deployment
	if result, err := r.ensureDeployment(ctx, vmcp); err != nil {
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
		dep := r.deploymentForVirtualMCPServer(ctx, vmcp, vmcpConfigChecksum)
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
	if r.deploymentNeedsUpdate(ctx, deployment, vmcp, vmcpConfigChecksum) {
		newDeployment := r.deploymentForVirtualMCPServer(ctx, vmcp, vmcpConfigChecksum)
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
) bool {
	if deployment == nil || vmcp == nil {
		return true
	}

	if len(deployment.Spec.Template.Spec.Containers) == 0 {
		return true
	}

	if r.containerNeedsUpdate(ctx, deployment, vmcp) {
		return true
	}

	if r.deploymentMetadataNeedsUpdate(deployment, vmcp) {
		return true
	}

	if r.podTemplateMetadataNeedsUpdate(deployment, vmcp, vmcpConfigChecksum) {
		return true
	}

	return false
}

// containerNeedsUpdate checks if the container specification has changed
func (r *VirtualMCPServerReconciler) containerNeedsUpdate(
	ctx context.Context,
	deployment *appsv1.Deployment,
	vmcp *mcpv1alpha1.VirtualMCPServer,
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

	// Check if environment variables have changed
	expectedEnv := r.buildEnvVarsForVmcp(ctx, vmcp)
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

	if !maps.Equal(deployment.Labels, expectedLabels) {
		return true
	}

	if !maps.Equal(deployment.Annotations, expectedAnnotations) {
		return true
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
//
//nolint:gocyclo // Status reconciliation requires multiple conditions for pod phases and backend health
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

	// Update the status based on the pod status
	var running, pending, failed int
	for _, pod := range podList.Items {
		switch pod.Status.Phase {
		case corev1.PodRunning:
			running++
		case corev1.PodPending:
			pending++
		case corev1.PodFailed:
			failed++
		case corev1.PodSucceeded:
			running++
		case corev1.PodUnknown:
			pending++
		}
	}

	// Update the status based on pod health
	if running > 0 {
		statusManager.SetPhase(mcpv1alpha1.VirtualMCPServerPhaseReady)
		statusManager.SetMessage("Virtual MCP server is running")
		statusManager.SetReadyCondition("DeploymentReady", "Deployment is ready", metav1.ConditionTrue)
	} else if pending > 0 {
		statusManager.SetPhase(mcpv1alpha1.VirtualMCPServerPhasePending)
		statusManager.SetMessage("Virtual MCP server is starting")
		statusManager.SetReadyCondition("DeploymentNotReady", "Deployment is not yet ready", metav1.ConditionFalse)
	} else if failed > 0 {
		statusManager.SetPhase(mcpv1alpha1.VirtualMCPServerPhaseFailed)
		statusManager.SetMessage("Virtual MCP server failed to start")
		statusManager.SetReadyCondition("DeploymentFailed", "Deployment failed", metav1.ConditionFalse)
	} else {
		statusManager.SetPhase(mcpv1alpha1.VirtualMCPServerPhasePending)
		statusManager.SetMessage("No pods found for Virtual MCP server")
		statusManager.SetReadyCondition("DeploymentNotReady", "No pods found", metav1.ConditionFalse)
	}

	// Update ObservedGeneration to reflect that we've processed this generation
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

// discoverBackends discovers all MCPServers in the referenced MCPGroup and returns
// a list of DiscoveredBackend objects with their current status.
// This reuses the existing workload discovery code from pkg/vmcp/workloads.
func (r *VirtualMCPServerReconciler) discoverBackends(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) ([]mcpv1alpha1.DiscoveredBackend, error) {
	ctxLogger := log.FromContext(ctx)

	// Create groups manager using the controller's client and VirtualMCPServer's namespace
	groupsManager := groups.NewCRDManager(r.Client, vmcp.Namespace)

	// Create K8S workload discoverer for the VirtualMCPServer's namespace
	workloadDiscoverer := workloads.NewK8SDiscovererWithClient(r.Client, vmcp.Namespace)

	// Use the aggregator's unified backend discoverer to reuse discovery logic
	// Pass nil for authConfig since we'll extract auth config from MCPServer directly
	backendDiscoverer := aggregator.NewUnifiedBackendDiscoverer(workloadDiscoverer, groupsManager, nil)

	// Discover backends using the aggregator
	backends, err := backendDiscoverer.Discover(ctx, vmcp.Spec.GroupRef.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to discover backends: %w", err)
	}

	// Get all workload names to track backends that weren't accessible
	workloadNames, err := workloadDiscoverer.ListWorkloadsInGroup(ctx, vmcp.Spec.GroupRef.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to list workloads in group: %w", err)
	}

	// Create a map of discovered backend names for quick lookup
	discoveredBackendMap := make(map[string]*vmcptypes.Backend, len(backends))
	for i := range backends {
		discoveredBackendMap[backends[i].Name] = &backends[i]
	}

	discoveredBackends := make([]mcpv1alpha1.DiscoveredBackend, 0, len(workloadNames))
	now := metav1.Now()

	// Convert vmcp.Backend to DiscoveredBackend for all workloads in the group
	for _, workloadName := range workloadNames {
		backend, found := discoveredBackendMap[workloadName]
		if !found {
			// Workload exists but is not accessible (no URL or error)
			discoveredBackends = append(discoveredBackends, mcpv1alpha1.DiscoveredBackend{
				Name:            workloadName,
				Status:          "unavailable",
				LastHealthCheck: now,
			})
			continue
		}

		// Convert vmcp.Backend to DiscoveredBackend
		// Map health status from BackendHealthStatus to string
		var backendStatus string
		switch backend.HealthStatus {
		case vmcptypes.BackendHealthy:
			backendStatus = "ready"
		case vmcptypes.BackendUnhealthy, vmcptypes.BackendUnauthenticated:
			backendStatus = "unavailable"
		case vmcptypes.BackendDegraded:
			backendStatus = "degraded"
		case vmcptypes.BackendUnknown:
			backendStatus = "unknown"
		default:
			backendStatus = "unknown"
		}

		// Extract auth config reference directly from MCPServer
		// (Backend.AuthMetadata is populated later by aggregator, so we query MCPServer directly)
		authConfigRef := ""
		authType := ""
		mcpServer := &mcpv1alpha1.MCPServer{}
		if err := r.Get(ctx, types.NamespacedName{Name: workloadName, Namespace: vmcp.Namespace}, mcpServer); err == nil {
			if mcpServer.Spec.ExternalAuthConfigRef != nil {
				authConfigRef = mcpServer.Spec.ExternalAuthConfigRef.Name
				authType = mcpv1alpha1.BackendAuthTypeExternalAuthConfigRef
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
		if r.vmcpReferencesExternalAuthConfig(&vmcp, externalAuthConfig.Name) {
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

// vmcpReferencesExternalAuthConfig checks if a VirtualMCPServer references the given MCPExternalAuthConfig
func (*VirtualMCPServerReconciler) vmcpReferencesExternalAuthConfig(
	vmcp *mcpv1alpha1.VirtualMCPServer,
	authConfigName string,
) bool {
	if vmcp.Spec.OutgoingAuth == nil {
		return false
	}

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

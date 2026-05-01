// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"errors"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/imagepullsecrets"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/registryapi"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/registryapi/config"
)

// Default timing constants for the controller
const (
	// DefaultControllerRetryAfterConstant is the constant default retry interval for controller operations that fail
	DefaultControllerRetryAfterConstant = time.Minute * 5
)

// Configurable timing variables for testing
var (
	// DefaultControllerRetryAfter is the configurable default retry interval for controller operations that fail
	// This can be modified in tests to speed up retry behavior
	DefaultControllerRetryAfter = DefaultControllerRetryAfterConstant
)

// MCPRegistryReconciler reconciles a MCPRegistry object
type MCPRegistryReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Registry API manager handles API deployment operations
	registryAPIManager registryapi.Manager
}

// NewMCPRegistryReconciler creates a new MCPRegistryReconciler with required
// dependencies. imagePullSecretsDefaults are cluster-wide pull-secret defaults
// from the operator chart that are merged with the per-CR list at registry-api
// workload-construction time.
func NewMCPRegistryReconciler(
	k8sClient client.Client,
	scheme *runtime.Scheme,
	imagePullSecretsDefaults imagepullsecrets.Defaults,
) *MCPRegistryReconciler {
	registryAPIManager := registryapi.NewManager(k8sClient, scheme, imagePullSecretsDefaults)
	return &MCPRegistryReconciler{
		Client:             k8sClient,
		Scheme:             scheme,
		registryAPIManager: registryAPIManager,
	}
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpregistries,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpregistries/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpregistries/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
//
// For creating registry-api deployment and service
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
//
// For creating registry-api RBAC resources
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete
//
// For granting registry-api permissions (operator must have these to grant them via Role)
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpservers;mcpremoteproxies;virtualmcpservers,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes;gateways,verbs=get;list;watch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
//nolint:gocyclo // Complex reconciliation logic requires multiple conditions
func (r *MCPRegistryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	// 1. Fetch MCPRegistry instance
	mcpRegistry := &mcpv1beta1.MCPRegistry{}
	err := r.Get(ctx, req.NamespacedName, mcpRegistry)
	if err != nil {
		if kerrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Return and don't requeue
			ctxLogger.Info("MCPRegistry resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		ctxLogger.Error(err, "Failed to get MCPRegistry")
		return ctrl.Result{}, err
	}

	ctxLogger.Info("Reconciling MCPRegistry", "MCPRegistry.Name", mcpRegistry.Name,
		"phase", mcpRegistry.Status.Phase, "url", mcpRegistry.Status.URL)

	// Validate PodTemplateSpec early - before other operations
	var podTemplateCondition *metav1.Condition
	if mcpRegistry.HasPodTemplateSpec() {
		valid, cond := r.validatePodTemplate(mcpRegistry)
		podTemplateCondition = cond
		if !valid {
			// Write status immediately for the failure case since we return early
			mcpRegistry.Status.Phase = mcpv1beta1.MCPRegistryPhaseFailed
			mcpRegistry.Status.Message = fmt.Sprintf("Invalid PodTemplateSpec: %v", cond.Message)
			meta.SetStatusCondition(&mcpRegistry.Status.Conditions, *cond)
			if statusErr := r.Status().Update(ctx, mcpRegistry); statusErr != nil {
				ctxLogger.Error(statusErr, "Failed to update MCPRegistry status with PodTemplateSpec validation")
			}
			// Invalid PodTemplateSpec - return without error to avoid infinite retries
			// The user must fix the spec and the next reconciliation will retry
			return ctrl.Result{}, nil
		}
	}

	// Validate spec fields (reserved names, mount paths, pgpassSecretRef)
	if err := validateSpec(mcpRegistry); err != nil {
		mcpRegistry.Status.Phase = mcpv1beta1.MCPRegistryPhaseFailed
		mcpRegistry.Status.Message = fmt.Sprintf("Spec validation failed: %v", err)
		setRegistryReadyCondition(mcpRegistry, metav1.ConditionFalse,
			"ValidationFailed", err.Error())
		if statusErr := r.Status().Update(ctx, mcpRegistry); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPRegistry status with spec validation error")
		}
		return ctrl.Result{}, nil
	}

	// 2. Handle deletion if DeletionTimestamp is set
	if mcpRegistry.GetDeletionTimestamp() != nil {
		// The object is being deleted
		if controllerutil.ContainsFinalizer(mcpRegistry, "mcpregistry.toolhive.stacklok.dev/finalizer") {
			// Run finalization logic. If the finalization logic fails,
			// don't remove the finalizer so that we can retry during the next reconciliation.
			if err := r.finalizeMCPRegistry(ctx, mcpRegistry); err != nil {
				ctxLogger.Error(err, "Reconciliation completed with error while finalizing MCPRegistry",
					"MCPRegistry.Name", mcpRegistry.Name)
				return ctrl.Result{}, err
			}

			// Remove the finalizer. Once all finalizers have been removed, the object will be deleted.
			controllerutil.RemoveFinalizer(mcpRegistry, "mcpregistry.toolhive.stacklok.dev/finalizer")
			err := r.Update(ctx, mcpRegistry)
			if err != nil {
				ctxLogger.Error(err, "Reconciliation completed with error while removing finalizer",
					"MCPRegistry.Name", mcpRegistry.Name)
				return ctrl.Result{}, err
			}
		}
		ctxLogger.Info("Reconciliation of deleted MCPRegistry completed successfully",
			"MCPRegistry.Name", mcpRegistry.Name,
			"phase", mcpRegistry.Status.Phase)
		return ctrl.Result{}, nil
	}

	// Add finalizer for this CR
	if !controllerutil.ContainsFinalizer(mcpRegistry, "mcpregistry.toolhive.stacklok.dev/finalizer") {
		controllerutil.AddFinalizer(mcpRegistry, "mcpregistry.toolhive.stacklok.dev/finalizer")
		err = r.Update(ctx, mcpRegistry)
		if err != nil {
			ctxLogger.Error(err, "Reconciliation completed with error while adding finalizer",
				"MCPRegistry.Name", mcpRegistry.Name)
			return ctrl.Result{}, err
		}
		ctxLogger.Info("Reconciliation completed successfully after adding finalizer",
			"MCPRegistry.Name", mcpRegistry.Name)
		return ctrl.Result{}, nil
	}

	// 3. Reconcile API service - capture error for status update
	var reconcileErr error
	if apiErr := r.registryAPIManager.ReconcileAPIService(ctx, mcpRegistry); apiErr != nil {
		ctxLogger.Error(apiErr, "Failed to reconcile API service")
		reconcileErr = apiErr
	}

	// 4. Determine and persist status
	isReady, statusUpdateErr := r.updateRegistryStatus(ctx, mcpRegistry, reconcileErr, podTemplateCondition)
	if statusUpdateErr != nil {
		ctxLogger.Error(statusUpdateErr, "Failed to update registry status")
		// Return the status update error only if there was no main reconciliation error
		if reconcileErr == nil {
			reconcileErr = statusUpdateErr
		}
	}

	// 5. Determine requeue based on phase
	result := ctrl.Result{}
	if reconcileErr == nil && !isReady {
		ctxLogger.Info("API not ready yet, scheduling requeue to check readiness")
		result.RequeueAfter = time.Second * 30
	}

	// Log reconciliation completion
	if reconcileErr != nil {
		ctxLogger.Error(reconcileErr, "Reconciliation completed with error",
			"MCPRegistry.Name", mcpRegistry.Name, "requeueAfter", result.RequeueAfter)
	} else {
		ctxLogger.Info("Reconciliation completed successfully",
			"MCPRegistry.Name", mcpRegistry.Name,
			"phase", mcpRegistry.Status.Phase,
			"requeueAfter", result.RequeueAfter)
	}

	return result, reconcileErr
}

// SetupWithManager sets up the controller with the Manager.
func (r *MCPRegistryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1beta1.MCPRegistry{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Complete(r)
}

// updateRegistryStatus determines the MCPRegistry phase from the API deployment state
// and persists it with a single status update. Returns whether the API is ready and any
// error from the status update.
func (r *MCPRegistryReconciler) updateRegistryStatus(
	ctx context.Context, mcpRegistry *mcpv1beta1.MCPRegistry, reconcileErr error, podTemplateCond *metav1.Condition,
) (bool, error) {
	// Refetch the latest version to avoid conflicts
	latest := &mcpv1beta1.MCPRegistry{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(mcpRegistry), latest); err != nil {
		return false, fmt.Errorf("failed to fetch latest MCPRegistry version: %w", err)
	}

	var isReady bool

	if reconcileErr != nil {
		latest.Status.Phase = mcpv1beta1.MCPRegistryPhaseFailed
		latest.Status.ReadyReplicas = 0
		// Use structured error fields if available
		var apiErr *registryapi.Error
		if errors.As(reconcileErr, &apiErr) {
			latest.Status.Message = apiErr.Message
			setRegistryReadyCondition(latest, metav1.ConditionFalse, apiErr.ConditionReason, apiErr.Message)
		} else {
			latest.Status.Message = reconcileErr.Error()
			setRegistryReadyCondition(latest, metav1.ConditionFalse,
				mcpv1beta1.ConditionReasonRegistryNotReady, reconcileErr.Error())
		}
	} else {
		var readyReplicas int32
		isReady, readyReplicas = r.registryAPIManager.GetAPIStatus(ctx, mcpRegistry)
		latest.Status.ReadyReplicas = readyReplicas

		if isReady {
			endpoint := fmt.Sprintf("http://%s.%s:8080",
				mcpRegistry.GetAPIResourceName(), mcpRegistry.Namespace)
			latest.Status.Phase = mcpv1beta1.MCPRegistryPhaseReady
			latest.Status.Message = "Registry API is ready and serving requests"
			latest.Status.URL = endpoint
			setRegistryReadyCondition(latest, metav1.ConditionTrue,
				mcpv1beta1.ConditionReasonRegistryReady, "Registry API is ready and serving requests")
		} else {
			latest.Status.Phase = mcpv1beta1.MCPRegistryPhasePending
			latest.Status.Message = "Registry API deployment is not ready yet"
			setRegistryReadyCondition(latest, metav1.ConditionFalse,
				mcpv1beta1.ConditionReasonRegistryNotReady, "Registry API deployment is not ready yet")
		}
	}

	// Apply PodTemplate condition if present
	if podTemplateCond != nil {
		meta.SetStatusCondition(&latest.Status.Conditions, *podTemplateCond)
	}

	latest.Status.ObservedGeneration = latest.Generation
	if err := r.Status().Update(ctx, latest); err != nil {
		return false, err
	}
	return isReady, nil
}

// setRegistryReadyCondition sets the top-level Ready condition on an MCPRegistry.
func setRegistryReadyCondition(registry *mcpv1beta1.MCPRegistry, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&registry.Status.Conditions, metav1.Condition{
		Type:               mcpv1beta1.ConditionTypeReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: registry.Generation,
	})
}

// finalizeMCPRegistry performs the finalizer logic for the MCPRegistry
func (r *MCPRegistryReconciler) finalizeMCPRegistry(ctx context.Context, registry *mcpv1beta1.MCPRegistry) error {
	ctxLogger := log.FromContext(ctx)

	// Update the MCPRegistry status to indicate termination - immediate update needed since object is being deleted
	registry.Status.Phase = mcpv1beta1.MCPRegistryPhaseTerminating
	registry.Status.Message = "MCPRegistry is being terminated"
	setRegistryReadyCondition(registry, metav1.ConditionFalse,
		mcpv1beta1.ConditionReasonRegistryNotReady, "MCPRegistry is being terminated")
	if err := r.Status().Update(ctx, registry); err != nil {
		ctxLogger.Error(err, "Failed to update MCPRegistry status during finalization")
		return err
	}

	ctxLogger.Info("MCPRegistry finalization completed", "registry", registry.Name)
	return nil
}

// validateSpec validates MCPRegistry spec fields for reserved resource name
// conflicts, mount path collisions, and pgpassSecretRef completeness. Returns
// nil if the spec is valid or a descriptive error if validation fails. CEL
// admission rules cover the common cases; this is defense-in-depth inside the
// reconciler.
func validateSpec(mcpRegistry *mcpv1beta1.MCPRegistry) error {
	spec := &mcpRegistry.Spec

	// Parse user PodTemplateSpec once for subsequent checks
	var userPTS *corev1.PodTemplateSpec
	if mcpRegistry.HasPodTemplateSpec() {
		parsed, err := registryapi.ParsePodTemplateSpec(mcpRegistry.GetPodTemplateSpecRaw())
		if err == nil && parsed != nil {
			userPTS = parsed
		}
	}

	if err := validateReservedNames(spec, userPTS); err != nil {
		return err
	}

	if err := validateMountPathCollisions(spec, userPTS); err != nil {
		return err
	}

	return validatePGPassSecretRef(spec.PGPassSecretRef)
}

// validatePGPassSecretRef checks that pgpassSecretRef has required name and key when set.
func validatePGPassSecretRef(ref *corev1.SecretKeySelector) error {
	if ref == nil {
		return nil
	}
	if ref.Name == "" {
		return fmt.Errorf("pgpassSecretRef.name is required")
	}
	if ref.Key == "" {
		return fmt.Errorf("pgpassSecretRef.key is required")
	}
	return nil
}

// validateReservedNames checks that user-provided volumes and init containers do not
// collide with operator-reserved names.
func validateReservedNames(spec *mcpv1beta1.MCPRegistrySpec, userPTS *corev1.PodTemplateSpec) error {
	reservedVolumeNames := map[string]bool{
		registryapi.RegistryServerConfigVolumeName: true,
	}
	if spec.PGPassSecretRef != nil {
		reservedVolumeNames[registryapi.PGPassSecretVolumeName] = true
		reservedVolumeNames[registryapi.PGPassVolumeName] = true
	}

	volumes, err := spec.ParseVolumes()
	if err != nil {
		return fmt.Errorf("invalid volumes: %w", err)
	}
	for _, vol := range volumes {
		if reservedVolumeNames[vol.Name] {
			return fmt.Errorf("volume name '%s' is reserved by the operator", vol.Name)
		}
	}

	if userPTS != nil {
		for _, vol := range userPTS.Spec.Volumes {
			if reservedVolumeNames[vol.Name] {
				return fmt.Errorf("volume name '%s' is reserved by the operator", vol.Name)
			}
		}

		if spec.PGPassSecretRef != nil {
			for _, ic := range userPTS.Spec.InitContainers {
				if ic.Name == registryapi.PGPassInitContainerName {
					return fmt.Errorf(
						"init container name '%s' is reserved by the operator when pgpassSecretRef is set",
						registryapi.PGPassInitContainerName)
				}
			}
		}
	}

	return nil
}

// validateMountPathCollisions detects duplicate mount paths across operator-generated mounts,
// spec.VolumeMounts, and user PodTemplateSpec container mounts.
func validateMountPathCollisions(spec *mcpv1beta1.MCPRegistrySpec, userPTS *corev1.PodTemplateSpec) error {
	mountPaths := make(map[string]struct{})

	// Operator-generated mounts
	mountPaths[config.RegistryServerConfigFilePath] = struct{}{}
	if spec.PGPassSecretRef != nil {
		mountPaths[registryapi.PGPassAppUserMountPath] = struct{}{}
	}

	mounts, err := spec.ParseVolumeMounts()
	if err != nil {
		return fmt.Errorf("invalid volumeMounts: %w", err)
	}
	for _, mount := range mounts {
		if _, exists := mountPaths[mount.MountPath]; exists {
			return fmt.Errorf("duplicate mount path '%s'", mount.MountPath)
		}
		mountPaths[mount.MountPath] = struct{}{}
	}

	if userPTS != nil {
		for i := range userPTS.Spec.Containers {
			if userPTS.Spec.Containers[i].Name == registryapi.RegistryAPIContainerName {
				for _, mount := range userPTS.Spec.Containers[i].VolumeMounts {
					if _, exists := mountPaths[mount.MountPath]; exists {
						return fmt.Errorf("duplicate mount path '%s'", mount.MountPath)
					}
					mountPaths[mount.MountPath] = struct{}{}
				}
				break
			}
		}
	}

	return nil
}

// validatePodTemplate validates the PodTemplateSpec and returns a condition reflecting the result.
// Returns true if validation passes, and a condition to apply during the next status update.
func (*MCPRegistryReconciler) validatePodTemplate(
	mcpRegistry *mcpv1beta1.MCPRegistry,
) (bool, *metav1.Condition) {
	err := registryapi.ValidatePodTemplateSpec(mcpRegistry.GetPodTemplateSpecRaw())
	if err != nil {
		return false, &metav1.Condition{
			Type:               mcpv1beta1.ConditionPodTemplateValid,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: mcpRegistry.Generation,
			Reason:             mcpv1beta1.ConditionReasonPodTemplateInvalid,
			Message:            fmt.Sprintf("Failed to parse PodTemplateSpec: %v. Deployment blocked until fixed.", err),
		}
	}
	return true, &metav1.Condition{
		Type:               mcpv1beta1.ConditionPodTemplateValid,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: mcpRegistry.Generation,
		Reason:             mcpv1beta1.ConditionReasonPodTemplateValid,
		Message:            "PodTemplateSpec is valid",
	}
}

package controllers

import (
	"context"
	"fmt"
	"os"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/sources"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/sync"
)

const (
	// registryAPIContainerName is the name of the registry-api container in deployments
	// nolint:unused // Will be used when ensureDeployment method is integrated
	registryAPIContainerName = "registry-api"
)

// getRegistryAPIImage returns the container image for the registry API.
// It checks the TOOLHIVE_REGISTRY_API_IMAGE environment variable first,
// falling back to the default image if not set.
// nolint:unused // Will be used when deployment functionality is integrated
func getRegistryAPIImage() string {
	if image := os.Getenv("TOOLHIVE_REGISTRY_API_IMAGE"); image != "" {
		return image
	}
	return "ghcr.io/stacklok/toolhive/thv-registry-api:latest"
}

// MCPRegistryReconciler reconciles a MCPRegistry object
type MCPRegistryReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Sync manager handles all sync operations
	syncManager    sync.Manager
	storageManager sources.StorageManager
}

// NewMCPRegistryReconciler creates a new MCPRegistryReconciler with required dependencies
func NewMCPRegistryReconciler(k8sClient client.Client, scheme *runtime.Scheme) *MCPRegistryReconciler {
	sourceHandlerFactory := sources.NewSourceHandlerFactory(k8sClient)
	storageManager := sources.NewConfigMapStorageManager(k8sClient, scheme)
	syncManager := sync.NewDefaultSyncManager(k8sClient, scheme, sourceHandlerFactory, storageManager)

	return &MCPRegistryReconciler{
		Client:         k8sClient,
		Scheme:         scheme,
		syncManager:    syncManager,
		storageManager: storageManager,
	}
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpregistries,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpregistries/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpregistries/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
//
// For creating registry-api deployment and service
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *MCPRegistryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	// 1. Fetch MCPRegistry instance
	mcpRegistry := &mcpv1alpha1.MCPRegistry{}
	err := r.Get(ctx, req.NamespacedName, mcpRegistry)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Return and don't requeue
			ctxLogger.Info("MCPRegistry resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		ctxLogger.Error(err, "Failed to get MCPRegistry")
		return ctrl.Result{}, err
	}
	ctxLogger.Info("Reconciling MCPRegistry", "MCPRegistry.Name", mcpRegistry.Name)

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

	// 3. Check if sync is needed before performing it
	result, err := r.reconcileSync(ctx, mcpRegistry)

	// Log reconciliation completion
	if err != nil {
		ctxLogger.Error(err, "Reconciliation completed with error",
			"MCPRegistry.Name", mcpRegistry.Name)
	} else {
		ctxLogger.Info("Reconciliation completed successfully",
			"MCPRegistry.Name", mcpRegistry.Name,
			"phase", mcpRegistry.Status.Phase,
			"requeueAfter", result.RequeueAfter)
	}

	return result, err
}

// reconcileSync checks if sync is needed and performs it if necessary
func (r *MCPRegistryReconciler) reconcileSync(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	// Refresh the object to get latest status for accurate timing calculations
	if err := r.Get(ctx, client.ObjectKeyFromObject(mcpRegistry), mcpRegistry); err != nil {
		ctxLogger.Error(err, "Failed to refresh MCPRegistry object for sync check")
		return ctrl.Result{}, err
	}

	// Check if sync is needed
	syncNeeded, syncReason, nextSyncTime, err := r.syncManager.ShouldSync(ctx, mcpRegistry)
	if err != nil {
		ctxLogger.Error(err, "Failed to determine if sync is needed")
		// Proceed with sync on error to be safe
		syncNeeded = true
		syncReason = sync.ReasonErrorCheckingSyncNeed
	}

	if !syncNeeded {
		ctxLogger.Info("Sync not needed", "reason", syncReason)
		// Schedule next reconciliation if we have a sync policy
		if nextSyncTime != nil {
			requeueAfter := time.Until(*nextSyncTime)
			ctxLogger.Info("Scheduling next automatic sync", "requeueAfter", requeueAfter)
			return ctrl.Result{RequeueAfter: requeueAfter}, nil
		}
		return ctrl.Result{}, nil
	}

	ctxLogger.Info("Sync needed", "reason", syncReason)

	// Handle manual sync with no data changes - update trigger tracking only
	if syncReason == sync.ReasonManualNoChanges {
		return r.syncManager.UpdateManualSyncTriggerOnly(ctx, mcpRegistry)
	}

	result, err := r.syncManager.PerformSync(ctx, mcpRegistry)

	if err != nil {
		// Sync failed - schedule retry with exponential backoff
		ctxLogger.Error(err, "Sync failed, scheduling retry")
		// Use a shorter retry interval instead of the full sync interval
		retryAfter := time.Minute * 5 // Default retry interval
		if result.RequeueAfter > 0 {
			// If PerformSync already set a retry interval, use it
			retryAfter = result.RequeueAfter
		}
		return ctrl.Result{RequeueAfter: retryAfter}, err
	}

	// Schedule next automatic sync only if this was an automatic sync (not manual)
	if mcpRegistry.Spec.SyncPolicy != nil && !sync.IsManualSync(syncReason) {
		interval, parseErr := time.ParseDuration(mcpRegistry.Spec.SyncPolicy.Interval)
		if parseErr == nil {
			result.RequeueAfter = interval
			ctxLogger.Info("Automatic sync successful, scheduled next automatic sync", "interval", interval)
		} else {
			ctxLogger.Error(parseErr, "Invalid sync interval in policy", "interval", mcpRegistry.Spec.SyncPolicy.Interval)
		}
	} else if sync.IsManualSync(syncReason) {
		ctxLogger.Info("Manual sync successful, automatic sync schedule unchanged")
	} else {
		ctxLogger.Info("Sync successful, no automatic sync policy configured")
	}

	return result, err
}

// finalizeMCPRegistry performs the finalizer logic for the MCPRegistry
func (r *MCPRegistryReconciler) finalizeMCPRegistry(ctx context.Context, registry *mcpv1alpha1.MCPRegistry) error {
	ctxLogger := log.FromContext(ctx)

	// Update the MCPRegistry status to indicate termination
	registry.Status.Phase = mcpv1alpha1.MCPRegistryPhaseTerminating
	registry.Status.Message = "MCPRegistry is being terminated"
	if err := r.Status().Update(ctx, registry); err != nil {
		ctxLogger.Error(err, "Failed to update MCPRegistry status during finalization")
		return err
	}

	// Clean up internal storage ConfigMaps
	if err := r.syncManager.Delete(ctx, registry); err != nil {
		ctxLogger.Error(err, "Failed to delete storage during finalization")
		// Continue with finalization even if storage cleanup fails
	}

	// TODO: Add additional cleanup logic when other features are implemented:
	// - Clean up Registry API deployment and service (will be handled by owner references)
	// - Cancel any running sync operations

	ctxLogger.Info("MCPRegistry finalization completed", "registry", registry.Name)
	return nil
}

// labelsForRegistryAPI generates standard labels for registry API resources
func labelsForRegistryAPI(mcpRegistry *mcpv1alpha1.MCPRegistry, resourceName string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":             resourceName,
		"app.kubernetes.io/component":        "registry-api",
		"app.kubernetes.io/managed-by":       "toolhive-operator",
		"toolhive.stacklok.io/registry-name": mcpRegistry.Name,
	}
}

// buildRegistryAPIDeployment creates and configures a Deployment object for the registry API.
// This function handles all deployment configuration including labels, container specs, probes,
// and storage manager integration. It returns a fully configured deployment ready for Kubernetes API operations.
// nolint:unused // Will be used when deployment functionality is integrated
func (r *MCPRegistryReconciler) buildRegistryAPIDeployment(mcpRegistry *mcpv1alpha1.MCPRegistry) (*appsv1.Deployment, error) {
	// Generate deployment name using the established pattern
	deploymentName := fmt.Sprintf("%s-api", mcpRegistry.Name)

	// Define labels using common function
	labels := labelsForRegistryAPI(mcpRegistry, deploymentName)

	// Create basic deployment specification with named container
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName,
			Namespace: mcpRegistry.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &[]int32{1}[0], // Single replica for registry API
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/name":      deploymentName,
					"app.kubernetes.io/component": "registry-api",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "toolhive-registry-api",
					Containers: []corev1.Container{
						{
							Name:  registryAPIContainerName,
							Image: getRegistryAPIImage(),
							Args: []string{
								"serve",
							},
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 8080,
									Name:          "http",
									Protocol:      corev1.ProtocolTCP,
								},
							},
							// Add resource limits and requests for production readiness
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("128Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("500m"),
									corev1.ResourceMemory: resource.MustParse("512Mi"),
								},
							},
							// Add liveness and readiness probes
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/health",
										Port: intstr.FromInt32(8080),
									},
								},
								InitialDelaySeconds: 30,
								PeriodSeconds:       10,
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/readiness",
										Port: intstr.FromInt32(8080),
									},
								},
								InitialDelaySeconds: 5,
								PeriodSeconds:       5,
							},
						},
					},
				},
			},
		},
	}

	// Configure storage-specific aspects using StorageManager
	if err := r.storageManager.ConfigureDeployment(deployment, mcpRegistry, registryAPIContainerName); err != nil {
		return nil, fmt.Errorf("failed to configure deployment with storage manager: %w", err)
	}

	// Set owner reference for automatic garbage collection
	if err := controllerutil.SetControllerReference(mcpRegistry, deployment, r.Scheme); err != nil {
		return nil, fmt.Errorf("failed to set controller reference for deployment: %w", err)
	}

	return deployment, nil
}

// ensureDeployment creates or updates the registry-api Deployment for the MCPRegistry.
// This function handles the Kubernetes API operations (Get, Create, Update) and delegates
// deployment configuration to buildRegistryAPIDeployment.
// nolint:unused // Will be used in future integration
func (r *MCPRegistryReconciler) ensureDeployment(
	ctx context.Context,
	mcpRegistry *mcpv1alpha1.MCPRegistry,
) (*appsv1.Deployment, error) {
	ctxLogger := log.FromContext(ctx).WithValues("mcpregistry", mcpRegistry.Name)

	// Build the desired deployment configuration
	deployment, err := r.buildRegistryAPIDeployment(mcpRegistry)
	if err != nil {
		ctxLogger.Error(err, "Failed to build deployment configuration")
		return nil, fmt.Errorf("failed to build deployment configuration: %w", err)
	}

	deploymentName := deployment.Name

	// Check if deployment already exists
	existing := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{
		Name:      deploymentName,
		Namespace: mcpRegistry.Namespace,
	}, existing)

	if err != nil {
		if errors.IsNotFound(err) {
			// Deployment doesn't exist, create it
			ctxLogger.Info("Creating registry-api deployment", "deployment", deploymentName)
			if err := r.Create(ctx, deployment); err != nil {
				ctxLogger.Error(err, "Failed to create deployment")
				return nil, fmt.Errorf("failed to create deployment %s: %w", deploymentName, err)
			}
			ctxLogger.Info("Successfully created registry-api deployment", "deployment", deploymentName)
			return deployment, nil
		}
		// Unexpected error
		ctxLogger.Error(err, "Failed to get deployment")
		return nil, fmt.Errorf("failed to get deployment %s: %w", deploymentName, err)
	}

	// Deployment exists, update it if necessary
	ctxLogger.V(1).Info("Deployment already exists, checking for updates", "deployment", deploymentName)

	// Update the existing deployment with our desired state
	existing.Spec = deployment.Spec
	existing.Labels = deployment.Labels

	// Ensure owner reference is set
	if err := controllerutil.SetControllerReference(mcpRegistry, existing, r.Scheme); err != nil {
		ctxLogger.Error(err, "Failed to set controller reference for existing deployment")
		return nil, fmt.Errorf("failed to set controller reference for existing deployment: %w", err)
	}

	if err := r.Update(ctx, existing); err != nil {
		ctxLogger.Error(err, "Failed to update deployment")
		return nil, fmt.Errorf("failed to update deployment %s: %w", deploymentName, err)
	}

	ctxLogger.Info("Successfully updated registry-api deployment", "deployment", deploymentName)
	return existing, nil
}

// buildRegistryAPIService creates and configures a Service object for the registry API.
// This function handles all service configuration including labels, ports, and selector.
// It returns a fully configured ClusterIP service ready for Kubernetes API operations.
// nolint:unused // Will be used when service functionality is integrated
func buildRegistryAPIService(mcpRegistry *mcpv1alpha1.MCPRegistry) *corev1.Service {
	// Generate service name using the established pattern
	serviceName := fmt.Sprintf("%s-api", mcpRegistry.Name)

	// Define labels using common function
	labels := labelsForRegistryAPI(mcpRegistry, serviceName)

	// Define selector to match deployment pod labels
	selector := map[string]string{
		"app.kubernetes.io/name":      serviceName,
		"app.kubernetes.io/component": "registry-api",
	}

	// Create service specification
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: mcpRegistry.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: selector,
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       8080,
					TargetPort: intstr.FromInt32(8080),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}

	return service
}

// ensureService creates or updates the registry-api Service for the MCPRegistry.
// This function handles the Kubernetes API operations (Get, Create, Update) and delegates
// service configuration to buildRegistryAPIService.
// nolint:unused // Will be used in future integration
func (r *MCPRegistryReconciler) ensureService(
	ctx context.Context,
	mcpRegistry *mcpv1alpha1.MCPRegistry,
) (*corev1.Service, error) {
	ctxLogger := log.FromContext(ctx).WithValues("mcpregistry", mcpRegistry.Name)

	// Build the desired service configuration
	service := buildRegistryAPIService(mcpRegistry)
	serviceName := service.Name

	// Set owner reference for automatic garbage collection
	if err := controllerutil.SetControllerReference(mcpRegistry, service, r.Scheme); err != nil {
		ctxLogger.Error(err, "Failed to set controller reference for service")
		return nil, fmt.Errorf("failed to set controller reference for service: %w", err)
	}

	// Check if service already exists
	existing := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      serviceName,
		Namespace: mcpRegistry.Namespace,
	}, existing)

	if err != nil {
		if errors.IsNotFound(err) {
			// Service doesn't exist, create it
			ctxLogger.Info("Creating registry-api service", "service", serviceName)
			if err := r.Create(ctx, service); err != nil {
				ctxLogger.Error(err, "Failed to create service")
				return nil, fmt.Errorf("failed to create service %s: %w", serviceName, err)
			}
			ctxLogger.Info("Successfully created registry-api service", "service", serviceName)
			return service, nil
		}
		// Unexpected error
		ctxLogger.Error(err, "Failed to get service")
		return nil, fmt.Errorf("failed to get service %s: %w", serviceName, err)
	}

	// Service exists, update it if necessary
	ctxLogger.V(1).Info("Service already exists, checking for updates", "service", serviceName)

	// Update the existing service with our desired state
	existing.Spec.Type = service.Spec.Type
	existing.Spec.Selector = service.Spec.Selector
	existing.Spec.Ports = service.Spec.Ports
	existing.Labels = service.Labels

	// Ensure owner reference is set
	if err := controllerutil.SetControllerReference(mcpRegistry, existing, r.Scheme); err != nil {
		ctxLogger.Error(err, "Failed to set controller reference for existing service")
		return nil, fmt.Errorf("failed to set controller reference for existing service: %w", err)
	}

	if err := r.Update(ctx, existing); err != nil {
		ctxLogger.Error(err, "Failed to update service")
		return nil, fmt.Errorf("failed to update service %s: %w", serviceName, err)
	}

	ctxLogger.Info("Successfully updated registry-api service", "service", serviceName)
	return existing, nil
}

// checkAPIReadiness verifies that the deployed registry-API Deployment is ready
// by checking deployment status for ready replicas. Returns true if the deployment
// has at least one ready replica, false otherwise. Returns an error only for
// unexpected failures, not for deployments that are simply not ready yet.
// nolint:unused,unparam // Will be used when deployment functionality is integrated; error param reserved for future use
func (*MCPRegistryReconciler) checkAPIReadiness(ctx context.Context, deployment *appsv1.Deployment) (bool, error) {
	ctxLogger := log.FromContext(ctx)

	// Handle nil deployment gracefully
	if deployment == nil {
		ctxLogger.V(1).Info("Deployment is nil, not ready")
		return false, nil
	}

	// Log deployment status for debugging
	ctxLogger.V(1).Info("Checking deployment readiness",
		"deployment", deployment.Name,
		"namespace", deployment.Namespace,
		"replicas", deployment.Status.Replicas,
		"readyReplicas", deployment.Status.ReadyReplicas,
		"availableReplicas", deployment.Status.AvailableReplicas,
		"updatedReplicas", deployment.Status.UpdatedReplicas)

	// Check if deployment has ready replicas
	if deployment.Status.ReadyReplicas > 0 {
		ctxLogger.V(1).Info("Deployment is ready",
			"deployment", deployment.Name,
			"readyReplicas", deployment.Status.ReadyReplicas)
		return true, nil
	}

	// Check deployment conditions for additional context
	for _, condition := range deployment.Status.Conditions {
		if condition.Type == appsv1.DeploymentProgressing {
			if condition.Status == corev1.ConditionFalse {
				ctxLogger.Info("Deployment is not progressing",
					"deployment", deployment.Name,
					"reason", condition.Reason,
					"message", condition.Message)
			}
		}
	}

	ctxLogger.V(1).Info("Deployment is not ready yet",
		"deployment", deployment.Name,
		"readyReplicas", deployment.Status.ReadyReplicas)

	return false, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MCPRegistryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.MCPRegistry{}).
		Complete(r)
}

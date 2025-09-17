package controllers

import (
	"context"
	"fmt"
	"os"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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
	registryAPIContainerName = "registry-api"
)

// statusUpdateCollector collects status changes during reconciliation
// and applies them in a single batch update at the end
type statusUpdateCollector struct {
	mcpRegistry *mcpv1alpha1.MCPRegistry
	hasChanges  bool
	phase       *mcpv1alpha1.MCPRegistryPhase
	message     *string
	apiEndpoint *string
	conditions  map[string]metav1.Condition
}

// newStatusUpdateCollector creates a new status update collector
func newStatusUpdateCollector(mcpRegistry *mcpv1alpha1.MCPRegistry) *statusUpdateCollector {
	return &statusUpdateCollector{
		mcpRegistry: mcpRegistry,
		conditions:  make(map[string]metav1.Condition),
	}
}

// setPhase sets the phase to be updated
func (s *statusUpdateCollector) setPhase(phase mcpv1alpha1.MCPRegistryPhase) {
	s.phase = &phase
	s.hasChanges = true
}

// setMessage sets the message to be updated
func (s *statusUpdateCollector) setMessage(message string) {
	s.message = &message
	s.hasChanges = true
}

// setAPIEndpoint sets the API endpoint to be updated
func (s *statusUpdateCollector) setAPIEndpoint(endpoint string) {
	s.apiEndpoint = &endpoint
	s.hasChanges = true
}

// setAPIReadyCondition adds or updates the API ready condition
func (s *statusUpdateCollector) setAPIReadyCondition(reason, message string, status metav1.ConditionStatus) {
	s.conditions[mcpv1alpha1.ConditionAPIReady] = metav1.Condition{
		Type:    mcpv1alpha1.ConditionAPIReady,
		Status:  status,
		Reason:  reason,
		Message: message,
	}
	s.hasChanges = true
}

// apply applies all collected status changes in a single update
func (s *statusUpdateCollector) apply(ctx context.Context, r *MCPRegistryReconciler) error {
	if !s.hasChanges {
		return nil
	}

	ctxLogger := log.FromContext(ctx)

	// Apply phase change
	if s.phase != nil {
		s.mcpRegistry.Status.Phase = *s.phase
	}

	// Apply message change
	if s.message != nil {
		s.mcpRegistry.Status.Message = *s.message
	}

	// Apply API endpoint change
	if s.apiEndpoint != nil {
		s.mcpRegistry.Status.APIEndpoint = *s.apiEndpoint
	}

	// Apply condition changes
	for _, condition := range s.conditions {
		meta.SetStatusCondition(&s.mcpRegistry.Status.Conditions, condition)
	}

	// Single status update
	if err := r.Status().Update(ctx, s.mcpRegistry); err != nil {
		ctxLogger.Error(err, "Failed to apply batched status update")
		return fmt.Errorf("failed to apply batched status update: %w", err)
	}

	ctxLogger.V(1).Info("Applied batched status update",
		"phase", s.phase,
		"message", s.message,
		"conditionsCount", len(s.conditions))

	return nil
}

// getRegistryAPIImage returns the container image for the registry API.
// It checks the TOOLHIVE_REGISTRY_API_IMAGE environment variable first,
// falling back to the default image if not set.
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
	syncManager          sync.Manager
	storageManager       sources.StorageManager
	sourceHandlerFactory sources.SourceHandlerFactory
}

// NewMCPRegistryReconciler creates a new MCPRegistryReconciler with required dependencies
func NewMCPRegistryReconciler(k8sClient client.Client, scheme *runtime.Scheme) *MCPRegistryReconciler {
	sourceHandlerFactory := sources.NewSourceHandlerFactory(k8sClient)
	storageManager := sources.NewConfigMapStorageManager(k8sClient, scheme)
	syncManager := sync.NewDefaultSyncManager(k8sClient, scheme, sourceHandlerFactory, storageManager)

	return &MCPRegistryReconciler{
		Client:               k8sClient,
		Scheme:               scheme,
		syncManager:          syncManager,
		storageManager:       storageManager,
		sourceHandlerFactory: sourceHandlerFactory,
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
//
//nolint:gocyclo // Complex reconciliation logic requires multiple conditions
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

	// 3. Create status collector for batched updates
	statusCollector := newStatusUpdateCollector(mcpRegistry)

	// 4. Reconcile sync operation
	result, err := r.reconcileSync(ctx, mcpRegistry, statusCollector)

	// 5. Reconcile API service (deployment and service, independent of sync status)
	if apiErr := r.reconcileAPIService(ctx, mcpRegistry, statusCollector); apiErr != nil {
		ctxLogger.Error(apiErr, "Failed to reconcile API service")
		if err == nil {
			err = apiErr
		}
	}

	// 6. Check if we need to requeue for API readiness
	if err == nil && !r.isAPIReady(ctx, mcpRegistry) {
		ctxLogger.Info("API not ready yet, scheduling requeue to check readiness")
		if result.RequeueAfter == 0 || result.RequeueAfter > time.Second*30 {
			result.RequeueAfter = time.Second * 30
		}
	}

	// 7. Apply all status changes in a single batch update
	if statusUpdateErr := statusCollector.apply(ctx, r); statusUpdateErr != nil {
		ctxLogger.Error(statusUpdateErr, "Failed to apply batched status update")
		// Return the status update error only if there was no main reconciliation error
		if err == nil {
			err = statusUpdateErr
		}
	}

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
// This method only handles data synchronization to the target ConfigMap
func (r *MCPRegistryReconciler) reconcileSync(
	ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry, statusCollector *statusUpdateCollector,
) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	// Check if sync is needed - no need to refresh object here since we just fetched it
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

	// Set phase to syncing before starting the sync process
	statusCollector.setPhase(mcpv1alpha1.MCPRegistryPhaseSyncing)
	statusCollector.setMessage("Syncing registry data")

	// Handle manual sync with no data changes - update trigger tracking only
	if syncReason == sync.ReasonManualNoChanges {
		return r.syncManager.UpdateManualSyncTriggerOnly(ctx, mcpRegistry)
	}

	result, err := r.syncManager.PerformSync(ctx, mcpRegistry)

	if err != nil {
		// Sync failed - schedule retry with exponential backoff
		ctxLogger.Error(err, "Sync failed, scheduling retry")
		statusCollector.setPhase(mcpv1alpha1.MCPRegistryPhaseFailed)
		statusCollector.setMessage(fmt.Sprintf("Sync failed: %v", err))
		// Use a shorter retry interval instead of the full sync interval
		retryAfter := time.Minute * 5 // Default retry interval
		if result.RequeueAfter > 0 {
			// If PerformSync already set a retry interval, use it
			retryAfter = result.RequeueAfter
		}
		return ctrl.Result{RequeueAfter: retryAfter}, err
	}

	// Sync successful - keep in syncing phase until API is also ready
	statusCollector.setMessage("Registry data synced successfully")

	ctxLogger.Info("Registry data sync completed successfully")

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

// isAPIReady checks if the registry API deployment is ready and serving requests
func (r *MCPRegistryReconciler) isAPIReady(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) bool {
	ctxLogger := log.FromContext(ctx).WithValues("mcpregistry", mcpRegistry.Name)

	deploymentName := fmt.Sprintf("%s-api", mcpRegistry.Name)
	deployment := &appsv1.Deployment{}

	err := r.Get(ctx, client.ObjectKey{
		Name:      deploymentName,
		Namespace: mcpRegistry.Namespace,
	}, deployment)

	if err != nil {
		ctxLogger.Info("API deployment not found, considering not ready", "error", err)
		return false
	}

	// Delegate to the existing checkAPIReadiness method for consistency
	return r.checkAPIReadiness(ctx, deployment)
}

// finalizeMCPRegistry performs the finalizer logic for the MCPRegistry
func (r *MCPRegistryReconciler) finalizeMCPRegistry(ctx context.Context, registry *mcpv1alpha1.MCPRegistry) error {
	ctxLogger := log.FromContext(ctx)

	// Update the MCPRegistry status to indicate termination - immediate update needed since object is being deleted
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
func (r *MCPRegistryReconciler) buildRegistryAPIDeployment(
	mcpRegistry *mcpv1alpha1.MCPRegistry, sourceHandler sources.SourceHandler,
) (*appsv1.Deployment, error) {
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
					Annotations: map[string]string{
						"toolhive.stacklok.dev/config-hash": r.getSourceDataHash(mcpRegistry, sourceHandler),
					},
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

// getSourceDataHash calculates the hash of the source ConfigMap data using the provided source handler
// This hash is used as a deployment annotation to trigger pod restarts when data changes
func (*MCPRegistryReconciler) getSourceDataHash(
	mcpRegistry *mcpv1alpha1.MCPRegistry, sourceHandler sources.SourceHandler,
) string {
	// Get current hash from source using the existing handler
	hash, err := sourceHandler.CurrentHash(context.Background(), mcpRegistry)
	if err != nil {
		// If we can't get the hash, return a time-based hash
		// This ensures deployments get updated when there are source issues
		return fmt.Sprintf("error-%d", time.Now().Unix())
	}

	return hash
}

// ensureDeployment creates or updates the registry-api Deployment for the MCPRegistry.
// This function handles the Kubernetes API operations (Get, Create, Update) and delegates
// deployment configuration to buildRegistryAPIDeployment.
func (r *MCPRegistryReconciler) ensureDeployment(
	ctx context.Context,
	mcpRegistry *mcpv1alpha1.MCPRegistry,
) (*appsv1.Deployment, error) {
	ctxLogger := log.FromContext(ctx).WithValues("mcpregistry", mcpRegistry.Name)

	// Build the desired deployment configuration
	// Get source handler for config hash calculation
	sourceHandler, err := r.sourceHandlerFactory.CreateHandler(mcpRegistry.Spec.Source.Type)
	if err != nil {
		return nil, fmt.Errorf("failed to create source handler for deployment: %w", err)
	}

	// Use CreateOrUpdate pattern for robust deployment management
	deploymentName := fmt.Sprintf("%s-api", mcpRegistry.Name)
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName,
			Namespace: mcpRegistry.Namespace,
		},
	}

	// Check if deployment already exists
	err = r.Get(ctx, client.ObjectKey{Name: deploymentName, Namespace: mcpRegistry.Namespace}, deployment)
	if errors.IsNotFound(err) {
		// Deployment doesn't exist, create it
		desired, buildErr := r.buildRegistryAPIDeployment(mcpRegistry, sourceHandler)
		if buildErr != nil {
			return nil, fmt.Errorf("failed to build deployment configuration: %w", buildErr)
		}

		// Set owner reference
		if ownerErr := controllerutil.SetControllerReference(mcpRegistry, desired, r.Scheme); ownerErr != nil {
			return nil, fmt.Errorf("failed to set controller reference: %w", ownerErr)
		}

		if createErr := r.Create(ctx, desired); createErr != nil {
			return nil, fmt.Errorf("failed to create deployment %s: %w", deploymentName, createErr)
		}

		ctxLogger.Info("Deployment created successfully", "deployment", deploymentName)
		return desired, nil
	} else if err != nil {
		// Some other error occurred
		return nil, fmt.Errorf("failed to get deployment %s: %w", deploymentName, err)
	}

	// TODO: Implement deployment updates when needed (e.g., when config hash changes)
	// For now, just return the existing deployment to avoid endless reconciliation loops
	ctxLogger.Info("Deployment already exists, skipping update", "deployment", deploymentName)
	return deployment, nil
}

// buildRegistryAPIService creates and configures a Service object for the registry API.
// This function handles all service configuration including labels, ports, and selector.
// It returns a fully configured ClusterIP service ready for Kubernetes API operations.
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
// has at least one ready replica, false otherwise.
func (*MCPRegistryReconciler) checkAPIReadiness(ctx context.Context, deployment *appsv1.Deployment) bool {
	ctxLogger := log.FromContext(ctx)

	// Handle nil deployment gracefully
	if deployment == nil {
		ctxLogger.V(1).Info("Deployment is nil, not ready")
		return false
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
		return true
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

	return false
}

// reconcileAPIService orchestrates the deployment, service creation, and readiness checking for the registry API.
// This method coordinates all aspects of API service including creating/updating the deployment and service,
// checking readiness, and updating the MCPRegistry status with deployment references and endpoint information.
func (r *MCPRegistryReconciler) reconcileAPIService(
	ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry, statusCollector *statusUpdateCollector,
) error {
	ctxLogger := log.FromContext(ctx).WithValues("mcpregistry", mcpRegistry.Name)
	ctxLogger.Info("Reconciling API service")

	// Step 1: Ensure deployment exists and is configured correctly
	deployment, err := r.ensureDeployment(ctx, mcpRegistry)
	if err != nil {
		ctxLogger.Error(err, "Failed to ensure deployment")
		// Update status with failure condition
		statusCollector.setAPIReadyCondition("DeploymentFailed",
			fmt.Sprintf("Failed to ensure deployment: %v", err), metav1.ConditionFalse)
		return fmt.Errorf("failed to ensure deployment: %w", err)
	}

	// Step 2: Ensure service exists and is configured correctly
	service, err := r.ensureService(ctx, mcpRegistry)
	if err != nil {
		ctxLogger.Error(err, "Failed to ensure service")
		// Update status with failure condition
		statusCollector.setAPIReadyCondition("ServiceFailed",
			fmt.Sprintf("Failed to ensure service: %v", err), metav1.ConditionFalse)
		return fmt.Errorf("failed to ensure service: %w", err)
	}

	// Step 3: Check API readiness
	isReady := r.checkAPIReadiness(ctx, deployment)

	// Step 4: Update MCPRegistry status with deployment and service references
	r.updateAPIStatus(ctx, mcpRegistry, deployment, service, isReady, statusCollector)

	// Step 5: Update overall phase based on API readiness
	if isReady {
		ctxLogger.Info("API service reconciliation completed successfully - API is ready")
		statusCollector.setPhase(mcpv1alpha1.MCPRegistryPhaseReady)
		statusCollector.setMessage("Registry is ready and API is serving requests")
	} else {
		ctxLogger.Info("API service reconciliation completed - API is not ready yet")
		// Don't change phase - let it stay in current state (likely Syncing)
		// Only update message if not in Failed state
		if mcpRegistry.Status.Phase != mcpv1alpha1.MCPRegistryPhaseFailed {
			statusCollector.setMessage("Registry data synced, API deployment in progress")
		}
	}

	return nil
}

// updateAPIStatus updates the MCPRegistry status with deployment and service references and API endpoint information
func (*MCPRegistryReconciler) updateAPIStatus(ctx context.Context, _ *mcpv1alpha1.MCPRegistry,
	_ *appsv1.Deployment, service *corev1.Service, isReady bool, statusCollector *statusUpdateCollector) {
	ctxLogger := log.FromContext(ctx)

	// Update API endpoint information
	if service != nil {
		// Construct internal URL from service information
		endpoint := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d",
			service.Name, service.Namespace, service.Spec.Ports[0].Port)
		statusCollector.setAPIEndpoint(endpoint)
	}

	// Set API readiness condition
	var conditionStatus metav1.ConditionStatus
	var reason, message string

	if isReady {
		conditionStatus = metav1.ConditionTrue
		reason = "APIReady"
		message = "Registry API is ready and serving requests"
	} else {
		conditionStatus = metav1.ConditionFalse
		reason = "APINotReady"
		message = "Registry API deployment is not ready yet"
	}

	statusCollector.setAPIReadyCondition(reason, message, conditionStatus)

	ctxLogger.V(1).Info("Prepared API status update for batching",
		"apiReady", isReady,
		"endpoint", statusCollector.apiEndpoint)
}

// SetupWithManager sets up the controller with the Manager.
func (r *MCPRegistryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.MCPRegistry{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Complete(r)
}

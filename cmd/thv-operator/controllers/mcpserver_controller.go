// Package controllers contains the reconciliation logic for the MCPServer custom resource.
// It handles the creation, update, and deletion of MCP servers in Kubernetes.
package controllers

import (
	"context"
	"encoding/json"
	goerr "errors"
	"fmt"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/validation"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

// MCPServerReconciler reconciles a MCPServer object
type MCPServerReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	platformDetector kubernetes.PlatformDetector
	detectedPlatform kubernetes.Platform
	platformOnce     sync.Once
	ImageValidation  validation.ImageValidation
}

// defaultRBACRules are the default RBAC rules that the
// ToolHive ProxyRunner and/or MCP server needs to have in order to run.
var defaultRBACRules = []rbacv1.PolicyRule{
	{
		APIGroups: []string{"apps"},
		Resources: []string{"statefulsets"},
		Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete", "apply"},
	},
	{
		APIGroups: []string{""},
		Resources: []string{"services"},
		Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete", "apply"},
	},
	{
		APIGroups: []string{""},
		Resources: []string{"pods"},
		Verbs:     []string{"get", "list", "watch"},
	},
	{
		APIGroups: []string{""},
		Resources: []string{"pods/log"},
		Verbs:     []string{"get"},
	},
	{
		APIGroups: []string{""},
		Resources: []string{"pods/attach"},
		Verbs:     []string{"create", "get"},
	},
	{
		APIGroups: []string{""},
		Resources: []string{"configmaps"},
		Verbs:     []string{"get", "list", "watch"},
	},
}

// mcpContainerName is the name of the mcp container used in pod templates
const mcpContainerName = "mcp"

// Restart annotation keys for triggering pod restart
const (
	RestartedAtAnnotationKey          = "mcpserver.toolhive.stacklok.dev/restarted-at"
	RestartStrategyAnnotationKey      = "mcpserver.toolhive.stacklok.dev/restart-strategy"
	LastProcessedRestartAnnotationKey = "mcpserver.toolhive.stacklok.dev/last-processed-restart"
)

// Restart strategy constants
const (
	RestartStrategyRolling   = "rolling"
	RestartStrategyImmediate = "immediate"
)

// Authorization ConfigMap label constants
const (
	// authzLabelKey is the label key for authorization configuration type
	authzLabelKey = "toolhive.stacklok.io/authz"

	// authzLabelValueInline is the label value for inline authorization configuration
	authzLabelValueInline = "inline"
)

// detectPlatform detects the Kubernetes platform type (Kubernetes vs OpenShift)
// It uses sync.Once to ensure the detection is only performed once and cached
func (r *MCPServerReconciler) detectPlatform(ctx context.Context) (kubernetes.Platform, error) {
	var err error
	r.platformOnce.Do(func() {
		// Initialize platform detector if not already done
		if r.platformDetector == nil {
			r.platformDetector = kubernetes.NewDefaultPlatformDetector()
		}

		cfg, configErr := rest.InClusterConfig()
		if configErr != nil {
			err = fmt.Errorf("failed to get in-cluster config for platform detection: %w", configErr)
			return
		}

		r.detectedPlatform, err = r.platformDetector.DetectPlatform(cfg)
		if err != nil {
			err = fmt.Errorf("failed to detect platform: %w", err)
			return
		}

		ctxLogger := log.FromContext(ctx)
		ctxLogger.Info("Platform detected for MCPServer controller", "platform", r.detectedPlatform.String())
	})

	return r.detectedPlatform, err
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpservers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpservers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpservers/finalizers,verbs=update
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcptoolconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups="",resources=services,verbs=create;delete;get;list;patch;update;watch;apply
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=roles,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=rolebindings,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete;apply
// +kubebuilder:rbac:groups="",resources=pods/attach,verbs=create;get
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
//nolint:gocyclo
func (r *MCPServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	// Fetch the MCPServer instance
	mcpServer := &mcpv1alpha1.MCPServer{}
	err := r.Get(ctx, req.NamespacedName, mcpServer)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Return and don't requeue
			ctxLogger.Info("MCPServer resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		ctxLogger.Error(err, "Failed to get MCPServer")
		return ctrl.Result{}, err
	}

	// Check if the restart annotation has been updated and trigger a rolling restart if needed
	if shouldTriggerRestart, err := r.handleRestartAnnotation(ctx, mcpServer); err != nil {
		ctxLogger.Error(err, "Failed to handle restart annotation")
		return ctrl.Result{}, err
	} else if shouldTriggerRestart {
		// Return and requeue to avoid double-processing after triggering restart
		return ctrl.Result{Requeue: true}, nil
	}

	// Check if MCPToolConfig is referenced and handle it
	if err := r.handleToolConfig(ctx, mcpServer); err != nil {
		ctxLogger.Error(err, "Failed to handle MCPToolConfig")
		// Update status to reflect the error
		mcpServer.Status.Phase = mcpv1alpha1.MCPServerPhaseFailed
		if statusErr := r.Status().Update(ctx, mcpServer); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPServer status after MCPToolConfig error")
		}
		return ctrl.Result{}, err
	}

	// Validate MCPServer image against enforcing registries
	imageValidator := validation.NewImageValidator(r.Client, mcpServer.Namespace, r.ImageValidation)
	err = imageValidator.ValidateImage(ctx, mcpServer.Spec.Image, mcpServer.ObjectMeta)
	if goerr.Is(err, validation.ErrImageNotChecked) {
		ctxLogger.Info("Image validation skipped - no enforcement configured")
		// Set condition to indicate validation was skipped
		setImageValidationCondition(mcpServer, metav1.ConditionTrue,
			mcpv1alpha1.ConditionReasonImageValidationSkipped,
			"Image validation was not performed (no enforcement configured)")
	} else if goerr.Is(err, validation.ErrImageInvalid) {
		ctxLogger.Error(err, "MCPServer image validation failed", "image", mcpServer.Spec.Image)
		// Update status to reflect validation failure
		mcpServer.Status.Phase = mcpv1alpha1.MCPServerPhaseFailed
		mcpServer.Status.Message = err.Error() // Gets the specific validation failure reason
		setImageValidationCondition(mcpServer, metav1.ConditionFalse,
			mcpv1alpha1.ConditionReasonImageValidationFailed,
			err.Error()) // This will include the wrapped error context with specific reason
		if statusErr := r.Status().Update(ctx, mcpServer); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPServer status after validation error")
		}
		// Requeue after 5 minutes to retry validation
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	} else if err != nil {
		// Other system/infrastructure errors
		ctxLogger.Error(err, "MCPServer image validation system error", "image", mcpServer.Spec.Image)
		setImageValidationCondition(mcpServer, metav1.ConditionFalse,
			mcpv1alpha1.ConditionReasonImageValidationError,
			fmt.Sprintf("Error checking image validity: %v", err))
		if statusErr := r.Status().Update(ctx, mcpServer); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPServer status after validation error")
		}
		// Requeue after 5 minutes to retry validation
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	} else {
		// Validation passed
		ctxLogger.Info("Image validation passed", "image", mcpServer.Spec.Image)
		setImageValidationCondition(mcpServer, metav1.ConditionTrue,
			mcpv1alpha1.ConditionReasonImageValidationSuccess,
			"Image validation passed - image found in enforced registries")
	}

	// Check if the MCPServer instance is marked to be deleted
	if mcpServer.GetDeletionTimestamp() != nil {
		// The object is being deleted
		if controllerutil.ContainsFinalizer(mcpServer, "mcpserver.toolhive.stacklok.dev/finalizer") {
			// Run finalization logic. If the finalization logic fails,
			// don't remove the finalizer so that we can retry during the next reconciliation.
			if err := r.finalizeMCPServer(ctx, mcpServer); err != nil {
				return ctrl.Result{}, err
			}

			// Remove the finalizer. Once all finalizers have been removed, the object will be deleted.
			controllerutil.RemoveFinalizer(mcpServer, "mcpserver.toolhive.stacklok.dev/finalizer")
			err := r.Update(ctx, mcpServer)
			if err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer for this CR
	if !controllerutil.ContainsFinalizer(mcpServer, "mcpserver.toolhive.stacklok.dev/finalizer") {
		controllerutil.AddFinalizer(mcpServer, "mcpserver.toolhive.stacklok.dev/finalizer")
		err = r.Update(ctx, mcpServer)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	// Update the MCPServer status with the pod status
	if err := r.updateMCPServerStatus(ctx, mcpServer); err != nil {
		ctxLogger.Error(err, "Failed to update MCPServer status")
		return ctrl.Result{}, err
	}

	// check if the RBAC resources are in place for the MCP server
	if err := r.ensureRBACResources(ctx, mcpServer); err != nil {
		ctxLogger.Error(err, "Failed to ensure RBAC resources")
		return ctrl.Result{}, err
	}

	// Ensure authorization ConfigMap for inline configuration
	if err := r.ensureAuthzConfigMap(ctx, mcpServer); err != nil {
		ctxLogger.Error(err, "Failed to ensure authorization ConfigMap")
		return ctrl.Result{}, err
	}

	// Ensure RunConfig ConfigMap exists and is up to date
	if err := r.ensureRunConfigConfigMap(ctx, mcpServer); err != nil {
		ctxLogger.Error(err, "Failed to ensure RunConfig ConfigMap")
		return ctrl.Result{}, err
	}

	// Check if the deployment already exists, if not create a new one
	deployment := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: mcpServer.Name, Namespace: mcpServer.Namespace}, deployment)
	if err != nil && errors.IsNotFound(err) {
		// Define a new deployment
		dep := r.deploymentForMCPServer(ctx, mcpServer)
		if dep == nil {
			ctxLogger.Error(nil, "Failed to create Deployment object")
			return ctrl.Result{}, fmt.Errorf("failed to create Deployment object")
		}
		ctxLogger.Info("Creating a new Deployment", "Deployment.Namespace", dep.Namespace, "Deployment.Name", dep.Name)
		err = r.Create(ctx, dep)
		if err != nil {
			ctxLogger.Error(err, "Failed to create new Deployment", "Deployment.Namespace", dep.Namespace, "Deployment.Name", dep.Name)
			return ctrl.Result{}, err
		}
		// Deployment created successfully - return and requeue
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		ctxLogger.Error(err, "Failed to get Deployment")
		return ctrl.Result{}, err
	}

	// Ensure the deployment size is the same as the spec
	if *deployment.Spec.Replicas != 1 {
		deployment.Spec.Replicas = int32Ptr(1)
		err = r.Update(ctx, deployment)
		if err != nil {
			ctxLogger.Error(err, "Failed to update Deployment",
				"Deployment.Namespace", deployment.Namespace,
				"Deployment.Name", deployment.Name)
			return ctrl.Result{}, err
		}
		// Spec updated - return and requeue
		return ctrl.Result{Requeue: true}, nil
	}

	// Check if the Service already exists, if not create a new one
	serviceName := createServiceName(mcpServer.Name)
	service := &corev1.Service{}
	err = r.Get(ctx, types.NamespacedName{Name: serviceName, Namespace: mcpServer.Namespace}, service)
	if err != nil && errors.IsNotFound(err) {
		// Define a new service
		svc := r.serviceForMCPServer(ctx, mcpServer)
		if svc == nil {
			ctxLogger.Error(nil, "Failed to create Service object")
			return ctrl.Result{}, fmt.Errorf("failed to create Service object")
		}
		ctxLogger.Info("Creating a new Service", "Service.Namespace", svc.Namespace, "Service.Name", svc.Name)
		err = r.Create(ctx, svc)
		if err != nil {
			ctxLogger.Error(err, "Failed to create new Service", "Service.Namespace", svc.Namespace, "Service.Name", svc.Name)
			return ctrl.Result{}, err
		}
		// Service created successfully - return and requeue
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		ctxLogger.Error(err, "Failed to get Service")
		return ctrl.Result{}, err
	}

	// Update the MCPServer status with the service URL
	if mcpServer.Status.URL == "" {
		mcpServer.Status.URL = createServiceURL(mcpServer.Name, mcpServer.Namespace, mcpServer.Spec.Port)
		err = r.Status().Update(ctx, mcpServer)
		if err != nil {
			ctxLogger.Error(err, "Failed to update MCPServer status")
			return ctrl.Result{}, err
		}
	}

	// Check if the deployment spec changed
	if r.deploymentNeedsUpdate(ctx, deployment, mcpServer) {
		// Update the deployment
		newDeployment := r.deploymentForMCPServer(ctx, mcpServer)
		deployment.Spec = newDeployment.Spec
		err = r.Update(ctx, deployment)
		if err != nil {
			ctxLogger.Error(err, "Failed to update Deployment",
				"Deployment.Namespace", deployment.Namespace,
				"Deployment.Name", deployment.Name)
			return ctrl.Result{}, err
		}
		// Spec updated - return and requeue
		return ctrl.Result{Requeue: true}, nil
	}

	// Check if the service spec changed
	if serviceNeedsUpdate(service, mcpServer) {
		// Update the service
		newService := r.serviceForMCPServer(ctx, mcpServer)
		service.Spec.Ports = newService.Spec.Ports
		err = r.Update(ctx, service)
		if err != nil {
			ctxLogger.Error(err, "Failed to update Service", "Service.Namespace", service.Namespace, "Service.Name", service.Name)
			return ctrl.Result{}, err
		}
		// Spec updated - return and requeue
		return ctrl.Result{Requeue: true}, nil
	}

	return ctrl.Result{}, nil
}

// setImageValidationCondition is a helper function to set the image validation status condition
// This reduces code duplication in the image validation logic
func setImageValidationCondition(mcpServer *mcpv1alpha1.MCPServer, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&mcpServer.Status.Conditions, metav1.Condition{
		Type:    mcpv1alpha1.ConditionImageValidated,
		Status:  status,
		Reason:  reason,
		Message: message,
	})
}

// handleRestartAnnotation checks if the restart annotation has been updated and triggers a restart if needed
// Returns true if a restart was triggered and the reconciliation should be requeued
func (r *MCPServerReconciler) handleRestartAnnotation(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) (bool, error) {
	ctxLogger := log.FromContext(ctx)

	// Get the current restarted-at annotation value from the CR
	currentRestartedAt := ""
	if mcpServer.Annotations != nil {
		currentRestartedAt = mcpServer.Annotations[RestartedAtAnnotationKey]
	}

	// Skip if no restart annotation is present
	if currentRestartedAt == "" {
		return false, nil
	}

	// Parse the timestamp from the annotation
	requestTime, err := time.Parse(time.RFC3339, currentRestartedAt)
	if err != nil {
		ctxLogger.Error(err, "Invalid timestamp format in restart annotation",
			"annotation", RestartedAtAnnotationKey,
			"value", currentRestartedAt)
		return false, nil
	}

	// Check if we've already processed this restart request
	lastProcessedRestart := ""
	if mcpServer.Annotations != nil {
		lastProcessedRestart = mcpServer.Annotations[LastProcessedRestartAnnotationKey]
	}

	if lastProcessedRestart != "" {
		lastProcessedTime, err := time.Parse(time.RFC3339, lastProcessedRestart)
		if err == nil && !requestTime.After(lastProcessedTime) {
			// This request has already been processed
			return false, nil
		}
	}

	// Get restart strategy (default to rolling)
	strategy := RestartStrategyRolling
	if mcpServer.Annotations != nil {
		if strategyValue, exists := mcpServer.Annotations[RestartStrategyAnnotationKey]; exists {
			strategy = strategyValue
		}
	}

	ctxLogger.Info("Processing restart request",
		"annotation", RestartedAtAnnotationKey,
		"timestamp", currentRestartedAt,
		"strategy", strategy)

	// Perform the restart based on strategy
	err = r.performRestart(ctx, mcpServer, strategy)
	if err != nil {
		return false, fmt.Errorf("failed to perform restart: %w", err)
	}

	// Update the last processed restart timestamp in annotations
	if mcpServer.Annotations == nil {
		mcpServer.Annotations = make(map[string]string)
	}
	mcpServer.Annotations[LastProcessedRestartAnnotationKey] = currentRestartedAt
	err = r.Update(ctx, mcpServer)
	if err != nil {
		return false, fmt.Errorf("failed to update MCPServer with last processed restart annotation: %w", err)
	}

	return true, nil
}

// performRestart executes the restart based on the specified strategy
func (r *MCPServerReconciler) performRestart(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer, strategy string) error {
	switch strategy {
	case RestartStrategyRolling:
		return r.performRollingRestart(ctx, mcpServer)
	case RestartStrategyImmediate:
		return r.performImmediateRestart(ctx, mcpServer)
	default:
		ctxLogger := log.FromContext(ctx)
		ctxLogger.Info("Unknown restart strategy, defaulting to rolling", "strategy", strategy)
		return r.performRollingRestart(ctx, mcpServer)
	}
}

// performRollingRestart triggers a rolling restart by updating the deployment's pod template annotation
func (r *MCPServerReconciler) performRollingRestart(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) error {
	ctxLogger := log.FromContext(ctx)
	deployment := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Name: mcpServer.Name, Namespace: mcpServer.Namespace}, deployment)
	if err != nil {
		if errors.IsNotFound(err) {
			ctxLogger.Info("Deployment not found, skipping rolling restart")
			return nil
		}
		return fmt.Errorf("failed to get deployment for rolling restart: %w", err)
	}

	// Update the deployment's pod template annotation to trigger a rolling restart
	if deployment.Spec.Template.Annotations == nil {
		deployment.Spec.Template.Annotations = map[string]string{}
	}
	deployment.Spec.Template.Annotations[RestartedAtAnnotationKey] = time.Now().Format(time.RFC3339)

	err = r.Update(ctx, deployment)
	if err != nil {
		return fmt.Errorf("failed to update deployment for rolling restart: %w", err)
	}

	ctxLogger.Info("Successfully triggered rolling restart of deployment", "deployment", deployment.Name)
	return nil
}

// performImmediateRestart triggers an immediate restart by deleting the pods directly
func (r *MCPServerReconciler) performImmediateRestart(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) error {
	ctxLogger := log.FromContext(ctx)

	// List pods belonging to this MCPServer
	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(mcpServer.Namespace),
		client.MatchingLabels(labelsForMCPServer(mcpServer.Name)),
	}

	err := r.List(ctx, podList, listOpts...)
	if err != nil {
		return fmt.Errorf("failed to list pods for immediate restart: %w", err)
	}

	// Delete each pod to trigger immediate restart
	for _, pod := range podList.Items {
		ctxLogger.Info("Deleting pod for immediate restart", "pod", pod.Name)
		err = r.Delete(ctx, &pod)
		if err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("failed to delete pod %s for immediate restart: %w", pod.Name, err)
		}
	}

	ctxLogger.Info("Successfully triggered immediate restart", "podsDeleted", len(podList.Items))
	return nil
}

// ensureRBACResource is a generic helper function to ensure a Kubernetes resource exists and is up to date
func (r *MCPServerReconciler) ensureRBACResource(
	ctx context.Context,
	mcpServer *mcpv1alpha1.MCPServer,
	resourceType string,
	createResource func() client.Object,
) error {
	current := createResource()
	objectKey := types.NamespacedName{Name: current.GetName(), Namespace: current.GetNamespace()}
	err := r.Get(ctx, objectKey, current)

	if errors.IsNotFound(err) {
		return r.createRBACResource(ctx, mcpServer, resourceType, createResource)
	} else if err != nil {
		return fmt.Errorf("failed to get %s: %w", resourceType, err)
	}

	return r.updateRBACResourceIfNeeded(ctx, mcpServer, resourceType, createResource, current)
}

// createRBACResource creates a new RBAC resource
func (r *MCPServerReconciler) createRBACResource(
	ctx context.Context,
	mcpServer *mcpv1alpha1.MCPServer,
	resourceType string,
	createResource func() client.Object,
) error {
	ctxLogger := log.FromContext(ctx)
	desired := createResource()
	if err := controllerutil.SetControllerReference(mcpServer, desired, r.Scheme); err != nil {
		ctxLogger.Error(err, "Failed to set controller reference", "resourceType", resourceType)
		return nil
	}

	ctxLogger.Info(
		fmt.Sprintf("%s does not exist, creating %s", resourceType, resourceType),
		fmt.Sprintf("%s.Name", resourceType),
		desired.GetName(),
	)
	if err := r.Create(ctx, desired); err != nil {
		return fmt.Errorf("failed to create %s: %w", resourceType, err)
	}
	ctxLogger.Info(fmt.Sprintf("%s created", resourceType), fmt.Sprintf("%s.Name", resourceType), desired.GetName())
	return nil
}

// updateRBACResourceIfNeeded updates an RBAC resource if changes are detected
func (r *MCPServerReconciler) updateRBACResourceIfNeeded(
	ctx context.Context,
	mcpServer *mcpv1alpha1.MCPServer,
	resourceType string,
	createResource func() client.Object,
	current client.Object,
) error {
	ctxLogger := log.FromContext(ctx)
	desired := createResource()
	if err := controllerutil.SetControllerReference(mcpServer, desired, r.Scheme); err != nil {
		ctxLogger.Error(err, "Failed to set controller reference", "resourceType", resourceType)
		return nil
	}

	if !reflect.DeepEqual(current, desired) {
		ctxLogger.Info(
			fmt.Sprintf("%s exists, updating %s", resourceType, resourceType),
			fmt.Sprintf("%s.Name", resourceType),
			desired.GetName(),
		)
		if err := r.Update(ctx, desired); err != nil {
			return fmt.Errorf("failed to update %s: %w", resourceType, err)
		}
		ctxLogger.Info(fmt.Sprintf("%s updated", resourceType), fmt.Sprintf("%s.Name", resourceType), desired.GetName())
	}
	return nil
}

// ensureRBACResources ensures that the RBAC resources are in place for the MCP server

// handleToolConfig handles MCPToolConfig reference for an MCPServer
func (r *MCPServerReconciler) handleToolConfig(ctx context.Context, m *mcpv1alpha1.MCPServer) error {
	ctxLogger := log.FromContext(ctx)
	if m.Spec.ToolConfigRef == nil {
		// No MCPToolConfig referenced, clear any stored hash
		if m.Status.ToolConfigHash != "" {
			m.Status.ToolConfigHash = ""
			if err := r.Status().Update(ctx, m); err != nil {
				return fmt.Errorf("failed to clear MCPToolConfig hash from status: %w", err)
			}
		}
		return nil
	}

	// Get the referenced MCPToolConfig
	toolConfig, err := GetToolConfigForMCPServer(ctx, r.Client, m)
	if err != nil {
		return err
	}

	if toolConfig == nil {
		return fmt.Errorf("MCPToolConfig %s not found", m.Spec.ToolConfigRef.Name)
	}

	// Check if the MCPToolConfig hash has changed
	if m.Status.ToolConfigHash != toolConfig.Status.ConfigHash {
		ctxLogger.Info("MCPToolConfig has changed, updating MCPServer",
			"mcpserver", m.Name,
			"toolconfig", toolConfig.Name,
			"oldHash", m.Status.ToolConfigHash,
			"newHash", toolConfig.Status.ConfigHash)

		// Update the stored hash
		m.Status.ToolConfigHash = toolConfig.Status.ConfigHash
		if err := r.Status().Update(ctx, m); err != nil {
			return fmt.Errorf("failed to update MCPToolConfig hash in status: %w", err)
		}

		// The change in hash will trigger a reconciliation of the RunConfig
		// which will pick up the new tool configuration
	}

	return nil
}
func (r *MCPServerReconciler) ensureRBACResources(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) error {
	proxyRunnerNameForRBAC := proxyRunnerServiceAccountName(mcpServer.Name)

	// Ensure Role
	if err := r.ensureRBACResource(ctx, mcpServer, "Role", func() client.Object {
		return &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      proxyRunnerNameForRBAC,
				Namespace: mcpServer.Namespace,
			},
			Rules: defaultRBACRules,
		}
	}); err != nil {
		return err
	}

	// Ensure ServiceAccount
	if err := r.ensureRBACResource(ctx, mcpServer, "ServiceAccount", func() client.Object {
		return &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      proxyRunnerNameForRBAC,
				Namespace: mcpServer.Namespace,
			},
		}
	}); err != nil {
		return err
	}

	if err := r.ensureRBACResource(ctx, mcpServer, "RoleBinding", func() client.Object {
		return &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      proxyRunnerNameForRBAC,
				Namespace: mcpServer.Namespace,
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "Role",
				Name:     proxyRunnerNameForRBAC,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      proxyRunnerNameForRBAC,
					Namespace: mcpServer.Namespace,
				},
			},
		}
	}); err != nil {
		return err
	}

	// If a service account is specified, we don't need to create one
	if mcpServer.Spec.ServiceAccount != nil {
		return nil
	}

	// otherwise, create a service account for the MCP server
	mcpServerServiceAccountName := mcpServerServiceAccountName(mcpServer.Name)
	return r.ensureRBACResource(ctx, mcpServer, "ServiceAccount", func() client.Object {
		return &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpServerServiceAccountName,
				Namespace: mcpServer.Namespace,
			},
		}
	})
}

// deploymentForMCPServer returns a MCPServer Deployment object
//
//nolint:gocyclo
func (r *MCPServerReconciler) deploymentForMCPServer(ctx context.Context, m *mcpv1alpha1.MCPServer) *appsv1.Deployment {
	ls := labelsForMCPServer(m.Name)
	replicas := int32(1)

	// Prepare container args
	args := []string{"run", "--foreground=true"}

	// Prepare container volume mounts
	volumeMounts := []corev1.VolumeMount{}
	volumes := []corev1.Volume{}

	// Check if global ConfigMap mode is enabled via environment variable
	useConfigMap := true

	// Also add pod template patch for secrets and service account (same as regular flags approach)
	// If service account is not specified, use the default MCP server service account
	serviceAccount := m.Spec.ServiceAccount
	if serviceAccount == nil {
		defaultSA := mcpServerServiceAccountName(m.Name)
		serviceAccount = &defaultSA
	}
	finalPodTemplateSpec := NewMCPServerPodTemplateSpecBuilder(m.Spec.PodTemplateSpec).
		WithServiceAccount(serviceAccount).
		WithSecrets(m.Spec.Secrets).
		Build()
	// Add pod template patch if we have one
	if finalPodTemplateSpec != nil {
		podTemplatePatch, err := json.Marshal(finalPodTemplateSpec)
		if err != nil {
			ctxLogger := log.FromContext(ctx)
			ctxLogger.Error(err, "Failed to marshal pod template spec")
		} else {
			args = append(args, fmt.Sprintf("--k8s-pod-patch=%s", string(podTemplatePatch)))
		}
	}

	// Add volume mount for ConfigMap
	configMapName := fmt.Sprintf("%s-runconfig", m.Name)
	volumeMounts = append(volumeMounts, corev1.VolumeMount{
		Name:      "runconfig",
		MountPath: "/etc/runconfig",
		ReadOnly:  true,
	})

	volumes = append(volumes, corev1.Volume{
		Name: "runconfig",
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: configMapName,
				},
			},
		},
	})

	// Add OIDC configuration args only if not using ConfigMap
	// When using ConfigMap, OIDC configuration is included in the runconfig.json
	if !useConfigMap && m.Spec.OIDCConfig != nil {
		// Create a context with timeout for OIDC configuration operations
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		oidcArgs := r.generateOIDCArgs(ctx, m)
		args = append(args, oidcArgs...)

		// Add OAuth discovery resource URL for RFC 9728 compliance
		resourceURL := m.Spec.OIDCConfig.ResourceURL
		if resourceURL == "" {
			resourceURL = createServiceURL(m.Name, m.Namespace, m.Spec.Port)
		}
		args = append(args, fmt.Sprintf("--resource-url=%s", resourceURL))
	}

	// Add authorization configuration args only if not using ConfigMap
	// When using ConfigMap, authorization configuration is included in the runconfig.json
	if !useConfigMap && m.Spec.AuthzConfig != nil {
		authzArgs := r.generateAuthzArgs(m)
		args = append(args, authzArgs...)
	}

	// Add audit configuration args only if not using ConfigMap
	// When using ConfigMap, audit configuration is included in the runconfig.json
	if !useConfigMap && m.Spec.Audit != nil && m.Spec.Audit.Enabled {
		args = append(args, "--enable-audit")
	}

	// Add environment variables and tools filter only if not using ConfigMap
	if !useConfigMap {
		// Add environment variables as --env flags for the MCP server
		for _, e := range m.Spec.Env {
			args = append(args, fmt.Sprintf("--env=%s=%s", e.Name, e.Value))
		}

		// Add tools filter args
		if len(m.Spec.ToolsFilter) > 0 {
			slices.Sort(m.Spec.ToolsFilter)
			args = append(args, fmt.Sprintf("--tools=%s", strings.Join(m.Spec.ToolsFilter, ",")))
		}
	}

	// Add OpenTelemetry configuration args only if not using ConfigMap
	// When using ConfigMap, telemetry configuration is included in the runconfig.json
	if !useConfigMap && m.Spec.Telemetry != nil {
		if m.Spec.Telemetry.OpenTelemetry != nil {
			otelArgs := r.generateOpenTelemetryArgs(m)
			args = append(args, otelArgs...)
		}

		if m.Spec.Telemetry.Prometheus != nil {
			prometheusArgs := r.generatePrometheusArgs(m)
			args = append(args, prometheusArgs...)
		}
	}

	// Always add the image as it's required by proxy runner command signature
	// When using ConfigMap, the image from ConfigMap takes precedence, but we still need
	// to provide this as a positional argument to satisfy the command requirements
	args = append(args, m.Spec.Image)

	// Add additional args only if not using ConfigMap
	if !useConfigMap && len(m.Spec.Args) > 0 {
		args = append(args, "--")
		args = append(args, m.Spec.Args...)
	}

	// Prepare container env vars for the proxy container
	env := []corev1.EnvVar{}

	// Add OpenTelemetry environment variables
	if m.Spec.Telemetry != nil && m.Spec.Telemetry.OpenTelemetry != nil {
		otelEnvVars := r.generateOpenTelemetryEnvVars(m)
		env = append(env, otelEnvVars...)
	}

	// Add user-specified proxy environment variables from ResourceOverrides
	if m.Spec.ResourceOverrides != nil && m.Spec.ResourceOverrides.ProxyDeployment != nil {
		for _, envVar := range m.Spec.ResourceOverrides.ProxyDeployment.Env {
			env = append(env, corev1.EnvVar{
				Name:  envVar.Name,
				Value: envVar.Value,
			})
		}
	}

	// Add volume mounts for user-defined volumes
	for _, v := range m.Spec.Volumes {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      v.Name,
			MountPath: v.MountPath,
			ReadOnly:  v.ReadOnly,
		})

		volumes = append(volumes, corev1.Volume{
			Name: v.Name,
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: v.HostPath,
				},
			},
		})
	}

	// Add volume mount for permission profile if using configmap
	if m.Spec.PermissionProfile != nil && m.Spec.PermissionProfile.Type == mcpv1alpha1.PermissionProfileTypeConfigMap {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "permission-profile",
			MountPath: "/etc/toolhive/profiles",
			ReadOnly:  true,
		})

		volumes = append(volumes, corev1.Volume{
			Name: "permission-profile",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: m.Spec.PermissionProfile.Name,
					},
				},
			},
		})
	}

	// Add volume mounts for authorization configuration
	authzVolumeMount, authzVolume := r.generateAuthzVolumeConfig(m)
	if authzVolumeMount != nil {
		volumeMounts = append(volumeMounts, *authzVolumeMount)
		volumes = append(volumes, *authzVolume)
	}

	// Prepare container resources
	resources := corev1.ResourceRequirements{}
	if m.Spec.Resources.Limits.CPU != "" || m.Spec.Resources.Limits.Memory != "" {
		resources.Limits = corev1.ResourceList{}
		if m.Spec.Resources.Limits.CPU != "" {
			resources.Limits[corev1.ResourceCPU] = resource.MustParse(m.Spec.Resources.Limits.CPU)
		}
		if m.Spec.Resources.Limits.Memory != "" {
			resources.Limits[corev1.ResourceMemory] = resource.MustParse(m.Spec.Resources.Limits.Memory)
		}
	}
	if m.Spec.Resources.Requests.CPU != "" || m.Spec.Resources.Requests.Memory != "" {
		resources.Requests = corev1.ResourceList{}
		if m.Spec.Resources.Requests.CPU != "" {
			resources.Requests[corev1.ResourceCPU] = resource.MustParse(m.Spec.Resources.Requests.CPU)
		}
		if m.Spec.Resources.Requests.Memory != "" {
			resources.Requests[corev1.ResourceMemory] = resource.MustParse(m.Spec.Resources.Requests.Memory)
		}
	}

	// Prepare deployment metadata with overrides
	deploymentLabels := ls
	deploymentAnnotations := make(map[string]string)

	deploymentTemplateLabels := ls
	deploymentTemplateAnnotations := make(map[string]string)

	if m.Spec.ResourceOverrides != nil && m.Spec.ResourceOverrides.ProxyDeployment != nil {
		if m.Spec.ResourceOverrides.ProxyDeployment.Labels != nil {
			deploymentLabels = mergeLabels(ls, m.Spec.ResourceOverrides.ProxyDeployment.Labels)
		}
		if m.Spec.ResourceOverrides.ProxyDeployment.Annotations != nil {
			deploymentAnnotations = mergeAnnotations(make(map[string]string), m.Spec.ResourceOverrides.ProxyDeployment.Annotations)
		}

		if m.Spec.ResourceOverrides.ProxyDeployment.PodTemplateMetadataOverrides != nil {
			if m.Spec.ResourceOverrides.ProxyDeployment.PodTemplateMetadataOverrides.Labels != nil {
				deploymentLabels = mergeLabels(ls, m.Spec.ResourceOverrides.ProxyDeployment.PodTemplateMetadataOverrides.Labels)
			}
			if m.Spec.ResourceOverrides.ProxyDeployment.PodTemplateMetadataOverrides.Annotations != nil {
				deploymentTemplateAnnotations = mergeAnnotations(deploymentAnnotations,
					m.Spec.ResourceOverrides.ProxyDeployment.PodTemplateMetadataOverrides.Annotations)
			}
		}
	}

	// Check for Vault Agent Injection and add env-file-dir argument if needed
	// Only add the flag when not using ConfigMap mode (when using ConfigMap, this is handled via the runconfig.json)
	if !useConfigMap && hasVaultAgentInjection(deploymentTemplateAnnotations) {
		args = append(args, "--env-file-dir=/vault/secrets")
	}

	// Detect platform and prepare ProxyRunner's pod and container security context
	_, err := r.detectPlatform(ctx)
	if err != nil {
		ctxLogger := log.FromContext(ctx)
		ctxLogger.Error(err, "Failed to detect platform, defaulting to Kubernetes", "mcpserver", m.Name)
	}

	// Use SecurityContextBuilder for platform-aware security context
	securityBuilder := kubernetes.NewSecurityContextBuilder(r.detectedPlatform)
	proxyRunnerPodSecurityContext := securityBuilder.BuildPodSecurityContext()
	proxyRunnerContainerSecurityContext := securityBuilder.BuildContainerSecurityContext()

	env = ensureRequiredEnvVars(ctx, env)

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        m.Name,
			Namespace:   m.Namespace,
			Labels:      deploymentLabels,
			Annotations: deploymentAnnotations,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: ls, // Keep original labels for selector
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      deploymentTemplateLabels,
					Annotations: deploymentTemplateAnnotations,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: proxyRunnerServiceAccountName(m.Name),
					Containers: []corev1.Container{{
						Image:        getToolhiveRunnerImage(),
						Name:         "toolhive",
						Args:         args,
						Env:          env,
						VolumeMounts: volumeMounts,
						Resources:    resources,
						Ports: []corev1.ContainerPort{{
							ContainerPort: m.Spec.Port,
							Name:          "http",
							Protocol:      corev1.ProtocolTCP,
						}},
						LivenessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/health",
									Port: intstr.FromString("http"),
								},
							},
							InitialDelaySeconds: 30,
							PeriodSeconds:       10,
							TimeoutSeconds:      5,
							FailureThreshold:    3,
						},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/health",
									Port: intstr.FromString("http"),
								},
							},
							InitialDelaySeconds: 5,
							PeriodSeconds:       5,
							TimeoutSeconds:      3,
							FailureThreshold:    3,
						},
						SecurityContext: proxyRunnerContainerSecurityContext,
					}},
					Volumes:         volumes,
					SecurityContext: proxyRunnerPodSecurityContext,
				},
			},
		},
	}

	// Set MCPServer instance as the owner and controller
	if err := controllerutil.SetControllerReference(m, dep, r.Scheme); err != nil {
		ctxLogger := log.FromContext(ctx)
		ctxLogger.Error(err, "Failed to set controller reference for Deployment")
		return nil
	}
	return dep
}

func ensureRequiredEnvVars(ctx context.Context, env []corev1.EnvVar) []corev1.EnvVar {
	// Check for the existence of the XDG_CONFIG_HOME, HOME, TOOLHIVE_RUNTIME, and UNSTRUCTURED_LOGS environment variables
	// and set them to defaults if they don't exist
	ctxLogger := log.FromContext(ctx)
	xdgConfigHomeFound := false
	homeFound := false
	toolhiveRuntimeFound := false
	unstructuredLogsFound := false
	for _, envVar := range env {
		if envVar.Name == "XDG_CONFIG_HOME" {
			xdgConfigHomeFound = true
		}
		if envVar.Name == "HOME" {
			homeFound = true
		}
		if envVar.Name == "TOOLHIVE_RUNTIME" {
			toolhiveRuntimeFound = true
		}
		if envVar.Name == "UNSTRUCTURED_LOGS" {
			unstructuredLogsFound = true
		}
	}
	if !xdgConfigHomeFound {
		ctxLogger.V(1).Info("XDG_CONFIG_HOME not found, setting to /tmp")
		env = append(env, corev1.EnvVar{
			Name:  "XDG_CONFIG_HOME",
			Value: "/tmp",
		})
	}
	if !homeFound {
		ctxLogger.V(1).Info("HOME not found, setting to /tmp")
		env = append(env, corev1.EnvVar{
			Name:  "HOME",
			Value: "/tmp",
		})
	}
	if !toolhiveRuntimeFound {
		ctxLogger.V(1).Info("TOOLHIVE_RUNTIME not found, setting to kubernetes")
		env = append(env, corev1.EnvVar{
			Name:  "TOOLHIVE_RUNTIME",
			Value: "kubernetes",
		})
	}
	// Always use structured JSON logs in Kubernetes (not configurable)
	if !unstructuredLogsFound {
		ctxLogger.V(1).Info("UNSTRUCTURED_LOGS not found, setting to false for structured JSON logging")
		env = append(env, corev1.EnvVar{
			Name:  "UNSTRUCTURED_LOGS",
			Value: "false",
		})
	}
	return env
}

func createServiceName(mcpServerName string) string {
	return fmt.Sprintf("mcp-%s-proxy", mcpServerName)
}

// createServiceURL generates the full cluster-local service URL for an MCP server
func createServiceURL(mcpServerName, namespace string, port int32) string {
	serviceName := createServiceName(mcpServerName)
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", serviceName, namespace, port)
}

// serviceForMCPServer returns a MCPServer Service object
func (r *MCPServerReconciler) serviceForMCPServer(ctx context.Context, m *mcpv1alpha1.MCPServer) *corev1.Service {
	ls := labelsForMCPServer(m.Name)

	// we want to generate a service name that is unique for the proxy service
	// to avoid conflicts with the headless service
	svcName := createServiceName(m.Name)

	// Prepare service metadata with overrides
	serviceLabels := ls
	serviceAnnotations := make(map[string]string)

	if m.Spec.ResourceOverrides != nil && m.Spec.ResourceOverrides.ProxyService != nil {
		if m.Spec.ResourceOverrides.ProxyService.Labels != nil {
			serviceLabels = mergeLabels(ls, m.Spec.ResourceOverrides.ProxyService.Labels)
		}
		if m.Spec.ResourceOverrides.ProxyService.Annotations != nil {
			serviceAnnotations = mergeAnnotations(make(map[string]string), m.Spec.ResourceOverrides.ProxyService.Annotations)
		}
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        svcName,
			Namespace:   m.Namespace,
			Labels:      serviceLabels,
			Annotations: serviceAnnotations,
		},
		Spec: corev1.ServiceSpec{
			Selector: ls, // Keep original labels for selector
			Ports: []corev1.ServicePort{{
				Port:       m.Spec.Port,
				TargetPort: intstr.FromInt(int(m.Spec.Port)),
				Protocol:   corev1.ProtocolTCP,
				Name:       "http",
			}},
		},
	}

	// Set MCPServer instance as the owner and controller
	if err := controllerutil.SetControllerReference(m, svc, r.Scheme); err != nil {
		ctxLogger := log.FromContext(ctx)
		ctxLogger.Error(err, "Failed to set controller reference for Service")
		return nil
	}
	return svc
}

// updateMCPServerStatus updates the status of the MCPServer
func (r *MCPServerReconciler) updateMCPServerStatus(ctx context.Context, m *mcpv1alpha1.MCPServer) error {
	// List the pods for this MCPServer's deployment
	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(m.Namespace),
		client.MatchingLabels(labelsForMCPServer(m.Name)),
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
			// Treat succeeded pods as running for status purposes
			running++
		case corev1.PodUnknown:
			// Treat unknown pods as pending for status purposes
			pending++
		}
	}

	// Update the status
	if running > 0 {
		m.Status.Phase = mcpv1alpha1.MCPServerPhaseRunning
		m.Status.Message = "MCP server is running"
	} else if pending > 0 {
		m.Status.Phase = mcpv1alpha1.MCPServerPhasePending
		m.Status.Message = "MCP server is starting"
	} else if failed > 0 {
		m.Status.Phase = mcpv1alpha1.MCPServerPhaseFailed
		m.Status.Message = "MCP server failed to start"
	} else {
		m.Status.Phase = mcpv1alpha1.MCPServerPhasePending
		m.Status.Message = "No pods found for MCP server"
	}

	// Update the status
	return r.Status().Update(ctx, m)
}

// finalizeMCPServer performs the finalizer logic for the MCPServer
func (r *MCPServerReconciler) finalizeMCPServer(ctx context.Context, m *mcpv1alpha1.MCPServer) error {
	ctxLogger := log.FromContext(ctx)
	// Update the MCPServer status
	m.Status.Phase = mcpv1alpha1.MCPServerPhaseTerminating
	m.Status.Message = "MCP server is being terminated"
	if err := r.Status().Update(ctx, m); err != nil {
		return err
	}

	// Step 2: Attempt to delete associated StatefulSet by name
	sts := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Name: m.Name, Namespace: m.Namespace}, sts)
	if err == nil {
		// StatefulSet found, delete it
		if delErr := r.Delete(ctx, sts); delErr != nil && !errors.IsNotFound(delErr) {
			return fmt.Errorf("failed to delete StatefulSet %s: %w", m.Name, delErr)
		}
	} else if !errors.IsNotFound(err) {
		// Unexpected error (not just "not found")
		return fmt.Errorf("failed to get StatefulSet %s: %w", m.Name, err)
	}

	// Step 3: Attempt to delete associated service by name
	svc := &corev1.Service{}
	serviceName := fmt.Sprintf("mcp-%s-headless", m.Name)
	err = r.Get(ctx, types.NamespacedName{Name: serviceName, Namespace: m.Namespace}, svc)
	if err == nil {
		if delErr := r.Delete(ctx, svc); delErr != nil && !errors.IsNotFound(delErr) {
			return fmt.Errorf("failed to delete Service %s: %w", serviceName, delErr)
		}
	} else if !errors.IsNotFound(err) {
		return fmt.Errorf("failed to check Service %s: %w", serviceName, err)
	}

	// Step 4: Delete associated RunConfig ConfigMap
	runConfigName := fmt.Sprintf("%s-runconfig", m.Name)
	runConfigMap := &corev1.ConfigMap{}
	err = r.Get(ctx, types.NamespacedName{Name: runConfigName, Namespace: m.Namespace}, runConfigMap)
	if err == nil {
		if delErr := r.Delete(ctx, runConfigMap); delErr != nil && !errors.IsNotFound(delErr) {
			return fmt.Errorf("failed to delete RunConfig ConfigMap %s: %w", runConfigName, delErr)
		}
		ctxLogger.Info("Deleted RunConfig ConfigMap", "name", runConfigName, "namespace", m.Namespace)
	} else if !errors.IsNotFound(err) {
		return fmt.Errorf("failed to check RunConfig ConfigMap %s: %w", runConfigName, err)
	}

	// The owner references will automatically delete the deployment and service
	// when the MCPServer is deleted, so we don't need to do anything here.
	return nil
}

// deploymentNeedsUpdate checks if the deployment needs to be updated
//
//nolint:gocyclo
func (r *MCPServerReconciler) deploymentNeedsUpdate(
	ctx context.Context,
	deployment *appsv1.Deployment,
	mcpServer *mcpv1alpha1.MCPServer,
) bool {
	// Check if the container args have changed
	if len(deployment.Spec.Template.Spec.Containers) > 0 {
		container := deployment.Spec.Template.Spec.Containers[0]

		// Check if the toolhive runner image has changed
		if container.Image != getToolhiveRunnerImage() {
			return true
		}

		// Check if the args contain the correct image
		imageArg := mcpServer.Spec.Image
		found := false
		for _, arg := range container.Args {
			if arg == imageArg {
				found = true
				break
			}
		}
		if !found {
			return true
		}

		// Check if the port has changed
		portArg := fmt.Sprintf("--proxy-port=%d", mcpServer.Spec.Port)
		found = false
		for _, arg := range container.Args {
			if arg == portArg {
				found = true
				break
			}
		}
		if !found {
			return true
		}

		// Check if the transport has changed
		transportArg := fmt.Sprintf("--transport=%s", mcpServer.Spec.Transport)
		found = false
		for _, arg := range container.Args {
			if arg == transportArg {
				found = true
				break
			}
		}
		if !found {
			return true
		}

		// Check if the tools filter has changed (order-independent)
		if !equalToolsFilter(mcpServer.Spec.ToolsFilter, container.Args) {
			return true
		}

		// Check if the pod template spec has changed

		// TODO: Add more comprehensive checks for PodTemplateSpec changes beyond just the args
		// This would involve comparing the actual pod template spec fields with what would be
		// generated by the operator, rather than just checking the command-line arguments.
		if mcpServer.Spec.PodTemplateSpec != nil {
			podTemplatePatch, err := json.Marshal(mcpServer.Spec.PodTemplateSpec)
			if err == nil {
				podTemplatePatchArg := fmt.Sprintf("--k8s-pod-patch=%s", string(podTemplatePatch))
				found := false
				for _, arg := range container.Args {
					if arg == podTemplatePatchArg {
						found = true
						break
					}
				}
				if !found {
					return true
				}
			}
		}

		// Check if the container port has changed
		if len(container.Ports) > 0 && container.Ports[0].ContainerPort != mcpServer.Spec.Port {
			return true
		}

		// Check if the environment variables have changed (now passed as --env flags)
		for _, envVar := range mcpServer.Spec.Env {
			envArg := fmt.Sprintf("--env=%s=%s", envVar.Name, envVar.Value)
			found := false
			for _, arg := range container.Args {
				if arg == envArg {
					found = true
					break
				}
			}
			if !found {
				return true
			}
		}

		// Check if the proxy environment variables have changed
		expectedProxyEnv := []corev1.EnvVar{}

		// Add OpenTelemetry environment variables first
		if mcpServer.Spec.Telemetry != nil && mcpServer.Spec.Telemetry.OpenTelemetry != nil {
			otelEnvVars := r.generateOpenTelemetryEnvVars(mcpServer)
			expectedProxyEnv = append(expectedProxyEnv, otelEnvVars...)
		}

		// Add user-specified environment variables
		if mcpServer.Spec.ResourceOverrides != nil && mcpServer.Spec.ResourceOverrides.ProxyDeployment != nil {
			for _, envVar := range mcpServer.Spec.ResourceOverrides.ProxyDeployment.Env {
				expectedProxyEnv = append(expectedProxyEnv, corev1.EnvVar{
					Name:  envVar.Name,
					Value: envVar.Value,
				})
			}
		}
		// Add default environment variables that are always injected
		expectedProxyEnv = ensureRequiredEnvVars(ctx, expectedProxyEnv)
		if !reflect.DeepEqual(container.Env, expectedProxyEnv) {
			return true
		}

		// Check if the pod template spec has changed (including secrets)
		// If service account is not specified, use the default MCP server service account
		serviceAccount := mcpServer.Spec.ServiceAccount
		if serviceAccount == nil {
			defaultSA := mcpServerServiceAccountName(mcpServer.Name)
			serviceAccount = &defaultSA
		}
		expectedPodTemplateSpec := NewMCPServerPodTemplateSpecBuilder(mcpServer.Spec.PodTemplateSpec).
			WithServiceAccount(serviceAccount).
			WithSecrets(mcpServer.Spec.Secrets).
			Build()

		// Find the current pod template patch in the container args
		var currentPodTemplatePatch string
		for _, arg := range container.Args {
			if strings.HasPrefix(arg, "--k8s-pod-patch=") {
				currentPodTemplatePatch = arg[16:] // Remove "--k8s-pod-patch=" prefix
				break
			}
		}

		// Compare expected vs current pod template spec
		if expectedPodTemplateSpec != nil {
			expectedPatch, err := json.Marshal(expectedPodTemplateSpec)
			if err != nil {
				ctxLogger := log.FromContext(ctx)
				ctxLogger.Error(err, "Failed to marshal expected pod template spec")
				return true // Assume change if we can't marshal
			}
			expectedPatchString := string(expectedPatch)

			if currentPodTemplatePatch != expectedPatchString {
				return true
			}
		} else if currentPodTemplatePatch != "" {
			// Expected no patch but current has one
			return true
		}

		// Check if the resource requirements have changed
		if !reflect.DeepEqual(container.Resources, resourceRequirementsForMCPServer(mcpServer)) {
			return true
		}

		// Check if the targetPort has changed
		if mcpServer.Spec.TargetPort != 0 {
			targetPortArg := fmt.Sprintf("--target-port=%d", mcpServer.Spec.TargetPort)
			found := false
			for _, arg := range container.Args {
				if arg == targetPortArg {
					found = true
					break
				}
			}
			if !found {
				return true
			}
		} else {
			for _, arg := range container.Args {
				if strings.HasPrefix(arg, "--target-port=") {
					return true
				}
			}
		}

		// Check if OpenTelemetry arguments have changed
		var otelConfig *mcpv1alpha1.OpenTelemetryConfig
		if mcpServer.Spec.Telemetry != nil {
			otelConfig = mcpServer.Spec.Telemetry.OpenTelemetry
		}
		if !equalOpenTelemetryArgs(otelConfig, container.Args) {
			return true
		}

	}

	// Check if the service account name has changed
	// ServiceAccountName: treat empty (not yet set) as equal to the expected default
	expectedServiceAccountName := proxyRunnerServiceAccountName(mcpServer.Name)
	currentServiceAccountName := deployment.Spec.Template.Spec.ServiceAccountName
	if currentServiceAccountName != "" && currentServiceAccountName != expectedServiceAccountName {
		return true
	}

	// Check if the deployment metadata (labels/annotations) have changed due to resource overrides
	expectedLabels := labelsForMCPServer(mcpServer.Name)
	expectedAnnotations := make(map[string]string)

	if mcpServer.Spec.ResourceOverrides != nil && mcpServer.Spec.ResourceOverrides.ProxyDeployment != nil {
		if mcpServer.Spec.ResourceOverrides.ProxyDeployment.Labels != nil {
			expectedLabels = mergeLabels(expectedLabels, mcpServer.Spec.ResourceOverrides.ProxyDeployment.Labels)
		}
		if mcpServer.Spec.ResourceOverrides.ProxyDeployment.Annotations != nil {
			expectedAnnotations = mergeAnnotations(make(map[string]string), mcpServer.Spec.ResourceOverrides.ProxyDeployment.Annotations)
		}
	}

	if !maps.Equal(deployment.Labels, expectedLabels) {
		return true
	}

	if !maps.Equal(deployment.Annotations, expectedAnnotations) {
		return true
	}

	return false
}

// serviceNeedsUpdate checks if the service needs to be updated
func serviceNeedsUpdate(service *corev1.Service, mcpServer *mcpv1alpha1.MCPServer) bool {
	// Check if the service port has changed
	if len(service.Spec.Ports) > 0 && service.Spec.Ports[0].Port != mcpServer.Spec.Port {
		return true
	}

	// Check if the service metadata (labels/annotations) have changed due to resource overrides
	expectedLabels := labelsForMCPServer(mcpServer.Name)
	expectedAnnotations := make(map[string]string)

	if mcpServer.Spec.ResourceOverrides != nil && mcpServer.Spec.ResourceOverrides.ProxyService != nil {
		if mcpServer.Spec.ResourceOverrides.ProxyService.Labels != nil {
			expectedLabels = mergeLabels(expectedLabels, mcpServer.Spec.ResourceOverrides.ProxyService.Labels)
		}
		if mcpServer.Spec.ResourceOverrides.ProxyService.Annotations != nil {
			expectedAnnotations = mergeAnnotations(make(map[string]string), mcpServer.Spec.ResourceOverrides.ProxyService.Annotations)
		}
	}

	if !maps.Equal(service.Labels, expectedLabels) {
		return true
	}

	if !maps.Equal(service.Annotations, expectedAnnotations) {
		return true
	}

	return false
}

// resourceRequirementsForMCPServer returns the resource requirements for the MCPServer
func resourceRequirementsForMCPServer(m *mcpv1alpha1.MCPServer) corev1.ResourceRequirements {
	resources := corev1.ResourceRequirements{}
	if m.Spec.Resources.Limits.CPU != "" || m.Spec.Resources.Limits.Memory != "" {
		resources.Limits = corev1.ResourceList{}
		if m.Spec.Resources.Limits.CPU != "" {
			resources.Limits[corev1.ResourceCPU] = resource.MustParse(m.Spec.Resources.Limits.CPU)
		}
		if m.Spec.Resources.Limits.Memory != "" {
			resources.Limits[corev1.ResourceMemory] = resource.MustParse(m.Spec.Resources.Limits.Memory)
		}
	}
	if m.Spec.Resources.Requests.CPU != "" || m.Spec.Resources.Requests.Memory != "" {
		resources.Requests = corev1.ResourceList{}
		if m.Spec.Resources.Requests.CPU != "" {
			resources.Requests[corev1.ResourceCPU] = resource.MustParse(m.Spec.Resources.Requests.CPU)
		}
		if m.Spec.Resources.Requests.Memory != "" {
			resources.Requests[corev1.ResourceMemory] = resource.MustParse(m.Spec.Resources.Requests.Memory)
		}
	}
	return resources
}

// proxyRunnerServiceAccountName returns the service account name for the proxy runner
func proxyRunnerServiceAccountName(mcpServerName string) string {
	return fmt.Sprintf("%s-proxy-runner", mcpServerName)
}

// mcpServerServiceAccountName returns the service account name for the mcp server
func mcpServerServiceAccountName(mcpServerName string) string {
	return fmt.Sprintf("%s-sa", mcpServerName)
}

// labelsForMCPServer returns the labels for selecting the resources
// belonging to the given MCPServer CR name.
func labelsForMCPServer(name string) map[string]string {
	return map[string]string{
		"app":                        "mcpserver",
		"app.kubernetes.io/name":     "mcpserver",
		"app.kubernetes.io/instance": name,
		"toolhive":                   "true",
		"toolhive-name":              name,
	}
}

// labelsForInlineAuthzConfig returns the labels for inline authorization ConfigMaps
// belonging to the given MCPServer CR name.
func labelsForInlineAuthzConfig(name string) map[string]string {
	labels := labelsForMCPServer(name)
	labels[authzLabelKey] = authzLabelValueInline
	return labels
}

// generateAuthzVolumeConfig generates volume mount and volume configuration for authorization policies
// Returns nil for both if no authorization configuration is present
func (*MCPServerReconciler) generateAuthzVolumeConfig(m *mcpv1alpha1.MCPServer) (*corev1.VolumeMount, *corev1.Volume) {
	if m.Spec.AuthzConfig == nil {
		return nil, nil
	}

	switch m.Spec.AuthzConfig.Type {
	case mcpv1alpha1.AuthzConfigTypeConfigMap:
		if m.Spec.AuthzConfig.ConfigMap == nil {
			return nil, nil
		}

		volumeMount := &corev1.VolumeMount{
			Name:      "authz-config",
			MountPath: "/etc/toolhive/authz",
			ReadOnly:  true,
		}

		volume := &corev1.Volume{
			Name: "authz-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: m.Spec.AuthzConfig.ConfigMap.Name,
					},
					Items: []corev1.KeyToPath{
						{
							Key: func() string {
								if m.Spec.AuthzConfig.ConfigMap.Key != "" {
									return m.Spec.AuthzConfig.ConfigMap.Key
								}
								return defaultAuthzKey
							}(),
							Path: defaultAuthzKey,
						},
					},
				},
			},
		}

		return volumeMount, volume

	case mcpv1alpha1.AuthzConfigTypeInline:
		if m.Spec.AuthzConfig.Inline == nil {
			return nil, nil
		}

		volumeMount := &corev1.VolumeMount{
			Name:      "authz-config",
			MountPath: "/etc/toolhive/authz",
			ReadOnly:  true,
		}

		volume := &corev1.Volume{
			Name: "authz-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: fmt.Sprintf("%s-authz-inline", m.Name),
					},
					Items: []corev1.KeyToPath{
						{
							Key:  defaultAuthzKey,
							Path: defaultAuthzKey,
						},
					},
				},
			},
		}

		return volumeMount, volume

	default:
		return nil, nil
	}
}

// mergeStringMaps merges override map with default map, with default map taking precedence
// This ensures that operator-required metadata is preserved for proper functionality
func mergeStringMaps(defaultMap, overrideMap map[string]string) map[string]string {
	result := maps.Clone(overrideMap)
	maps.Copy(result, defaultMap) // default map takes precedence
	return result
}

// mergeLabels merges override labels with default labels, with default labels taking precedence
func mergeLabels(defaultLabels, overrideLabels map[string]string) map[string]string {
	return mergeStringMaps(defaultLabels, overrideLabels)
}

// mergeAnnotations merges override annotations with default annotations, with default annotations taking precedence
func mergeAnnotations(defaultAnnotations, overrideAnnotations map[string]string) map[string]string {
	return mergeStringMaps(defaultAnnotations, overrideAnnotations)
}

// hasVaultAgentInjection checks if Vault Agent Injection is enabled in the pod annotations
func hasVaultAgentInjection(annotations map[string]string) bool {
	if annotations == nil {
		return false
	}

	// Check if vault.hashicorp.com/agent-inject annotation is present and set to "true"
	value, exists := annotations["vault.hashicorp.com/agent-inject"]
	return exists && value == "true"
}

// getToolhiveRunnerImage returns the image to use for the toolhive runner container
func getToolhiveRunnerImage() string {
	// Get the image from the environment variable or use a default
	image := os.Getenv("TOOLHIVE_RUNNER_IMAGE")
	if image == "" {
		// Default to the published image
		image = "ghcr.io/stacklok/toolhive/proxyrunner:latest"
	}
	return image
}

// generateOIDCArgs generates OIDC command-line arguments based on the OIDC configuration
func (r *MCPServerReconciler) generateOIDCArgs(ctx context.Context, m *mcpv1alpha1.MCPServer) []string {
	var args []string

	if m.Spec.OIDCConfig == nil {
		return args
	}

	switch m.Spec.OIDCConfig.Type {
	case mcpv1alpha1.OIDCConfigTypeKubernetes:
		args = append(args, r.generateKubernetesOIDCArgs(ctx, m)...)
	case mcpv1alpha1.OIDCConfigTypeConfigMap:
		args = append(args, r.generateConfigMapOIDCArgs(ctx, m)...)
	case mcpv1alpha1.OIDCConfigTypeInline:
		args = append(args, r.generateInlineOIDCArgs(m)...)
	}

	return args
}

// generateKubernetesOIDCArgs generates OIDC args for Kubernetes service account token validation
func (*MCPServerReconciler) generateKubernetesOIDCArgs(ctx context.Context, m *mcpv1alpha1.MCPServer) []string {
	var args []string
	config := m.Spec.OIDCConfig.Kubernetes

	// Set defaults if config is nil
	if config == nil {
		ctxLogger := log.FromContext(ctx)
		ctxLogger.Info("Kubernetes OIDCConfig is nil, using default configuration", "mcpServer", m.Name)
		defaultUseClusterAuth := true
		config = &mcpv1alpha1.KubernetesOIDCConfig{
			UseClusterAuth: &defaultUseClusterAuth, // Default to true
		}
	}

	// Handle UseClusterAuth with default of true if nil
	useClusterAuth := true // default value
	if config.UseClusterAuth != nil {
		useClusterAuth = *config.UseClusterAuth
	}

	// Issuer (default: https://kubernetes.default.svc)
	issuer := config.Issuer
	if issuer == "" {
		issuer = "https://kubernetes.default.svc"
	}
	args = append(args, fmt.Sprintf("--oidc-issuer=%s", issuer))

	// Audience (default: toolhive)
	audience := config.Audience
	if audience == "" {
		audience = "toolhive"
	}
	args = append(args, fmt.Sprintf("--oidc-audience=%s", audience))

	// JWKS URL (optional - if empty, thv will use OIDC discovery)
	jwksURL := config.JWKSURL
	if jwksURL != "" {
		args = append(args, fmt.Sprintf("--oidc-jwks-url=%s", jwksURL))
	}

	// Introspection URL (optional - if empty, thv will use OIDC discovery)
	introspectionURL := config.IntrospectionURL
	if introspectionURL != "" {
		args = append(args, fmt.Sprintf("--oidc-introspection-url=%s", introspectionURL))
	}

	// Add cluster auth flags if enabled (default is true)
	if useClusterAuth {
		args = append(args, "--thv-ca-bundle=/var/run/secrets/kubernetes.io/serviceaccount/ca.crt")
		args = append(args, "--jwks-auth-token-file=/var/run/secrets/kubernetes.io/serviceaccount/token")
		args = append(args, "--jwks-allow-private-ip")
	}

	// Client ID (format: {serviceAccount}.{namespace}.svc.cluster.local)
	serviceAccount := config.ServiceAccount
	if serviceAccount == "" {
		serviceAccount = "default" // Use default service account if not specified
	}

	namespace := config.Namespace
	if namespace == "" {
		namespace = m.Namespace // Use MCPServer's namespace if not specified
	}

	clientID := fmt.Sprintf("%s.%s.svc.cluster.local", serviceAccount, namespace)
	args = append(args, fmt.Sprintf("--oidc-client-id=%s", clientID))

	return args
}

// generateConfigMapOIDCArgs generates OIDC args for ConfigMap-based configuration
func (r *MCPServerReconciler) generateConfigMapOIDCArgs( // nolint:gocyclo
	ctx context.Context,
	m *mcpv1alpha1.MCPServer,
) []string {
	var args []string
	config := m.Spec.OIDCConfig.ConfigMap

	if config == nil {
		return args
	}

	// Read the ConfigMap and extract OIDC configuration from documented keys
	configMap := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      config.Name,
		Namespace: m.Namespace,
	}, configMap)
	if err != nil {
		ctxLogger := log.FromContext(ctx)
		ctxLogger.Error(err, "Failed to get ConfigMap", "configMapName", config.Name)
		return args
	}

	// Extract OIDC configuration from well-known keys
	if issuer, exists := configMap.Data["issuer"]; exists && issuer != "" {
		args = append(args, fmt.Sprintf("--oidc-issuer=%s", issuer))
	}
	if audience, exists := configMap.Data["audience"]; exists && audience != "" {
		args = append(args, fmt.Sprintf("--oidc-audience=%s", audience))
	}
	if jwksURL, exists := configMap.Data["jwksUrl"]; exists && jwksURL != "" {
		args = append(args, fmt.Sprintf("--oidc-jwks-url=%s", jwksURL))
	}
	if introspectionURL, exists := configMap.Data["introspectionUrl"]; exists && introspectionURL != "" {
		args = append(args, fmt.Sprintf("--oidc-introspection-url=%s", introspectionURL))
	}
	if clientID, exists := configMap.Data["clientId"]; exists && clientID != "" {
		args = append(args, fmt.Sprintf("--oidc-client-id=%s", clientID))
	}
	if clientSecret, exists := configMap.Data["clientSecret"]; exists && clientSecret != "" {
		args = append(args, fmt.Sprintf("--oidc-client-secret=%s", clientSecret))
	}
	if thvCABundlePath, exists := configMap.Data["thvCABundlePath"]; exists && thvCABundlePath != "" {
		args = append(args, fmt.Sprintf("--thv-ca-bundle=%s", thvCABundlePath))
	}
	if jwksAuthTokenPath, exists := configMap.Data["jwksAuthTokenPath"]; exists && jwksAuthTokenPath != "" {
		args = append(args, fmt.Sprintf("--jwks-auth-token-file=%s", jwksAuthTokenPath))
	}
	if jwksAllowPrivateIP, exists := configMap.Data["jwksAllowPrivateIP"]; exists && jwksAllowPrivateIP == "true" {
		args = append(args, "--jwks-allow-private-ip")
	}

	return args
}

// generateInlineOIDCArgs generates OIDC args for inline configuration
func (*MCPServerReconciler) generateInlineOIDCArgs(m *mcpv1alpha1.MCPServer) []string {
	var args []string
	config := m.Spec.OIDCConfig.Inline

	if config == nil {
		return args
	}

	// Issuer (required)
	if config.Issuer != "" {
		args = append(args, fmt.Sprintf("--oidc-issuer=%s", config.Issuer))
	}

	// Audience (optional)
	if config.Audience != "" {
		args = append(args, fmt.Sprintf("--oidc-audience=%s", config.Audience))
	}

	// JWKS URL (optional)
	if config.JWKSURL != "" {
		args = append(args, fmt.Sprintf("--oidc-jwks-url=%s", config.JWKSURL))
	}

	// Introspection URL (optional)
	if config.IntrospectionURL != "" {
		args = append(args, fmt.Sprintf("--oidc-introspection-url=%s", config.IntrospectionURL))
	}

	// CA Bundle path (optional)
	if config.ThvCABundlePath != "" {
		args = append(args, fmt.Sprintf("--thv-ca-bundle=%s", config.ThvCABundlePath))
	}

	// Auth token path (optional)
	if config.JWKSAuthTokenPath != "" {
		args = append(args, fmt.Sprintf("--jwks-auth-token-file=%s", config.JWKSAuthTokenPath))
	}

	// Allow private IP access (optional)
	if config.JWKSAllowPrivateIP {
		args = append(args, "--jwks-allow-private-ip")
	}

	// Client ID (optional)
	if config.ClientID != "" {
		args = append(args, fmt.Sprintf("--oidc-client-id=%s", config.ClientID))
	}

	return args
}

// generateAuthzArgs generates authorization command-line arguments based on the configuration type
func (*MCPServerReconciler) generateAuthzArgs(m *mcpv1alpha1.MCPServer) []string {
	var args []string

	if m.Spec.AuthzConfig == nil {
		return args
	}

	// Validate that the configuration is properly set based on type
	switch m.Spec.AuthzConfig.Type {
	case mcpv1alpha1.AuthzConfigTypeConfigMap:
		if m.Spec.AuthzConfig.ConfigMap == nil {
			return args
		}
	case mcpv1alpha1.AuthzConfigTypeInline:
		if m.Spec.AuthzConfig.Inline == nil {
			return args
		}
	default:
		return args
	}

	// Both ConfigMap and inline configurations use the same mounted path
	authzConfigPath := fmt.Sprintf("/etc/toolhive/authz/%s", defaultAuthzKey)
	args = append(args, fmt.Sprintf("--authz-config=%s", authzConfigPath))

	return args
}

// generatePrometheusArgs generates Prometheus command-line arguments based on the configuration
func (*MCPServerReconciler) generatePrometheusArgs(m *mcpv1alpha1.MCPServer) []string {
	var args []string

	if m.Spec.Telemetry == nil || m.Spec.Telemetry.Prometheus == nil {
		return args
	}

	// Add Prometheus metrics path flag if Prometheus is enabled in telemetry config
	if m.Spec.Telemetry.Prometheus.Enabled {
		args = append(args, "--enable-prometheus-metrics-path")
	}

	return args
}

// generateOpenTelemetryArgs generates OpenTelemetry command-line arguments based on the configuration
func (*MCPServerReconciler) generateOpenTelemetryArgs(m *mcpv1alpha1.MCPServer) []string {
	var args []string

	if m.Spec.Telemetry == nil || m.Spec.Telemetry.OpenTelemetry == nil {
		return args
	}

	otel := m.Spec.Telemetry.OpenTelemetry

	// Add endpoint
	if otel.Endpoint != "" {
		args = append(args, fmt.Sprintf("--otel-endpoint=%s", otel.Endpoint))
	}

	// Add service name
	if otel.ServiceName != "" {
		args = append(args, fmt.Sprintf("--otel-service-name=%s", otel.ServiceName))
	}

	// Add headers (multiple --otel-headers flags)
	for _, header := range otel.Headers {
		args = append(args, fmt.Sprintf("--otel-headers=%s", header))
	}

	// Add insecure flag
	if otel.Insecure {
		args = append(args, "--otel-insecure")
	}

	// Handle tracing configuration
	if otel.Tracing != nil {
		if otel.Tracing.Enabled {
			args = append(args, "--otel-tracing-enabled=true")
			args = append(args, fmt.Sprintf("--otel-tracing-sampling-rate=%s", otel.Tracing.SamplingRate))
		} else {
			args = append(args, "--otel-tracing-enabled=false")
		}
	}

	// Handle metrics configuration
	if otel.Metrics != nil {
		if otel.Metrics.Enabled {
			args = append(args, "--otel-metrics-enabled=true")
		} else {
			args = append(args, "--otel-metrics-enabled=false")
		}
	}

	return args
}

// generateOpenTelemetryEnvVars generates OpenTelemetry environment variables for the proxy container
func (*MCPServerReconciler) generateOpenTelemetryEnvVars(m *mcpv1alpha1.MCPServer) []corev1.EnvVar {
	var envVars []corev1.EnvVar

	if m.Spec.Telemetry == nil || m.Spec.Telemetry.OpenTelemetry == nil {
		return envVars
	}

	otel := m.Spec.Telemetry.OpenTelemetry

	// Add service name
	serviceName := otel.ServiceName
	if serviceName == "" {
		serviceName = m.Name // Default to MCPServer name if not specified
	}

	// Enable resource detection
	envVars = append(envVars, corev1.EnvVar{
		Name:  "OTEL_RESOURCE_ATTRIBUTES",
		Value: fmt.Sprintf("service.name=%s,service.namespace=%s", serviceName, m.Namespace),
	})

	return envVars
}

// ensureAuthzConfigMap ensures the authorization ConfigMap exists for inline configuration
func (r *MCPServerReconciler) ensureAuthzConfigMap(ctx context.Context, m *mcpv1alpha1.MCPServer) error {
	ctxLogger := log.FromContext(ctx)
	// Only create ConfigMap for inline authorization configuration
	if m.Spec.AuthzConfig == nil || m.Spec.AuthzConfig.Type != mcpv1alpha1.AuthzConfigTypeInline ||
		m.Spec.AuthzConfig.Inline == nil {
		return nil
	}

	configMapName := fmt.Sprintf("%s-authz-inline", m.Name)

	// Create authorization configuration data
	authzConfigData := map[string]interface{}{
		"version": "1.0",
		"type":    "cedarv1",
		"cedar": map[string]interface{}{
			"policies": m.Spec.AuthzConfig.Inline.Policies,
			"entities_json": func() string {
				if m.Spec.AuthzConfig.Inline.EntitiesJSON != "" {
					return m.Spec.AuthzConfig.Inline.EntitiesJSON
				}
				return "[]"
			}(),
		},
	}

	// Marshal to JSON
	authzConfigJSON, err := json.Marshal(authzConfigData)
	if err != nil {
		return fmt.Errorf("failed to marshal inline authz config: %w", err)
	}

	// Define the ConfigMap
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: m.Namespace,
			Labels:    labelsForInlineAuthzConfig(m.Name),
		},
		Data: map[string]string{
			defaultAuthzKey: string(authzConfigJSON),
		},
	}

	// Set the MCPServer as the owner of the ConfigMap
	if err := controllerutil.SetControllerReference(m, configMap, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference for authorization ConfigMap: %w", err)
	}

	// Check if the ConfigMap already exists
	existingConfigMap := &corev1.ConfigMap{}
	err = r.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: m.Namespace}, existingConfigMap)
	if err != nil && errors.IsNotFound(err) {
		// Create the ConfigMap
		ctxLogger.Info("Creating authorization ConfigMap", "ConfigMap.Namespace", configMap.Namespace, "ConfigMap.Name", configMap.Name)
		if err := r.Create(ctx, configMap); err != nil {
			return fmt.Errorf("failed to create authorization ConfigMap: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("failed to get authorization ConfigMap: %w", err)
	} else {
		// ConfigMap exists, check if it needs to be updated
		if !reflect.DeepEqual(existingConfigMap.Data, configMap.Data) {
			ctxLogger.Info("Updating authorization ConfigMap",
				"ConfigMap.Namespace", configMap.Namespace, "ConfigMap.Name", configMap.Name)
			existingConfigMap.Data = configMap.Data
			if err := r.Update(ctx, existingConfigMap); err != nil {
				return fmt.Errorf("failed to update authorization ConfigMap: %w", err)
			}
		}
	}

	return nil
}

// int32Ptr returns a pointer to an int32
func int32Ptr(i int32) *int32 {
	return &i
}

// SetupWithManager sets up the controller with the Manager.
func (r *MCPServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.MCPServer{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Complete(r)
}

// equalToolsFilter returns true when the desired toolsFilter slice and the
// currently-applied `--tools=` argument in the container args represent the
// same unordered set of tools.
func equalToolsFilter(spec []string, args []string) bool {
	// Build canonical form for spec
	specCanon := canonicalToolsList(spec)

	// Extract current tools argument (if any) from args
	var currentArg string
	for _, a := range args {
		if strings.HasPrefix(a, "--tools=") {
			currentArg = strings.TrimPrefix(a, "--tools=")
			break
		}
	}

	if specCanon == "" && currentArg == "" {
		return true // both unset/empty
	}

	// Canonicalise current list
	currentCanon := canonicalToolsList(strings.Split(strings.TrimSpace(currentArg), ","))
	return specCanon == currentCanon
}

// canonicalToolsList sorts a slice and joins it with commas; empty slice yields "".
func canonicalToolsList(list []string) string {
	if len(list) == 0 || (len(list) == 1 && list[0] == "") {
		return ""
	}
	cp := slices.Clone(list)
	slices.Sort(cp)
	return strings.Join(cp, ",")
}

// equalOpenTelemetryArgs checks if OpenTelemetry command-line arguments have changed
func equalOpenTelemetryArgs(spec *mcpv1alpha1.OpenTelemetryConfig, args []string) bool {
	if spec == nil {
		return !hasOtelArgs(args)
	}

	return equalServiceName(spec, args) &&
		equalHeaders(spec, args) &&
		equalInsecureFlag(spec, args)
}

func hasOtelArgs(args []string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, "--otel-") {
			return true
		}
	}
	return false
}

func equalServiceName(spec *mcpv1alpha1.OpenTelemetryConfig, args []string) bool {
	expectedArg := ""
	if spec.ServiceName != "" {
		expectedArg = fmt.Sprintf("--otel-service-name=%s", spec.ServiceName)
	}

	foundArg := ""
	for _, arg := range args {
		if strings.HasPrefix(arg, "--otel-service-name=") {
			foundArg = arg
			break
		}
	}

	return expectedArg == foundArg
}

func equalHeaders(spec *mcpv1alpha1.OpenTelemetryConfig, args []string) bool {
	expectedCount := len(spec.Headers)
	currentCount := countHeaderArgs(args)

	if expectedCount != currentCount {
		return false
	}

	return allHeadersPresent(spec.Headers, args)
}

func countHeaderArgs(args []string) int {
	count := 0
	for _, arg := range args {
		if strings.HasPrefix(arg, "--otel-headers=") {
			count++
		}
	}
	return count
}

func allHeadersPresent(expectedHeaders []string, args []string) bool {
	for _, expectedHeader := range expectedHeaders {
		expectedArg := fmt.Sprintf("--otel-headers=%s", expectedHeader)
		if !contains(args, expectedArg) {
			return false
		}
	}
	return true
}

func equalInsecureFlag(spec *mcpv1alpha1.OpenTelemetryConfig, args []string) bool {
	return spec.Insecure == contains(args, "--otel-insecure")
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

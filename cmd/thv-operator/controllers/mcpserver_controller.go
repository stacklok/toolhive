// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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
	"strings"
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
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/validation"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
	"github.com/stacklok/toolhive/pkg/transport"
)

// MCPServerReconciler reconciles a MCPServer object
type MCPServerReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Recorder         record.EventRecorder
	PlatformDetector *ctrlutil.SharedPlatformDetector
	ImageValidation  validation.ImageValidation
}

// defaultRBACRules are the default RBAC rules that the
// ToolHive ProxyRunner and/or MCP server needs to have in order to run.
// These permissions are needed for MCPServer which deploys and manages MCP server containers.
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

// remoteProxyRBACRules defines minimal RBAC permissions for MCPRemoteProxy.
// Remote proxies only connect to external MCP servers and do not deploy containers,
// so they only need read access to ConfigMaps and Secrets (for OIDC/token exchange).
var remoteProxyRBACRules = []rbacv1.PolicyRule{
	{
		APIGroups: []string{""},
		Resources: []string{"configmaps"},
		Verbs:     []string{"get", "list", "watch"},
	},
	{
		APIGroups: []string{""},
		Resources: []string{"secrets"},
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
// It uses the shared platform detector to ensure detection is only performed once and cached
func (r *MCPServerReconciler) detectPlatform(ctx context.Context) (kubernetes.Platform, error) {
	return r.PlatformDetector.DetectPlatform(ctx)
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

	// Check if the GroupRef is valid if specified
	r.validateGroupRef(ctx, mcpServer)

	// Validate CABundleRef if specified
	r.validateCABundleRef(ctx, mcpServer)

	// Validate PodTemplateSpec early - before other validations
	// This ensures we fail fast if the spec is invalid
	if !r.validateAndUpdatePodTemplateStatus(ctx, mcpServer) {
		// Invalid PodTemplateSpec - return without error to avoid infinite retries
		// The user must fix the spec and the next reconciliation will retry
		return ctrl.Result{}, nil
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

	// Check if MCPExternalAuthConfig is referenced and handle it
	if err := r.handleExternalAuthConfig(ctx, mcpServer); err != nil {
		ctxLogger.Error(err, "Failed to handle MCPExternalAuthConfig")
		// Update status to reflect the error
		mcpServer.Status.Phase = mcpv1alpha1.MCPServerPhaseFailed
		if statusErr := r.Status().Update(ctx, mcpServer); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPServer status after MCPExternalAuthConfig error")
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
		// Update status to persist the condition
		if statusErr := r.Status().Update(ctx, mcpServer); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPServer status after image validation")
		}
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
		// Update status to persist the condition
		if statusErr := r.Status().Update(ctx, mcpServer); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPServer status after image validation")
		}
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

	// Fetch RunConfig ConfigMap checksum to include in pod template annotations
	runConfigChecksum, err := r.getRunConfigChecksum(ctx, mcpServer)
	if err != nil {
		if errors.IsNotFound(err) {
			// ConfigMap doesn't exist yet - requeue with a short delay to allow
			// API server propagation.
			ctxLogger.Info("RunConfig ConfigMap not found yet, will retry",
				"server", mcpServer.Name, "namespace", mcpServer.Namespace)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		ctxLogger.Error(err, "Failed to get RunConfig checksum")
		return ctrl.Result{}, err
	}

	// Check if the deployment already exists, if not create a new one
	deployment := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: mcpServer.Name, Namespace: mcpServer.Namespace}, deployment)
	if err != nil && errors.IsNotFound(err) {
		// Define a new deployment
		dep := r.deploymentForMCPServer(ctx, mcpServer, runConfigChecksum)
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
	serviceName := ctrlutil.CreateProxyServiceName(mcpServer.Name)
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

	// Update the MCPServer status with the service URL including transport-specific path
	if mcpServer.Status.URL == "" {
		host := fmt.Sprintf("%s.%s.svc.cluster.local", serviceName, mcpServer.Namespace)
		mcpServer.Status.URL = transport.GenerateMCPServerURL(
			mcpServer.Spec.Transport,
			mcpServer.Spec.ProxyMode,
			host,
			int(mcpServer.GetProxyPort()),
			mcpServer.Name,
			"", // empty remoteURL for MCPServer (not remote proxy)
		)
		err = r.Status().Update(ctx, mcpServer)
		if err != nil {
			ctxLogger.Error(err, "Failed to update MCPServer status")
			return ctrl.Result{}, err
		}
	}

	// Check if the deployment spec changed
	if r.deploymentNeedsUpdate(ctx, deployment, mcpServer, runConfigChecksum) {
		// Update the deployment
		newDeployment := r.deploymentForMCPServer(ctx, mcpServer, runConfigChecksum)
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

func (r *MCPServerReconciler) validateGroupRef(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) {
	if mcpServer.Spec.GroupRef == "" {
		// No group reference, nothing to validate
		return
	}

	ctxLogger := log.FromContext(ctx)

	// Find the referenced MCPGroup
	group := &mcpv1alpha1.MCPGroup{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: mcpServer.Namespace, Name: mcpServer.Spec.GroupRef}, group); err != nil {
		ctxLogger.Error(err, "Failed to validate GroupRef")
		meta.SetStatusCondition(&mcpServer.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionGroupRefValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1alpha1.ConditionReasonGroupRefNotFound,
			Message:            fmt.Sprintf("MCPGroup '%s' not found in namespace '%s'", mcpServer.Spec.GroupRef, mcpServer.Namespace),
			ObservedGeneration: mcpServer.Generation,
		})
	} else if group.Status.Phase != mcpv1alpha1.MCPGroupPhaseReady {
		meta.SetStatusCondition(&mcpServer.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionGroupRefValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1alpha1.ConditionReasonGroupRefNotReady,
			Message:            fmt.Sprintf("MCPGroup '%s' is not ready (current phase: %s)", mcpServer.Spec.GroupRef, group.Status.Phase),
			ObservedGeneration: mcpServer.Generation,
		})
	} else {
		meta.SetStatusCondition(&mcpServer.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionGroupRefValidated,
			Status:             metav1.ConditionTrue,
			Reason:             mcpv1alpha1.ConditionReasonGroupRefValidated,
			Message:            fmt.Sprintf("MCPGroup '%s' is valid and ready", mcpServer.Spec.GroupRef),
			ObservedGeneration: mcpServer.Generation,
		})
	}

	if err := r.Status().Update(ctx, mcpServer); err != nil {
		ctxLogger.Error(err, "Failed to update MCPServer status after GroupRef validation")
	}

}

// getCABundleRef extracts the CABundleRef from the OIDC configuration based on type
func getCABundleRef(oidcConfig *mcpv1alpha1.OIDCConfigRef) *mcpv1alpha1.CABundleSource {
	if oidcConfig == nil {
		return nil
	}
	switch oidcConfig.Type {
	case mcpv1alpha1.OIDCConfigTypeInline:
		if oidcConfig.Inline != nil {
			return oidcConfig.Inline.CABundleRef
		}
	case mcpv1alpha1.OIDCConfigTypeConfigMap:
		if oidcConfig.ConfigMap != nil {
			return oidcConfig.ConfigMap.CABundleRef
		}
	}
	return nil
}

// setCABundleRefCondition sets the CA bundle validation status condition
func setCABundleRefCondition(mcpServer *mcpv1alpha1.MCPServer, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&mcpServer.Status.Conditions, metav1.Condition{
		Type:               mcpv1alpha1.ConditionCABundleRefValidated,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: mcpServer.Generation,
	})
}

// validateCABundleRef validates the CABundleRef ConfigMap reference if specified
func (r *MCPServerReconciler) validateCABundleRef(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) {
	caBundleRef := getCABundleRef(mcpServer.Spec.OIDCConfig)
	if caBundleRef == nil || caBundleRef.ConfigMapRef == nil {
		return
	}

	ctxLogger := log.FromContext(ctx)

	// Validate the CABundleRef configuration
	if err := validation.ValidateCABundleSource(caBundleRef); err != nil {
		ctxLogger.Error(err, "Invalid CABundleRef configuration")
		setCABundleRefCondition(mcpServer, metav1.ConditionFalse, mcpv1alpha1.ConditionReasonCABundleRefInvalid, err.Error())
		r.updateCABundleStatus(ctx, mcpServer)
		return
	}

	// Check if the referenced ConfigMap exists
	cmName := caBundleRef.ConfigMapRef.Name
	configMap := &corev1.ConfigMap{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: mcpServer.Namespace, Name: cmName}, configMap); err != nil {
		ctxLogger.Error(err, "Failed to find CA bundle ConfigMap", "configMap", cmName)
		setCABundleRefCondition(mcpServer, metav1.ConditionFalse, mcpv1alpha1.ConditionReasonCABundleRefNotFound,
			fmt.Sprintf("CA bundle ConfigMap '%s' not found in namespace '%s'", cmName, mcpServer.Namespace))
		r.updateCABundleStatus(ctx, mcpServer)
		return
	}

	// Verify the key exists in the ConfigMap
	key := caBundleRef.ConfigMapRef.Key
	if key == "" {
		key = validation.OIDCCABundleDefaultKey
	}
	if _, exists := configMap.Data[key]; !exists {
		ctxLogger.Error(nil, "CA bundle key not found in ConfigMap", "configMap", cmName, "key", key)
		setCABundleRefCondition(mcpServer, metav1.ConditionFalse, mcpv1alpha1.ConditionReasonCABundleRefInvalid,
			fmt.Sprintf("Key '%s' not found in ConfigMap '%s'", key, cmName))
		r.updateCABundleStatus(ctx, mcpServer)
		return
	}

	// Validation passed
	setCABundleRefCondition(mcpServer, metav1.ConditionTrue, mcpv1alpha1.ConditionReasonCABundleRefValid,
		fmt.Sprintf("CA bundle ConfigMap '%s' is valid (key: %s)", cmName, key))
	r.updateCABundleStatus(ctx, mcpServer)
}

// updateCABundleStatus updates the MCPServer status after CA bundle validation
func (r *MCPServerReconciler) updateCABundleStatus(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) {
	ctxLogger := log.FromContext(ctx)
	if err := r.Status().Update(ctx, mcpServer); err != nil {
		ctxLogger.Error(err, "Failed to update MCPServer status after CABundleRef validation")
	}
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

// validateAndUpdatePodTemplateStatus validates the PodTemplateSpec and updates the MCPServer status
// with appropriate conditions and events
func (r *MCPServerReconciler) validateAndUpdatePodTemplateStatus(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) bool {
	ctxLogger := log.FromContext(ctx)

	// Only validate if PodTemplateSpec is provided
	if mcpServer.Spec.PodTemplateSpec == nil || mcpServer.Spec.PodTemplateSpec.Raw == nil {
		// No PodTemplateSpec provided, validation passes
		return true
	}

	_, err := ctrlutil.NewPodTemplateSpecBuilder(mcpServer.Spec.PodTemplateSpec, mcpContainerName)
	if err != nil {
		// Record event for invalid PodTemplateSpec
		if r.Recorder != nil {
			r.Recorder.Eventf(mcpServer, corev1.EventTypeWarning, "InvalidPodTemplateSpec",
				"Failed to parse PodTemplateSpec: %v. Deployment blocked until PodTemplateSpec is fixed.", err)
		}

		// Set phase and message
		mcpServer.Status.Phase = mcpv1alpha1.MCPServerPhaseFailed
		mcpServer.Status.Message = fmt.Sprintf("Invalid PodTemplateSpec: %v", err)

		// Set condition for invalid PodTemplateSpec
		meta.SetStatusCondition(&mcpServer.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionPodTemplateValid,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: mcpServer.Generation,
			Reason:             mcpv1alpha1.ConditionReasonPodTemplateInvalid,
			Message:            fmt.Sprintf("Failed to parse PodTemplateSpec: %v. Deployment blocked until fixed.", err),
		})

		// Update status with the condition
		if statusErr := r.Status().Update(ctx, mcpServer); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPServer status with PodTemplateSpec validation")
			return false
		}

		ctxLogger.Error(err, "PodTemplateSpec validation failed")
		return false
	}

	// Set condition for valid PodTemplateSpec
	meta.SetStatusCondition(&mcpServer.Status.Conditions, metav1.Condition{
		Type:               mcpv1alpha1.ConditionPodTemplateValid,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: mcpServer.Generation,
		Reason:             mcpv1alpha1.ConditionReasonPodTemplateValid,
		Message:            "PodTemplateSpec is valid",
	})

	// Update status with the condition
	if statusErr := r.Status().Update(ctx, mcpServer); statusErr != nil {
		ctxLogger.Error(statusErr, "Failed to update MCPServer status with PodTemplateSpec validation")
	}

	return true
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

// getRunConfigChecksum fetches the RunConfig ConfigMap checksum annotation for this server.
// Uses the shared RunConfigChecksumFetcher to maintain consistency with MCPRemoteProxy.
func (r *MCPServerReconciler) getRunConfigChecksum(
	ctx context.Context, mcpServer *mcpv1alpha1.MCPServer,
) (string, error) {
	if mcpServer == nil {
		return "", fmt.Errorf("mcpServer cannot be nil")
	}

	fetcher := checksum.NewRunConfigChecksumFetcher(r.Client)
	return fetcher.GetRunConfigChecksum(ctx, mcpServer.Namespace, mcpServer.Name)
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
	toolConfig, err := ctrlutil.GetToolConfigForMCPServer(ctx, r.Client, m)
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
	proxyRunnerNameForRBAC := ctrlutil.ProxyRunnerServiceAccountName(mcpServer.Name)

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
func (r *MCPServerReconciler) deploymentForMCPServer(
	ctx context.Context, m *mcpv1alpha1.MCPServer, runConfigChecksum string,
) *appsv1.Deployment {
	ls := labelsForMCPServer(m.Name)
	replicas := int32(1)

	// Prepare container args
	args := []string{"run"}

	// Prepare container volume mounts
	volumeMounts := []corev1.VolumeMount{}
	volumes := []corev1.Volume{}

	// Using ConfigMap mode for all configuration
	// Pod template patch for secrets and service account
	builder, err := ctrlutil.NewPodTemplateSpecBuilder(m.Spec.PodTemplateSpec, mcpContainerName)
	if err != nil {
		// NOTE: This should be unreachable - early validation in Reconcile() blocks invalid specs
		// This is defense-in-depth: if somehow reached, log and continue without pod customizations
		ctxLogger := log.FromContext(ctx)
		ctxLogger.Error(err, "UNEXPECTED: Invalid PodTemplateSpec passed early validation")
	} else {
		// If service account is not specified, use the default MCP server service account
		serviceAccount := m.Spec.ServiceAccount
		if serviceAccount == nil {
			defaultSA := mcpServerServiceAccountName(m.Name)
			serviceAccount = &defaultSA
		}
		finalPodTemplateSpec := builder.
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

	// Pod template patch, permission profile, OIDC, authorization, audit, environment variables,
	// tools filter, and telemetry configuration are all included in the ConfigMap
	// so we don't need to add them as individual flags

	// Always add the image as it's required by proxy runner command signature
	// When using ConfigMap, the image from ConfigMap takes precedence, but we still need
	// to provide this as a positional argument to satisfy the command requirements
	args = append(args, m.Spec.Image)

	// Prepare container env vars for the proxy container
	env := []corev1.EnvVar{}

	// Add OpenTelemetry environment variables
	if m.Spec.Telemetry != nil && m.Spec.Telemetry.OpenTelemetry != nil {
		otelEnvVars := ctrlutil.GenerateOpenTelemetryEnvVars(m.Spec.Telemetry, m.Name, m.Namespace)
		env = append(env, otelEnvVars...)
	}

	// Add token exchange environment variables
	if m.Spec.ExternalAuthConfigRef != nil {
		tokenExchangeEnvVars, err := ctrlutil.GenerateTokenExchangeEnvVars(
			ctx, r.Client, m.Namespace, m.Spec.ExternalAuthConfigRef, ctrlutil.GetExternalAuthConfigByName,
		)
		if err != nil {
			ctxLogger := log.FromContext(ctx)
			ctxLogger.Error(err, "Failed to generate token exchange environment variables")
		} else {
			env = append(env, tokenExchangeEnvVars...)
		}
	}

	// Add OIDC client secret environment variable if using inline config with secretRef
	if m.Spec.OIDCConfig != nil && m.Spec.OIDCConfig.Inline != nil {
		oidcClientSecretEnvVar, err := ctrlutil.GenerateOIDCClientSecretEnvVar(
			ctx, r.Client, m.Namespace, m.Spec.OIDCConfig.Inline.ClientSecretRef,
		)
		if err != nil {
			ctxLogger := log.FromContext(ctx)
			ctxLogger.Error(err, "Failed to generate OIDC client secret environment variable")
		} else if oidcClientSecretEnvVar != nil {
			env = append(env, *oidcClientSecretEnvVar)
		}
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
	authzVolumeMount, authzVolume := ctrlutil.GenerateAuthzVolumeConfig(m.Spec.AuthzConfig, m.Name)
	if authzVolumeMount != nil {
		volumeMounts = append(volumeMounts, *authzVolumeMount)
		volumes = append(volumes, *authzVolume)
	}

	// Add OIDC CA bundle volume if configured
	if m.Spec.OIDCConfig != nil {
		caVolumes, caMounts := ctrlutil.AddOIDCCABundleVolumes(m.Spec.OIDCConfig)
		volumes = append(volumes, caVolumes...)
		volumeMounts = append(volumeMounts, caMounts...)
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

	// Add RunConfig checksum annotation to trigger pod rollout when config changes
	deploymentTemplateAnnotations = checksum.AddRunConfigChecksumToPodTemplate(deploymentTemplateAnnotations, runConfigChecksum)

	if m.Spec.ResourceOverrides != nil && m.Spec.ResourceOverrides.ProxyDeployment != nil {
		if m.Spec.ResourceOverrides.ProxyDeployment.Labels != nil {
			deploymentLabels = ctrlutil.MergeLabels(ls, m.Spec.ResourceOverrides.ProxyDeployment.Labels)
		}
		if m.Spec.ResourceOverrides.ProxyDeployment.Annotations != nil {
			deploymentAnnotations = ctrlutil.MergeAnnotations(
				make(map[string]string),
				m.Spec.ResourceOverrides.ProxyDeployment.Annotations,
			)
		}

		if m.Spec.ResourceOverrides.ProxyDeployment.PodTemplateMetadataOverrides != nil {
			if m.Spec.ResourceOverrides.ProxyDeployment.PodTemplateMetadataOverrides.Labels != nil {
				deploymentTemplateLabels = ctrlutil.MergeLabels(ls,
					m.Spec.ResourceOverrides.ProxyDeployment.PodTemplateMetadataOverrides.Labels)
			}
			if m.Spec.ResourceOverrides.ProxyDeployment.PodTemplateMetadataOverrides.Annotations != nil {
				deploymentTemplateAnnotations = ctrlutil.MergeAnnotations(deploymentAnnotations,
					m.Spec.ResourceOverrides.ProxyDeployment.PodTemplateMetadataOverrides.Annotations)
			}
		}
	}

	// Vault Agent Injection is handled via the runconfig.json in ConfigMap mode

	// Detect platform and prepare ProxyRunner's pod and container security context
	detectedPlatform, err := r.detectPlatform(ctx)
	if err != nil {
		ctxLogger := log.FromContext(ctx)
		ctxLogger.Error(err, "Failed to detect platform, defaulting to Kubernetes", "mcpserver", m.Name)
		detectedPlatform = kubernetes.PlatformKubernetes // Default to Kubernetes on error
	}

	// Use SecurityContextBuilder for platform-aware security context
	securityBuilder := kubernetes.NewSecurityContextBuilder(detectedPlatform)
	proxyRunnerPodSecurityContext := securityBuilder.BuildPodSecurityContext()
	proxyRunnerContainerSecurityContext := securityBuilder.BuildContainerSecurityContext()

	env = ctrlutil.EnsureRequiredEnvVars(ctx, env)

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
					ServiceAccountName: ctrlutil.ProxyRunnerServiceAccountName(m.Name),
					Containers: []corev1.Container{{
						Image:            getToolhiveRunnerImage(),
						Name:             "toolhive",
						ImagePullPolicy:  getImagePullPolicyForToolhiveRunner(),
						Args:             args,
						Env:              env,
						VolumeMounts:     volumeMounts,
						Resources:        resources,
						Ports: []corev1.ContainerPort{{
							ContainerPort: m.GetProxyPort(),
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

// serviceForMCPServer returns a MCPServer Service object
func (r *MCPServerReconciler) serviceForMCPServer(ctx context.Context, m *mcpv1alpha1.MCPServer) *corev1.Service {
	ls := labelsForMCPServer(m.Name)

	// we want to generate a service name that is unique for the proxy service
	// to avoid conflicts with the headless service
	svcName := ctrlutil.CreateProxyServiceName(m.Name)

	// Prepare service metadata with overrides
	serviceLabels := ls
	serviceAnnotations := make(map[string]string)

	if m.Spec.ResourceOverrides != nil && m.Spec.ResourceOverrides.ProxyService != nil {
		if m.Spec.ResourceOverrides.ProxyService.Labels != nil {
			serviceLabels = ctrlutil.MergeLabels(ls, m.Spec.ResourceOverrides.ProxyService.Labels)
		}
		if m.Spec.ResourceOverrides.ProxyService.Annotations != nil {
			serviceAnnotations = ctrlutil.MergeAnnotations(make(map[string]string), m.Spec.ResourceOverrides.ProxyService.Annotations)
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
				Port:       m.GetProxyPort(),
				TargetPort: intstr.FromInt(int(m.GetProxyPort())),
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

// checkContainerError checks if a container is in an error state and returns the error reason.
func checkContainerError(containerStatus corev1.ContainerStatus) (bool, string) {
	if containerStatus.State.Waiting != nil {
		reason := containerStatus.State.Waiting.Reason
		// These reasons indicate definitive failures (not transient)
		// Note: ImagePullBackOff and ErrImagePull are treated as pending conditions
		// because they are often transient (network issues, temporary registry unavailability)
		// and Kubernetes will keep retrying
		if reason == "CrashLoopBackOff" || reason == "CreateContainerError" ||
			reason == "InvalidImageName" {
			return true, reason
		}
	}
	if containerStatus.State.Terminated != nil && containerStatus.State.Terminated.ExitCode != 0 {
		return true, "ContainerTerminated"
	}
	return false, ""
}

// areAllContainersReady checks if all containers in the pod are ready.
func areAllContainersReady(containerStatuses []corev1.ContainerStatus) bool {
	if len(containerStatuses) == 0 {
		return false
	}
	for _, containerStatus := range containerStatuses {
		if !containerStatus.Ready {
			return false
		}
	}
	return true
}

// categorizePodStatus categorizes a pod into running, pending, or failed and returns the failure reason.
func categorizePodStatus(pod corev1.Pod) (running, pending, failed int, failureReason string) {
	// Check container statuses for failures (CrashLoopBackOff, CreateContainerError, etc.)
	for _, containerStatus := range pod.Status.ContainerStatuses {
		if hasError, reason := checkContainerError(containerStatus); hasError {
			return 0, 0, 1, reason
		}
	}

	// Check pod phase if containers are not in error state
	switch pod.Status.Phase {
	case corev1.PodRunning:
		if areAllContainersReady(pod.Status.ContainerStatuses) {
			return 1, 0, 0, ""
		}
		return 0, 1, 0, ""
	case corev1.PodPending:
		return 0, 1, 0, ""
	case corev1.PodFailed:
		return 0, 0, 1, "PodFailed"
	case corev1.PodSucceeded:
		return 1, 0, 0, ""
	case corev1.PodUnknown:
		return 0, 1, 0, ""
	}
	return 0, 0, 0, ""
}

// updateMCPServerStatus updates the status of the MCPServer
func (r *MCPServerReconciler) updateMCPServerStatus(ctx context.Context, m *mcpv1alpha1.MCPServer) error {
	// List pods for the MCPServer Deployment only (not proxy pods)
	// The Deployment pods are labeled with "app": "mcpserver"
	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(m.Namespace),
		client.MatchingLabels(labelsForMCPServer(m.Name)),
	}
	if err := r.List(ctx, podList, listOpts...); err != nil {
		return err
	}

	if len(podList.Items) == 0 {
		// No Deployment pods found yet
		m.Status.Phase = mcpv1alpha1.MCPServerPhasePending
		m.Status.Message = "MCP server is being created"
		return r.Status().Update(ctx, m)
	}

	// Check pod and container statuses
	var running, pending, failed int
	var failureReason string

	for _, pod := range podList.Items {
		r, p, f, reason := categorizePodStatus(pod)
		running += r
		pending += p
		failed += f
		if reason != "" && failureReason == "" {
			failureReason = reason
		}
	}

	// Update the status based on pod health
	if running > 0 {
		m.Status.Phase = mcpv1alpha1.MCPServerPhaseRunning
		m.Status.Message = "MCP server is running"
	} else if failed > 0 {
		m.Status.Phase = mcpv1alpha1.MCPServerPhaseFailed
		if failureReason != "" {
			m.Status.Message = fmt.Sprintf("MCP server pod failed: %s", failureReason)
		} else {
			m.Status.Message = "MCP server pod failed"
		}
	} else if pending > 0 {
		m.Status.Phase = mcpv1alpha1.MCPServerPhasePending
		m.Status.Message = "MCP server is starting"
	} else {
		m.Status.Phase = mcpv1alpha1.MCPServerPhasePending
		m.Status.Message = "No healthy pods found"
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
	runConfigChecksum string,
) bool {
	if deployment == nil || mcpServer == nil {
		return true
	}
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

		// Check if the container port has changed
		if len(container.Ports) > 0 && container.Ports[0].ContainerPort != mcpServer.GetProxyPort() {
			return true
		}

		// Check if the proxy environment variables have changed
		expectedProxyEnv := []corev1.EnvVar{}

		// Add OpenTelemetry environment variables first
		if mcpServer.Spec.Telemetry != nil && mcpServer.Spec.Telemetry.OpenTelemetry != nil {
			otelEnvVars := ctrlutil.GenerateOpenTelemetryEnvVars(mcpServer.Spec.Telemetry, mcpServer.Name, mcpServer.Namespace)
			expectedProxyEnv = append(expectedProxyEnv, otelEnvVars...)
		}

		// Add token exchange environment variables
		if mcpServer.Spec.ExternalAuthConfigRef != nil {
			tokenExchangeEnvVars, err := ctrlutil.GenerateTokenExchangeEnvVars(
				ctx, r.Client, mcpServer.Namespace, mcpServer.Spec.ExternalAuthConfigRef, ctrlutil.GetExternalAuthConfigByName,
			)
			if err != nil {
				// If we can't generate env vars, consider the deployment needs update
				// The actual error will be caught during reconciliation
				return true
			}
			expectedProxyEnv = append(expectedProxyEnv, tokenExchangeEnvVars...)
		}

		// Add OIDC client secret environment variable if using inline config with secretRef
		if mcpServer.Spec.OIDCConfig != nil && mcpServer.Spec.OIDCConfig.Inline != nil {
			oidcClientSecretEnvVar, err := ctrlutil.GenerateOIDCClientSecretEnvVar(
				ctx, r.Client, mcpServer.Namespace, mcpServer.Spec.OIDCConfig.Inline.ClientSecretRef,
			)
			if err != nil {
				// If we can't generate env var, consider the deployment needs update
				return true
			}
			if oidcClientSecretEnvVar != nil {
				expectedProxyEnv = append(expectedProxyEnv, *oidcClientSecretEnvVar)
			}
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
		expectedProxyEnv = ctrlutil.EnsureRequiredEnvVars(ctx, expectedProxyEnv)
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

		builder, err := ctrlutil.NewPodTemplateSpecBuilder(mcpServer.Spec.PodTemplateSpec, mcpContainerName)
		if err != nil {
			// If we can't parse the PodTemplateSpec, consider it as needing update
			return true
		}

		expectedPodTemplateSpec := builder.
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
	}

	// Check if the service account name has changed
	// ServiceAccountName: treat empty (not yet set) as equal to the expected default
	expectedServiceAccountName := ctrlutil.ProxyRunnerServiceAccountName(mcpServer.Name)
	currentServiceAccountName := deployment.Spec.Template.Spec.ServiceAccountName
	if currentServiceAccountName != "" && currentServiceAccountName != expectedServiceAccountName {
		return true
	}

	// Check if the deployment metadata (labels/annotations) have changed due to resource overrides
	expectedLabels := labelsForMCPServer(mcpServer.Name)
	expectedAnnotations := make(map[string]string)

	if mcpServer.Spec.ResourceOverrides != nil && mcpServer.Spec.ResourceOverrides.ProxyDeployment != nil {
		if mcpServer.Spec.ResourceOverrides.ProxyDeployment.Labels != nil {
			expectedLabels = ctrlutil.MergeLabels(
				expectedLabels,
				mcpServer.Spec.ResourceOverrides.ProxyDeployment.Labels,
			)
		}
		if mcpServer.Spec.ResourceOverrides.ProxyDeployment.Annotations != nil {
			expectedAnnotations = ctrlutil.MergeAnnotations(
				make(map[string]string),
				mcpServer.Spec.ResourceOverrides.ProxyDeployment.Annotations,
			)
		}
	}

	if !maps.Equal(deployment.Labels, expectedLabels) {
		return true
	}

	if !maps.Equal(deployment.Annotations, expectedAnnotations) {
		return true
	}

	// Check if pod template annotations have changed (including runconfig checksum)
	expectedPodTemplateAnnotations := make(map[string]string)
	expectedPodTemplateAnnotations = checksum.AddRunConfigChecksumToPodTemplate(expectedPodTemplateAnnotations, runConfigChecksum)

	if mcpServer.Spec.ResourceOverrides != nil &&
		mcpServer.Spec.ResourceOverrides.ProxyDeployment != nil &&
		mcpServer.Spec.ResourceOverrides.ProxyDeployment.PodTemplateMetadataOverrides != nil &&
		mcpServer.Spec.ResourceOverrides.ProxyDeployment.PodTemplateMetadataOverrides.Annotations != nil {
		expectedPodTemplateAnnotations = ctrlutil.MergeAnnotations(
			expectedPodTemplateAnnotations,
			mcpServer.Spec.ResourceOverrides.ProxyDeployment.PodTemplateMetadataOverrides.Annotations,
		)
	}

	if !maps.Equal(deployment.Spec.Template.Annotations, expectedPodTemplateAnnotations) {
		return true
	}

	return false
}

// serviceNeedsUpdate checks if the service needs to be updated
func serviceNeedsUpdate(service *corev1.Service, mcpServer *mcpv1alpha1.MCPServer) bool {
	// Check if the service port has changed
	if len(service.Spec.Ports) > 0 && service.Spec.Ports[0].Port != mcpServer.GetProxyPort() {
		return true
	}

	// Check if the service metadata (labels/annotations) have changed due to resource overrides
	expectedLabels := labelsForMCPServer(mcpServer.Name)
	expectedAnnotations := make(map[string]string)

	if mcpServer.Spec.ResourceOverrides != nil && mcpServer.Spec.ResourceOverrides.ProxyService != nil {
		if mcpServer.Spec.ResourceOverrides.ProxyService.Labels != nil {
			expectedLabels = ctrlutil.MergeLabels(expectedLabels, mcpServer.Spec.ResourceOverrides.ProxyService.Labels)
		}
		if mcpServer.Spec.ResourceOverrides.ProxyService.Annotations != nil {
			expectedAnnotations = ctrlutil.MergeAnnotations(
				make(map[string]string),
				mcpServer.Spec.ResourceOverrides.ProxyService.Annotations,
			)
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

// getImagePullPolicyForToolhiveRunner returns the appropriate imagePullPolicy for the toolhive runner container.
// If the image is a local image (starts with "kind.local/" or "localhost/"), use Never.
// Otherwise, use IfNotPresent to allow pulling when needed but avoid unnecessary pulls.
func getImagePullPolicyForToolhiveRunner() corev1.PullPolicy {
	image := getToolhiveRunnerImage()
	// Check if it's a local image that should use Never
	if strings.HasPrefix(image, "kind.local/") || strings.HasPrefix(image, "localhost/") {
		return corev1.PullNever
	}
	// For other images, use IfNotPresent to allow pulling when needed
	return corev1.PullIfNotPresent
}

// handleExternalAuthConfig validates and tracks the hash of the referenced MCPExternalAuthConfig.
// It updates the MCPServer status when the external auth configuration changes.
func (r *MCPServerReconciler) handleExternalAuthConfig(ctx context.Context, m *mcpv1alpha1.MCPServer) error {
	ctxLogger := log.FromContext(ctx)
	if m.Spec.ExternalAuthConfigRef == nil {
		// No MCPExternalAuthConfig referenced, clear any stored hash
		if m.Status.ExternalAuthConfigHash != "" {
			m.Status.ExternalAuthConfigHash = ""
			if err := r.Status().Update(ctx, m); err != nil {
				return fmt.Errorf("failed to clear MCPExternalAuthConfig hash from status: %w", err)
			}
		}
		return nil
	}

	// Get the referenced MCPExternalAuthConfig
	externalAuthConfig, err := GetExternalAuthConfigForMCPServer(ctx, r.Client, m)
	if err != nil {
		return err
	}

	if externalAuthConfig == nil {
		return fmt.Errorf("MCPExternalAuthConfig %s not found", m.Spec.ExternalAuthConfigRef.Name)
	}

	// Check if the MCPExternalAuthConfig hash has changed
	if m.Status.ExternalAuthConfigHash != externalAuthConfig.Status.ConfigHash {
		ctxLogger.Info("MCPExternalAuthConfig has changed, updating MCPServer",
			"mcpserver", m.Name,
			"externalAuthConfig", externalAuthConfig.Name,
			"oldHash", m.Status.ExternalAuthConfigHash,
			"newHash", externalAuthConfig.Status.ConfigHash)

		// Update the stored hash
		m.Status.ExternalAuthConfigHash = externalAuthConfig.Status.ConfigHash
		if err := r.Status().Update(ctx, m); err != nil {
			return fmt.Errorf("failed to update MCPExternalAuthConfig hash in status: %w", err)
		}

		// The change in hash will trigger a reconciliation of the RunConfig
		// which will pick up the new external auth configuration
	}

	return nil
}

// ensureAuthzConfigMap ensures the authorization ConfigMap exists for inline configuration
func (r *MCPServerReconciler) ensureAuthzConfigMap(ctx context.Context, m *mcpv1alpha1.MCPServer) error {
	return ctrlutil.EnsureAuthzConfigMap(
		ctx, r.Client, r.Scheme, m, m.Namespace, m.Name, m.Spec.AuthzConfig, labelsForInlineAuthzConfig(m.Name),
	)
}

// int32Ptr returns a pointer to an int32
func int32Ptr(i int32) *int32 {
	return &i
}

// SetupWithManager sets up the controller with the Manager.
func (r *MCPServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Create a handler that maps MCPExternalAuthConfig changes to MCPServer reconciliation requests
	externalAuthConfigHandler := handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []reconcile.Request {
			externalAuthConfig, ok := obj.(*mcpv1alpha1.MCPExternalAuthConfig)
			if !ok {
				return nil
			}

			// List all MCPServers in the same namespace
			mcpServerList := &mcpv1alpha1.MCPServerList{}
			if err := r.List(ctx, mcpServerList, client.InNamespace(externalAuthConfig.Namespace)); err != nil {
				log.FromContext(ctx).Error(err, "Failed to list MCPServers for MCPExternalAuthConfig watch")
				return nil
			}

			// Find MCPServers that reference this MCPExternalAuthConfig
			var requests []reconcile.Request
			for _, server := range mcpServerList.Items {
				if server.Spec.ExternalAuthConfigRef != nil &&
					server.Spec.ExternalAuthConfigRef.Name == externalAuthConfig.Name {
					requests = append(requests, reconcile.Request{
						NamespacedName: types.NamespacedName{
							Name:      server.Name,
							Namespace: server.Namespace,
						},
					})
				}
			}

			return requests
		},
	)

	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.MCPServer{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Watches(&mcpv1alpha1.MCPExternalAuthConfig{}, externalAuthConfigHandler).
		Complete(r)
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package controllers contains the reconciliation logic for the MCPServer custom resource.
// It handles the creation, update, and deletion of MCP servers in Kubernetes.
package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	equality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/kubernetes/rbac"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/validation"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
	"github.com/stacklok/toolhive/pkg/transport"
)

// MCPServerReconciler reconciles a MCPServer object
type MCPServerReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Recorder         events.EventRecorder
	PlatformDetector *ctrlutil.SharedPlatformDetector
}

// defaultRBACRules are the default RBAC rules that the
// ToolHive ProxyRunner and/or MCP server needs to have in order to run.
// These permissions are needed for MCPServer which deploys and manages MCP server containers.
var defaultRBACRules = []rbacv1.PolicyRule{
	{
		APIGroups: []string{"apps"},
		Resources: []string{"statefulsets"},
		Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
	},
	{
		APIGroups: []string{""},
		Resources: []string{"services"},
		Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
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

// MCPServerFinalizerName is the name of the finalizer for MCPServer
const MCPServerFinalizerName = "mcpserver.toolhive.stacklok.dev/finalizer"

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

const defaultTerminationGracePeriodSeconds = int64(30)

const stdioTransport = "stdio"

// detectPlatform detects the Kubernetes platform type (Kubernetes vs OpenShift)
// It uses the shared platform detector to ensure detection is only performed once and cached
func (r *MCPServerReconciler) detectPlatform(ctx context.Context) (kubernetes.Platform, error) {
	return r.PlatformDetector.DetectPlatform(ctx)
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpservers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpservers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpservers/finalizers,verbs=update
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcptoolconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpoidcconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpoidcconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcptelemetryconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups="",resources=services,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=roles,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=rolebindings,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods/attach,verbs=create;get
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
//nolint:gocyclo
func (r *MCPServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	// Fetch the MCPServer instance
	mcpServer := &mcpv1beta1.MCPServer{}
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

	// Check if the MCPServer instance is marked to be deleted — do this before
	// any validation or external API calls to avoid unnecessary work during deletion
	if mcpServer.GetDeletionTimestamp() != nil {
		if controllerutil.ContainsFinalizer(mcpServer, MCPServerFinalizerName) {
			if err := r.finalizeMCPServer(ctx, mcpServer); err != nil {
				return ctrl.Result{}, err
			}

			if err := ctrlutil.MutateAndPatchSpec(ctx, r.Client, mcpServer, func(m *mcpv1beta1.MCPServer) {
				controllerutil.RemoveFinalizer(m, MCPServerFinalizerName)
			}); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer for this CR
	if !controllerutil.ContainsFinalizer(mcpServer, MCPServerFinalizerName) {
		if err := ctrlutil.MutateAndPatchSpec(ctx, r.Client, mcpServer, func(m *mcpv1beta1.MCPServer) {
			controllerutil.AddFinalizer(m, MCPServerFinalizerName)
		}); err != nil {
			return ctrl.Result{}, err
		}
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

	// Validate stdio replica cap, session storage, and rate limit config
	r.validateStdioReplicaCap(ctx, mcpServer)
	r.validateSessionStorageForReplicas(ctx, mcpServer)
	r.validateRateLimitConfig(ctx, mcpServer)

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
		mcpServer.Status.Phase = mcpv1beta1.MCPServerPhaseFailed
		setReadyCondition(mcpServer, metav1.ConditionFalse, mcpv1beta1.ConditionReasonNotReady, err.Error())
		if statusErr := r.Status().Update(ctx, mcpServer); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPServer status after MCPToolConfig error")
		}
		return ctrl.Result{}, err
	}

	// Check if MCPTelemetryConfig is referenced and handle it
	if err := r.handleTelemetryConfig(ctx, mcpServer); err != nil {
		ctxLogger.Error(err, "Failed to handle MCPTelemetryConfig")
		mcpServer.Status.Phase = mcpv1beta1.MCPServerPhaseFailed
		setReadyCondition(mcpServer, metav1.ConditionFalse, mcpv1beta1.ConditionReasonNotReady, err.Error())
		if statusErr := r.Status().Update(ctx, mcpServer); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPServer status after MCPTelemetryConfig error")
		}
		return ctrl.Result{}, err
	}

	// Check if MCPExternalAuthConfig is referenced and handle it
	if err := r.handleExternalAuthConfig(ctx, mcpServer); err != nil {
		ctxLogger.Error(err, "Failed to handle MCPExternalAuthConfig")
		// Update status to reflect the error
		mcpServer.Status.Phase = mcpv1beta1.MCPServerPhaseFailed
		setReadyCondition(mcpServer, metav1.ConditionFalse, mcpv1beta1.ConditionReasonNotReady, err.Error())
		if statusErr := r.Status().Update(ctx, mcpServer); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPServer status after MCPExternalAuthConfig error")
		}
		return ctrl.Result{}, err
	}

	// Check if authServerRef is referenced and handle config hash tracking
	if err := r.handleAuthServerRef(ctx, mcpServer); err != nil {
		ctxLogger.Error(err, "Failed to handle authServerRef")
		mcpServer.Status.Phase = mcpv1beta1.MCPServerPhaseFailed
		setReadyCondition(mcpServer, metav1.ConditionFalse, mcpv1beta1.ConditionReasonNotReady, err.Error())
		if statusErr := r.Status().Update(ctx, mcpServer); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPServer status after authServerRef error")
		}
		return ctrl.Result{}, err
	}

	// Check if MCPOIDCConfig is referenced and handle it
	if err := r.handleOIDCConfig(ctx, mcpServer); err != nil {
		ctxLogger.Error(err, "Failed to handle MCPOIDCConfig")
		mcpServer.Status.Phase = mcpv1beta1.MCPServerPhaseFailed
		setReadyCondition(mcpServer, metav1.ConditionFalse, mcpv1beta1.ConditionReasonNotReady, err.Error())
		if statusErr := r.Status().Update(ctx, mcpServer); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPServer status after MCPOIDCConfig error")
		}
		return ctrl.Result{}, err
	}

	// Update the MCPServer status with the pod status
	if err := r.updateMCPServerStatus(ctx, mcpServer); err != nil {
		ctxLogger.Error(err, "Failed to update MCPServer status")
		return ctrl.Result{}, err
	}

	// check if the RBAC resources are in place for the MCP server
	if err := r.ensureRBACResources(ctx, mcpServer); err != nil {
		ctxLogger.Error(err, "Failed to ensure RBAC resources")
		mcpServer.Status.Phase = mcpv1beta1.MCPServerPhaseFailed
		mcpServer.Status.Message = fmt.Sprintf("Failed to ensure RBAC resources: %s", err.Error())
		setReadyCondition(mcpServer, metav1.ConditionFalse, mcpv1beta1.ConditionReasonNotReady, mcpServer.Status.Message)
		if statusErr := r.Status().Update(ctx, mcpServer); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPServer status after RBAC error")
		}
		return ctrl.Result{}, err
	}

	// Ensure authorization ConfigMap for inline configuration
	if err := r.ensureAuthzConfigMap(ctx, mcpServer); err != nil {
		ctxLogger.Error(err, "Failed to ensure authorization ConfigMap")
		mcpServer.Status.Phase = mcpv1beta1.MCPServerPhaseFailed
		mcpServer.Status.Message = fmt.Sprintf("Failed to ensure authorization ConfigMap: %s", err.Error())
		setReadyCondition(mcpServer, metav1.ConditionFalse, mcpv1beta1.ConditionReasonNotReady, mcpServer.Status.Message)
		if statusErr := r.Status().Update(ctx, mcpServer); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPServer status after authz ConfigMap error")
		}
		return ctrl.Result{}, err
	}

	// Ensure RunConfig ConfigMap exists and is up to date
	if err := r.ensureRunConfigConfigMap(ctx, mcpServer); err != nil {
		ctxLogger.Error(err, "Failed to ensure RunConfig ConfigMap")
		mcpServer.Status.Phase = mcpv1beta1.MCPServerPhaseFailed
		mcpServer.Status.Message = fmt.Sprintf("Failed to build configuration: %s", err.Error())
		setReadyCondition(mcpServer, metav1.ConditionFalse, mcpv1beta1.ConditionReasonNotReady, mcpServer.Status.Message)
		if statusErr := r.Status().Update(ctx, mcpServer); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPServer status after RunConfig error")
		}
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
		mcpServer.Status.Phase = mcpv1beta1.MCPServerPhaseFailed
		mcpServer.Status.Message = fmt.Sprintf("Failed to build configuration: %s", err.Error())
		setReadyCondition(mcpServer, metav1.ConditionFalse, mcpv1beta1.ConditionReasonNotReady, mcpServer.Status.Message)
		if statusErr := r.Status().Update(ctx, mcpServer); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPServer status after RunConfig checksum error")
		}
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
			deploymentErr := fmt.Errorf("failed to create Deployment object")
			mcpServer.Status.Phase = mcpv1beta1.MCPServerPhaseFailed
			mcpServer.Status.Message = deploymentErr.Error()
			setReadyCondition(mcpServer, metav1.ConditionFalse, mcpv1beta1.ConditionReasonNotReady, mcpServer.Status.Message)
			if statusErr := r.Status().Update(ctx, mcpServer); statusErr != nil {
				ctxLogger.Error(statusErr, "Failed to update MCPServer status after Deployment build failure")
			}
			return ctrl.Result{}, deploymentErr
		}
		ctxLogger.Info("Creating a new Deployment", "Deployment.Namespace", dep.Namespace, "Deployment.Name", dep.Name)
		err = r.Create(ctx, dep)
		if err != nil {
			ctxLogger.Error(err, "Failed to create new Deployment", "Deployment.Namespace", dep.Namespace, "Deployment.Name", dep.Name)
			mcpServer.Status.Phase = mcpv1beta1.MCPServerPhaseFailed
			mcpServer.Status.Message = fmt.Sprintf("Failed to create Deployment: %s", err.Error())
			setReadyCondition(mcpServer, metav1.ConditionFalse, mcpv1beta1.ConditionReasonNotReady, mcpServer.Status.Message)
			if statusErr := r.Status().Update(ctx, mcpServer); statusErr != nil {
				ctxLogger.Error(statusErr, "Failed to update MCPServer status after Deployment creation failure")
			}
			return ctrl.Result{}, err
		}
		// Deployment created successfully - return and requeue
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		ctxLogger.Error(err, "Failed to get Deployment")
		return ctrl.Result{}, err
	}

	// Enforce stdio transport replica cap: stdio requires 1:1 proxy-to-backend
	// connections and cannot scale beyond 1. Other transports are hands-off
	// to allow HPAs, KEDA, or manual kubectl scale to manage replicas freely.
	if mcpServer.Spec.Transport == stdioTransport &&
		deployment.Spec.Replicas != nil && *deployment.Spec.Replicas > 1 {
		deployment.Spec.Replicas = int32Ptr(1)
		err = r.Update(ctx, deployment)
		if err != nil {
			ctxLogger.Error(err, "Failed to cap stdio deployment replicas",
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
			"", // empty remoteUrl for MCPServer (not remote proxy)
		)
		err = r.Status().Update(ctx, mcpServer)
		if err != nil {
			ctxLogger.Error(err, "Failed to update MCPServer status")
			return ctrl.Result{}, err
		}
	}

	// Check if the deployment spec changed
	if r.deploymentNeedsUpdate(ctx, deployment, mcpServer, runConfigChecksum) {
		// Update template and metadata. Also sync Spec.Replicas when spec.replicas is
		// explicitly set — this makes the operator authoritative for spec-driven scaling.
		// When spec.replicas is nil, preserve the live count so HPAs, KEDA, and manual
		// kubectl scale remain in control.
		newDeployment := r.deploymentForMCPServer(ctx, mcpServer, runConfigChecksum)
		deployment.Spec.Template = newDeployment.Spec.Template
		deployment.Spec.Selector = newDeployment.Spec.Selector
		deployment.Labels = newDeployment.Labels
		deployment.Annotations = ctrlutil.MergeAnnotations(newDeployment.Annotations, deployment.Annotations)
		if newDeployment.Spec.Replicas != nil {
			deployment.Spec.Replicas = newDeployment.Spec.Replicas
		}
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
		service.Spec.SessionAffinity = newService.Spec.SessionAffinity
		service.Labels = newService.Labels
		service.Annotations = newService.Annotations
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

func (r *MCPServerReconciler) validateGroupRef(ctx context.Context, mcpServer *mcpv1beta1.MCPServer) {
	if mcpServer.Spec.GroupRef == nil {
		// No group reference, nothing to validate
		return
	}

	ctxLogger := log.FromContext(ctx)
	groupName := mcpServer.Spec.GroupRef.Name

	// Find the referenced MCPGroup
	group := &mcpv1beta1.MCPGroup{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: mcpServer.Namespace, Name: groupName}, group); err != nil {
		ctxLogger.Error(err, "Failed to validate GroupRef")
		meta.SetStatusCondition(&mcpServer.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionGroupRefValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1beta1.ConditionReasonGroupRefNotFound,
			Message:            fmt.Sprintf("MCPGroup '%s' not found in namespace '%s'", groupName, mcpServer.Namespace),
			ObservedGeneration: mcpServer.Generation,
		})
	} else if group.Status.Phase != mcpv1beta1.MCPGroupPhaseReady {
		meta.SetStatusCondition(&mcpServer.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionGroupRefValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1beta1.ConditionReasonGroupRefNotReady,
			Message:            fmt.Sprintf("MCPGroup '%s' is not ready (current phase: %s)", groupName, group.Status.Phase),
			ObservedGeneration: mcpServer.Generation,
		})
	} else {
		meta.SetStatusCondition(&mcpServer.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionGroupRefValidated,
			Status:             metav1.ConditionTrue,
			Reason:             mcpv1beta1.ConditionReasonGroupRefValidated,
			Message:            fmt.Sprintf("MCPGroup '%s' is valid and ready", groupName),
			ObservedGeneration: mcpServer.Generation,
		})
	}

	if err := r.Status().Update(ctx, mcpServer); err != nil {
		ctxLogger.Error(err, "Failed to update MCPServer status after GroupRef validation")
	}

}

// setCABundleRefCondition sets the CA bundle validation status condition
func setCABundleRefCondition(mcpServer *mcpv1beta1.MCPServer, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&mcpServer.Status.Conditions, metav1.Condition{
		Type:               mcpv1beta1.ConditionCABundleRefValidated,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: mcpServer.Generation,
	})
}

// validateCABundleRef validates the CABundleRef ConfigMap reference if specified.
// Checks the MCPOIDCConfig path for CA bundle references.
func (r *MCPServerReconciler) validateCABundleRef(ctx context.Context, mcpServer *mcpv1beta1.MCPServer) {
	var caBundleRef *mcpv1beta1.CABundleSource

	// Check MCPOIDCConfig inline CA bundle if using the reference path
	if mcpServer.Spec.OIDCConfigRef != nil {
		oidcCfg, err := ctrlutil.GetOIDCConfigForServer(ctx, r.Client, mcpServer.Namespace, mcpServer.Spec.OIDCConfigRef)
		if err == nil && oidcCfg != nil &&
			oidcCfg.Spec.Type == mcpv1beta1.MCPOIDCConfigTypeInline &&
			oidcCfg.Spec.Inline != nil {
			caBundleRef = oidcCfg.Spec.Inline.CABundleRef
		}
	}

	if caBundleRef == nil || caBundleRef.ConfigMapRef == nil {
		return
	}

	ctxLogger := log.FromContext(ctx)

	// Validate the CABundleRef configuration
	if err := validation.ValidateCABundleSource(caBundleRef); err != nil {
		ctxLogger.Error(err, "Invalid CABundleRef configuration")
		setCABundleRefCondition(mcpServer, metav1.ConditionFalse, mcpv1beta1.ConditionReasonCABundleRefInvalid, err.Error())
		r.updateCABundleStatus(ctx, mcpServer)
		return
	}

	// Check if the referenced ConfigMap exists
	cmName := caBundleRef.ConfigMapRef.Name
	configMap := &corev1.ConfigMap{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: mcpServer.Namespace, Name: cmName}, configMap); err != nil {
		ctxLogger.Error(err, "Failed to find CA bundle ConfigMap", "configMap", cmName)
		setCABundleRefCondition(mcpServer, metav1.ConditionFalse, mcpv1beta1.ConditionReasonCABundleRefNotFound,
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
		setCABundleRefCondition(mcpServer, metav1.ConditionFalse, mcpv1beta1.ConditionReasonCABundleRefInvalid,
			fmt.Sprintf("Key '%s' not found in ConfigMap '%s'", key, cmName))
		r.updateCABundleStatus(ctx, mcpServer)
		return
	}

	// Validation passed
	setCABundleRefCondition(mcpServer, metav1.ConditionTrue, mcpv1beta1.ConditionReasonCABundleRefValid,
		fmt.Sprintf("CA bundle ConfigMap '%s' is valid (key: %s)", cmName, key))
	r.updateCABundleStatus(ctx, mcpServer)
}

// updateCABundleStatus updates the MCPServer status after CA bundle validation
func (r *MCPServerReconciler) updateCABundleStatus(ctx context.Context, mcpServer *mcpv1beta1.MCPServer) {
	ctxLogger := log.FromContext(ctx)
	if err := r.Status().Update(ctx, mcpServer); err != nil {
		ctxLogger.Error(err, "Failed to update MCPServer status after CABundleRef validation")
	}
}

// setReadyCondition sets the top-level Ready status condition.
func setReadyCondition(mcpServer *mcpv1beta1.MCPServer, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&mcpServer.Status.Conditions, metav1.Condition{
		Type:               mcpv1beta1.ConditionTypeReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: mcpServer.Generation,
	})
}

// validateAndUpdatePodTemplateStatus validates the PodTemplateSpec and updates the MCPServer status
// with appropriate conditions and events
func (r *MCPServerReconciler) validateAndUpdatePodTemplateStatus(ctx context.Context, mcpServer *mcpv1beta1.MCPServer) bool {
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
			r.Recorder.Eventf(mcpServer, nil, corev1.EventTypeWarning, "InvalidPodTemplateSpec", "ValidatePodTemplateSpec",
				"Failed to parse PodTemplateSpec: %v. Deployment blocked until PodTemplateSpec is fixed.", err)
		}

		// Set phase and message
		mcpServer.Status.Phase = mcpv1beta1.MCPServerPhaseFailed
		mcpServer.Status.Message = fmt.Sprintf("Invalid PodTemplateSpec: %v", err)

		// Set condition for invalid PodTemplateSpec
		meta.SetStatusCondition(&mcpServer.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionPodTemplateValid,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: mcpServer.Generation,
			Reason:             mcpv1beta1.ConditionReasonPodTemplateInvalid,
			Message:            fmt.Sprintf("Failed to parse PodTemplateSpec: %v. Deployment blocked until fixed.", err),
		})

		setReadyCondition(mcpServer, metav1.ConditionFalse, mcpv1beta1.ConditionReasonNotReady,
			fmt.Sprintf("Invalid PodTemplateSpec: %v", err))

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
		Type:               mcpv1beta1.ConditionPodTemplateValid,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: mcpServer.Generation,
		Reason:             mcpv1beta1.ConditionReasonPodTemplateValid,
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
func (r *MCPServerReconciler) handleRestartAnnotation(ctx context.Context, mcpServer *mcpv1beta1.MCPServer) (bool, error) {
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

	// Update the last processed restart timestamp in annotations.
	if err := ctrlutil.MutateAndPatchSpec(ctx, r.Client, mcpServer, func(m *mcpv1beta1.MCPServer) {
		if m.Annotations == nil {
			m.Annotations = make(map[string]string)
		}
		m.Annotations[LastProcessedRestartAnnotationKey] = currentRestartedAt
	}); err != nil {
		return false, fmt.Errorf("failed to update MCPServer with last processed restart annotation: %w", err)
	}

	return true, nil
}

// performRestart executes the restart based on the specified strategy
func (r *MCPServerReconciler) performRestart(ctx context.Context, mcpServer *mcpv1beta1.MCPServer, strategy string) error {
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
	ctx context.Context, mcpServer *mcpv1beta1.MCPServer,
) (string, error) {
	if mcpServer == nil {
		return "", fmt.Errorf("mcpServer cannot be nil")
	}

	fetcher := checksum.NewRunConfigChecksumFetcher(r.Client)
	return fetcher.GetRunConfigChecksum(ctx, mcpServer.Namespace, mcpServer.Name)
}

// performRollingRestart triggers a rolling restart by updating the deployment's pod template annotation
func (r *MCPServerReconciler) performRollingRestart(ctx context.Context, mcpServer *mcpv1beta1.MCPServer) error {
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
func (r *MCPServerReconciler) performImmediateRestart(ctx context.Context, mcpServer *mcpv1beta1.MCPServer) error {
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

// handleToolConfig handles MCPToolConfig reference for an MCPServer
func (r *MCPServerReconciler) handleToolConfig(ctx context.Context, m *mcpv1beta1.MCPServer) error {
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
func (r *MCPServerReconciler) ensureRBACResources(ctx context.Context, mcpServer *mcpv1beta1.MCPServer) error {
	rbacClient := rbac.NewClient(r.Client, r.Scheme)
	proxyRunnerNameForRBAC := ctrlutil.ProxyRunnerServiceAccountName(mcpServer.Name)

	// Extract ImagePullSecrets from ResourceOverrides if present
	var imagePullSecrets []corev1.LocalObjectReference
	if mcpServer.Spec.ResourceOverrides != nil &&
		mcpServer.Spec.ResourceOverrides.ProxyDeployment != nil {
		imagePullSecrets = mcpServer.Spec.ResourceOverrides.ProxyDeployment.ImagePullSecrets
	}

	// Ensure RBAC resources for proxy runner
	if _, err := rbacClient.EnsureRBACResources(ctx, rbac.EnsureRBACResourcesParams{
		Name:             proxyRunnerNameForRBAC,
		Namespace:        mcpServer.Namespace,
		Rules:            defaultRBACRules,
		Owner:            mcpServer,
		ImagePullSecrets: imagePullSecrets,
	}); err != nil {
		return err
	}

	// If a service account is specified, we don't need to create one
	if mcpServer.Spec.ServiceAccount != nil {
		return nil
	}

	// Otherwise, create a service account for the MCP server
	mcpServerSAName := mcpServerServiceAccountName(mcpServer.Name)
	mcpServerSA := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mcpServerSAName,
			Namespace: mcpServer.Namespace,
		},
		ImagePullSecrets: imagePullSecrets,
	}
	_, err := rbacClient.UpsertServiceAccountWithOwnerReference(ctx, mcpServerSA, mcpServer)
	return err
}

// deploymentForMCPServer returns a MCPServer Deployment object
//
//nolint:gocyclo
func (r *MCPServerReconciler) deploymentForMCPServer(
	ctx context.Context, m *mcpv1beta1.MCPServer, runConfigChecksum string,
) *appsv1.Deployment {
	ls := labelsForMCPServer(m.Name)

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

	// Add OpenTelemetry environment variables: prefer TelemetryConfigRef over deprecated inline.
	// handleTelemetryConfig already validated this ref earlier in the reconcile loop;
	// a failure here means a transient issue, so we log a warning and proceed without
	// telemetry env vars rather than blocking the entire deployment creation.
	if m.Spec.TelemetryConfigRef != nil {
		telCfg, telErr := getTelemetryConfigForMCPServer(ctx, r.Client, m)
		if telErr != nil {
			ctxLogger := log.FromContext(ctx)
			ctxLogger.V(0).Info("MCPTelemetryConfig fetch failed after prior validation; deployment may lack telemetry env vars",
				"telemetryConfig", m.Spec.TelemetryConfigRef.Name, "error", telErr)
		} else if telCfg != nil {
			env = append(env, ctrlutil.GenerateOpenTelemetryEnvVarsFromRef(telCfg, m.Spec.TelemetryConfigRef, m.Name, m.Namespace)...)
		}
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

	// Add OIDC client secret environment variable if using MCPOIDCConfigRef with inline config
	if m.Spec.OIDCConfigRef != nil {
		// Check MCPOIDCConfig inline config for client secret
		oidcCfg, err := ctrlutil.GetOIDCConfigForServer(ctx, r.Client, m.Namespace, m.Spec.OIDCConfigRef)
		if err == nil && oidcCfg != nil &&
			oidcCfg.Spec.Type == mcpv1beta1.MCPOIDCConfigTypeInline &&
			oidcCfg.Spec.Inline != nil {
			oidcClientSecretEnvVar, err := ctrlutil.GenerateOIDCClientSecretEnvVar(
				ctx, r.Client, m.Namespace, oidcCfg.Spec.Inline.ClientSecretRef,
			)
			if err != nil {
				ctxLogger := log.FromContext(ctx)
				ctxLogger.Error(err, "Failed to generate OIDC client secret environment variable from MCPOIDCConfig")
			} else if oidcClientSecretEnvVar != nil {
				env = append(env, *oidcClientSecretEnvVar)
			}
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
	if m.Spec.PermissionProfile != nil && m.Spec.PermissionProfile.Type == mcpv1beta1.PermissionProfileTypeConfigMap {
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

	// Add OIDC CA bundle volume if configured via MCPOIDCConfigRef
	if m.Spec.OIDCConfigRef != nil {
		oidcCfg, err := ctrlutil.GetOIDCConfigForServer(ctx, r.Client, m.Namespace, m.Spec.OIDCConfigRef)
		if err == nil && oidcCfg != nil {
			caVolumes, caMounts := ctrlutil.AddOIDCConfigRefCABundleVolumes(oidcCfg)
			volumes = append(volumes, caVolumes...)
			volumeMounts = append(volumeMounts, caMounts...)
		}
	}

	// Add telemetry CA bundle volume if configured via MCPTelemetryConfig
	if m.Spec.TelemetryConfigRef != nil {
		telCfg, err := getTelemetryConfigForMCPServer(ctx, r.Client, m)
		if err != nil {
			ctxLogger := log.FromContext(ctx)
			ctxLogger.Error(err, "Failed to fetch MCPTelemetryConfig for CA bundle volume")
			return nil
		}
		if telCfg != nil {
			caVolumes, caMounts := ctrlutil.AddTelemetryCABundleVolumes(telCfg)
			volumes = append(volumes, caVolumes...)
			volumeMounts = append(volumeMounts, caMounts...)
		}
	}

	// Add embedded auth server volumes and env vars. AuthServerRef takes precedence;
	// externalAuthConfigRef is used as a fallback (legacy path).
	if configName := ctrlutil.EmbeddedAuthServerConfigName(m.Spec.ExternalAuthConfigRef, m.Spec.AuthServerRef); configName != "" {
		authServerVolumes, authServerMounts, authServerEnvVars, err := ctrlutil.GenerateAuthServerConfigByName(
			ctx, r.Client, m.Namespace, configName,
		)
		if err != nil {
			log.FromContext(ctx).Error(err, "Failed to generate auth server configuration")
			return nil
		}
		volumes = append(volumes, authServerVolumes...)
		volumeMounts = append(volumeMounts, authServerMounts...)
		env = append(env, authServerEnvVars...)
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

	// Extract ImagePullSecrets from ResourceOverrides if present
	var imagePullSecrets []corev1.LocalObjectReference
	if m.Spec.ResourceOverrides != nil && m.Spec.ResourceOverrides.ProxyDeployment != nil {
		imagePullSecrets = m.Spec.ResourceOverrides.ProxyDeployment.ImagePullSecrets
	}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        m.Name,
			Namespace:   m.Namespace,
			Labels:      deploymentLabels,
			Annotations: deploymentAnnotations,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: resolveDeploymentReplicas(m.Spec.Transport, m.Spec.Replicas),
			Selector: &metav1.LabelSelector{
				MatchLabels: ls, // Keep original labels for selector
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      deploymentTemplateLabels,
					Annotations: deploymentTemplateAnnotations,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName:            ctrlutil.ProxyRunnerServiceAccountName(m.Name),
					ImagePullSecrets:              imagePullSecrets,
					TerminationGracePeriodSeconds: int64Ptr(defaultTerminationGracePeriodSeconds),
					Containers: []corev1.Container{{
						Image:        getToolhiveRunnerImage(),
						Name:         "toolhive",
						Args:         args,
						Env:          env,
						VolumeMounts: volumeMounts,
						Resources:    resources,
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
func (r *MCPServerReconciler) serviceForMCPServer(ctx context.Context, m *mcpv1beta1.MCPServer) *corev1.Service {
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

	sessionAffinity := func() corev1.ServiceAffinity {
		if m.Spec.SessionAffinity != "" {
			return corev1.ServiceAffinity(m.Spec.SessionAffinity)
		}
		return corev1.ServiceAffinityClientIP
	}()

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        svcName,
			Namespace:   m.Namespace,
			Labels:      serviceLabels,
			Annotations: serviceAnnotations,
		},
		Spec: corev1.ServiceSpec{
			Selector:        ls, // Keep original labels for selector
			SessionAffinity: sessionAffinity,
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
	// Exclude terminating pods from status counts to avoid inflated ReadyReplicas
	// during rolling updates (see https://github.com/stacklok/toolhive/issues/4498)
	if pod.DeletionTimestamp != nil {
		return 0, 0, 0, ""
	}

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
func (r *MCPServerReconciler) updateMCPServerStatus(ctx context.Context, m *mcpv1beta1.MCPServer) error {
	// Update ObservedGeneration to reflect that we've processed this generation
	m.Status.ObservedGeneration = m.Generation

	// Handle scale-to-zero: if deployment exists with 0 replicas, report Stopped
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Name: m.Name, Namespace: m.Namespace}, deployment); err == nil {
		if deployment.Spec.Replicas != nil && *deployment.Spec.Replicas == 0 {
			m.Status.Phase = mcpv1beta1.MCPServerPhaseStopped
			m.Status.Message = "MCP server is stopped (scaled to zero)"
			m.Status.ReadyReplicas = 0
			setReadyCondition(m, metav1.ConditionFalse, mcpv1beta1.ConditionReasonNotReady, "MCP server is stopped (scaled to zero)")
			return r.Status().Update(ctx, m)
		}
	}

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
		// No Deployment pods found yet. If a previous reconciliation already set Phase=Failed
		// (e.g. due to a RunConfig or RBAC error), preserve that status so the failure
		// reason remains visible. Only reset to Pending when the phase is not Failed.
		if m.Status.Phase != mcpv1beta1.MCPServerPhaseFailed {
			m.Status.Phase = mcpv1beta1.MCPServerPhasePending
			m.Status.Message = "MCP server is being created"
			m.Status.ReadyReplicas = 0
			setReadyCondition(m, metav1.ConditionFalse, mcpv1beta1.ConditionReasonNotReady, "MCP server is being created")
			return r.Status().Update(ctx, m)
		}
		return nil
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

	// Set ReadyReplicas to the count of running pods
	m.Status.ReadyReplicas = int32(running)

	// Update the status based on pod health
	if running > 0 {
		m.Status.Phase = mcpv1beta1.MCPServerPhaseReady
		m.Status.Message = "MCP server is running"
	} else if failed > 0 {
		m.Status.Phase = mcpv1beta1.MCPServerPhaseFailed
		if failureReason != "" {
			m.Status.Message = fmt.Sprintf("MCP server pod failed: %s", failureReason)
		} else {
			m.Status.Message = "MCP server pod failed"
		}
	} else if pending > 0 {
		m.Status.Phase = mcpv1beta1.MCPServerPhasePending
		m.Status.Message = "MCP server is starting"
	} else {
		m.Status.Phase = mcpv1beta1.MCPServerPhasePending
		m.Status.Message = "No healthy pods found"
	}

	// Set the top-level Ready condition based on the determined phase
	if m.Status.Phase == mcpv1beta1.MCPServerPhaseReady {
		setReadyCondition(m, metav1.ConditionTrue, mcpv1beta1.ConditionReasonReady, "MCP server is running")
	} else {
		setReadyCondition(m, metav1.ConditionFalse, mcpv1beta1.ConditionReasonNotReady, m.Status.Message)
	}

	// Update the status
	return r.Status().Update(ctx, m)
}

// deleteIfExists fetches a Kubernetes object by name and namespace, and deletes it if it exists.
// Returns nil if the object was not found or was successfully deleted.
func (r *MCPServerReconciler) deleteIfExists(ctx context.Context, obj client.Object, name, namespace, kind string) error {
	ctxLogger := log.FromContext(ctx)
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, obj)
	if err == nil {
		if delErr := r.Delete(ctx, obj); delErr != nil && !errors.IsNotFound(delErr) {
			return fmt.Errorf("failed to delete %s %s: %w", kind, name, delErr)
		}
		ctxLogger.V(1).Info("deleted resource", "kind", kind, "name", name, "namespace", namespace)
		return nil
	}
	if !errors.IsNotFound(err) {
		return fmt.Errorf("failed to check %s %s: %w", kind, name, err)
	}
	return nil
}

// finalizeMCPServer performs the finalizer logic for the MCPServer
func (r *MCPServerReconciler) finalizeMCPServer(ctx context.Context, m *mcpv1beta1.MCPServer) error {
	// Update the MCPServer status
	m.Status.Phase = mcpv1beta1.MCPServerPhaseTerminating
	m.Status.Message = "MCP server is being terminated"
	setReadyCondition(m, metav1.ConditionFalse, mcpv1beta1.ConditionReasonNotReady, "MCP server is being terminated")
	if err := r.Status().Update(ctx, m); err != nil {
		return err
	}

	// Delete associated StatefulSet
	if err := r.deleteIfExists(ctx, &appsv1.StatefulSet{}, m.Name, m.Namespace, "StatefulSet"); err != nil {
		return err
	}

	// Delete associated services
	if err := r.deleteIfExists(ctx, &corev1.Service{}, fmt.Sprintf("mcp-%s-headless", m.Name), m.Namespace, "Service"); err != nil {
		return err
	}
	if err := r.deleteIfExists(ctx, &corev1.Service{}, fmt.Sprintf("mcp-%s", m.Name), m.Namespace, "Service"); err != nil {
		return err
	}

	// Delete associated RunConfig ConfigMap
	return r.deleteIfExists(ctx, &corev1.ConfigMap{}, fmt.Sprintf("%s-runconfig", m.Name), m.Namespace, "ConfigMap")
}

// deploymentNeedsUpdate checks if the deployment needs to be updated
//
//nolint:gocyclo
func (r *MCPServerReconciler) deploymentNeedsUpdate(
	ctx context.Context,
	deployment *appsv1.Deployment,
	mcpServer *mcpv1beta1.MCPServer,
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

		// Add OpenTelemetry environment variables: prefer TelemetryConfigRef over deprecated inline
		if mcpServer.Spec.TelemetryConfigRef != nil {
			telCfg, telErr := getTelemetryConfigForMCPServer(ctx, r.Client, mcpServer)
			if telErr != nil {
				// Can't determine expected env vars; assume deployment needs update.
				// The actual error will surface during deployment creation.
				return true
			}
			if telCfg != nil {
				otelEnvVars := ctrlutil.GenerateOpenTelemetryEnvVarsFromRef(
					telCfg, mcpServer.Spec.TelemetryConfigRef, mcpServer.Name, mcpServer.Namespace,
				)
				expectedProxyEnv = append(expectedProxyEnv, otelEnvVars...)
			}
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

		// Add OIDC client secret environment variable if using MCPOIDCConfigRef with inline config
		if mcpServer.Spec.OIDCConfigRef != nil {
			oidcCfg, err := ctrlutil.GetOIDCConfigForServer(ctx, r.Client, mcpServer.Namespace, mcpServer.Spec.OIDCConfigRef)
			if err != nil {
				return true
			}
			if oidcCfg != nil &&
				oidcCfg.Spec.Type == mcpv1beta1.MCPOIDCConfigTypeInline &&
				oidcCfg.Spec.Inline != nil {
				oidcClientSecretEnvVar, err := ctrlutil.GenerateOIDCClientSecretEnvVar(
					ctx, r.Client, mcpServer.Namespace, oidcCfg.Spec.Inline.ClientSecretRef,
				)
				if err != nil {
					return true
				}
				if oidcClientSecretEnvVar != nil {
					expectedProxyEnv = append(expectedProxyEnv, *oidcClientSecretEnvVar)
				}
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

		// Add embedded auth server environment variables. AuthServerRef takes precedence;
		// externalAuthConfigRef is used as a fallback (legacy path).
		if configName := ctrlutil.EmbeddedAuthServerConfigName(
			mcpServer.Spec.ExternalAuthConfigRef, mcpServer.Spec.AuthServerRef,
		); configName != "" {
			_, _, authServerEnvVars, err := ctrlutil.GenerateAuthServerConfigByName(
				ctx, r.Client, mcpServer.Namespace, configName,
			)
			if err != nil {
				return true
			}
			expectedProxyEnv = append(expectedProxyEnv, authServerEnvVars...)
		}
		// Add default environment variables that are always injected
		expectedProxyEnv = ctrlutil.EnsureRequiredEnvVars(ctx, expectedProxyEnv)
		if !equality.Semantic.DeepEqual(container.Env, expectedProxyEnv) {
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

		// Check if image pull secrets have changed.
		// Sourced from spec.resourceOverrides.proxyDeployment.imagePullSecrets.
		// Uses equality.Semantic.DeepEqual so nil and empty slices are treated as equal.
		var expectedPullSecrets []corev1.LocalObjectReference
		if mcpServer.Spec.ResourceOverrides != nil &&
			mcpServer.Spec.ResourceOverrides.ProxyDeployment != nil {
			expectedPullSecrets = mcpServer.Spec.ResourceOverrides.ProxyDeployment.ImagePullSecrets
		}
		if !equality.Semantic.DeepEqual(deployment.Spec.Template.Spec.ImagePullSecrets, expectedPullSecrets) {
			return true
		}

		// Check if the resource requirements have changed
		if !equality.Semantic.DeepEqual(container.Resources, resourceRequirementsForMCPServer(mcpServer)) {
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

	if !ctrlutil.MapIsSubset(expectedAnnotations, deployment.Annotations) {
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

	// Check if spec.replicas has changed. Only compare when spec.replicas is non-nil;
	// nil means hands-off mode (HPA/KEDA manages replicas) and the live count is authoritative.
	expectedReplicas := resolveDeploymentReplicas(mcpServer.Spec.Transport, mcpServer.Spec.Replicas)
	if expectedReplicas != nil {
		if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != *expectedReplicas {
			return true
		}
	}

	return false
}

// serviceNeedsUpdate checks if the service needs to be updated
func serviceNeedsUpdate(service *corev1.Service, mcpServer *mcpv1beta1.MCPServer) bool {
	// Check if the service port has changed
	if len(service.Spec.Ports) > 0 && service.Spec.Ports[0].Port != mcpServer.GetProxyPort() {
		return true
	}

	// Check if session affinity has drifted from spec
	expectedAffinity := func() corev1.ServiceAffinity {
		if mcpServer.Spec.SessionAffinity != "" {
			return corev1.ServiceAffinity(mcpServer.Spec.SessionAffinity)
		}
		return corev1.ServiceAffinityClientIP
	}()
	if service.Spec.SessionAffinity != expectedAffinity {
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
func resourceRequirementsForMCPServer(m *mcpv1beta1.MCPServer) corev1.ResourceRequirements {
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

// handleExternalAuthConfig validates and tracks the hash of the referenced MCPExternalAuthConfig.
// It updates the MCPServer status when the external auth configuration changes.
func (r *MCPServerReconciler) handleExternalAuthConfig(ctx context.Context, m *mcpv1beta1.MCPServer) error {
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

	// MCPServer supports only single-upstream embedded auth server configs.
	// Multi-upstream requires VirtualMCPServer.
	if embeddedCfg := externalAuthConfig.Spec.EmbeddedAuthServer; embeddedCfg != nil && len(embeddedCfg.UpstreamProviders) > 1 {
		meta.SetStatusCondition(&m.Status.Conditions, metav1.Condition{
			Type:   mcpv1beta1.ConditionTypeExternalAuthConfigValidated,
			Status: metav1.ConditionFalse,
			Reason: mcpv1beta1.ConditionReasonExternalAuthConfigMultiUpstream,
			Message: fmt.Sprintf(
				"MCPServer supports only one upstream provider (found %d); "+
					"use VirtualMCPServer for multi-upstream",
				len(embeddedCfg.UpstreamProviders)),
			ObservedGeneration: m.Generation,
		})
		return fmt.Errorf(
			"MCPServer %s/%s: embedded auth server has %d upstream providers, "+
				"but only 1 is supported; use VirtualMCPServer",
			m.Namespace, m.Name, len(embeddedCfg.UpstreamProviders))
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

// handleAuthServerRef validates and tracks the hash of the referenced authServerRef config.
// It updates the MCPServer status when the auth server configuration changes and sets
// the AuthServerRefValidated condition.
func (r *MCPServerReconciler) handleAuthServerRef(ctx context.Context, m *mcpv1beta1.MCPServer) error {
	ctxLogger := log.FromContext(ctx)
	if m.Spec.AuthServerRef == nil {
		meta.RemoveStatusCondition(&m.Status.Conditions, mcpv1beta1.ConditionTypeAuthServerRefValidated)
		if m.Status.AuthServerConfigHash != "" {
			m.Status.AuthServerConfigHash = ""
			if err := r.Status().Update(ctx, m); err != nil {
				return fmt.Errorf("failed to clear authServerRef hash from status: %w", err)
			}
		}
		return nil
	}

	// Only MCPExternalAuthConfig kind is supported
	if m.Spec.AuthServerRef.Kind != "MCPExternalAuthConfig" {
		meta.SetStatusCondition(&m.Status.Conditions, metav1.Condition{
			Type:   mcpv1beta1.ConditionTypeAuthServerRefValidated,
			Status: metav1.ConditionFalse,
			Reason: mcpv1beta1.ConditionReasonAuthServerRefInvalidKind,
			Message: fmt.Sprintf("unsupported authServerRef kind %q: only MCPExternalAuthConfig is supported",
				m.Spec.AuthServerRef.Kind),
			ObservedGeneration: m.Generation,
		})
		return fmt.Errorf("unsupported authServerRef kind %q: only MCPExternalAuthConfig is supported", m.Spec.AuthServerRef.Kind)
	}

	// Fetch the referenced MCPExternalAuthConfig
	authConfig, err := ctrlutil.GetExternalAuthConfigByName(ctx, r.Client, m.Namespace, m.Spec.AuthServerRef.Name)
	if err != nil {
		if errors.IsNotFound(err) {
			meta.SetStatusCondition(&m.Status.Conditions, metav1.Condition{
				Type:   mcpv1beta1.ConditionTypeAuthServerRefValidated,
				Status: metav1.ConditionFalse,
				Reason: mcpv1beta1.ConditionReasonAuthServerRefNotFound,
				Message: fmt.Sprintf("MCPExternalAuthConfig '%s' not found in namespace '%s'",
					m.Spec.AuthServerRef.Name, m.Namespace),
				ObservedGeneration: m.Generation,
			})
			return fmt.Errorf("MCPExternalAuthConfig '%s' not found in namespace '%s'",
				m.Spec.AuthServerRef.Name, m.Namespace)
		}
		meta.SetStatusCondition(&m.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionTypeAuthServerRefValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1beta1.ConditionReasonAuthServerRefFetchError,
			Message:            fmt.Sprintf("Failed to fetch MCPExternalAuthConfig '%s'", m.Spec.AuthServerRef.Name),
			ObservedGeneration: m.Generation,
		})
		return fmt.Errorf("failed to get authServerRef MCPExternalAuthConfig %s: %w", m.Spec.AuthServerRef.Name, err)
	}

	// Validate the config type is embeddedAuthServer
	if authConfig.Spec.Type != mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer {
		meta.SetStatusCondition(&m.Status.Conditions, metav1.Condition{
			Type:   mcpv1beta1.ConditionTypeAuthServerRefValidated,
			Status: metav1.ConditionFalse,
			Reason: mcpv1beta1.ConditionReasonAuthServerRefInvalidType,
			Message: fmt.Sprintf("authServerRef '%s' has type %q, but only embeddedAuthServer is supported",
				m.Spec.AuthServerRef.Name, authConfig.Spec.Type),
			ObservedGeneration: m.Generation,
		})
		return fmt.Errorf("authServerRef '%s' has type %q, but only embeddedAuthServer is supported",
			m.Spec.AuthServerRef.Name, authConfig.Spec.Type)
	}

	// MCPServer supports only single-upstream embedded auth server configs
	if embeddedCfg := authConfig.Spec.EmbeddedAuthServer; embeddedCfg != nil && len(embeddedCfg.UpstreamProviders) > 1 {
		meta.SetStatusCondition(&m.Status.Conditions, metav1.Condition{
			Type:   mcpv1beta1.ConditionTypeAuthServerRefValidated,
			Status: metav1.ConditionFalse,
			Reason: mcpv1beta1.ConditionReasonAuthServerRefMultiUpstream,
			Message: fmt.Sprintf("MCPServer supports only one upstream provider (found %d); "+
				"use VirtualMCPServer for multi-upstream",
				len(embeddedCfg.UpstreamProviders)),
			ObservedGeneration: m.Generation,
		})
		return fmt.Errorf("MCPServer %s/%s: embedded auth server has %d upstream providers, "+
			"but only 1 is supported; use VirtualMCPServer",
			m.Namespace, m.Name, len(embeddedCfg.UpstreamProviders))
	}

	// AuthServerRef valid
	meta.SetStatusCondition(&m.Status.Conditions, metav1.Condition{
		Type:               mcpv1beta1.ConditionTypeAuthServerRefValidated,
		Status:             metav1.ConditionTrue,
		Reason:             mcpv1beta1.ConditionReasonAuthServerRefValid,
		Message:            fmt.Sprintf("AuthServerRef '%s' is valid", authConfig.Name),
		ObservedGeneration: m.Generation,
	})

	// Check if the config hash has changed
	if m.Status.AuthServerConfigHash != authConfig.Status.ConfigHash {
		ctxLogger.Info("authServerRef config has changed, updating MCPServer",
			"mcpserver", m.Name,
			"authServerRef", authConfig.Name,
			"oldHash", m.Status.AuthServerConfigHash,
			"newHash", authConfig.Status.ConfigHash)

		m.Status.AuthServerConfigHash = authConfig.Status.ConfigHash
		if err := r.Status().Update(ctx, m); err != nil {
			return fmt.Errorf("failed to update authServerRef hash in status: %w", err)
		}
	}

	return nil
}

// handleOIDCConfig validates and tracks the hash of the referenced MCPOIDCConfig.
// It updates the MCPServer status when the OIDC configuration changes and sets
// the OIDCConfigRefValidated condition.
func (r *MCPServerReconciler) handleOIDCConfig(ctx context.Context, m *mcpv1beta1.MCPServer) error {
	ctxLogger := log.FromContext(ctx)

	if m.Spec.OIDCConfigRef == nil {
		// No MCPOIDCConfig referenced, clear any stored hash
		if m.Status.OIDCConfigHash != "" {
			m.Status.OIDCConfigHash = ""
			if err := r.Status().Update(ctx, m); err != nil {
				return fmt.Errorf("failed to clear MCPOIDCConfig hash from status: %w", err)
			}
		}
		return nil
	}

	// Fetch and validate the referenced MCPOIDCConfig
	oidcConfig, err := r.fetchAndValidateOIDCConfig(ctx, m)
	if err != nil {
		return err
	}

	// Update ReferencingWorkloads on the MCPOIDCConfig status
	if err := r.updateOIDCConfigReferencingWorkloads(ctx, oidcConfig, m.Name); err != nil {
		ctxLogger.Error(err, "Failed to update MCPOIDCConfig ReferencingWorkloads")
		// Non-fatal: continue with reconciliation
	}

	// Detect whether the condition is transitioning to True (e.g. recovering from
	// a transient error). Without this check the status update is skipped when the
	// hash is unchanged, leaving a stale False condition (#4511).
	prevCondition := meta.FindStatusCondition(m.Status.Conditions, mcpv1beta1.ConditionOIDCConfigRefValidated)
	needsUpdate := prevCondition == nil || prevCondition.Status != metav1.ConditionTrue

	setOIDCConfigRefCondition(m, metav1.ConditionTrue,
		mcpv1beta1.ConditionReasonOIDCConfigRefValid,
		fmt.Sprintf("MCPOIDCConfig %s is valid and ready", m.Spec.OIDCConfigRef.Name))

	if m.Status.OIDCConfigHash != oidcConfig.Status.ConfigHash {
		ctxLogger.Info("MCPOIDCConfig has changed, updating MCPServer",
			"mcpserver", m.Name,
			"oidcConfig", oidcConfig.Name,
			"oldHash", m.Status.OIDCConfigHash,
			"newHash", oidcConfig.Status.ConfigHash)
		m.Status.OIDCConfigHash = oidcConfig.Status.ConfigHash
		needsUpdate = true
	}

	if needsUpdate {
		if err := r.Status().Update(ctx, m); err != nil {
			return fmt.Errorf("failed to update MCPOIDCConfig status: %w", err)
		}
	}

	return nil
}

// fetchAndValidateOIDCConfig fetches the referenced MCPOIDCConfig, validates it is
// ready, and sets appropriate failure conditions on the MCPServer if not.
func (r *MCPServerReconciler) fetchAndValidateOIDCConfig(
	ctx context.Context, m *mcpv1beta1.MCPServer,
) (*mcpv1beta1.MCPOIDCConfig, error) {
	ctxLogger := log.FromContext(ctx)

	oidcConfig, err := ctrlutil.GetOIDCConfigForServer(ctx, r.Client, m.Namespace, m.Spec.OIDCConfigRef)
	if err != nil {
		setOIDCConfigRefCondition(m, metav1.ConditionFalse,
			mcpv1beta1.ConditionReasonOIDCConfigRefNotFound,
			fmt.Sprintf("MCPOIDCConfig %s not found: %v", m.Spec.OIDCConfigRef.Name, err))
		if statusErr := r.Status().Update(ctx, m); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update status after MCPOIDCConfig lookup error")
		}
		return nil, err
	}

	if oidcConfig == nil {
		setOIDCConfigRefCondition(m, metav1.ConditionFalse,
			mcpv1beta1.ConditionReasonOIDCConfigRefNotFound,
			fmt.Sprintf("MCPOIDCConfig %s not found", m.Spec.OIDCConfigRef.Name))
		if statusErr := r.Status().Update(ctx, m); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update status after MCPOIDCConfig not found")
		}
		return nil, fmt.Errorf("MCPOIDCConfig %s not found", m.Spec.OIDCConfigRef.Name)
	}

	validCondition := meta.FindStatusCondition(oidcConfig.Status.Conditions, mcpv1beta1.ConditionTypeOIDCConfigValid)
	if validCondition == nil || validCondition.Status != metav1.ConditionTrue {
		msg := fmt.Sprintf("MCPOIDCConfig %s is not valid", m.Spec.OIDCConfigRef.Name)
		if validCondition != nil {
			msg = fmt.Sprintf("MCPOIDCConfig %s is not valid: %s", m.Spec.OIDCConfigRef.Name, validCondition.Message)
		}
		setOIDCConfigRefCondition(m, metav1.ConditionFalse,
			mcpv1beta1.ConditionReasonOIDCConfigRefNotValid, msg)
		if statusErr := r.Status().Update(ctx, m); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update status after MCPOIDCConfig validation check")
		}
		return nil, fmt.Errorf("%s", msg)
	}

	return oidcConfig, nil
}

// setOIDCConfigRefCondition sets the OIDCConfigRefValidated status condition
func setOIDCConfigRefCondition(m *mcpv1beta1.MCPServer, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&m.Status.Conditions, metav1.Condition{
		Type:               mcpv1beta1.ConditionOIDCConfigRefValidated,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: m.Generation,
	})
}

// updateOIDCConfigReferencingWorkloads ensures the MCPServer is listed in
// the MCPOIDCConfig's ReferencingWorkloads status field.
func (r *MCPServerReconciler) updateOIDCConfigReferencingWorkloads(
	ctx context.Context,
	oidcConfig *mcpv1beta1.MCPOIDCConfig,
	serverName string,
) error {
	ref := mcpv1beta1.WorkloadReference{
		Kind: mcpv1beta1.WorkloadKindMCPServer,
		Name: serverName,
	}

	// Check if already listed
	for _, entry := range oidcConfig.Status.ReferencingWorkloads {
		if entry.Kind == ref.Kind && entry.Name == ref.Name {
			return nil
		}
	}

	// Add the workload reference
	oidcConfig.Status.ReferencingWorkloads = append(oidcConfig.Status.ReferencingWorkloads, ref)
	if err := r.Status().Update(ctx, oidcConfig); err != nil {
		return fmt.Errorf("failed to update MCPOIDCConfig ReferencingWorkloads: %w", err)
	}

	return nil
}

// ensureAuthzConfigMap ensures the authorization ConfigMap exists for inline configuration
func (r *MCPServerReconciler) ensureAuthzConfigMap(ctx context.Context, m *mcpv1beta1.MCPServer) error {
	return ctrlutil.EnsureAuthzConfigMap(
		ctx, r.Client, r.Scheme, m, m.Namespace, m.Name, m.Spec.AuthzConfig, labelsForInlineAuthzConfig(m.Name),
	)
}

// int32Ptr returns a pointer to an int32
func int32Ptr(i int32) *int32 {
	return &i
}

// int64Ptr returns a pointer to an int64
func int64Ptr(i int64) *int64 {
	return &i
}

// resolveDeploymentReplicas returns the replica count to set on Deployment.Spec.Replicas.
// Returns nil when spec.replicas is nil (hands-off mode for HPA/KEDA).
// Enforces stdio cap at 1 as defense-in-depth (reconciler also enforces this via status condition).
func resolveDeploymentReplicas(mcpTransport string, specReplicas *int32) *int32 {
	if specReplicas == nil {
		return nil
	}
	if mcpTransport == stdioTransport && *specReplicas > 1 {
		return int32Ptr(1)
	}
	return specReplicas
}

// setStdioReplicaCappedCondition sets the StdioReplicaCapped status condition
func setStdioReplicaCappedCondition(mcpServer *mcpv1beta1.MCPServer, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&mcpServer.Status.Conditions, metav1.Condition{
		Type:               mcpv1beta1.ConditionStdioReplicaCapped,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: mcpServer.Generation,
	})
}

// validateStdioReplicaCap checks if spec.replicas > 1 for stdio transport and sets a warning condition.
// The deployment builder enforces the cap at 1 as defense-in-depth.
// Clears the condition when transport or replica count no longer violates the cap.
func (r *MCPServerReconciler) validateStdioReplicaCap(ctx context.Context, mcpServer *mcpv1beta1.MCPServer) {
	if mcpServer.Spec.Transport == stdioTransport && mcpServer.Spec.Replicas != nil && *mcpServer.Spec.Replicas > 1 {
		setStdioReplicaCappedCondition(mcpServer, metav1.ConditionTrue,
			mcpv1beta1.ConditionReasonStdioReplicaCapped,
			"stdio transport requires exactly 1 replica; deployment will use 1 regardless of spec.replicas")
	} else {
		setStdioReplicaCappedCondition(mcpServer, metav1.ConditionFalse,
			mcpv1beta1.ConditionReasonStdioReplicaCapNotActive,
			"stdio replica cap is not active")
	}
	if err := r.Status().Update(ctx, mcpServer); err != nil {
		log.FromContext(ctx).Error(err, "Failed to update MCPServer status after stdio replica cap validation")
	}
}

// setSessionStorageCondition sets the SessionStorageWarning status condition
func setSessionStorageCondition(mcpServer *mcpv1beta1.MCPServer, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&mcpServer.Status.Conditions, metav1.Condition{
		Type:               mcpv1beta1.ConditionSessionStorageWarning,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: mcpServer.Generation,
	})
}

// validateSessionStorageForReplicas emits a Warning condition when replicas > 1 but session storage
// is not configured with a Redis backend. The deployment still proceeds; this is advisory only.
// Clears the condition when replicas drop back to nil or <= 1.
func (r *MCPServerReconciler) validateSessionStorageForReplicas(ctx context.Context, mcpServer *mcpv1beta1.MCPServer) {
	if mcpServer.Spec.Replicas != nil && *mcpServer.Spec.Replicas > 1 {
		if mcpServer.Spec.SessionStorage == nil || mcpServer.Spec.SessionStorage.Provider != mcpv1beta1.SessionStorageProviderRedis {
			setSessionStorageCondition(mcpServer, metav1.ConditionTrue,
				mcpv1beta1.ConditionReasonSessionStorageMissing,
				"replicas > 1 but sessionStorage.provider is not redis; sessions are not shared across replicas")
		} else {
			setSessionStorageCondition(mcpServer, metav1.ConditionFalse,
				mcpv1beta1.ConditionReasonSessionStorageConfigured,
				"Redis session storage is configured")
		}
	} else {
		setSessionStorageCondition(mcpServer, metav1.ConditionFalse,
			mcpv1beta1.ConditionReasonSessionStorageNotApplicable,
			"session storage warning is not active")
	}
	if err := r.Status().Update(ctx, mcpServer); err != nil {
		log.FromContext(ctx).Error(err, "Failed to update MCPServer status after session storage validation")
	}
}

// setRateLimitConfigCondition sets the RateLimitConfigValid status condition.
func setRateLimitConfigCondition(mcpServer *mcpv1beta1.MCPServer, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&mcpServer.Status.Conditions, metav1.Condition{
		Type:               mcpv1beta1.ConditionRateLimitConfigValid,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: mcpServer.Generation,
	})
}

// validateRateLimitConfig validates that per-user rate limiting has authentication enabled.
// Sets the RateLimitConfigValid condition. This is defense-in-depth only; CEL admission
// validation is the primary gate. Reconciliation continues even when the condition is False
// because per-user buckets are silently skipped when userID is empty (graceful degradation).
func (r *MCPServerReconciler) validateRateLimitConfig(ctx context.Context, mcpServer *mcpv1beta1.MCPServer) {
	rl := mcpServer.Spec.RateLimiting
	if rl == nil {
		setRateLimitConfigCondition(mcpServer, metav1.ConditionTrue,
			mcpv1beta1.ConditionReasonRateLimitNotApplicable,
			"rate limiting is not configured")
		if err := r.Status().Update(ctx, mcpServer); err != nil {
			log.FromContext(ctx).Error(err, "Failed to update MCPServer status after rate limit validation")
		}
		return
	}

	authEnabled := mcpServer.Spec.OIDCConfigRef != nil ||
		mcpServer.Spec.ExternalAuthConfigRef != nil

	hasPerUser := rl.PerUser != nil
	if !hasPerUser {
		for _, t := range rl.Tools {
			if t.PerUser != nil {
				hasPerUser = true
				break
			}
		}
	}

	if hasPerUser && !authEnabled {
		setRateLimitConfigCondition(mcpServer, metav1.ConditionFalse,
			mcpv1beta1.ConditionReasonRateLimitPerUserRequiresAuth,
			"perUser rate limiting requires authentication to be enabled (oidcConfigRef or externalAuthConfigRef)")
	} else {
		setRateLimitConfigCondition(mcpServer, metav1.ConditionTrue,
			mcpv1beta1.ConditionReasonRateLimitConfigValid,
			"rate limit configuration is valid")
	}
	if err := r.Status().Update(ctx, mcpServer); err != nil {
		log.FromContext(ctx).Error(err, "Failed to update MCPServer status after rate limit validation")
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *MCPServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Create a handler that maps MCPExternalAuthConfig changes to MCPServer reconciliation requests
	externalAuthConfigHandler := handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []reconcile.Request {
			externalAuthConfig, ok := obj.(*mcpv1beta1.MCPExternalAuthConfig)
			if !ok {
				return nil
			}

			// List all MCPServers in the same namespace
			mcpServerList := &mcpv1beta1.MCPServerList{}
			if err := r.List(ctx, mcpServerList, client.InNamespace(externalAuthConfig.Namespace)); err != nil {
				log.FromContext(ctx).Error(err, "Failed to list MCPServers for MCPExternalAuthConfig watch")
				return nil
			}

			// Find MCPServers that reference this MCPExternalAuthConfig
			var requests []reconcile.Request
			for _, server := range mcpServerList.Items {
				if (server.Spec.ExternalAuthConfigRef != nil &&
					server.Spec.ExternalAuthConfigRef.Name == externalAuthConfig.Name) ||
					(server.Spec.AuthServerRef != nil &&
						server.Spec.AuthServerRef.Name == externalAuthConfig.Name) {
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

	// Create a handler that maps MCPOIDCConfig changes to MCPServer reconciliation requests
	oidcConfigHandler := handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []reconcile.Request {
			oidcConfig, ok := obj.(*mcpv1beta1.MCPOIDCConfig)
			if !ok {
				return nil
			}

			mcpServerList := &mcpv1beta1.MCPServerList{}
			if err := r.List(ctx, mcpServerList, client.InNamespace(oidcConfig.Namespace)); err != nil {
				log.FromContext(ctx).Error(err, "Failed to list MCPServers for MCPOIDCConfig watch")
				return nil
			}

			var requests []reconcile.Request
			for _, server := range mcpServerList.Items {
				if server.Spec.OIDCConfigRef != nil &&
					server.Spec.OIDCConfigRef.Name == oidcConfig.Name {
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

	telemetryConfigHandler := handler.EnqueueRequestsFromMapFunc(r.mapTelemetryConfigToServers)

	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1beta1.MCPServer{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Watches(&mcpv1beta1.MCPExternalAuthConfig{}, externalAuthConfigHandler).
		Watches(&mcpv1beta1.MCPOIDCConfig{}, oidcConfigHandler).
		Watches(&mcpv1beta1.MCPTelemetryConfig{}, telemetryConfigHandler).
		Complete(r)
}

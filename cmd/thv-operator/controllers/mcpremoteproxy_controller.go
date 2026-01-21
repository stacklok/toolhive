// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package controllers contains the reconciliation logic for the MCPRemoteProxy custom resource.
// It handles the creation, update, and deletion of remote MCP proxies in Kubernetes.
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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
)

// MCPRemoteProxyReconciler reconciles a MCPRemoteProxy object
type MCPRemoteProxyReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	PlatformDetector *ctrlutil.SharedPlatformDetector
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpremoteproxies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpremoteproxies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcptoolconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpexternalauthconfigs,verbs=get;list;watch
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
func (r *MCPRemoteProxyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	// Fetch the MCPRemoteProxy instance
	proxy := &mcpv1alpha1.MCPRemoteProxy{}
	err := r.Get(ctx, req.NamespacedName, proxy)
	if err != nil {
		if errors.IsNotFound(err) {
			ctxLogger.Info("MCPRemoteProxy resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		ctxLogger.Error(err, "Failed to get MCPRemoteProxy")
		return ctrl.Result{}, err
	}

	// Validate and handle configurations
	if err := r.validateAndHandleConfigs(ctx, proxy); err != nil {
		return ctrl.Result{}, err
	}

	// Ensure all resources
	if err := r.ensureAllResources(ctx, proxy); err != nil {
		return ctrl.Result{}, err
	}

	// Update status
	if err := r.updateMCPRemoteProxyStatus(ctx, proxy); err != nil {
		ctxLogger.Error(err, "Failed to update MCPRemoteProxy status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// validateAndHandleConfigs validates spec and handles referenced configurations
func (r *MCPRemoteProxyReconciler) validateAndHandleConfigs(ctx context.Context, proxy *mcpv1alpha1.MCPRemoteProxy) error {
	ctxLogger := log.FromContext(ctx)

	// Validate the spec
	if err := r.validateSpec(ctx, proxy); err != nil {
		ctxLogger.Error(err, "MCPRemoteProxy spec validation failed")
		proxy.Status.Phase = mcpv1alpha1.MCPRemoteProxyPhaseFailed
		proxy.Status.Message = fmt.Sprintf("Validation failed: %v", err)
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:    mcpv1alpha1.ConditionTypeAuthConfigured,
			Status:  metav1.ConditionFalse,
			Reason:  mcpv1alpha1.ConditionReasonAuthInvalid,
			Message: err.Error(),
		})
		if statusErr := r.Status().Update(ctx, proxy); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPRemoteProxy status after validation error")
		}
		return err
	}

	// Validate the GroupRef if specified
	r.validateGroupRef(ctx, proxy)

	// Handle MCPToolConfig
	if err := r.handleToolConfig(ctx, proxy); err != nil {
		ctxLogger.Error(err, "Failed to handle MCPToolConfig")
		proxy.Status.Phase = mcpv1alpha1.MCPRemoteProxyPhaseFailed
		if statusErr := r.Status().Update(ctx, proxy); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPRemoteProxy status after MCPToolConfig error")
		}
		return err
	}

	// Handle MCPExternalAuthConfig
	if err := r.handleExternalAuthConfig(ctx, proxy); err != nil {
		ctxLogger.Error(err, "Failed to handle MCPExternalAuthConfig")
		proxy.Status.Phase = mcpv1alpha1.MCPRemoteProxyPhaseFailed
		if statusErr := r.Status().Update(ctx, proxy); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPRemoteProxy status after MCPExternalAuthConfig error")
		}
		return err
	}

	return nil
}

// ensureAllResources ensures all Kubernetes resources for the proxy
func (r *MCPRemoteProxyReconciler) ensureAllResources(ctx context.Context, proxy *mcpv1alpha1.MCPRemoteProxy) error {
	ctxLogger := log.FromContext(ctx)

	// Ensure RBAC resources
	if err := r.ensureRBACResources(ctx, proxy); err != nil {
		ctxLogger.Error(err, "Failed to ensure RBAC resources")
		return err
	}

	// Ensure authorization ConfigMap
	if err := r.ensureAuthzConfigMapForProxy(ctx, proxy); err != nil {
		ctxLogger.Error(err, "Failed to ensure authorization ConfigMap")
		return err
	}

	// Ensure RunConfig ConfigMap
	if err := r.ensureRunConfigConfigMap(ctx, proxy); err != nil {
		ctxLogger.Error(err, "Failed to ensure RunConfig ConfigMap")
		return err
	}

	// Ensure Deployment
	if result, err := r.ensureDeployment(ctx, proxy); err != nil {
		return err
	} else if result.RequeueAfter > 0 {
		return nil
	}

	// Ensure Service
	if result, err := r.ensureService(ctx, proxy); err != nil {
		return err
	} else if result.RequeueAfter > 0 {
		return nil
	}

	// Update service URL in status
	return r.ensureServiceURL(ctx, proxy)
}

// ensureAuthzConfigMapForProxy ensures the authorization ConfigMap for inline configuration
func (r *MCPRemoteProxyReconciler) ensureAuthzConfigMapForProxy(ctx context.Context, proxy *mcpv1alpha1.MCPRemoteProxy) error {
	authzLabels := labelsForMCPRemoteProxy(proxy.Name)
	authzLabels[authzLabelKey] = authzLabelValueInline
	return ctrlutil.EnsureAuthzConfigMap(
		ctx, r.Client, r.Scheme, proxy, proxy.Namespace, proxy.Name, proxy.Spec.AuthzConfig, authzLabels,
	)
}

// getRunConfigChecksum fetches the RunConfig ConfigMap checksum annotation for this proxy.
// Uses the shared RunConfigChecksumFetcher to maintain consistency with MCPServer.
func (r *MCPRemoteProxyReconciler) getRunConfigChecksum(
	ctx context.Context, proxy *mcpv1alpha1.MCPRemoteProxy,
) (string, error) {
	if proxy == nil {
		return "", fmt.Errorf("proxy cannot be nil")
	}

	fetcher := checksum.NewRunConfigChecksumFetcher(r.Client)
	return fetcher.GetRunConfigChecksum(ctx, proxy.Namespace, proxy.Name)
}

// ensureDeployment ensures the Deployment exists and is up to date.
//
// This function coordinates deployment creation and updates, including:
//   - Fetching the RunConfig ConfigMap checksum for pod restart triggering
//   - Creating deployments when they don't exist
//   - Updating deployments when configuration changes
//   - Preserving replica counts for HPA compatibility
//
// If the RunConfig ConfigMap doesn't exist yet (e.g., during initial resource creation),
// the function returns an error that will trigger reconciliation requeue, allowing the
// ConfigMap to be created first in ensureAllResources().
func (r *MCPRemoteProxyReconciler) ensureDeployment(
	ctx context.Context, proxy *mcpv1alpha1.MCPRemoteProxy,
) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	// Fetch RunConfig ConfigMap checksum to include in pod template annotations
	// This ensures pods restart when configuration changes
	runConfigChecksum, err := r.getRunConfigChecksum(ctx, proxy)
	if err != nil {
		if errors.IsNotFound(err) {
			// ConfigMap doesn't exist yet - it will be created by ensureRunConfigConfigMap
			// before this function is called. If we still hit this, it's likely a timing
			// issue with API server consistency. Requeue with a short delay to allow
			// API server propagation.
			ctxLogger.Info("RunConfig ConfigMap not found yet, will retry",
				"proxy", proxy.Name, "namespace", proxy.Namespace)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		// Other errors (missing annotation, empty checksum, etc.) are real problems
		ctxLogger.Error(err, "Failed to get RunConfig checksum")
		return ctrl.Result{}, err
	}

	deployment := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: proxy.Name, Namespace: proxy.Namespace}, deployment)

	if errors.IsNotFound(err) {
		dep := r.deploymentForMCPRemoteProxy(ctx, proxy, runConfigChecksum)
		if dep == nil {
			return ctrl.Result{}, fmt.Errorf("failed to create Deployment object")
		}
		ctxLogger.Info("Creating a new Deployment", "Deployment.Namespace", dep.Namespace, "Deployment.Name", dep.Name)
		if err := r.Create(ctx, dep); err != nil {
			ctxLogger.Error(err, "Failed to create new Deployment")
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 0}, nil
	} else if err != nil {
		ctxLogger.Error(err, "Failed to get Deployment")
		return ctrl.Result{}, err
	}

	// Deployment exists - check if it needs to be updated
	if r.deploymentNeedsUpdate(ctx, deployment, proxy, runConfigChecksum) {
		newDeployment := r.deploymentForMCPRemoteProxy(ctx, proxy, runConfigChecksum)
		if newDeployment == nil {
			return ctrl.Result{}, fmt.Errorf("failed to create updated Deployment object")
		}
		// Update the deployment spec but preserve replica count for HPA compatibility
		deployment.Spec.Template = newDeployment.Spec.Template
		deployment.Labels = newDeployment.Labels
		deployment.Annotations = newDeployment.Annotations

		ctxLogger.Info("Updating Deployment", "Deployment.Namespace", deployment.Namespace, "Deployment.Name", deployment.Name)
		if err := r.Update(ctx, deployment); err != nil {
			ctxLogger.Error(err, "Failed to update Deployment")
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	return ctrl.Result{}, nil
}

// ensureService ensures the Service exists and is up to date
func (r *MCPRemoteProxyReconciler) ensureService(
	ctx context.Context, proxy *mcpv1alpha1.MCPRemoteProxy,
) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	serviceName := createProxyServiceName(proxy.Name)
	service := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: serviceName, Namespace: proxy.Namespace}, service)

	if errors.IsNotFound(err) {
		svc := r.serviceForMCPRemoteProxy(ctx, proxy)
		if svc == nil {
			return ctrl.Result{}, fmt.Errorf("failed to create Service object")
		}
		ctxLogger.Info("Creating a new Service", "Service.Namespace", svc.Namespace, "Service.Name", svc.Name)
		if err := r.Create(ctx, svc); err != nil {
			ctxLogger.Error(err, "Failed to create new Service")
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 0}, nil
	} else if err != nil {
		ctxLogger.Error(err, "Failed to get Service")
		return ctrl.Result{}, err
	}

	// Service exists - check if it needs to be updated
	if r.serviceNeedsUpdate(service, proxy) {
		newService := r.serviceForMCPRemoteProxy(ctx, proxy)
		if newService == nil {
			return ctrl.Result{}, fmt.Errorf("failed to create updated Service object")
		}
		service.Spec.Ports = newService.Spec.Ports
		service.Labels = newService.Labels
		service.Annotations = newService.Annotations

		ctxLogger.Info("Updating Service", "Service.Namespace", service.Namespace, "Service.Name", service.Name)
		if err := r.Update(ctx, service); err != nil {
			ctxLogger.Error(err, "Failed to update Service")
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	return ctrl.Result{}, nil
}

// ensureServiceURL ensures the service URL is set in the status
func (r *MCPRemoteProxyReconciler) ensureServiceURL(ctx context.Context, proxy *mcpv1alpha1.MCPRemoteProxy) error {
	if proxy.Status.URL == "" {
		// Note: createProxyServiceURL uses the remote-prefixed service name
		proxy.Status.URL = createProxyServiceURL(proxy.Name, proxy.Namespace, int32(proxy.GetProxyPort()))
		return r.Status().Update(ctx, proxy)
	}
	return nil
}

// validateSpec validates the MCPRemoteProxy spec
func (r *MCPRemoteProxyReconciler) validateSpec(ctx context.Context, proxy *mcpv1alpha1.MCPRemoteProxy) error {
	if proxy.Spec.RemoteURL == "" {
		return fmt.Errorf("remoteURL is required")
	}

	// Validate external auth config if referenced
	if proxy.Spec.ExternalAuthConfigRef != nil {
		externalAuthConfig, err := ctrlutil.GetExternalAuthConfigForMCPRemoteProxy(ctx, r.Client, proxy)
		if err != nil {
			return fmt.Errorf("failed to validate external auth config: %w", err)
		}
		if externalAuthConfig == nil {
			return fmt.Errorf("referenced MCPExternalAuthConfig %s not found", proxy.Spec.ExternalAuthConfigRef.Name)
		}
	}

	return nil
}

// handleToolConfig handles MCPToolConfig reference for an MCPRemoteProxy
func (r *MCPRemoteProxyReconciler) handleToolConfig(ctx context.Context, proxy *mcpv1alpha1.MCPRemoteProxy) error {
	ctxLogger := log.FromContext(ctx)
	if proxy.Spec.ToolConfigRef == nil {
		if proxy.Status.ToolConfigHash != "" {
			proxy.Status.ToolConfigHash = ""
			if err := r.Status().Update(ctx, proxy); err != nil {
				return fmt.Errorf("failed to clear MCPToolConfig hash from status: %w", err)
			}
		}
		return nil
	}

	toolConfig, err := ctrlutil.GetToolConfigForMCPRemoteProxy(ctx, r.Client, proxy)
	if err != nil {
		return err
	}

	if toolConfig == nil {
		return fmt.Errorf("MCPToolConfig %s not found", proxy.Spec.ToolConfigRef.Name)
	}

	if proxy.Status.ToolConfigHash != toolConfig.Status.ConfigHash {
		ctxLogger.Info("MCPToolConfig has changed, updating MCPRemoteProxy",
			"proxy", proxy.Name,
			"toolconfig", toolConfig.Name,
			"oldHash", proxy.Status.ToolConfigHash,
			"newHash", toolConfig.Status.ConfigHash)

		proxy.Status.ToolConfigHash = toolConfig.Status.ConfigHash
		if err := r.Status().Update(ctx, proxy); err != nil {
			return fmt.Errorf("failed to update MCPToolConfig hash in status: %w", err)
		}
	}

	return nil
}

// handleExternalAuthConfig validates and tracks the hash of the referenced MCPExternalAuthConfig
func (r *MCPRemoteProxyReconciler) handleExternalAuthConfig(ctx context.Context, proxy *mcpv1alpha1.MCPRemoteProxy) error {
	ctxLogger := log.FromContext(ctx)
	if proxy.Spec.ExternalAuthConfigRef == nil {
		if proxy.Status.ExternalAuthConfigHash != "" {
			proxy.Status.ExternalAuthConfigHash = ""
			if err := r.Status().Update(ctx, proxy); err != nil {
				return fmt.Errorf("failed to clear MCPExternalAuthConfig hash from status: %w", err)
			}
		}
		return nil
	}

	externalAuthConfig, err := ctrlutil.GetExternalAuthConfigForMCPRemoteProxy(ctx, r.Client, proxy)
	if err != nil {
		return err
	}

	if externalAuthConfig == nil {
		return fmt.Errorf("MCPExternalAuthConfig %s not found", proxy.Spec.ExternalAuthConfigRef.Name)
	}

	if proxy.Status.ExternalAuthConfigHash != externalAuthConfig.Status.ConfigHash {
		ctxLogger.Info("MCPExternalAuthConfig has changed, updating MCPRemoteProxy",
			"proxy", proxy.Name,
			"externalAuthConfig", externalAuthConfig.Name,
			"oldHash", proxy.Status.ExternalAuthConfigHash,
			"newHash", externalAuthConfig.Status.ConfigHash)

		proxy.Status.ExternalAuthConfigHash = externalAuthConfig.Status.ConfigHash
		if err := r.Status().Update(ctx, proxy); err != nil {
			return fmt.Errorf("failed to update MCPExternalAuthConfig hash in status: %w", err)
		}
	}

	return nil
}

// validateGroupRef validates the GroupRef field of the MCPRemoteProxy.
// This function only sets conditions on the proxy object - the caller is responsible
// for persisting the status update to avoid multiple conflicting status updates.
func (r *MCPRemoteProxyReconciler) validateGroupRef(ctx context.Context, proxy *mcpv1alpha1.MCPRemoteProxy) {
	if proxy.Spec.GroupRef == "" {
		// No group reference - remove any existing GroupRefValidated condition
		// to avoid showing stale info from a previous reconciliation
		meta.RemoveStatusCondition(&proxy.Status.Conditions, mcpv1alpha1.ConditionTypeMCPRemoteProxyGroupRefValidated)
		return
	}

	ctxLogger := log.FromContext(ctx)

	// Find the referenced MCPGroup
	group := &mcpv1alpha1.MCPGroup{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: proxy.Namespace, Name: proxy.Spec.GroupRef}, group); err != nil {
		ctxLogger.Error(err, "Failed to validate GroupRef")
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionTypeMCPRemoteProxyGroupRefValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1alpha1.ConditionReasonMCPRemoteProxyGroupRefNotFound,
			Message:            fmt.Sprintf("MCPGroup '%s' not found in namespace '%s'", proxy.Spec.GroupRef, proxy.Namespace),
			ObservedGeneration: proxy.Generation,
		})
	} else if group.Status.Phase != mcpv1alpha1.MCPGroupPhaseReady {
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionTypeMCPRemoteProxyGroupRefValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1alpha1.ConditionReasonMCPRemoteProxyGroupRefNotReady,
			Message:            fmt.Sprintf("MCPGroup '%s' is not ready (current phase: %s)", proxy.Spec.GroupRef, group.Status.Phase),
			ObservedGeneration: proxy.Generation,
		})
	} else {
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionTypeMCPRemoteProxyGroupRefValidated,
			Status:             metav1.ConditionTrue,
			Reason:             mcpv1alpha1.ConditionReasonMCPRemoteProxyGroupRefValidated,
			Message:            fmt.Sprintf("MCPGroup '%s' is valid and ready", proxy.Spec.GroupRef),
			ObservedGeneration: proxy.Generation,
		})
	}
}

// ensureRBACResources ensures that the RBAC resources are in place for the remote proxy
// TODO: This uses EnsureRBACResource which only creates RBAC but never updates them.
// Consider adopting the MCPRegistry pattern (pkg/registryapi/rbac.go) which uses
// CreateOrUpdate + RetryOnConflict to automatically update RBAC rules during operator upgrades.
func (r *MCPRemoteProxyReconciler) ensureRBACResources(ctx context.Context, proxy *mcpv1alpha1.MCPRemoteProxy) error {
	proxyRunnerNameForRBAC := proxyRunnerServiceAccountNameForRemoteProxy(proxy.Name)

	// Ensure Role with minimal permissions for remote proxies
	// Remote proxies only need ConfigMap and Secret read access (no StatefulSet/Pod management)
	if err := ctrlutil.EnsureRBACResource(ctx, r.Client, r.Scheme, proxy, "Role", func() client.Object {
		return &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      proxyRunnerNameForRBAC,
				Namespace: proxy.Namespace,
			},
			Rules: remoteProxyRBACRules,
		}
	}); err != nil {
		return err
	}

	// Ensure ServiceAccount
	if err := ctrlutil.EnsureRBACResource(ctx, r.Client, r.Scheme, proxy, "ServiceAccount", func() client.Object {
		return &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      proxyRunnerNameForRBAC,
				Namespace: proxy.Namespace,
			},
		}
	}); err != nil {
		return err
	}

	// Ensure RoleBinding
	return ctrlutil.EnsureRBACResource(ctx, r.Client, r.Scheme, proxy, "RoleBinding", func() client.Object {
		return &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      proxyRunnerNameForRBAC,
				Namespace: proxy.Namespace,
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
					Namespace: proxy.Namespace,
				},
			},
		}
	})
}

// updateMCPRemoteProxyStatus updates the status of the MCPRemoteProxy
func (r *MCPRemoteProxyReconciler) updateMCPRemoteProxyStatus(ctx context.Context, proxy *mcpv1alpha1.MCPRemoteProxy) error {
	// List the pods for this MCPRemoteProxy's deployment
	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(proxy.Namespace),
		client.MatchingLabels(labelsForMCPRemoteProxy(proxy.Name)),
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

	// Update the status
	if running > 0 {
		proxy.Status.Phase = mcpv1alpha1.MCPRemoteProxyPhaseReady
		proxy.Status.Message = "Remote proxy is running"
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:    mcpv1alpha1.ConditionTypeReady,
			Status:  metav1.ConditionTrue,
			Reason:  mcpv1alpha1.ConditionReasonDeploymentReady,
			Message: "Deployment is ready and running",
		})
	} else if pending > 0 {
		proxy.Status.Phase = mcpv1alpha1.MCPRemoteProxyPhasePending
		proxy.Status.Message = "Remote proxy is starting"
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:    mcpv1alpha1.ConditionTypeReady,
			Status:  metav1.ConditionFalse,
			Reason:  mcpv1alpha1.ConditionReasonDeploymentNotReady,
			Message: "Deployment is not yet ready",
		})
	} else if failed > 0 {
		proxy.Status.Phase = mcpv1alpha1.MCPRemoteProxyPhaseFailed
		proxy.Status.Message = "Remote proxy failed to start"
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:    mcpv1alpha1.ConditionTypeReady,
			Status:  metav1.ConditionFalse,
			Reason:  mcpv1alpha1.ConditionReasonDeploymentNotReady,
			Message: "Deployment failed",
		})
	} else {
		proxy.Status.Phase = mcpv1alpha1.MCPRemoteProxyPhasePending
		proxy.Status.Message = "No pods found for remote proxy"
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:    mcpv1alpha1.ConditionTypeReady,
			Status:  metav1.ConditionFalse,
			Reason:  mcpv1alpha1.ConditionReasonDeploymentNotReady,
			Message: "No pods found",
		})
	}

	// Update ObservedGeneration to reflect that we've processed this generation
	proxy.Status.ObservedGeneration = proxy.Generation

	return r.Status().Update(ctx, proxy)
}

// labelsForMCPRemoteProxy returns the labels for selecting the resources belonging to the given MCPRemoteProxy CR name
func labelsForMCPRemoteProxy(name string) map[string]string {
	return map[string]string{
		"app":                        "mcpremoteproxy",
		"app.kubernetes.io/name":     "mcpremoteproxy",
		"app.kubernetes.io/instance": name,
		"toolhive":                   "true",
		"toolhive-name":              name,
	}
}

// proxyRunnerServiceAccountNameForRemoteProxy returns the service account name for the proxy runner
// Uses "remote-" prefix to avoid conflicts with MCPServer resources of the same name
func proxyRunnerServiceAccountNameForRemoteProxy(proxyName string) string {
	return fmt.Sprintf("%s-remote-proxy-runner", proxyName)
}

// createProxyServiceName generates the service name for a remote proxy
// Uses "remote-" prefix to avoid conflicts with MCPServer resources of the same name
func createProxyServiceName(proxyName string) string {
	return fmt.Sprintf("mcp-%s-remote-proxy", proxyName)
}

// createProxyServiceURL generates the full cluster-local service URL for a remote proxy
func createProxyServiceURL(proxyName, namespace string, port int32) string {
	serviceName := createProxyServiceName(proxyName)
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", serviceName, namespace, port)
}

// deploymentNeedsUpdate checks if the deployment needs to be updated based on spec changes.
//
// This function compares the existing deployment with the desired state derived from the
// MCPRemoteProxy spec. It checks container specs, deployment metadata, and pod template
// metadata (including the RunConfig checksum annotation).
//
// Returns true if any aspect of the deployment differs from the desired state.
func (r *MCPRemoteProxyReconciler) deploymentNeedsUpdate(
	ctx context.Context,
	deployment *appsv1.Deployment,
	proxy *mcpv1alpha1.MCPRemoteProxy,
	runConfigChecksum string,
) bool {
	if deployment == nil || proxy == nil {
		return true
	}

	if len(deployment.Spec.Template.Spec.Containers) == 0 {
		return true
	}

	if r.containerNeedsUpdate(ctx, deployment, proxy) {
		return true
	}

	if r.deploymentMetadataNeedsUpdate(deployment, proxy) {
		return true
	}

	if r.podTemplateMetadataNeedsUpdate(deployment, proxy, runConfigChecksum) {
		return true
	}

	return false
}

// containerNeedsUpdate checks if the container specification has changed.
//
// Compares container image, ports, environment variables, resource requirements,
// and service account between the existing deployment and desired state.
func (r *MCPRemoteProxyReconciler) containerNeedsUpdate(
	ctx context.Context,
	deployment *appsv1.Deployment,
	proxy *mcpv1alpha1.MCPRemoteProxy,
) bool {
	if deployment == nil || proxy == nil || len(deployment.Spec.Template.Spec.Containers) == 0 {
		return true
	}

	container := deployment.Spec.Template.Spec.Containers[0]

	// Check if runner image has changed
	if container.Image != getToolhiveRunnerImage() {
		return true
	}

	// Check if port has changed
	if len(container.Ports) > 0 && container.Ports[0].ContainerPort != int32(proxy.GetProxyPort()) {
		return true
	}

	// Check if environment variables have changed
	expectedEnv := r.buildEnvVarsForProxy(ctx, proxy)
	if !reflect.DeepEqual(container.Env, expectedEnv) {
		return true
	}

	// Check if resources have changed
	expectedResources := ctrlutil.BuildResourceRequirements(proxy.Spec.Resources)
	if !reflect.DeepEqual(container.Resources, expectedResources) {
		return true
	}

	// Check if service account has changed
	expectedServiceAccountName := proxyRunnerServiceAccountNameForRemoteProxy(proxy.Name)
	currentServiceAccountName := deployment.Spec.Template.Spec.ServiceAccountName
	if currentServiceAccountName != "" && currentServiceAccountName != expectedServiceAccountName {
		return true
	}

	return false
}

// deploymentMetadataNeedsUpdate checks if deployment-level metadata has changed.
//
// Compares deployment labels and annotations, including any user-specified overrides
// from ResourceOverrides.ProxyDeployment.
func (*MCPRemoteProxyReconciler) deploymentMetadataNeedsUpdate(
	deployment *appsv1.Deployment,
	proxy *mcpv1alpha1.MCPRemoteProxy,
) bool {
	if deployment == nil || proxy == nil {
		return true
	}

	expectedLabels := labelsForMCPRemoteProxy(proxy.Name)
	expectedAnnotations := make(map[string]string)

	if proxy.Spec.ResourceOverrides != nil && proxy.Spec.ResourceOverrides.ProxyDeployment != nil {
		if proxy.Spec.ResourceOverrides.ProxyDeployment.Labels != nil {
			expectedLabels = ctrlutil.MergeLabels(expectedLabels, proxy.Spec.ResourceOverrides.ProxyDeployment.Labels)
		}
		if proxy.Spec.ResourceOverrides.ProxyDeployment.Annotations != nil {
			expectedAnnotations = ctrlutil.MergeAnnotations(
				make(map[string]string),
				proxy.Spec.ResourceOverrides.ProxyDeployment.Annotations,
			)
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

// podTemplateMetadataNeedsUpdate checks if pod template metadata has changed.
//
// Compares pod template labels and annotations, including the critical RunConfig
// checksum annotation that triggers pod restarts when configuration changes.
// Also includes any user-specified overrides from ResourceOverrides.PodTemplateMetadata.
func (r *MCPRemoteProxyReconciler) podTemplateMetadataNeedsUpdate(
	deployment *appsv1.Deployment,
	proxy *mcpv1alpha1.MCPRemoteProxy,
	runConfigChecksum string,
) bool {
	if deployment == nil || proxy == nil {
		return true
	}

	expectedPodTemplateLabels, expectedPodTemplateAnnotations := r.buildPodTemplateMetadata(
		labelsForMCPRemoteProxy(proxy.Name), proxy, runConfigChecksum,
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
func (*MCPRemoteProxyReconciler) serviceNeedsUpdate(service *corev1.Service, proxy *mcpv1alpha1.MCPRemoteProxy) bool {
	// Check if port has changed
	if len(service.Spec.Ports) > 0 && service.Spec.Ports[0].Port != int32(proxy.GetProxyPort()) {
		return true
	}

	// Check if service metadata has changed
	expectedLabels := labelsForMCPRemoteProxy(proxy.Name)
	expectedAnnotations := make(map[string]string)

	if proxy.Spec.ResourceOverrides != nil && proxy.Spec.ResourceOverrides.ProxyService != nil {
		if proxy.Spec.ResourceOverrides.ProxyService.Labels != nil {
			expectedLabels = ctrlutil.MergeLabels(expectedLabels, proxy.Spec.ResourceOverrides.ProxyService.Labels)
		}
		if proxy.Spec.ResourceOverrides.ProxyService.Annotations != nil {
			expectedAnnotations = ctrlutil.MergeAnnotations(make(map[string]string), proxy.Spec.ResourceOverrides.ProxyService.Annotations)
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

// SetupWithManager sets up the controller with the Manager
func (r *MCPRemoteProxyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Create a handler that maps MCPExternalAuthConfig changes to MCPRemoteProxy reconciliation requests
	externalAuthConfigHandler := handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []reconcile.Request {
			externalAuthConfig, ok := obj.(*mcpv1alpha1.MCPExternalAuthConfig)
			if !ok {
				return nil
			}

			// List all MCPRemoteProxies in the same namespace
			proxyList := &mcpv1alpha1.MCPRemoteProxyList{}
			if err := r.List(ctx, proxyList, client.InNamespace(externalAuthConfig.Namespace)); err != nil {
				log.FromContext(ctx).Error(err, "Failed to list MCPRemoteProxies for MCPExternalAuthConfig watch")
				return nil
			}

			// Find MCPRemoteProxies that reference this MCPExternalAuthConfig
			var requests []reconcile.Request
			for _, proxy := range proxyList.Items {
				if proxy.Spec.ExternalAuthConfigRef != nil &&
					proxy.Spec.ExternalAuthConfigRef.Name == externalAuthConfig.Name {
					requests = append(requests, reconcile.Request{
						NamespacedName: types.NamespacedName{
							Name:      proxy.Name,
							Namespace: proxy.Namespace,
						},
					})
				}
			}

			return requests
		},
	)

	// Create a handler that maps MCPToolConfig changes to MCPRemoteProxy reconciliation requests
	toolConfigHandler := handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []reconcile.Request {
			toolConfig, ok := obj.(*mcpv1alpha1.MCPToolConfig)
			if !ok {
				return nil
			}

			// List all MCPRemoteProxies in the same namespace
			proxyList := &mcpv1alpha1.MCPRemoteProxyList{}
			if err := r.List(ctx, proxyList, client.InNamespace(toolConfig.Namespace)); err != nil {
				log.FromContext(ctx).Error(err, "Failed to list MCPRemoteProxies for MCPToolConfig watch")
				return nil
			}

			// Find MCPRemoteProxies that reference this MCPToolConfig
			var requests []reconcile.Request
			for _, proxy := range proxyList.Items {
				if proxy.Spec.ToolConfigRef != nil &&
					proxy.Spec.ToolConfigRef.Name == toolConfig.Name {
					requests = append(requests, reconcile.Request{
						NamespacedName: types.NamespacedName{
							Name:      proxy.Name,
							Namespace: proxy.Namespace,
						},
					})
				}
			}

			return requests
		},
	)

	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.MCPRemoteProxy{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Watches(&mcpv1alpha1.MCPExternalAuthConfig{}, externalAuthConfigHandler).
		Watches(&mcpv1alpha1.MCPToolConfig{}, toolConfigHandler).
		Complete(r)
}

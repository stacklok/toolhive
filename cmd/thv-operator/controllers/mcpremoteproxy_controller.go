// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package controllers contains the reconciliation logic for the MCPRemoteProxy custom resource.
// It handles the creation, update, and deletion of remote MCP proxies in Kubernetes.
package controllers

import (
	"context"
	stderrors "errors"
	"fmt"
	"maps"
	"reflect"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/imagepullsecrets"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/kubernetes/rbac"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/validation"
)

// MCPRemoteProxyReconciler reconciles a MCPRemoteProxy object
type MCPRemoteProxyReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Recorder         events.EventRecorder
	PlatformDetector *ctrlutil.SharedPlatformDetector
	// ImagePullSecretsDefaults are cluster-wide defaults sourced from the
	// operator chart that are merged with the per-CR imagePullSecrets when
	// constructing workloads. The zero value is a usable empty Defaults.
	ImagePullSecretsDefaults imagepullsecrets.Defaults
}

var errInvalidMCPRemoteProxyPodTemplateSpec = stderrors.New("invalid MCPRemoteProxy PodTemplateSpec")

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpremoteproxies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpremoteproxies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcptoolconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpexternalauthconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpoidcconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcptelemetryconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpauthzconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups="",resources=services,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=roles,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=rolebindings,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=create;delete;get;list;patch;update;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *MCPRemoteProxyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	// Fetch the MCPRemoteProxy instance
	proxy := &mcpv1beta1.MCPRemoteProxy{}
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
		if stderrors.Is(err, errInvalidMCPRemoteProxyPodTemplateSpec) {
			return ctrl.Result{}, nil
		}
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
func (r *MCPRemoteProxyReconciler) validateAndHandleConfigs(ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy) error {
	ctxLogger := log.FromContext(ctx)

	if err := r.validateSpecAndPodTemplate(ctx, proxy); err != nil {
		return err
	}

	// Validate the GroupRef if specified
	r.validateGroupRef(ctx, proxy)

	// Validate the OIDC CA bundle reference if specified
	r.validateCABundleRef(ctx, proxy)

	// Surface advisory condition when primaryUpstreamProvider is set but ignored
	r.validateAuthzPrimaryUpstreamProviderIgnored(proxy)

	// Surface advisory condition when replicas > 1 without Redis session storage
	r.validateSessionStorageForReplicas(proxy)

	// Handle MCPToolConfig
	if err := r.handleToolConfig(ctx, proxy); err != nil {
		ctxLogger.Error(err, "Failed to handle MCPToolConfig")
		proxy.Status.Phase = mcpv1beta1.MCPRemoteProxyPhaseFailed
		if statusErr := r.Status().Update(ctx, proxy); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPRemoteProxy status after MCPToolConfig error")
		}
		return err
	}

	// Handle MCPTelemetryConfig
	if err := r.handleTelemetryConfig(ctx, proxy); err != nil {
		ctxLogger.Error(err, "Failed to handle MCPTelemetryConfig")
		proxy.Status.Phase = mcpv1beta1.MCPRemoteProxyPhaseFailed
		if statusErr := r.Status().Update(ctx, proxy); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPRemoteProxy status after MCPTelemetryConfig error")
		}
		return err
	}

	// Handle MCPExternalAuthConfig
	if err := r.handleExternalAuthConfig(ctx, proxy); err != nil {
		ctxLogger.Error(err, "Failed to handle MCPExternalAuthConfig")
		proxy.Status.Phase = mcpv1beta1.MCPRemoteProxyPhaseFailed
		if statusErr := r.Status().Update(ctx, proxy); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPRemoteProxy status after MCPExternalAuthConfig error")
		}
		return err
	}

	// Handle authServerRef config hash tracking
	if err := r.handleAuthServerRef(ctx, proxy); err != nil {
		ctxLogger.Error(err, "Failed to handle authServerRef")
		proxy.Status.Phase = mcpv1beta1.MCPRemoteProxyPhaseFailed
		if statusErr := r.Status().Update(ctx, proxy); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPRemoteProxy status after authServerRef error")
		}
		return err
	}

	// Handle MCPOIDCConfig
	if err := r.handleOIDCConfig(ctx, proxy); err != nil {
		ctxLogger.Error(err, "Failed to handle MCPOIDCConfig")
		proxy.Status.Phase = mcpv1beta1.MCPRemoteProxyPhaseFailed
		if statusErr := r.Status().Update(ctx, proxy); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPRemoteProxy status after MCPOIDCConfig error")
		}
		return err
	}

	// Handle MCPAuthzConfig
	if err := r.handleAuthzConfig(ctx, proxy); err != nil {
		ctxLogger.Error(err, "Failed to handle MCPAuthzConfig")
		proxy.Status.Phase = mcpv1beta1.MCPRemoteProxyPhaseFailed
		if statusErr := r.Status().Update(ctx, proxy); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPRemoteProxy status after MCPAuthzConfig error")
		}
		return err
	}

	return nil
}

func (r *MCPRemoteProxyReconciler) validateSpecAndPodTemplate(
	ctx context.Context,
	proxy *mcpv1beta1.MCPRemoteProxy,
) error {
	ctxLogger := log.FromContext(ctx)

	if err := r.validateSpec(ctx, proxy); err != nil {
		ctxLogger.Error(err, "MCPRemoteProxy spec validation failed")
		proxy.Status.Phase = mcpv1beta1.MCPRemoteProxyPhaseFailed
		proxy.Status.Message = fmt.Sprintf("Validation failed: %v", err)
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:    mcpv1beta1.ConditionTypeAuthConfigured,
			Status:  metav1.ConditionFalse,
			Reason:  mcpv1beta1.ConditionReasonAuthInvalid,
			Message: err.Error(),
		})
		if statusErr := r.Status().Update(ctx, proxy); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPRemoteProxy status after validation error")
		}
		return err
	}

	if !r.validateAndUpdatePodTemplateStatus(ctx, proxy) {
		return errInvalidMCPRemoteProxyPodTemplateSpec
	}

	return nil
}

// validateAndUpdatePodTemplateStatus validates the PodTemplateSpec and updates status conditions.
// It returns false when the PodTemplateSpec is invalid and reconciliation should stop until the user fixes it.
func (r *MCPRemoteProxyReconciler) validateAndUpdatePodTemplateStatus(
	ctx context.Context,
	proxy *mcpv1beta1.MCPRemoteProxy,
) bool {
	ctxLogger := log.FromContext(ctx)

	if proxy.Spec.PodTemplateSpec == nil || proxy.Spec.PodTemplateSpec.Raw == nil {
		return true
	}

	_, err := ctrlutil.NewPodTemplateSpecBuilder(proxy.Spec.PodTemplateSpec, mcpRemoteProxyContainerName)
	if err != nil {
		if r.Recorder != nil {
			r.Recorder.Eventf(proxy, nil, corev1.EventTypeWarning, "InvalidPodTemplateSpec", "ValidatePodTemplateSpec",
				"Failed to parse PodTemplateSpec: %v. Deployment blocked until PodTemplateSpec is fixed.", err)
		}

		proxy.Status.Phase = mcpv1beta1.MCPRemoteProxyPhaseFailed
		proxy.Status.Message = fmt.Sprintf("Invalid PodTemplateSpec: %v", err)
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionTypeMCPRemoteProxyPodTemplateValid,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: proxy.Generation,
			Reason:             mcpv1beta1.ConditionReasonMCPRemoteProxyPodTemplateInvalid,
			Message:            fmt.Sprintf("Failed to parse PodTemplateSpec: %v. Deployment blocked until fixed.", err),
		})
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionTypeReady,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: proxy.Generation,
			Reason:             mcpv1beta1.ConditionReasonDeploymentNotReady,
			Message:            fmt.Sprintf("Invalid PodTemplateSpec: %v", err),
		})

		if statusErr := r.Status().Update(ctx, proxy); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPRemoteProxy status with PodTemplateSpec validation")
			return false
		}

		ctxLogger.Error(err, "PodTemplateSpec validation failed")
		return false
	}

	meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
		Type:               mcpv1beta1.ConditionTypeMCPRemoteProxyPodTemplateValid,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: proxy.Generation,
		Reason:             mcpv1beta1.ConditionReasonMCPRemoteProxyPodTemplateValid,
		Message:            "PodTemplateSpec is valid",
	})
	if statusErr := r.Status().Update(ctx, proxy); statusErr != nil {
		ctxLogger.Error(statusErr, "Failed to update MCPRemoteProxy status with PodTemplateSpec validation")
	}

	return true
}

// ensureAllResources ensures all Kubernetes resources for the proxy
func (r *MCPRemoteProxyReconciler) ensureAllResources(ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy) error {
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

// ensureAuthzConfigMapForProxy ensures the authorization ConfigMap exists for inline
// configuration (spec.authzConfig). A referenced MCPAuthzConfig
// (spec.authzConfigRef) is not materialized into a ConfigMap: it is enforced by
// embedding the resolved authz config directly in the RunConfig (see
// AddAuthzConfigRefOptions), which is the path the proxy actually reads.
func (r *MCPRemoteProxyReconciler) ensureAuthzConfigMapForProxy(ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy) error {
	authzLabels := labelsForMCPRemoteProxy(proxy.Name)
	authzLabels[authzLabelKey] = authzLabelValueInline
	return ctrlutil.EnsureAuthzConfigMap(
		ctx, r.Client, r.Scheme, proxy, proxy.Namespace, proxy.Name, proxy.Spec.AuthzConfig, authzLabels,
	)
}

// getRunConfigChecksum fetches the RunConfig ConfigMap checksum annotation for this proxy.
// Uses the shared RunConfigChecksumFetcher to maintain consistency with MCPServer.
func (r *MCPRemoteProxyReconciler) getRunConfigChecksum(
	ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy,
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
	ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy,
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
		// Update template and metadata. Also sync Spec.Replicas when spec.replicas
		// is non-nil (operator authoritative); preserve it when nil so an HPA or
		// other external controller can manage scaling.
		deployment.Spec.Template = newDeployment.Spec.Template
		deployment.Labels = newDeployment.Labels
		deployment.Annotations = ctrlutil.MergeAnnotations(newDeployment.Annotations, deployment.Annotations)

		if proxy.Spec.PodTemplateSpec == nil || len(proxy.Spec.PodTemplateSpec.Raw) == 0 {
			delete(deployment.Annotations, podTemplateSpecHashAnnotation)
		}

		if newDeployment.Spec.Replicas != nil {
			deployment.Spec.Replicas = newDeployment.Spec.Replicas
		}

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
	ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy,
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
		service.Spec.SessionAffinity = newService.Spec.SessionAffinity
		// Merge (not replace) Labels/Annotations so keys written by external controllers
		// (e.g. GKE NEG's cloud.google.com/* annotations) are preserved while the
		// operator-owned values are applied.
		service.Labels = ctrlutil.MergeLabels(newService.Labels, service.Labels)
		service.Annotations = ctrlutil.MergeAnnotations(newService.Annotations, service.Annotations)

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
func (r *MCPRemoteProxyReconciler) ensureServiceURL(ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy) error {
	if proxy.Status.URL == "" {
		// Note: createProxyServiceURL uses the remote-prefixed service name
		proxy.Status.URL = createProxyServiceURL(proxy.Name, proxy.Namespace, int32(proxy.GetProxyPort()))
		return r.Status().Update(ctx, proxy)
	}
	return nil
}

// validateSpec validates the MCPRemoteProxy spec
func (r *MCPRemoteProxyReconciler) validateSpec(ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy) error {
	// Validate external auth config if referenced
	if proxy.Spec.ExternalAuthConfigRef != nil {
		externalAuthConfig, err := ctrlutil.GetExternalAuthConfigForMCPRemoteProxy(ctx, r.Client, proxy)
		if err != nil {
			return r.failValidation(proxy,
				mcpv1beta1.ConditionReasonMCPRemoteProxyExternalAuthConfigFetchError,
				fmt.Errorf("failed to validate external auth config: %w", err),
			)
		}
		if externalAuthConfig == nil {
			return r.failValidation(proxy,
				mcpv1beta1.ConditionReasonMCPRemoteProxyExternalAuthConfigNotFound,
				fmt.Errorf("referenced MCPExternalAuthConfig %s not found", proxy.Spec.ExternalAuthConfigRef.Name),
			)
		}
	}

	// Validate remote URL format (also rejects empty URLs)
	if err := validation.ValidateRemoteURL(proxy.Spec.RemoteURL); err != nil {
		return r.failValidation(proxy, mcpv1beta1.ConditionReasonRemoteURLInvalid, err)
	}

	// Validate inline Cedar policy syntax
	if err := r.validateAuthzPolicySyntax(proxy); err != nil {
		return r.failValidation(proxy, mcpv1beta1.ConditionReasonAuthzPolicySyntaxInvalid, err)
	}

	// Validate Kubernetes resource references (ConfigMaps, Secrets)
	if err := r.validateK8sRefs(ctx, proxy); err != nil {
		return err
	}

	// All validations passed
	meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
		Type:               mcpv1beta1.ConditionTypeConfigurationValid,
		Status:             metav1.ConditionTrue,
		Reason:             mcpv1beta1.ConditionReasonConfigurationValid,
		Message:            "All configuration validations passed",
		ObservedGeneration: proxy.Generation,
	})

	return nil
}

// failValidation records a validation event, sets the ConfigurationValid condition to False,
// and returns the error. This consolidates the repeated validate → event → condition → return pattern.
func (r *MCPRemoteProxyReconciler) failValidation(proxy *mcpv1beta1.MCPRemoteProxy, reason string, err error) error {
	r.recordValidationEvent(proxy, reason, err.Error())
	setConfigurationInvalidCondition(proxy, reason, err.Error())
	return err
}

// recordValidationEvent emits a Warning event for a validation failure.
func (r *MCPRemoteProxyReconciler) recordValidationEvent(proxy *mcpv1beta1.MCPRemoteProxy, reason, message string) {
	if r.Recorder != nil {
		r.Recorder.Eventf(proxy, nil, corev1.EventTypeWarning, reason, "ValidateSpec", message)
	}
}

// setConfigurationInvalidCondition sets the ConfigurationValid condition to False.
func setConfigurationInvalidCondition(proxy *mcpv1beta1.MCPRemoteProxy, reason, message string) {
	meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
		Type:               mcpv1beta1.ConditionTypeConfigurationValid,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: proxy.Generation,
	})
}

// validateAuthzPolicySyntax validates inline Cedar authorization policy syntax.
func (*MCPRemoteProxyReconciler) validateAuthzPolicySyntax(
	proxy *mcpv1beta1.MCPRemoteProxy,
) error {
	if proxy.Spec.AuthzConfig == nil ||
		proxy.Spec.AuthzConfig.Type != mcpv1beta1.AuthzConfigTypeInline ||
		proxy.Spec.AuthzConfig.Inline == nil {
		return nil
	}
	return validation.ValidateCedarPolicies(proxy.Spec.AuthzConfig.Inline.Policies)
}

// validateK8sRefs validates that referenced ConfigMaps and Secrets exist.
// Authz ConfigMap references are validated through the shared loader so a malformed
// payload surfaces as AuthzConfigMapInvalid here instead of crashing the runner pod.
func (r *MCPRemoteProxyReconciler) validateK8sRefs(
	ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy,
) error {
	// Check authz ConfigMap reference.
	if proxy.Spec.AuthzConfig != nil &&
		proxy.Spec.AuthzConfig.Type == mcpv1beta1.AuthzConfigTypeConfigMap &&
		proxy.Spec.AuthzConfig.ConfigMap != nil {
		cmName := proxy.Spec.AuthzConfig.ConfigMap.Name
		cfg, err := ctrlutil.LoadAuthzConfigFromConfigMap(ctx, r.Client, proxy.Namespace, proxy.Spec.AuthzConfig)
		if err == nil {
			if _, cerr := ctrlutil.ExtractCedarAuthzOptions(cfg); cerr != nil {
				err = fmt.Errorf("authz ConfigMap %q is not a Cedar config: %w", cmName, cerr)
			}
		}
		if err != nil {
			reason := mcpv1beta1.ConditionReasonAuthzConfigMapInvalid
			msg := fmt.Sprintf("authorization ConfigMap %q is invalid: %v", cmName, err)
			if errors.IsNotFound(err) {
				reason = mcpv1beta1.ConditionReasonAuthzConfigMapNotFound
				msg = fmt.Sprintf("authorization ConfigMap %q not found in namespace %q", cmName, proxy.Namespace)
			} else {
				log.FromContext(ctx).Error(err, "Authz ConfigMap validation failed", "name", cmName, "namespace", proxy.Namespace)
			}
			r.recordValidationEvent(proxy, reason, msg)
			setConfigurationInvalidCondition(proxy, reason, msg)
			return stderrors.New(msg)
		}
	}

	// Check header Secret references
	if proxy.Spec.HeaderForward != nil {
		for _, headerRef := range proxy.Spec.HeaderForward.AddHeadersFromSecret {
			if headerRef.ValueSecretRef == nil {
				continue
			}
			secret := &corev1.Secret{}
			secretName := headerRef.ValueSecretRef.Name
			err := r.Get(ctx, types.NamespacedName{
				Name: secretName, Namespace: proxy.Namespace,
			}, secret)
			if err != nil {
				if errors.IsNotFound(err) {
					msg := fmt.Sprintf(
						"secret %q referenced for header %q not found in namespace %q",
						secretName, headerRef.HeaderName, proxy.Namespace,
					)
					r.recordValidationEvent(
						proxy,
						mcpv1beta1.ConditionReasonHeaderSecretNotFound,
						msg,
					)
					setConfigurationInvalidCondition(
						proxy,
						mcpv1beta1.ConditionReasonHeaderSecretNotFound,
						msg,
					)
					return stderrors.New(msg)
				}
				ctxLogger := log.FromContext(ctx)
				ctxLogger.Error(err, "Failed to fetch secret", "name", secretName, "namespace", proxy.Namespace)
				genericMsg := fmt.Sprintf("failed to fetch secret %q for header %q", secretName, headerRef.HeaderName)
				r.recordValidationEvent(proxy, mcpv1beta1.ConditionReasonHeaderSecretNotFound, genericMsg)
				setConfigurationInvalidCondition(proxy, mcpv1beta1.ConditionReasonHeaderSecretNotFound, genericMsg)
				return stderrors.New(genericMsg)
			}
		}
	}

	return nil
}

// handleToolConfig handles MCPToolConfig reference for an MCPRemoteProxy
func (r *MCPRemoteProxyReconciler) handleToolConfig(ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy) error {
	ctxLogger := log.FromContext(ctx)
	if proxy.Spec.ToolConfigRef == nil {
		// Remove condition if ToolConfigRef is not set
		meta.RemoveStatusCondition(&proxy.Status.Conditions, mcpv1beta1.ConditionTypeMCPRemoteProxyToolConfigValidated)
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
		if errors.IsNotFound(err) {
			meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
				Type:   mcpv1beta1.ConditionTypeMCPRemoteProxyToolConfigValidated,
				Status: metav1.ConditionFalse,
				Reason: mcpv1beta1.ConditionReasonMCPRemoteProxyToolConfigNotFound,
				Message: fmt.Sprintf("MCPToolConfig '%s' not found in namespace '%s'",
					proxy.Spec.ToolConfigRef.Name, proxy.Namespace),
				ObservedGeneration: proxy.Generation,
			})
			return fmt.Errorf("MCPToolConfig '%s' not found in namespace '%s'",
				proxy.Spec.ToolConfigRef.Name, proxy.Namespace)
		}
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionTypeMCPRemoteProxyToolConfigValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1beta1.ConditionReasonMCPRemoteProxyToolConfigFetchError,
			Message:            "Failed to fetch MCPToolConfig",
			ObservedGeneration: proxy.Generation,
		})
		return fmt.Errorf("failed to fetch MCPToolConfig: %w", err)
	}

	// ToolConfig found and valid
	meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
		Type:               mcpv1beta1.ConditionTypeMCPRemoteProxyToolConfigValidated,
		Status:             metav1.ConditionTrue,
		Reason:             mcpv1beta1.ConditionReasonMCPRemoteProxyToolConfigValid,
		Message:            fmt.Sprintf("MCPToolConfig '%s' is valid", toolConfig.Name),
		ObservedGeneration: proxy.Generation,
	})

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

// handleTelemetryConfig validates and tracks the hash of the referenced MCPTelemetryConfig.
// It updates the MCPRemoteProxy status when the telemetry configuration changes.
func (r *MCPRemoteProxyReconciler) handleTelemetryConfig(ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy) error {
	ctxLogger := log.FromContext(ctx)

	if proxy.Spec.TelemetryConfigRef == nil {
		// No MCPTelemetryConfig referenced, clear any stored hash and condition.
		condType := mcpv1beta1.ConditionTypeMCPRemoteProxyTelemetryConfigRefValidated
		condRemoved := meta.FindStatusCondition(proxy.Status.Conditions, condType) != nil
		meta.RemoveStatusCondition(&proxy.Status.Conditions, condType)
		if condRemoved || proxy.Status.TelemetryConfigHash != "" {
			proxy.Status.TelemetryConfigHash = ""
			if err := r.Status().Update(ctx, proxy); err != nil {
				return fmt.Errorf("failed to clear MCPTelemetryConfig hash from status: %w", err)
			}
		}
		return nil
	}

	// Get the referenced MCPTelemetryConfig
	telemetryConfig, err := ctrlutil.GetTelemetryConfigForMCPRemoteProxy(ctx, r.Client, proxy)
	if err != nil {
		// Transient API error (not a NotFound)
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionTypeMCPRemoteProxyTelemetryConfigRefValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1beta1.ConditionReasonMCPRemoteProxyTelemetryConfigRefFetchError,
			Message:            err.Error(),
			ObservedGeneration: proxy.Generation,
		})
		return err
	}

	if telemetryConfig == nil {
		// Resource genuinely does not exist
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionTypeMCPRemoteProxyTelemetryConfigRefValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1beta1.ConditionReasonMCPRemoteProxyTelemetryConfigRefNotFound,
			Message:            fmt.Sprintf("MCPTelemetryConfig %s not found", proxy.Spec.TelemetryConfigRef.Name),
			ObservedGeneration: proxy.Generation,
		})
		return fmt.Errorf("MCPTelemetryConfig %s not found", proxy.Spec.TelemetryConfigRef.Name)
	}

	// Validate that the MCPTelemetryConfig is valid (has Valid=True condition)
	if err := telemetryConfig.Validate(); err != nil {
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionTypeMCPRemoteProxyTelemetryConfigRefValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1beta1.ConditionReasonMCPRemoteProxyTelemetryConfigRefInvalid,
			Message:            fmt.Sprintf("MCPTelemetryConfig %s is invalid: %v", proxy.Spec.TelemetryConfigRef.Name, err),
			ObservedGeneration: proxy.Generation,
		})
		return fmt.Errorf("MCPTelemetryConfig %s is invalid: %w", proxy.Spec.TelemetryConfigRef.Name, err)
	}

	// Detect whether the condition is transitioning to True (e.g. recovering from
	// a transient error). Without this check the status update is skipped when the
	// hash is unchanged, leaving a stale False condition.
	condType := mcpv1beta1.ConditionTypeMCPRemoteProxyTelemetryConfigRefValidated
	prevCondition := meta.FindStatusCondition(proxy.Status.Conditions, condType)
	needsUpdate := prevCondition == nil || prevCondition.Status != metav1.ConditionTrue

	meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
		Type:               mcpv1beta1.ConditionTypeMCPRemoteProxyTelemetryConfigRefValidated,
		Status:             metav1.ConditionTrue,
		Reason:             mcpv1beta1.ConditionReasonMCPRemoteProxyTelemetryConfigRefValid,
		Message:            fmt.Sprintf("MCPTelemetryConfig %s is valid", proxy.Spec.TelemetryConfigRef.Name),
		ObservedGeneration: proxy.Generation,
	})

	if proxy.Status.TelemetryConfigHash != telemetryConfig.Status.ConfigHash {
		ctxLogger.Info("MCPTelemetryConfig has changed, updating MCPRemoteProxy",
			"proxy", proxy.Name,
			"telemetryConfig", telemetryConfig.Name,
			"oldHash", proxy.Status.TelemetryConfigHash,
			"newHash", telemetryConfig.Status.ConfigHash)
		proxy.Status.TelemetryConfigHash = telemetryConfig.Status.ConfigHash
		needsUpdate = true
	}

	if needsUpdate {
		if err := r.Status().Update(ctx, proxy); err != nil {
			return fmt.Errorf("failed to update MCPTelemetryConfig status: %w", err)
		}
	}

	return nil
}

// handleExternalAuthConfig validates and tracks the hash of the referenced MCPExternalAuthConfig
func (r *MCPRemoteProxyReconciler) handleExternalAuthConfig(ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy) error {
	ctxLogger := log.FromContext(ctx)
	if proxy.Spec.ExternalAuthConfigRef == nil {
		// Remove condition if ExternalAuthConfigRef is not set
		meta.RemoveStatusCondition(&proxy.Status.Conditions, mcpv1beta1.ConditionTypeMCPRemoteProxyExternalAuthConfigValidated)
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
		if errors.IsNotFound(err) {
			meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
				Type:   mcpv1beta1.ConditionTypeMCPRemoteProxyExternalAuthConfigValidated,
				Status: metav1.ConditionFalse,
				Reason: mcpv1beta1.ConditionReasonMCPRemoteProxyExternalAuthConfigNotFound,
				Message: fmt.Sprintf("MCPExternalAuthConfig '%s' not found in namespace '%s'",
					proxy.Spec.ExternalAuthConfigRef.Name, proxy.Namespace),
				ObservedGeneration: proxy.Generation,
			})
			return fmt.Errorf("MCPExternalAuthConfig '%s' not found in namespace '%s'",
				proxy.Spec.ExternalAuthConfigRef.Name, proxy.Namespace)
		}
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionTypeMCPRemoteProxyExternalAuthConfigValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1beta1.ConditionReasonMCPRemoteProxyExternalAuthConfigFetchError,
			Message:            "Failed to fetch MCPExternalAuthConfig",
			ObservedGeneration: proxy.Generation,
		})
		return fmt.Errorf("failed to fetch MCPExternalAuthConfig: %w", err)
	}

	// Mirror the referenced MCPExternalAuthConfig's Valid=False condition onto
	// the MCPRemoteProxy so the failure is visible on the consumer CR (e.g.
	// obo-typed configs surface Valid=False/EnterpriseRequired here without the
	// user having to inspect the referenced MCPExternalAuthConfig).
	if mirrored, err := mirrorInvalidOnRemoteProxy(proxy, externalAuthConfig); mirrored {
		return err
	}

	// MCPRemoteProxy supports only single-upstream embedded auth server configs.
	// Multi-upstream requires VirtualMCPServer.
	if embeddedCfg := externalAuthConfig.Spec.EmbeddedAuthServer; embeddedCfg != nil && len(embeddedCfg.UpstreamProviders) > 1 {
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:   mcpv1beta1.ConditionTypeMCPRemoteProxyExternalAuthConfigValidated,
			Status: metav1.ConditionFalse,
			Reason: mcpv1beta1.ConditionReasonMCPRemoteProxyExternalAuthConfigMultiUpstream,
			Message: fmt.Sprintf(
				"MCPRemoteProxy supports only one upstream provider (found %d); "+
					"use VirtualMCPServer for multi-upstream",
				len(embeddedCfg.UpstreamProviders)),
			ObservedGeneration: proxy.Generation,
		})
		return fmt.Errorf("MCPRemoteProxy %s/%s: embedded auth server has %d upstream providers, but only 1 is supported",
			proxy.Namespace, proxy.Name, len(embeddedCfg.UpstreamProviders))
	}

	// ExternalAuthConfig found and valid
	meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
		Type:               mcpv1beta1.ConditionTypeMCPRemoteProxyExternalAuthConfigValidated,
		Status:             metav1.ConditionTrue,
		Reason:             mcpv1beta1.ConditionReasonMCPRemoteProxyExternalAuthConfigValid,
		Message:            fmt.Sprintf("MCPExternalAuthConfig '%s' is valid", externalAuthConfig.Name),
		ObservedGeneration: proxy.Generation,
	})

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

// handleAuthServerRef validates and tracks the hash of the referenced authServerRef config.
// It updates the MCPRemoteProxy status when the auth server configuration changes and sets
// the AuthServerRefValidated condition.
func (r *MCPRemoteProxyReconciler) handleAuthServerRef(ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy) error {
	ctxLogger := log.FromContext(ctx)
	if proxy.Spec.AuthServerRef == nil {
		meta.RemoveStatusCondition(&proxy.Status.Conditions, mcpv1beta1.ConditionTypeMCPRemoteProxyAuthServerRefValidated)
		if proxy.Status.AuthServerConfigHash != "" {
			proxy.Status.AuthServerConfigHash = ""
			if err := r.Status().Update(ctx, proxy); err != nil {
				return fmt.Errorf("failed to clear authServerRef hash from status: %w", err)
			}
		}
		return nil
	}

	// Only MCPExternalAuthConfig kind is supported
	if proxy.Spec.AuthServerRef.Kind != "MCPExternalAuthConfig" {
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:   mcpv1beta1.ConditionTypeMCPRemoteProxyAuthServerRefValidated,
			Status: metav1.ConditionFalse,
			Reason: mcpv1beta1.ConditionReasonMCPRemoteProxyAuthServerRefInvalidKind,
			Message: fmt.Sprintf("unsupported authServerRef kind %q: only MCPExternalAuthConfig is supported",
				proxy.Spec.AuthServerRef.Kind),
			ObservedGeneration: proxy.Generation,
		})
		return fmt.Errorf("unsupported authServerRef kind %q: only MCPExternalAuthConfig is supported",
			proxy.Spec.AuthServerRef.Kind)
	}

	// Fetch the referenced MCPExternalAuthConfig
	authConfig, err := ctrlutil.GetExternalAuthConfigByName(ctx, r.Client, proxy.Namespace, proxy.Spec.AuthServerRef.Name)
	if err != nil {
		if errors.IsNotFound(err) {
			meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
				Type:   mcpv1beta1.ConditionTypeMCPRemoteProxyAuthServerRefValidated,
				Status: metav1.ConditionFalse,
				Reason: mcpv1beta1.ConditionReasonMCPRemoteProxyAuthServerRefNotFound,
				Message: fmt.Sprintf("MCPExternalAuthConfig '%s' not found in namespace '%s'",
					proxy.Spec.AuthServerRef.Name, proxy.Namespace),
				ObservedGeneration: proxy.Generation,
			})
			return fmt.Errorf("MCPExternalAuthConfig '%s' not found in namespace '%s'",
				proxy.Spec.AuthServerRef.Name, proxy.Namespace)
		}
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionTypeMCPRemoteProxyAuthServerRefValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1beta1.ConditionReasonMCPRemoteProxyAuthServerRefFetchError,
			Message:            fmt.Sprintf("Failed to fetch MCPExternalAuthConfig '%s'", proxy.Spec.AuthServerRef.Name),
			ObservedGeneration: proxy.Generation,
		})
		return fmt.Errorf("failed to get authServerRef MCPExternalAuthConfig %s: %w", proxy.Spec.AuthServerRef.Name, err)
	}

	// Validate the config type is embeddedAuthServer
	if authConfig.Spec.Type != mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer {
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:   mcpv1beta1.ConditionTypeMCPRemoteProxyAuthServerRefValidated,
			Status: metav1.ConditionFalse,
			Reason: mcpv1beta1.ConditionReasonMCPRemoteProxyAuthServerRefInvalidType,
			Message: fmt.Sprintf("authServerRef '%s' has type %q, but only embeddedAuthServer is supported",
				proxy.Spec.AuthServerRef.Name, authConfig.Spec.Type),
			ObservedGeneration: proxy.Generation,
		})
		return fmt.Errorf("authServerRef '%s' has type %q, but only embeddedAuthServer is supported",
			proxy.Spec.AuthServerRef.Name, authConfig.Spec.Type)
	}

	// MCPRemoteProxy supports only single-upstream embedded auth server configs
	if embeddedCfg := authConfig.Spec.EmbeddedAuthServer; embeddedCfg != nil && len(embeddedCfg.UpstreamProviders) > 1 {
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:   mcpv1beta1.ConditionTypeMCPRemoteProxyAuthServerRefValidated,
			Status: metav1.ConditionFalse,
			Reason: mcpv1beta1.ConditionReasonMCPRemoteProxyAuthServerRefMultiUpstream,
			Message: fmt.Sprintf("MCPRemoteProxy supports only one upstream provider (found %d); "+
				"use VirtualMCPServer for multi-upstream",
				len(embeddedCfg.UpstreamProviders)),
			ObservedGeneration: proxy.Generation,
		})
		return fmt.Errorf("MCPRemoteProxy %s/%s: embedded auth server has %d upstream providers, "+
			"but only 1 is supported; use VirtualMCPServer",
			proxy.Namespace, proxy.Name, len(embeddedCfg.UpstreamProviders))
	}

	// AuthServerRef valid
	meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
		Type:               mcpv1beta1.ConditionTypeMCPRemoteProxyAuthServerRefValidated,
		Status:             metav1.ConditionTrue,
		Reason:             mcpv1beta1.ConditionReasonMCPRemoteProxyAuthServerRefValid,
		Message:            fmt.Sprintf("AuthServerRef '%s' is valid", authConfig.Name),
		ObservedGeneration: proxy.Generation,
	})

	// Check if the config hash has changed
	if proxy.Status.AuthServerConfigHash != authConfig.Status.ConfigHash {
		ctxLogger.Info("authServerRef config has changed, updating MCPRemoteProxy",
			"proxy", proxy.Name,
			"authServerRef", authConfig.Name,
			"oldHash", proxy.Status.AuthServerConfigHash,
			"newHash", authConfig.Status.ConfigHash)

		proxy.Status.AuthServerConfigHash = authConfig.Status.ConfigHash
		if err := r.Status().Update(ctx, proxy); err != nil {
			return fmt.Errorf("failed to update authServerRef hash in status: %w", err)
		}
	}

	return nil
}

// handleOIDCConfig validates and tracks the hash of the referenced MCPOIDCConfig.
// It updates the MCPRemoteProxy status when the OIDC configuration changes and sets
// the OIDCConfigRefValidated condition.
func (r *MCPRemoteProxyReconciler) handleOIDCConfig(ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy) error {
	ctxLogger := log.FromContext(ctx)

	if proxy.Spec.OIDCConfigRef == nil {
		// Remove condition if OIDCConfigRef is not set
		meta.RemoveStatusCondition(&proxy.Status.Conditions, mcpv1beta1.ConditionOIDCConfigRefValidated)
		if proxy.Status.OIDCConfigHash != "" {
			proxy.Status.OIDCConfigHash = ""
			if err := r.Status().Update(ctx, proxy); err != nil {
				return fmt.Errorf("failed to clear MCPOIDCConfig hash from status: %w", err)
			}
		}
		return nil
	}

	// Fetch and validate the referenced MCPOIDCConfig
	oidcConfig, err := r.fetchAndValidateOIDCConfig(ctx, proxy)
	if err != nil {
		return err
	}

	// ReferencingWorkloads on the MCPOIDCConfig is maintained solely by the
	// MCPOIDCConfig controller, which watches MCPServer/VirtualMCPServer/
	// MCPRemoteProxy and recomputes the full list (additions and removals). The
	// MCPRemoteProxy controller must not write the config's status: a full
	// r.Status().Update here would clobber conditions the config controller
	// owns, and the previous append-only write never removed stale entries.
	// See #5511.

	// Detect whether the condition is transitioning to True (e.g. recovering from
	// a transient error). Without this check the status update is skipped when the
	// hash is unchanged, leaving a stale False condition (#4511).
	prevCondition := meta.FindStatusCondition(proxy.Status.Conditions, mcpv1beta1.ConditionOIDCConfigRefValidated)
	needsUpdate := prevCondition == nil || prevCondition.Status != metav1.ConditionTrue

	meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
		Type:               mcpv1beta1.ConditionOIDCConfigRefValidated,
		Status:             metav1.ConditionTrue,
		Reason:             mcpv1beta1.ConditionReasonOIDCConfigRefValid,
		Message:            fmt.Sprintf("MCPOIDCConfig %s is valid and ready", proxy.Spec.OIDCConfigRef.Name),
		ObservedGeneration: proxy.Generation,
	})

	if proxy.Status.OIDCConfigHash != oidcConfig.Status.ConfigHash {
		ctxLogger.Info("MCPOIDCConfig has changed, updating MCPRemoteProxy",
			"proxy", proxy.Name,
			"oidcConfig", oidcConfig.Name,
			"oldHash", proxy.Status.OIDCConfigHash,
			"newHash", oidcConfig.Status.ConfigHash)
		proxy.Status.OIDCConfigHash = oidcConfig.Status.ConfigHash
		needsUpdate = true
	}

	if needsUpdate {
		if err := r.Status().Update(ctx, proxy); err != nil {
			return fmt.Errorf("failed to update MCPOIDCConfig status: %w", err)
		}
	}

	return nil
}

// fetchAndValidateOIDCConfig fetches the referenced MCPOIDCConfig, validates it is
// ready, and sets appropriate failure conditions on the MCPRemoteProxy if not.
func (r *MCPRemoteProxyReconciler) fetchAndValidateOIDCConfig(
	ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy,
) (*mcpv1beta1.MCPOIDCConfig, error) {
	ctxLogger := log.FromContext(ctx)

	oidcConfig, err := ctrlutil.GetOIDCConfigForServer(ctx, r.Client, proxy.Namespace, proxy.Spec.OIDCConfigRef)
	if err != nil {
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionOIDCConfigRefValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1beta1.ConditionReasonOIDCConfigRefNotFound,
			Message:            fmt.Sprintf("MCPOIDCConfig %s not found: %v", proxy.Spec.OIDCConfigRef.Name, err),
			ObservedGeneration: proxy.Generation,
		})
		if statusErr := r.Status().Update(ctx, proxy); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update status after MCPOIDCConfig lookup error")
		}
		return nil, err
	}

	if oidcConfig == nil {
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionOIDCConfigRefValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1beta1.ConditionReasonOIDCConfigRefNotFound,
			Message:            fmt.Sprintf("MCPOIDCConfig %s not found", proxy.Spec.OIDCConfigRef.Name),
			ObservedGeneration: proxy.Generation,
		})
		if statusErr := r.Status().Update(ctx, proxy); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update status after MCPOIDCConfig not found")
		}
		return nil, fmt.Errorf("MCPOIDCConfig %s not found", proxy.Spec.OIDCConfigRef.Name)
	}

	validCondition := meta.FindStatusCondition(oidcConfig.Status.Conditions, mcpv1beta1.ConditionTypeOIDCConfigValid)
	if validCondition == nil || validCondition.Status != metav1.ConditionTrue {
		msg := fmt.Sprintf("MCPOIDCConfig %s is not valid", proxy.Spec.OIDCConfigRef.Name)
		if validCondition != nil {
			msg = fmt.Sprintf("MCPOIDCConfig %s is not valid: %s", proxy.Spec.OIDCConfigRef.Name, validCondition.Message)
		}
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionOIDCConfigRefValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1beta1.ConditionReasonOIDCConfigRefNotValid,
			Message:            msg,
			ObservedGeneration: proxy.Generation,
		})
		if statusErr := r.Status().Update(ctx, proxy); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update status after MCPOIDCConfig validation check")
		}
		return nil, fmt.Errorf("%s", msg)
	}

	return oidcConfig, nil
}

// validateGroupRef validates the GroupRef field of the MCPRemoteProxy.
// This function only sets conditions on the proxy object - the caller is responsible
// for persisting the status update to avoid multiple conflicting status updates.
func (r *MCPRemoteProxyReconciler) validateGroupRef(ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy) {
	if proxy.Spec.GroupRef == nil {
		// No group reference - remove any existing GroupRefValidated condition
		// to avoid showing stale info from a previous reconciliation
		meta.RemoveStatusCondition(&proxy.Status.Conditions, mcpv1beta1.ConditionTypeMCPRemoteProxyGroupRefValidated)
		return
	}

	ctxLogger := log.FromContext(ctx)
	groupName := proxy.Spec.GroupRef.Name

	// Find the referenced MCPGroup
	group := &mcpv1beta1.MCPGroup{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: proxy.Namespace, Name: groupName}, group); err != nil {
		ctxLogger.Error(err, "Failed to validate GroupRef")
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionTypeMCPRemoteProxyGroupRefValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1beta1.ConditionReasonMCPRemoteProxyGroupRefNotFound,
			Message:            fmt.Sprintf("MCPGroup '%s' not found in namespace '%s'", groupName, proxy.Namespace),
			ObservedGeneration: proxy.Generation,
		})
	} else if group.Status.Phase != mcpv1beta1.MCPGroupPhaseReady {
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionTypeMCPRemoteProxyGroupRefValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1beta1.ConditionReasonMCPRemoteProxyGroupRefNotReady,
			Message:            fmt.Sprintf("MCPGroup '%s' is not ready (current phase: %s)", groupName, group.Status.Phase),
			ObservedGeneration: proxy.Generation,
		})
	} else {
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionTypeMCPRemoteProxyGroupRefValidated,
			Status:             metav1.ConditionTrue,
			Reason:             mcpv1beta1.ConditionReasonMCPRemoteProxyGroupRefValidated,
			Message:            fmt.Sprintf("MCPGroup '%s' is valid and ready", groupName),
			ObservedGeneration: proxy.Generation,
		})
	}
}

// validateCABundleRef validates the OIDC CA bundle ConfigMap reference if specified.
// The CA bundle is sourced from the referenced MCPOIDCConfig's inline configuration.
// It mirrors MCPServer.validateCABundleRef so the proxy surfaces the same
// CABundleRefValidated condition (see the Status Condition Parity rule).
//
// Writes go through MutateAndPatchStatus (per the operator.md Status Writes rule),
// which diffs and skips the wire call when nothing changed, so a steady-state
// reconcile is a no-op (idempotency) and only the condition is patched rather than
// the whole status PUT.
//
// This validator runs before handleOIDCConfig, which is the authoritative validator
// for the OIDCConfigRef itself. So when the MCPOIDCConfig cannot be resolved (e.g. a
// transient apiserver/cache error), it leaves the existing condition untouched and
// lets handleOIDCConfig's requeue drive recovery — clearing the condition only when
// the config resolves and genuinely has no CA bundle.
func (r *MCPRemoteProxyReconciler) validateCABundleRef(ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy) {
	condType := mcpv1beta1.ConditionTypeMCPRemoteProxyCABundleRefValidated

	caBundleRef, resolved := r.resolveOIDCCABundleRef(ctx, proxy)
	if !resolved {
		// Could not resolve the MCPOIDCConfig; leave any existing condition in place.
		return
	}
	if caBundleRef == nil || caBundleRef.ConfigMapRef == nil {
		// Config resolved and has no CA bundle: clear any stale condition.
		r.patchCABundleStatus(ctx, proxy, func(p *mcpv1beta1.MCPRemoteProxy) {
			meta.RemoveStatusCondition(&p.Status.Conditions, condType)
		})
		return
	}

	status, reason, message := r.evaluateCABundleRef(ctx, proxy, caBundleRef)
	r.patchCABundleStatus(ctx, proxy, func(p *mcpv1beta1.MCPRemoteProxy) {
		setRemoteProxyCABundleRefCondition(p, status, reason, message)
	})
}

// evaluateCABundleRef checks the referenced CA bundle ConfigMap and returns the
// resulting CABundleRefValidated condition status, reason, and message. It performs
// no status mutation or persistence. Mirrors MCPServer.validateCABundleRef's checks.
func (r *MCPRemoteProxyReconciler) evaluateCABundleRef(
	ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy, caBundleRef *mcpv1beta1.CABundleSource,
) (metav1.ConditionStatus, string, string) {
	ctxLogger := log.FromContext(ctx)

	// Validate the CABundleRef configuration
	if err := validation.ValidateCABundleSource(caBundleRef); err != nil {
		ctxLogger.Error(err, "Invalid CABundleRef configuration")
		return metav1.ConditionFalse, mcpv1beta1.ConditionReasonMCPRemoteProxyCABundleRefInvalid, err.Error()
	}

	// Check if the referenced ConfigMap exists
	cmName := caBundleRef.ConfigMapRef.Name
	configMap := &corev1.ConfigMap{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: proxy.Namespace, Name: cmName}, configMap); err != nil {
		ctxLogger.Error(err, "Failed to find CA bundle ConfigMap", "configMap", cmName)
		return metav1.ConditionFalse, mcpv1beta1.ConditionReasonMCPRemoteProxyCABundleRefNotFound,
			fmt.Sprintf("CA bundle ConfigMap '%s' not found in namespace '%s'", cmName, proxy.Namespace)
	}

	// Verify the key exists in the ConfigMap. A missing key is a configuration state
	// surfaced through the condition, not a Go error, so it logs at Info (the two
	// branches above carry a real error and log at Error).
	key := caBundleRef.ConfigMapRef.Key
	if key == "" {
		key = validation.OIDCCABundleDefaultKey
	}
	if _, exists := configMap.Data[key]; !exists {
		ctxLogger.Info("CA bundle key not found in ConfigMap", "configMap", cmName, "key", key)
		return metav1.ConditionFalse, mcpv1beta1.ConditionReasonMCPRemoteProxyCABundleRefInvalid,
			fmt.Sprintf("Key '%s' not found in ConfigMap '%s'", key, cmName)
	}

	return metav1.ConditionTrue, mcpv1beta1.ConditionReasonMCPRemoteProxyCABundleRefValid,
		fmt.Sprintf("CA bundle ConfigMap '%s' is valid (key: %s)", cmName, key)
}

// resolveOIDCCABundleRef returns the CA bundle reference from the proxy's referenced
// MCPOIDCConfig inline configuration. The bool reports whether the OIDC config was
// successfully resolved: (nil, true) means "resolved, no CA bundle" and (nil, false)
// means "could not resolve" (no ref, or a transient fetch error). The caller must not
// clear the condition when resolved is false, to avoid flapping it on a transient
// apiserver/cache error — handleOIDCConfig is the authoritative validator for the ref.
func (r *MCPRemoteProxyReconciler) resolveOIDCCABundleRef(
	ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy,
) (ref *mcpv1beta1.CABundleSource, resolved bool) {
	if proxy.Spec.OIDCConfigRef == nil {
		// No OIDC reference at all: there is nothing to resolve and no CA bundle, so
		// the condition should be cleared. Treat as resolved with no CA bundle.
		return nil, true
	}
	oidcCfg, err := ctrlutil.GetOIDCConfigForServer(ctx, r.Client, proxy.Namespace, proxy.Spec.OIDCConfigRef)
	if err != nil || oidcCfg == nil {
		return nil, false
	}
	if oidcCfg.Spec.Type != mcpv1beta1.MCPOIDCConfigTypeInline || oidcCfg.Spec.Inline == nil {
		return nil, true
	}
	return oidcCfg.Spec.Inline.CABundleRef, true
}

// setRemoteProxyCABundleRefCondition sets the CA bundle ref validation condition on the proxy.
func setRemoteProxyCABundleRefCondition(
	proxy *mcpv1beta1.MCPRemoteProxy, status metav1.ConditionStatus, reason, message string,
) {
	meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
		Type:               mcpv1beta1.ConditionTypeMCPRemoteProxyCABundleRefValidated,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: proxy.Generation,
	})
}

// patchCABundleStatus applies the CA bundle condition mutation via MutateAndPatchStatus
// (diff-only merge patch, skipped when nothing changed). The error is logged and
// swallowed: the CABundleRefValidated condition is advisory, and a failed write is
// healed on the next reconcile rather than blocking the reconcile's objective.
func (r *MCPRemoteProxyReconciler) patchCABundleStatus(
	ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy, mutate func(*mcpv1beta1.MCPRemoteProxy),
) {
	if err := ctrlutil.MutateAndPatchStatus(ctx, r.Client, proxy, mutate); err != nil {
		log.FromContext(ctx).Error(err, "Failed to update MCPRemoteProxy status after CABundleRef validation")
	}
}

// validateAuthzPrimaryUpstreamProviderIgnored surfaces an advisory condition
// when spec.authzConfig.inline.primaryUpstreamProvider is set on an
// MCPRemoteProxy. The proxy has no embedded auth server, so the field has no
// runtime effect — the condition gives operators a kubectl-visible signal
// that a configured value is being silently ignored.
//
// Mirrors the validateGroupRef convention: this only sets/removes the
// condition; the caller is responsible for persisting status.
func (*MCPRemoteProxyReconciler) validateAuthzPrimaryUpstreamProviderIgnored(proxy *mcpv1beta1.MCPRemoteProxy) {
	provider := proxy.Spec.AuthzConfig.DeprecatedInlinePrimaryUpstreamProvider()
	conditionType := mcpv1beta1.ConditionTypeAuthzPrimaryUpstreamProviderIgnored
	if provider == "" {
		meta.RemoveStatusCondition(&proxy.Status.Conditions, conditionType)
		return
	}
	meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
		Type:   conditionType,
		Status: metav1.ConditionTrue,
		Reason: mcpv1beta1.ConditionReasonAuthzPrimaryUpstreamProviderIgnored,
		Message: fmt.Sprintf("spec.authzConfig.inline.primaryUpstreamProvider=%q has no effect on MCPRemoteProxy; "+
			"the field only takes effect on VirtualMCPServer with an embedded auth server", provider),
		ObservedGeneration: proxy.Generation,
	})
}

// validateSessionStorageForReplicas surfaces a SessionStorageWarning condition
// when replicas > 1 but session storage is not configured with the Redis
// backend. Reconciliation continues regardless; this is advisory only.
// Mirrors the MCPServer and VirtualMCPServer validators so the condition is
// consistent across all types that share the replicas + sessionStorage pair.
// The caller is responsible for persisting status.
func (*MCPRemoteProxyReconciler) validateSessionStorageForReplicas(proxy *mcpv1beta1.MCPRemoteProxy) {
	condition := func() metav1.Condition {
		if proxy.Spec.Replicas == nil || *proxy.Spec.Replicas <= 1 {
			return metav1.Condition{
				Status:  metav1.ConditionFalse,
				Reason:  mcpv1beta1.ConditionReasonSessionStorageNotApplicable,
				Message: "session storage warning is not active",
			}
		}
		if proxy.Spec.SessionStorage == nil ||
			proxy.Spec.SessionStorage.Provider != mcpv1beta1.SessionStorageProviderRedis {
			return metav1.Condition{
				Status:  metav1.ConditionTrue,
				Reason:  mcpv1beta1.ConditionReasonSessionStorageMissing,
				Message: "replicas > 1 but sessionStorage.provider is not redis; sessions are not shared across replicas",
			}
		}
		return metav1.Condition{
			Status:  metav1.ConditionFalse,
			Reason:  mcpv1beta1.ConditionReasonSessionStorageConfigured,
			Message: "Redis session storage is configured",
		}
	}()
	condition.Type = mcpv1beta1.ConditionSessionStorageWarning
	condition.ObservedGeneration = proxy.Generation
	meta.SetStatusCondition(&proxy.Status.Conditions, condition)
}

// ensureRBACResources ensures that the RBAC resources are in place for the remote proxy.
// Uses the RBAC client (pkg/kubernetes/rbac) which creates or updates RBAC resources
// automatically during operator upgrades.
func (r *MCPRemoteProxyReconciler) ensureRBACResources(ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy) error {
	// If a service account is specified, we don't need to create one
	if proxy.Spec.ServiceAccount != nil {
		return nil
	}

	rbacClient := rbac.NewClient(r.Client, r.Scheme)
	proxyRunnerNameForRBAC := proxyRunnerServiceAccountNameForRemoteProxy(proxy.Name)

	// Ensure Role with minimal permissions for remote proxies
	// Remote proxies only need ConfigMap and Secret read access (no StatefulSet/Pod management)
	_, err := rbacClient.EnsureRBACResources(ctx, rbac.EnsureRBACResourcesParams{
		Name:             proxyRunnerNameForRBAC,
		Namespace:        proxy.Namespace,
		Rules:            remoteProxyRBACRules,
		Owner:            proxy,
		ImagePullSecrets: r.imagePullSecretsForRemoteProxy(proxy),
	})
	return err
}

// imagePullSecretsForRemoteProxy returns the image pull secrets the operator
// will set on the workload's PodSpec and ServiceAccount. The list is the merge
// of cluster-wide chart defaults (from r.ImagePullSecretsDefaults) with the
// per-CR list from spec.resourceOverrides.proxyDeployment.imagePullSecrets.
// CR-level entries win on name collisions; chart-level entries are appended
// additively. Returns nil when both inputs are empty.
func (r *MCPRemoteProxyReconciler) imagePullSecretsForRemoteProxy(
	proxy *mcpv1beta1.MCPRemoteProxy,
) []corev1.LocalObjectReference {
	var crLevel []corev1.LocalObjectReference
	if proxy.Spec.ResourceOverrides != nil && proxy.Spec.ResourceOverrides.ProxyDeployment != nil {
		crLevel = proxy.Spec.ResourceOverrides.ProxyDeployment.ImagePullSecrets
	}
	return r.ImagePullSecretsDefaults.Merge(crLevel)
}

// updateMCPRemoteProxyStatus updates the status of the MCPRemoteProxy
func (r *MCPRemoteProxyReconciler) updateMCPRemoteProxyStatus(ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy) error {
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
		proxy.Status.Phase = mcpv1beta1.MCPRemoteProxyPhaseReady
		proxy.Status.Message = "Remote proxy is running"
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionTypeReady,
			Status:             metav1.ConditionTrue,
			Reason:             mcpv1beta1.ConditionReasonDeploymentReady,
			Message:            "Deployment is ready and running",
			ObservedGeneration: proxy.Generation,
		})
	} else if pending > 0 {
		proxy.Status.Phase = mcpv1beta1.MCPRemoteProxyPhasePending
		proxy.Status.Message = "Remote proxy is starting"
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionTypeReady,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1beta1.ConditionReasonDeploymentNotReady,
			Message:            "Deployment is not yet ready",
			ObservedGeneration: proxy.Generation,
		})
	} else if failed > 0 {
		proxy.Status.Phase = mcpv1beta1.MCPRemoteProxyPhaseFailed
		proxy.Status.Message = "Remote proxy failed to start"
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionTypeReady,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1beta1.ConditionReasonDeploymentNotReady,
			Message:            "Deployment failed",
			ObservedGeneration: proxy.Generation,
		})
	} else {
		proxy.Status.Phase = mcpv1beta1.MCPRemoteProxyPhasePending
		proxy.Status.Message = "No pods found for remote proxy"
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionTypeReady,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1beta1.ConditionReasonDeploymentNotReady,
			Message:            "No pods found",
			ObservedGeneration: proxy.Generation,
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

// serviceAccountNameForRemoteProxy returns the service account name for a MCPRemoteProxy
// If a service account is specified in the spec, it returns that. Otherwise, returns the default.
func serviceAccountNameForRemoteProxy(proxy *mcpv1beta1.MCPRemoteProxy) string {
	if proxy.Spec.ServiceAccount != nil {
		return *proxy.Spec.ServiceAccount
	}
	return proxyRunnerServiceAccountNameForRemoteProxy(proxy.Name)
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
	proxy *mcpv1beta1.MCPRemoteProxy,
	runConfigChecksum string,
) bool {
	if deployment == nil || proxy == nil {
		return true
	}

	if len(deployment.Spec.Template.Spec.Containers) == 0 {
		return true
	}

	if r.containerNeedsUpdate(ctx, deployment, proxy, runConfigChecksum) {
		return true
	}

	if r.deploymentMetadataNeedsUpdate(deployment, proxy) {
		return true
	}

	if r.podTemplateMetadataNeedsUpdate(deployment, proxy, runConfigChecksum) {
		return true
	}

	if r.podTemplateSpecNeedsUpdate(ctx, deployment, proxy) {
		return true
	}

	if r.podSpecNeedsUpdate(ctx, deployment, proxy, runConfigChecksum) {
		return true
	}

	// Check if spec.replicas has changed. Only compare when spec.replicas is
	// non-nil; nil means hands-off mode (HPA or another external controller
	// manages replicas) and the live count is authoritative.
	if proxy.Spec.Replicas != nil {
		if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != *proxy.Spec.Replicas {
			return true
		}
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
	proxy *mcpv1beta1.MCPRemoteProxy,
	runConfigChecksum string,
) bool {
	if deployment == nil || proxy == nil || len(deployment.Spec.Template.Spec.Containers) == 0 {
		return true
	}

	if proxy.Spec.PodTemplateSpec != nil && len(proxy.Spec.PodTemplateSpec.Raw) > 0 {
		return r.containerNeedsUpdateWithPodTemplate(ctx, deployment, proxy, runConfigChecksum)
	}

	return r.generatedContainerNeedsUpdate(ctx, deployment, proxy)
}

func (r *MCPRemoteProxyReconciler) containerNeedsUpdateWithPodTemplate(
	ctx context.Context,
	deployment *appsv1.Deployment,
	proxy *mcpv1beta1.MCPRemoteProxy,
	runConfigChecksum string,
) bool {
	expectedDeployment := r.deploymentForMCPRemoteProxy(ctx, proxy, runConfigChecksum)
	if expectedDeployment == nil {
		return true
	}
	currentContainer, ok := findContainerByName(deployment.Spec.Template.Spec.Containers, mcpRemoteProxyContainerName)
	if !ok {
		return true
	}
	expectedContainer, ok := findContainerByName(expectedDeployment.Spec.Template.Spec.Containers, mcpRemoteProxyContainerName)
	if !ok {
		return true
	}
	return remoteProxyContainerFieldsNeedUpdate(currentContainer, expectedContainer)
}

func (r *MCPRemoteProxyReconciler) generatedContainerNeedsUpdate(
	ctx context.Context,
	deployment *appsv1.Deployment,
	proxy *mcpv1beta1.MCPRemoteProxy,
) bool {
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
	configName := ctrlutil.EmbeddedAuthServerConfigName(
		proxy.Spec.ExternalAuthConfigRef, proxy.Spec.AuthServerRef,
	)
	if configName != "" {
		_, _, authServerEnvVars, err := ctrlutil.GenerateAuthServerConfigByName(
			ctx, r.Client, proxy.Namespace, configName,
		)
		if err != nil {
			return true
		}
		expectedEnv = append(expectedEnv, authServerEnvVars...)
	}
	if !reflect.DeepEqual(container.Env, expectedEnv) {
		return true
	}

	// Check if resources have changed
	expectedResources := ctrlutil.BuildResourceRequirements(proxy.Spec.Resources)
	if !reflect.DeepEqual(container.Resources, expectedResources) {
		return true
	}

	// Check if service account has changed
	expectedServiceAccountName := serviceAccountNameForRemoteProxy(proxy)
	currentServiceAccountName := deployment.Spec.Template.Spec.ServiceAccountName
	if currentServiceAccountName != "" && currentServiceAccountName != expectedServiceAccountName {
		return true
	}

	return false
}

func findContainerByName(containers []corev1.Container, name string) (*corev1.Container, bool) {
	for i := range containers {
		if containers[i].Name == name {
			return &containers[i], true
		}
	}
	return nil, false
}

func remoteProxyContainerFieldsNeedUpdate(current, expected *corev1.Container) bool {
	if current == nil || expected == nil {
		return true
	}

	return current.Image != expected.Image ||
		!equality.Semantic.DeepEqual(current.Args, expected.Args) ||
		!equality.Semantic.DeepEqual(current.Env, expected.Env) ||
		!equality.Semantic.DeepEqual(current.VolumeMounts, expected.VolumeMounts) ||
		!equality.Semantic.DeepEqual(current.Resources, expected.Resources) ||
		!equality.Semantic.DeepEqual(current.Ports, expected.Ports) ||
		!equality.Semantic.DeepEqual(current.StartupProbe, expected.StartupProbe) ||
		!equality.Semantic.DeepEqual(current.LivenessProbe, expected.LivenessProbe) ||
		!equality.Semantic.DeepEqual(current.ReadinessProbe, expected.ReadinessProbe) ||
		!equality.Semantic.DeepEqual(current.SecurityContext, expected.SecurityContext)
}

// deploymentMetadataNeedsUpdate checks if deployment-level metadata has changed.
//
// Compares deployment labels and annotations, including any user-specified overrides
// from ResourceOverrides.ProxyDeployment.
func (*MCPRemoteProxyReconciler) deploymentMetadataNeedsUpdate(
	deployment *appsv1.Deployment,
	proxy *mcpv1beta1.MCPRemoteProxy,
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

	if !ctrlutil.MapIsSubset(expectedAnnotations, deployment.Annotations) {
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
	proxy *mcpv1beta1.MCPRemoteProxy,
	runConfigChecksum string,
) bool {
	if deployment == nil || proxy == nil {
		return true
	}

	expectedPodTemplateLabels, expectedPodTemplateAnnotations := r.buildPodTemplateMetadata(
		labelsForMCPRemoteProxy(proxy.Name), proxy, runConfigChecksum,
	)

	if proxy.Spec.PodTemplateSpec != nil && len(proxy.Spec.PodTemplateSpec.Raw) > 0 {
		return !ctrlutil.MapIsSubset(expectedPodTemplateLabels, deployment.Spec.Template.Labels) ||
			!ctrlutil.MapIsSubset(expectedPodTemplateAnnotations, deployment.Spec.Template.Annotations)
	}

	if !maps.Equal(deployment.Spec.Template.Labels, expectedPodTemplateLabels) {
		return true
	}

	if !maps.Equal(deployment.Spec.Template.Annotations, expectedPodTemplateAnnotations) {
		return true
	}

	return false
}

// podTemplateSpecNeedsUpdate checks whether the user-provided PodTemplateSpec raw input changed.
func (*MCPRemoteProxyReconciler) podTemplateSpecNeedsUpdate(
	ctx context.Context,
	deployment *appsv1.Deployment,
	proxy *mcpv1beta1.MCPRemoteProxy,
) bool {
	if deployment == nil || proxy == nil {
		return true
	}

	if proxy.Spec.PodTemplateSpec == nil || proxy.Spec.PodTemplateSpec.Raw == nil {
		_, hadPrevious := deployment.Annotations[podTemplateSpecHashAnnotation]
		return hadPrevious
	}

	expectedHash, err := checksum.HashRawJSON(proxy.Spec.PodTemplateSpec.Raw)
	if err != nil {
		log.FromContext(ctx).Error(err, "Failed to hash PodTemplateSpec, assuming update needed")
		return true
	}
	return deployment.Annotations[podTemplateSpecHashAnnotation] != expectedHash
}

// podSpecNeedsUpdate checks if pod-level fields (not container fields) have drifted.
//
// Currently compares ImagePullSecrets — the merge of cluster-wide chart
// defaults with spec.resourceOverrides.proxyDeployment.imagePullSecrets. Uses
// equality.Semantic.DeepEqual so nil and empty slices are treated as equal,
// which matches Kubernetes' own serialization semantics.
func (r *MCPRemoteProxyReconciler) podSpecNeedsUpdate(
	ctx context.Context,
	deployment *appsv1.Deployment,
	proxy *mcpv1beta1.MCPRemoteProxy,
	runConfigChecksum string,
) bool {
	if proxy.Spec.PodTemplateSpec != nil && len(proxy.Spec.PodTemplateSpec.Raw) > 0 {
		expectedDeployment := r.deploymentForMCPRemoteProxy(ctx, proxy, runConfigChecksum)
		if expectedDeployment == nil {
			return true
		}
		return deployment.Spec.Template.Spec.ServiceAccountName != expectedDeployment.Spec.Template.Spec.ServiceAccountName ||
			!equality.Semantic.DeepEqual(
				deployment.Spec.Template.Spec.ImagePullSecrets,
				expectedDeployment.Spec.Template.Spec.ImagePullSecrets,
			)
	}

	if deployment.Spec.Template.Spec.ServiceAccountName != serviceAccountNameForRemoteProxy(proxy) {
		return true
	}

	expected := r.imagePullSecretsForRemoteProxy(proxy)
	return !equality.Semantic.DeepEqual(deployment.Spec.Template.Spec.ImagePullSecrets, expected)
}

// serviceNeedsUpdate checks if the service needs to be updated
func (*MCPRemoteProxyReconciler) serviceNeedsUpdate(service *corev1.Service, proxy *mcpv1beta1.MCPRemoteProxy) bool {
	// Check if port has changed
	if len(service.Spec.Ports) > 0 && service.Spec.Ports[0].Port != int32(proxy.GetProxyPort()) {
		return true
	}

	// Check if session affinity has drifted from spec
	expectedAffinity := func() corev1.ServiceAffinity {
		if proxy.Spec.SessionAffinity != "" {
			return corev1.ServiceAffinity(proxy.Spec.SessionAffinity)
		}
		return corev1.ServiceAffinityClientIP
	}()
	if service.Spec.SessionAffinity != expectedAffinity {
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

	// Subset check rather than exact equality: the Service is co-owned by external
	// controllers (e.g. GKE NEG/Gateway writes cloud.google.com/* annotations), so only
	// the operator-owned keys must match. maps.Equal would treat those external
	// annotations as drift and hot-loop Update against the concurrent writer.
	if !ctrlutil.MapIsSubset(expectedLabels, service.Labels) {
		return true
	}

	if !ctrlutil.MapIsSubset(expectedAnnotations, service.Annotations) {
		return true
	}

	return false
}

// mapOIDCConfigToMCPRemoteProxy maps MCPOIDCConfig changes to MCPRemoteProxy reconciliation requests.
// It finds all MCPRemoteProxies that reference the changed MCPOIDCConfig and enqueues them.
func (r *MCPRemoteProxyReconciler) mapOIDCConfigToMCPRemoteProxy(
	ctx context.Context, obj client.Object,
) []reconcile.Request {
	oidcConfig, ok := obj.(*mcpv1beta1.MCPOIDCConfig)
	if !ok {
		return nil
	}

	// List all MCPRemoteProxies in the same namespace
	proxyList := &mcpv1beta1.MCPRemoteProxyList{}
	if err := r.List(ctx, proxyList, client.InNamespace(oidcConfig.Namespace)); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list MCPRemoteProxies for MCPOIDCConfig watch")
		return nil
	}

	// Find MCPRemoteProxies that reference this MCPOIDCConfig
	var requests []reconcile.Request
	for _, proxy := range proxyList.Items {
		if proxy.Spec.OIDCConfigRef != nil &&
			proxy.Spec.OIDCConfigRef.Name == oidcConfig.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      proxy.Name,
					Namespace: proxy.Namespace,
				},
			})
		}
	}

	return requests
}

// mapTelemetryConfigToMCPRemoteProxy maps MCPTelemetryConfig changes to MCPRemoteProxy reconciliation requests.
func (r *MCPRemoteProxyReconciler) mapTelemetryConfigToMCPRemoteProxy(
	ctx context.Context, obj client.Object,
) []reconcile.Request {
	telemetryConfig, ok := obj.(*mcpv1beta1.MCPTelemetryConfig)
	if !ok {
		return nil
	}

	proxyList := &mcpv1beta1.MCPRemoteProxyList{}
	if err := r.List(ctx, proxyList, client.InNamespace(telemetryConfig.Namespace)); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list MCPRemoteProxies for MCPTelemetryConfig watch")
		return nil
	}

	var requests []reconcile.Request
	for _, proxy := range proxyList.Items {
		if proxy.Spec.TelemetryConfigRef != nil &&
			proxy.Spec.TelemetryConfigRef.Name == telemetryConfig.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      proxy.Name,
					Namespace: proxy.Namespace,
				},
			})
		}
	}

	return requests
}

// handleAuthzConfig validates the referenced MCPAuthzConfig, tracks its hash on
// the MCPRemoteProxy status, and sets the AuthzConfigRefValidated condition. When
// the ref is cleared it removes both the hash and the condition so a stale "valid"
// signal does not linger. ReferencingWorkloads on the MCPAuthzConfig is owned by
// the MCPAuthzConfig controller (#5511); this controller never writes it.
func (r *MCPRemoteProxyReconciler) handleAuthzConfig(ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy) error {
	if proxy.Spec.AuthzConfigRef == nil {
		// No MCPAuthzConfig referenced: clear any stored hash and remove the
		// condition so it does not remain stale-True after the ref is removed.
		changed := false
		if proxy.Status.AuthzConfigHash != "" {
			proxy.Status.AuthzConfigHash = ""
			changed = true
		}
		if meta.RemoveStatusCondition(&proxy.Status.Conditions, mcpv1beta1.ConditionAuthzConfigRefValidated) {
			changed = true
		}
		if changed {
			if err := r.Status().Update(ctx, proxy); err != nil {
				return fmt.Errorf("failed to clear MCPAuthzConfig hash from MCPRemoteProxy status: %w", err)
			}
		}
		return nil
	}

	authzConfig, err := r.fetchAndValidateAuthzConfig(ctx, proxy)
	if err != nil {
		return err
	}

	prevCondition := meta.FindStatusCondition(proxy.Status.Conditions, mcpv1beta1.ConditionAuthzConfigRefValidated)
	needsUpdate := prevCondition == nil || prevCondition.Status != metav1.ConditionTrue

	meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
		Type:               mcpv1beta1.ConditionAuthzConfigRefValidated,
		Status:             metav1.ConditionTrue,
		Reason:             mcpv1beta1.ConditionReasonAuthzConfigRefValid,
		Message:            fmt.Sprintf("MCPAuthzConfig %s is valid and ready", proxy.Spec.AuthzConfigRef.Name),
		ObservedGeneration: proxy.Generation,
	})

	if proxy.Status.AuthzConfigHash != authzConfig.Status.ConfigHash {
		ctxLogger := log.FromContext(ctx)
		ctxLogger.Info("MCPAuthzConfig has changed, updating MCPRemoteProxy",
			"proxy", proxy.Name,
			"authzConfig", authzConfig.Name,
			"oldHash", proxy.Status.AuthzConfigHash,
			"newHash", authzConfig.Status.ConfigHash)
		proxy.Status.AuthzConfigHash = authzConfig.Status.ConfigHash
		needsUpdate = true
	}

	if needsUpdate {
		if err := r.Status().Update(ctx, proxy); err != nil {
			return fmt.Errorf("failed to update MCPRemoteProxy status after validating MCPAuthzConfig: %w", err)
		}
	}

	return nil
}

// fetchAndValidateAuthzConfig fetches the referenced MCPAuthzConfig, validates it
// is ready, and sets the appropriate failure condition on the MCPRemoteProxy if not.
func (r *MCPRemoteProxyReconciler) fetchAndValidateAuthzConfig(
	ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy,
) (*mcpv1beta1.MCPAuthzConfig, error) {
	ctxLogger := log.FromContext(ctx)

	authzConfig, err := ctrlutil.GetAuthzConfigForWorkload(ctx, r.Client, proxy.Namespace, proxy.Spec.AuthzConfigRef)
	if err != nil {
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionAuthzConfigRefValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1beta1.ConditionReasonAuthzConfigRefNotFound,
			Message:            fmt.Sprintf("MCPAuthzConfig %s not found: %v", proxy.Spec.AuthzConfigRef.Name, err),
			ObservedGeneration: proxy.Generation,
		})
		if statusErr := r.Status().Update(ctx, proxy); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update status after MCPAuthzConfig lookup error")
		}
		return nil, err
	}

	if authzConfig == nil {
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionAuthzConfigRefValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1beta1.ConditionReasonAuthzConfigRefNotFound,
			Message:            fmt.Sprintf("MCPAuthzConfig %s not found", proxy.Spec.AuthzConfigRef.Name),
			ObservedGeneration: proxy.Generation,
		})
		if statusErr := r.Status().Update(ctx, proxy); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update status after MCPAuthzConfig not found")
		}
		return nil, fmt.Errorf("MCPAuthzConfig %s not found", proxy.Spec.AuthzConfigRef.Name)
	}

	if err := ctrlutil.ValidateAuthzConfigReady(authzConfig); err != nil {
		meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionAuthzConfigRefValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1beta1.ConditionReasonAuthzConfigRefNotValid,
			Message:            fmt.Sprintf("MCPAuthzConfig %s is not valid: %v", proxy.Spec.AuthzConfigRef.Name, err),
			ObservedGeneration: proxy.Generation,
		})
		if statusErr := r.Status().Update(ctx, proxy); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update status after MCPAuthzConfig validation check")
		}
		return nil, err
	}

	return authzConfig, nil
}

// mapAuthzConfigToMCPRemoteProxy maps MCPAuthzConfig changes to MCPRemoteProxy reconciliation requests.
// It finds all MCPRemoteProxies that reference the changed MCPAuthzConfig and enqueues them.
func (r *MCPRemoteProxyReconciler) mapAuthzConfigToMCPRemoteProxy(
	ctx context.Context, obj client.Object,
) []reconcile.Request {
	authzConfig, ok := obj.(*mcpv1beta1.MCPAuthzConfig)
	if !ok {
		return nil
	}

	proxyList := &mcpv1beta1.MCPRemoteProxyList{}
	if err := r.List(ctx, proxyList, client.InNamespace(authzConfig.Namespace)); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list MCPRemoteProxies for MCPAuthzConfig watch")
		return nil
	}

	var requests []reconcile.Request
	for _, proxy := range proxyList.Items {
		if proxy.Spec.AuthzConfigRef != nil &&
			proxy.Spec.AuthzConfigRef.Name == authzConfig.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      proxy.Name,
					Namespace: proxy.Namespace,
				},
			})
		}
	}

	return requests
}

// SetupWithManager sets up the controller with the Manager
func (r *MCPRemoteProxyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Create a handler that maps MCPExternalAuthConfig changes to MCPRemoteProxy reconciliation requests
	externalAuthConfigHandler := handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []reconcile.Request {
			externalAuthConfig, ok := obj.(*mcpv1beta1.MCPExternalAuthConfig)
			if !ok {
				return nil
			}

			// List all MCPRemoteProxies in the same namespace
			proxyList := &mcpv1beta1.MCPRemoteProxyList{}
			if err := r.List(ctx, proxyList, client.InNamespace(externalAuthConfig.Namespace)); err != nil {
				log.FromContext(ctx).Error(err, "Failed to list MCPRemoteProxies for MCPExternalAuthConfig watch")
				return nil
			}

			// Find MCPRemoteProxies that reference this MCPExternalAuthConfig
			var requests []reconcile.Request
			for _, proxy := range proxyList.Items {
				if (proxy.Spec.ExternalAuthConfigRef != nil &&
					proxy.Spec.ExternalAuthConfigRef.Name == externalAuthConfig.Name) ||
					(proxy.Spec.AuthServerRef != nil &&
						proxy.Spec.AuthServerRef.Name == externalAuthConfig.Name) {
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
			toolConfig, ok := obj.(*mcpv1beta1.MCPToolConfig)
			if !ok {
				return nil
			}

			// List all MCPRemoteProxies in the same namespace
			proxyList := &mcpv1beta1.MCPRemoteProxyList{}
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
		For(&mcpv1beta1.MCPRemoteProxy{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Watches(&mcpv1beta1.MCPExternalAuthConfig{}, externalAuthConfigHandler).
		Watches(&mcpv1beta1.MCPToolConfig{}, toolConfigHandler).
		Watches(
			&mcpv1beta1.MCPOIDCConfig{},
			handler.EnqueueRequestsFromMapFunc(r.mapOIDCConfigToMCPRemoteProxy),
		).
		Watches(
			&mcpv1beta1.MCPTelemetryConfig{},
			handler.EnqueueRequestsFromMapFunc(r.mapTelemetryConfigToMCPRemoteProxy),
		).
		Watches(
			&mcpv1beta1.MCPAuthzConfig{},
			handler.EnqueueRequestsFromMapFunc(r.mapAuthzConfigToMCPRemoteProxy),
		).
		Complete(r)
}

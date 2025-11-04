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
)

// VirtualMCPServerReconciler reconciles a VirtualMCPServer object
type VirtualMCPServerReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
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

	// Validate GroupRef and discover backends
	if err := r.validateAndDiscoverBackends(ctx, vmcp); err != nil {
		return ctrl.Result{}, err
	}

	// Ensure all resources
	if err := r.ensureAllResources(ctx, vmcp); err != nil {
		return ctrl.Result{}, err
	}

	// Update status
	if err := r.updateVirtualMCPServerStatus(ctx, vmcp); err != nil {
		ctxLogger.Error(err, "Failed to update VirtualMCPServer status")
		return ctrl.Result{}, err
	}

	// Schedule periodic backend health checks
	return ctrl.Result{RequeueAfter: r.getHealthCheckInterval(vmcp)}, nil
}

// validateAndDiscoverBackends validates the GroupRef and discovers backend MCPServers
func (r *VirtualMCPServerReconciler) validateAndDiscoverBackends(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) error {
	ctxLogger := log.FromContext(ctx)

	// Validate GroupRef exists
	mcpGroup := &mcpv1alpha1.MCPGroup{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      vmcp.Spec.GroupRef.Name,
		Namespace: vmcp.Namespace,
	}, mcpGroup)

	if errors.IsNotFound(err) {
		vmcp.Status.Phase = mcpv1alpha1.VirtualMCPServerPhaseFailed
		vmcp.Status.Message = fmt.Sprintf("Referenced MCPGroup %s not found", vmcp.Spec.GroupRef.Name)
		meta.SetStatusCondition(&vmcp.Status.Conditions, metav1.Condition{
			Type:    mcpv1alpha1.ConditionTypeVirtualMCPServerGroupRefValidated,
			Status:  metav1.ConditionFalse,
			Reason:  mcpv1alpha1.ConditionReasonVirtualMCPServerGroupRefNotFound,
			Message: vmcp.Status.Message,
		})
		if statusErr := r.Status().Update(ctx, vmcp); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update VirtualMCPServer status after GroupRef validation error")
		}
		return err
	} else if err != nil {
		ctxLogger.Error(err, "Failed to get MCPGroup")
		return err
	}

	// Check if MCPGroup is ready
	if mcpGroup.Status.Phase != mcpv1alpha1.MCPGroupPhaseReady {
		vmcp.Status.Phase = mcpv1alpha1.VirtualMCPServerPhasePending
		vmcp.Status.Message = fmt.Sprintf("Referenced MCPGroup %s is not ready (phase: %s)",
			vmcp.Spec.GroupRef.Name, mcpGroup.Status.Phase)
		meta.SetStatusCondition(&vmcp.Status.Conditions, metav1.Condition{
			Type:    mcpv1alpha1.ConditionTypeVirtualMCPServerGroupRefValidated,
			Status:  metav1.ConditionFalse,
			Reason:  mcpv1alpha1.ConditionReasonVirtualMCPServerGroupRefNotReady,
			Message: vmcp.Status.Message,
		})
		if statusErr := r.Status().Update(ctx, vmcp); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update VirtualMCPServer status after GroupRef validation")
		}
		// Requeue to check again later
		return fmt.Errorf("MCPGroup %s is not ready", vmcp.Spec.GroupRef.Name)
	}

	// GroupRef is valid and ready
	meta.SetStatusCondition(&vmcp.Status.Conditions, metav1.Condition{
		Type:    mcpv1alpha1.ConditionTypeVirtualMCPServerGroupRefValidated,
		Status:  metav1.ConditionTrue,
		Reason:  mcpv1alpha1.ConditionReasonVirtualMCPServerGroupRefValid,
		Message: fmt.Sprintf("MCPGroup %s is valid and ready", vmcp.Spec.GroupRef.Name),
	})

	// Discover backends from MCPGroup
	if err := r.discoverBackends(ctx, vmcp, mcpGroup); err != nil {
		ctxLogger.Error(err, "Failed to discover backends")
		vmcp.Status.Phase = mcpv1alpha1.VirtualMCPServerPhaseFailed
		vmcp.Status.Message = fmt.Sprintf("Backend discovery failed: %v", err)
		meta.SetStatusCondition(&vmcp.Status.Conditions, metav1.Condition{
			Type:    mcpv1alpha1.ConditionTypeBackendsDiscovered,
			Status:  metav1.ConditionFalse,
			Reason:  mcpv1alpha1.ConditionReasonDiscoveryFailed,
			Message: err.Error(),
		})
		if statusErr := r.Status().Update(ctx, vmcp); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update VirtualMCPServer status after backend discovery error")
		}
		return err
	}

	return nil
}

// ensureAllResources ensures all Kubernetes resources for the VirtualMCPServer
func (r *VirtualMCPServerReconciler) ensureAllResources(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) error {
	ctxLogger := log.FromContext(ctx)

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
	return r.ensureServiceURL(ctx, vmcp)
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

// getVmcpConfigChecksum fetches the vmcp Config ConfigMap checksum annotation
func (r *VirtualMCPServerReconciler) getVmcpConfigChecksum(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) (string, error) {
	if vmcp == nil {
		return "", fmt.Errorf("vmcp cannot be nil")
	}

	configMapName := fmt.Sprintf("%s-vmcp-config", vmcp.Name)
	configMap := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      configMapName,
		Namespace: vmcp.Namespace,
	}, configMap)

	if err != nil {
		return "", err
	}

	checksum, ok := configMap.Annotations["toolhive.stacklok.dev/content-checksum"]
	if !ok || checksum == "" {
		return "", fmt.Errorf("ConfigMap %s missing content-checksum annotation", configMapName)
	}

	return checksum, nil
}

// ensureDeployment ensures the Deployment exists and is up to date
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
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 0}, nil
	} else if err != nil {
		ctxLogger.Error(err, "Failed to get Deployment")
		return ctrl.Result{}, err
	}

	// Deployment exists - check if it needs to be updated
	if r.deploymentNeedsUpdate(ctx, deployment, vmcp, vmcpConfigChecksum) {
		newDeployment := r.deploymentForVirtualMCPServer(ctx, vmcp, vmcpConfigChecksum)
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
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 0}, nil
	} else if err != nil {
		ctxLogger.Error(err, "Failed to get Service")
		return ctrl.Result{}, err
	}

	// Service exists - check if it needs to be updated
	if r.serviceNeedsUpdate(service, vmcp) {
		newService := r.serviceForVirtualMCPServer(ctx, vmcp)
		if newService == nil {
			return ctrl.Result{}, fmt.Errorf("failed to create updated Service object")
		}
		service.Spec.Ports = newService.Spec.Ports
		service.Spec.Type = newService.Spec.Type
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
func (r *VirtualMCPServerReconciler) ensureServiceURL(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) error {
	if vmcp.Status.URL == "" {
		vmcp.Status.URL = createVmcpServiceURL(vmcp.Name, vmcp.Namespace, vmcpDefaultPort)
		return r.Status().Update(ctx, vmcp)
	}
	return nil
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

// updateVirtualMCPServerStatus updates the status of the VirtualMCPServer
func (r *VirtualMCPServerReconciler) updateVirtualMCPServerStatus(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
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

	// Check backend health
	backendHealth := r.checkBackendHealth(ctx, vmcp)

	// Update the status based on pod and backend health
	if running > 0 && backendHealth.allHealthy {
		vmcp.Status.Phase = mcpv1alpha1.VirtualMCPServerPhaseReady
		vmcp.Status.Message = "Virtual MCP server is running and all backends are healthy"
		meta.SetStatusCondition(&vmcp.Status.Conditions, metav1.Condition{
			Type:    mcpv1alpha1.ConditionTypeVirtualMCPServerReady,
			Status:  metav1.ConditionTrue,
			Reason:  mcpv1alpha1.ConditionReasonAllBackendsReady,
			Message: "All backends are ready and healthy",
		})
	} else if running > 0 && backendHealth.someHealthy {
		vmcp.Status.Phase = mcpv1alpha1.VirtualMCPServerPhaseDegraded
		vmcp.Status.Message = fmt.Sprintf("Virtual MCP server is running but %d/%d backends are unavailable",
			backendHealth.unavailableCount, backendHealth.totalCount)
		meta.SetStatusCondition(&vmcp.Status.Conditions, metav1.Condition{
			Type:    mcpv1alpha1.ConditionTypeVirtualMCPServerReady,
			Status:  metav1.ConditionTrue,
			Reason:  mcpv1alpha1.ConditionReasonSomeBackendsUnavailable,
			Message: vmcp.Status.Message,
		})
	} else if pending > 0 {
		vmcp.Status.Phase = mcpv1alpha1.VirtualMCPServerPhasePending
		vmcp.Status.Message = "Virtual MCP server is starting"
		meta.SetStatusCondition(&vmcp.Status.Conditions, metav1.Condition{
			Type:    mcpv1alpha1.ConditionTypeVirtualMCPServerReady,
			Status:  metav1.ConditionFalse,
			Reason:  "DeploymentNotReady",
			Message: "Deployment is not yet ready",
		})
	} else if failed > 0 || !backendHealth.someHealthy {
		vmcp.Status.Phase = mcpv1alpha1.VirtualMCPServerPhaseFailed
		vmcp.Status.Message = "Virtual MCP server failed to start or all backends are unavailable"
		meta.SetStatusCondition(&vmcp.Status.Conditions, metav1.Condition{
			Type:    mcpv1alpha1.ConditionTypeVirtualMCPServerReady,
			Status:  metav1.ConditionFalse,
			Reason:  "DeploymentFailed",
			Message: "Deployment failed or all backends unavailable",
		})
	} else {
		vmcp.Status.Phase = mcpv1alpha1.VirtualMCPServerPhasePending
		vmcp.Status.Message = "No pods found for Virtual MCP server"
		meta.SetStatusCondition(&vmcp.Status.Conditions, metav1.Condition{
			Type:    mcpv1alpha1.ConditionTypeVirtualMCPServerReady,
			Status:  metav1.ConditionFalse,
			Reason:  "DeploymentNotReady",
			Message: "No pods found",
		})
	}

	// Update ObservedGeneration to reflect that we've processed this generation
	vmcp.Status.ObservedGeneration = vmcp.Generation

	return r.Status().Update(ctx, vmcp)
}

// getHealthCheckInterval returns the health check interval for the VirtualMCPServer
func (*VirtualMCPServerReconciler) getHealthCheckInterval(vmcp *mcpv1alpha1.VirtualMCPServer) time.Duration {
	if vmcp.Spec.Operational != nil &&
		vmcp.Spec.Operational.FailureHandling != nil &&
		vmcp.Spec.Operational.FailureHandling.HealthCheckInterval != "" {
		// TODO: Parse the duration string from spec
		// For now, return default
	}
	return 30 * time.Second
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
func vmcpServiceAccountName(vmcpName string) string {
	return fmt.Sprintf("%s-vmcp", vmcpName)
}

// vmcpServiceName generates the service name for a VirtualMCPServer
func vmcpServiceName(vmcpName string) string {
	return fmt.Sprintf("vmcp-%s", vmcpName)
}

// createVmcpServiceURL generates the full cluster-local service URL for a VirtualMCPServer
func createVmcpServiceURL(vmcpName, namespace string, port int32) string {
	serviceName := vmcpServiceName(vmcpName)
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", serviceName, namespace, port)
}

// SetupWithManager sets up the controller with the Manager
func (r *VirtualMCPServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Create a handler that maps MCPGroup changes to VirtualMCPServer reconciliation requests
	mcpGroupHandler := handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []reconcile.Request {
			mcpGroup, ok := obj.(*mcpv1alpha1.MCPGroup)
			if !ok {
				return nil
			}

			// List all VirtualMCPServers in the same namespace
			vmcpList := &mcpv1alpha1.VirtualMCPServerList{}
			if err := r.List(ctx, vmcpList, client.InNamespace(mcpGroup.Namespace)); err != nil {
				log.FromContext(ctx).Error(err, "Failed to list VirtualMCPServers for MCPGroup watch")
				return nil
			}

			// Find VirtualMCPServers that reference this MCPGroup
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
		},
	)

	// Create a handler that maps MCPServer changes to VirtualMCPServer reconciliation requests
	mcpServerHandler := handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []reconcile.Request {
			mcpServer, ok := obj.(*mcpv1alpha1.MCPServer)
			if !ok {
				return nil
			}

			// Find VirtualMCPServers that might include this MCPServer via their MCPGroup
			// This requires checking which MCPGroups include this server
			// For simplicity, we'll reconcile all VirtualMCPServers in the same namespace
			// A more optimized approach would track group memberships
			vmcpList := &mcpv1alpha1.VirtualMCPServerList{}
			if err := r.List(ctx, vmcpList, client.InNamespace(mcpServer.Namespace)); err != nil {
				log.FromContext(ctx).Error(err, "Failed to list VirtualMCPServers for MCPServer watch")
				return nil
			}

			var requests []reconcile.Request
			for _, vmcp := range vmcpList.Items {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      vmcp.Name,
						Namespace: vmcp.Namespace,
					},
				})
			}

			return requests
		},
	)

	// Create a handler that maps MCPExternalAuthConfig changes to VirtualMCPServer reconciliation requests
	externalAuthConfigHandler := handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []reconcile.Request {
			externalAuthConfig, ok := obj.(*mcpv1alpha1.MCPExternalAuthConfig)
			if !ok {
				return nil
			}

			// List all VirtualMCPServers in the same namespace that might reference this auth config
			vmcpList := &mcpv1alpha1.VirtualMCPServerList{}
			if err := r.List(ctx, vmcpList, client.InNamespace(externalAuthConfig.Namespace)); err != nil {
				log.FromContext(ctx).Error(err, "Failed to list VirtualMCPServers for MCPExternalAuthConfig watch")
				return nil
			}

			// For now, reconcile all VirtualMCPServers in the namespace
			// A more optimized approach would track which backends reference which auth configs
			var requests []reconcile.Request
			for _, vmcp := range vmcpList.Items {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      vmcp.Name,
						Namespace: vmcp.Namespace,
					},
				})
			}

			return requests
		},
	)

	// Create a handler that maps MCPToolConfig changes to VirtualMCPServer reconciliation requests
	toolConfigHandler := handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []reconcile.Request {
			toolConfig, ok := obj.(*mcpv1alpha1.MCPToolConfig)
			if !ok {
				return nil
			}

			// List all VirtualMCPServers in the same namespace
			vmcpList := &mcpv1alpha1.VirtualMCPServerList{}
			if err := r.List(ctx, vmcpList, client.InNamespace(toolConfig.Namespace)); err != nil {
				log.FromContext(ctx).Error(err, "Failed to list VirtualMCPServers for MCPToolConfig watch")
				return nil
			}

			// Find VirtualMCPServers that reference this MCPToolConfig in their aggregation settings
			var requests []reconcile.Request
			for _, vmcp := range vmcpList.Items {
				if vmcp.Spec.Aggregation != nil && len(vmcp.Spec.Aggregation.Tools) > 0 {
					for _, toolConfig := range vmcp.Spec.Aggregation.Tools {
						if toolConfig.ToolConfigRef != nil && toolConfig.ToolConfigRef.Name == toolConfig.ToolConfigRef.Name {
							requests = append(requests, reconcile.Request{
								NamespacedName: types.NamespacedName{
									Name:      vmcp.Name,
									Namespace: vmcp.Namespace,
								},
							})
							break
						}
					}
				}
			}

			return requests
		},
	)

	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.VirtualMCPServer{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Watches(&mcpv1alpha1.MCPGroup{}, mcpGroupHandler).
		Watches(&mcpv1alpha1.MCPServer{}, mcpServerHandler).
		Watches(&mcpv1alpha1.MCPExternalAuthConfig{}, externalAuthConfigHandler).
		Watches(&mcpv1alpha1.MCPToolConfig{}, toolConfigHandler).
		Complete(r)
}

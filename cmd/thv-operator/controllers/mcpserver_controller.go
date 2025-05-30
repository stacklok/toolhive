// Package controllers contains the reconciliation logic for the MCPServer custom resource.
// It handles the creation, update, and deletion of MCP servers in Kubernetes.
package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"

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
	"github.com/stacklok/toolhive/pkg/logger"
)

// MCPServerReconciler reconciles a MCPServer object
type MCPServerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// Allow the operator to manage MCPServer resources
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpservers,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpservers/status,verbs=get;patch;update
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpservers/finalizers,verbs=update

// Allow the operator to manage Deployments
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=create;delete;get;list;patch;update;watch

// Allow the operator to manage Services
// +kubebuilder:rbac:groups="",resources=services,verbs=create;delete;get;list;patch;update;watch

// Allow the operator read manage Pods
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch

// Allow the operator read manage ConfigMaps
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch

// Allow the operator read manage Secrets
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Allow the operator to manage events
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

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

	// Check if the deployment already exists, if not create a new one
	deployment := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: mcpServer.Name, Namespace: mcpServer.Namespace}, deployment)
	if err != nil && errors.IsNotFound(err) {
		// Define a new deployment
		dep := r.deploymentForMCPServer(mcpServer)
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
		svc := r.serviceForMCPServer(mcpServer)
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
		mcpServer.Status.URL = fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", service.Name, service.Namespace, mcpServer.Spec.Port)
		err = r.Status().Update(ctx, mcpServer)
		if err != nil {
			ctxLogger.Error(err, "Failed to update MCPServer status")
			return ctrl.Result{}, err
		}
	}

	// Check if the deployment spec changed
	if deploymentNeedsUpdate(deployment, mcpServer) {
		// Update the deployment
		newDeployment := r.deploymentForMCPServer(mcpServer)
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
		newService := r.serviceForMCPServer(mcpServer)
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

// deploymentForMCPServer returns a MCPServer Deployment object
//
//nolint:gocyclo
func (r *MCPServerReconciler) deploymentForMCPServer(m *mcpv1alpha1.MCPServer) *appsv1.Deployment {
	ls := labelsForMCPServer(m.Name)
	replicas := int32(1)

	// Prepare container args
	args := []string{"run", "--foreground=true"}
	args = append(args, fmt.Sprintf("--port=%d", m.Spec.Port))
	args = append(args, fmt.Sprintf("--name=%s", m.Name))
	args = append(args, fmt.Sprintf("--transport=%s", m.Spec.Transport))
	args = append(args, fmt.Sprintf("--host=%s", getProxyHost()))

	// Add pod template patch if provided
	if m.Spec.PodTemplateSpec != nil {
		podTemplatePatch, err := json.Marshal(m.Spec.PodTemplateSpec)
		if err != nil {
			logger.Errorf("Failed to marshal pod template spec: %v", err)
		} else {
			args = append(args, fmt.Sprintf("--k8s-pod-patch=%s", string(podTemplatePatch)))
		}
	}

	// Add permission profile args
	if m.Spec.PermissionProfile != nil {
		switch m.Spec.PermissionProfile.Type {
		case mcpv1alpha1.PermissionProfileTypeBuiltin:
			args = append(args, fmt.Sprintf("--permission-profile=%s", m.Spec.PermissionProfile.Name))
		case mcpv1alpha1.PermissionProfileTypeConfigMap:
			args = append(args, fmt.Sprintf("--permission-profile-path=/etc/toolhive/profiles/%s", m.Spec.PermissionProfile.Key))
		}
	}

	// Add secrets
	for _, secret := range m.Spec.Secrets {
		args = append(args, formatSecretArg(secret))
	}

	// Add the image
	args = append(args, m.Spec.Image)

	// Add additional args
	if len(m.Spec.Args) > 0 {
		args = append(args, "--")
		args = append(args, m.Spec.Args...)
	}

	// Prepare container env vars
	env := []corev1.EnvVar{}
	for _, e := range m.Spec.Env {
		env = append(env, corev1.EnvVar{
			Name:  e.Name,
			Value: e.Value,
		})
	}

	// Prepare container volume mounts
	volumeMounts := []corev1.VolumeMount{}
	volumes := []corev1.Volume{}

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

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name,
			Namespace: m.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: ls,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: ls,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "toolhive",
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
					}},
					Volumes: volumes,
				},
			},
		},
	}

	// Set MCPServer instance as the owner and controller
	if err := controllerutil.SetControllerReference(m, dep, r.Scheme); err != nil {
		logger.Error("Failed to set controller reference for Deployment", err)
		return nil
	}
	return dep
}

func createServiceName(mcpServerName string) string {
	return fmt.Sprintf("mcp-%s-proxy", mcpServerName)
}

// serviceForMCPServer returns a MCPServer Service object
func (r *MCPServerReconciler) serviceForMCPServer(m *mcpv1alpha1.MCPServer) *corev1.Service {
	ls := labelsForMCPServer(m.Name)

	// we want to generate a service name that is unique for the proxy service
	// to avoid conflicts with the headless service
	svcName := createServiceName(m.Name)

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      svcName,
			Namespace: m.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: ls,
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
		logger.Error("Failed to set controller reference for Service", err)
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
	// Update the MCPServer status
	m.Status.Phase = mcpv1alpha1.MCPServerPhaseTerminating
	m.Status.Message = "MCP server is being terminated"
	if err := r.Status().Update(ctx, m); err != nil {
		return err
	}

	// The owner references will automatically delete the deployment and service
	// when the MCPServer is deleted, so we don't need to do anything here.
	return nil
}

// deploymentNeedsUpdate checks if the deployment needs to be updated
//
//nolint:gocyclo
func deploymentNeedsUpdate(deployment *appsv1.Deployment, mcpServer *mcpv1alpha1.MCPServer) bool {
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
		portArg := fmt.Sprintf("--port=%d", mcpServer.Spec.Port)
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

		// Check if the secrets have changed
		for _, secret := range mcpServer.Spec.Secrets {
			secretArg := formatSecretArg(secret)
			found := false
			for _, arg := range container.Args {
				if arg == secretArg {
					found = true
					break
				}
			}
			if !found {
				return true
			}
		}

		// Check if the container port has changed
		if len(container.Ports) > 0 && container.Ports[0].ContainerPort != mcpServer.Spec.Port {
			return true
		}

		// Check if the environment variables have changed
		if len(container.Env) != len(mcpServer.Spec.Env) {
			return true
		}
		for i, env := range container.Env {
			if i >= len(mcpServer.Spec.Env) || env.Name != mcpServer.Spec.Env[i].Name || env.Value != mcpServer.Spec.Env[i].Value {
				return true
			}
		}

		// Check if the resource requirements have changed
		if !reflect.DeepEqual(container.Resources, resourceRequirementsForMCPServer(mcpServer)) {
			return true
		}
	}

	// Check if the service account name has changed
	if deployment.Spec.Template.Spec.ServiceAccountName != "toolhive" {
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

// getProxyHost returns the host to bind the proxy to
func getProxyHost() string {
	host := os.Getenv("TOOLHIVE_PROXY_HOST")
	if host == "" {
		host = "0.0.0.0"
	}
	return host
}

// getToolhiveRunnerImage returns the image to use for the toolhive runner container
func getToolhiveRunnerImage() string {
	// Get the image from the environment variable or use a default
	image := os.Getenv("TOOLHIVE_RUNNER_IMAGE")
	if image == "" {
		// Default to the published image
		image = "ghcr.io/stacklok/toolhive:latest"
	}
	return image
}

// formatSecretArg formats a secret reference into a command-line argument
func formatSecretArg(secret mcpv1alpha1.SecretRef) string {
	targetEnv := secret.Key
	if secret.TargetEnvName != "" {
		targetEnv = secret.TargetEnvName
	}
	return fmt.Sprintf("--secret=%s/%s,target=%s", secret.Name, secret.Key, targetEnv)
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

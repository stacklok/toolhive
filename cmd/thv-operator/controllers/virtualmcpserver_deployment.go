package controllers

import (
	"context"
	"fmt"
	"os"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

const (
	vmcpDefaultPort = int32(8080)
)

// RBAC rules for VirtualMCPServer service account
var vmcpRBACRules = []rbacv1.PolicyRule{
	{
		APIGroups: []string{""},
		Resources: []string{"configmaps", "secrets"},
		Verbs:     []string{"get", "list", "watch"},
	},
}

// deploymentForVirtualMCPServer returns a VirtualMCPServer Deployment object
func (r *VirtualMCPServerReconciler) deploymentForVirtualMCPServer(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	vmcpConfigChecksum string,
) *appsv1.Deployment {
	ls := labelsForVirtualMCPServer(vmcp.Name)
	replicas := int32(1)

	// Build deployment components using helper functions
	args := r.buildContainerArgsForVmcp()
	volumeMounts, volumes := r.buildVolumesForVmcp(vmcp)
	env := r.buildEnvVarsForVmcp(ctx, vmcp)
	deploymentLabels, deploymentAnnotations := r.buildDeploymentMetadataForVmcp(ls, vmcp)
	deploymentTemplateLabels, deploymentTemplateAnnotations := r.buildPodTemplateMetadata(ls, vmcp, vmcpConfigChecksum)
	podSecurityContext, containerSecurityContext := r.buildSecurityContextsForVmcp(ctx, vmcp)

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        vmcp.Name,
			Namespace:   vmcp.Namespace,
			Labels:      deploymentLabels,
			Annotations: deploymentAnnotations,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: ls,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      deploymentTemplateLabels,
					Annotations: deploymentTemplateAnnotations,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: vmcpServiceAccountName(vmcp.Name),
					Containers: []corev1.Container{{
						Image:           getVmcpImage(),
						Name:            "vmcp",
						Args:            args,
						Env:             env,
						VolumeMounts:    volumeMounts,
						Ports:           r.buildContainerPortsForVmcp(vmcp),
						LivenessProbe:   ctrlutil.BuildHealthProbe("/health", "http", 30, 10, 5, 3),
						ReadinessProbe:  ctrlutil.BuildHealthProbe("/health", "http", 15, 5, 3, 3),
						SecurityContext: containerSecurityContext,
					}},
					Volumes:         volumes,
					SecurityContext: podSecurityContext,
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(vmcp, dep, r.Scheme); err != nil {
		ctxLogger := log.FromContext(ctx)
		ctxLogger.Error(err, "Failed to set controller reference for Deployment")
		return nil
	}
	return dep
}

// buildContainerArgsForVmcp builds the container arguments for vmcp
func (*VirtualMCPServerReconciler) buildContainerArgsForVmcp() []string {
	return []string{
		"serve",
		"--config=/etc/vmcp-config/config.json",
	}
}

// buildVolumesForVmcp builds volumes and volume mounts for vmcp
func (*VirtualMCPServerReconciler) buildVolumesForVmcp(
	vmcp *mcpv1alpha1.VirtualMCPServer,
) ([]corev1.VolumeMount, []corev1.Volume) {
	volumeMounts := []corev1.VolumeMount{}
	volumes := []corev1.Volume{}

	// Add vmcp Config ConfigMap volume
	configMapName := fmt.Sprintf("%s-vmcp-config", vmcp.Name)
	volumeMounts = append(volumeMounts, corev1.VolumeMount{
		Name:      "vmcp-config",
		MountPath: "/etc/vmcp-config",
		ReadOnly:  true,
	})

	volumes = append(volumes, corev1.Volume{
		Name: "vmcp-config",
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: configMapName,
				},
			},
		},
	})

	// TODO: Add volumes for composite tool definitions from VirtualMCPCompositeToolDefinition refs

	return volumeMounts, volumes
}

// buildEnvVarsForVmcp builds environment variables for the vmcp container
func (r *VirtualMCPServerReconciler) buildEnvVarsForVmcp(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) []corev1.EnvVar {
	env := []corev1.EnvVar{}

	// Add basic environment variables
	env = append(env, corev1.EnvVar{
		Name:  "VMCP_NAME",
		Value: vmcp.Name,
	})

	env = append(env, corev1.EnvVar{
		Name:  "VMCP_NAMESPACE",
		Value: vmcp.Namespace,
	})

	// Add log level if specified
	if vmcp.Spec.Operational != nil {
		// TODO: Add log level from operational config
	}

	// TODO: Add environment variables for:
	// - Redis connection (if using Redis token cache)
	// - OIDC client secrets
	// - Other secrets referenced in the config

	return ctrlutil.EnsureRequiredEnvVars(ctx, env)
}

// buildDeploymentMetadataForVmcp builds deployment-level labels and annotations
func (*VirtualMCPServerReconciler) buildDeploymentMetadataForVmcp(
	baseLabels map[string]string,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) (map[string]string, map[string]string) {
	deploymentLabels := baseLabels
	deploymentAnnotations := make(map[string]string)

	// TODO: Add support for ResourceOverrides if needed in the future

	return deploymentLabels, deploymentAnnotations
}

// buildPodTemplateMetadata builds pod template labels and annotations for vmcp
func (*VirtualMCPServerReconciler) buildPodTemplateMetadata(
	baseLabels map[string]string,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	vmcpConfigChecksum string,
) (map[string]string, map[string]string) {
	templateLabels := baseLabels
	templateAnnotations := make(map[string]string)

	// Add vmcp Config checksum annotation to trigger pod rollout when config changes
	templateAnnotations["toolhive.stacklok.dev/vmcp-config-checksum"] = vmcpConfigChecksum

	// TODO: Add support for PodTemplateSpec overrides from spec

	return templateLabels, templateAnnotations
}

// buildSecurityContextsForVmcp builds pod and container security contexts
func (r *VirtualMCPServerReconciler) buildSecurityContextsForVmcp(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) (*corev1.PodSecurityContext, *corev1.SecurityContext) {
	if r.PlatformDetector == nil {
		r.PlatformDetector = ctrlutil.NewSharedPlatformDetector()
	}

	detectedPlatform, err := r.PlatformDetector.DetectPlatform(ctx)
	if err != nil {
		ctxLogger := log.FromContext(ctx)
		ctxLogger.Error(err, "Failed to detect platform, defaulting to Kubernetes", "virtualmcpserver", vmcp.Name)
	}

	securityBuilder := kubernetes.NewSecurityContextBuilder(detectedPlatform)
	return securityBuilder.BuildPodSecurityContext(), securityBuilder.BuildContainerSecurityContext()
}

// buildContainerPortsForVmcp builds container port configuration
func (*VirtualMCPServerReconciler) buildContainerPortsForVmcp(
	vmcp *mcpv1alpha1.VirtualMCPServer,
) []corev1.ContainerPort {
	return []corev1.ContainerPort{{
		ContainerPort: vmcpDefaultPort,
		Name:          "http",
		Protocol:      corev1.ProtocolTCP,
	}}
}

// serviceForVirtualMCPServer returns a VirtualMCPServer Service object
func (r *VirtualMCPServerReconciler) serviceForVirtualMCPServer(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) *corev1.Service {
	ls := labelsForVirtualMCPServer(vmcp.Name)
	svcName := vmcpServiceName(vmcp.Name)

	// Build service metadata
	serviceLabels, serviceAnnotations := r.buildServiceMetadataForVmcp(ls, vmcp)

	// Determine service type (ClusterIP by default, LoadBalancer if specified)
	serviceType := corev1.ServiceTypeClusterIP
	// TODO: Add configuration option for LoadBalancer service type

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        svcName,
			Namespace:   vmcp.Namespace,
			Labels:      serviceLabels,
			Annotations: serviceAnnotations,
		},
		Spec: corev1.ServiceSpec{
			Type:     serviceType,
			Selector: ls,
			Ports: []corev1.ServicePort{{
				Port:       vmcpDefaultPort,
				TargetPort: intstr.FromInt(int(vmcpDefaultPort)),
				Protocol:   corev1.ProtocolTCP,
				Name:       "http",
			}},
		},
	}

	if err := controllerutil.SetControllerReference(vmcp, svc, r.Scheme); err != nil {
		ctxLogger := log.FromContext(ctx)
		ctxLogger.Error(err, "Failed to set controller reference for Service")
		return nil
	}
	return svc
}

// buildServiceMetadataForVmcp builds service labels and annotations
func (*VirtualMCPServerReconciler) buildServiceMetadataForVmcp(
	baseLabels map[string]string,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) (map[string]string, map[string]string) {
	serviceLabels := baseLabels
	serviceAnnotations := make(map[string]string)

	// TODO: Add support for ResourceOverrides if needed in the future

	return serviceLabels, serviceAnnotations
}

// getVmcpImage returns the vmcp container image
func getVmcpImage() string {
	if image := os.Getenv("VMCP_IMAGE"); image != "" {
		return image
	}
	// Default to latest vmcp image
	// TODO: Use versioned image from build
	return "ghcr.io/stacklok/toolhive/vmcp:latest"
}

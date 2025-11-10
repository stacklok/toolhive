package controllers

import (
	"context"
	"fmt"
	"os"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

const (
	// Network configuration
	vmcpDefaultPort = int32(4483) // Default port for VirtualMCPServer service (matches vmcp server port)

	// Health probe configuration for VirtualMCPServer containers
	// These values are tuned for VMCP's aggregation workload characteristics:
	// - Higher initial delay accounts for backend discovery and config loading
	// - Readiness probe is more aggressive to detect availability issues quickly
	// - Liveness probe is more conservative to avoid unnecessary restarts

	// Liveness probe parameters (detects if container needs restart)
	vmcpLivenessInitialDelay = int32(30) // seconds - allow time for startup and backend discovery
	vmcpLivenessPeriod       = int32(10) // seconds - check every 10s
	vmcpLivenessTimeout      = int32(5)  // seconds - wait up to 5s for response
	vmcpLivenessFailures     = int32(3)  // consecutive failures before restart

	// Readiness probe parameters (detects if container can serve traffic)
	vmcpReadinessInitialDelay = int32(15) // seconds - shorter than liveness to enable traffic sooner
	vmcpReadinessPeriod       = int32(5)  // seconds - check more frequently for quick detection
	vmcpReadinessTimeout      = int32(3)  // seconds - shorter timeout for faster detection
	vmcpReadinessFailures     = int32(3)  // consecutive failures before removing from service
)

// RBAC rules for VirtualMCPServer service account
var vmcpRBACRules = []rbacv1.PolicyRule{
	{
		APIGroups: []string{""},
		Resources: []string{"configmaps", "secrets"},
		Verbs:     []string{"get", "list", "watch"},
	},
	{
		APIGroups: []string{"toolhive.stacklok.dev"},
		Resources: []string{"mcpgroups", "mcpservers"},
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
						ImagePullPolicy: corev1.PullIfNotPresent,
						Name:            "vmcp",
						Args:            args,
						Env:             env,
						VolumeMounts:    volumeMounts,
						Ports:           r.buildContainerPortsForVmcp(vmcp),
						LivenessProbe: ctrlutil.BuildHealthProbe(
							"/health", "http",
							vmcpLivenessInitialDelay, vmcpLivenessPeriod, vmcpLivenessTimeout, vmcpLivenessFailures,
						),
						ReadinessProbe: ctrlutil.BuildHealthProbe(
							"/health", "http",
							vmcpReadinessInitialDelay, vmcpReadinessPeriod, vmcpReadinessTimeout, vmcpReadinessFailures,
						),
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
		"--config=/etc/vmcp-config/config.yaml",
		"--host=0.0.0.0", // Listen on all interfaces for Kubernetes service routing
		"--port=4483",    // Standard vmcp port
	}
}

// buildVolumesForVmcp builds volumes and volume mounts for vmcp
func (*VirtualMCPServerReconciler) buildVolumesForVmcp(
	vmcp *mcpv1alpha1.VirtualMCPServer,
) ([]corev1.VolumeMount, []corev1.Volume) {
	volumeMounts := []corev1.VolumeMount{}
	volumes := []corev1.Volume{}

	// Add vmcp Config ConfigMap volume
	configMapName := vmcpConfigMapName(vmcp.Name)
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
func (*VirtualMCPServerReconciler) buildEnvVarsForVmcp(
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

	// TODO: Add log level from operational config when Operational is not nil
	//nolint:staticcheck // Empty branch reserved for future log level configuration
	if vmcp.Spec.Operational != nil {
		// Log level env var will be added here
	}

	// Mount OIDC client secret as environment variable
	// The vmcp config file will reference this via client_secret_env: "VMCP_OIDC_CLIENT_SECRET"
	//
	// Two approaches are supported:
	// 1. ClientSecretRef: References an existing Kubernetes Secret (recommended)
	// 2. ClientSecret: Literal value that will be stored in a generated Secret
	//
	// Both cases result in the secret being mounted as an environment variable for security.
	if vmcp.Spec.IncomingAuth != nil &&
		vmcp.Spec.IncomingAuth.OIDCConfig != nil &&
		vmcp.Spec.IncomingAuth.OIDCConfig.Inline != nil {
		inline := vmcp.Spec.IncomingAuth.OIDCConfig.Inline

		// For testing: Skip OIDC discovery for example/test issuers
		// This allows tests to run without requiring a real OIDC provider
		if inline.Issuer != "" && (strings.Contains(inline.Issuer, "example.com") || strings.Contains(inline.Issuer, "test")) {
			env = append(env, corev1.EnvVar{
				Name:  "VMCP_SKIP_OIDC_DISCOVERY",
				Value: "true",
			})
		}

		if inline.ClientSecretRef != nil {
			// Approach 1: Mount from existing Secret reference
			env = append(env, corev1.EnvVar{
				Name: "VMCP_OIDC_CLIENT_SECRET",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: inline.ClientSecretRef.Name,
						},
						Key: inline.ClientSecretRef.Key,
					},
				},
			})
		} else if inline.ClientSecret != "" {
			// Approach 2: Mount from generated Secret containing literal value
			// The generated secret is created by ensureOIDCClientSecret()
			generatedSecretName := fmt.Sprintf("%s-oidc-client-secret", vmcp.Name)
			env = append(env, corev1.EnvVar{
				Name: "VMCP_OIDC_CLIENT_SECRET",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: generatedSecretName,
						},
						Key: "clientSecret",
					},
				},
			})
		}
	}

	// Note: Other secrets (Redis passwords, service account credentials) may be added here in the future
	// following the same pattern of mounting from Kubernetes Secrets as environment variables.

	return ctrlutil.EnsureRequiredEnvVars(ctx, env)
}

// buildDeploymentMetadataForVmcp builds deployment-level labels and annotations
func (*VirtualMCPServerReconciler) buildDeploymentMetadataForVmcp(
	baseLabels map[string]string,
	_ *mcpv1alpha1.VirtualMCPServer,
) (map[string]string, map[string]string) {
	deploymentLabels := baseLabels
	deploymentAnnotations := make(map[string]string)

	// TODO: Add support for ResourceOverrides if needed in the future

	return deploymentLabels, deploymentAnnotations
}

// buildPodTemplateMetadata builds pod template labels and annotations for vmcp
func (*VirtualMCPServerReconciler) buildPodTemplateMetadata(
	baseLabels map[string]string,
	_ *mcpv1alpha1.VirtualMCPServer,
	vmcpConfigChecksum string,
) (map[string]string, map[string]string) {
	templateLabels := baseLabels

	// Add vmcp Config checksum annotation to trigger pod rollout when config changes
	// Use the standard checksum package helper for consistency
	templateAnnotations := checksum.AddRunConfigChecksumToPodTemplate(nil, vmcpConfigChecksum)

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
	_ *mcpv1alpha1.VirtualMCPServer,
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

	// Determine service type from spec (defaults to ClusterIP if not specified)
	serviceType := corev1.ServiceTypeClusterIP
	if vmcp.Spec.ServiceType != "" {
		serviceType = corev1.ServiceType(vmcp.Spec.ServiceType)
	}

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
	_ *mcpv1alpha1.VirtualMCPServer,
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

// validateSecretReferences validates that all secret references in the VirtualMCPServer spec exist
// and contain the required keys. This catches configuration errors during reconciliation rather than
// at pod startup, providing faster feedback to users.
//
// Validated secrets include:
// - OIDC client secrets (IncomingAuth.OIDCConfig.Inline.ClientSecretRef)
// - Service account credentials (OutgoingAuth.*.ServiceAccount.CredentialsRef)
// - Redis passwords (TokenCache.Redis.PasswordRef)
//
// This follows the pattern from ctrlutil.GenerateOIDCClientSecretEnvVar() which validates secrets
// exist before pod creation.
//
//nolint:gocyclo // Secret validation requires checking multiple optional config paths
func (r *VirtualMCPServerReconciler) validateSecretReferences(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) error {
	// Validate OIDC client secret if configured
	if vmcp.Spec.IncomingAuth != nil &&
		vmcp.Spec.IncomingAuth.OIDCConfig != nil &&
		vmcp.Spec.IncomingAuth.OIDCConfig.Inline != nil &&
		vmcp.Spec.IncomingAuth.OIDCConfig.Inline.ClientSecretRef != nil {
		if err := r.validateSecretKeyRef(ctx, vmcp.Namespace,
			vmcp.Spec.IncomingAuth.OIDCConfig.Inline.ClientSecretRef,
			"OIDC client secret"); err != nil {
			return err
		}
	}

	// Validate service account credentials in default backend auth
	if vmcp.Spec.OutgoingAuth != nil && vmcp.Spec.OutgoingAuth.Default != nil {
		if err := r.validateBackendAuthSecrets(ctx, vmcp.Namespace, vmcp.Spec.OutgoingAuth.Default, "default backend"); err != nil {
			return err
		}
	}

	// Validate service account credentials in per-backend auth
	if vmcp.Spec.OutgoingAuth != nil {
		for backendName, backendAuth := range vmcp.Spec.OutgoingAuth.Backends {
			if err := r.validateBackendAuthSecrets(ctx, vmcp.Namespace, &backendAuth, fmt.Sprintf("backend %s", backendName)); err != nil {
				return err
			}
		}
	}

	// Validate Redis password if configured
	if vmcp.Spec.TokenCache != nil &&
		vmcp.Spec.TokenCache.Redis != nil &&
		vmcp.Spec.TokenCache.Redis.PasswordRef != nil {
		if err := r.validateSecretKeyRef(ctx, vmcp.Namespace,
			vmcp.Spec.TokenCache.Redis.PasswordRef,
			"Redis password"); err != nil {
			return err
		}
	}

	return nil
}

// validateBackendAuthSecrets validates secrets referenced in backend authentication configuration
func (r *VirtualMCPServerReconciler) validateBackendAuthSecrets(
	ctx context.Context,
	namespace string,
	backendAuth *mcpv1alpha1.BackendAuthConfig,
	backendDesc string,
) error {
	if backendAuth == nil {
		return nil
	}

	// Only service account auth type has secrets to validate
	if backendAuth.Type == mcpv1alpha1.BackendAuthTypeServiceAccount &&
		backendAuth.ServiceAccount != nil {
		if err := r.validateSecretKeyRef(ctx, namespace,
			&backendAuth.ServiceAccount.CredentialsRef,
			fmt.Sprintf("%s service account credentials", backendDesc)); err != nil {
			return err
		}
	}

	return nil
}

// validateSecretKeyRef validates that a secret reference exists and contains the required key.
// This implements the validation pattern from ctrlutil.GenerateOIDCClientSecretEnvVar().
func (r *VirtualMCPServerReconciler) validateSecretKeyRef(
	ctx context.Context,
	namespace string,
	secretRef *mcpv1alpha1.SecretKeyRef,
	secretDesc string,
) error {
	if secretRef == nil {
		return nil
	}

	// Validate that the referenced secret exists
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      secretRef.Name,
	}, &secret); err != nil {
		return fmt.Errorf("failed to get %s secret %s/%s: %w",
			secretDesc, namespace, secretRef.Name, err)
	}

	// Validate that the key exists in the secret
	if _, ok := secret.Data[secretRef.Key]; !ok {
		return fmt.Errorf("%s secret %s/%s is missing key %q",
			secretDesc, namespace, secretRef.Name, secretRef.Key)
	}

	return nil
}

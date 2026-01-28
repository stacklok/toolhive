// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

// deploymentForMCPRemoteProxy returns a MCPRemoteProxy Deployment object
func (r *MCPRemoteProxyReconciler) deploymentForMCPRemoteProxy(
	ctx context.Context, proxy *mcpv1alpha1.MCPRemoteProxy, runConfigChecksum string,
) *appsv1.Deployment {
	ls := labelsForMCPRemoteProxy(proxy.Name)
	replicas := int32(1)

	// Build deployment components using helper functions
	args := r.buildContainerArgs()
	volumeMounts, volumes := r.buildVolumesForProxy(proxy)
	env := r.buildEnvVarsForProxy(ctx, proxy)
	resources := ctrlutil.BuildResourceRequirements(proxy.Spec.Resources)
	deploymentLabels, deploymentAnnotations := r.buildDeploymentMetadata(ls, proxy)
	deploymentTemplateLabels, deploymentTemplateAnnotations := r.buildPodTemplateMetadata(ls, proxy, runConfigChecksum)
	podSecurityContext, containerSecurityContext := r.buildSecurityContexts(ctx, proxy)

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        proxy.Name,
			Namespace:   proxy.Namespace,
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
					ServiceAccountName: serviceAccountNameForRemoteProxy(proxy),
					Containers: []corev1.Container{{
						Image:           getToolhiveRunnerImage(),
						Name:            "toolhive",
						Args:            args,
						Env:             env,
						VolumeMounts:    volumeMounts,
						Resources:       resources,
						Ports:           r.buildContainerPorts(proxy),
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

	if err := controllerutil.SetControllerReference(proxy, dep, r.Scheme); err != nil {
		ctxLogger := log.FromContext(ctx)
		ctxLogger.Error(err, "Failed to set controller reference for Deployment")
		return nil
	}
	return dep
}

// buildContainerArgs builds the container arguments for the proxy
func (*MCPRemoteProxyReconciler) buildContainerArgs() []string {
	// The third argument is required by proxyrunner command signature but is ignored
	// when RemoteURL is set (HTTPTransport.Setup returns early for remote servers)
	return []string{"run", "--foreground=true", "placeholder-for-remote-proxy"}
}

// buildVolumesForProxy builds volumes and volume mounts for the proxy
func (*MCPRemoteProxyReconciler) buildVolumesForProxy(
	proxy *mcpv1alpha1.MCPRemoteProxy,
) ([]corev1.VolumeMount, []corev1.Volume) {
	volumeMounts := []corev1.VolumeMount{}
	volumes := []corev1.Volume{}

	// Add RunConfig ConfigMap volume
	configMapName := fmt.Sprintf("%s-runconfig", proxy.Name)
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

	// Add authz config volume if needed
	authzVolumeMount, authzVolume := ctrlutil.GenerateAuthzVolumeConfig(proxy.Spec.AuthzConfig, proxy.Name)
	if authzVolumeMount != nil {
		volumeMounts = append(volumeMounts, *authzVolumeMount)
		volumes = append(volumes, *authzVolume)
	}

	return volumeMounts, volumes
}

// buildEnvVarsForProxy builds environment variables for the proxy container
func (r *MCPRemoteProxyReconciler) buildEnvVarsForProxy(
	ctx context.Context, proxy *mcpv1alpha1.MCPRemoteProxy,
) []corev1.EnvVar {
	env := []corev1.EnvVar{}

	// Add OpenTelemetry environment variables
	if proxy.Spec.Telemetry != nil && proxy.Spec.Telemetry.OpenTelemetry != nil {
		otelEnvVars := ctrlutil.GenerateOpenTelemetryEnvVars(proxy.Spec.Telemetry, proxy.Name, proxy.Namespace)
		env = append(env, otelEnvVars...)
	}

	// Add token exchange environment variables
	if proxy.Spec.ExternalAuthConfigRef != nil {
		tokenExchangeEnvVars, err := ctrlutil.GenerateTokenExchangeEnvVars(
			ctx,
			r.Client,
			proxy.Namespace,
			proxy.Spec.ExternalAuthConfigRef,
			ctrlutil.GetExternalAuthConfigByName,
		)
		if err != nil {
			ctxLogger := log.FromContext(ctx)
			ctxLogger.Error(err, "Failed to generate token exchange environment variables")
		} else {
			env = append(env, tokenExchangeEnvVars...)
		}

		// Add bearer token environment variables
		bearerTokenEnvVars, err := ctrlutil.GenerateBearerTokenEnvVar(
			ctx,
			r.Client,
			proxy.Namespace,
			proxy.Spec.ExternalAuthConfigRef,
			ctrlutil.GetExternalAuthConfigByName,
		)
		if err != nil {
			ctxLogger := log.FromContext(ctx)
			ctxLogger.Error(err, "Failed to generate bearer token environment variables")
		} else {
			env = append(env, bearerTokenEnvVars...)
		}
	}

	// Add OIDC client secret environment variable if using inline config with secretRef
	if proxy.Spec.OIDCConfig.Type == "inline" && proxy.Spec.OIDCConfig.Inline != nil {
		oidcClientSecretEnvVar, err := ctrlutil.GenerateOIDCClientSecretEnvVar(
			ctx, r.Client, proxy.Namespace, proxy.Spec.OIDCConfig.Inline.ClientSecretRef,
		)
		if err != nil {
			ctxLogger := log.FromContext(ctx)
			ctxLogger.Error(err, "Failed to generate OIDC client secret environment variable")
		} else if oidcClientSecretEnvVar != nil {
			env = append(env, *oidcClientSecretEnvVar)
		}
	}

	// Add user-specified environment variables
	if proxy.Spec.ResourceOverrides != nil && proxy.Spec.ResourceOverrides.ProxyDeployment != nil {
		for _, envVar := range proxy.Spec.ResourceOverrides.ProxyDeployment.Env {
			env = append(env, corev1.EnvVar{
				Name:  envVar.Name,
				Value: envVar.Value,
			})
		}
	}

	return ctrlutil.EnsureRequiredEnvVars(ctx, env)
}

// buildDeploymentMetadata builds deployment-level labels and annotations
func (*MCPRemoteProxyReconciler) buildDeploymentMetadata(
	baseLabels map[string]string, proxy *mcpv1alpha1.MCPRemoteProxy,
) (map[string]string, map[string]string) {
	deploymentLabels := baseLabels
	deploymentAnnotations := make(map[string]string)

	if proxy.Spec.ResourceOverrides != nil && proxy.Spec.ResourceOverrides.ProxyDeployment != nil {
		if proxy.Spec.ResourceOverrides.ProxyDeployment.Labels != nil {
			deploymentLabels = ctrlutil.MergeLabels(baseLabels, proxy.Spec.ResourceOverrides.ProxyDeployment.Labels)
		}
		if proxy.Spec.ResourceOverrides.ProxyDeployment.Annotations != nil {
			deploymentAnnotations = ctrlutil.MergeAnnotations(
				make(map[string]string), proxy.Spec.ResourceOverrides.ProxyDeployment.Annotations,
			)
		}
	}

	return deploymentLabels, deploymentAnnotations
}

// buildPodTemplateMetadata builds pod template labels and annotations.
//
// The runConfigChecksum parameter must be a non-empty SHA256 hash of the RunConfig.
// This checksum is added as an annotation to the pod template, which triggers
// Kubernetes to perform a rolling update when the configuration changes.
//
// User-specified overrides from ResourceOverrides.PodTemplateMetadataOverrides
// are merged after the checksum annotation is set.
func (*MCPRemoteProxyReconciler) buildPodTemplateMetadata(
	baseLabels map[string]string, proxy *mcpv1alpha1.MCPRemoteProxy, runConfigChecksum string,
) (map[string]string, map[string]string) {
	templateLabels := baseLabels
	templateAnnotations := make(map[string]string)

	// Add RunConfig checksum annotation to trigger pod rollout when config changes
	// This is critical for ensuring pods restart with updated configuration
	templateAnnotations = checksum.AddRunConfigChecksumToPodTemplate(templateAnnotations, runConfigChecksum)

	if proxy.Spec.ResourceOverrides != nil &&
		proxy.Spec.ResourceOverrides.ProxyDeployment != nil &&
		proxy.Spec.ResourceOverrides.ProxyDeployment.PodTemplateMetadataOverrides != nil {

		overrides := proxy.Spec.ResourceOverrides.ProxyDeployment.PodTemplateMetadataOverrides
		if overrides.Labels != nil {
			templateLabels = ctrlutil.MergeLabels(baseLabels, overrides.Labels)
		}
		if overrides.Annotations != nil {
			templateAnnotations = ctrlutil.MergeAnnotations(templateAnnotations, overrides.Annotations)
		}
	}

	return templateLabels, templateAnnotations
}

// buildSecurityContexts builds pod and container security contexts
func (r *MCPRemoteProxyReconciler) buildSecurityContexts(
	ctx context.Context, proxy *mcpv1alpha1.MCPRemoteProxy,
) (*corev1.PodSecurityContext, *corev1.SecurityContext) {
	if r.PlatformDetector == nil {
		r.PlatformDetector = ctrlutil.NewSharedPlatformDetector()
	}

	detectedPlatform, err := r.PlatformDetector.DetectPlatform(ctx)
	if err != nil {
		ctxLogger := log.FromContext(ctx)
		ctxLogger.Error(err, "Failed to detect platform, defaulting to Kubernetes", "mcpremoteproxy", proxy.Name)
	}

	securityBuilder := kubernetes.NewSecurityContextBuilder(detectedPlatform)
	return securityBuilder.BuildPodSecurityContext(), securityBuilder.BuildContainerSecurityContext()
}

// buildContainerPorts builds container port configuration
func (*MCPRemoteProxyReconciler) buildContainerPorts(proxy *mcpv1alpha1.MCPRemoteProxy) []corev1.ContainerPort {
	return []corev1.ContainerPort{{
		ContainerPort: int32(proxy.GetProxyPort()),
		Name:          "http",
		Protocol:      corev1.ProtocolTCP,
	}}
}

// serviceForMCPRemoteProxy returns a MCPRemoteProxy Service object
func (r *MCPRemoteProxyReconciler) serviceForMCPRemoteProxy(
	ctx context.Context, proxy *mcpv1alpha1.MCPRemoteProxy,
) *corev1.Service {
	ls := labelsForMCPRemoteProxy(proxy.Name)
	svcName := createProxyServiceName(proxy.Name)

	// Build service metadata with overrides
	serviceLabels, serviceAnnotations := r.buildServiceMetadata(ls, proxy)

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        svcName,
			Namespace:   proxy.Namespace,
			Labels:      serviceLabels,
			Annotations: serviceAnnotations,
		},
		Spec: corev1.ServiceSpec{
			Selector: ls,
			Ports: []corev1.ServicePort{{
				Port:       int32(proxy.GetProxyPort()),
				TargetPort: intstr.FromInt(int(proxy.GetProxyPort())),
				Protocol:   corev1.ProtocolTCP,
				Name:       "http",
			}},
		},
	}

	if err := controllerutil.SetControllerReference(proxy, svc, r.Scheme); err != nil {
		ctxLogger := log.FromContext(ctx)
		ctxLogger.Error(err, "Failed to set controller reference for Service")
		return nil
	}
	return svc
}

// buildServiceMetadata builds service labels and annotations
func (*MCPRemoteProxyReconciler) buildServiceMetadata(
	baseLabels map[string]string, proxy *mcpv1alpha1.MCPRemoteProxy,
) (map[string]string, map[string]string) {
	serviceLabels := baseLabels
	serviceAnnotations := make(map[string]string)

	if proxy.Spec.ResourceOverrides != nil && proxy.Spec.ResourceOverrides.ProxyService != nil {
		if proxy.Spec.ResourceOverrides.ProxyService.Labels != nil {
			serviceLabels = ctrlutil.MergeLabels(baseLabels, proxy.Spec.ResourceOverrides.ProxyService.Labels)
		}
		if proxy.Spec.ResourceOverrides.ProxyService.Annotations != nil {
			serviceAnnotations = ctrlutil.MergeAnnotations(
				make(map[string]string), proxy.Spec.ResourceOverrides.ProxyService.Annotations,
			)
		}
	}

	return serviceLabels, serviceAnnotations
}

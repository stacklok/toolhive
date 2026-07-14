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

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
	"github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp/headerforward/wirefmt"
)

const mcpRemoteProxyContainerName = "toolhive"

// deploymentForMCPRemoteProxy returns a MCPRemoteProxy Deployment object
func (r *MCPRemoteProxyReconciler) deploymentForMCPRemoteProxy(
	ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy, runConfigChecksum string,
) *appsv1.Deployment {
	ls := labelsForMCPRemoteProxy(proxy.Name)

	// Build deployment components using helper functions
	args := r.buildContainerArgs()
	volumeMounts, volumes := r.buildVolumesForProxy(proxy)
	r.addTelemetryCABundleVolumes(ctx, proxy, &volumes, &volumeMounts)
	if err := r.addOIDCCABundleVolumes(ctx, proxy, &volumes, &volumeMounts); err != nil {
		// Returning nil aborts the build so ensureDeployment requeues with backoff and
		// leaves the existing Deployment untouched, rather than building a CA-less pod
		// that would crash-loop and flip the RunConfig checksum (restart flap).
		log.FromContext(ctx).Error(err, "Failed to add OIDC CA bundle volumes")
		return nil
	}
	env := r.buildEnvVarsForProxy(ctx, proxy)

	// Add embedded auth server volumes and env vars. AuthServerRef takes precedence;
	// externalAuthConfigRef is used as a fallback (legacy path).
	configName := ctrlutil.EmbeddedAuthServerConfigName(proxy.Spec.ExternalAuthConfigRef, proxy.Spec.AuthServerRef)
	if configName != "" {
		authServerVolumes, authServerMounts, authServerEnvVars, err := ctrlutil.GenerateAuthServerConfigByName(
			ctx, r.Client, proxy.Namespace, configName,
		)
		if err != nil {
			log.FromContext(ctx).Error(err, "Failed to generate auth server configuration")
			return nil
		}
		volumes = append(volumes, authServerVolumes...)
		volumeMounts = append(volumeMounts, authServerMounts...)
		env = append(env, authServerEnvVars...)
	}
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
			// nil leaves the replica count to the apiserver default (1) on create
			// and to an HPA or other external controller thereafter; non-nil is
			// operator-owned and reconciled by deploymentNeedsUpdate.
			Replicas: proxy.Spec.Replicas,
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
					ImagePullSecrets:   r.imagePullSecretsForRemoteProxy(proxy),
					Containers: []corev1.Container{{
						Image:           getToolhiveRunnerImage(),
						Name:            mcpRemoteProxyContainerName,
						Args:            args,
						Env:             env,
						VolumeMounts:    volumeMounts,
						Resources:       resources,
						Ports:           r.buildContainerPorts(proxy),
						StartupProbe:    ctrlutil.BuildHealthProbe("/health", "http", 0, 5, 3, 18),
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

	if err := r.applyPodTemplateSpecToDeployment(ctx, proxy, dep); err != nil {
		log.FromContext(ctx).Error(err, "Failed to apply PodTemplateSpec to Deployment",
			"mcpremoteproxy", proxy.Name,
			"namespace", proxy.Namespace)
		return nil
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

// buildVolumesForProxy builds volumes and volume mounts for the proxy.
// Note: Embedded auth server volumes are added separately in deploymentForMCPRemoteProxy
// to avoid duplicate API calls.
func (*MCPRemoteProxyReconciler) buildVolumesForProxy(
	proxy *mcpv1beta1.MCPRemoteProxy,
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

	// Add authz config volume if needed (inline spec.authzConfig only).
	// A referenced MCPAuthzConfig (spec.authzConfigRef) is not mounted: it is
	// enforced via the authz config embedded in the RunConfig, not a file mount.
	authzVolumeMount, authzVolume := ctrlutil.GenerateAuthzVolumeConfig(proxy.Spec.AuthzConfig, proxy.Name)
	if authzVolumeMount != nil {
		volumeMounts = append(volumeMounts, *authzVolumeMount)
		volumes = append(volumes, *authzVolume)
	}

	return volumeMounts, volumes
}

// addTelemetryCABundleVolumes appends CA bundle volumes for the referenced MCPTelemetryConfig.
// Must be called from deploymentForMCPRemoteProxy where the client is available.
func (r *MCPRemoteProxyReconciler) addTelemetryCABundleVolumes(
	ctx context.Context,
	proxy *mcpv1beta1.MCPRemoteProxy,
	volumes *[]corev1.Volume,
	volumeMounts *[]corev1.VolumeMount,
) {
	if proxy.Spec.TelemetryConfigRef == nil {
		return
	}
	telCfg, err := ctrlutil.GetTelemetryConfigForMCPRemoteProxy(ctx, r.Client, proxy)
	if err != nil {
		log.FromContext(ctx).Error(err, "Failed to fetch MCPTelemetryConfig for CA bundle volume")
		return
	}
	if telCfg != nil {
		caVolumes, caMounts := ctrlutil.AddTelemetryCABundleVolumes(telCfg)
		*volumes = append(*volumes, caVolumes...)
		*volumeMounts = append(*volumeMounts, caMounts...)
	}
}

// addOIDCCABundleVolumes appends the CA bundle volume and mount for the referenced
// MCPOIDCConfig's inline CA bundle, so the runner pod can read the CA file at the
// path the RunConfig points it at (resolved.ThvCABundlePath). Mirrors MCPServer's
// OIDC CA bundle mount.
//
// A fetch error is returned (not swallowed) so the caller can abort the build: a
// transient failure must not produce a CA-less Deployment, which would flip the
// RunConfig checksum and crash-loop the pod (restart flap). The CA bundle reference
// content is validated separately in validateCABundleRef.
//
// Must be called from deploymentForMCPRemoteProxy where the client is available.
func (r *MCPRemoteProxyReconciler) addOIDCCABundleVolumes(
	ctx context.Context,
	proxy *mcpv1beta1.MCPRemoteProxy,
	volumes *[]corev1.Volume,
	volumeMounts *[]corev1.VolumeMount,
) error {
	if proxy.Spec.OIDCConfigRef == nil {
		return nil
	}
	oidcCfg, err := ctrlutil.GetOIDCConfigForServer(ctx, r.Client, proxy.Namespace, proxy.Spec.OIDCConfigRef)
	if err != nil {
		return fmt.Errorf("failed to fetch MCPOIDCConfig for CA bundle volume: %w", err)
	}
	if oidcCfg != nil {
		caVolumes, caMounts := ctrlutil.AddOIDCConfigRefCABundleVolumes(oidcCfg)
		*volumes = append(*volumes, caVolumes...)
		*volumeMounts = append(*volumeMounts, caMounts...)
	}
	return nil
}

// buildEnvVarsForProxy builds environment variables for the proxy container
func (r *MCPRemoteProxyReconciler) buildEnvVarsForProxy(
	ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy,
) []corev1.EnvVar {
	env := r.buildOIDCClientSecretEnvVars(ctx, proxy)

	// Add token exchange environment variables
	// Note: Embedded auth server env vars are added separately in deploymentForMCPRemoteProxy
	// to avoid duplicate API calls.
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

		// Add OBO secret environment variables. Dispatched through the
		// registered OBO handler; inert (no env vars) in builds without one.
		// This function feeds both the deployment builder and containerNeedsUpdate,
		// so builder/drift symmetry is automatic.
		oboEnvVars, err := ctrlutil.AddOBOSecretEnvVars(
			ctx,
			r.Client,
			proxy.Namespace,
			proxy.Spec.ExternalAuthConfigRef,
		)
		if err != nil {
			logOBOSecretEnvVarError(ctx, err)
		} else {
			env = append(env, oboEnvVars...)
		}
	}

	// Add header forward secret environment variables
	if proxy.Spec.HeaderForward != nil && len(proxy.Spec.HeaderForward.AddHeadersFromSecret) > 0 {
		// Set secrets provider to environment so runner uses environment variables for secrets.
		// This is needed because header forward secrets use the ToolHive secrets provider
		// (unlike token exchange and OIDC secrets which read directly from os.Getenv).
		// The EnvironmentProvider reads env vars with the TOOLHIVE_SECRET_ prefix.
		env = append(env, corev1.EnvVar{
			Name:  "TOOLHIVE_SECRETS_PROVIDER",
			Value: "environment",
		})
		headerEnvVars := buildHeaderForwardSecretEnvVars(proxy)
		env = append(env, headerEnvVars...)
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

	// Add THV_SESSION_REDIS_PASSWORD when sessionStorage uses a passwordRef.
	// The non-sensitive parts (address/db/keyPrefix) are populated into the
	// runconfig by populateScalingConfigForRemoteProxy; the password is
	// injected separately so it never lands in the ConfigMap.
	env = append(env, buildRedisPasswordEnvVarForRemoteProxy(proxy)...)

	return ctrlutil.EnsureRequiredEnvVars(ctx, env)
}

// buildRedisPasswordEnvVarForRemoteProxy returns the THV_SESSION_REDIS_PASSWORD
// env var sourced from spec.sessionStorage.passwordRef when sessionStorage uses
// the redis provider; returns nil otherwise. Mirrors VirtualMCPServer's
// buildRedisPasswordEnvVar in virtualmcpserver_deployment.go.
func buildRedisPasswordEnvVarForRemoteProxy(proxy *mcpv1beta1.MCPRemoteProxy) []corev1.EnvVar {
	if proxy.Spec.SessionStorage == nil ||
		proxy.Spec.SessionStorage.Provider != mcpv1beta1.SessionStorageProviderRedis ||
		proxy.Spec.SessionStorage.PasswordRef == nil {
		return nil
	}
	return []corev1.EnvVar{{
		Name: session.RedisPasswordEnvVar,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: proxy.Spec.SessionStorage.PasswordRef.Name,
				},
				Key: proxy.Spec.SessionStorage.PasswordRef.Key,
			},
		},
	}}
}

// buildOIDCClientSecretEnvVars returns OIDC client secret env vars when the proxy
// references an MCPOIDCConfig with an inline client secret. Returns nil otherwise.
func (r *MCPRemoteProxyReconciler) buildOIDCClientSecretEnvVars(
	ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy,
) []corev1.EnvVar {
	if proxy.Spec.OIDCConfigRef == nil {
		return nil
	}
	oidcCfg, err := ctrlutil.GetOIDCConfigForServer(ctx, r.Client, proxy.Namespace, proxy.Spec.OIDCConfigRef)
	if err != nil {
		log.FromContext(ctx).Error(err, "Failed to fetch MCPOIDCConfig for client secret")
		return nil
	}
	if oidcCfg == nil ||
		oidcCfg.Spec.Type != mcpv1beta1.MCPOIDCConfigTypeInline ||
		oidcCfg.Spec.Inline == nil {
		return nil
	}
	envVar, err := ctrlutil.GenerateOIDCClientSecretEnvVar(
		ctx, r.Client, proxy.Namespace, oidcCfg.Spec.Inline.ClientSecretRef,
	)
	if err != nil {
		log.FromContext(ctx).Error(err, "Failed to generate OIDC client secret environment variable")
		return nil
	}
	if envVar == nil {
		return nil
	}
	return []corev1.EnvVar{*envVar}
}

// buildHeaderForwardSecretEnvVars builds environment variables for header forward secrets.
// Each secret is mounted as an env var using Kubernetes SecretKeyRef, with a name following
// the TOOLHIVE_SECRET_<identifier> pattern expected by the secrets.EnvironmentProvider.
func buildHeaderForwardSecretEnvVars(proxy *mcpv1beta1.MCPRemoteProxy) []corev1.EnvVar {
	var envVars []corev1.EnvVar

	for _, headerSecret := range proxy.Spec.HeaderForward.AddHeadersFromSecret {
		if headerSecret.ValueSecretRef == nil {
			continue
		}

		// Generate env var name following the TOOLHIVE_SECRET_ pattern
		envVarName, _ := wirefmt.SecretEnvVarName(proxy.Name, headerSecret.HeaderName)

		envVars = append(envVars, corev1.EnvVar{
			Name: envVarName,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: headerSecret.ValueSecretRef.Name,
					},
					Key: headerSecret.ValueSecretRef.Key,
				},
			},
		})
	}

	return envVars
}

// buildDeploymentMetadata builds deployment-level labels and annotations
func (*MCPRemoteProxyReconciler) buildDeploymentMetadata(
	baseLabels map[string]string, proxy *mcpv1beta1.MCPRemoteProxy,
) (map[string]string, map[string]string) {
	deploymentLabels := baseLabels
	deploymentAnnotations := make(map[string]string)

	if proxy.Spec.PodTemplateSpec != nil && len(proxy.Spec.PodTemplateSpec.Raw) > 0 {
		hash, err := checksum.HashRawJSON(proxy.Spec.PodTemplateSpec.Raw)
		if err == nil {
			deploymentAnnotations[podTemplateSpecHashAnnotation] = hash
		}
	}

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

// applyPodTemplateSpecToDeployment applies user-provided PodTemplateSpec customizations
// to the generated MCPRemoteProxy Deployment using Kubernetes strategic merge semantics.
func (*MCPRemoteProxyReconciler) applyPodTemplateSpecToDeployment(
	ctx context.Context,
	proxy *mcpv1beta1.MCPRemoteProxy,
	deployment *appsv1.Deployment,
) error {
	if proxy.Spec.PodTemplateSpec == nil || len(proxy.Spec.PodTemplateSpec.Raw) == 0 {
		return nil
	}

	if _, err := ctrlutil.NewPodTemplateSpecBuilder(proxy.Spec.PodTemplateSpec, mcpRemoteProxyContainerName); err != nil {
		return fmt.Errorf("failed to build PodTemplateSpec: %w", err)
	}

	merged, err := ctrlutil.ApplyPodTemplateSpecPatch(deployment.Spec.Template, proxy.Spec.PodTemplateSpec.Raw)
	if err != nil {
		return err
	}
	deployment.Spec.Template = merged

	log.FromContext(ctx).V(1).Info("Applied PodTemplateSpec customizations to deployment",
		"mcpremoteproxy", proxy.Name,
		"namespace", proxy.Namespace)
	return nil
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
	baseLabels map[string]string, proxy *mcpv1beta1.MCPRemoteProxy, runConfigChecksum string,
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
	ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy,
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
func (*MCPRemoteProxyReconciler) buildContainerPorts(proxy *mcpv1beta1.MCPRemoteProxy) []corev1.ContainerPort {
	return []corev1.ContainerPort{{
		ContainerPort: int32(proxy.GetProxyPort()),
		Name:          "http",
		Protocol:      corev1.ProtocolTCP,
	}}
}

// serviceForMCPRemoteProxy returns a MCPRemoteProxy Service object
func (r *MCPRemoteProxyReconciler) serviceForMCPRemoteProxy(
	ctx context.Context, proxy *mcpv1beta1.MCPRemoteProxy,
) *corev1.Service {
	ls := labelsForMCPRemoteProxy(proxy.Name)
	svcName := createProxyServiceName(proxy.Name)

	// Build service metadata with overrides
	serviceLabels, serviceAnnotations := r.buildServiceMetadata(ls, proxy)

	sessionAffinity := func() corev1.ServiceAffinity {
		if proxy.Spec.SessionAffinity != "" {
			return corev1.ServiceAffinity(proxy.Spec.SessionAffinity)
		}
		return corev1.ServiceAffinityClientIP
	}()

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        svcName,
			Namespace:   proxy.Namespace,
			Labels:      serviceLabels,
			Annotations: serviceAnnotations,
		},
		Spec: corev1.ServiceSpec{
			Selector:        ls,
			SessionAffinity: sessionAffinity,
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
	baseLabels map[string]string, proxy *mcpv1beta1.MCPRemoteProxy,
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

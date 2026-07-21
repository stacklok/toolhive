// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
	vmcptypes "github.com/stacklok/toolhive/pkg/vmcp"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/headerforward/wirefmt"
	"github.com/stacklok/toolhive/pkg/vmcp/workloads"
)

const (
	// imagePullRefsHashAnnotation tracks the SHA256 hash of the desired
	// imagePullSecrets list — chart-level defaults merged with
	// vmcp.Spec.ImagePullSecrets — used by buildDeploymentMetadataForVmcp.
	// Mirrors the podTemplateSpecHashAnnotation pattern to detect drift on
	// these inputs without re-running strategic-merge logic during
	// reconciliation. Combined with podTemplateSpecHashAnnotation (which
	// covers any imagePullSecrets the user added under
	// spec.podTemplateSpec.spec.imagePullSecrets), this is sufficient to
	// detect every input that influences the deployed PodSpec.ImagePullSecrets.
	imagePullRefsHashAnnotation = "toolhive.stacklok.io/imagepullsecrets-hash"

	// Log level configuration
	logLevelDebug = "debug" // Debug log level value

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

	// Graceful shutdown configuration
	vmcpTerminationGracePeriodSeconds = int64(30) // seconds - allow in-flight requests to complete

	// Default resource requirements for VirtualMCPServer vmcp container
	// These provide sensible defaults that can be overridden via PodTemplateSpec
	vmcpDefaultCPURequest    = "100m"
	vmcpDefaultMemoryRequest = "128Mi"
	vmcpDefaultCPULimit      = "500m"
	vmcpDefaultMemoryLimit   = "512Mi"
)

// RBAC rules for VirtualMCPServer service account in inline mode
// These minimal rules only allow vMCP to:
// - Read its own VirtualMCPServer spec
// - Update VirtualMCPServer status (via K8sReporter)
// No access to secrets or other Kubernetes resources since config is provided inline
var vmcpInlineRBACRules = []rbacv1.PolicyRule{
	{
		APIGroups: []string{"toolhive.stacklok.dev"},
		Resources: []string{"virtualmcpservers"},
		Verbs:     []string{"get"},
	},
	{
		APIGroups: []string{"toolhive.stacklok.dev"},
		Resources: []string{"virtualmcpservers/status"},
		Verbs:     []string{"update", "patch"},
	},
}

// RBAC rules for VirtualMCPServer service account in discovered mode
// These rules allow vMCP to:
// - Discover backends and configurations at runtime (read secrets, configmaps, and MCP resources)
// - Update VirtualMCPServer status (via K8sReporter)
var vmcpDiscoveredRBACRules = []rbacv1.PolicyRule{
	{
		APIGroups: []string{""},
		Resources: []string{"configmaps", "secrets"},
		Verbs:     []string{"get", "list", "watch"},
	},
	{
		APIGroups: []string{"toolhive.stacklok.dev"},
		Resources: []string{
			"mcpgroups", "mcpservers", "mcpremoteproxies", "mcpserverentries",
			"mcpexternalauthconfigs", "mcptoolconfigs",
		},
		Verbs: []string{"get", "list", "watch"},
	},
	{
		APIGroups: []string{"toolhive.stacklok.dev"},
		Resources: []string{"virtualmcpservers"},
		Verbs:     []string{"get"},
	},
	{
		APIGroups: []string{"toolhive.stacklok.dev"},
		Resources: []string{"virtualmcpservers/status"},
		Verbs:     []string{"update", "patch"},
	},
}

// deploymentForVirtualMCPServer returns a VirtualMCPServer Deployment object.
// telemetryCfg is the already-fetched MCPTelemetryConfig (nil when not referenced),
// used for CA bundle volumes and OpenTelemetry env vars without redundant API calls.
func (r *VirtualMCPServerReconciler) deploymentForVirtualMCPServer(
	ctx context.Context,
	vmcp *mcpv1beta1.VirtualMCPServer,
	vmcpConfigChecksum string,
	telemetryCfg *mcpv1beta1.MCPTelemetryConfig,
	typedWorkloads []workloads.TypedWorkload,
) *appsv1.Deployment {
	ls := labelsForVirtualMCPServer(vmcp.Name)

	// Build deployment components using helper functions
	args := r.buildContainerArgsForVmcp(vmcp)
	volumeMounts, volumes, err := r.buildVolumesForVmcp(ctx, vmcp)
	if err != nil {
		log.FromContext(ctx).Error(err, "Failed to build volumes for VirtualMCPServer")
		return nil
	}
	env, err := r.buildEnvVarsForVmcp(ctx, vmcp, telemetryCfg, typedWorkloads)
	if err != nil {
		log.FromContext(ctx).Error(err, "Failed to build env vars for VirtualMCPServer")
		return nil
	}

	// Add CA bundle volumes for MCPServerEntry backends with caBundleRef
	caVolumes, caMounts, err := r.buildCABundleVolumesForEntries(ctx, vmcp.Namespace, typedWorkloads)
	if err != nil {
		log.FromContext(ctx).Error(err, "Failed to build CA bundle volumes for MCPServerEntries")
		return nil
	}
	volumes = append(volumes, caVolumes...)
	volumeMounts = append(volumeMounts, caMounts...)

	// Add telemetry CA bundle volumes from the pre-fetched MCPTelemetryConfig
	if telemetryCfg != nil {
		telVolumes, telMounts := ctrlutil.AddTelemetryCABundleVolumes(telemetryCfg)
		volumes = append(volumes, telVolumes...)
		volumeMounts = append(volumeMounts, telMounts...)
	}

	// Add embedded auth server volumes if configured (inline config). The matching
	// env vars are injected by buildEnvVarsForVmcp above so the drift check stays
	// symmetric with what is built here (see #5616).
	if vmcp.Spec.AuthServerConfig != nil {
		authServerVolumes, authServerMounts := ctrlutil.GenerateAuthServerVolumes(vmcp.Spec.AuthServerConfig)
		volumes = append(volumes, authServerVolumes...)
		volumeMounts = append(volumeMounts, authServerMounts...)
	}
	deploymentLabels, deploymentAnnotations := r.buildDeploymentMetadataForVmcp(ls, vmcp)
	deploymentTemplateLabels, deploymentTemplateAnnotations := r.buildPodTemplateMetadata(ls, vmcp, vmcpConfigChecksum)
	podSecurityContext, containerSecurityContext := r.buildSecurityContextsForVmcp(ctx, vmcp)
	serviceAccountName := r.serviceAccountNameForVmcp(vmcp)

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        vmcp.Name,
			Namespace:   vmcp.Namespace,
			Labels:      deploymentLabels,
			Annotations: deploymentAnnotations,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: vmcp.Spec.Replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: ls,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      deploymentTemplateLabels,
					Annotations: deploymentTemplateAnnotations,
				},
				Spec: corev1.PodSpec{
					TerminationGracePeriodSeconds: int64Ptr(vmcpTerminationGracePeriodSeconds),
					ServiceAccountName:            serviceAccountName,
					ImagePullSecrets:              r.imagePullSecretsForVMCP(vmcp),
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
							"/readyz", "http",
							vmcpReadinessInitialDelay, vmcpReadinessPeriod, vmcpReadinessTimeout, vmcpReadinessFailures,
						),
						SecurityContext: containerSecurityContext,
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse(vmcpDefaultCPURequest),
								corev1.ResourceMemory: resource.MustParse(vmcpDefaultMemoryRequest),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse(vmcpDefaultCPULimit),
								corev1.ResourceMemory: resource.MustParse(vmcpDefaultMemoryLimit),
							},
						},
					}},
					Volumes:         volumes,
					SecurityContext: podSecurityContext,
				},
			},
		},
	}

	// Apply user-provided PodTemplateSpec customizations if present
	if vmcp.Spec.PodTemplateSpec != nil && vmcp.Spec.PodTemplateSpec.Raw != nil {
		if err := r.applyPodTemplateSpecToDeployment(ctx, vmcp, dep); err != nil {
			ctxLogger := log.FromContext(ctx)
			ctxLogger.Error(err, "Failed to apply PodTemplateSpec to Deployment")
			// Return nil to block deployment creation until PodTemplateSpec is fixed
			return nil
		}
	}

	if err := controllerutil.SetControllerReference(vmcp, dep, r.Scheme); err != nil {
		ctxLogger := log.FromContext(ctx)
		ctxLogger.Error(err, "Failed to set controller reference for Deployment")
		return nil
	}
	return dep
}

// buildContainerArgsForVmcp builds the container arguments for vmcp
func (*VirtualMCPServerReconciler) buildContainerArgsForVmcp(
	vmcp *mcpv1beta1.VirtualMCPServer,
) []string {
	args := []string{
		"serve",
		"--config=/etc/vmcp-config/config.yaml",
		"--host=0.0.0.0", // Listen on all interfaces for Kubernetes service routing
		"--port=4483",    // Standard vmcp port
	}

	// Add --debug flag if log level is set to debug
	// Note: vmcp binary currently only supports --debug flag, not other log levels
	// The flag must be passed at startup because logging is initialized early in the process
	if vmcp.Spec.Config.Operational != nil && vmcp.Spec.Config.Operational.LogLevel == logLevelDebug {
		args = append(args, "--debug")
	}

	return args
}

// buildVolumesForVmcp builds volumes and volume mounts for vmcp
func (r *VirtualMCPServerReconciler) buildVolumesForVmcp(
	ctx context.Context,
	vmcp *mcpv1beta1.VirtualMCPServer,
) ([]corev1.VolumeMount, []corev1.Volume, error) {
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

	// Add OIDC CA bundle volume if configured
	if vmcp.Spec.IncomingAuth != nil && vmcp.Spec.IncomingAuth.OIDCConfigRef != nil {
		oidcCfg, err := ctrlutil.GetOIDCConfigForServer(
			ctx, r.Client, vmcp.Namespace, vmcp.Spec.IncomingAuth.OIDCConfigRef)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get MCPOIDCConfig %s for CA bundle: %w",
				vmcp.Spec.IncomingAuth.OIDCConfigRef.Name, err)
		}
		if oidcCfg != nil {
			caVolumes, caMounts := ctrlutil.AddOIDCConfigRefCABundleVolumes(oidcCfg)
			volumes = append(volumes, caVolumes...)
			volumeMounts = append(volumeMounts, caMounts...)
		}
	}

	// TODO: Add volumes for composite tool definitions from VirtualMCPCompositeToolDefinition refs

	return volumeMounts, volumes, nil
}

// buildEnvVarsForVmcp builds environment variables for the vmcp container.
// telemetryCfg is the already-fetched MCPTelemetryConfig (nil when not referenced).
func (r *VirtualMCPServerReconciler) buildEnvVarsForVmcp(
	ctx context.Context,
	vmcp *mcpv1beta1.VirtualMCPServer,
	telemetryCfg *mcpv1beta1.MCPTelemetryConfig,
	typedWorkloads []workloads.TypedWorkload,
) ([]corev1.EnvVar, error) {
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

	// Mount OIDC client secret
	oidcEnv, err := r.buildOIDCEnvVars(ctx, vmcp)
	if err != nil {
		return nil, fmt.Errorf("failed to build OIDC env vars: %w", err)
	}
	env = append(env, oidcEnv...)

	// Mount outgoing auth secrets
	env = append(env, r.buildOutgoingAuthEnvVars(ctx, vmcp, typedWorkloads)...)

	// Mount per-(entry, header) env vars for MCPServerEntry headerForward.
	// Plaintext entries are emitted as literal-value env vars; secret entries
	// as valueFrom.secretKeyRef. The vMCP runtime walks the well-known prefixes
	// at startup to reconstruct the per-backend HeaderForwardConfig in static
	// mode (issue #4996).
	headerEnv, err := r.buildHeaderForwardEnvVarsForEntries(ctx, vmcp.Namespace, typedWorkloads)
	if err != nil {
		return nil, fmt.Errorf("failed to build header forward env vars: %w", err)
	}
	env = append(env, headerEnv...)

	// Always mount HMAC secret for session token binding.
	env = append(env, r.buildHMACSecretEnvVar(vmcp))

	// Mount Redis password secret when session storage provider is Redis.
	env = append(env, r.buildRedisPasswordEnvVar(vmcp)...)

	// Mount OpenTelemetry env vars (resource attributes, sensitive headers) from the pre-fetched MCPTelemetryConfig
	if telemetryCfg != nil && vmcp.Spec.TelemetryConfigRef != nil {
		otelEnv := ctrlutil.GenerateOpenTelemetryEnvVarsFromRef(
			telemetryCfg, vmcp.Spec.TelemetryConfigRef, vmcp.Name, vmcp.Namespace)
		env = append(env, otelEnv...)
	}

	// Mount embedded auth server upstream client secrets (and Redis ACL creds).
	// This must live here, not only in deploymentForVirtualMCPServer, so that the
	// env-var drift check in containerNeedsUpdate compares against the same set.
	// Otherwise the live container carries these env vars but the expected set
	// does not, reflect.DeepEqual never matches, and the operator updates the
	// Deployment on every reconcile (see #5616).
	if vmcp.Spec.AuthServerConfig != nil {
		env = append(env, ctrlutil.GenerateAuthServerEnvVars(vmcp.Spec.AuthServerConfig)...)
	}

	return ctrlutil.EnsureRequiredEnvVars(ctx, env), nil
}

// buildOIDCEnvVars builds environment variables for OIDC client secret mounting.
func (r *VirtualMCPServerReconciler) buildOIDCEnvVars(
	ctx context.Context, vmcp *mcpv1beta1.VirtualMCPServer,
) ([]corev1.EnvVar, error) {
	var env []corev1.EnvVar

	if vmcp.Spec.IncomingAuth == nil {
		return env, nil
	}

	// MCPOIDCConfig inline client secret
	if vmcp.Spec.IncomingAuth.OIDCConfigRef != nil {
		oidcCfg, err := ctrlutil.GetOIDCConfigForServer(
			ctx, r.Client, vmcp.Namespace, vmcp.Spec.IncomingAuth.OIDCConfigRef)
		if err != nil {
			return nil, fmt.Errorf("failed to get MCPOIDCConfig %s for client secret: %w",
				vmcp.Spec.IncomingAuth.OIDCConfigRef.Name, err)
		}
		if oidcCfg != nil &&
			oidcCfg.Spec.Type == mcpv1beta1.MCPOIDCConfigTypeInline &&
			oidcCfg.Spec.Inline != nil &&
			oidcCfg.Spec.Inline.ClientSecretRef != nil {
			env = append(env, corev1.EnvVar{
				Name: "VMCP_OIDC_CLIENT_SECRET",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: oidcCfg.Spec.Inline.ClientSecretRef.Name,
						},
						Key: oidcCfg.Spec.Inline.ClientSecretRef.Key,
					},
				},
			})
		}
	}

	return env, nil
}

// buildHMACSecretEnvVar builds environment variable for HMAC secret mounting.
// This secret is used for session token binding in Session Management V2.
// The operator automatically generates and manages this secret if it doesn't exist.
func (*VirtualMCPServerReconciler) buildHMACSecretEnvVar(vmcp *mcpv1beta1.VirtualMCPServer) corev1.EnvVar {
	secretName := fmt.Sprintf("%s-hmac-secret", vmcp.Name)

	return corev1.EnvVar{
		Name: "VMCP_SESSION_HMAC_SECRET",
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: secretName,
				},
				Key: "hmac-secret",
			},
		},
	}
}

// buildRedisPasswordEnvVar returns the THV_SESSION_REDIS_PASSWORD env var when
// sessionStorage.provider == "redis" and passwordRef is set, or when
// TOOLHIVE_DEFAULT_REDIS_SECRET_NAME is set via the global default.
func (*VirtualMCPServerReconciler) buildRedisPasswordEnvVar(vmcp *mcpv1beta1.VirtualMCPServer) []corev1.EnvVar {
	if vmcp.Spec.SessionStorage != nil {
		if vmcp.Spec.SessionStorage.Provider == mcpv1beta1.SessionStorageProviderRedis &&
			vmcp.Spec.SessionStorage.PasswordRef != nil {
			return []corev1.EnvVar{{
				Name: vmcpconfig.RedisPasswordEnvVar,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: vmcp.Spec.SessionStorage.PasswordRef.Name,
						},
						Key: vmcp.Spec.SessionStorage.PasswordRef.Key,
					},
				},
			}}
		}
		// spec.sessionStorage was set explicitly — never fall through to the
		// global default regardless of provider.
		return nil
	}

	def := ctrlutil.ReadDefaultRedisConfig()
	if def == nil || def.SecretName == "" {
		return nil
	}
	return []corev1.EnvVar{{
		Name: vmcpconfig.RedisPasswordEnvVar,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: def.SecretName,
				},
				Key: def.SecretKey,
			},
		},
	}}
}

// buildOutgoingAuthEnvVars builds environment variables for outgoing auth secrets.
func (r *VirtualMCPServerReconciler) buildOutgoingAuthEnvVars(
	ctx context.Context,
	vmcp *mcpv1beta1.VirtualMCPServer,
	typedWorkloads []workloads.TypedWorkload,
) []corev1.EnvVar {
	var env []corev1.EnvVar

	if vmcp.Spec.OutgoingAuth == nil {
		return env
	}

	// Mount secrets from discovered ExternalAuthConfigs (discovered mode)
	if vmcp.Spec.OutgoingAuth.Source == OutgoingAuthSourceDiscovered {
		discoveredSecrets := r.discoverExternalAuthConfigSecrets(ctx, vmcp, typedWorkloads)
		env = append(env, discoveredSecrets...)
	}

	// Mount secrets from inline ExternalAuthConfigRefs
	if vmcp.Spec.OutgoingAuth.Backends != nil {
		inlineSecrets := r.discoverInlineExternalAuthConfigSecrets(ctx, vmcp)
		env = append(env, inlineSecrets...)
	}

	// Mount secret from Default ExternalAuthConfigRef
	if vmcp.Spec.OutgoingAuth.Default != nil && vmcp.Spec.OutgoingAuth.Default.ExternalAuthConfigRef != nil {
		defaultSecrets, err := r.getExternalAuthConfigSecretEnvVars(
			ctx, vmcp.Namespace, vmcp.Spec.OutgoingAuth.Default.ExternalAuthConfigRef.Name)
		if err != nil {
			ctxLogger := log.FromContext(ctx)
			ctxLogger.V(1).Info("Failed to get Default ExternalAuthConfig secret, continuing without it",
				"error", err)
		} else {
			env = append(env, defaultSecrets...)
		}
	}

	return env
}

// buildHeaderForwardEnvVarsForEntries iterates entry-type workloads and emits
// header-forward env vars on the vMCP pod for each entry that declares
// spec.headerForward.
//
// One JSON-encoded manifest env var per backend carries the full
// HeaderForwardConfig with original header names preserved
// (TOOLHIVE_HEADER_FORWARD_<NORMALIZED_ENTRY>). Plaintext header values
// appear inline in the JSON; secret-backed entries carry only the secret
// identifier — the Secret value rides a sibling
// TOOLHIVE_SECRET_HEADER_FORWARD_<HEADER>_<ENTRY> env var emitted via
// valueFrom.secretKeyRef so the value never enters the operator's view of
// the world and resolves at request time inside resolveHeaderForward via
// secrets.EnvironmentProvider.
//
// Scoping by entry name prevents collisions when multiple entries in the
// group declare the same header name.
//
// Returns (nil, nil) when no entries declare any HeaderForward — the common
// case, producing no env-var churn on the Deployment spec.
func (r *VirtualMCPServerReconciler) buildHeaderForwardEnvVarsForEntries(
	ctx context.Context,
	namespace string,
	typedWorkloads []workloads.TypedWorkload,
) ([]corev1.EnvVar, error) {
	// Early return if no MCPServerEntry workloads to avoid unnecessary API calls.
	hasEntries := false
	for _, workload := range typedWorkloads {
		if workload.Type == workloads.WorkloadTypeMCPServerEntry {
			hasEntries = true
			break
		}
	}
	if !hasEntries {
		return nil, nil
	}

	entryMap, err := r.listMCPServerEntriesAsMap(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to list MCPServerEntries: %w", err)
	}

	var envVars []corev1.EnvVar
	for _, workload := range typedWorkloads {
		if workload.Type != workloads.WorkloadTypeMCPServerEntry {
			continue
		}
		entry, found := entryMap[workload.Name]
		if !found || entry.Spec.HeaderForward == nil {
			continue
		}

		manifest, err := buildHeaderForwardManifestForEntry(entry)
		if err != nil {
			return nil, fmt.Errorf("failed to build header-forward manifest for entry %q: %w", entry.Name, err)
		}
		if manifest != "" {
			manifestVarName, _ := wirefmt.ManifestEnvVarName(entry.Name)
			envVars = append(envVars, corev1.EnvVar{
				Name:  manifestVarName,
				Value: manifest,
			})
		}

		// Secret-backed headers — emit valueFrom.secretKeyRef env vars so
		// the resolved Secret value never enters the operator's view of the
		// world. AddHeadersFromSecret is a slice in the v1beta1 API so the
		// order is already deterministic from the user's manifest.
		for _, ref := range entry.Spec.HeaderForward.AddHeadersFromSecret {
			if ref.ValueSecretRef == nil {
				continue
			}
			envVarName, _ := wirefmt.SecretEnvVarName(entry.Name, ref.HeaderName)
			envVars = append(envVars, corev1.EnvVar{
				Name: envVarName,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: ref.ValueSecretRef.Name,
						},
						Key: ref.ValueSecretRef.Key,
					},
				},
			})
		}
	}

	// Sort the resulting env vars by Name. The Kubernetes informer cache
	// returns items in non-deterministic order (Go map iteration), so
	// without sorting the env vars appear in a different sequence on each
	// reconcile. reflect.DeepEqual in containerNeedsUpdate is order-
	// sensitive, so non-deterministic ordering causes a continuous
	// deployment update loop. Mirrors the pattern in
	// discoverExternalAuthConfigSecrets / discoverInlineExternalAuthConfigSecrets.
	sort.Slice(envVars, func(i, j int) bool {
		return envVars[i].Name < envVars[j].Name
	})

	return envVars, nil
}

// buildHeaderForwardManifestForEntry builds the JSON-encoded manifest for
// a single MCPServerEntry. AddHeadersFromSecret values carry the secret
// identifier (HEADER_FORWARD_<H>_<OWNER>) the runtime hands to
// secrets.EnvironmentProvider at request time; the resolved Secret value
// itself never appears in the JSON.
//
// Returns the empty string when the entry has no plaintext or
// secret-backed entries — the caller skips emitting the env var in that
// case to keep Deployment specs minimal.
func buildHeaderForwardManifestForEntry(entry *mcpv1beta1.MCPServerEntry) (string, error) {
	if entry == nil || entry.Spec.HeaderForward == nil {
		return "", nil
	}
	src := entry.Spec.HeaderForward

	// Build vmcp.HeaderForwardConfig directly so the JSON shape is the
	// runtime contract — there is no intermediate operator-side type to
	// drift away from it. Header keys preserve original casing.
	manifest := vmcptypes.HeaderForwardConfig{
		AddPlaintextHeaders: src.AddPlaintextHeaders,
	}
	for _, ref := range src.AddHeadersFromSecret {
		if ref.ValueSecretRef == nil {
			continue
		}
		_, identifier := wirefmt.SecretEnvVarName(entry.Name, ref.HeaderName)
		if manifest.AddHeadersFromSecret == nil {
			manifest.AddHeadersFromSecret = make(map[string]string)
		}
		manifest.AddHeadersFromSecret[ref.HeaderName] = identifier
	}

	if len(manifest.AddPlaintextHeaders) == 0 && len(manifest.AddHeadersFromSecret) == 0 {
		return "", nil
	}

	// json.Marshal sorts map keys alphabetically by default, giving
	// deterministic Deployment-spec rendering across reconciles regardless
	// of Go's randomized map iteration.
	out, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("marshal manifest: %w", err)
	}
	return string(out), nil
}

// discoverExternalAuthConfigSecrets discovers ExternalAuthConfigs from workloads in the group
// and returns environment variables for their client secrets. This is used for discovered mode.
func (r *VirtualMCPServerReconciler) discoverExternalAuthConfigSecrets(
	ctx context.Context,
	vmcp *mcpv1beta1.VirtualMCPServer,
	typedWorkloads []workloads.TypedWorkload,
) []corev1.EnvVar {
	ctxLogger := log.FromContext(ctx)
	var envVars []corev1.EnvVar
	seenConfigs := make(map[string]bool) // Track which ExternalAuthConfigs we've already processed

	// Build maps of MCPServers and MCPRemoteProxies for efficient lookup
	mcpServerMap, err := r.listMCPServersAsMap(ctx, vmcp.Namespace)
	if err != nil {
		ctxLogger.Error(err, "Failed to list MCPServers")
		return envVars
	}

	mcpRemoteProxyMap, err := r.listMCPRemoteProxiesAsMap(ctx, vmcp.Namespace)
	if err != nil {
		ctxLogger.Error(err, "Failed to list MCPRemoteProxies")
		return envVars
	}

	mcpServerEntryMap, err := r.listMCPServerEntriesAsMap(ctx, vmcp.Namespace)
	if err != nil {
		ctxLogger.Error(err, "Failed to list MCPServerEntries")
		return envVars
	}

	// Discover ExternalAuthConfigs from workloads (MCPServers, MCPRemoteProxies, and MCPServerEntries)
	for _, workloadInfo := range typedWorkloads {
		configName := r.getExternalAuthConfigNameFromWorkload(
			workloadInfo, mcpServerMap, mcpRemoteProxyMap, mcpServerEntryMap)
		if configName == "" {
			continue
		}

		// Skip if we've already processed this ExternalAuthConfig
		if seenConfigs[configName] {
			continue
		}
		seenConfigs[configName] = true

		// Get the secret env var for this ExternalAuthConfig
		secretEnvVars, err := r.getExternalAuthConfigSecretEnvVars(ctx, vmcp.Namespace, configName)
		if err != nil {
			ctxLogger.V(1).Info("Failed to get ExternalAuthConfig secret, skipping",
				"externalAuthConfig", configName,
				"error", err)
			continue
		}
		envVars = append(envVars, secretEnvVars...)
	}

	// Sort by name for deterministic ordering. The Kubernetes informer cache returns
	// items in non-deterministic order (Go map iteration), so without sorting the env
	// vars appear in a different sequence on each reconcile. reflect.DeepEqual in
	// containerNeedsUpdate is order-sensitive, so non-deterministic ordering causes a
	// continuous deployment update loop with 4+ configs.
	sort.Slice(envVars, func(i, j int) bool {
		return envVars[i].Name < envVars[j].Name
	})

	return envVars
}

// discoverInlineExternalAuthConfigSecrets discovers ExternalAuthConfigs referenced in inline Backends
// and returns environment variables for their client secrets.
func (r *VirtualMCPServerReconciler) discoverInlineExternalAuthConfigSecrets(
	ctx context.Context,
	vmcp *mcpv1beta1.VirtualMCPServer,
) []corev1.EnvVar {
	var envVars []corev1.EnvVar
	seenConfigs := make(map[string]bool) // Track which ExternalAuthConfigs we've already processed

	// Process per-backend configs
	for _, backendAuth := range vmcp.Spec.OutgoingAuth.Backends {
		if backendAuth.ExternalAuthConfigRef == nil {
			continue
		}

		configName := backendAuth.ExternalAuthConfigRef.Name
		// Skip if we've already processed this ExternalAuthConfig
		if seenConfigs[configName] {
			continue
		}
		seenConfigs[configName] = true

		// Get the secret env var for this ExternalAuthConfig
		secretEnvVars, err := r.getExternalAuthConfigSecretEnvVars(ctx, vmcp.Namespace, configName)
		if err != nil {
			ctxLogger := log.FromContext(ctx)
			ctxLogger.V(1).Info("Failed to get ExternalAuthConfig secret, skipping",
				"externalAuthConfig", configName,
				"error", err)
			continue
		}
		envVars = append(envVars, secretEnvVars...)
	}

	// Sort by name for the same reason as discoverExternalAuthConfigSecrets: Go map
	// iteration over Spec.OutgoingAuth.Backends is non-deterministic, which would
	// cause a continuous deployment update loop via reflect.DeepEqual in containerNeedsUpdate.
	sort.Slice(envVars, func(i, j int) bool {
		return envVars[i].Name < envVars[j].Name
	})

	return envVars
}

// getExternalAuthConfigSecretEnvVars returns the environment variables for
// secrets from an ExternalAuthConfig (token exchange client secrets, header
// injection values, or — for obo — whatever the registered OBO handler asks
// for). Generates unique env var names per ExternalAuthConfig to avoid conflicts
// when multiple configs of the same type reference different secrets.
//
// The obo arm forwards every env var the handler returns (matching MCPServer and
// MCPRemoteProxy) and propagates obo.ErrEnterpriseRequired so the wired-but-
// disabled state is not masked as a no-op (see #5328); callers log-and-continue
// on error.
func (r *VirtualMCPServerReconciler) getExternalAuthConfigSecretEnvVars(
	ctx context.Context,
	namespace string,
	externalAuthConfigName string,
) ([]corev1.EnvVar, error) {
	// Fetch the MCPExternalAuthConfig
	externalAuthConfig, err := ctrlutil.GetExternalAuthConfigByName(
		ctx, r.Client, namespace, externalAuthConfigName)
	if err != nil {
		return nil, fmt.Errorf("failed to get MCPExternalAuthConfig %s: %w", externalAuthConfigName, err)
	}

	var envVarName string
	var secretRef *mcpv1beta1.SecretKeyRef

	switch externalAuthConfig.Spec.Type {
	case mcpv1beta1.ExternalAuthTypeTokenExchange:
		if externalAuthConfig.Spec.TokenExchange == nil {
			return nil, nil
		}
		if externalAuthConfig.Spec.TokenExchange.ClientSecretRef == nil {
			return nil, nil // No secret to mount
		}
		envVarName = ctrlutil.GenerateUniqueTokenExchangeEnvVarName(externalAuthConfigName)
		secretRef = externalAuthConfig.Spec.TokenExchange.ClientSecretRef

	case mcpv1beta1.ExternalAuthTypeHeaderInjection:
		if externalAuthConfig.Spec.HeaderInjection == nil {
			return nil, nil
		}
		if externalAuthConfig.Spec.HeaderInjection.ValueSecretRef == nil {
			return nil, nil // No secret to mount
		}
		envVarName = ctrlutil.GenerateUniqueHeaderInjectionEnvVarName(externalAuthConfigName)
		secretRef = externalAuthConfig.Spec.HeaderInjection.ValueSecretRef

	case mcpv1beta1.ExternalAuthTypeBearerToken:
		// Bearer token secrets are handled differently (via RemoteAuthConfig in RunConfig)
		// No environment variable mounting needed for bearer tokens
		return nil, nil

	case mcpv1beta1.ExternalAuthTypeUnauthenticated:
		// No secrets to mount for unauthenticated
		return nil, nil

	case mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer:
		// Embedded auth server secrets are handled separately (via volume mounts, not env vars)
		// Controller integration will be in a future task
		return nil, nil

	case mcpv1beta1.ExternalAuthTypeAWSSts:
		// AWS STS authentication doesn't require secret mounting via env vars
		// It uses the incoming OIDC token for AssumeRoleWithWebIdentity
		return nil, nil

	case mcpv1beta1.ExternalAuthTypeUpstreamInject:
		// Upstream inject uses the embedded auth server's upstream tokens at runtime
		// No secrets to mount via env vars
		return nil, nil

	case mcpv1beta1.ExternalAuthTypeOBO:
		// Dispatch through the registered OBO handler, forwarding every env var
		// it returns. OBOSecretEnvVars propagates obo.ErrEnterpriseRequired in
		// upstream-only builds (unlike ctrlutil.AddOBOSecretEnvVars, which
		// swallows it for MCPServer/MCPRemoteProxy builder/drift symmetry); vMCP
		// must propagate it per #5328 so the wired-but-disabled state is not
		// masked as a no-op.
		return ctrlutil.OBOSecretEnvVars(externalAuthConfig)

	case mcpv1beta1.ExternalAuthTypeXAA:
		return xaaSecretEnvVars(externalAuthConfig, externalAuthConfigName), nil

	default:
		return nil, nil // Not applicable
	}

	return []corev1.EnvVar{{
		Name: envVarName,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: secretRef.Name,
				},
				Key: secretRef.Key,
			},
		},
	}}, nil
}

// xaaSecretEnvVars returns the env vars needed to mount XAA client secrets into
// the vMCP pod. Returns nil when the XAA spec is absent or has no secret refs.
func xaaSecretEnvVars(externalAuthConfig *mcpv1beta1.MCPExternalAuthConfig, configName string) []corev1.EnvVar {
	if externalAuthConfig.Spec.XAA == nil {
		return nil
	}
	var envVars []corev1.EnvVar
	if ref := externalAuthConfig.Spec.XAA.IDPClientSecretRef; ref != nil {
		envVars = append(envVars, corev1.EnvVar{
			Name: ctrlutil.GenerateUniqueXAAIDPSecretEnvVarName(configName),
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: ref.Name},
					Key:                  ref.Key,
				},
			},
		})
	}
	if ref := externalAuthConfig.Spec.XAA.TargetClientSecretRef; ref != nil {
		envVars = append(envVars, corev1.EnvVar{
			Name: ctrlutil.GenerateUniqueXAATargetSecretEnvVarName(configName),
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: ref.Name},
					Key:                  ref.Key,
				},
			},
		})
	}
	return envVars
}

// buildDeploymentMetadataForVmcp builds deployment-level labels and annotations
func (r *VirtualMCPServerReconciler) buildDeploymentMetadataForVmcp(
	baseLabels map[string]string,
	vmcp *mcpv1beta1.VirtualMCPServer,
) (map[string]string, map[string]string) {
	deploymentLabels := baseLabels
	deploymentAnnotations := make(map[string]string)

	// Store hash of user-provided PodTemplateSpec to detect changes without
	// comparing full rendered templates (which include K8s-defaulted fields).
	// Uses HashRawJSON to ensure deterministic hashing regardless of JSON field ordering.
	if vmcp.Spec.PodTemplateSpec != nil && len(vmcp.Spec.PodTemplateSpec.Raw) > 0 {
		hash, err := checksum.HashRawJSON(vmcp.Spec.PodTemplateSpec.Raw)
		if err == nil {
			deploymentAnnotations[podTemplateSpecHashAnnotation] = hash
		}
	}

	// Store hash of the desired imagePullSecrets list — chart-level defaults
	// merged with vmcp.Spec.ImagePullSecrets — so deploymentNeedsUpdate can
	// detect drift on this field. Without this annotation, edits to either
	// the chart default or spec.imagePullSecrets on an existing CR would not
	// propagate to the running Deployment because the drift checks compare
	// individual fields and never look at PodSpec.ImagePullSecrets directly
	// (the live value is the strategic-merge union with PodTemplateSpec).
	if hash, err := imagePullSecretsHash(r.imagePullSecretsForVMCP(vmcp)); err == nil && hash != "" {
		deploymentAnnotations[imagePullRefsHashAnnotation] = hash
	}

	// TODO: Add support for ResourceOverrides if needed in the future

	return deploymentLabels, deploymentAnnotations
}

// imagePullSecretsHash returns a deterministic SHA256 hash of the given LocalObjectReference list.
// The list is normalized by sorting on Name before hashing so that semantically equal slices
// (same set of secret names, possibly in different order) produce the same hash. Returns an
// empty string with no error when the list is empty so callers can skip writing the annotation.
func imagePullSecretsHash(secrets []corev1.LocalObjectReference) (string, error) {
	if len(secrets) == 0 {
		return "", nil
	}
	normalized := make([]corev1.LocalObjectReference, len(secrets))
	copy(normalized, secrets)
	sort.Slice(normalized, func(i, j int) bool {
		return normalized[i].Name < normalized[j].Name
	})
	canonical, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("failed to marshal imagePullSecrets for hashing: %w", err)
	}
	h := sha256.Sum256(canonical)
	return hex.EncodeToString(h[:]), nil
}

// buildPodTemplateMetadata builds pod template labels and annotations for vmcp
func (*VirtualMCPServerReconciler) buildPodTemplateMetadata(
	baseLabels map[string]string,
	_ *mcpv1beta1.VirtualMCPServer,
	vmcpConfigChecksum string,
) (map[string]string, map[string]string) {
	templateLabels := baseLabels

	// Add vmcp Config checksum annotation to trigger pod rollout when config changes
	// Use the standard checksum package helper for consistency
	templateAnnotations := checksum.AddRunConfigChecksumToPodTemplate(nil, vmcpConfigChecksum)

	return templateLabels, templateAnnotations
}

// buildSecurityContextsForVmcp builds pod and container security contexts
func (r *VirtualMCPServerReconciler) buildSecurityContextsForVmcp(
	ctx context.Context,
	vmcp *mcpv1beta1.VirtualMCPServer,
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
	_ *mcpv1beta1.VirtualMCPServer,
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
	vmcp *mcpv1beta1.VirtualMCPServer,
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

	sessionAffinity := func() corev1.ServiceAffinity {
		if vmcp.Spec.SessionAffinity != "" {
			return corev1.ServiceAffinity(vmcp.Spec.SessionAffinity)
		}
		return corev1.ServiceAffinityClientIP
	}()

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        svcName,
			Namespace:   vmcp.Namespace,
			Labels:      serviceLabels,
			Annotations: serviceAnnotations,
		},
		Spec: corev1.ServiceSpec{
			Type:            serviceType,
			Selector:        ls,
			SessionAffinity: sessionAffinity,
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
	_ *mcpv1beta1.VirtualMCPServer,
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
// - OIDC client secrets (via MCPOIDCConfig inline ClientSecretRef)
// - Service account credentials (OutgoingAuth.*.ServiceAccount.CredentialsRef)
//
// This follows the pattern from ctrlutil.GenerateOIDCClientSecretEnvVar() which validates secrets
// exist before pod creation.
//
//nolint:gocyclo // Secret validation requires checking multiple optional config paths
func (r *VirtualMCPServerReconciler) validateSecretReferences(
	ctx context.Context,
	vmcp *mcpv1beta1.VirtualMCPServer,
) error {
	// Validate MCPOIDCConfig inline client secret if configured
	if vmcp.Spec.IncomingAuth != nil && vmcp.Spec.IncomingAuth.OIDCConfigRef != nil {
		oidcCfg, err := ctrlutil.GetOIDCConfigForServer(
			ctx, r.Client, vmcp.Namespace, vmcp.Spec.IncomingAuth.OIDCConfigRef)
		if err != nil {
			return fmt.Errorf("failed to get MCPOIDCConfig %s for secret validation: %w",
				vmcp.Spec.IncomingAuth.OIDCConfigRef.Name, err)
		}
		if oidcCfg != nil &&
			oidcCfg.Spec.Type == mcpv1beta1.MCPOIDCConfigTypeInline &&
			oidcCfg.Spec.Inline != nil &&
			oidcCfg.Spec.Inline.ClientSecretRef != nil {
			if err := r.validateSecretKeyRef(ctx, vmcp.Namespace,
				oidcCfg.Spec.Inline.ClientSecretRef,
				"MCPOIDCConfig OIDC client secret"); err != nil {
				return err
			}
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

	return nil
}

// validateBackendAuthSecrets validates secrets referenced in backend authentication configuration
func (*VirtualMCPServerReconciler) validateBackendAuthSecrets(
	_ context.Context,
	_ string,
	_ *mcpv1beta1.BackendAuthConfig,
	_ string,
) error {
	// No backend auth types currently require secret validation
	return nil
}

// validateAuthzConfigMapRef pre-validates the referenced authz ConfigMap when
// spec.incomingAuth.authzConfig.type is "configMap". It uses the same shared loader
// as the converter so the diagnostic surfaces the same parse/validation errors the
// converter would later produce, but earlier in the reconcile and as a status condition
// rather than as a generic conversion failure. Inline and absent authzConfig are no-ops.
//
// Also runs ExtractCedarAuthzOptions on the loaded payload so a configMap that
// parses as a valid authz.Config but isn't Cedar-flavoured (wrong "type" field,
// or a future HTTP authorizer) is rejected here with AuthzConfigMapInvalid
// rather than passing pre-validation and then failing opaquely at convert time.
//
// Mirrors the pattern in mcpremoteproxy_controller.go's validateK8sRefs.
func (r *VirtualMCPServerReconciler) validateAuthzConfigMapRef(
	ctx context.Context,
	vmcp *mcpv1beta1.VirtualMCPServer,
) error {
	if vmcp.Spec.IncomingAuth == nil ||
		vmcp.Spec.IncomingAuth.AuthzConfig == nil ||
		vmcp.Spec.IncomingAuth.AuthzConfig.Type != mcpv1beta1.AuthzConfigTypeConfigMap {
		return nil
	}
	cfg, err := ctrlutil.LoadAuthzConfigFromConfigMap(
		ctx, r.Client, vmcp.Namespace, vmcp.Spec.IncomingAuth.AuthzConfig,
	)
	if err != nil {
		return err
	}
	if _, err := ctrlutil.ExtractCedarAuthzOptions(cfg); err != nil {
		return fmt.Errorf("authz ConfigMap %s/%s is not a Cedar config: %w",
			vmcp.Namespace, vmcp.Spec.IncomingAuth.AuthzConfig.ConfigMap.Name, err)
	}
	return nil
}

// validateSecretKeyRef validates that a secret reference exists and contains the required key.
// This implements the validation pattern from ctrlutil.GenerateOIDCClientSecretEnvVar().
func (r *VirtualMCPServerReconciler) validateSecretKeyRef(
	ctx context.Context,
	namespace string,
	secretRef *mcpv1beta1.SecretKeyRef,
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

// applyPodTemplateSpecToDeployment applies user-provided PodTemplateSpec customizations to the deployment
// using strategic merge patch. This allows users to customize pod-level settings like node selectors,
// tolerations, affinity rules, security contexts, and additional containers.
//
// The merge strategy:
// - User-provided fields override controller-generated defaults
// - Arrays are merged based on strategic merge patch rules (e.g., containers merged by name)
// - The "vmcp" container is preserved from the controller-generated spec
//
// Hard-fail policy: any patch failure (marshal, patch apply, unmarshal) is returned as
// an error that blocks Deployment creation. This is the opposite of the EmbeddingServer
// caller's soft-fail choice. ApplyPodTemplateSpecPatch is policy-neutral; the choice is
// at this call site by design.
func (*VirtualMCPServerReconciler) applyPodTemplateSpecToDeployment(
	ctx context.Context,
	vmcp *mcpv1beta1.VirtualMCPServer,
	deployment *appsv1.Deployment,
) error {
	ctxLogger := log.FromContext(ctx)

	// Early return if no PodTemplateSpec provided
	if vmcp.Spec.PodTemplateSpec == nil || len(vmcp.Spec.PodTemplateSpec.Raw) == 0 {
		return nil
	}

	// Validate the user-provided PodTemplateSpec is well-formed.
	// We don't check builder.Build() == nil for "empty" customizations: that helper
	// only enumerates a subset of PodSpec fields and would skip the patch for
	// fields like runtimeClassName or topologySpreadConstraints. Strategic merge
	// patch is a no-op for `{}` anyway, so always running it is safe.
	if _, err := ctrlutil.NewPodTemplateSpecBuilder(vmcp.Spec.PodTemplateSpec, "vmcp"); err != nil {
		return fmt.Errorf("failed to build PodTemplateSpec: %w", err)
	}

	merged, err := ctrlutil.ApplyPodTemplateSpecPatch(deployment.Spec.Template, vmcp.Spec.PodTemplateSpec.Raw)
	if err != nil {
		return err
	}

	deployment.Spec.Template = merged

	ctxLogger.V(1).Info("Applied PodTemplateSpec customizations to deployment",
		"virtualmcpserver", vmcp.Name,
		"namespace", vmcp.Namespace)

	return nil
}

const (
	// caBundleBasePath is the base path where CA bundle ConfigMaps are mounted in the vMCP pod.
	caBundleBasePath = "/etc/toolhive/ca-bundles"
)

// caBundleMountPath returns the mount path for a CA bundle ConfigMap for a given entry name.
// The key defaults to "ca.crt" if not specified in the CABundleSource.
func caBundleMountPath(entryName string, caBundleRef *mcpv1beta1.CABundleSource) string {
	if caBundleRef == nil {
		return path.Join(caBundleBasePath, entryName, "ca.crt")
	}
	key := "ca.crt"
	if caBundleRef.ConfigMapRef != nil && caBundleRef.ConfigMapRef.Key != "" {
		key = caBundleRef.ConfigMapRef.Key
	}
	return path.Join(caBundleBasePath, entryName, key)
}

// caBundleVolumeName returns a deterministic volume name for a CA bundle.
// Kubernetes volume names are limited to 63 characters and must be valid DNS labels.
// For short names, the format is "ca-bundle-<entryName>".
// For long names that would exceed 63 chars, a hash suffix is appended to the
// truncated name to avoid collisions: "ca-bundle-<truncated>-<sha256[:8]>".
// Trailing hyphens are trimmed to maintain DNS label validity.
func caBundleVolumeName(entryName string) string {
	name := fmt.Sprintf("ca-bundle-%s", entryName)
	if len(name) <= 63 {
		return name
	}

	// Use a hash suffix to avoid collisions between long names sharing a prefix
	hash := sha256.Sum256([]byte(entryName))
	suffix := hex.EncodeToString(hash[:4]) // 8 hex chars
	// "ca-bundle-" (10) + truncated + "-" (1) + hash (8) = 19 overhead, leaving 44 for entry name
	maxNameLen := 63 - 10 - 1 - 8 // 44
	truncated := entryName
	if len(truncated) > maxNameLen {
		truncated = truncated[:maxNameLen]
	}
	truncated = strings.TrimRight(truncated, "-")
	return fmt.Sprintf("ca-bundle-%s-%s", truncated, suffix)
}

// buildCABundleVolumesForEntries builds volumes and volume mounts for MCPServerEntry CA bundles.
func (r *VirtualMCPServerReconciler) buildCABundleVolumesForEntries(
	ctx context.Context,
	namespace string,
	typedWorkloads []workloads.TypedWorkload,
) ([]corev1.Volume, []corev1.VolumeMount, error) {
	var volumes []corev1.Volume
	var mounts []corev1.VolumeMount

	// Early return if no MCPServerEntry workloads to avoid unnecessary API calls
	hasEntries := false
	for _, workload := range typedWorkloads {
		if workload.Type == workloads.WorkloadTypeMCPServerEntry {
			hasEntries = true
			break
		}
	}
	if !hasEntries {
		return volumes, mounts, nil
	}

	mcpServerEntryMap, err := r.listMCPServerEntriesAsMap(ctx, namespace)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list MCPServerEntries: %w", err)
	}

	for _, workload := range typedWorkloads {
		if workload.Type != workloads.WorkloadTypeMCPServerEntry {
			continue
		}
		entry, found := mcpServerEntryMap[workload.Name]
		if !found || entry.Spec.CABundleRef == nil || entry.Spec.CABundleRef.ConfigMapRef == nil {
			continue
		}

		volName := caBundleVolumeName(workload.Name)
		mountPath := path.Join(caBundleBasePath, workload.Name)

		key := "ca.crt"
		if entry.Spec.CABundleRef.ConfigMapRef.Key != "" {
			key = entry.Spec.CABundleRef.ConfigMapRef.Key
		}

		volumes = append(volumes, corev1.Volume{
			Name: volName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: entry.Spec.CABundleRef.ConfigMapRef.Name,
					},
					Items: []corev1.KeyToPath{
						{
							Key:  key,
							Path: key,
						},
					},
				},
			},
		})

		mounts = append(mounts, corev1.VolumeMount{
			Name:      volName,
			MountPath: mountPath,
			ReadOnly:  true,
		})
	}

	return volumes, mounts, nil
}

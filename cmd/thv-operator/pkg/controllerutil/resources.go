// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/secrets"
)

// Default proxy-runner resource values used when a CR omits resource requests/limits.
// These keep remote proxy containers bounded while remaining overridable per CR.
const (
	defaultProxyRunnerCPURequest    = "50m"
	defaultProxyRunnerCPULimit      = "200m"
	defaultProxyRunnerMemoryRequest = "64Mi"
	defaultProxyRunnerMemoryLimit   = "256Mi"
)

// BuildResourceRequirements builds Kubernetes resource requirements from CRD spec
// Shared between MCPServer and MCPRemoteProxy
func BuildResourceRequirements(resourceSpec mcpv1beta1.ResourceRequirements) corev1.ResourceRequirements {
	resources := corev1.ResourceRequirements{}

	if resourceSpec.Limits.CPU != "" || resourceSpec.Limits.Memory != "" {
		resources.Limits = corev1.ResourceList{}
		if resourceSpec.Limits.CPU != "" {
			resources.Limits[corev1.ResourceCPU] = resource.MustParse(resourceSpec.Limits.CPU)
		}
		if resourceSpec.Limits.Memory != "" {
			resources.Limits[corev1.ResourceMemory] = resource.MustParse(resourceSpec.Limits.Memory)
		}
	}

	if resourceSpec.Requests.CPU != "" || resourceSpec.Requests.Memory != "" {
		resources.Requests = corev1.ResourceList{}
		if resourceSpec.Requests.CPU != "" {
			resources.Requests[corev1.ResourceCPU] = resource.MustParse(resourceSpec.Requests.CPU)
		}
		if resourceSpec.Requests.Memory != "" {
			resources.Requests[corev1.ResourceMemory] = resource.MustParse(resourceSpec.Requests.Memory)
		}
	}

	return resources
}

// BuildDefaultProxyRunnerResourceRequirements returns the default resource
// requirements for proxy-runner containers (MCPRemoteProxy).
func BuildDefaultProxyRunnerResourceRequirements() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(defaultProxyRunnerCPURequest),
			corev1.ResourceMemory: resource.MustParse(defaultProxyRunnerMemoryRequest),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(defaultProxyRunnerCPULimit),
			corev1.ResourceMemory: resource.MustParse(defaultProxyRunnerMemoryLimit),
		},
	}
}

// MergeResourceRequirements merges user-provided resource requirements on top of
// defaults. User values take precedence for any field that is explicitly set.
//
// A defaulted value never contradicts a user value: when the user sets only one
// side of a request/limit pair, the defaulted side is clamped so the request does
// not exceed the limit, which the API server would otherwise reject. A user who
// sets both sides inconsistently gets their values unchanged.
func MergeResourceRequirements(defaults, user corev1.ResourceRequirements) corev1.ResourceRequirements {
	merged := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{},
		Limits:   corev1.ResourceList{},
	}

	for resourceName, quantity := range defaults.Requests {
		merged.Requests[resourceName] = quantity.DeepCopy()
	}
	for resourceName, quantity := range defaults.Limits {
		merged.Limits[resourceName] = quantity.DeepCopy()
	}
	for resourceName, quantity := range user.Requests {
		merged.Requests[resourceName] = quantity.DeepCopy()
	}
	for resourceName, quantity := range user.Limits {
		merged.Limits[resourceName] = quantity.DeepCopy()
	}

	for resourceName, request := range merged.Requests {
		limit, hasLimit := merged.Limits[resourceName]
		if !hasLimit || request.Cmp(limit) <= 0 {
			continue
		}
		_, userSetRequest := user.Requests[resourceName]
		_, userSetLimit := user.Limits[resourceName]
		switch {
		case userSetRequest && !userSetLimit:
			merged.Limits[resourceName] = request.DeepCopy()
		case userSetLimit && !userSetRequest:
			merged.Requests[resourceName] = limit.DeepCopy()
		}
	}

	if len(merged.Requests) == 0 {
		merged.Requests = nil
	}
	if len(merged.Limits) == 0 {
		merged.Limits = nil
	}

	return merged
}

// BuildContainerResourceRequirements returns the resources to set on a
// controller-generated container before the user's podTemplateSpec is applied.
//
// Values are layered in increasing precedence: defaults, then the CRD's
// resources field, then any resources the podTemplateSpec sets on the container
// named containerName. The result goes through MergeResourceRequirements, so a
// defaulted value never pushes a request above a limit the user chose, or a
// limit below a request the user chose, whichever field the user set it in.
//
// The podTemplateSpec is still applied afterwards as a strategic merge patch.
// That re-sets the same values, except that a null entry deletes a key. A key
// the podTemplateSpec deletes is dropped here too, so no default stands in for
// it and it takes no part in clamping.
//
// It returns an error if podTemplateSpec is not valid JSON.
func BuildContainerResourceRequirements(
	defaults corev1.ResourceRequirements,
	specResources mcpv1beta1.ResourceRequirements,
	podTemplateSpec *runtime.RawExtension,
	containerName string,
) (corev1.ResourceRequirements, error) {
	user := BuildResourceRequirements(specResources)

	templateResources, err := podTemplateContainerResources(podTemplateSpec, containerName)
	if err != nil {
		return corev1.ResourceRequirements{}, err
	}

	defaults = *defaults.DeepCopy()
	user.Requests = overlayResourceList(user.Requests, defaults.Requests, templateResources.Requests)
	user.Limits = overlayResourceList(user.Limits, defaults.Limits, templateResources.Limits)

	return MergeResourceRequirements(defaults, user), nil
}

// podTemplateResourceLists holds a container's resources as written in a
// podTemplateSpec patch. Pointer values keep a null entry (a strategic merge
// patch deletion) distinct from an explicit quantity.
type podTemplateResourceLists struct {
	Requests map[corev1.ResourceName]*resource.Quantity `json:"requests,omitempty"`
	Limits   map[corev1.ResourceName]*resource.Quantity `json:"limits,omitempty"`
}

// podTemplateContainerResources extracts the resources the podTemplateSpec
// patch sets on the named container. It returns empty lists if there is no
// patch or the patch does not mention that container.
func podTemplateContainerResources(
	podTemplateSpec *runtime.RawExtension,
	containerName string,
) (podTemplateResourceLists, error) {
	if podTemplateSpec == nil || len(podTemplateSpec.Raw) == 0 {
		return podTemplateResourceLists{}, nil
	}

	var patch struct {
		Spec struct {
			Containers []struct {
				Name      string                   `json:"name"`
				Resources podTemplateResourceLists `json:"resources"`
			} `json:"containers"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(podTemplateSpec.Raw, &patch); err != nil {
		return podTemplateResourceLists{}, fmt.Errorf("failed to read container resources from PodTemplateSpec: %w", err)
	}

	for _, container := range patch.Spec.Containers {
		if container.Name == containerName {
			return container.Resources, nil
		}
	}
	return podTemplateResourceLists{}, nil
}

// overlayResourceList applies podTemplateSpec entries on top of base. A
// quantity overrides the base value; a null entry removes the key from both
// base and defaults, matching the strategic merge patch that runs later.
func overlayResourceList(
	base, defaults corev1.ResourceList,
	overlay map[corev1.ResourceName]*resource.Quantity,
) corev1.ResourceList {
	if len(overlay) == 0 {
		return base
	}

	result := corev1.ResourceList{}
	for resourceName, quantity := range base {
		result[resourceName] = quantity.DeepCopy()
	}
	for resourceName, quantity := range overlay {
		if quantity == nil {
			delete(result, resourceName)
			delete(defaults, resourceName)
			continue
		}
		result[resourceName] = quantity.DeepCopy()
	}
	return result
}

// BuildHealthProbe builds a Kubernetes health probe configuration
// Shared between MCPServer and MCPRemoteProxy
func BuildHealthProbe(
	path, port string, initialDelay, period, timeout, failureThreshold int32,
) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: path,
				Port: intstr.FromString(port),
			},
		},
		InitialDelaySeconds: initialDelay,
		PeriodSeconds:       period,
		TimeoutSeconds:      timeout,
		FailureThreshold:    failureThreshold,
	}
}

// EnsureRequiredEnvVars ensures required environment variables are set with defaults
// Shared between MCPServer and MCPRemoteProxy
func EnsureRequiredEnvVars(ctx context.Context, env []corev1.EnvVar) []corev1.EnvVar {
	ctxLogger := log.FromContext(ctx)
	xdgConfigHomeFound := false
	homeFound := false
	toolhiveRuntimeFound := false
	unstructuredLogsFound := false
	hasSecrets := false

	for _, envVar := range env {
		switch envVar.Name {
		case "XDG_CONFIG_HOME":
			xdgConfigHomeFound = true
		case "HOME":
			homeFound = true
		case "TOOLHIVE_RUNTIME":
			toolhiveRuntimeFound = true
		case "UNSTRUCTURED_LOGS":
			unstructuredLogsFound = true
		}
		// Check if this is a TOOLHIVE_SECRET_* env var (but not TOOLHIVE_SECRETS_PROVIDER itself)
		if strings.HasPrefix(envVar.Name, secrets.EnvVarPrefix) && envVar.Name != secrets.ProviderEnvVar {
			hasSecrets = true
		}
	}

	if !xdgConfigHomeFound {
		ctxLogger.V(1).Info("XDG_CONFIG_HOME not found, setting to /tmp")
		env = append(env, corev1.EnvVar{
			Name:  "XDG_CONFIG_HOME",
			Value: "/tmp",
		})
	}

	if !homeFound {
		ctxLogger.V(1).Info("HOME not found, setting to /tmp")
		env = append(env, corev1.EnvVar{
			Name:  "HOME",
			Value: "/tmp",
		})
	}

	if !toolhiveRuntimeFound {
		ctxLogger.V(1).Info("TOOLHIVE_RUNTIME not found, setting to kubernetes")
		env = append(env, corev1.EnvVar{
			Name:  "TOOLHIVE_RUNTIME",
			Value: "kubernetes",
		})
	}

	// Always use structured JSON logs in Kubernetes (not configurable)
	if !unstructuredLogsFound {
		ctxLogger.V(1).Info("UNSTRUCTURED_LOGS not found, setting to false for structured JSON logging")
		env = append(env, corev1.EnvVar{
			Name:  "UNSTRUCTURED_LOGS",
			Value: "false",
		})
	}

	// Set secrets provider to environment if secrets are being used via TOOLHIVE_SECRET_* env vars
	// This is needed to resolve CLI format secrets (e.g., "secret-name,target=bearer_token")
	// The environment provider reads from TOOLHIVE_SECRET_* env vars to resolve CLI format secrets
	//
	// If TOOLHIVE_SECRETS_PROVIDER is already set to something other than "environment",
	// we override it because TOOLHIVE_SECRET_* env vars REQUIRE the environment provider.
	// Other providers (encrypted, 1password) cannot read from TOOLHIVE_SECRET_* env vars.
	if hasSecrets {
		ctxLogger.V(1).Info("TOOLHIVE_SECRET_* env vars found, setting TOOLHIVE_SECRETS_PROVIDER to environment")
		env = append(env, corev1.EnvVar{
			Name:  secrets.ProviderEnvVar,
			Value: string(secrets.EnvironmentType),
		})
	}

	return env
}

// MergeLabels merges override labels with default labels
// Default labels take precedence to ensure operator-required metadata is preserved
// Shared between MCPServer and MCPRemoteProxy
func MergeLabels(defaultLabels, overrideLabels map[string]string) map[string]string {
	return MergeStringMaps(defaultLabels, overrideLabels)
}

// MergeAnnotations merges override annotations with default annotations
// Default annotations take precedence to ensure operator-required metadata is preserved
// Shared between MCPServer and MCPRemoteProxy
func MergeAnnotations(defaultAnnotations, overrideAnnotations map[string]string) map[string]string {
	return MergeStringMaps(defaultAnnotations, overrideAnnotations)
}

// MergeStringMaps merges override map with default map, with default map taking precedence
func MergeStringMaps(defaultMap, overrideMap map[string]string) map[string]string {
	result := make(map[string]string)
	for k, v := range overrideMap {
		result[k] = v
	}
	for k, v := range defaultMap {
		result[k] = v // default takes precedence
	}
	return result
}

// CreateProxyServiceName generates the service name for a proxy (MCPServer or MCPRemoteProxy)
// Shared naming convention across both controllers
func CreateProxyServiceName(resourceName string) string {
	return fmt.Sprintf("mcp-%s-proxy", resourceName)
}

// ProxyRunnerServiceAccountName generates the service account name for the proxy runner
// Shared between MCPServer and MCPRemoteProxy
func ProxyRunnerServiceAccountName(resourceName string) string {
	return fmt.Sprintf("%s-proxy-runner", resourceName)
}

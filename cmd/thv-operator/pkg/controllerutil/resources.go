// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// BuildResourceRequirements builds Kubernetes resource requirements from CRD spec
// Shared between MCPServer and MCPRemoteProxy
func BuildResourceRequirements(resourceSpec mcpv1alpha1.ResourceRequirements) corev1.ResourceRequirements {
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

// CreateProxyServiceURL generates the full cluster-local service URL
// Shared between MCPServer and MCPRemoteProxy
func CreateProxyServiceURL(resourceName, namespace string, port int32) string {
	serviceName := CreateProxyServiceName(resourceName)
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", serviceName, namespace, port)
}

// ProxyRunnerServiceAccountName generates the service account name for the proxy runner
// Shared between MCPServer and MCPRemoteProxy
func ProxyRunnerServiceAccountName(resourceName string) string {
	return fmt.Sprintf("%s-proxy-runner", resourceName)
}

// BuildDefaultProxyRunnerResourceRequirements returns default resource requirements for proxy runners.
// Defaults: 50m/200m CPU (requests/limits), 64Mi/256Mi memory (requests/limits).
// These defaults provide reasonable resource limits to prevent containers from monopolizing cluster resources.
func BuildDefaultProxyRunnerResourceRequirements() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("50m"),
			corev1.ResourceMemory: resource.MustParse("64Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("200m"),
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		},
	}
}

// MergeResourceRequirements merges user-provided resource requirements with defaults.
// User-provided values take precedence over defaults.
// If a resource is specified in userResources, it will override the corresponding default.
func MergeResourceRequirements(defaultResources, userResources corev1.ResourceRequirements) corev1.ResourceRequirements {
	result := corev1.ResourceRequirements{}

	// Start with defaults
	if defaultResources.Requests != nil {
		result.Requests = make(corev1.ResourceList)
		for k, v := range defaultResources.Requests {
			result.Requests[k] = v
		}
	}
	if defaultResources.Limits != nil {
		result.Limits = make(corev1.ResourceList)
		for k, v := range defaultResources.Limits {
			result.Limits[k] = v
		}
	}

	// Override with user-provided values
	if userResources.Requests != nil {
		if result.Requests == nil {
			result.Requests = make(corev1.ResourceList)
		}
		for k, v := range userResources.Requests {
			result.Requests[k] = v
		}
	}
	if userResources.Limits != nil {
		if result.Limits == nil {
			result.Limits = make(corev1.ResourceList)
		}
		for k, v := range userResources.Limits {
			result.Limits[k] = v
		}
	}

	return result
}
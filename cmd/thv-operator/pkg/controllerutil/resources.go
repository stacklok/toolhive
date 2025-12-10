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

const (
	// DefaultCPURequest is the default CPU request for MCP server containers.
	// These values provide reasonable limits to prevent resource monopolization
	// while allowing sufficient resources for typical MCP server workloads.
	DefaultCPURequest = "100m"
	// DefaultCPULimit is the default CPU limit for MCP server containers.
	DefaultCPULimit = "500m"
	// DefaultMemoryRequest is the default memory request for MCP server containers.
	DefaultMemoryRequest = "128Mi"
	// DefaultMemoryLimit is the default memory limit for MCP server containers.
	DefaultMemoryLimit = "512Mi"

	// DefaultProxyRunnerCPURequest is the default CPU request for proxy runner containers.
	// The proxy runner is a lightweight Go process that manages the connection
	// to the MCP server container, so it needs fewer resources.
	DefaultProxyRunnerCPURequest = "50m"
	// DefaultProxyRunnerCPULimit is the default CPU limit for proxy runner containers.
	DefaultProxyRunnerCPULimit = "200m"
	// DefaultProxyRunnerMemoryRequest is the default memory request for proxy runner containers.
	DefaultProxyRunnerMemoryRequest = "64Mi"
	// DefaultProxyRunnerMemoryLimit is the default memory limit for proxy runner containers.
	DefaultProxyRunnerMemoryLimit = "256Mi"
)

// BuildDefaultResourceRequirements returns standard default resource requirements
// for MCP server containers (MCPServer, VirtualMCPServer, MCPRemoteProxy).
// These defaults prevent resource monopolization while allowing user customization.
func BuildDefaultResourceRequirements() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(DefaultCPULimit),
			corev1.ResourceMemory: resource.MustParse(DefaultMemoryLimit),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(DefaultCPURequest),
			corev1.ResourceMemory: resource.MustParse(DefaultMemoryRequest),
		},
	}
}

// BuildDefaultProxyRunnerResourceRequirements returns default resource requirements
// for the ToolHive proxy runner container (the container running `thv run`).
// The proxy runner is lighter weight than the MCP server container it manages.
func BuildDefaultProxyRunnerResourceRequirements() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(DefaultProxyRunnerCPULimit),
			corev1.ResourceMemory: resource.MustParse(DefaultProxyRunnerMemoryLimit),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(DefaultProxyRunnerCPURequest),
			corev1.ResourceMemory: resource.MustParse(DefaultProxyRunnerMemoryRequest),
		},
	}
}

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

// MergeResourceRequirements intelligently merges user-provided resources with default resources.
// For each resource type (CPU, Memory), handles request and limit independently:
// - If user provides both → use user values
// - If user provides limit only:
//   - If limit >= default request → use default request + user limit
//   - If limit < default request → use user limit for both
//
// - If user provides request only:
//   - If request >= default limit → use user request for both
//   - If request < default limit → use user request + default limit
//
// - If user provides neither → use defaults
func MergeResourceRequirements(defaults, user corev1.ResourceRequirements) corev1.ResourceRequirements {
	result := corev1.ResourceRequirements{
		Requests: defaults.Requests.DeepCopy(),
		Limits:   defaults.Limits.DeepCopy(),
	}

	mergeResourceType(corev1.ResourceCPU, &result, defaults, user)
	mergeResourceType(corev1.ResourceMemory, &result, defaults, user)

	return result
}

// mergeResourceType handles merging logic for a single resource type (CPU or Memory)
func mergeResourceType(
	resourceName corev1.ResourceName,
	result *corev1.ResourceRequirements,
	defaults, user corev1.ResourceRequirements,
) {
	userLimit, hasUserLimit := user.Limits[resourceName]
	userRequest, hasUserRequest := user.Requests[resourceName]
	defaultRequest := defaults.Requests[resourceName]
	defaultLimit := defaults.Limits[resourceName]

	// Process user-provided limit
	if hasUserLimit {
		result.Limits[resourceName] = userLimit.DeepCopy()
		// If no request provided, check against default request
		if !hasUserRequest {
			if userLimit.Cmp(defaultRequest) >= 0 {
				// User limit >= default request, use default request
				result.Requests[resourceName] = defaultRequest.DeepCopy()
			} else {
				// User limit < default request, use user limit for both
				result.Requests[resourceName] = userLimit.DeepCopy()
			}
		}
	}

	// Process user-provided request
	if hasUserRequest {
		result.Requests[resourceName] = userRequest.DeepCopy()
		// If no limit provided, compare request with default limit
		if !hasUserLimit {
			if userRequest.Cmp(defaultLimit) >= 0 {
				result.Limits[resourceName] = userRequest.DeepCopy()
			} else {
				result.Limits[resourceName] = defaultLimit.DeepCopy()
			}
		}
	}
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

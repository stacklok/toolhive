package controllerutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// mustParseQuantity is a test helper that panics if parsing fails
func mustParseQuantity(s string) resource.Quantity {
	q := resource.MustParse(s)
	return q
}

func TestBuildDefaultProxyRunnerResourceRequirements(t *testing.T) {
	t.Parallel()

	resources := BuildDefaultProxyRunnerResourceRequirements()

	// Verify limits
	assert.Equal(t, DefaultProxyRunnerCPULimit, resources.Limits.Cpu().String())
	assert.Equal(t, DefaultProxyRunnerMemoryLimit, resources.Limits.Memory().String())

	// Verify requests
	assert.Equal(t, DefaultProxyRunnerCPURequest, resources.Requests.Cpu().String())
	assert.Equal(t, DefaultProxyRunnerMemoryRequest, resources.Requests.Memory().String())
}

func TestBuildDefaultResourceRequirements(t *testing.T) {
	t.Parallel()

	resources := BuildDefaultResourceRequirements()

	// Verify limits
	assert.Equal(t, DefaultCPULimit, resources.Limits.Cpu().String())
	assert.Equal(t, DefaultMemoryLimit, resources.Limits.Memory().String())

	// Verify requests
	assert.Equal(t, DefaultCPURequest, resources.Requests.Cpu().String())
	assert.Equal(t, DefaultMemoryRequest, resources.Requests.Memory().String())
}

func TestProxyRunnerResourcesAreLighterThanMCPServerResources(t *testing.T) {
	t.Parallel()

	proxyResources := BuildDefaultProxyRunnerResourceRequirements()
	mcpResources := BuildDefaultResourceRequirements()

	// Proxy runner should use less CPU than MCP server
	assert.True(t, proxyResources.Limits.Cpu().Cmp(*mcpResources.Limits.Cpu()) < 0,
		"Proxy runner CPU limit should be less than MCP server CPU limit")
	assert.True(t, proxyResources.Requests.Cpu().Cmp(*mcpResources.Requests.Cpu()) < 0,
		"Proxy runner CPU request should be less than MCP server CPU request")

	// Proxy runner should use less memory than MCP server
	assert.True(t, proxyResources.Limits.Memory().Cmp(*mcpResources.Limits.Memory()) < 0,
		"Proxy runner memory limit should be less than MCP server memory limit")
	assert.True(t, proxyResources.Requests.Memory().Cmp(*mcpResources.Requests.Memory()) < 0,
		"Proxy runner memory request should be less than MCP server memory request")
}

func TestMergeResourceRequirements_ProxyRunner(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		defaults corev1.ResourceRequirements
		user     corev1.ResourceRequirements
		expected map[string]string // resource name -> expected value
	}{
		{
			name:     "empty user resources uses defaults",
			defaults: BuildDefaultProxyRunnerResourceRequirements(),
			user:     corev1.ResourceRequirements{},
			expected: map[string]string{
				"cpu_limit":      DefaultProxyRunnerCPULimit,
				"memory_limit":   DefaultProxyRunnerMemoryLimit,
				"cpu_request":    DefaultProxyRunnerCPURequest,
				"memory_request": DefaultProxyRunnerMemoryRequest,
			},
		},
		{
			name:     "user overrides only CPU limit",
			defaults: BuildDefaultProxyRunnerResourceRequirements(),
			user: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU: mustParseQuantity("300m"),
				},
			},
			expected: map[string]string{
				"cpu_limit":      "300m",
				"memory_limit":   DefaultProxyRunnerMemoryLimit,
				"cpu_request":    DefaultProxyRunnerCPURequest,
				"memory_request": DefaultProxyRunnerMemoryRequest,
			},
		},
		{
			name:     "user overrides all resources",
			defaults: BuildDefaultProxyRunnerResourceRequirements(),
			user: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    mustParseQuantity("500m"),
					corev1.ResourceMemory: mustParseQuantity("512Mi"),
				},
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    mustParseQuantity("100m"),
					corev1.ResourceMemory: mustParseQuantity("128Mi"),
				},
			},
			expected: map[string]string{
				"cpu_limit":      "500m",
				"memory_limit":   "512Mi",
				"cpu_request":    "100m",
				"memory_request": "128Mi",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := MergeResourceRequirements(tt.defaults, tt.user)

			assert.Equal(t, tt.expected["cpu_limit"], result.Limits.Cpu().String())
			assert.Equal(t, tt.expected["memory_limit"], result.Limits.Memory().String())
			assert.Equal(t, tt.expected["cpu_request"], result.Requests.Cpu().String())
			assert.Equal(t, tt.expected["memory_request"], result.Requests.Memory().String())
		})
	}
}

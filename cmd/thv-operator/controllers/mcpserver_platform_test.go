package controllers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

// mockPlatformDetector is a mock implementation of PlatformDetector for testing
type mockPlatformDetector struct {
	platform kubernetes.Platform
	err      error
}

func (m *mockPlatformDetector) DetectPlatform(_ *rest.Config) (kubernetes.Platform, error) {
	return m.platform, m.err
}

func TestMCPServerReconciler_DetectPlatform_Success(t *testing.T) {
	t.Skip("Platform detection requires in-cluster Kubernetes configuration - skipping for unit tests")

	t.Parallel()

	tests := []struct {
		name             string
		platform         kubernetes.Platform
		expectedPlatform kubernetes.Platform
	}{
		{
			name:             "Kubernetes platform",
			platform:         kubernetes.PlatformKubernetes,
			expectedPlatform: kubernetes.PlatformKubernetes,
		},
		{
			name:             "OpenShift platform",
			platform:         kubernetes.PlatformOpenShift,
			expectedPlatform: kubernetes.PlatformOpenShift,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reconciler := &MCPServerReconciler{
				platformDetector: &mockPlatformDetector{
					platform: tt.platform,
					err:      nil,
				},
			}

			ctx := context.Background()
			detectedPlatform, err := reconciler.detectPlatform(ctx)

			require.NoError(t, err)
			assert.Equal(t, tt.expectedPlatform, detectedPlatform)

			// Test that subsequent calls return cached result
			detectedPlatform2, err2 := reconciler.detectPlatform(ctx)
			require.NoError(t, err2)
			assert.Equal(t, tt.expectedPlatform, detectedPlatform2)
		})
	}
}

func TestMCPServerReconciler_DetectPlatform_Error(t *testing.T) {
	t.Skip("Platform detection requires in-cluster Kubernetes configuration - skipping for unit tests")

	t.Parallel()

	reconciler := &MCPServerReconciler{
		platformDetector: &mockPlatformDetector{
			platform: kubernetes.PlatformKubernetes,
			err:      assert.AnError,
		},
	}

	ctx := context.Background()
	detectedPlatform, err := reconciler.detectPlatform(ctx)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get in-cluster config")
	// Should return zero value when error occurs
	assert.Equal(t, kubernetes.Platform(0), detectedPlatform)
}

func TestMCPServerReconciler_DeploymentForMCPServer_Kubernetes(t *testing.T) {
	t.Parallel()

	// Create a test MCPServer
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mcp-server",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "stdio",
			Port:      8080,
		},
	}

	// Create reconciler with mock platform detector for Kubernetes
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	reconciler := &MCPServerReconciler{
		Scheme: scheme,
		platformDetector: &mockPlatformDetector{
			platform: kubernetes.PlatformKubernetes,
			err:      nil,
		},
		// Pre-set the detected platform to avoid calling detectPlatform which requires in-cluster config
		detectedPlatform: kubernetes.PlatformKubernetes,
	}
	// Simulate that platform detection has already been called
	reconciler.platformOnce.Do(func() {})

	ctx := context.Background()
	deployment := reconciler.deploymentForMCPServer(ctx, mcpServer)

	require.NotNil(t, deployment, "Deployment should not be nil")

	// Check pod security context for Kubernetes
	podSecurityContext := deployment.Spec.Template.Spec.SecurityContext
	require.NotNil(t, podSecurityContext, "Pod security context should not be nil")

	assert.NotNil(t, podSecurityContext.RunAsNonRoot)
	assert.True(t, *podSecurityContext.RunAsNonRoot)

	assert.NotNil(t, podSecurityContext.RunAsUser)
	assert.Equal(t, int64(1000), *podSecurityContext.RunAsUser)

	assert.NotNil(t, podSecurityContext.RunAsGroup)
	assert.Equal(t, int64(1000), *podSecurityContext.RunAsGroup)

	assert.NotNil(t, podSecurityContext.FSGroup)
	assert.Equal(t, int64(1000), *podSecurityContext.FSGroup)

	// Check container security context for Kubernetes
	containerSecurityContext := deployment.Spec.Template.Spec.Containers[0].SecurityContext
	require.NotNil(t, containerSecurityContext, "Container security context should not be nil")

	assert.NotNil(t, containerSecurityContext.Privileged)
	assert.False(t, *containerSecurityContext.Privileged)

	assert.NotNil(t, containerSecurityContext.RunAsNonRoot)
	assert.True(t, *containerSecurityContext.RunAsNonRoot)

	assert.NotNil(t, containerSecurityContext.RunAsUser)
	assert.Equal(t, int64(1000), *containerSecurityContext.RunAsUser)

	assert.NotNil(t, containerSecurityContext.RunAsGroup)
	assert.Equal(t, int64(1000), *containerSecurityContext.RunAsGroup)

	assert.NotNil(t, containerSecurityContext.AllowPrivilegeEscalation)
	assert.False(t, *containerSecurityContext.AllowPrivilegeEscalation)

	assert.NotNil(t, containerSecurityContext.ReadOnlyRootFilesystem)
	assert.True(t, *containerSecurityContext.ReadOnlyRootFilesystem)
}

func TestMCPServerReconciler_DeploymentForMCPServer_OpenShift(t *testing.T) {
	t.Parallel()

	// Create a test MCPServer
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mcp-server",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "stdio",
			Port:      8080,
		},
	}

	// Create reconciler with mock platform detector for OpenShift
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	reconciler := &MCPServerReconciler{
		Scheme: scheme,
		platformDetector: &mockPlatformDetector{
			platform: kubernetes.PlatformOpenShift,
			err:      nil,
		},
		// Pre-set the detected platform to avoid calling detectPlatform which requires in-cluster config
		detectedPlatform: kubernetes.PlatformOpenShift,
	}
	// Simulate that platform detection has already been called
	reconciler.platformOnce.Do(func() {})

	ctx := context.Background()
	deployment := reconciler.deploymentForMCPServer(ctx, mcpServer)

	require.NotNil(t, deployment, "Deployment should not be nil")

	// Check pod security context for OpenShift
	podSecurityContext := deployment.Spec.Template.Spec.SecurityContext
	require.NotNil(t, podSecurityContext, "Pod security context should not be nil")

	assert.NotNil(t, podSecurityContext.RunAsNonRoot)
	assert.True(t, *podSecurityContext.RunAsNonRoot)

	// These should be nil for OpenShift to allow SCCs to assign them
	assert.Nil(t, podSecurityContext.RunAsUser)
	assert.Nil(t, podSecurityContext.RunAsGroup)
	assert.Nil(t, podSecurityContext.FSGroup)

	// SeccompProfile should be set for OpenShift
	require.NotNil(t, podSecurityContext.SeccompProfile)
	assert.Equal(t, corev1.SeccompProfileTypeRuntimeDefault, podSecurityContext.SeccompProfile.Type)

	// Check container security context for OpenShift
	containerSecurityContext := deployment.Spec.Template.Spec.Containers[0].SecurityContext
	require.NotNil(t, containerSecurityContext, "Container security context should not be nil")

	assert.NotNil(t, containerSecurityContext.Privileged)
	assert.False(t, *containerSecurityContext.Privileged)

	assert.NotNil(t, containerSecurityContext.RunAsNonRoot)
	assert.True(t, *containerSecurityContext.RunAsNonRoot)

	// These should be nil for OpenShift to allow SCCs to assign them
	assert.Nil(t, containerSecurityContext.RunAsUser)
	assert.Nil(t, containerSecurityContext.RunAsGroup)

	assert.NotNil(t, containerSecurityContext.AllowPrivilegeEscalation)
	assert.False(t, *containerSecurityContext.AllowPrivilegeEscalation)

	assert.NotNil(t, containerSecurityContext.ReadOnlyRootFilesystem)
	assert.True(t, *containerSecurityContext.ReadOnlyRootFilesystem)

	// SeccompProfile should be set for OpenShift
	require.NotNil(t, containerSecurityContext.SeccompProfile)
	assert.Equal(t, corev1.SeccompProfileTypeRuntimeDefault, containerSecurityContext.SeccompProfile.Type)

	// Capabilities should drop all for OpenShift
	require.NotNil(t, containerSecurityContext.Capabilities)
	assert.Equal(t, []corev1.Capability{"ALL"}, containerSecurityContext.Capabilities.Drop)
}

func TestMCPServerReconciler_DeploymentForMCPServer_PlatformDetectionError(t *testing.T) {
	t.Parallel()

	// Create a test MCPServer
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mcp-server",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "stdio",
			Port:      8080,
		},
	}

	// Create reconciler with mock platform detector that returns error
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	reconciler := &MCPServerReconciler{
		Scheme: scheme,
		platformDetector: &mockPlatformDetector{
			platform: kubernetes.PlatformKubernetes,
			err:      assert.AnError,
		},
		// Don't pre-set the platform so it will try to detect and fall back to Kubernetes
	}

	ctx := context.Background()
	deployment := reconciler.deploymentForMCPServer(ctx, mcpServer)

	require.NotNil(t, deployment, "Deployment should not be nil")

	// Should fall back to Kubernetes defaults when platform detection fails
	podSecurityContext := deployment.Spec.Template.Spec.SecurityContext
	require.NotNil(t, podSecurityContext, "Pod security context should not be nil")

	assert.NotNil(t, podSecurityContext.RunAsUser)
	assert.Equal(t, int64(1000), *podSecurityContext.RunAsUser)

	assert.NotNil(t, podSecurityContext.RunAsGroup)
	assert.Equal(t, int64(1000), *podSecurityContext.RunAsGroup)

	assert.NotNil(t, podSecurityContext.FSGroup)
	assert.Equal(t, int64(1000), *podSecurityContext.FSGroup)
}

func TestMCPServerReconciler_DeploymentForMCPServer_EnvironmentOverride(t *testing.T) {
	t.Parallel()
	t.Skip("Environment variable tests require special setup - skipping for now")
	// This test would require setting OPERATOR_OPENSHIFT environment variable
	// and testing that it overrides the platform detection logic
}

package kubernetes

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestNewSecurityContextBuilder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		platform Platform
	}{
		{
			name:     "Kubernetes platform",
			platform: PlatformKubernetes,
		},
		{
			name:     "OpenShift platform",
			platform: PlatformOpenShift,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			builder := NewSecurityContextBuilder(tt.platform)
			assert.NotNil(t, builder)
			assert.Equal(t, tt.platform, builder.platform)
		})
	}
}

func TestSecurityContextBuilder_BuildPodSecurityContext_Kubernetes(t *testing.T) {
	t.Parallel()

	builder := NewSecurityContextBuilder(PlatformKubernetes)
	podCtx := builder.BuildPodSecurityContext()

	require.NotNil(t, podCtx)

	// Verify Kubernetes-specific settings
	assert.NotNil(t, podCtx.RunAsNonRoot)
	assert.True(t, *podCtx.RunAsNonRoot)

	assert.NotNil(t, podCtx.RunAsUser)
	assert.Equal(t, int64(1000), *podCtx.RunAsUser)

	assert.NotNil(t, podCtx.RunAsGroup)
	assert.Equal(t, int64(1000), *podCtx.RunAsGroup)

	assert.NotNil(t, podCtx.FSGroup)
	assert.Equal(t, int64(1000), *podCtx.FSGroup)

	// SeccompProfile should not be explicitly set for standard Kubernetes
	assert.Nil(t, podCtx.SeccompProfile)
}

func TestSecurityContextBuilder_BuildPodSecurityContext_OpenShift(t *testing.T) {
	t.Parallel()

	builder := NewSecurityContextBuilder(PlatformOpenShift)
	podCtx := builder.BuildPodSecurityContext()

	require.NotNil(t, podCtx)

	// Verify OpenShift-specific settings
	assert.NotNil(t, podCtx.RunAsNonRoot)
	assert.True(t, *podCtx.RunAsNonRoot)

	// These should be nil to allow OpenShift SCCs to assign them
	assert.Nil(t, podCtx.RunAsUser)
	assert.Nil(t, podCtx.RunAsGroup)
	assert.Nil(t, podCtx.FSGroup)

	// SeccompProfile should be explicitly set for OpenShift
	require.NotNil(t, podCtx.SeccompProfile)
	assert.Equal(t, corev1.SeccompProfileTypeRuntimeDefault, podCtx.SeccompProfile.Type)
}

func TestSecurityContextBuilder_BuildContainerSecurityContext_Kubernetes(t *testing.T) {
	t.Parallel()

	builder := NewSecurityContextBuilder(PlatformKubernetes)
	containerCtx := builder.BuildContainerSecurityContext()

	require.NotNil(t, containerCtx)

	// Verify Kubernetes-specific settings
	assert.NotNil(t, containerCtx.Privileged)
	assert.False(t, *containerCtx.Privileged)

	assert.NotNil(t, containerCtx.RunAsNonRoot)
	assert.True(t, *containerCtx.RunAsNonRoot)

	assert.NotNil(t, containerCtx.RunAsUser)
	assert.Equal(t, int64(1000), *containerCtx.RunAsUser)

	assert.NotNil(t, containerCtx.RunAsGroup)
	assert.Equal(t, int64(1000), *containerCtx.RunAsGroup)

	assert.NotNil(t, containerCtx.AllowPrivilegeEscalation)
	assert.False(t, *containerCtx.AllowPrivilegeEscalation)

	assert.NotNil(t, containerCtx.ReadOnlyRootFilesystem)
	assert.True(t, *containerCtx.ReadOnlyRootFilesystem)

	// SeccompProfile and Capabilities should not be explicitly set for standard Kubernetes
	assert.Nil(t, containerCtx.SeccompProfile)
	assert.Nil(t, containerCtx.Capabilities)
}

func TestSecurityContextBuilder_BuildContainerSecurityContext_OpenShift(t *testing.T) {
	t.Parallel()

	builder := NewSecurityContextBuilder(PlatformOpenShift)
	containerCtx := builder.BuildContainerSecurityContext()

	require.NotNil(t, containerCtx)

	// Verify OpenShift-specific settings
	assert.NotNil(t, containerCtx.Privileged)
	assert.False(t, *containerCtx.Privileged)

	assert.NotNil(t, containerCtx.RunAsNonRoot)
	assert.True(t, *containerCtx.RunAsNonRoot)

	// These should be nil to allow OpenShift SCCs to assign them
	assert.Nil(t, containerCtx.RunAsUser)
	assert.Nil(t, containerCtx.RunAsGroup)

	assert.NotNil(t, containerCtx.AllowPrivilegeEscalation)
	assert.False(t, *containerCtx.AllowPrivilegeEscalation)

	assert.NotNil(t, containerCtx.ReadOnlyRootFilesystem)
	assert.True(t, *containerCtx.ReadOnlyRootFilesystem)

	// SeccompProfile should be explicitly set for OpenShift
	require.NotNil(t, containerCtx.SeccompProfile)
	assert.Equal(t, corev1.SeccompProfileTypeRuntimeDefault, containerCtx.SeccompProfile.Type)

	// Capabilities should drop all for OpenShift
	require.NotNil(t, containerCtx.Capabilities)
	assert.Equal(t, []corev1.Capability{"ALL"}, containerCtx.Capabilities.Drop)
}

func TestSecurityContextBuilder_ConsistentBehavior(t *testing.T) {
	t.Parallel()

	// Test that multiple calls to the same builder produce consistent results
	builder := NewSecurityContextBuilder(PlatformKubernetes)

	podCtx1 := builder.BuildPodSecurityContext()
	podCtx2 := builder.BuildPodSecurityContext()

	containerCtx1 := builder.BuildContainerSecurityContext()
	containerCtx2 := builder.BuildContainerSecurityContext()

	// Pod contexts should be equal
	assert.Equal(t, podCtx1.RunAsUser, podCtx2.RunAsUser)
	assert.Equal(t, podCtx1.RunAsGroup, podCtx2.RunAsGroup)
	assert.Equal(t, podCtx1.FSGroup, podCtx2.FSGroup)
	assert.Equal(t, podCtx1.RunAsNonRoot, podCtx2.RunAsNonRoot)

	// Container contexts should be equal
	assert.Equal(t, containerCtx1.RunAsUser, containerCtx2.RunAsUser)
	assert.Equal(t, containerCtx1.RunAsGroup, containerCtx2.RunAsGroup)
	assert.Equal(t, containerCtx1.Privileged, containerCtx2.Privileged)
	assert.Equal(t, containerCtx1.RunAsNonRoot, containerCtx2.RunAsNonRoot)
	assert.Equal(t, containerCtx1.AllowPrivilegeEscalation, containerCtx2.AllowPrivilegeEscalation)
	assert.Equal(t, containerCtx1.ReadOnlyRootFilesystem, containerCtx2.ReadOnlyRootFilesystem)
}

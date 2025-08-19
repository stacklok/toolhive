package kubernetes

import (
	corev1 "k8s.io/api/core/v1"
	corev1apply "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/utils/ptr"

	"github.com/stacklok/toolhive/pkg/logger"
)

// SecurityContextBuilder provides platform-aware security context configuration
type SecurityContextBuilder struct {
	platform Platform
}

// NewSecurityContextBuilder creates a new SecurityContextBuilder for the given platform
func NewSecurityContextBuilder(platform Platform) *SecurityContextBuilder {
	return &SecurityContextBuilder{
		platform: platform,
	}
}

// BuildPodSecurityContext creates a platform-appropriate pod security context
func (b *SecurityContextBuilder) BuildPodSecurityContext() *corev1.PodSecurityContext {
	// Start with base security context
	podSecurityContext := &corev1.PodSecurityContext{
		RunAsNonRoot: ptr.To(true),
		RunAsUser:    ptr.To(int64(1000)),
		RunAsGroup:   ptr.To(int64(1000)),
		FSGroup:      ptr.To(int64(1000)),
	}

	// Apply platform-specific modifications
	if b.platform == PlatformOpenShift {
		logger.Info("Configuring pod security context for OpenShift")
		// OpenShift uses Security Context Constraints (SCCs) to manage user/group assignments
		// Setting these to nil allows OpenShift to assign them dynamically
		podSecurityContext.RunAsUser = nil
		podSecurityContext.RunAsGroup = nil
		podSecurityContext.FSGroup = nil

		// OpenShift requires explicit seccomp profile
		podSecurityContext.SeccompProfile = &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		}
	} else {
		logger.Info("Configuring pod security context for Kubernetes")
	}

	return podSecurityContext
}

// BuildContainerSecurityContext creates a platform-appropriate container security context
func (b *SecurityContextBuilder) BuildContainerSecurityContext() *corev1.SecurityContext {
	// Start with base security context
	containerSecurityContext := &corev1.SecurityContext{
		Privileged:               ptr.To(false),
		RunAsNonRoot:             ptr.To(true),
		RunAsUser:                ptr.To(int64(1000)),
		RunAsGroup:               ptr.To(int64(1000)),
		AllowPrivilegeEscalation: ptr.To(false),
		ReadOnlyRootFilesystem:   ptr.To(true),
	}

	// Apply platform-specific modifications
	if b.platform == PlatformOpenShift {
		logger.Info("Configuring container security context for OpenShift")
		// OpenShift uses Security Context Constraints (SCCs) to manage user/group assignments
		// Setting these to nil allows OpenShift to assign them dynamically
		containerSecurityContext.RunAsUser = nil
		containerSecurityContext.RunAsGroup = nil

		// OpenShift requires explicit seccomp profile
		containerSecurityContext.SeccompProfile = &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		}

		// OpenShift security best practices: drop all capabilities
		containerSecurityContext.Capabilities = &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		}
	} else {
		logger.Info("Configuring container security context for Kubernetes")
	}

	return containerSecurityContext
}

// BuildPodSecurityContextApplyConfiguration creates a platform-appropriate pod security context
// using the ApplyConfiguration types used by the client
func (b *SecurityContextBuilder) BuildPodSecurityContextApplyConfiguration() *corev1apply.PodSecurityContextApplyConfiguration {
	baseContext := b.BuildPodSecurityContext()

	applyConfig := corev1apply.PodSecurityContext()

	if baseContext.RunAsNonRoot != nil {
		applyConfig = applyConfig.WithRunAsNonRoot(*baseContext.RunAsNonRoot)
	}

	if baseContext.RunAsUser != nil {
		applyConfig = applyConfig.WithRunAsUser(*baseContext.RunAsUser)
	}

	if baseContext.RunAsGroup != nil {
		applyConfig = applyConfig.WithRunAsGroup(*baseContext.RunAsGroup)
	}

	if baseContext.FSGroup != nil {
		applyConfig = applyConfig.WithFSGroup(*baseContext.FSGroup)
	}

	if baseContext.SeccompProfile != nil {
		applyConfig = applyConfig.WithSeccompProfile(
			corev1apply.SeccompProfile().WithType(baseContext.SeccompProfile.Type))
	}

	return applyConfig
}

// BuildContainerSecurityContextApplyConfiguration creates a platform-appropriate container security context
// using the ApplyConfiguration types used by the client
func (b *SecurityContextBuilder) BuildContainerSecurityContextApplyConfiguration() *corev1apply.SecurityContextApplyConfiguration { //nolint:lll
	baseContext := b.BuildContainerSecurityContext()

	applyConfig := corev1apply.SecurityContext()

	if baseContext.Privileged != nil {
		applyConfig = applyConfig.WithPrivileged(*baseContext.Privileged)
	}

	if baseContext.RunAsNonRoot != nil {
		applyConfig = applyConfig.WithRunAsNonRoot(*baseContext.RunAsNonRoot)
	}

	if baseContext.RunAsUser != nil {
		applyConfig = applyConfig.WithRunAsUser(*baseContext.RunAsUser)
	}

	if baseContext.RunAsGroup != nil {
		applyConfig = applyConfig.WithRunAsGroup(*baseContext.RunAsGroup)
	}

	if baseContext.AllowPrivilegeEscalation != nil {
		applyConfig = applyConfig.WithAllowPrivilegeEscalation(*baseContext.AllowPrivilegeEscalation)
	}

	if baseContext.ReadOnlyRootFilesystem != nil {
		applyConfig = applyConfig.WithReadOnlyRootFilesystem(*baseContext.ReadOnlyRootFilesystem)
	}

	if baseContext.SeccompProfile != nil {
		applyConfig = applyConfig.WithSeccompProfile(
			corev1apply.SeccompProfile().WithType(baseContext.SeccompProfile.Type))
	}

	if baseContext.Capabilities != nil {
		capabilities := corev1apply.Capabilities()
		if len(baseContext.Capabilities.Drop) > 0 {
			capabilities = capabilities.WithDrop(baseContext.Capabilities.Drop...)
		}
		if len(baseContext.Capabilities.Add) > 0 {
			capabilities = capabilities.WithAdd(baseContext.Capabilities.Add...)
		}
		applyConfig = applyConfig.WithCapabilities(capabilities)
	}

	return applyConfig
}

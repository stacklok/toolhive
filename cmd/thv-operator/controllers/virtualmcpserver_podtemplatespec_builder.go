package controllers

import (
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// VirtualMCPServerPodTemplateSpecBuilder provides an interface for building PodTemplateSpec patches for Virtual MCP Servers
type VirtualMCPServerPodTemplateSpecBuilder struct {
	spec            *corev1.PodTemplateSpec
	hasUserTemplate bool // Track if we started with user-provided template
}

// NewVirtualMCPServerPodTemplateSpecBuilder creates a new builder, optionally starting
// with a user-provided template. Returns an error if the provided raw extension cannot
// be unmarshaled into a valid PodTemplateSpec.
func NewVirtualMCPServerPodTemplateSpecBuilder(
	userTemplateRaw *runtime.RawExtension,
) (*VirtualMCPServerPodTemplateSpecBuilder, error) {
	var spec *corev1.PodTemplateSpec
	hasUserTemplate := false

	if userTemplateRaw != nil && userTemplateRaw.Raw != nil {
		// Try to unmarshal the raw extension into a PodTemplateSpec
		var userTemplate corev1.PodTemplateSpec
		if err := json.Unmarshal(userTemplateRaw.Raw, &userTemplate); err != nil {
			// Return error if unmarshaling fails - this indicates invalid PodTemplateSpec data
			return nil, fmt.Errorf("failed to unmarshal PodTemplateSpec: %w", err)
		}
		spec = (&userTemplate).DeepCopy()
		hasUserTemplate = true
	} else {
		spec = &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{},
			},
		}
	}

	return &VirtualMCPServerPodTemplateSpecBuilder{
		spec:            spec,
		hasUserTemplate: hasUserTemplate,
	}, nil
}

// Build returns the final PodTemplateSpec, or nil if no customizations were made
func (b *VirtualMCPServerPodTemplateSpecBuilder) Build() *corev1.PodTemplateSpec {
	// If user provided a template, always return it (even if "empty")
	if b.hasUserTemplate {
		return b.spec
	}

	// Otherwise, only return if we added customizations
	if b.isEmpty() {
		return nil
	}
	return b.spec
}

// isEmpty checks if the builder contains any meaningful customizations
func (b *VirtualMCPServerPodTemplateSpecBuilder) isEmpty() bool {
	// Check if spec has any meaningful customizations
	spec := b.spec.Spec

	return spec.ServiceAccountName == "" &&
		len(spec.Containers) == 0 &&
		len(spec.Volumes) == 0 &&
		len(spec.InitContainers) == 0 &&
		len(spec.Tolerations) == 0 &&
		len(spec.NodeSelector) == 0 &&
		spec.Affinity == nil &&
		spec.SecurityContext == nil &&
		spec.PriorityClassName == "" &&
		len(spec.ImagePullSecrets) == 0 &&
		len(b.spec.Labels) == 0 &&
		len(b.spec.Annotations) == 0
}

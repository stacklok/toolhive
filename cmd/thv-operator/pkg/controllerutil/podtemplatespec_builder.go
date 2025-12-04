// Package controllerutil provides shared utilities for ToolHive Kubernetes controllers.
package controllerutil

import (
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// PodTemplateSpecBuilder provides an interface for building PodTemplateSpec patches.
// It is used by both MCPServer and VirtualMCPServer controllers.
type PodTemplateSpecBuilder struct {
	spec          *corev1.PodTemplateSpec
	containerName string // Container name for WithSecrets (e.g., "mcp" or "vmcp")
}

// NewPodTemplateSpecBuilder creates a new builder, optionally starting with a user-provided template.
// The containerName parameter specifies which container WithSecrets() will target.
// Returns an error if the provided raw extension cannot be unmarshaled into a valid PodTemplateSpec.
func NewPodTemplateSpecBuilder(userTemplateRaw *runtime.RawExtension, containerName string) (*PodTemplateSpecBuilder, error) {
	if containerName == "" {
		return nil, fmt.Errorf("containerName cannot be empty")
	}

	spec, err := parsePodTemplateSpec(userTemplateRaw)
	if err != nil {
		return nil, err
	}

	if spec == nil {
		spec = &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{},
			},
		}
	}

	return &PodTemplateSpecBuilder{
		spec:          spec,
		containerName: containerName,
	}, nil
}

// WithServiceAccount sets the service account name if non-empty.
func (b *PodTemplateSpecBuilder) WithServiceAccount(serviceAccount *string) *PodTemplateSpecBuilder {
	if serviceAccount != nil && *serviceAccount != "" {
		b.spec.Spec.ServiceAccountName = *serviceAccount
	}
	return b
}

// WithSecrets adds secret environment variables to the target container.
// The target container is specified by containerName in the constructor.
func (b *PodTemplateSpecBuilder) WithSecrets(secrets []mcpv1alpha1.SecretRef) *PodTemplateSpecBuilder {
	if len(secrets) == 0 {
		return b
	}

	// Generate secret env vars
	secretEnvVars := make([]corev1.EnvVar, 0, len(secrets))
	for _, secret := range secrets {
		targetEnv := secret.Key
		if secret.TargetEnvName != "" {
			targetEnv = secret.TargetEnvName
		}

		secretEnvVars = append(secretEnvVars, corev1.EnvVar{
			Name: targetEnv,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: secret.Name,
					},
					Key: secret.Key,
				},
			},
		})
	}

	// Find existing container or create new one
	containerIndex := -1
	for i, container := range b.spec.Spec.Containers {
		if container.Name == b.containerName {
			containerIndex = i
			break
		}
	}

	if containerIndex >= 0 {
		// Merge env vars into existing container
		b.spec.Spec.Containers[containerIndex].Env = append(
			b.spec.Spec.Containers[containerIndex].Env,
			secretEnvVars...,
		)
	} else {
		// Add new container with env vars
		b.spec.Spec.Containers = append(b.spec.Spec.Containers, corev1.Container{
			Name: b.containerName,
			Env:  secretEnvVars,
		})
	}
	return b
}

// Build returns the final PodTemplateSpec, or nil if no customizations were made.
func (b *PodTemplateSpecBuilder) Build() *corev1.PodTemplateSpec {
	if b.isEmpty() {
		return nil
	}
	return b.spec
}

// isEmpty checks if the builder contains any meaningful customizations.
func (b *PodTemplateSpecBuilder) isEmpty() bool {
	if b.spec == nil {
		return true
	}

	podSpec := b.spec.Spec

	return podSpec.ServiceAccountName == "" &&
		len(podSpec.Containers) == 0 &&
		len(podSpec.Volumes) == 0 &&
		len(podSpec.InitContainers) == 0 &&
		len(podSpec.Tolerations) == 0 &&
		len(podSpec.NodeSelector) == 0 &&
		podSpec.Affinity == nil &&
		podSpec.SecurityContext == nil &&
		podSpec.PriorityClassName == "" &&
		len(podSpec.ImagePullSecrets) == 0 &&
		len(b.spec.Labels) == 0 &&
		len(b.spec.Annotations) == 0
}

// parsePodTemplateSpec parses a RawExtension into a PodTemplateSpec.
// Returns (nil, nil) if raw is nil or raw.Raw is nil.
// Returns (*PodTemplateSpec, nil) on success (returns a deep copy).
// Returns (nil, error) if JSON unmarshal fails.
func parsePodTemplateSpec(raw *runtime.RawExtension) (*corev1.PodTemplateSpec, error) {
	if raw == nil || raw.Raw == nil {
		return nil, nil
	}

	var spec corev1.PodTemplateSpec
	if err := json.Unmarshal(raw.Raw, &spec); err != nil {
		return nil, fmt.Errorf("failed to unmarshal PodTemplateSpec: %w", err)
	}

	return spec.DeepCopy(), nil
}

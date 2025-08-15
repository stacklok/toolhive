package controllers

import (
	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

// MCPServerPodTemplateSpecBuilder provides an interface for building PodTemplateSpec patches for MCP Servers
type MCPServerPodTemplateSpecBuilder struct {
	spec *corev1.PodTemplateSpec
}

// NewMCPServerPodTemplateSpecBuilder creates a new builder, optionally starting with a user-provided template
func NewMCPServerPodTemplateSpecBuilder(userTemplate *corev1.PodTemplateSpec) *MCPServerPodTemplateSpecBuilder {
	var spec *corev1.PodTemplateSpec
	if userTemplate != nil {
		spec = userTemplate.DeepCopy()
	} else {
		spec = &corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{},
			},
		}
	}

	return &MCPServerPodTemplateSpecBuilder{spec: spec}
}

// WithServiceAccount sets the service account name
func (b *MCPServerPodTemplateSpecBuilder) WithServiceAccount(serviceAccount *string) *MCPServerPodTemplateSpecBuilder {
	if serviceAccount != nil && *serviceAccount != "" {
		b.spec.Spec.ServiceAccountName = *serviceAccount
	}
	return b
}

// WithSecrets adds secret environment variables to the MCP container
func (b *MCPServerPodTemplateSpecBuilder) WithSecrets(secrets []mcpv1alpha1.SecretRef) *MCPServerPodTemplateSpecBuilder {
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

	if len(secretEnvVars) == 0 {
		return b
	}

	// add secret env vars to MCP container
	mcpIndex := -1
	for i, container := range b.spec.Spec.Containers {
		if container.Name == mcpContainerName {
			mcpIndex = i
			break
		}
	}

	if mcpIndex >= 0 {
		// Merge env vars into existing MCP container
		b.spec.Spec.Containers[mcpIndex].Env = append(
			b.spec.Spec.Containers[mcpIndex].Env,
			secretEnvVars...,
		)
	} else {
		// Add new MCP container with env vars
		b.spec.Spec.Containers = append(b.spec.Spec.Containers, corev1.Container{
			Name: mcpContainerName,
			Env:  secretEnvVars,
		})
	}
	return b
}

// Build returns the final PodTemplateSpec, or nil if no customizations were made
func (b *MCPServerPodTemplateSpecBuilder) Build() *corev1.PodTemplateSpec {
	// Return nil if the spec is effectively empty (no meaningful customizations)
	if b.isEmpty() {
		return nil
	}
	return b.spec
}

// isEmpty checks if the builder contains any meaningful customizations
func (b *MCPServerPodTemplateSpecBuilder) isEmpty() bool {
	return b.spec.Spec.ServiceAccountName == "" &&
		len(b.spec.Spec.Containers) == 0
}

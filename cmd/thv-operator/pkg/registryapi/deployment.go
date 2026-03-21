// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package registryapi provides deployment management for the registry API component.
package registryapi

import (
	"context"
	"fmt"
	"os"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/registryapi/config"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
)

const (
	// configHashAnnotation is the annotation key for the MCPRegistry spec hash on the pod template.
	// Changes to this hash trigger a pod rollout.
	configHashAnnotation = "toolhive.stacklok.dev/config-hash"

	// podTemplateSpecHashAnnotation is the annotation key for the user-provided PodTemplateSpec hash
	// on the Deployment metadata. Used to detect PodTemplateSpec changes without comparing
	// full rendered templates (which include Kubernetes-defaulted fields).
	podTemplateSpecHashAnnotation = "toolhive.stacklok.io/podtemplatespec-hash"
)

// CheckAPIReadiness verifies that the deployed registry-API Deployment is ready
// by checking deployment status for ready replicas. Returns true if the deployment
// has at least one ready replica, false otherwise.
func (*manager) CheckAPIReadiness(ctx context.Context, deployment *appsv1.Deployment) bool {
	ctxLogger := log.FromContext(ctx)

	// Handle nil deployment gracefully
	if deployment == nil {
		ctxLogger.V(1).Info("Deployment is nil, not ready")
		return false
	}

	// Log deployment status for debugging
	ctxLogger.V(1).Info("Checking deployment readiness",
		"deployment", deployment.Name,
		"namespace", deployment.Namespace,
		"replicas", deployment.Status.Replicas,
		"readyReplicas", deployment.Status.ReadyReplicas,
		"availableReplicas", deployment.Status.AvailableReplicas,
		"updatedReplicas", deployment.Status.UpdatedReplicas)

	// Check if deployment has ready replicas
	if deployment.Status.ReadyReplicas > 0 {
		ctxLogger.V(1).Info("Deployment is ready",
			"deployment", deployment.Name,
			"readyReplicas", deployment.Status.ReadyReplicas)
		return true
	}

	// Check deployment conditions for additional context
	for _, condition := range deployment.Status.Conditions {
		if condition.Type == appsv1.DeploymentProgressing {
			if condition.Status == corev1.ConditionFalse {
				ctxLogger.Info("Deployment is not progressing",
					"deployment", deployment.Name,
					"reason", condition.Reason,
					"message", condition.Message)
			}
		}
	}

	ctxLogger.V(1).Info("Deployment is not ready yet",
		"deployment", deployment.Name,
		"readyReplicas", deployment.Status.ReadyReplicas)

	return false
}

// ensureDeployment creates or updates the registry-api Deployment for the MCPRegistry.
// This function handles the Kubernetes API operations (Get, Create, Update) and delegates
// deployment configuration to buildRegistryAPIDeployment.
func (m *manager) ensureDeployment(
	ctx context.Context,
	mcpRegistry *mcpv1alpha1.MCPRegistry,
	configManager config.ConfigManager,
) (*appsv1.Deployment, error) {
	ctxLogger := log.FromContext(ctx).WithValues("mcpregistry", mcpRegistry.Name)

	// Build the desired deployment configuration
	deployment := m.buildRegistryAPIDeployment(ctx, mcpRegistry, configManager)
	deploymentName := deployment.Name

	// Set owner reference for automatic garbage collection
	if err := controllerutil.SetControllerReference(mcpRegistry, deployment, m.scheme); err != nil {
		ctxLogger.Error(err, "Failed to set controller reference for deployment")
		return nil, fmt.Errorf("failed to set controller reference for deployment: %w", err)
	}

	// Check if deployment already exists
	existing := &appsv1.Deployment{}
	err := m.client.Get(ctx, client.ObjectKey{
		Name:      deploymentName,
		Namespace: mcpRegistry.Namespace,
	}, existing)

	if err != nil {
		if errors.IsNotFound(err) {
			// Deployment doesn't exist, create it
			ctxLogger.Info("Creating registry-api deployment", "deployment", deploymentName)
			if err := m.client.Create(ctx, deployment); err != nil {
				ctxLogger.Error(err, "Failed to create deployment")
				return nil, fmt.Errorf("failed to create deployment %s: %w", deploymentName, err)
			}
			ctxLogger.Info("Successfully created registry-api deployment", "deployment", deploymentName)
			return deployment, nil
		}
		// Unexpected error
		ctxLogger.Error(err, "Failed to get deployment")
		return nil, fmt.Errorf("failed to get deployment %s: %w", deploymentName, err)
	}

	// Check if the deployment needs to be updated
	if !deploymentNeedsUpdate(existing, deployment) {
		ctxLogger.V(1).Info("Deployment already up-to-date, skipping update", "deployment", deploymentName)
		return existing, nil
	}

	// Selective field update: update Spec.Template and metadata, preserve Spec.Replicas for HPA
	existing.Spec.Template = deployment.Spec.Template
	existing.Labels = deployment.Labels

	// Merge annotations to preserve Kubernetes-managed annotations (e.g., deployment.kubernetes.io/revision)
	if existing.Annotations == nil {
		existing.Annotations = make(map[string]string)
	}
	for k, v := range deployment.Annotations {
		existing.Annotations[k] = v
	}

	// Ensure owner reference is set
	if err := controllerutil.SetControllerReference(mcpRegistry, existing, m.scheme); err != nil {
		return nil, fmt.Errorf("failed to set controller reference for existing deployment: %w", err)
	}

	if err := m.client.Update(ctx, existing); err != nil {
		ctxLogger.Error(err, "Failed to update deployment")
		return nil, fmt.Errorf("failed to update deployment %s: %w", deploymentName, err)
	}

	ctxLogger.Info("Successfully updated registry-api deployment", "deployment", deploymentName)
	return existing, nil
}

// buildRegistryAPIDeployment creates and configures a Deployment object for the registry API.
// This function handles all deployment configuration including labels, container specs, probes,
// and storage manager integration. It returns a fully configured deployment ready for Kubernetes API operations.
func (*manager) buildRegistryAPIDeployment(
	ctx context.Context,
	mcpRegistry *mcpv1alpha1.MCPRegistry,
	configManager config.ConfigManager,
) *appsv1.Deployment {
	ctxLogger := log.FromContext(ctx).WithValues("mcpregistry", mcpRegistry.Name)
	// Generate deployment name using the established pattern
	deploymentName := mcpRegistry.GetAPIResourceName()

	// Define labels using common function
	labels := labelsForRegistryAPI(mcpRegistry, deploymentName)

	// Parse user-provided PodTemplateSpec if present
	var userPTS *corev1.PodTemplateSpec
	if mcpRegistry.HasPodTemplateSpec() {
		var err error
		userPTS, err = ParsePodTemplateSpec(mcpRegistry.GetPodTemplateSpecRaw())
		if err != nil {
			ctxLogger.Error(err, "Failed to parse PodTemplateSpec")
			return nil
		}
	}

	// Compute config hash from the full MCPRegistry spec to detect any spec changes
	configHash := ctrlutil.CalculateConfigHash(mcpRegistry.Spec)

	// Build list of options for PodTemplateSpec
	opts := []PodTemplateSpecOption{
		WithLabels(labels),
		WithAnnotations(map[string]string{
			configHashAnnotation: configHash,
		}),
		WithServiceAccountName(GetServiceAccountName(mcpRegistry)),
		WithContainer(BuildRegistryAPIContainer(getRegistryAPIImage())),
		WithRegistryServerConfigMount(registryAPIContainerName, configManager.GetRegistryServerConfigMapName()),
		WithRegistrySourceMounts(registryAPIContainerName, mcpRegistry.Spec.Registries),
		WithRegistryStorageMount(registryAPIContainerName),
	}

	// Add pgpass mount if databaseConfig is specified
	if mcpRegistry.HasDatabaseConfig() {
		secretName := mcpRegistry.BuildPGPassSecretName()
		opts = append(opts, WithPGPassMount(registryAPIContainerName, secretName))
	}

	// Add git auth mounts for registries that have authentication configured
	for _, registry := range mcpRegistry.Spec.Registries {
		if registry.Git != nil && registry.Git.Auth != nil {
			opts = append(opts, WithGitAuthMount(registryAPIContainerName, registry.Git.Auth.PasswordSecretRef))
		}
	}

	// Build PodTemplateSpec with defaults and user customizations merged
	builder := NewPodTemplateSpecBuilderFrom(userPTS)
	podTemplateSpec := builder.Apply(opts...).Build()

	// Build deployment-level annotations with PodTemplateSpec hash for change detection
	deploymentAnnotations := make(map[string]string)
	if mcpRegistry.HasPodTemplateSpec() && mcpRegistry.Spec.PodTemplateSpec.Raw != nil {
		hash, err := checksum.HashRawJSON(mcpRegistry.Spec.PodTemplateSpec.Raw)
		if err == nil {
			deploymentAnnotations[podTemplateSpecHashAnnotation] = hash
		}
	}

	// Create basic deployment specification with named container
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        deploymentName,
			Namespace:   mcpRegistry.Namespace,
			Labels:      labels,
			Annotations: deploymentAnnotations,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &[]int32{DefaultReplicas}[0], // Single replica for registry API
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/name":      deploymentName,
					"app.kubernetes.io/component": "registry-api",
				},
			},
			Template: podTemplateSpec,
		},
	}

	return deployment
}

// deploymentNeedsUpdate checks if the existing deployment differs from the desired one
// by comparing hash annotations. This avoids endless reconciliation loops caused by
// Kubernetes-defaulted fields (terminationGracePeriodSeconds, dnsPolicy, etc.) that
// would always differ when comparing full specs with reflect.DeepEqual.
func deploymentNeedsUpdate(existing, desired *appsv1.Deployment) bool {
	if existing == nil || desired == nil {
		return true
	}

	// Check if the config hash (derived from MCPRegistry spec) has changed
	existingConfigHash := existing.Spec.Template.Annotations[configHashAnnotation]
	desiredConfigHash := desired.Spec.Template.Annotations[configHashAnnotation]
	if existingConfigHash != desiredConfigHash {
		return true
	}

	// Check if the user-provided PodTemplateSpec has changed
	existingPTSHash := existing.Annotations[podTemplateSpecHashAnnotation]
	desiredPTSHash := desired.Annotations[podTemplateSpecHashAnnotation]
	if existingPTSHash != desiredPTSHash {
		return true
	}

	// Check if the container image has changed (e.g., from TOOLHIVE_REGISTRY_API_IMAGE env override)
	if len(existing.Spec.Template.Spec.Containers) > 0 && len(desired.Spec.Template.Spec.Containers) > 0 {
		if existing.Spec.Template.Spec.Containers[0].Image != desired.Spec.Template.Spec.Containers[0].Image {
			return true
		}
	}

	return false
}

// getRegistryAPIImage returns the registry API container image to use
func getRegistryAPIImage() string {
	return getRegistryAPIImageWithEnvGetter(os.Getenv)
}

// getRegistryAPIImageWithEnvGetter returns the registry API container image to use
// with a custom environment variable getter function for testing
func getRegistryAPIImageWithEnvGetter(envGetter func(string) string) string {
	if img := envGetter("TOOLHIVE_REGISTRY_API_IMAGE"); img != "" {
		return img
	}
	return "ghcr.io/stacklok/thv-registry-api:latest"
}

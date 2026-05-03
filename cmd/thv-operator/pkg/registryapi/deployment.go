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

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
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

// upsertDeployment creates or updates a registry-api Deployment for the given MCPRegistry.
// It sets the owner reference, checks for an existing deployment, and either creates,
// updates (preserving Spec.Replicas for HPA compatibility), or skips if already up-to-date.
func (m *manager) upsertDeployment(
	ctx context.Context,
	mcpRegistry *mcpv1beta1.MCPRegistry,
	deployment *appsv1.Deployment,
) (*appsv1.Deployment, error) {
	ctxLogger := log.FromContext(ctx).WithValues("mcpregistry", mcpRegistry.Name)
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

	// Ensure owner reference is set on the existing object
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

// ensureDeployment creates or updates the registry-api Deployment for the MCPRegistry.
// It builds the deployment via buildRegistryAPIDeployment and delegates the create-or-update
// logic to upsertDeployment.
func (m *manager) ensureDeployment(
	ctx context.Context,
	mcpRegistry *mcpv1beta1.MCPRegistry,
	configMapName string,
) (*appsv1.Deployment, error) {
	deployment, err := m.buildRegistryAPIDeployment(ctx, mcpRegistry, configMapName)
	if err != nil {
		return nil, fmt.Errorf("failed to build deployment: %w", err)
	}

	return m.upsertDeployment(ctx, mcpRegistry, deployment)
}

// buildRegistryAPIDeployment creates a Deployment for the registry API. It mounts a ConfigMap
// created from the raw ConfigYAML string and supports user-provided Volumes, VolumeMounts,
// and PGPassSecretRef.
func (m *manager) buildRegistryAPIDeployment(
	ctx context.Context,
	mcpRegistry *mcpv1beta1.MCPRegistry,
	configMapName string,
) (*appsv1.Deployment, error) {
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
			return nil, fmt.Errorf("failed to parse PodTemplateSpec: %w", err)
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
		WithRegistryServerConfigMount(RegistryAPIContainerName, configMapName),
		WithImagePullSecrets(m.imagePullSecretsDefaults.Merge(mcpRegistry.Spec.ImagePullSecrets)),
	}

	// Add user-provided volumes (deserialized from raw JSON)
	userVolumes, err := mcpRegistry.Spec.ParseVolumes()
	if err != nil {
		return nil, fmt.Errorf("failed to parse user-provided volumes: %w", err)
	}
	for _, vol := range userVolumes {
		opts = append(opts, WithVolume(vol))
	}

	// Add user-provided volume mounts (deserialized from raw JSON)
	userMounts, err := mcpRegistry.Spec.ParseVolumeMounts()
	if err != nil {
		return nil, fmt.Errorf("failed to parse user-provided volume mounts: %w", err)
	}
	for _, mount := range userMounts {
		opts = append(opts, WithVolumeMount(RegistryAPIContainerName, mount))
	}

	// Add pgpass mount if a pre-created pgpass secret reference is specified
	if mcpRegistry.Spec.PGPassSecretRef != nil {
		opts = append(opts, WithPGPassSecretRefMount(RegistryAPIContainerName, *mcpRegistry.Spec.PGPassSecretRef))
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

	return deployment, nil
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

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
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/registryapi/config"
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
	deployment, err := m.buildRegistryAPIDeployment(mcpRegistry, configManager)
	if err != nil {
		ctxLogger.Error(err, "Failed to build deployment configuration")
		return nil, fmt.Errorf("failed to build deployment configuration: %w", err)
	}
	deploymentName := deployment.Name

	// Set owner reference for automatic garbage collection
	if err := controllerutil.SetControllerReference(mcpRegistry, deployment, m.scheme); err != nil {
		ctxLogger.Error(err, "Failed to set controller reference for deployment")
		return nil, fmt.Errorf("failed to set controller reference for deployment: %w", err)
	}

	// Check if deployment already exists
	existing := &appsv1.Deployment{}
	err = m.client.Get(ctx, client.ObjectKey{
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

	// TODO: Implement deployment updates when needed (e.g., when config hash changes)
	// For now, just return the existing deployment to avoid endless reconciliation loops
	ctxLogger.Info("Deployment already exists, skipping update", "deployment", deploymentName)
	return existing, nil
}

// buildRegistryAPIDeployment creates and configures a Deployment object for the registry API.
// This function handles all deployment configuration including labels, container specs, probes,
// and storage manager integration. It returns a fully configured deployment ready for Kubernetes API operations.
func (m *manager) buildRegistryAPIDeployment(
	mcpRegistry *mcpv1alpha1.MCPRegistry,
	configManager config.ConfigManager,
) (*appsv1.Deployment, error) {
	// Generate deployment name using the established pattern
	deploymentName := mcpRegistry.GetAPIResourceName()

	// Define labels using common function
	labels := labelsForRegistryAPI(mcpRegistry, deploymentName)

	// Build the PodTemplateSpec using the functional options pattern
	podTemplateSpec := DefaultRegistryAPIPodTemplateSpec(labels, "hash-dummy-value")

	// Create basic deployment specification with named container
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName,
			Namespace: mcpRegistry.Namespace,
			Labels:    labels,
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

	// Configure storage-specific aspects using the new inverted dependency approach
	if err := m.configureRegistryServerConfigMounts(deployment, registryAPIContainerName, configManager); err != nil {
		return nil, fmt.Errorf("failed to configure registry server config mounts: %w", err)
	}

	if err := m.configureRegistrySourceMounts(deployment, mcpRegistry, registryAPIContainerName); err != nil {
		return nil, fmt.Errorf("failed to configure registry source mounts: %w", err)
	}

	if err := m.configureRegistryStorageMounts(deployment, registryAPIContainerName); err != nil {
		return nil, fmt.Errorf("failed to configure registry storage mounts: %w", err)
	}

	return deployment, nil
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

// findContainerByName finds a container by name in a slice of containers
func findContainerByName(containers []corev1.Container, name string) *corev1.Container {
	for i := range containers {
		if containers[i].Name == name {
			return &containers[i]
		}
	}
	return nil
}

// hasVolume checks if a volume with the given name exists in the volumes slice
func hasVolume(volumes []corev1.Volume, name string) bool {
	for _, volume := range volumes {
		if volume.Name == name {
			return true
		}
	}
	return false
}

// hasVolumeMount checks if a volume mount with the given name exists in the volume mounts slice
func hasVolumeMount(volumeMounts []corev1.VolumeMount, name string) bool {
	for _, mount := range volumeMounts {
		if mount.Name == name {
			return true
		}
	}
	return false
}

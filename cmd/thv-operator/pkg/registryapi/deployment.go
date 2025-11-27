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
	deployment := m.buildRegistryAPIDeployment(mcpRegistry, configManager)
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

	// TODO: Implement deployment updates when needed (e.g., when config hash changes)
	// For now, just return the existing deployment to avoid endless reconciliation loops
	ctxLogger.Info("Deployment already exists, skipping update", "deployment", deploymentName)
	return existing, nil
}

// buildRegistryAPIDeployment creates and configures a Deployment object for the registry API.
// This function handles all deployment configuration including labels, container specs, probes,
// and storage manager integration. It returns a fully configured deployment ready for Kubernetes API operations.
func (*manager) buildRegistryAPIDeployment(
	mcpRegistry *mcpv1alpha1.MCPRegistry,
	configManager config.ConfigManager,
) *appsv1.Deployment {
	// Generate deployment name using the established pattern
	deploymentName := mcpRegistry.GetAPIResourceName()

	// Define labels using common function
	labels := labelsForRegistryAPI(mcpRegistry, deploymentName)

	// Build the PodTemplateSpec using the functional options pattern
	// This includes all mount configurations via the builder pattern
	builder := NewPodTemplateSpecBuilder()
	podTemplateSpec := builder.Apply(
		WithLabels(labels),
		WithAnnotations(map[string]string{
			"toolhive.stacklok.dev/config-hash": "hash-dummy-value",
		}),
		WithServiceAccountName(DefaultServiceAccountName),
		WithContainer(BuildRegistryAPIContainer(getRegistryAPIImage())),
		WithRegistryServerConfigMount(registryAPIContainerName, configManager.GetRegistryServerConfigMapName()),
		WithRegistrySourceMounts(registryAPIContainerName, mcpRegistry.Spec.Registries),
		WithRegistryStorageMount(registryAPIContainerName),
	).Build()

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

	return deployment
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

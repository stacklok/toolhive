package registryapi

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/mcpregistrystatus"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/sources"
)

// manager implements the Manager interface
type manager struct {
	client               client.Client
	scheme               *runtime.Scheme
	storageManager       sources.StorageManager
	sourceHandlerFactory sources.SourceHandlerFactory
}

// NewManager creates a new registry API manager
func NewManager(
	k8sClient client.Client,
	scheme *runtime.Scheme,
	storageManager sources.StorageManager,
	sourceHandlerFactory sources.SourceHandlerFactory,
) Manager {
	return &manager{
		client:               k8sClient,
		scheme:               scheme,
		storageManager:       storageManager,
		sourceHandlerFactory: sourceHandlerFactory,
	}
}

// ReconcileAPIService orchestrates the deployment, service creation, and readiness checking for the registry API.
// This method coordinates all aspects of API service including creating/updating the deployment and service,
// checking readiness, and updating the MCPRegistry status with deployment references and endpoint information.
func (m *manager) ReconcileAPIService(
	ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry,
) *mcpregistrystatus.Error {
	ctxLogger := log.FromContext(ctx).WithValues("mcpregistry", mcpRegistry.Name)
	ctxLogger.Info("Reconciling API service")

	// Step 1: Ensure deployment exists and is configured correctly
	deployment, err := m.ensureDeployment(ctx, mcpRegistry)
	if err != nil {
		ctxLogger.Error(err, "Failed to ensure deployment")
		return &mcpregistrystatus.Error{
			Err:             err,
			Message:         fmt.Sprintf("Failed to ensure deployment: %v", err),
			ConditionType:   mcpv1alpha1.ConditionAPIReady,
			ConditionReason: "DeploymentFailed",
		}
	}

	// Step 2: Ensure service exists and is configured correctly
	_, err = m.ensureService(ctx, mcpRegistry)
	if err != nil {
		ctxLogger.Error(err, "Failed to ensure service")
		return &mcpregistrystatus.Error{
			Err:             err,
			Message:         fmt.Sprintf("Failed to ensure service: %v", err),
			ConditionType:   mcpv1alpha1.ConditionAPIReady,
			ConditionReason: "ServiceFailed",
		}
	}

	// Step 3: Check API readiness
	isReady := m.CheckAPIReadiness(ctx, deployment)

	// Note: Status updates are now handled by the controller
	// The controller can call IsAPIReady to check readiness and update status accordingly

	// Step 4: Log completion status
	if isReady {
		ctxLogger.Info("API service reconciliation completed successfully - API is ready")
	} else {
		ctxLogger.Info("API service reconciliation completed - API is not ready yet")
	}

	return nil
}

// IsAPIReady checks if the registry API deployment is ready and serving requests
func (m *manager) IsAPIReady(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) bool {
	ctxLogger := log.FromContext(ctx).WithValues("mcpregistry", mcpRegistry.Name)

	deploymentName := mcpRegistry.GetAPIResourceName()
	deployment := &appsv1.Deployment{}

	err := m.client.Get(ctx, client.ObjectKey{
		Name:      deploymentName,
		Namespace: mcpRegistry.Namespace,
	}, deployment)

	if err != nil {
		ctxLogger.Info("API deployment not found, considering not ready", "error", err)
		return false
	}

	// Delegate to the existing CheckAPIReadiness method for consistency
	return m.CheckAPIReadiness(ctx, deployment)
}

// ConfigureDeploymentStorage configures a deployment with storage-specific requirements.
// This method inverts the dependency by having the deployment manager configure itself
// based on the storage manager type, following the dependency inversion principle.
func (m *manager) configureDeploymentStorage(
	deployment *appsv1.Deployment,
	mcpRegistry *mcpv1alpha1.MCPRegistry,
	containerName string,
) error {
	// Use string-based switching to handle different storage manager implementations
	// This provides cleaner code while maintaining type safety through string constants
	switch m.storageManager.GetType() {
	case sources.StorageTypeConfigMap:
		return m.configureConfigMapStorage(deployment, mcpRegistry, containerName)
	default:
		return fmt.Errorf("unsupported storage manager type: %s", m.storageManager.GetType())
	}
}

// configureConfigMapStorage handles ConfigMap-specific deployment configuration
func (*manager) configureConfigMapStorage(
	deployment *appsv1.Deployment,
	mcpRegistry *mcpv1alpha1.MCPRegistry,
	containerName string,
) error {
	// Get the ConfigMap name that will be used by the storage manager
	configMapName := getConfigMapName(mcpRegistry)

	// Find the container by name
	container := findContainerByName(deployment.Spec.Template.Spec.Containers, containerName)
	if container == nil {
		return fmt.Errorf("container '%s' not found in deployment", containerName)
	}

	// Replace container args completely with the correct set of arguments
	// This ensures idempotent behavior across multiple reconciliations
	filePath := fmt.Sprintf("%s/%s", RegistryDataMountPath, sources.ConfigMapStorageDataKey)
	container.Args = []string{
		ServeCommand,
		fmt.Sprintf("--from-file=%s", filePath),
		fmt.Sprintf("--registry-name=%s", mcpRegistry.Name),
	}

	// Add ConfigMap volume to deployment if not already present
	volumeName := RegistryDataVolumeName
	if !hasVolume(deployment.Spec.Template.Spec.Volumes, volumeName) {
		volume := corev1.Volume{
			Name: volumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: configMapName,
					},
				},
			},
		}
		deployment.Spec.Template.Spec.Volumes = append(deployment.Spec.Template.Spec.Volumes, volume)
	}

	// Add volume mount to the container if not already present
	mountPath := RegistryDataMountPath
	if !hasVolumeMount(container.VolumeMounts, volumeName) {
		volumeMount := corev1.VolumeMount{
			Name:      volumeName,
			MountPath: mountPath,
			ReadOnly:  true,
		}
		container.VolumeMounts = append(container.VolumeMounts, volumeMount)
	}

	return nil
}

// getConfigMapName generates the ConfigMap name for registry storage
// This mirrors the logic in ConfigMapStorageManager to maintain consistency
func getConfigMapName(mcpRegistry *mcpv1alpha1.MCPRegistry) string {
	return mcpRegistry.GetStorageName()
}

// labelsForRegistryAPI generates standard labels for registry API resources
func labelsForRegistryAPI(mcpRegistry *mcpv1alpha1.MCPRegistry, resourceName string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":             resourceName,
		"app.kubernetes.io/component":        "registry-api",
		"app.kubernetes.io/managed-by":       "toolhive-operator",
		"toolhive.stacklok.io/registry-name": mcpRegistry.Name,
	}
}

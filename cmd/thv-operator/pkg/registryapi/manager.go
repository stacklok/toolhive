package registryapi

import (
	"context"
	"fmt"
	"path/filepath"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/mcpregistrystatus"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/registryapi/config"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
)

// manager implements the Manager interface
type manager struct {
	client client.Client
	scheme *runtime.Scheme
}

// NewManager creates a new registry API manager
func NewManager(
	k8sClient client.Client,
	scheme *runtime.Scheme,
) Manager {
	return &manager{
		client: k8sClient,
		scheme: scheme,
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

	// Create ConfigManager locally to avoid concurrency issues
	configManager, err := config.NewConfigManager(m.client, m.scheme, checksum.NewRunConfigConfigMapChecksum(), mcpRegistry)
	if err != nil {
		return &mcpregistrystatus.Error{
			Err:             err,
			Message:         fmt.Sprintf("failed to create config manager: %v", err),
			ConditionType:   mcpv1alpha1.ConditionAPIReady,
			ConditionReason: "ConfigManagerFailed",
		}
	}

	// Pass configManager to methods that need it
	err = m.ensureRegistryServerConfigConfigMap(ctx, mcpRegistry, configManager)
	if err != nil {
		ctxLogger.Error(err, "Failed to ensure registry server config config map")
		return &mcpregistrystatus.Error{
			Err:             err,
			Message:         fmt.Sprintf("Failed to ensure registry server config config map: %v", err),
			ConditionType:   mcpv1alpha1.ConditionAPIReady,
			ConditionReason: "ConfigMapFailed",
		}
	}

	// Step 1: Ensure deployment exists and is configured correctly
	deployment, err := m.ensureDeployment(ctx, mcpRegistry, configManager)
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

func (*manager) configureRegistryServerConfigMounts(
	deployment *appsv1.Deployment,
	containerName string,
	configManager config.ConfigManager,
) error {

	// Find the container by name
	container := findContainerByName(deployment.Spec.Template.Spec.Containers, containerName)
	if container == nil {
		return fmt.Errorf("container '%s' not found in deployment", containerName)
	}

	// Replace container args completely with the correct set of arguments
	// This ensures idempotent behavior across multiple reconciliations
	container.Args = []string{
		ServeCommand,
		fmt.Sprintf("--config=%s", filepath.Join(config.RegistryServerConfigFilePath, config.RegistryServerConfigFileName)),
	}

	// Add ConfigMap volume to deployment if not already present
	configVolumeName := RegistryServerConfigVolumeName
	if !hasVolume(deployment.Spec.Template.Spec.Volumes, configVolumeName) {
		configVolume := corev1.Volume{
			Name: configVolumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: configManager.GetRegistryServerConfigMapName(),
					},
				},
			},
		}
		deployment.Spec.Template.Spec.Volumes = append(deployment.Spec.Template.Spec.Volumes, configVolume)
	}

	// Add volume mount to the container if not already present
	if !hasVolumeMount(container.VolumeMounts, configVolumeName) {
		volumeMount := corev1.VolumeMount{
			Name:      configVolumeName,
			MountPath: config.RegistryServerConfigFilePath, // Mount to directory, not the file path
			ReadOnly:  true,                                // ConfigMaps are always read-only anyway
		}
		container.VolumeMounts = append(container.VolumeMounts, volumeMount)
	}

	return nil
}

func (*manager) configureRegistryStorageMounts(
	deployment *appsv1.Deployment,
	containerName string,
) error {
	// Find the container by name
	container := findContainerByName(deployment.Spec.Template.Spec.Containers, containerName)
	if container == nil {
		return fmt.Errorf("container '%s' not found in deployment", containerName)
	}

	// Add emptyDir volume for storage data if not already present
	storageDataVolumeName := "storage-data"
	if !hasVolume(deployment.Spec.Template.Spec.Volumes, storageDataVolumeName) {
		storageDataVolume := corev1.Volume{
			Name: storageDataVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		}
		deployment.Spec.Template.Spec.Volumes = append(deployment.Spec.Template.Spec.Volumes, storageDataVolume)
	}

	// Add emptyDir mount to the container if not already present
	if !hasVolumeMount(container.VolumeMounts, storageDataVolumeName) {
		storageDataVolumeMount := corev1.VolumeMount{
			Name:      storageDataVolumeName,
			MountPath: "/data", // You can modify this path as needed
			ReadOnly:  false,
		}
		container.VolumeMounts = append(container.VolumeMounts, storageDataVolumeMount)
	}
	return nil
}

func (*manager) configureRegistrySourceMounts(
	deployment *appsv1.Deployment,
	mcpRegistry *mcpv1alpha1.MCPRegistry,
	containerName string,
) error {

	// Find the container by name
	container := findContainerByName(deployment.Spec.Template.Spec.Containers, containerName)
	if container == nil {
		return fmt.Errorf("container '%s' not found in deployment", containerName)
	}

	// Iterate through all registry configurations to handle multiple ConfigMap sources
	for i, registry := range mcpRegistry.Spec.Registries {
		if registry.ConfigMapRef != nil {
			// Create unique volume name for each ConfigMap source
			volumeName := fmt.Sprintf("registry-data-%d-%s", i, registry.Name)

			// Add volume if it doesn't exist
			if !hasVolume(deployment.Spec.Template.Spec.Volumes, volumeName) {
				volume := corev1.Volume{
					Name: volumeName,
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: registry.ConfigMapRef.Name,
							},
							// Mount only the specified key as registry.json
							Items: []corev1.KeyToPath{
								{
									Key:  registry.ConfigMapRef.Key,
									Path: "registry.json",
								},
							},
						},
					},
				}
				deployment.Spec.Template.Spec.Volumes = append(deployment.Spec.Template.Spec.Volumes, volume)
			}

			// Add volume mount if it doesn't exist
			if !hasVolumeMount(container.VolumeMounts, volumeName) {
				// Mount path follows the pattern /config/registry/{registryName}/
				// This matches what buildFilePath expects in config.go
				mountPath := filepath.Join(config.RegistryJSONFilePath, registry.Name)
				volumeMount := corev1.VolumeMount{
					Name:      volumeName,
					MountPath: mountPath,
					ReadOnly:  true, // ConfigMaps are always read-only
				}
				container.VolumeMounts = append(container.VolumeMounts, volumeMount)
			}
		}
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

func (*manager) ensureRegistryServerConfigConfigMap(
	ctx context.Context,
	mcpRegistry *mcpv1alpha1.MCPRegistry,
	configManager config.ConfigManager,
) error {
	cfg, err := configManager.BuildConfig()
	if err != nil {
		return fmt.Errorf("failed to build registry server config configuration: %w", err)
	}

	configMap, err := cfg.ToConfigMapWithContentChecksum(mcpRegistry)
	if err != nil {
		return fmt.Errorf("failed to create config map: %w", err)
	}

	err = configManager.UpsertConfigMap(ctx, mcpRegistry, configMap)
	if err != nil {
		return fmt.Errorf("failed to upsert registry server config config map: %w", err)
	}
	return nil
}

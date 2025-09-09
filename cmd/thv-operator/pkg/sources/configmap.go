package sources

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/registry"
)

// ConfigMapSourceHandler handles registry data from Kubernetes ConfigMaps
type ConfigMapSourceHandler struct {
	client    client.Client
	converter FormatConverter
	validator SourceDataValidator
}

// NewConfigMapSourceHandler creates a new ConfigMap source handler
func NewConfigMapSourceHandler(k8sClient client.Client) *ConfigMapSourceHandler {
	return &ConfigMapSourceHandler{
		client:    k8sClient,
		converter: NewRegistryFormatConverter(),
		validator: NewSourceDataValidator(),
	}
}

// Validate validates the ConfigMap source configuration
func (*ConfigMapSourceHandler) Validate(source *mcpv1alpha1.MCPRegistrySource) error {
	if source.Type != mcpv1alpha1.RegistrySourceTypeConfigMap {
		return fmt.Errorf("invalid source type: expected %s, got %s",
			mcpv1alpha1.RegistrySourceTypeConfigMap, source.Type)
	}

	if source.ConfigMap == nil {
		return fmt.Errorf("configMap configuration is required for source type %s",
			mcpv1alpha1.RegistrySourceTypeConfigMap)
	}

	if source.ConfigMap.Name == "" {
		return fmt.Errorf("configMap name cannot be empty")
	}

	// Key defaults to "registry.json" if not specified (handled by kubebuilder defaults)
	if source.ConfigMap.Key == "" {
		source.ConfigMap.Key = "registry.json"
	}

	return nil
}

// Sync retrieves registry data from the ConfigMap source
func (h *ConfigMapSourceHandler) Sync(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (*SyncResult, error) {
	source := &mcpRegistry.Spec.Source

	// Validate source configuration
	if err := h.Validate(source); err != nil {
		return nil, fmt.Errorf("source validation failed: %w", err)
	}

	// Determine ConfigMap namespace (use registry namespace since ConfigMapSource doesn't have namespace field)
	configMapNamespace := mcpRegistry.Namespace

	// Retrieve ConfigMap
	configMap := &corev1.ConfigMap{}
	configMapKey := types.NamespacedName{
		Name:      source.ConfigMap.Name,
		Namespace: configMapNamespace,
	}

	if err := h.client.Get(ctx, configMapKey, configMap); err != nil {
		return nil, fmt.Errorf("failed to get ConfigMap %s/%s: %w",
			configMapNamespace, source.ConfigMap.Name, err)
	}

	// Get registry data from ConfigMap
	key := source.ConfigMap.Key
	if key == "" {
		key = "registry.json"
	}

	data, exists := configMap.Data[key]
	if !exists {
		return nil, fmt.Errorf("key %s not found in ConfigMap %s/%s",
			key, configMapNamespace, source.ConfigMap.Name)
	}

	if source.Format == mcpv1alpha1.RegistryFormatUpstream {
		return nil, fmt.Errorf("upstream registry format is not yet supported")
	}

	// Convert string data to bytes
	registryData := []byte(data)

	// Validate registry data format
	if err := h.validator.ValidateData(registryData, source.Format); err != nil {
		return nil, fmt.Errorf("registry data validation failed: %w", err)
	}

	// Parse and count servers based on format
	serverCount, err := h.countServers(registryData, source.Format)
	if err != nil {
		return nil, fmt.Errorf("failed to parse registry data: %w", err)
	}

	// Create and return sync result
	return NewSyncResult(registryData, serverCount), nil
}

// countServers counts the number of servers in the registry data
func (h *ConfigMapSourceHandler) countServers(data []byte, format string) (int, error) {
	switch format {
	case mcpv1alpha1.RegistryFormatToolHive:
		return h.countToolHiveServers(data)
	case mcpv1alpha1.RegistryFormatUpstream:
		return 0, fmt.Errorf("upstream registry format is not yet supported")
	default:
		return h.countToolHiveServers(data) // Default to ToolHive format
	}
}

// countToolHiveServers counts servers in ToolHive format
func (*ConfigMapSourceHandler) countToolHiveServers(data []byte) (int, error) {
	var mcpRegistry registry.Registry

	if err := json.Unmarshal(data, &mcpRegistry); err != nil {
		return 0, fmt.Errorf("failed to parse ToolHive registry format: %w", err)
	}

	// Count both container servers and remote servers
	return len(mcpRegistry.Servers) + len(mcpRegistry.RemoteServers), nil
}

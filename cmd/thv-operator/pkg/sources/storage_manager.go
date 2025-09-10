package sources

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/registry"
)

const (
	// ConfigMapStorageDataKey is the key used to store registry data in ConfigMaps by the storage manager
	ConfigMapStorageDataKey = "registry.json"
)

// StorageManager defines the interface for registry data persistence
type StorageManager interface {
	// Store saves a Registry instance to persistent storage
	Store(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry, reg *registry.Registry) error

	// Get retrieves and parses registry data from persistent storage
	Get(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (*registry.Registry, error)

	// Delete removes registry data from persistent storage
	Delete(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) error

	// GetStorageReference returns a reference to where the data is stored
	GetStorageReference(mcpRegistry *mcpv1alpha1.MCPRegistry) *mcpv1alpha1.StorageReference

	// Legacy methods for backward compatibility (can be deprecated later)
	StoreRaw(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry, data []byte) error
	GetRaw(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) ([]byte, error)
}

// ConfigMapStorageManager implements StorageManager using Kubernetes ConfigMaps
type ConfigMapStorageManager struct {
	client client.Client
	scheme *runtime.Scheme
}

// NewConfigMapStorageManager creates a new ConfigMap-based storage manager
func NewConfigMapStorageManager(k8sClient client.Client, scheme *runtime.Scheme) StorageManager {
	return &ConfigMapStorageManager{
		client: k8sClient,
		scheme: scheme,
	}
}

// Store saves a Registry instance to a ConfigMap
func (s *ConfigMapStorageManager) Store(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry, reg *registry.Registry) error {
	// Serialize the registry to JSON
	data, err := json.Marshal(reg)
	if err != nil {
		return NewStorageError("serialize", mcpRegistry.Name, "failed to marshal registry", err)
	}

	return s.StoreRaw(ctx, mcpRegistry, data)
}

// StoreRaw saves raw registry data to a ConfigMap (legacy method)
func (s *ConfigMapStorageManager) StoreRaw(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry, data []byte) error {
	configMapName := s.getConfigMapName(mcpRegistry)

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: mcpRegistry.Namespace,
			Annotations: map[string]string{
				"toolhive.stacklok.dev/registry-name":   mcpRegistry.Name,
				"toolhive.stacklok.dev/registry-format": string(mcpRegistry.Spec.Source.Format),
			},
			Labels: map[string]string{
				"app.kubernetes.io/name":         "toolhive-operator",
				"app.kubernetes.io/component":    "registry-storage",
				"app.kubernetes.io/managed-by":   "toolhive-operator",
				"toolhive.stacklok.dev/registry": mcpRegistry.Name,
			},
		},
		Data: map[string]string{
			ConfigMapStorageDataKey: string(data),
		},
	}

	// Set owner reference for automatic cleanup
	if err := controllerutil.SetControllerReference(mcpRegistry, configMap, s.scheme); err != nil {
		return NewStorageError("set_owner_reference", mcpRegistry.Name, "failed to set controller reference", err)
	}

	// Create or update the ConfigMap
	existing := &corev1.ConfigMap{}
	err := s.client.Get(ctx, types.NamespacedName{
		Name:      configMapName,
		Namespace: mcpRegistry.Namespace,
	}, existing)

	if err != nil {
		// ConfigMap doesn't exist, create it
		if err := s.client.Create(ctx, configMap); err != nil {
			return NewStorageError("create", mcpRegistry.Name, "failed to create storage ConfigMap", err)
		}
	} else {
		// ConfigMap exists, update it
		existing.Data = configMap.Data
		existing.Annotations = configMap.Annotations
		existing.Labels = configMap.Labels

		// Ensure owner reference is set on existing ConfigMap too
		if err := controllerutil.SetControllerReference(mcpRegistry, existing, s.scheme); err != nil {
			return NewStorageError("set_owner_reference", mcpRegistry.Name,
				"failed to set controller reference on existing ConfigMap", err)
		}

		if err := s.client.Update(ctx, existing); err != nil {
			return NewStorageError("update", mcpRegistry.Name, "failed to update storage ConfigMap", err)
		}
	}

	return nil
}

// Get retrieves and parses registry data from a ConfigMap
func (s *ConfigMapStorageManager) Get(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) (*registry.Registry, error) {
	data, err := s.GetRaw(ctx, mcpRegistry)
	if err != nil {
		return nil, err
	}

	// Parse the JSON data into a Registry
	var reg registry.Registry
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil, NewStorageError("parse", mcpRegistry.Name, "failed to parse registry data", err)
	}

	return &reg, nil
}

// GetRaw retrieves raw registry data from a ConfigMap (legacy method)
func (s *ConfigMapStorageManager) GetRaw(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) ([]byte, error) {
	configMapName := s.getConfigMapName(mcpRegistry)

	configMap := &corev1.ConfigMap{}
	err := s.client.Get(ctx, types.NamespacedName{
		Name:      configMapName,
		Namespace: mcpRegistry.Namespace,
	}, configMap)

	if err != nil {
		return nil, NewStorageError("get", mcpRegistry.Name, "failed to get storage ConfigMap", err)
	}

	data, exists := configMap.Data[ConfigMapStorageDataKey]
	if !exists {
		return nil, NewStorageError("get", mcpRegistry.Name, "registry data not found in ConfigMap", nil)
	}

	return []byte(data), nil
}

// Delete removes the storage ConfigMap
func (s *ConfigMapStorageManager) Delete(ctx context.Context, mcpRegistry *mcpv1alpha1.MCPRegistry) error {
	configMapName := s.getConfigMapName(mcpRegistry)

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: mcpRegistry.Namespace,
		},
	}

	if err := s.client.Delete(ctx, configMap); err != nil {
		return NewStorageError("delete", mcpRegistry.Name, "failed to delete storage ConfigMap", err)
	}

	return nil
}

// GetStorageReference returns a reference to the ConfigMap storage
func (s *ConfigMapStorageManager) GetStorageReference(mcpRegistry *mcpv1alpha1.MCPRegistry) *mcpv1alpha1.StorageReference {
	return &mcpv1alpha1.StorageReference{
		Type: "configmap",
		ConfigMapRef: &corev1.LocalObjectReference{
			Name: s.getConfigMapName(mcpRegistry),
		},
	}
}

// getConfigMapName generates the ConfigMap name for registry storage
func (*ConfigMapStorageManager) getConfigMapName(mcpRegistry *mcpv1alpha1.MCPRegistry) string {
	return fmt.Sprintf("%s-registry-storage", mcpRegistry.Name)
}

// StorageError represents an error that occurred during storage operations
type StorageError struct {
	Operation    string
	RegistryName string
	Reason       string
	Err          error
}

// NewStorageError creates a new storage error
func NewStorageError(operation, registryName, reason string, err error) *StorageError {
	return &StorageError{
		Operation:    operation,
		RegistryName: registryName,
		Reason:       reason,
		Err:          err,
	}
}

// Error implements the error interface
func (e *StorageError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("storage operation '%s' failed for registry '%s': %s: %v",
			e.Operation, e.RegistryName, e.Reason, e.Err)
	}
	return fmt.Sprintf("storage operation '%s' failed for registry '%s': %s",
		e.Operation, e.RegistryName, e.Reason)
}

// Unwrap returns the wrapped error
func (e *StorageError) Unwrap() error {
	return e.Err
}

// Is checks if the error matches the target error type
func (e *StorageError) Is(target error) bool {
	if se, ok := target.(*StorageError); ok {
		return e.Operation == se.Operation && e.RegistryName == se.RegistryName
	}
	return false
}

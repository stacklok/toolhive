package sources

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
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
	// RegistryStorageComponent is the component label for the registry storage
	RegistryStorageComponent = "registry-storage"

	// StorageTypeConfigMap identifies the ConfigMap storage manager implementation
	StorageTypeConfigMap = "configmap"
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

	// GetType returns the storage manager type as a string
	GetType() string
}

// ConfigMapStorageManager implements StorageManager using Kubernetes ConfigMaps
type ConfigMapStorageManager struct {
	client client.Client
	scheme *runtime.Scheme
}

// NewConfigMapStorageManager creates a new ConfigMap-based storage manager
func NewConfigMapStorageManager(k8sClient client.Client, scheme *runtime.Scheme) *ConfigMapStorageManager {
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
				"app.kubernetes.io/component":    RegistryStorageComponent,
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
	err = s.client.Get(ctx, types.NamespacedName{
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

	// Parse the JSON data into a Registry
	var reg registry.Registry
	if err := json.Unmarshal([]byte(data), &reg); err != nil {
		return nil, NewStorageError("parse", mcpRegistry.Name, "failed to parse registry data", err)
	}

	return &reg, nil
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
		// Ignore "not found" errors - delete should be idempotent
		if errors.IsNotFound(err) {
			return nil
		}
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

// GetType returns the storage manager type
func (*ConfigMapStorageManager) GetType() string {
	return StorageTypeConfigMap
}

// getConfigMapName generates the ConfigMap name for registry storage
func (*ConfigMapStorageManager) getConfigMapName(mcpRegistry *mcpv1alpha1.MCPRegistry) string {
	return fmt.Sprintf("%s-registry-storage", mcpRegistry.Name)
}

// StorageError represents an error that occurred during storage operations
type StorageError struct {
	Operation string
	Registry  string
	Message   string
	Cause     error
}

func (e *StorageError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("storage error [%s] for registry %s: %s: %v", e.Operation, e.Registry, e.Message, e.Cause)
	}
	return fmt.Sprintf("storage error [%s] for registry %s: %s", e.Operation, e.Registry, e.Message)
}

func (e *StorageError) Unwrap() error {
	return e.Cause
}

// NewStorageError creates a new StorageError
func NewStorageError(operation, mcpRegistry, message string, cause error) *StorageError {
	return &StorageError{
		Operation: operation,
		Registry:  mcpRegistry,
		Message:   message,
		Cause:     cause,
	}
}

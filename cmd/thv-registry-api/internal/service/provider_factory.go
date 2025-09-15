// Package service provides the business logic for the MCP registry API
package service

import (
	"fmt"

	"k8s.io/client-go/kubernetes"
)

// RegistryProviderType represents the type of registry data provider
type RegistryProviderType string

const (
	// RegistryProviderTypeConfigMap represents a ConfigMap-based registry provider
	RegistryProviderTypeConfigMap RegistryProviderType = "configmap"
	// RegistryProviderTypeFile represents a file-based registry provider
	RegistryProviderTypeFile RegistryProviderType = "file"
)

// RegistryProviderConfig holds configuration for creating a registry data provider
type RegistryProviderConfig struct {
	Type      RegistryProviderType
	ConfigMap *ConfigMapProviderConfig
	File      *FileProviderConfig
}

// ConfigMapProviderConfig holds configuration for ConfigMap-based registry provider
type ConfigMapProviderConfig struct {
	Name      string
	Namespace string
	Clientset kubernetes.Interface
}

// FileProviderConfig holds configuration for file-based registry provider
type FileProviderConfig struct {
	FilePath string
}

// Validate validates the RegistryProviderConfig
func (c *RegistryProviderConfig) Validate() error {
	if c.Type == "" {
		return fmt.Errorf("provider type is required")
	}

	switch c.Type {
	case RegistryProviderTypeConfigMap:
		if c.ConfigMap == nil {
			return fmt.Errorf("configmap configuration required for configmap provider")
		}
		return c.ConfigMap.Validate()
	case RegistryProviderTypeFile:
		if c.File == nil {
			return fmt.Errorf("file configuration required for file provider")
		}
		return c.File.Validate()
	default:
		return fmt.Errorf("unsupported provider type: %s", c.Type)
	}
}

// Validate validates the ConfigMapProviderConfig
func (c *ConfigMapProviderConfig) Validate() error {
	if c.Clientset == nil {
		return fmt.Errorf("kubernetes clientset is required")
	}
	if c.Name == "" {
		return fmt.Errorf("configmap name is required")
	}
	if c.Namespace == "" {
		return fmt.Errorf("configmap namespace is required")
	}
	return nil
}

// Validate validates the FileProviderConfig
func (c *FileProviderConfig) Validate() error {
	if c.FilePath == "" {
		return fmt.Errorf("file path is required")
	}
	return nil
}

//go:generate mockgen -destination=mocks/mock_provider_factory.go -package=mocks -source=provider_factory.go RegistryProviderFactory

// RegistryProviderFactory creates registry data providers based on configuration
type RegistryProviderFactory interface {
	// CreateProvider creates a registry data provider based on the provided configuration
	CreateProvider(config *RegistryProviderConfig) (RegistryDataProvider, error)
}

// DefaultRegistryProviderFactory is the default implementation of RegistryProviderFactory
type DefaultRegistryProviderFactory struct{}

// NewRegistryProviderFactory creates a new default registry provider factory
func NewRegistryProviderFactory() RegistryProviderFactory {
	return &DefaultRegistryProviderFactory{}
}

// CreateProvider implements RegistryProviderFactory.CreateProvider
func (f *DefaultRegistryProviderFactory) CreateProvider(config *RegistryProviderConfig) (RegistryDataProvider, error) {
	if config == nil {
		return nil, fmt.Errorf("registry provider config cannot be nil")
	}

	switch config.Type {
	case RegistryProviderTypeConfigMap:
		if config.ConfigMap == nil {
			return nil, fmt.Errorf("configmap configuration required for configmap provider type")
		}
		return f.createConfigMapProvider(config.ConfigMap)

	case RegistryProviderTypeFile:
		if config.File == nil {
			return nil, fmt.Errorf("file configuration required for file provider type")
		}
		return f.createFileProvider(config.File)

	default:
		return nil, fmt.Errorf("unsupported registry provider type: %s", config.Type)
	}
}

// createConfigMapProvider creates a ConfigMap-based registry data provider
func (*DefaultRegistryProviderFactory) createConfigMapProvider(config *ConfigMapProviderConfig) (RegistryDataProvider, error) {
	if config.Clientset == nil {
		return nil, fmt.Errorf("kubernetes clientset is required for configmap provider")
	}
	if config.Name == "" {
		return nil, fmt.Errorf("configmap name is required")
	}
	if config.Namespace == "" {
		return nil, fmt.Errorf("configmap namespace is required")
	}

	return NewK8sRegistryDataProvider(config.Clientset, config.Name, config.Namespace), nil
}

// createFileProvider creates a file-based registry data provider
func (*DefaultRegistryProviderFactory) createFileProvider(config *FileProviderConfig) (RegistryDataProvider, error) {
	if config.FilePath == "" {
		return nil, fmt.Errorf("file path is required for file provider")
	}

	return NewFileRegistryDataProvider(config.FilePath), nil
}

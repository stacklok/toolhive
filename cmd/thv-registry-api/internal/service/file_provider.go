// Package service provides the business logic for the MCP registry API
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/stacklok/toolhive/pkg/registry"
)

// FileRegistryDataProvider implements RegistryDataProvider using local file system.
// This implementation reads registry data from a mounted file instead of calling the Kubernetes API.
// It is designed to work with ConfigMaps mounted as volumes in Kubernetes deployments.
type FileRegistryDataProvider struct {
	filePath     string
	registryName string
}

// NewFileRegistryDataProvider creates a new file-based registry data provider.
// The filePath parameter should point to the registry.json file, typically mounted from a ConfigMap.
// The registryName parameter specifies the registry identifier for business logic purposes.
func NewFileRegistryDataProvider(filePath, registryName string) *FileRegistryDataProvider {
	return &FileRegistryDataProvider{
		filePath:     filePath,
		registryName: registryName,
	}
}

// GetRegistryData implements RegistryDataProvider.GetRegistryData.
// It reads the registry.json file from the local filesystem and parses it into a Registry struct.
func (p *FileRegistryDataProvider) GetRegistryData(_ context.Context) (*registry.Registry, error) {
	if p.filePath == "" {
		return nil, fmt.Errorf("file path not configured")
	}

	// Check if the file exists and is readable
	if _, err := os.Stat(p.filePath); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("registry file not found at %s: %w", p.filePath, err)
		}
		return nil, fmt.Errorf("cannot access registry file at %s: %w", p.filePath, err)
	}

	// Read the file contents
	data, err := os.ReadFile(p.filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read registry file at %s: %w", p.filePath, err)
	}

	// Parse the JSON data
	var reg registry.Registry
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("failed to parse registry data from file %s: %w", p.filePath, err)
	}

	return &reg, nil
}

// GetSource implements RegistryDataProvider.GetSource.
// It returns a descriptive string indicating the file source.
func (p *FileRegistryDataProvider) GetSource() string {
	if p.filePath == "" {
		return "file:<not-configured>"
	}

	// Clean the path for consistent display
	cleanPath := filepath.Clean(p.filePath)
	return fmt.Sprintf("file:%s", cleanPath)
}

// GetRegistryName implements RegistryDataProvider.GetRegistryName.
// It returns the injected registry name identifier.
func (p *FileRegistryDataProvider) GetRegistryName() string {
	return p.registryName
}

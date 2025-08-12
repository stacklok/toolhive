package registry

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
)

//go:embed data/registry.json
var embeddedRegistryFS embed.FS

// LocalRegistryProvider provides registry data from embedded JSON files or local files
type LocalRegistryProvider struct {
	*BaseProvider
	filePath string
}

// NewLocalRegistryProvider creates a new local registry provider
// If filePath is provided, it will read from that file; otherwise uses embedded data
func NewLocalRegistryProvider(filePath ...string) *LocalRegistryProvider {
	var path string
	if len(filePath) > 0 {
		path = filePath[0]
	}

	p := &LocalRegistryProvider{
		filePath: path,
	}

	// Initialize the base provider with the GetRegistry function
	p.BaseProvider = NewBaseProvider(p.GetRegistry)

	return p
}

// GetRegistry returns the registry data from file path or embedded data
func (p *LocalRegistryProvider) GetRegistry() (*Registry, error) {
	var data []byte
	var err error

	if p.filePath != "" {
		// Read from local file
		data, err = os.ReadFile(p.filePath)
		if err != nil {
			return nil, fmt.Errorf("failed to read local registry file %s: %w", p.filePath, err)
		}
	} else {
		// Read from embedded data
		data, err = embeddedRegistryFS.ReadFile("data/registry.json")
		if err != nil {
			return nil, fmt.Errorf("failed to read embedded registry data: %w", err)
		}
	}

	registry, err := parseRegistryData(data)
	if err != nil {
		return nil, err
	}

	// Set name field on each server based on map key
	for name, server := range registry.Servers {
		server.Name = name
	}
	// Set name field on each remote server based on map key
	for name, server := range registry.RemoteServers {
		server.Name = name
	}

	return registry, nil
}

// parseRegistryData parses JSON data into a Registry struct
func parseRegistryData(data []byte) (*Registry, error) {
	registry := &Registry{}
	if err := json.Unmarshal(data, registry); err != nil {
		return nil, fmt.Errorf("failed to parse registry data: %w", err)
	}
	return registry, nil
}

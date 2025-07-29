package registry

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

//go:embed data/registry.json
var embeddedRegistryFS embed.FS

// EmbeddedRegistryProvider provides registry data from embedded JSON files or local files
type EmbeddedRegistryProvider struct {
	filePath string
}

// NewEmbeddedRegistryProvider creates a new embedded registry provider
// If filePath is provided, it will read from that file; otherwise uses embedded data
func NewEmbeddedRegistryProvider(filePath ...string) *EmbeddedRegistryProvider {
	var path string
	if len(filePath) > 0 {
		path = filePath[0]
	}
	return &EmbeddedRegistryProvider{filePath: path}
}

// GetRegistry returns the registry data from file path or embedded data
func (p *EmbeddedRegistryProvider) GetRegistry() (*Registry, error) {
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

	return registry, nil
}

// GetServer returns a specific server by name
func (p *EmbeddedRegistryProvider) GetServer(name string) (*ImageMetadata, error) {
	reg, err := p.GetRegistry()
	if err != nil {
		return nil, err
	}

	server, ok := reg.Servers[name]
	if !ok {
		return nil, fmt.Errorf("server not found: %s", name)
	}

	return server, nil
}

// SearchServers searches for servers matching the query
func (p *EmbeddedRegistryProvider) SearchServers(query string) ([]*ImageMetadata, error) {
	reg, err := p.GetRegistry()
	if err != nil {
		return nil, err
	}

	query = strings.ToLower(query)
	var results []*ImageMetadata

	for name, server := range reg.Servers {
		// Search in name
		if strings.Contains(strings.ToLower(name), query) {
			results = append(results, server)
			continue
		}

		// Search in description
		if strings.Contains(strings.ToLower(server.Description), query) {
			results = append(results, server)
			continue
		}

		// Search in tags
		for _, tag := range server.Tags {
			if strings.Contains(strings.ToLower(tag), query) {
				results = append(results, server)
				break
			}
		}
	}

	return results, nil
}

// ListServers returns all available servers
func (p *EmbeddedRegistryProvider) ListServers() ([]*ImageMetadata, error) {
	reg, err := p.GetRegistry()
	if err != nil {
		return nil, err
	}

	servers := make([]*ImageMetadata, 0, len(reg.Servers))
	for _, server := range reg.Servers {
		servers = append(servers, server)
	}

	return servers, nil
}

// parseRegistryData parses JSON data into a Registry struct
func parseRegistryData(data []byte) (*Registry, error) {
	registry := &Registry{}
	if err := json.Unmarshal(data, registry); err != nil {
		return nil, fmt.Errorf("failed to parse registry data: %w", err)
	}
	return registry, nil
}

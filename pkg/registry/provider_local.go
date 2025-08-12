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

// LocalRegistryProvider provides registry data from embedded JSON files or local files
type LocalRegistryProvider struct {
	filePath string
}

// NewLocalRegistryProvider creates a new local registry provider
// If filePath is provided, it will read from that file; otherwise uses embedded data
func NewLocalRegistryProvider(filePath ...string) *LocalRegistryProvider {
	var path string
	if len(filePath) > 0 {
		path = filePath[0]
	}
	return &LocalRegistryProvider{filePath: path}
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

// GetServer returns a specific server by name (container or remote)
func (p *LocalRegistryProvider) GetServer(name string) (ServerMetadata, error) {
	reg, err := p.GetRegistry()
	if err != nil {
		return nil, err
	}

	// Use the registry's helper method
	server, found := reg.GetServerByName(name)
	if !found {
		return nil, fmt.Errorf("server not found: %s", name)
	}

	return server, nil
}

// SearchServers searches for servers matching the query (both container and remote)
func (p *LocalRegistryProvider) SearchServers(query string) ([]ServerMetadata, error) {
	reg, err := p.GetRegistry()
	if err != nil {
		return nil, err
	}

	query = strings.ToLower(query)
	var results []ServerMetadata

	// Search container servers
	for name, server := range reg.Servers {
		if matchesQuery(name, server.Description, server.Tags, query) {
			results = append(results, server)
		}
	}

	// Search remote servers
	for name, server := range reg.RemoteServers {
		if matchesQuery(name, server.Description, server.Tags, query) {
			results = append(results, server)
		}
	}

	return results, nil
}

// ListServers returns all available servers (both container and remote)
func (p *LocalRegistryProvider) ListServers() ([]ServerMetadata, error) {
	reg, err := p.GetRegistry()
	if err != nil {
		return nil, err
	}

	// Use the registry's helper method
	return reg.GetAllServers(), nil
}

// Legacy methods for backward compatibility

// GetImageServer returns a specific container server by name
func (p *LocalRegistryProvider) GetImageServer(name string) (*ImageMetadata, error) {
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

// SearchImageServers searches for container servers matching the query
func (p *LocalRegistryProvider) SearchImageServers(query string) ([]*ImageMetadata, error) {
	reg, err := p.GetRegistry()
	if err != nil {
		return nil, err
	}

	query = strings.ToLower(query)
	var results []*ImageMetadata

	for name, server := range reg.Servers {
		if matchesQuery(name, server.Description, server.Tags, query) {
			results = append(results, server)
		}
	}

	return results, nil
}

// ListImageServers returns all available container servers
func (p *LocalRegistryProvider) ListImageServers() ([]*ImageMetadata, error) {
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

// matchesQuery checks if a server matches the search query
func matchesQuery(name, description string, tags []string, query string) bool {
	// Search in name
	if strings.Contains(strings.ToLower(name), query) {
		return true
	}

	// Search in description
	if strings.Contains(strings.ToLower(description), query) {
		return true
	}

	// Search in tags
	for _, tag := range tags {
		if strings.Contains(strings.ToLower(tag), query) {
			return true
		}
	}

	return false
}

// parseRegistryData parses JSON data into a Registry struct
func parseRegistryData(data []byte) (*Registry, error) {
	registry := &Registry{}
	if err := json.Unmarshal(data, registry); err != nil {
		return nil, fmt.Errorf("failed to parse registry data: %w", err)
	}
	return registry, nil
}

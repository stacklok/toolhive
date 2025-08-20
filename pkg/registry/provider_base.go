package registry

import (
	"fmt"
	"strings"
)

// BaseProvider provides common implementation for registry providers
type BaseProvider struct {
	// GetRegistryFunc is a function that fetches the registry data
	// This allows different providers to implement their own data fetching logic
	GetRegistryFunc func() (*Registry, error)
}

// NewBaseProvider creates a new base provider with the given registry function
func NewBaseProvider(getRegistry func() (*Registry, error)) *BaseProvider {
	return &BaseProvider{
		GetRegistryFunc: getRegistry,
	}
}

// GetServer returns a specific server by name (container or remote)
func (p *BaseProvider) GetServer(name string) (ServerMetadata, error) {
	reg, err := p.GetRegistryFunc()
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
func (p *BaseProvider) SearchServers(query string) ([]ServerMetadata, error) {
	reg, err := p.GetRegistryFunc()
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

// ListServers returns all servers (both container and remote)
func (p *BaseProvider) ListServers() ([]ServerMetadata, error) {
	reg, err := p.GetRegistryFunc()
	if err != nil {
		return nil, err
	}

	// Use the registry's helper method
	return reg.GetAllServers(), nil
}

// Legacy methods for backward compatibility

// GetImageServer returns a specific container server by name (legacy method)
func (p *BaseProvider) GetImageServer(name string) (*ImageMetadata, error) {
	server, err := p.GetServer(name)
	if err != nil {
		return nil, err
	}

	// Type assert to ImageMetadata
	if img, ok := server.(*ImageMetadata); ok {
		return img, nil
	}

	return nil, fmt.Errorf("server %s is not a container server", name)
}

// SearchImageServers searches for container servers matching the query (legacy method)
func (p *BaseProvider) SearchImageServers(query string) ([]*ImageMetadata, error) {
	servers, err := p.SearchServers(query)
	if err != nil {
		return nil, err
	}

	// Filter to only container servers
	var results []*ImageMetadata
	for _, server := range servers {
		if img, ok := server.(*ImageMetadata); ok {
			results = append(results, img)
		}
	}

	return results, nil
}

// ListImageServers returns all container servers (legacy method)
func (p *BaseProvider) ListImageServers() ([]*ImageMetadata, error) {
	servers, err := p.ListServers()
	if err != nil {
		return nil, err
	}

	// Filter to only container servers
	var results []*ImageMetadata
	for _, server := range servers {
		if img, ok := server.(*ImageMetadata); ok {
			results = append(results, img)
		}
	}

	return results, nil
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

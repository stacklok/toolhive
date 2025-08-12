package registry

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/stacklok/toolhive/pkg/networking"
)

// RemoteRegistryProvider provides registry data from a remote HTTP endpoint
type RemoteRegistryProvider struct {
	registryURL    string
	allowPrivateIp bool
}

// NewRemoteRegistryProvider creates a new remote registry provider
func NewRemoteRegistryProvider(registryURL string, allowPrivateIp bool) *RemoteRegistryProvider {
	return &RemoteRegistryProvider{
		registryURL:    registryURL,
		allowPrivateIp: allowPrivateIp,
	}
}

// GetRegistry returns the remote registry data
func (p *RemoteRegistryProvider) GetRegistry() (*Registry, error) {
	client, err := networking.NewHttpClientBuilder().
		WithPrivateIPs(p.allowPrivateIp).
		Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build http client: %w", err)
	}

	resp, err := client.Get(p.registryURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch registry data from URL %s: %w", p.registryURL, err)
	}
	defer resp.Body.Close()

	// Check if the response status code is OK
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("response status code from URL %s not OK: status code %d", p.registryURL, resp.StatusCode)
	}

	// Read the response body
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read registry data from response body: %w", err)
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
func (p *RemoteRegistryProvider) GetServer(name string) (ServerMetadata, error) {
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
func (p *RemoteRegistryProvider) SearchServers(query string) ([]ServerMetadata, error) {
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
func (p *RemoteRegistryProvider) ListServers() ([]ServerMetadata, error) {
	reg, err := p.GetRegistry()
	if err != nil {
		return nil, err
	}

	// Use the registry's helper method
	return reg.GetAllServers(), nil
}

// Legacy methods for backward compatibility

// GetImageServer returns a specific container server by name
func (p *RemoteRegistryProvider) GetImageServer(name string) (*ImageMetadata, error) {
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
func (p *RemoteRegistryProvider) SearchImageServers(query string) ([]*ImageMetadata, error) {
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
func (p *RemoteRegistryProvider) ListImageServers() ([]*ImageMetadata, error) {
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

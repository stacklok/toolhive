package registry

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/stacklok/toolhive/pkg/networking"
)

// RemoteRegistryProvider provides registry data from a remote HTTP endpoint
type RemoteRegistryProvider struct {
	*BaseProvider
	registryURL    string
	allowPrivateIp bool
}

// NewRemoteRegistryProvider creates a new remote registry provider
func NewRemoteRegistryProvider(registryURL string, allowPrivateIp bool) *RemoteRegistryProvider {
	p := &RemoteRegistryProvider{
		registryURL:    registryURL,
		allowPrivateIp: allowPrivateIp,
	}

	// Initialize the base provider with the GetRegistry function
	p.BaseProvider = NewBaseProvider(p.GetRegistry)

	return p
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

	registry := &Registry{}
	if err := json.Unmarshal(data, registry); err != nil {
		return nil, fmt.Errorf("failed to parse registry data: %w", err)
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

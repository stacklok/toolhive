package registry

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/networking"
)

//go:embed data/registry.json
var registryFS embed.FS

var (
	registry     *Registry
	registryOnce sync.Once
	registryErr  error
)

// GetRegistry returns the MCP server registry
func GetRegistry() (*Registry, error) {
	registryOnce.Do(func() {

		// Load the config to check if a custom registry URL was provided
		cfg, err := config.LoadOrCreateConfig()
		if err != nil {
			registryErr = fmt.Errorf("failed to load config: %w", err)
			return
		}
		rawRegistryUrl := cfg.RegistryUrl

		// Check if the custom registry URL if different than the default value
		if len(rawRegistryUrl) > 0 {

			// Fetch registry data from the provided URL
			registry, registryErr = getRemoteRegistry(rawRegistryUrl)
		} else {
			// Load the embedded registry data
			registry, registryErr = getEmbeddedRegistry()
		}
		// Make sure we have a valid registry
		if registryErr != nil {
			return
		}

		// Set name field on each server based on map key
		for name, server := range registry.Servers {
			server.Name = name
		}
	})

	return registry, registryErr
}

func getRemoteRegistry(registryUrl string) (*Registry, error) {

	client := networking.GetProtectedHttpClient()
	resp, err := client.Get(registryUrl)
	if err != nil {
		registryErr = fmt.Errorf("failed to fetch registry data from URL %s: %w", registryUrl, err)
		return nil, registryErr
	}
	defer resp.Body.Close()

	// Check if the response status code is OK
	if resp.StatusCode != http.StatusOK {
		registryErr = fmt.Errorf("response status code from URL %s not OK: status code %d", registryUrl, resp.StatusCode)
		return nil, registryErr
	}

	// Read the response body
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		registryErr = fmt.Errorf("failed to read registry data from response body: %w", err)
		return nil, registryErr
	}

	registry, parseErr := parseRegistryData(data)
	if parseErr != nil {
		return nil, parseErr
	}

	return registry, nil
}

func getEmbeddedRegistry() (*Registry, error) {
	data, err := registryFS.ReadFile("data/registry.json")
	if err != nil {
		return nil, fmt.Errorf("failed to read embedded registry data: %w", err)
	}

	registry, parseErr := parseRegistryData(data)
	if parseErr != nil {
		return nil, parseErr
	}

	return registry, nil
}

func parseRegistryData(data []byte) (*Registry, error) {
	registry := &Registry{}
	if err := json.Unmarshal(data, registry); err != nil {
		return nil, fmt.Errorf("failed to parse registry data: %w", err)
	}
	return registry, nil
}

// GetServer returns a server from the registry by name
func GetServer(name string) (*Server, error) {
	reg, err := GetRegistry()
	if err != nil {
		return nil, err
	}

	server, ok := reg.Servers[name]
	if !ok {
		return nil, fmt.Errorf("server not found: %s", name)
	}

	return server, nil
}

// SearchServers searches for servers in the registry
// It searches in server names, descriptions, and tags
func SearchServers(query string) ([]*Server, error) {
	reg, err := GetRegistry()
	if err != nil {
		return nil, err
	}

	query = strings.ToLower(query)
	var results []*Server

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

// ListServers returns all servers in the registry
func ListServers() ([]*Server, error) {
	reg, err := GetRegistry()
	if err != nil {
		return nil, err
	}

	servers := make([]*Server, 0, len(reg.Servers))
	for _, server := range reg.Servers {
		servers = append(servers, server)
	}

	return servers, nil
}

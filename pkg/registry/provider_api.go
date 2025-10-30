package registry

import (
	"context"
	"fmt"
	"time"

	v0 "github.com/modelcontextprotocol/registry/pkg/api/v0"

	"github.com/stacklok/toolhive/pkg/registry/api"
)

// NOTE: Using converters from converters.go (same package) to avoid circular dependency.
// This is a TEMPORARY solution - converters are copied from toolhive-registry.
// TODO: Move types to toolhive-registry and import converters as a library.

// APIRegistryProvider provides registry data from an MCP Registry API endpoint
// It queries the API on-demand for each operation, ensuring fresh data.
type APIRegistryProvider struct {
	*BaseProvider
	apiURL         string
	allowPrivateIp bool
	client         api.Client
}

// NewAPIRegistryProvider creates a new API registry provider
func NewAPIRegistryProvider(apiURL string, allowPrivateIp bool) (*APIRegistryProvider, error) {
	// Create API client
	client, err := api.NewClient(apiURL, allowPrivateIp)
	if err != nil {
		return nil, fmt.Errorf("failed to create API client: %w", err)
	}

	p := &APIRegistryProvider{
		apiURL:         apiURL,
		allowPrivateIp: allowPrivateIp,
		client:         client,
	}

	// Initialize the base provider with the GetRegistry function
	p.BaseProvider = NewBaseProvider(p.GetRegistry)

	// Validate the endpoint
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := client.ValidateEndpoint(ctx); err != nil {
		return nil, fmt.Errorf("invalid MCP Registry API endpoint: %w", err)
	}

	return p, nil
}

// GetRegistry returns the registry data by fetching all servers from the API
// This method queries the API and converts all servers to ToolHive format.
// Note: This can be slow for large registries as it fetches everything.
func (p *APIRegistryProvider) GetRegistry() (*Registry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Fetch all servers from the API
	servers, err := p.client.ListServers(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list servers from API: %w", err)
	}

	// Convert servers to ToolHive format
	serverMetadata, err := ConvertServersToMetadata(servers)
	if err != nil {
		return nil, fmt.Errorf("failed to convert servers to ToolHive format: %w", err)
	}

	// Build Registry structure
	registry := &Registry{
		Version:       "1.0.0",
		LastUpdated:   time.Now().Format(time.RFC3339),
		Servers:       make(map[string]*ImageMetadata),
		RemoteServers: make(map[string]*RemoteServerMetadata),
		Groups:        []*Group{},
	}

	// Separate servers into container and remote
	for _, server := range serverMetadata {
		if server.IsRemote() {
			if remoteServer, ok := server.(*RemoteServerMetadata); ok {
				registry.RemoteServers[remoteServer.Name] = remoteServer
			}
		} else {
			if imageServer, ok := server.(*ImageMetadata); ok {
				registry.Servers[imageServer.Name] = imageServer
			}
		}
	}

	return registry, nil
}

// GetServer returns a specific server by name (queries API directly)
func (p *APIRegistryProvider) GetServer(name string) (ServerMetadata, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Try to find server by searching (since API uses reverse-DNS names)
	// First try direct lookup by assuming simple name
	servers, err := p.client.SearchServers(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("failed to search for server %s: %w", name, err)
	}

	// Find exact match
	for _, server := range servers {
		// Extract simple name from reverse-DNS format
		simpleName := ExtractServerName(server.Name)
		if simpleName == name || server.Name == name {
			return ConvertServerJSON(server)
		}
	}

	return nil, fmt.Errorf("server %s not found in API", name)
}

// SearchServers searches for servers matching the query (queries API directly)
func (p *APIRegistryProvider) SearchServers(query string) ([]ServerMetadata, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Search via API
	servers, err := p.client.SearchServers(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to search servers: %w", err)
	}

	return ConvertServersToMetadata(servers)
}

// ListServers returns all servers from the API
func (p *APIRegistryProvider) ListServers() ([]ServerMetadata, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	servers, err := p.client.ListServers(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list servers: %w", err)
	}

	return ConvertServersToMetadata(servers)
}

// ConvertServerJSON converts an MCP Registry API ServerJSON to ToolHive ServerMetadata
// Uses converters from converters.go (same package)
// Note: Only handles OCI packages and remote servers, skips npm/pypi by design
func ConvertServerJSON(serverJSON *v0.ServerJSON) (ServerMetadata, error) {
	if serverJSON == nil {
		return nil, fmt.Errorf("serverJSON is nil")
	}

	// Determine if this is a remote server or container-based server
	// Remote servers have the 'remotes' field populated
	// Container servers have the 'packages' field populated
	if len(serverJSON.Remotes) > 0 {
		return ServerJSONToRemoteServerMetadata(serverJSON)
	}

	// Check if server has packages
	if len(serverJSON.Packages) == 0 {
		// Skip servers without packages or remotes (incomplete entries)
		return nil, fmt.Errorf("server %s has no packages or remotes, skipping", serverJSON.Name)
	}

	// ServerJSONToImageMetadata only handles OCI packages, will error on npm/pypi
	return ServerJSONToImageMetadata(serverJSON)
}

// ConvertServersToMetadata converts a slice of ServerJSON to a slice of ServerMetadata
// Skips servers that cannot be converted (e.g., incomplete entries)
// Uses official converters from toolhive-registry package
func ConvertServersToMetadata(servers []*v0.ServerJSON) ([]ServerMetadata, error) {
	result := make([]ServerMetadata, 0, len(servers))

	for _, server := range servers {
		metadata, err := ConvertServerJSON(server)
		if err != nil {
			// Skip servers that can't be converted (e.g., missing packages/remotes)
			// Log the error but continue processing other servers
			continue
		}
		result = append(result, metadata)
	}

	return result, nil
}

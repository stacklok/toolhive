// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"fmt"
	"time"

	v0 "github.com/modelcontextprotocol/registry/pkg/api/v0"

	"github.com/stacklok/toolhive/pkg/registry/api"
	"github.com/stacklok/toolhive/pkg/registry/converters"
	types "github.com/stacklok/toolhive/pkg/registry/registry"
)

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

	// Validate the endpoint by actually trying to use it (not checking openapi.yaml)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Try to list servers with a small limit to verify API functionality
	_, err = client.ListServers(ctx, &api.ListOptions{Limit: 1})
	if err != nil {
		return nil, fmt.Errorf("API endpoint not functional: %w", err)
	}

	return p, nil
}

// GetRegistry returns the registry data by fetching all servers from the API
// This method queries the API and converts all servers to ToolHive format.
// Note: This can be slow for large registries as it fetches everything.
func (p *APIRegistryProvider) GetRegistry() (*types.Registry, error) {
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
	registry := &types.Registry{
		Version:       "1.0.0",
		LastUpdated:   time.Now().Format(time.RFC3339),
		Servers:       make(map[string]*types.ImageMetadata),
		RemoteServers: make(map[string]*types.RemoteServerMetadata),
		Groups:        []*types.Group{},
	}

	// Separate servers into container and remote
	for _, server := range serverMetadata {
		if server.IsRemote() {
			if remoteServer, ok := server.(*types.RemoteServerMetadata); ok {
				registry.RemoteServers[remoteServer.Name] = remoteServer
			}
		} else {
			if imageServer, ok := server.(*types.ImageMetadata); ok {
				registry.Servers[imageServer.Name] = imageServer
			}
		}
	}

	return registry, nil
}

// GetServer returns a specific server by name (queries API directly)
func (p *APIRegistryProvider) GetServer(name string) (types.ServerMetadata, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Try direct API lookup first (supports both reverse-DNS and simple names)
	// Build potential reverse-DNS name
	reverseDNSName := converters.BuildReverseDNSName(name)

	// Try the reverse-DNS format first
	serverJSON, err := p.client.GetServer(ctx, reverseDNSName)
	if err == nil {
		return ConvertServerJSON(serverJSON)
	}

	// If that failed and the name is already in reverse-DNS format, try as-is
	if reverseDNSName != name {
		serverJSON, err = p.client.GetServer(ctx, name)
		if err == nil {
			return ConvertServerJSON(serverJSON)
		}
	}

	// Fall back to search for backward compatibility
	servers, err := p.client.SearchServers(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("failed to find server %s: %w", name, err)
	}

	// Find exact match in search results
	for _, server := range servers {
		simpleName := converters.ExtractServerName(server.Name)
		if simpleName == name || server.Name == name {
			return ConvertServerJSON(server)
		}
	}

	return nil, fmt.Errorf("server %s not found in API", name)
}

// SearchServers searches for servers matching the query (queries API directly)
func (p *APIRegistryProvider) SearchServers(query string) ([]types.ServerMetadata, error) {
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
func (p *APIRegistryProvider) ListServers() ([]types.ServerMetadata, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	servers, err := p.client.ListServers(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list servers: %w", err)
	}

	return ConvertServersToMetadata(servers)
}

// GetImageServer returns a specific container server by name (overrides BaseProvider)
// This override is necessary because BaseProvider.GetImageServer calls p.GetServer,
// which would call BaseProvider.GetServer instead of APIRegistryProvider.GetServer
func (p *APIRegistryProvider) GetImageServer(name string) (*types.ImageMetadata, error) {
	server, err := p.GetServer(name)
	if err != nil {
		return nil, err
	}

	// Type assert to ImageMetadata
	if img, ok := server.(*types.ImageMetadata); ok {
		return img, nil
	}

	return nil, fmt.Errorf("server %s is not a container server", name)
}

// ConvertServerJSON converts an MCP Registry API ServerJSON to ToolHive ServerMetadata
// Uses converters from converters.go (same package)
// Note: Only handles OCI packages and remote servers, skips npm/pypi by design
func ConvertServerJSON(serverJSON *v0.ServerJSON) (types.ServerMetadata, error) {
	if serverJSON == nil {
		return nil, fmt.Errorf("serverJSON is nil")
	}

	// Determine if this is a remote server or container-based server
	// Remote servers have the 'remotes' field populated
	// Container servers have the 'packages' field populated
	var result types.ServerMetadata
	var err error

	if len(serverJSON.Remotes) > 0 {
		result, err = converters.ServerJSONToRemoteServerMetadata(serverJSON)
	} else if len(serverJSON.Packages) == 0 {
		// Skip servers without packages or remotes (incomplete entries)
		return nil, fmt.Errorf("server %s has no packages or remotes, skipping", serverJSON.Name)
	} else {
		// ServerJSONToImageMetadata only handles OCI packages, will error on npm/pypi
		result, err = converters.ServerJSONToImageMetadata(serverJSON)
	}

	if err != nil {
		return nil, err
	}

	return result, nil
}

// ConvertServersToMetadata converts a slice of ServerJSON to a slice of ServerMetadata
// Skips servers that cannot be converted (e.g., incomplete entries)
// Uses official converters from toolhive-catalog package
func ConvertServersToMetadata(servers []*v0.ServerJSON) ([]types.ServerMetadata, error) {
	result := make([]types.ServerMetadata, 0, len(servers))

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

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/networking"
	types "github.com/stacklok/toolhive/pkg/registry/registry"
)

// RemoteRegistryProvider provides registry data from a remote HTTP endpoint
type RemoteRegistryProvider struct {
	*BaseProvider
	registryURL    string
	allowPrivateIp bool
}

// NewRemoteRegistryProvider creates a new remote registry provider.
// Validates the registry is reachable before returning with a 5-second timeout.
func NewRemoteRegistryProvider(registryURL string, allowPrivateIp bool) (*RemoteRegistryProvider, error) {
	p := &RemoteRegistryProvider{
		registryURL:    registryURL,
		allowPrivateIp: allowPrivateIp,
	}

	// Initialize the base provider with the GetRegistry function
	p.BaseProvider = NewBaseProvider(p.GetRegistry)

	// Validate the registry is reachable with 5-second timeout
	if err := p.validateConnectivity(); err != nil {
		return nil, fmt.Errorf("registry validation failed: %w", err)
	}

	return p, nil
}

// validateConnectivity checks if the registry is reachable with a 5-second timeout
// and returns valid registry JSON
func (p *RemoteRegistryProvider) validateConnectivity() error {
	// Build HTTP client with 5-second timeout for validation
	builder := networking.NewHttpClientBuilder().
		WithPrivateIPs(p.allowPrivateIp).
		WithTimeout(5 * time.Second)
	if p.allowPrivateIp {
		builder = builder.WithInsecureAllowHTTP(true)
	}
	client, err := builder.Build()
	if err != nil {
		return fmt.Errorf("failed to build http client: %w", err)
	}

	resp, err := client.Get(p.registryURL)
	if err != nil {
		return fmt.Errorf("registry unreachable at %s: %w", p.registryURL, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			logger.Debugf("Failed to close response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("registry returned status %d from %s", resp.StatusCode, p.registryURL)
	}

	// Read and validate the response body contains valid registry JSON
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read registry response: %w", err)
	}

	registry := &types.Registry{}
	if err := json.Unmarshal(data, registry); err != nil {
		return fmt.Errorf("registry returned invalid JSON from %s: %w", p.registryURL, err)
	}

	// Validate the registry has at least the required structure
	// (we don't require servers/groups to exist, but the structure must be valid)
	if registry.Servers == nil && registry.RemoteServers == nil && registry.Groups == nil {
		return fmt.Errorf("registry at %s returned invalid structure: "+
			"missing servers, remote_servers, and groups fields", p.registryURL)
	}

	return nil
}

// GetRegistry returns the remote registry data
func (p *RemoteRegistryProvider) GetRegistry() (*types.Registry, error) {
	// Build HTTP client with security controls
	// If private IPs are allowed, also allow HTTP (for localhost testing)
	builder := networking.NewHttpClientBuilder().WithPrivateIPs(p.allowPrivateIp)
	if p.allowPrivateIp {
		builder = builder.WithInsecureAllowHTTP(true)
	}
	client, err := builder.Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build http client: %w", err)
	}

	resp, err := client.Get(p.registryURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch registry data from URL %s: %w", p.registryURL, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			logger.Debugf("Failed to close response body: %v", err)
		}
	}()

	// Check if the response status code is OK
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("response status code from URL %s not OK: status code %d", p.registryURL, resp.StatusCode)
	}

	// Read the response body
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read registry data from response body: %w", err)
	}

	registry := &types.Registry{}
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

	// Set name field on servers within groups
	for _, group := range registry.Groups {
		if group != nil {
			for name, server := range group.Servers {
				server.Name = name
			}
			for name, server := range group.RemoteServers {
				server.Name = name
			}
		}
	}

	return registry, nil
}

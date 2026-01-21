// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

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
// Validates the registry is reachable before returning.
func NewRemoteRegistryProvider(registryURL string, allowPrivateIp bool) (*RemoteRegistryProvider, error) {
	p := &RemoteRegistryProvider{
		registryURL:    registryURL,
		allowPrivateIp: allowPrivateIp,
	}

	// Initialize the base provider with the GetRegistry function
	p.BaseProvider = NewBaseProvider(p.GetRegistry)

	// Validate the registry is reachable
	if _, err := p.GetRegistry(); err != nil {
		return nil, err
	}

	return p, nil
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

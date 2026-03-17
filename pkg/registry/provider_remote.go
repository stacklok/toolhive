// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	types "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/networking"
)

// RemoteRegistryProvider provides registry data from a remote HTTP endpoint
type RemoteRegistryProvider struct {
	*BaseProvider
	registryURL    string
	allowPrivateIp bool
	skillsMu       sync.RWMutex
	skills         []types.Skill
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
			slog.Debug("failed to close response body", "error", err)
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

	// Try upstream format first, fall back to legacy
	if isUpstreamFormat(data) {
		var upstream types.UpstreamRegistry
		if err := json.Unmarshal(data, &upstream); err != nil {
			return fmt.Errorf("registry returned invalid upstream JSON from %s: %w", p.registryURL, err)
		}
		if len(upstream.Data.Servers) == 0 && len(upstream.Data.Groups) == 0 {
			return fmt.Errorf("registry at %s returned upstream format with no servers or groups", p.registryURL)
		}
		return nil
	}

	registry := &types.Registry{}
	if err := json.Unmarshal(data, registry); err != nil {
		return fmt.Errorf("registry returned invalid JSON from %s: %w", p.registryURL, err)
	}

	// Validate the registry has at least the required structure
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
			slog.Debug("failed to close response body", "error", err)
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

	registry, skills, isLegacy, err := parseRegistryAutoDetect(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse registry data from %s: %w", p.registryURL, err)
	}
	p.setSkills(skills)
	if isLegacy {
		slog.Warn("Remote registry uses legacy format; please migrate to the upstream MCP format. "+
			"Legacy format support will be removed in a future release.",
			"url", p.registryURL)
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

// ListAvailableSkills returns skills discovered from the remote registry data.
// Triggers a registry load if skills haven't been populated yet.
func (p *RemoteRegistryProvider) ListAvailableSkills() ([]types.Skill, error) {
	p.skillsMu.RLock()
	skills := p.skills
	p.skillsMu.RUnlock()

	if skills == nil {
		// Skills are populated as a side effect of GetRegistry
		if _, err := p.GetRegistry(); err != nil {
			return nil, err
		}
		p.skillsMu.RLock()
		skills = p.skills
		p.skillsMu.RUnlock()
	}

	return skills, nil
}

// GetSkill returns a specific skill by namespace and name.
func (p *RemoteRegistryProvider) GetSkill(namespace, name string) (*types.Skill, error) {
	skills, err := p.ListAvailableSkills()
	if err != nil {
		return nil, err
	}
	for i := range skills {
		if skills[i].Namespace == namespace && skills[i].Name == name {
			return &skills[i], nil
		}
	}
	return nil, nil
}

// SearchSkills searches for skills matching the query in name or description.
func (p *RemoteRegistryProvider) SearchSkills(query string) ([]types.Skill, error) {
	skills, err := p.ListAvailableSkills()
	if err != nil {
		return nil, err
	}
	query = strings.ToLower(query)
	var results []types.Skill
	for _, s := range skills {
		if strings.Contains(strings.ToLower(s.Name), query) ||
			strings.Contains(strings.ToLower(s.Description), query) ||
			strings.Contains(strings.ToLower(s.Namespace), query) {
			results = append(results, s)
		}
	}
	return results, nil
}

func (p *RemoteRegistryProvider) setSkills(skills []types.Skill) {
	p.skillsMu.Lock()
	defer p.skillsMu.Unlock()
	p.skills = skills
}

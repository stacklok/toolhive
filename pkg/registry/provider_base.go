// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"fmt"
	"strings"

	types "github.com/stacklok/toolhive-core/registry/types"
)

// BaseProvider provides common implementation for registry providers
type BaseProvider struct {
	// GetRegistryFunc is a function that fetches the registry data
	// This allows different providers to implement their own data fetching logic
	GetRegistryFunc func() (*types.Registry, error)
}

// NewBaseProvider creates a new base provider with the given registry function
func NewBaseProvider(getRegistry func() (*types.Registry, error)) *BaseProvider {
	return &BaseProvider{
		GetRegistryFunc: getRegistry,
	}
}

// GetServer returns a specific server by name (container or remote).
// Supports both full reverse-DNS names (io.github.stacklok/osv) and
// short names (osv) for backward compatibility.
func (p *BaseProvider) GetServer(name string) (types.ServerMetadata, error) {
	reg, err := p.GetRegistryFunc()
	if err != nil {
		return nil, err
	}

	// Try exact match first
	server, found := reg.GetServerByName(name)
	if found {
		return server, nil
	}

	// Fall back to short-name matching: check if name matches the last
	// path component of any server's full reverse-DNS name.
	// e.g. "osv" matches "io.github.stacklok/osv"
	if !strings.Contains(name, "/") {
		matches := findServersByShortName(reg, name)
		if len(matches) == 1 {
			return matches[0].server, nil
		}
		if len(matches) > 1 {
			names := make([]string, len(matches))
			for i, m := range matches {
				names[i] = m.fullName
			}
			return nil, fmt.Errorf("multiple servers match '%s': %s — use the full name",
				name, strings.Join(names, ", "))
		}
	}

	return nil, fmt.Errorf("server not found: %s", name)
}

type shortNameMatch struct {
	fullName string
	server   types.ServerMetadata
}

// findServersByShortName returns all servers whose name ends with "/<shortName>".
func findServersByShortName(reg *types.Registry, shortName string) []shortNameMatch {
	suffix := "/" + shortName
	var matches []shortNameMatch
	for fullName, server := range reg.Servers {
		if strings.HasSuffix(fullName, suffix) {
			matches = append(matches, shortNameMatch{fullName, server})
		}
	}
	for fullName, server := range reg.RemoteServers {
		if strings.HasSuffix(fullName, suffix) {
			matches = append(matches, shortNameMatch{fullName, server})
		}
	}
	return matches
}

// SearchServers searches for servers matching the query (both container and remote)
func (p *BaseProvider) SearchServers(query string) ([]types.ServerMetadata, error) {
	reg, err := p.GetRegistryFunc()
	if err != nil {
		return nil, err
	}

	query = strings.ToLower(query)
	var results []types.ServerMetadata

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
func (p *BaseProvider) ListServers() ([]types.ServerMetadata, error) {
	reg, err := p.GetRegistryFunc()
	if err != nil {
		return nil, err
	}

	// Use the registry's helper method
	return reg.GetAllServers(), nil
}

// ListAvailableSkills returns an empty slice by default.
// Providers that support skills (local, remote) override this.
func (*BaseProvider) ListAvailableSkills() ([]types.Skill, error) {
	return nil, nil
}

// GetSkill returns nil for providers that don't support skills.
func (*BaseProvider) GetSkill(_, _ string) (*types.Skill, error) {
	return nil, nil
}

// SearchSkills returns nil for providers that don't support skills.
func (*BaseProvider) SearchSkills(_ string) ([]types.Skill, error) {
	return nil, nil
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

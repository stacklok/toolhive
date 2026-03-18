// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	catalog "github.com/stacklok/toolhive-catalog/pkg/catalog/toolhive"
	types "github.com/stacklok/toolhive-core/registry/types"
)

// LocalRegistryProvider provides registry data from embedded JSON files or local files
type LocalRegistryProvider struct {
	*BaseProvider
	filePath string
	skillsMu sync.RWMutex
	skills   []types.Skill
}

// NewLocalRegistryProvider creates a new local registry provider
// If filePath is provided, it will read from that file; otherwise uses embedded data
func NewLocalRegistryProvider(filePath ...string) *LocalRegistryProvider {
	var path string
	if len(filePath) > 0 {
		path = filePath[0]
	}

	p := &LocalRegistryProvider{
		filePath: path,
	}

	// Initialize the base provider with the GetRegistry function
	p.BaseProvider = NewBaseProvider(p.GetRegistry)

	return p
}

// GetRegistry returns the registry data from file path or embedded data
func (p *LocalRegistryProvider) GetRegistry() (*types.Registry, error) {
	var registry *types.Registry

	if p.filePath != "" {
		// Read from local file — auto-detect format
		data, err := os.ReadFile(p.filePath)
		if err != nil {
			return nil, fmt.Errorf("failed to read local registry file %s: %w", p.filePath, err)
		}

		var skills []types.Skill
		var isLegacy bool
		registry, skills, isLegacy, err = parseRegistryAutoDetect(data)
		if err != nil {
			return nil, err
		}
		p.setSkills(skills)
		if isLegacy {
			slog.Warn("Registry file uses legacy format; please migrate to the upstream MCP format. "+
				"Legacy format support will be removed in a future release.",
				"file", p.filePath)
		}
	} else {
		// Embedded catalog — always upstream format
		var err error
		var skills []types.Skill
		registry, skills, err = parseUpstreamRegistry(catalog.Upstream())
		if err != nil {
			return nil, fmt.Errorf("failed to parse embedded upstream registry: %w", err)
		}
		p.setSkills(skills)
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

func (p *LocalRegistryProvider) setSkills(skills []types.Skill) {
	p.skillsMu.Lock()
	defer p.skillsMu.Unlock()
	p.skills = skills
}

// ListAvailableSkills returns skills discovered from the upstream registry data.
// Triggers a registry load if skills haven't been populated yet.
func (p *LocalRegistryProvider) ListAvailableSkills() ([]types.Skill, error) {
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
func (p *LocalRegistryProvider) GetSkill(namespace, name string) (*types.Skill, error) {
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
func (p *LocalRegistryProvider) SearchSkills(query string) ([]types.Skill, error) {
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

// parseRegistryData parses JSON data into a Registry struct
func parseRegistryData(data []byte) (*types.Registry, error) {
	registry := &types.Registry{}
	if err := json.Unmarshal(data, registry); err != nil {
		return nil, fmt.Errorf("failed to parse registry data: %w", err)
	}
	return registry, nil
}

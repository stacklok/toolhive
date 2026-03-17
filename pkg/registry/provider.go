// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import types "github.com/stacklok/toolhive-core/registry/types"

//go:generate mockgen -destination=mocks/mock_provider.go -package=mocks -source=provider.go Provider

// Provider defines the interface for registry storage implementations
type Provider interface {
	// GetRegistry returns the complete registry data
	GetRegistry() (*types.Registry, error)

	// GetServer returns a specific server by name (container or remote)
	GetServer(name string) (types.ServerMetadata, error)

	// SearchServers searches for servers matching the query (both container and remote)
	SearchServers(query string) ([]types.ServerMetadata, error)

	// ListServers returns all available servers (both container and remote)
	ListServers() ([]types.ServerMetadata, error)

	// ListAvailableSkills returns skills discovered from the registry data
	ListAvailableSkills() ([]types.Skill, error)

	// GetSkill returns a specific skill by namespace and name
	GetSkill(namespace, name string) (*types.Skill, error)

	// SearchSkills searches for skills matching the query
	SearchSkills(query string) ([]types.Skill, error)
}

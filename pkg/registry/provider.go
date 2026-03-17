// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	types "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/registry/api"
)

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

	// Legacy methods for backward compatibility
	// GetImageServer returns a specific container server by name
	GetImageServer(name string) (*types.ImageMetadata, error)

	// SearchImageServers searches for container servers matching the query
	SearchImageServers(query string) ([]*types.ImageMetadata, error)

	// ListImageServers returns all available container servers
	ListImageServers() ([]*types.ImageMetadata, error)

	// Skills methods
	// Providers that don't support skills (Local, Remote) return nil/empty results via BaseProvider.

	// GetSkill returns a specific skill by namespace and name
	GetSkill(namespace, name string) (*types.Skill, error)
	// ListSkills returns all available skills
	ListSkills(opts *api.SkillsListOptions) (*api.SkillsListResult, error)
	// SearchSkills searches for skills matching the query
	SearchSkills(query string) (*api.SkillsListResult, error)
}

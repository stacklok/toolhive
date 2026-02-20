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

	// Legacy methods for backward compatibility
	// GetImageServer returns a specific container server by name
	GetImageServer(name string) (*types.ImageMetadata, error)

	// SearchImageServers searches for container servers matching the query
	SearchImageServers(query string) ([]*types.ImageMetadata, error)

	// ListImageServers returns all available container servers
	ListImageServers() ([]*types.ImageMetadata, error)
}

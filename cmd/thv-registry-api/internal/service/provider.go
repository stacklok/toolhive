// Package service provides the business logic for the MCP registry API
package service

import (
	"context"

	"github.com/stacklok/toolhive/pkg/registry"
)

//go:generate mockgen -destination=mocks/mock_provider.go -package=mocks -source=provider.go RegistryDataProvider,DeploymentProvider

// RegistryDataProvider abstracts the source of registry data.
// This interface follows the Go principle of small, focused interfaces
// and enables easy testing and multiple implementations.
type RegistryDataProvider interface {
	// GetRegistryData fetches the current registry data.
	// Returns the registry data and any error encountered.
	GetRegistryData(ctx context.Context) (*registry.Registry, error)

	// GetSource returns a descriptive string about where the registry data comes from.
	// Examples: "configmap:namespace/name", "file:/path/to/registry.json", "remote:https://example.com/registry"
	GetSource() string

	// GetRegistryName returns the registry name/identifier for this provider.
	// This name is used for business logic such as finding related Kubernetes resources.
	GetRegistryName() string
}

// DeploymentProvider abstracts access to deployed MCP servers.
type DeploymentProvider interface {
	// ListDeployedServers returns all currently deployed MCP servers.
	// Returns an empty slice if no servers are deployed
	ListDeployedServers(ctx context.Context) ([]*DeployedServer, error)

	// GetDeployedServer returns all deployed servers matching the server registry name.
	// Returns an empty slice if no servers are found.
	GetDeployedServer(ctx context.Context, name string) ([]*DeployedServer, error)
}

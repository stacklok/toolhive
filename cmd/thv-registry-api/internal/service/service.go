// Package regSvc provides the business logic for the MCP registry API
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/registry"
)

var (
	// ErrServerNotFound is returned when a server is not found
	ErrServerNotFound = errors.New("server not found")
)

// serverWithName wraps a ServerMetadata and overrides the name
type serverWithName struct {
	registry.ServerMetadata
	nameOverride string
}

// GetName returns the overridden name if provided, otherwise the original name
func (s *serverWithName) GetName() string {
	if s.nameOverride != "" {
		return s.nameOverride
	}
	return s.ServerMetadata.GetName()
}

//go:generate mockgen -destination=mocks/mock_service.go -package=mocks -source=service.go Service

// RegistryService defines the interface for registry operations
type RegistryService interface {
	// CheckReadiness checks if the regSvc is ready to serve requests
	CheckReadiness(ctx context.Context) error

	// GetRegistry returns the registry data with metadata
	GetRegistry(ctx context.Context) (*registry.Registry, string, error) // returns registry, source, error

	// ListServers returns all servers in the registry
	ListServers(ctx context.Context) ([]registry.ServerMetadata, error)

	// GetServer returns a specific server by name
	GetServer(ctx context.Context, name string) (registry.ServerMetadata, error)

	// ListDeployedServers returns all deployed MCP servers
	ListDeployedServers(ctx context.Context) ([]*DeployedServer, error)

	// GetDeployedServer returns all deployed servers matching the server registry name
	GetDeployedServer(ctx context.Context, name string) ([]*DeployedServer, error)
}

// regSvc implements the RegistryService interface
type regSvc struct {
	registryProvider   RegistryDataProvider
	deploymentProvider DeploymentProvider

	registryData *registry.Registry

	lastFetch     time.Time
	cacheDuration time.Duration
}

// Option is a functional option for configuring the regSvc
type Option func(*regSvc)

// WithCacheDuration sets a custom cache duration for registry data
func WithCacheDuration(duration time.Duration) Option {
	return func(s *regSvc) {
		s.cacheDuration = duration
	}
}

// NewService creates a new registry regSvc with the given providers and options.
// registryProvider is required for registry data access.
// deploymentProvider can be nil if deployed servers functionality is not needed.
func NewService(
	ctx context.Context,
	registryProvider RegistryDataProvider,
	deploymentProvider DeploymentProvider,
	opts ...Option,
) (RegistryService, error) {
	if registryProvider == nil {
		return nil, fmt.Errorf("registry data provider is required")
	}

	s := &regSvc{
		registryProvider:   registryProvider,
		deploymentProvider: deploymentProvider,
		cacheDuration:      30 * time.Second, // Default cache duration
	}

	// Apply functional options
	for _, opt := range opts {
		opt(s)
	}

	// Load initial data
	if err := s.loadRegistryData(ctx); err != nil {
		logger.Warnf("Failed to load initial registry data: %v", err)
		// Don't fail regSvc creation, allow it to retry later
	}

	return s, nil
}

// loadRegistryData loads registry data using the configured provider
func (s *regSvc) loadRegistryData(ctx context.Context) error {
	if s.registryProvider == nil {
		return fmt.Errorf("registry data provider not initialized")
	}

	data, err := s.registryProvider.GetRegistryData(ctx)
	if err != nil {
		return fmt.Errorf("failed to get registry data: %w", err)
	}

	s.registryData = data
	s.lastFetch = time.Now()

	// Count total servers (both container and remote)
	totalServers := len(data.Servers) + len(data.RemoteServers)
	logger.Infof("Loaded registry data: %d servers", totalServers)
	return nil
}

// refreshDataIfNeeded refreshes the registry data if cache has expired
func (s *regSvc) refreshDataIfNeeded(ctx context.Context) error {
	if s.registryProvider == nil {
		return nil // No registry provider configured
	}

	if time.Since(s.lastFetch) > s.cacheDuration {
		if err := s.loadRegistryData(ctx); err != nil {
			logger.Warnf("Failed to refresh registry data: %v", err)
			// Continue with stale data if available
			if s.registryData == nil {
				return err
			}
		}
	}
	return nil
}

// CheckReadiness implements RegistryService.CheckReadiness
func (s *regSvc) CheckReadiness(ctx context.Context) error {
	// Check if we have registry data loaded when a provider is configured
	if s.registryProvider != nil {
		if s.registryData == nil {
			// Try to load it
			if err := s.loadRegistryData(ctx); err != nil {
				return fmt.Errorf("registry data not available: %w", err)
			}
		}
	}
	return nil
}

// GetRegistry implements RegistryService.GetRegistry
func (s *regSvc) GetRegistry(ctx context.Context) (*registry.Registry, string, error) {
	if err := s.refreshDataIfNeeded(ctx); err != nil {
		logger.Warnf("Failed to refresh data: %v", err)
	}

	// Get source information from the provider
	source := "unknown"
	if s.registryProvider != nil {
		source = s.registryProvider.GetSource()
	}

	if s.registryData == nil {
		// Return an empty registry if no data is loaded
		return &registry.Registry{
			Version:     "1.0.0",
			LastUpdated: time.Now().Format(time.RFC3339),
			Servers:     make(map[string]*registry.ImageMetadata),
		}, source, nil
	}

	return s.registryData, source, nil
}

// ListServers implements RegistryService.ListServers
func (s *regSvc) ListServers(ctx context.Context) ([]registry.ServerMetadata, error) {
	if err := s.refreshDataIfNeeded(ctx); err != nil {
		logger.Warnf("Failed to refresh data: %v", err)
	}

	if s.registryData != nil {
		return s.getAllServersWithNames(), nil
	}

	return []registry.ServerMetadata{}, nil
}

// GetServer implements RegistryService.GetServer
func (s *regSvc) GetServer(ctx context.Context, name string) (registry.ServerMetadata, error) {
	if err := s.refreshDataIfNeeded(ctx); err != nil {
		logger.Warnf("Failed to refresh data: %v", err)
	}

	if s.registryData != nil {
		return s.getServerByNameWithName(name)
	}

	return nil, ErrServerNotFound
}

// ListDeployedServers implements RegistryService.ListDeployedServers
func (s *regSvc) ListDeployedServers(ctx context.Context) ([]*DeployedServer, error) {
	if s.deploymentProvider == nil {
		return []*DeployedServer{}, nil
	}

	return s.deploymentProvider.ListDeployedServers(ctx)
}

// GetDeployedServer implements RegistryService.GetDeployedServer
func (s *regSvc) GetDeployedServer(ctx context.Context, name string) ([]*DeployedServer, error) {
	if s.deploymentProvider == nil {
		return []*DeployedServer{}, nil
	}

	return s.deploymentProvider.GetDeployedServer(ctx, name)
}

// getAllServersWithNames returns all servers with names properly populated from map keys
func (s *regSvc) getAllServersWithNames() []registry.ServerMetadata {
	servers := make([]registry.ServerMetadata, 0, len(s.registryData.Servers)+len(s.registryData.RemoteServers))

	// Add container servers with names
	for name, server := range s.registryData.Servers {
		servers = append(servers, &serverWithName{
			ServerMetadata: server,
			nameOverride:   name,
		})
	}

	// Add remote servers with names
	for name, server := range s.registryData.RemoteServers {
		servers = append(servers, &serverWithName{
			ServerMetadata: server,
			nameOverride:   name,
		})
	}

	return servers
}

// getServerByNameWithName returns a server by name with name properly populated
func (s *regSvc) getServerByNameWithName(name string) (registry.ServerMetadata, error) {
	// Check container servers first
	if server, ok := s.registryData.Servers[name]; ok {
		return &serverWithName{
			ServerMetadata: server,
			nameOverride:   name,
		}, nil
	}

	// Check remote servers
	if server, ok := s.registryData.RemoteServers[name]; ok {
		return &serverWithName{
			ServerMetadata: server,
			nameOverride:   name,
		}, nil
	}

	return nil, ErrServerNotFound
}

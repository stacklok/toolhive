package client

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/viper"
	"github.com/stacklok/toolhive/pkg/config"
	ct "github.com/stacklok/toolhive/pkg/container"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/transport"
)

// Client represents a registered ToolHive client.
type Client struct {
	Name MCPClient `json:"name"`
}

// Manager is the interface for managing registered ToolHive clients.
type Manager interface {
	// ListClients returns a list of all registered.
	ListClients() ([]Client, error)
	// RegisterClients registers multiple clients with ToolHive.
	RegisterClients(ctx context.Context, clients []Client) error
	// UnregisterClients unregisters multiple clients from ToolHive.
	UnregisterClients(ctx context.Context, clients []Client) error
	// GetConfig returns a copy of the current configuration
	GetConfig() (*config.Config, error)
	// SetConfig updates the configuration with the provided config
	SetConfig(cfg *config.Config) error
}

type defaultManager struct {
	runtime    rt.Runtime
	config     *config.Config
	configPath string
}

// NewManager creates a new client manager instance.
func NewManager(ctx context.Context) (Manager, error) {
	// Check if config path is set via viper (from CLI flag)
	if configPath := viper.GetString("config"); configPath != "" {
		return NewManagerWithConfigPath(ctx, configPath)
	}
	return NewManagerWithConfigPath(ctx, "")
}

// NewManagerWithConfigPath creates a new client manager instance with a specific config path.
func NewManagerWithConfigPath(ctx context.Context, configPath string) (Manager, error) {
	runtime, err := ct.NewFactory().Create(ctx)
	if err != nil {
		return nil, err
	}

	// Load config from the specified path or default
	cfg, err := config.LoadOrCreateConfigWithPath(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	return &defaultManager{
		runtime:    runtime,
		config:     cfg,
		configPath: configPath,
	}, nil
}

// GetConfig returns a copy of the current configuration to prevent race conditions
func (m *defaultManager) GetConfig() (*config.Config, error) {
	// Return a deep copy to prevent race conditions
	cfgCopy := *m.config
	return &cfgCopy, nil
}

// SetConfig updates the configuration and persists it to disk
func (m *defaultManager) SetConfig(cfg *config.Config) error {
	// Update the in-memory config
	m.config = cfg

	// Persist to disk using the locking mechanism
	if m.configPath != "" {
		return config.UpdateConfigAtPath(m.configPath, func(c *config.Config) {
			*c = *cfg
		})
	}
	return config.UpdateConfig(func(c *config.Config) {
		*c = *cfg
	})
}

func (m *defaultManager) ListClients() ([]Client, error) {
	clients := []Client{}

	for _, clientName := range m.config.Clients.RegisteredClients {
		clients = append(clients, Client{Name: MCPClient(clientName)})
	}

	return clients, nil
}

// RegisterClients registers multiple clients with ToolHive.
func (m *defaultManager) RegisterClients(ctx context.Context, clients []Client) error {
	for _, client := range clients {
		// Check if client is already registered and skip.
		for _, registeredClient := range m.config.Clients.RegisteredClients {
			if registeredClient == string(client.Name) {
				logger.Infof("Client %s is already registered, skipping...", client.Name)
				continue
			}
		}

		// Add the client to the registered clients list
		m.config.Clients.RegisteredClients = append(m.config.Clients.RegisteredClients, string(client.Name))

		// Persist the updated config
		if err := m.SetConfig(m.config); err != nil {
			return fmt.Errorf("failed to update configuration for client %s: %w", client.Name, err)
		}

		logger.Infof("Successfully registered client: %s\n", client.Name)

		// Add currently running MCPs to the newly registered client
		if err := m.addRunningMCPsToClient(ctx, client.Name); err != nil {
			return fmt.Errorf("failed to add running MCPs to client %s: %v", client.Name, err)
		}
	}
	return nil
}

// addRunningMCPsToClient adds currently running MCP servers to the specified client's configuration
func (m *defaultManager) addRunningMCPsToClient(ctx context.Context, clientType MCPClient) error {
	// List workloads
	containers, err := m.runtime.ListWorkloads(ctx)
	if err != nil {
		return fmt.Errorf("failed to list containers: %v", err)
	}

	// Filter containers to only show those managed by ToolHive and running
	var runningContainers []rt.ContainerInfo
	for _, c := range containers {
		if labels.IsToolHiveContainer(c.Labels) && c.State == "running" {
			runningContainers = append(runningContainers, c)
		}
	}

	if len(runningContainers) == 0 {
		// No running servers, nothing to do
		return nil
	}

	// Find the client configuration for the specified client
	clientConfig, err := FindClientConfig(clientType)
	if err != nil {
		if errors.Is(err, ErrConfigFileNotFound) {
			// Create a new client configuration if it doesn't exist
			clientConfig, err = CreateClientConfig(clientType)
			if err != nil {
				return fmt.Errorf("failed to create client configuration for %s: %w", clientType, err)
			}
		} else {
			return fmt.Errorf("failed to find client configuration: %w", err)
		}
	}

	// For each running container, add it to the client configuration
	for _, c := range runningContainers {
		// Get container name from labels
		name := labels.GetContainerName(c.Labels)
		if name == "" {
			name = c.Name // Fallback to container name
		}

		// Get tool type from labels
		toolType := labels.GetToolType(c.Labels)

		// Only include containers with tool type "mcp"
		if toolType != "mcp" {
			continue
		}

		// Get port from labels
		port, err := labels.GetPort(c.Labels)
		if err != nil {
			continue // Skip if we can't get the port
		}

		transportType := labels.GetTransportType(c.Labels)

		// Generate URL for the MCP server
		url := GenerateMCPServerURL(transportType, transport.LocalhostIPv4, port, name)

		// Update the MCP server configuration with locking
		if err := Upsert(*clientConfig, name, url, transportType); err != nil {
			logger.Warnf("Warning: Failed to update MCP server configuration in %s: %v", clientConfig.Path, err)
			continue
		}

		logger.Infof("Added MCP server %s to client %s\n", name, clientType)
	}

	return nil
}

// UnregisterClients unregisters multiple clients from ToolHive.
func (m *defaultManager) UnregisterClients(ctx context.Context, clients []Client) error {
	for _, client := range clients {
		// Find and remove the client from registered clients list
		for i, registeredClient := range m.config.Clients.RegisteredClients {
			if registeredClient == string(client.Name) {
				// Remove client from slice
				m.config.Clients.RegisteredClients = append(m.config.Clients.RegisteredClients[:i], m.config.Clients.RegisteredClients[i+1:]...)
				logger.Infof("Successfully unregistered client: %s\n", client.Name)
				break // Found and removed, no need to continue
			}
		}
		// Persist the updated config
		if err := m.SetConfig(m.config); err != nil {
			return fmt.Errorf("failed to update configuration for client %s: %w", client.Name, err)
		}
		// Remove MCPs from client configuration
		if err := m.removeMCPsFromClient(ctx, client.Name); err != nil {
			logger.Warnf("Warning: Failed to remove MCPs from client %s: %v", client.Name, err)
		}
	}
	return nil
}

// removeMCPsFromClient removes currently running MCP servers from the specified client's configuration
func (m *defaultManager) removeMCPsFromClient(ctx context.Context, clientType MCPClient) error {
	// List workloads
	containers, err := m.runtime.ListWorkloads(ctx)
	if err != nil {
		return fmt.Errorf("failed to list containers: %v", err)
	}

	// Filter containers to only show those managed by ToolHive and running
	var runningContainers []rt.ContainerInfo
	for _, c := range containers {
		if labels.IsToolHiveContainer(c.Labels) && c.State == "running" {
			runningContainers = append(runningContainers, c)
		}
	}

	if len(runningContainers) == 0 {
		// No running servers, nothing to do
		return nil
	}

	// Find the client configuration for the specified client
	clientConfig, err := FindClientConfig(clientType)
	if err != nil {
		return fmt.Errorf("failed to find client configurations: %w", err)
	}

	// For each running container, remove it from the client configuration
	for _, c := range runningContainers {
		// Get container name from labels
		name := labels.GetContainerName(c.Labels)
		if name == "" {
			name = c.Name // Fallback to container name
		}

		// Get tool type from labels
		toolType := labels.GetToolType(c.Labels)

		// Only include containers with tool type "mcp"
		if toolType != "mcp" {
			continue
		}

		// Remove the MCP server configuration with locking
		if err := clientConfig.ConfigUpdater.Remove(name); err != nil {
			logger.Warnf("Warning: Failed to remove MCP server configuration from %s: %v", clientConfig.Path, err)
			continue
		}

		logger.Infof("Removed MCP server %s from client %s\n", name, clientType)
	}

	return nil
}

// ConfigPath returns the config path used by the manager (for debugging)
func (m *defaultManager) ConfigPath() string {
	return m.configPath
}

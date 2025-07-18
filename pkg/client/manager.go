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

// ClientStatus represents the status of a client
type ClientStatus struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	ConfigPath string `json:"config_path,omitempty"`
}

// Client represents a registered ToolHive client.
type Client struct {
	Name MCPClient `json:"name"`
}

// Manager defines the interface for managing MCP clients
type Manager interface {
	// GetConfig returns the current configuration
	GetConfig() (*config.Config, error)
	// SetConfig updates the configuration and persists it to disk
	SetConfig(cfg *config.Config) error

	// Config update methods for specific sections
	UpdateConfig(updateFn func(*config.Config)) error
	UpdateOtelConfig(updateFn func(*config.OpenTelemetryConfig)) error
	UpdateSecretsConfig(updateFn func(*config.Secrets)) error
	UpdateClientsConfig(updateFn func(*config.Clients)) error

	// Client management methods
	ListClients() ([]Client, error)
	RegisterClients(ctx context.Context, clients []Client) error
	UnregisterClients(ctx context.Context, clients []Client) error
	ListRegisteredClients(ctx context.Context) ([]string, error)
	GetClientStatus(ctx context.Context, clientType string) (*ClientStatus, error)
	SetupClient(ctx context.Context, clientType string) error

	// ConfigPath returns the config path used by the manager (for debugging)
	ConfigPath() string
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

// UpdateConfig updates the configuration using the provided function
func (m *defaultManager) UpdateConfig(updateFn func(*config.Config)) error {
	if m.configPath != "" {
		return config.UpdateConfigAtPath(m.configPath, updateFn)
	}
	return config.UpdateConfig(updateFn)
}

// UpdateOtelConfig updates the OpenTelemetry configuration
func (m *defaultManager) UpdateOtelConfig(updateFn func(*config.OpenTelemetryConfig)) error {
	return m.UpdateConfig(func(cfg *config.Config) {
		updateFn(&cfg.OTEL)
	})
}

// UpdateSecretsConfig updates the secrets configuration
func (m *defaultManager) UpdateSecretsConfig(updateFn func(*config.Secrets)) error {
	return m.UpdateConfig(func(cfg *config.Config) {
		updateFn(&cfg.Secrets)
	})
}

// UpdateClientsConfig updates the clients configuration
func (m *defaultManager) UpdateClientsConfig(updateFn func(*config.Clients)) error {
	return m.UpdateConfig(func(cfg *config.Config) {
		updateFn(&cfg.Clients)
	})
}

// ListRegisteredClients returns a list of registered client names
func (m *defaultManager) ListRegisteredClients(ctx context.Context) ([]string, error) {
	cfg, err := m.GetConfig()
	if err != nil {
		return nil, err
	}
	return cfg.Clients.RegisteredClients, nil
}

// GetClientStatus returns the status of a specific client
func (m *defaultManager) GetClientStatus(ctx context.Context, clientType string) (*ClientStatus, error) {
	cfg, err := m.GetConfig()
	if err != nil {
		return nil, err
	}

	// Check if client is registered
	status := "unregistered"
	for _, registeredClient := range cfg.Clients.RegisteredClients {
		if registeredClient == clientType {
			status = "registered"
			break
		}
	}

	return &ClientStatus{
		Name:       clientType,
		Status:     status,
		ConfigPath: m.configPath,
	}, nil
}

// SetupClient sets up a client (placeholder implementation)
func (m *defaultManager) SetupClient(ctx context.Context, clientType string) error {
	// This is a placeholder - actual implementation would depend on client-specific setup
	return fmt.Errorf("client setup not implemented for %s", clientType)
}

// ConfigPath returns the config path used by the manager (for debugging)
func (m *defaultManager) ConfigPath() string {
	return m.configPath
}

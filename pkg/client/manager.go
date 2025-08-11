package client

import (
	"context"
	"errors"
	"fmt"

	"github.com/stacklok/toolhive/pkg/config"
	ct "github.com/stacklok/toolhive/pkg/container"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/logger"
)

const (
	mcpToolType = "mcp"
)

// Client represents a registered ToolHive client.
type Client struct {
	Name MCPClient `json:"name"`
}

// Manager is the interface for managing registered ToolHive clients.
type Manager interface {
	// ListClients returns a list of all registered.
	ListClients() ([]Client, error)
	// RegisterClients registers multiple clients with ToolHive for the specified workloads.
	RegisterClients(clients []Client, workloads []core.Workload) error
	// UnregisterClients unregisters multiple clients from ToolHive.
	UnregisterClients(ctx context.Context, clients []Client) error
	// AddServerToClients adds an MCP server to the appropriate client configurations.
	AddServerToClients(ctx context.Context, serverName, serverURL, transportType, group string) error
	// RemoveServerFromClients removes an MCP server from the appropriate client configurations.
	RemoveServerFromClients(ctx context.Context, serverName, group string) error
}

type defaultManager struct {
	runtime      rt.Runtime
	groupManager groups.Manager
}

// NewManager creates a new client manager instance.
func NewManager(ctx context.Context) (Manager, error) {
	runtime, err := ct.NewFactory().Create(ctx)
	if err != nil {
		return nil, err
	}

	groupManager, err := groups.NewManager()
	if err != nil {
		return nil, err
	}

	return &defaultManager{
		runtime:      runtime,
		groupManager: groupManager,
	}, nil
}

func (*defaultManager) ListClients() ([]Client, error) {
	clients := []Client{}
	appConfig := config.GetConfig()

	for _, clientName := range appConfig.Clients.RegisteredClients {
		clients = append(clients, Client{Name: MCPClient(clientName)})
	}

	return clients, nil
}

// RegisterClients registers multiple clients with ToolHive for the specified workloads.
func (m *defaultManager) RegisterClients(clients []Client, workloads []core.Workload) error {
	for _, client := range clients {
		// Add specified workloads to the client
		if err := m.addWorkloadsToClient(client.Name, workloads); err != nil {
			return fmt.Errorf("failed to add workloads to client %s: %v", client.Name, err)
		}
	}
	return nil
}

// UnregisterClients unregisters multiple clients from ToolHive.
func (m *defaultManager) UnregisterClients(ctx context.Context, clients []Client) error {
	for _, client := range clients {
		err := config.UpdateConfig(func(c *config.Config) {
			// Find and remove the client from registered clients list
			for i, registeredClient := range c.Clients.RegisteredClients {
				if registeredClient == string(client.Name) {
					// Remove client from slice
					c.Clients.RegisteredClients = append(c.Clients.RegisteredClients[:i], c.Clients.RegisteredClients[i+1:]...)
					logger.Infof("Successfully unregistered client: %s\n", client.Name)
					return
				}
			}
			logger.Warnf("Client %s was not found in registered clients list", client.Name)
		})
		if err != nil {
			return fmt.Errorf("failed to update configuration for client %s: %w", client.Name, err)
		}
		// Remove MCPs from client configuration
		if err := m.removeMCPsFromClient(ctx, client.Name); err != nil {
			logger.Warnf("Warning: Failed to remove MCPs from client %s: %v", client.Name, err)
		}
	}
	return nil
}

// AddServerToClients adds an MCP server to the appropriate client configurations.
// If the workload belongs to a group, only clients registered with that group are updated.
// If the workload has no group, all registered clients are updated (backward compatibility).
func (m *defaultManager) AddServerToClients(
	ctx context.Context, serverName, serverURL, transportType, group string,
) error {
	targetClients := m.getTargetClients(ctx, serverName, group)

	if len(targetClients) == 0 {
		logger.Infof("No target clients found for server %s", serverName)
		return nil
	}

	// Add the server to each target client
	for _, clientName := range targetClients {
		if err := m.updateClientWithServer(clientName, serverName, serverURL, transportType); err != nil {
			logger.Warnf("Warning: Failed to update client %s: %v", clientName, err)
		}
	}
	return nil
}

// RemoveServerFromClients removes an MCP server from the appropriate client configurations.
// If the server belongs to a group, only clients registered with that group are updated.
// If the server has no group, all registered clients are updated (backward compatibility).
func (m *defaultManager) RemoveServerFromClients(ctx context.Context, serverName, group string) error {
	targetClients := m.getTargetClients(ctx, serverName, group)

	if len(targetClients) == 0 {
		logger.Infof("No target clients found for server %s", serverName)
		return nil
	}

	// Remove the server from each target client
	for _, clientName := range targetClients {
		if err := m.removeServerFromClient(MCPClient(clientName), serverName); err != nil {
			logger.Warnf("Warning: Failed to remove server from client %s: %v", clientName, err)
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
		if toolType != mcpToolType {
			continue
		}

		if err := m.removeServerFromClient(clientType, name); err != nil {
			logger.Warnf("Warning: %v", err)
			continue
		}
	}

	return nil
}

// addWorkloadsToClient adds the specified workloads to the client's configuration
func (m *defaultManager) addWorkloadsToClient(clientType MCPClient, workloads []core.Workload) error {
	if len(workloads) == 0 {
		// No workloads to add, nothing more to do
		return nil
	}

	// For each workload, add it to the client configuration
	for _, workload := range workloads {
		if workload.ToolType != mcpToolType {
			continue
		}

		// Use the common update function (creates config if needed)
		err := m.updateClientWithServer(
			string(clientType), workload.Name, workload.URL, string(workload.TransportType),
		)
		if err != nil {
			return fmt.Errorf("failed to add workload %s to client %s: %v", workload.Name, clientType, err)
		}

		logger.Infof("Added MCP server %s to client %s\n", workload.Name, clientType)
	}

	return nil
}

// removeServerFromClient removes an MCP server from a single client configuration
func (*defaultManager) removeServerFromClient(clientName MCPClient, serverName string) error {
	clientConfig, err := FindClientConfig(clientName)
	if err != nil {
		return fmt.Errorf("failed to find client configurations: %w", err)
	}

	// Remove the MCP server configuration with locking
	if err := clientConfig.ConfigUpdater.Remove(serverName); err != nil {
		return fmt.Errorf("failed to remove MCP server configuration from %s: %v", clientConfig.Path, err)
	}

	logger.Infof("Removed MCP server %s from client %s\n", serverName, clientName)
	return nil
}

// updateClientWithServer updates a single client with an MCP server configuration, creating config if needed
func (*defaultManager) updateClientWithServer(clientName, serverName, serverURL, transportType string) error {
	clientConfig, err := FindClientConfig(MCPClient(clientName))
	if err != nil {
		if errors.Is(err, ErrConfigFileNotFound) {
			// Create a new client configuration if it doesn't exist
			clientConfig, err = CreateClientConfig(MCPClient(clientName))
			if err != nil {
				return fmt.Errorf("failed to create client configuration for %s: %w", clientName, err)
			}
		} else {
			return fmt.Errorf("failed to find client configuration: %w", err)
		}
	}

	logger.Infof("Updating client configuration: %s", clientConfig.Path)

	if err := Upsert(*clientConfig, serverName, serverURL, transportType); err != nil {
		return fmt.Errorf("failed to update MCP server configuration in %s: %v", clientConfig.Path, err)
	}

	logger.Infof("Successfully updated client configuration: %s", clientConfig.Path)
	return nil
}

// getTargetClients determines which clients should be updated based on workload group
func (m *defaultManager) getTargetClients(ctx context.Context, serverName, groupName string) []string {
	// Server belongs to a group - only update clients registered with that group
	if groupName != "" {
		group, err := m.groupManager.Get(ctx, groupName)
		if err != nil {
			logger.Warnf(
				"Warning: Failed to get group %s for server %s, skipping client config updates: %v",
				group, serverName, err,
			)
			return nil
		}

		logger.Infof(
			"Server %s belongs to group %s, updating %d registered client(s)",
			serverName, group, len(group.RegisteredClients),
		)
		return group.RegisteredClients
	}

	// Server has no group - use backward compatible behavior (update all registered clients)
	appConfig := config.GetConfig()
	targetClients := appConfig.Clients.RegisteredClients
	logger.Infof(
		"Server %s has no group, updating %d globally registered client(s) for backward compatibility",
		serverName, len(targetClients),
	)
	return targetClients
}

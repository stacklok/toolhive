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
//
//go:generate mockgen -destination=mocks/mock_manager.go -package=mocks -source=manager.go Manager
type Manager interface {
	// ListClients returns a list of all registered.
	ListClients() ([]Client, error)
	// RegisterClients registers multiple clients with ToolHive for the specified workloads.
	RegisterClients(clients []Client, workloads []core.Workload) error
	// UnregisterClients unregisters multiple clients from ToolHive for the specified workloads.
	UnregisterClients(ctx context.Context, clients []Client, workloads []core.Workload) error
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

// UnregisterClients unregisters multiple clients from ToolHive for the specified workloads.
func (m *defaultManager) UnregisterClients(_ context.Context, clients []Client, workloads []core.Workload) error {
	for _, client := range clients {
		// Remove specified workloads from the client
		if err := m.removeWorkloadsFromClient(client.Name, workloads); err != nil {
			return fmt.Errorf("failed to remove workloads from client %s: %v", client.Name, err)
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

// removeWorkloadsFromClient removes the specified workloads from the client's configuration
func (m *defaultManager) removeWorkloadsFromClient(clientType MCPClient, workloads []core.Workload) error {
	if len(workloads) == 0 {
		// No workloads to remove, nothing to do
		return nil
	}

	// For each workload, remove it from the client configuration
	for _, workload := range workloads {
		if workload.ToolType != mcpToolType {
			continue
		}

		err := m.removeServerFromClient(clientType, workload.Name)
		if err != nil {
			return fmt.Errorf("failed to remove workload %s from client %s: %v", workload.Name, clientType, err)
		}
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

	logger.Infof("Removed MCP server %s from client %s", serverName, clientName)
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
			serverName, group.Name, len(group.RegisteredClients),
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

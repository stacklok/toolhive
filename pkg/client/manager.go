// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/stacklok/toolhive/pkg/config"
	ct "github.com/stacklok/toolhive/pkg/container"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/groups"
)

// Client represents a registered ToolHive client.
type Client struct {
	Name ClientApp `json:"name"`
}

// RegisteredClient represents a registered client with its associated groups.
type RegisteredClient struct {
	Name   ClientApp `json:"name"`
	Groups []string  `json:"groups"`
}

// Manager is the interface for managing registered ToolHive clients.
//
//go:generate mockgen -destination=mocks/mock_manager.go -package=mocks -source=manager.go Manager
type Manager interface {
	// ListClients returns a list of all registered clients with their group information.
	ListClients(ctx context.Context) ([]RegisteredClient, error)
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
	runtime        rt.Runtime
	groupManager   groups.Manager
	configProvider config.Provider
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
		runtime:        runtime,
		groupManager:   groupManager,
		configProvider: config.NewDefaultProvider(),
	}, nil
}

// NewManagerWithProvider creates a new client manager instance with a custom config provider.
// This is useful for testing to avoid using the singleton config.
func NewManagerWithProvider(ctx context.Context, configProvider config.Provider) (Manager, error) {
	runtime, err := ct.NewFactory().Create(ctx)
	if err != nil {
		return nil, err
	}

	groupManager, err := groups.NewManager()
	if err != nil {
		return nil, err
	}

	return &defaultManager{
		runtime:        runtime,
		groupManager:   groupManager,
		configProvider: configProvider,
	}, nil
}

// SetConfigProvider sets a custom config provider for testing purposes.
// This allows tests to inject a test config provider to avoid modifying the real config file.
func (m *defaultManager) SetConfigProvider(provider config.Provider) {
	m.configProvider = provider
}

func (m *defaultManager) ListClients(ctx context.Context) ([]RegisteredClient, error) {
	cfg := m.configProvider.GetConfig()

	// Get all groups
	allGroups, err := m.groupManager.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list groups: %w", err)
	}

	clientGroups := make(map[string][]string) // client -> groups
	allRegisteredClients := make(map[string]bool)

	if len(allGroups) > 0 {
		// Collect clients from all groups
		for _, group := range allGroups {
			for _, clientName := range group.RegisteredClients {
				allRegisteredClients[clientName] = true
				clientGroups[clientName] = append(clientGroups[clientName], group.Name)
			}
		}
	}

	// Add clients from global config that might not be in any group
	for _, clientName := range cfg.Clients.RegisteredClients {
		if !allRegisteredClients[clientName] {
			allRegisteredClients[clientName] = true
			if len(allGroups) > 0 {
				clientGroups[clientName] = []string{} // no groups
			}
		}
	}

	// Convert to slice for return
	// Initialize as empty slice to ensure JSON encodes as [] instead of null when empty
	registeredClients := make([]RegisteredClient, 0)
	for clientName := range allRegisteredClients {
		registered := RegisteredClient{
			Name:   ClientApp(clientName),
			Groups: clientGroups[clientName],
		}
		registeredClients = append(registeredClients, registered)
	}

	return registeredClients, nil
}

// RegisterClients registers multiple clients with ToolHive for the specified workloads.
func (m *defaultManager) RegisterClients(clients []Client, workloads []core.Workload) error {
	for _, client := range clients {
		// Add specified workloads to the client
		if err := m.addWorkloadsToClient(client.Name, workloads); err != nil {
			return fmt.Errorf("failed to add workloads to client %s: %w", client.Name, err)
		}
	}
	return nil
}

// UnregisterClients unregisters multiple clients from ToolHive for the specified workloads.
func (m *defaultManager) UnregisterClients(_ context.Context, clients []Client, workloads []core.Workload) error {
	for _, client := range clients {
		// Remove specified workloads from the client
		if err := m.removeWorkloadsFromClient(client.Name, workloads); err != nil {
			return fmt.Errorf("failed to remove workloads from client %s: %w", client.Name, err)
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
		slog.Debug("no target clients found for server", "server", serverName)
		return nil
	}

	// Add the server to each target client
	for _, clientName := range targetClients {
		if err := m.updateClientWithServer(clientName, serverName, serverURL, transportType); err != nil {
			slog.Warn("failed to update client", "client", clientName, "error", err)
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
		slog.Debug("no target clients found for server", "server", serverName)
		return nil
	}

	// Remove the server from each target client
	for _, clientName := range targetClients {
		if err := m.removeServerFromClient(ClientApp(clientName), serverName); err != nil && !errors.Is(err, ErrConfigFileNotFound) {
			slog.Warn("failed to remove server from client", "client", clientName, "error", err)
		}
	}

	return nil
}

// addWorkloadsToClient adds the specified workloads to the client's configuration
func (m *defaultManager) addWorkloadsToClient(clientType ClientApp, workloads []core.Workload) error {
	if len(workloads) == 0 {
		// No workloads to add, nothing more to do
		return nil
	}

	// For each workload, add it to the client configuration
	for _, workload := range workloads {
		// Use the common update function (creates config if needed)
		err := m.updateClientWithServer(
			string(clientType), workload.Name, workload.URL, string(workload.TransportType),
		)
		if err != nil {
			return fmt.Errorf("failed to add workload %s to client %s: %w", workload.Name, clientType, err)
		}

		slog.Debug("added mcp server to client", "server", workload.Name, "client", clientType)
	}

	return nil
}

// removeWorkloadsFromClient removes the specified workloads from the client's configuration
func (m *defaultManager) removeWorkloadsFromClient(clientType ClientApp, workloads []core.Workload) error {
	if len(workloads) == 0 {
		// No workloads to remove, nothing to do
		return nil
	}

	// For each workload, remove it from the client configuration
	for _, workload := range workloads {
		err := m.removeServerFromClient(clientType, workload.Name)
		if err != nil {
			return fmt.Errorf("failed to remove workload %s from client %s: %w", workload.Name, clientType, err)
		}
	}

	return nil
}

// removeServerFromClient removes an MCP server from a single client configuration
func (*defaultManager) removeServerFromClient(clientName ClientApp, serverName string) error {
	clientConfig, err := FindClientConfig(clientName)
	if err != nil {
		return fmt.Errorf("failed to find client configurations: %w", err)
	}

	// Remove the MCP server configuration with locking
	if err := clientConfig.ConfigUpdater.Remove(serverName); err != nil {
		return fmt.Errorf("failed to remove MCP server configuration from %s: %w", clientConfig.Path, err)
	}

	slog.Debug("removed mcp server from client", "server", serverName, "client", clientName)
	return nil
}

// updateClientWithServer updates a single client with an MCP server configuration, creating config if needed
func (*defaultManager) updateClientWithServer(clientName, serverName, serverURL, transportType string) error {
	clientConfig, err := FindClientConfig(ClientApp(clientName))
	if err != nil {
		if errors.Is(err, ErrConfigFileNotFound) {
			// Create a new client configuration if it doesn't exist
			clientConfig, err = CreateClientConfig(ClientApp(clientName))
			if err != nil {
				return fmt.Errorf("failed to create client configuration for %s: %w", clientName, err)
			}
		} else {
			return fmt.Errorf("failed to find client configuration: %w", err)
		}
	}

	slog.Debug("updating client configuration", "path", clientConfig.Path)

	if err := Upsert(*clientConfig, serverName, serverURL, transportType); err != nil {
		return fmt.Errorf("failed to update MCP server configuration in %s: %w", clientConfig.Path, err)
	}

	slog.Debug("successfully updated client configuration", "path", clientConfig.Path)
	return nil
}

// getTargetClients determines which clients should be updated based on workload group
func (m *defaultManager) getTargetClients(ctx context.Context, serverName, groupName string) []string {
	// Server belongs to a group - only update clients registered with that group
	if groupName != "" {
		group, err := m.groupManager.Get(ctx, groupName)
		if err != nil {
			slog.Warn("failed to get group, skipping client config updates",
				"group", groupName, "server", serverName, "error", err)
			return nil
		}

		slog.Debug("server belongs to group, updating registered clients",
			"server", serverName, "group", group.Name, "count", len(group.RegisteredClients))
		return group.RegisteredClients
	}

	// Server has no group - use backward compatible behavior (update all registered clients)
	appConfig := m.configProvider.GetConfig()
	targetClients := appConfig.Clients.RegisteredClients
	slog.Debug("server has no group, updating globally registered clients for backward compatibility",
		"server", serverName, "count", len(targetClients))
	return targetClients
}

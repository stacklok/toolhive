// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/groups"
)

// ClientManager encapsulates dependencies for client operations
//
//nolint:revive // ClientManager is intentionally named to avoid conflict with existing Manager interface
type ClientManager struct {
	homeDir            string
	groupManager       groups.Manager
	clientIntegrations []clientAppConfig
	configProvider     config.Provider
}

// NewClientManager creates a new ClientManager with default dependencies
func NewClientManager() (*ClientManager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	groupManager, err := groups.NewManager()
	if err != nil {
		// If group manager fails to initialize, we'll just skip group checks
		groupManager = nil
	}

	return &ClientManager{
		homeDir:            home,
		groupManager:       groupManager,
		clientIntegrations: supportedClientIntegrations,
		configProvider:     config.NewDefaultProvider(),
	}, nil
}

// NewTestClientManager creates a new ClientManager with test dependencies
func NewTestClientManager(
	homeDir string,
	groupManager groups.Manager,
	clientIntegrations []clientAppConfig,
	configProvider config.Provider,
) *ClientManager {
	return &ClientManager{
		homeDir:            homeDir,
		groupManager:       groupManager,
		clientIntegrations: clientIntegrations,
		configProvider:     configProvider,
	}
}

// ClientAppStatus represents the status of a supported client application
//
//nolint:revive // ClientAppStatus is intentionally named for clarity across packages
type ClientAppStatus struct {
	// ClientType is the type of MCP client
	ClientType ClientApp `json:"client_type"`

	// Installed indicates whether the client is installed on the system
	Installed bool `json:"installed"`

	// Registered indicates whether the client is registered in the ToolHive configuration
	Registered bool `json:"registered"`
}

// GetClientStatus returns the status of all supported MCP clients using this manager's dependencies
func (cm *ClientManager) GetClientStatus(ctx context.Context) ([]ClientAppStatus, error) {
	var statuses []ClientAppStatus

	// Get app configuration to check for registered clients
	appConfig := cm.configProvider.GetConfig()
	registeredClients := make(map[string]bool)

	// Create a map of registered clients for quick lookup from config
	for _, client := range appConfig.Clients.RegisteredClients {
		registeredClients[client] = true
	}

	// Also check for clients registered in groups if group manager is available
	if cm.groupManager != nil {
		allGroups, err := cm.groupManager.List(ctx)
		if err == nil {
			// Collect clients from all groups
			for _, group := range allGroups {
				for _, clientName := range group.RegisteredClients {
					registeredClients[clientName] = true
				}
			}
		}
	}

	for _, cfg := range cm.clientIntegrations {
		status := ClientAppStatus{
			ClientType: cfg.ClientType,
			Installed:  false, // start with assuming client is not installed
			Registered: registeredClients[string(cfg.ClientType)],
		}

		// Determine path to check based on configuration
		var pathToCheck string
		if len(cfg.RelPath) == 0 {
			// If RelPath is empty, look at just the settings file
			pathToCheck = filepath.Join(cm.homeDir, cfg.SettingsFile)
		} else {
			// Otherwise build the directory path using RelPath
			pathToCheck = buildConfigDirectoryPath(cfg.RelPath, cfg.PlatformPrefix, []string{cm.homeDir})
		}

		// Check if the path exists
		if _, err := os.Stat(pathToCheck); err == nil {
			status.Installed = true
		}

		statuses = append(statuses, status)
	}

	return statuses, nil
}

// GetClientStatus returns the status of all supported MCP clients using the default config provider
func GetClientStatus(ctx context.Context) ([]ClientAppStatus, error) {
	manager, err := NewClientManager()
	if err != nil {
		return nil, err
	}
	return manager.GetClientStatus(ctx)
}

func buildConfigDirectoryPath(relPath []string, platformPrefix map[string][]string, path []string) string {
	if prefix, ok := platformPrefix[runtime.GOOS]; ok {
		path = append(path, prefix...)
	}
	path = append(path, relPath...)
	return filepath.Clean(filepath.Join(path...))
}

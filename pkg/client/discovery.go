package client

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// MCPClientStatus represents the status of a supported MCP client
type MCPClientStatus struct {
	// ClientType is the type of MCP client
	ClientType MCPClient `json:"client_type"`

	// Installed indicates whether the client is installed on the system
	Installed bool `json:"installed"`

	// Registered indicates whether the client is registered in the ToolHive configuration
	Registered bool `json:"registered"`
}

// GetClientStatus returns the installation status of all supported MCP clients
func GetClientStatus() ([]MCPClientStatus, error) {
	// Create a temporary manager to access config
	ctx := context.Background()
	manager, err := NewManager(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create manager: %w", err)
	}

	return GetClientStatusWithManager(manager)
}

// GetClientStatusWithManager returns the installation status of all supported MCP clients using the provided manager
func GetClientStatusWithManager(manager Manager) ([]MCPClientStatus, error) {
	var statuses []MCPClientStatus

	// Get home directory
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	// Get app configuration to check for registered clients
	appConfig, err := manager.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get config: %w", err)
	}

	registeredClients := make(map[string]bool)

	// Create a map of registered clients for quick lookup
	for _, client := range appConfig.Clients.RegisteredClients {
		registeredClients[client] = true
	}

	for _, cfg := range supportedClientIntegrations {
		status := MCPClientStatus{
			ClientType: cfg.ClientType,
			Installed:  false, // start with assuming client is not installed
			Registered: registeredClients[string(cfg.ClientType)],
		}

		// Determine path to check based on configuration
		var pathToCheck string
		if len(cfg.RelPath) == 0 {
			// If RelPath is empty, look at just the settings file
			pathToCheck = filepath.Join(home, cfg.SettingsFile)
		} else {
			// Otherwise build the directory path using RelPath
			pathToCheck = buildConfigDirectoryPath(cfg.RelPath, cfg.PlatformPrefix, []string{home})
		}

		// Check if the path exists
		if _, err := os.Stat(pathToCheck); err == nil {
			status.Installed = true
		}

		statuses = append(statuses, status)
	}

	return statuses, nil
}

func buildConfigDirectoryPath(relPath []string, platformPrefix map[string][]string, path []string) string {
	if prefix, ok := platformPrefix[runtime.GOOS]; ok {
		path = append(path, prefix...)
	}
	path = append(path, relPath...)
	return filepath.Clean(filepath.Join(path...))
}

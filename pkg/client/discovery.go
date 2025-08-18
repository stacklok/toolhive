package client

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/groups"
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
func GetClientStatus(ctx context.Context) ([]MCPClientStatus, error) {
	var statuses []MCPClientStatus

	// Get home directory
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	// Get app configuration to check for registered clients
	appConfig := config.GetConfig()
	registeredClients := make(map[string]bool)

	// Create a map of registered clients for quick lookup from global config
	for _, client := range appConfig.Clients.RegisteredClients {
		registeredClients[client] = true
	}

	// Also check for clients registered in groups
	groupManager, err := groups.NewManager()
	if err == nil { // If group manager fails to initialize, we'll just skip group checks
		allGroups, err := groupManager.List(ctx)
		if err == nil {
			// Collect clients from all groups
			for _, group := range allGroups {
				for _, clientName := range group.RegisteredClients {
					registeredClients[clientName] = true
				}
			}
		}
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

	// Sort statuses alphabetically by ClientType
	sort.Slice(statuses, func(i, j int) bool {
		return string(statuses[i].ClientType) < string(statuses[j].ClientType)
	})

	return statuses, nil
}

func buildConfigDirectoryPath(relPath []string, platformPrefix map[string][]string, path []string) string {
	if prefix, ok := platformPrefix[runtime.GOOS]; ok {
		path = append(path, prefix...)
	}
	path = append(path, relPath...)
	return filepath.Clean(filepath.Join(path...))
}

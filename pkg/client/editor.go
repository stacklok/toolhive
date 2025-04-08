package client

import (
	"fmt"

	"github.com/stacklok/toolhive/pkg/logger"
)

// ConfigEditor defines the interface for types which can edit MCP client config files.
type ConfigEditor interface {
	AddServer(config *ConfigFile, serverName, url string) error
	RemoveServer(config *ConfigFile, serverName string) error
}

// StandardConfigEditor edits the MCP client config format used by most clients.
type StandardConfigEditor struct{}

// AddServer inserts or updates a server in the MCP client config file.
func (*StandardConfigEditor) AddServer(config *ConfigFile, serverName, url string) error {
	// Get mcpServers object
	mcpServers, ok := config.Contents["mcpServers"]
	if !ok {
		// Create mcpServers object if it doesn't exist
		config.Contents["mcpServers"] = make(map[string]any)
		mcpServers = config.Contents["mcpServers"]
	}

	// Convert to map
	mcpServersMap, ok := mcpServers.(map[string]any)
	if !ok {
		return fmt.Errorf("mcpServers is not a map")
	}

	// Check if the server already exists
	existingConfig, exists := mcpServersMap[serverName]
	if exists {
		// Update only the URL field and preserve all other fields
		existingConfigMap, ok := existingConfig.(map[string]any)
		if ok {
			// Update the URL field
			existingConfigMap["url"] = url
			// Keep the existing config
			mcpServersMap[serverName] = existingConfigMap
		} else {
			// If the existing config is not a map, replace it
			mcpServersMap[serverName] = map[string]any{
				"url": url,
			}
		}
	} else {
		// Create a new server config
		mcpServersMap[serverName] = map[string]any{
			"url": url,
		}
	}

	return nil
}

// RemoveServer removes the specified MCP server from the Client config file.
func (*StandardConfigEditor) RemoveServer(config *ConfigFile, serverName string) error {
	// Get mcpServers object
	mcpServers, ok := config.Contents["mcpServers"]
	if !ok {
		// Create mcpServers object if it doesn't exist
		config.Contents["mcpServers"] = make(map[string]any)
		mcpServers = config.Contents["mcpServers"]
	}

	// Convert to map
	mcpServersMap, ok := mcpServers.(map[string]any)
	if !ok {
		return fmt.Errorf("mcpServers is not a map")
	}

	// Check if the server already exists
	_, exists := mcpServersMap[serverName]
	if exists {
		delete(mcpServersMap, serverName)
		logger.Log.Info(fmt.Sprintf("Removed MCP server %s from config file %s", serverName, config.Path))
	} else {
		logger.Log.Info(fmt.Sprintf("Nothing to do, MCP server %s was not found in config file %s", serverName, config.Path))
	}

	return nil
}

// VSCodeConfigEditor edits the MCP client config format used by VS Code.
type VSCodeConfigEditor struct{}

// AddServer inserts or updates a server in the MCP client config file.
func (*VSCodeConfigEditor) AddServer(config *ConfigFile, serverName, url string) error {
	// TODO: This pattern of "descend through JSON and apply a diff" can be generalized.
	// Get mcp object
	mcp, ok := config.Contents["mcp"]
	if !ok {
		// Create mcp object if it doesn't exist
		config.Contents["mcp"] = make(map[string]any)
		mcp = config.Contents["mcp"]
	}
	mcpMap := mcp.(map[string]any)

	// Get servers child object
	mcpServers, ok := mcpMap["servers"]
	if !ok {
		// Create servers object if it doesn't exist
		(config.Contents["mcp"].(map[string]any))["servers"] = make(map[string]any)
		mcpServers = (config.Contents["mcp"].(map[string]any))["servers"]
	}

	// Convert to map
	mcpServersMap, ok := mcpServers.(map[string]any)
	if !ok {
		return fmt.Errorf("mcpServers is not a map")
	}

	// Check if the server already exists
	existingConfig, exists := mcpServersMap[serverName]
	if exists {
		// Update only the URL field and preserve all other fields
		existingConfigMap, ok := existingConfig.(map[string]any)
		if ok {
			// Update the URL field
			existingConfigMap["url"] = url
			// Keep the existing config
			mcpServersMap[serverName] = existingConfigMap
		} else {
			// If the existing config is not a map, replace it
			mcpServersMap[serverName] = map[string]any{
				"url":  url,
				"type": "sse",
			}
		}
	} else {
		// Create a new server config
		mcpServersMap[serverName] = map[string]any{
			"url":  url,
			"type": "sse",
		}
	}

	return nil
}

// RemoveServer removes the specified MCP server from the Client config file (VSCode)
func (*VSCodeConfigEditor) RemoveServer(config *ConfigFile, serverName string) error {
	// TODO: This pattern of "descend through JSON and apply a diff" can be generalized.
	// Get mcp object
	mcp, ok := config.Contents["mcp"]
	if !ok {
		// Create mcp object if it doesn't exist
		config.Contents["mcp"] = make(map[string]any)
		mcp = config.Contents["mcp"]
	}
	mcpMap := mcp.(map[string]any)

	// Get servers child object
	mcpServers, ok := mcpMap["servers"]
	if !ok {
		// Create servers object if it doesn't exist
		(config.Contents["mcp"].(map[string]any))["servers"] = make(map[string]any)
		mcpServers = (config.Contents["mcp"].(map[string]any))["servers"]
	}

	// Convert to map
	mcpServersMap, ok := mcpServers.(map[string]any)
	if !ok {
		return fmt.Errorf("mcpServers is not a map")
	}

	// Check if the server already exists
	_, exists := mcpServersMap[serverName]
	if exists {
		delete(mcpServersMap, serverName)
		logger.Log.Info(fmt.Sprintf("Removed MCP server %s from config file %s", serverName, config.Path))
	} else {
		logger.Log.Info(fmt.Sprintf("Nothing to do, MCP server %s was not found in config file %s", serverName, config.Path))
	}

	return nil
}

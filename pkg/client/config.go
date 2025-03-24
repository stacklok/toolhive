// Package client provides utilities for managing client configurations
// and interacting with MCP servers.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"gopkg.in/yaml.v3"

	"github.com/stacklok/vibetool/pkg/transport"
)

// lockTimeout is the maximum time to wait for a file lock
const lockTimeout = 1 * time.Second

// YAML file extensions
const (
	YAMLExt = ".yaml"
	YMLExt  = ".yml"
)

// IsYAML checks if a file extension is a YAML extension
func IsYAML(ext string) bool {
	return ext == YAMLExt || ext == YMLExt
}

// configPaths defines the standard locations for client configuration files
var configPaths = []struct {
	Description string
	RelPath     []string
}{
	{
		Description: "VSCode Roo extension (Linux)",
		RelPath: []string{
			".config", "Code", "User", "globalStorage",
			"rooveterinaryinc.roo-cline", "settings", "cline_mcp_settings.json",
		},
	},
	{
		Description: "VSCode Roo extension (macOS)",
		RelPath: []string{
			"Library", "Application Support", "Code", "User", "globalStorage",
			"rooveterinaryinc.roo-cline", "settings", "cline_mcp_settings.json",
		},
	},
	{
		Description: "VSCode Claude extension (Linux)",
		RelPath: []string{
			".config", "Code", "User", "globalStorage",
			"saoudrizwan.claude-dev", "settings", "cline_mcp_settings.json",
		},
	},
	{
		Description: "VSCode Claude extension (macOS)",
		RelPath: []string{
			"Library", "Application Support", "Code", "User", "globalStorage",
			"saoudrizwan.claude-dev", "settings", "cline_mcp_settings.json",
		},
	},
	{
		Description: "Claude desktop app (Linux)",
		RelPath:     []string{".config", "Claude", "claude_desktop_config.json"},
	},
	{
		Description: "Claude desktop app (macOS)",
		RelPath:     []string{"Library", "Application Support", "Claude", "claude_desktop_config.json"},
	},
	{
		Description: "Continue config (YAML) - Linux",
		RelPath:     []string{".continue", "config.yaml"},
	},
	{
		Description: "Continue config (YAML) - macOS",
		RelPath:     []string{"Library", "Application Support", ".continue", "config.yaml"},
	},
	{
		Description: "Cursor editor (Linux/macOS)",
		RelPath:     []string{".cursor", "mcp.json"},
	},
	{
		Description: "Windsurf (Linux/macOS)",
		RelPath:     []string{".codeium", "windsurf", "mcp_config.json"},
	},
	// Add more paths as needed
}

// ConfigFile represents a client configuration file
type ConfigFile struct {
	Path     string
	Contents map[string]interface{}
}

// MCPServerConfig represents an MCP server configuration in a client config file
type MCPServerConfig struct {
	URL string `json:"url,omitempty"`
}

// FindClientConfigs searches for client configuration files in standard locations
func FindClientConfigs() ([]ConfigFile, error) {
	var configs []ConfigFile

	// Get home directory
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	// Check each path
	for _, pathInfo := range configPaths {
		// Construct the full path
		elements := append([]string{home}, pathInfo.RelPath...)
		path := filepath.Join(elements...)

		config, err := readConfigFile(path)
		if err == nil {
			configs = append(configs, config)
		}
	}

	return configs, nil
}

// readConfigFile reads and parses a client configuration file
func readConfigFile(path string) (ConfigFile, error) {
	// Check if file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return ConfigFile{}, fmt.Errorf("file does not exist: %s", path)
	}

	// Read file
	cleanpath := filepath.Clean(path)
	data, err := os.ReadFile(cleanpath)
	if err != nil {
		return ConfigFile{}, fmt.Errorf("failed to read file: %w", err)
	}

	// Determine format based on file extension
	var contents map[string]interface{}
	ext := strings.ToLower(filepath.Ext(path))

	if IsYAML(ext) {
		// Parse YAML
		if err := yaml.Unmarshal(data, &contents); err != nil {
			return ConfigFile{}, fmt.Errorf("failed to parse YAML: %w", err)
		}
	} else {
		// Default to JSON
		if err := json.Unmarshal(data, &contents); err != nil {
			return ConfigFile{}, fmt.Errorf("failed to parse JSON: %w", err)
		}
	}

	return ConfigFile{
		Path:     path,
		Contents: contents,
	}, nil
}

// isContinueConfig checks if the file is a Continue configuration file
func isContinueConfig(path string) bool {
	// Check if the file is in the .continue directory
	return strings.Contains(path, ".continue")
}

// UpdateMCPServerConfig updates the MCP server configuration in memory
// This does not save the changes to the file
func (c *ConfigFile) UpdateMCPServerConfig(serverName, url string) error {
	// Check if this is a Continue config file
	if isContinueConfig(c.Path) {
		ext := strings.ToLower(filepath.Ext(c.Path))
		if IsYAML(ext) {
			return c.updateContinueYAMLMCPServerConfig(serverName, url)
		}
	}

	// Default behavior for non-Continue configs or Continue JSON configs
	// Get mcpServers object
	mcpServers, ok := c.Contents["mcpServers"]
	if !ok {
		// Create mcpServers object if it doesn't exist
		c.Contents["mcpServers"] = make(map[string]interface{})
		mcpServers = c.Contents["mcpServers"]
	}

	// Convert to map
	mcpServersMap, ok := mcpServers.(map[string]interface{})
	if !ok {
		return fmt.Errorf("mcpServers is not a map")
	}

	// Check if the server already exists
	existingConfig, exists := mcpServersMap[serverName]
	if exists {
		// Update only the URL field and preserve all other fields
		existingConfigMap, ok := existingConfig.(map[string]interface{})
		if ok {
			// Update the URL field
			existingConfigMap["url"] = url
			// Keep the existing config
			mcpServersMap[serverName] = existingConfigMap
		} else {
			// If the existing config is not a map, replace it
			mcpServersMap[serverName] = map[string]interface{}{
				"url": url,
			}
		}
	} else {
		// Create a new server config
		mcpServersMap[serverName] = map[string]interface{}{
			"url": url,
		}
	}

	return nil
}

// updateContinueYAMLMCPServerConfig updates the MCP server configuration in Continue YAML format
// In Continue YAML, mcpServers is an array of objects with a name field
func (c *ConfigFile) updateContinueYAMLMCPServerConfig(serverName, url string) error {
	// Get mcpServers array
	mcpServers, ok := c.Contents["mcpServers"]
	if !ok {
		// Create mcpServers array if it doesn't exist
		c.Contents["mcpServers"] = []interface{}{}
		mcpServers = c.Contents["mcpServers"]
	}

	// Convert to array
	mcpServersArray, ok := mcpServers.([]interface{})
	if !ok {
		// If it's not an array, convert it to an array
		c.Contents["mcpServers"] = []interface{}{}
		mcpServersArray = c.Contents["mcpServers"].([]interface{})
	}

	// Look for the server by name
	serverFound := false
	for i, server := range mcpServersArray {
		serverMap, ok := server.(map[string]interface{})
		if !ok {
			continue
		}

		name, ok := serverMap["name"].(string)
		if !ok {
			continue
		}

		if name == serverName {
			// Update the URL field
			serverMap["url"] = url
			mcpServersArray[i] = serverMap
			serverFound = true
			break
		}
	}

	// If server not found, add it
	if !serverFound {
		mcpServersArray = append(mcpServersArray, map[string]interface{}{
			"name": serverName,
			"url":  url,
		})
		c.Contents["mcpServers"] = mcpServersArray
	}

	return nil
}

// Save writes the updated configuration back to the file without locking
// This is unsafe for concurrent access and should only be used in tests
func (c *ConfigFile) Save() error {
	// Determine format based on file extension
	ext := strings.ToLower(filepath.Ext(c.Path))

	var data []byte
	var err error

	if IsYAML(ext) {
		// Marshal YAML
		data, err = yaml.Marshal(c.Contents)
		if err != nil {
			return fmt.Errorf("failed to marshal YAML: %w", err)
		}
	} else {
		// Default to JSON
		data, err = json.MarshalIndent(c.Contents, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}
	}

	// Write file
	if err := os.WriteFile(c.Path, data, 0600); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}

// SaveWithLock safely updates the MCP server configuration in the file
// It acquires a lock, reads the latest content, applies the change, and saves the file
func (c *ConfigFile) SaveWithLock(serverName, url string) error {
	// Create a lock file
	fileLock := flock.New(c.Path + ".lock")

	// Create a context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), lockTimeout)
	defer cancel()

	// Try to acquire the lock with a timeout
	locked, err := fileLock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		return fmt.Errorf("failed to acquire lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("failed to acquire lock: timeout after %v", lockTimeout)
	}
	defer fileLock.Unlock()

	// Read the latest content from the file
	latestConfig, err := readConfigFile(c.Path)
	if err != nil {
		return fmt.Errorf("failed to read latest config: %w", err)
	}

	// Apply our change to the latest content
	if err := latestConfig.UpdateMCPServerConfig(serverName, url); err != nil {
		return fmt.Errorf("failed to update latest config: %w", err)
	}

	// Determine format based on file extension
	ext := strings.ToLower(filepath.Ext(c.Path))

	var data []byte

	if IsYAML(ext) {
		// Marshal YAML
		data, err = yaml.Marshal(latestConfig.Contents)
		if err != nil {
			return fmt.Errorf("failed to marshal YAML: %w", err)
		}
	} else {
		// Default to JSON
		data, err = json.MarshalIndent(latestConfig.Contents, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}
	}

	// Write file
	if err := os.WriteFile(c.Path, data, 0600); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	// Update our in-memory representation to match the file
	c.Contents = latestConfig.Contents

	return nil
}

// GenerateMCPServerURL generates the URL for an MCP server
func GenerateMCPServerURL(host string, port int, containerName string) string {
	// The URL format is: http://host:port/sse#container-name
	// Both SSE and STDIO transport types use an SSE proxy
	return fmt.Sprintf("http://%s:%d%s#%s", host, port, transport.HTTPSSEEndpoint, containerName)
}

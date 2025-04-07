// Package client provides utilities for managing client configurations
// and interacting with MCP servers.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"gopkg.in/yaml.v3"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/transport/ssecommon"
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

// TODO: This type could be removed with more refactoring.
type pathAndEditor struct {
	Path   string
	Editor ConfigEditor
}

// MCPClient is an enum of supported MCP clients.
type MCPClient string

const (
	// RooCode represents the RooCode extension for VSCode.
	RooCode MCPClient = "roo-code"
	// Cursor represents the Cursor editor.
	Cursor MCPClient = "cursor"
	// VSCodeInsider represents the VSCode Insider editor.
	VSCodeInsider MCPClient = "vscode-insider"
	// VSCode represents the standard VSCode editor.
	VSCode MCPClient = "vscode"
)

// mcpClientConfig represents a configuration path for a supported MCP client.
type mcpClientConfig struct {
	ClientType     MCPClient
	Description    string
	RelPath        []string
	PlatformPrefix map[string][]string
	Editor         ConfigEditor
}

var supportedClientIntegrations = []mcpClientConfig{
	{
		ClientType:  RooCode,
		Description: "VSCode Roo extension",
		RelPath: []string{
			"Code", "User", "globalStorage", "rooveterinaryinc.roo-cline", "settings", "mcp_settings.json",
		},
		PlatformPrefix: map[string][]string{
			"linux":  {".config"},
			"darwin": {"Library", "Application Support"},
		},
		Editor: &StandardConfigEditor{},
	},
	{
		ClientType:  VSCodeInsider,
		Description: "Visual Studio Code Insider",
		RelPath: []string{
			"Code - Insiders", "User", "settings.json",
		},
		PlatformPrefix: map[string][]string{
			"linux":  {".config"},
			"darwin": {"Library", "Application Support"},
		},
		Editor: &VSCodeConfigEditor{},
	},
	{
		ClientType:  VSCode,
		Description: "Visual Studio Code",
		RelPath: []string{
			"Code", "User", "settings.json",
		},
		PlatformPrefix: map[string][]string{
			"linux":  {".config"},
			"darwin": {"Library", "Application Support"},
		},
		Editor: &VSCodeConfigEditor{},
	},
	{
		ClientType:  Cursor,
		Description: "Cursor editor",
		RelPath:     []string{".cursor", "mcp.json"},
		Editor:      &StandardConfigEditor{},
	},
}

// ConfigFile represents a client configuration file
type ConfigFile struct {
	Path     string
	Contents map[string]interface{}
	Editor   ConfigEditor
}

// MCPServerConfig represents an MCP server configuration in a client config file
type MCPServerConfig struct {
	URL string `json:"url,omitempty"`
}

// FindClientConfigs searches for client configuration files in standard locations
func FindClientConfigs() ([]ConfigFile, error) {
	// Start by assuming all clients are enabled
	var filters []MCPClient
	appConfig := config.GetConfig()
	// If we are not using auto-discovery, we need to filter the set of clients to configure.
	if !appConfig.Clients.AutoDiscovery {
		if len(appConfig.Clients.RegisteredClients) > 0 {
			filters = make([]MCPClient, len(appConfig.Clients.RegisteredClients))
			for _, client := range appConfig.Clients.RegisteredClients {
				// Not validating client names here - assuming that A) they are
				// validated when set, and B) they will be dropped later if not valid.
				filters = append(filters, MCPClient(client))
			}
		} else {
			// No clients configured - exit early.
			return nil, nil
		}
	}

	// Get the set of paths we need to configure
	configPaths, err := getSupportedPaths(filters)
	if err != nil {
		return nil, fmt.Errorf("failed to get client config paths: %w", err)
	}

	var configs []ConfigFile
	// Check each path
	for _, pe := range configPaths {
		clientConfig, err := readConfigFile(pe.Path)
		if err == nil {
			// ugly hack, refactor away in future.
			clientConfig.Editor = pe.Editor
			configs = append(configs, clientConfig)
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

// UpdateMCPServerConfig updates the MCP server configuration in memory
// This does not save the changes to the file
func (c *ConfigFile) UpdateMCPServerConfig(serverName, url string) error {
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
func (c *ConfigFile) SaveWithLock(serverName, url string, editor ConfigEditor) error {
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
	if err := editor.AddServer(&latestConfig, serverName, url); err != nil {
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
	return fmt.Sprintf("http://%s:%d%s#%s", host, port, ssecommon.HTTPSSEEndpoint, containerName)
}

func getSupportedPaths(filters []MCPClient) ([]pathAndEditor, error) {
	var paths []pathAndEditor

	// Get home directory
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	for _, cfg := range supportedClientIntegrations {
		// If filters are specified, filter out the clients we don't care about.
		if len(filters) > 0 && !slices.Contains(filters, cfg.ClientType) {
			continue
		}
		path := []string{home}
		if prefix, ok := cfg.PlatformPrefix[runtime.GOOS]; ok {
			path = append(path, prefix...)
		}
		path = append(path, cfg.RelPath...)
		paths = append(paths, pathAndEditor{
			Path:   filepath.Join(path...),
			Editor: cfg.Editor,
		})
	}

	return paths, nil
}

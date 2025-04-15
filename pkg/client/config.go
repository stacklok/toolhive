// Package client provides utilities for managing client configurations
// and interacting with MCP servers.
package client

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/tailscale/hujson"
	"gopkg.in/yaml.v3"

	"github.com/StacklokLabs/toolhive/pkg/config"
	"github.com/StacklokLabs/toolhive/pkg/logger"
	"github.com/StacklokLabs/toolhive/pkg/transport/ssecommon"
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
	Path                 string
	MCPServersPathPrefix string
	ClientType           MCPClient
}

// MCPClient is an enum of supported MCP clients.
type MCPClient string

const (
	// RooCode represents the Roo Code extension for VS Code.
	RooCode MCPClient = "roo-code"
	// Cursor represents the Cursor editor.
	Cursor MCPClient = "cursor"
	// VSCodeInsider represents the VS Code Insiders editor.
	VSCodeInsider MCPClient = "vscode-insider"
	// VSCode represents the standard VS Code editor.
	VSCode MCPClient = "vscode"
)

// mcpClientConfig represents a configuration path for a supported MCP client.
type mcpClientConfig struct {
	ClientType           MCPClient
	Description          string
	RelPath              []string
	PlatformPrefix       map[string][]string
	MCPServersPathPrefix string
}

var supportedClientIntegrations = []mcpClientConfig{
	{
		ClientType:  RooCode,
		Description: "VS Code Roo Code extension",
		RelPath: []string{
			"Code", "User", "globalStorage", "rooveterinaryinc.roo-cline", "settings", "mcp_settings.json",
		},
		PlatformPrefix: map[string][]string{
			"linux":  {".config"},
			"darwin": {"Library", "Application Support"},
		},
		MCPServersPathPrefix: "/mcpServers",
	},
	{
		ClientType:  VSCodeInsider,
		Description: "Visual Studio Code Insiders",
		RelPath: []string{
			"Code - Insiders", "User", "settings.json",
		},
		PlatformPrefix: map[string][]string{
			"linux":  {".config"},
			"darwin": {"Library", "Application Support"},
		},
		MCPServersPathPrefix: "/mcp/servers",
	},
	{
		ClientType:  VSCode,
		Description: "Visual Studio Code",
		RelPath: []string{
			"Code", "User", "settings.json",
		},
		MCPServersPathPrefix: "/mcp/servers",
		PlatformPrefix: map[string][]string{
			"linux":  {".config"},
			"darwin": {"Library", "Application Support"},
		},
	},
	{
		ClientType:           Cursor,
		Description:          "Cursor editor",
		MCPServersPathPrefix: "/mcpServers",
		RelPath:              []string{".cursor", "mcp.json"},
	},
}

// ConfigFile represents a client configuration file
type ConfigFile struct {
	Path                 string
	ClientType           MCPClient
	Contents             map[string]interface{}
	ConfigUpdater        ConfigUpdater
	MCPServersPathPrefix string
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
		// TODO: This is a bit of a hack to get the client type into the ConfigFile
		// object. We should probably refactor this to be more elegant.
		// We can also rename the `readConfigFile` function so that it expresses that it
		// only retrieves client config file metadata, not the contents.
		clientConfig, err := readConfigFile(pe.Path, pe.MCPServersPathPrefix)
		clientConfig.ClientType = pe.ClientType
		if err == nil {
			configs = append(configs, clientConfig)
		}

		if err != nil {
			logger.Log.Error(fmt.Sprintf("Error reading client config file %s: %v", pe.Path, err))
		}
	}

	return configs, nil
}

// Upsert updates/inserts an MCP server in a client configuration file
// It is a wrapper around the ConfigUpdater.Upsert method. Because the
// ConfigUpdater is different for each client type, we need to handle
// the different types of McpServer objects. For example, VSCode allows
// for a `type` field, but Cursor and others do not. This allows us to
// build up more complex MCP server configurations for different clients
// without leaking them into the CMD layer.
func Upsert(cf ConfigFile, name string, url string) error {
	if cf.ClientType == VSCode || cf.ClientType == VSCodeInsider {
		return cf.ConfigUpdater.Upsert(name, MCPServer{Url: url, Type: "sse"})
	}

	return cf.ConfigUpdater.Upsert(name, MCPServer{Url: url})
}

// readConfigFile reads and parses a client configuration file
func readConfigFile(path, mcpServersPathPrefix string) (ConfigFile, error) {
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
	var configUpdater ConfigUpdater

	if IsYAML(ext) {
		// Parse YAML
		if err := yaml.Unmarshal(data, &contents); err != nil {
			return ConfigFile{}, fmt.Errorf("failed to parse YAML: %w", err)
		}
	} else {
		// Default to JSON
		_, err := hujson.Parse(data)
		if err != nil {
			return ConfigFile{}, fmt.Errorf("failed to parse JSON: %w", err)
		}
		configUpdater = &JSONConfigUpdater{Path: cleanpath, MCPServersPathPrefix: mcpServersPathPrefix}
	}

	return ConfigFile{
		Path:          path,
		Contents:      contents,
		ConfigUpdater: configUpdater,
	}, nil
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
		// TODO: This is a bit of a hack to get the client type into the pathAndEditor
		// object. We should probably refactor this to be more elegant.
		paths = append(paths, pathAndEditor{
			Path:                 filepath.Join(path...),
			MCPServersPathPrefix: cfg.MCPServersPathPrefix,
			ClientType:           cfg.ClientType,
		})
	}

	return paths, nil
}

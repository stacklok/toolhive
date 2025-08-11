// Package client provides utilities for managing client configurations
// and interacting with MCP servers.
package client

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/tailscale/hujson"
	"go.uber.org/zap"

	"github.com/stacklok/toolhive/pkg/transport/types"
)

// lockTimeout is the maximum time to wait for a file lock
const lockTimeout = 1 * time.Second

// MCPClient is an enum of supported MCP clients.
type MCPClient string

const (
	// RooCode represents the Roo Code extension for VS Code.
	RooCode MCPClient = "roo-code"
	// Cline represents the Cline extension for VS Code.
	Cline MCPClient = "cline"
	// Cursor represents the Cursor editor.
	Cursor MCPClient = "cursor"
	// VSCodeInsider represents the VS Code Insiders editor.
	VSCodeInsider MCPClient = "vscode-insider"
	// VSCode represents the standard VS Code editor.
	VSCode MCPClient = "vscode"
	// ClaudeCode represents the Claude Code CLI.
	ClaudeCode MCPClient = "claude-code"
	// Windsurf represents the Windsurf IDE.
	Windsurf MCPClient = "windsurf"
	// WindsurfJetBrains represents the Windsurf plugin for JetBrains.
	WindsurfJetBrains MCPClient = "windsurf-jetbrains"
	// AmpCli represents the Sourcegraph Amp CLI.
	AmpCli MCPClient = "amp-cli"
	// AmpVSCode represents the Sourcegraph Amp extension for VS Code.
	AmpVSCode MCPClient = "amp-vscode"
	// AmpCursor represents the Sourcegraph Amp extension for Cursor.
	AmpCursor MCPClient = "amp-cursor"
	// AmpVSCodeInsider represents the Sourcegraph Amp extension for VS Code Insiders.
	AmpVSCodeInsider MCPClient = "amp-vscode-insider"
	// AmpWindsurf represents the Sourcegraph Amp extension for Windsurf.
	AmpWindsurf MCPClient = "amp-windsurf"
)

// Extension is extension of the client config file.
type Extension string

const (
	// JSON represents a JSON extension.
	JSON Extension = "json"
)

// mcpClientConfig represents a configuration path for a supported MCP client.
type mcpClientConfig struct {
	ClientType                    MCPClient
	Description                   string
	RelPath                       []string
	SettingsFile                  string
	PlatformPrefix                map[string][]string
	MCPServersPathPrefix          string
	Extension                     Extension
	SupportedTransportTypesMap    map[types.TransportType]string // stdio should be mapped to sse
	IsTransportTypeFieldSupported bool
	MCPServersUrlLabel            string
}

var (
	// ErrConfigFileNotFound is returned when a client configuration file is not found
	ErrConfigFileNotFound = fmt.Errorf("client config file not found")
)

var supportedClientIntegrations = []mcpClientConfig{
	{
		ClientType:   RooCode,
		Description:  "VS Code Roo Code extension",
		SettingsFile: "mcp_settings.json",
		RelPath: []string{
			"Code", "User", "globalStorage", "rooveterinaryinc.roo-cline", "settings",
		},
		PlatformPrefix: map[string][]string{
			"linux":   {".config"},
			"darwin":  {"Library", "Application Support"},
			"windows": {"AppData", "Roaming"},
		},
		MCPServersPathPrefix: "/mcpServers",
		Extension:            JSON,
		SupportedTransportTypesMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "sse",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "streamable-http",
		},
		IsTransportTypeFieldSupported: true,
		MCPServersUrlLabel:            "url",
	},
	{
		ClientType:   Cline,
		Description:  "VS Code Cline extension",
		SettingsFile: "cline_mcp_settings.json",
		RelPath: []string{
			"Code", "User", "globalStorage", "saoudrizwan.claude-dev", "settings",
		},
		PlatformPrefix: map[string][]string{
			"linux":   {".config"},
			"darwin":  {"Library", "Application Support"},
			"windows": {"AppData", "Roaming"},
		},
		MCPServersPathPrefix: "/mcpServers",
		Extension:            JSON,
		SupportedTransportTypesMap: map[types.TransportType]string{
			types.TransportTypeSSE:   "sse",
			types.TransportTypeStdio: "sse",
		},
		IsTransportTypeFieldSupported: false,
		MCPServersUrlLabel:            "url",
	},
	{
		ClientType:   VSCodeInsider,
		Description:  "Visual Studio Code Insiders",
		SettingsFile: "mcp.json",
		RelPath: []string{
			"Code - Insiders", "User",
		},
		PlatformPrefix: map[string][]string{
			"linux":   {".config"},
			"darwin":  {"Library", "Application Support"},
			"windows": {"AppData", "Roaming"},
		},
		MCPServersPathPrefix: "/servers",
		Extension:            JSON,
		SupportedTransportTypesMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "sse",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "http",
		},
		IsTransportTypeFieldSupported: true,
		MCPServersUrlLabel:            "url",
	},
	{
		ClientType:   VSCode,
		Description:  "Visual Studio Code",
		SettingsFile: "mcp.json",
		RelPath: []string{
			"Code", "User",
		},
		MCPServersPathPrefix: "/servers",
		PlatformPrefix: map[string][]string{
			"linux":   {".config"},
			"darwin":  {"Library", "Application Support"},
			"windows": {"AppData", "Roaming"},
		},
		Extension: JSON,
		SupportedTransportTypesMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "sse",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "http",
		},
		IsTransportTypeFieldSupported: true,
		MCPServersUrlLabel:            "url",
	},
	{
		ClientType:           Cursor,
		Description:          "Cursor editor",
		SettingsFile:         "mcp.json",
		MCPServersPathPrefix: "/mcpServers",
		RelPath:              []string{".cursor"},
		Extension:            JSON,
		SupportedTransportTypesMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "sse",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "http",
		},
		// Adding type field is not explicitly required though, Cursor auto-detects and is able to
		// connect to both sse and streamable-http types
		IsTransportTypeFieldSupported: true,
		MCPServersUrlLabel:            "url",
	},
	{
		ClientType:           ClaudeCode,
		Description:          "Claude Code CLI",
		SettingsFile:         ".claude.json",
		MCPServersPathPrefix: "/mcpServers",
		RelPath:              []string{},
		Extension:            JSON,
		SupportedTransportTypesMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "sse",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "http",
		},
		IsTransportTypeFieldSupported: true,
		MCPServersUrlLabel:            "url",
	},
	{
		ClientType:           Windsurf,
		Description:          "Windsurf IDE",
		SettingsFile:         "mcp_config.json",
		MCPServersPathPrefix: "/mcpServers",
		RelPath:              []string{".codeium", "windsurf"},
		Extension:            JSON,
		SupportedTransportTypesMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "sse",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "http",
		},
		IsTransportTypeFieldSupported: true,
		MCPServersUrlLabel:            "serverUrl",
	},
	{
		ClientType:           WindsurfJetBrains,
		Description:          "Windsurf plugin for JetBrains IDEs",
		SettingsFile:         "mcp_config.json",
		MCPServersPathPrefix: "/mcpServers",
		RelPath:              []string{".codeium"},
		Extension:            JSON,
		SupportedTransportTypesMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "sse",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "http",
		},
		IsTransportTypeFieldSupported: true,
		MCPServersUrlLabel:            "serverUrl",
	},
	{
		ClientType:           AmpCli,
		Description:          "Sourcegraph Amp CLI",
		SettingsFile:         "settings.json",
		MCPServersPathPrefix: "/amp.mcpServers",
		RelPath:              []string{"amp"},
		PlatformPrefix: map[string][]string{
			"linux":   {".config"},
			"darwin":  {".config"},
			"windows": {"AppData", "Roaming"},
		},
		Extension: JSON,
		SupportedTransportTypesMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "sse",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "http",
		},
		IsTransportTypeFieldSupported: true,
	},
	{
		ClientType:           AmpVSCode,
		Description:          "VS Code Sourcegraph Amp extension",
		SettingsFile:         "settings.json",
		MCPServersPathPrefix: "/amp.mcpServers",
		RelPath:              []string{"Code", "User"},
		PlatformPrefix: map[string][]string{
			"linux":   {".config"},
			"darwin":  {"Library", "Application Support"},
			"windows": {"AppData", "Roaming"},
		},
		Extension: JSON,
		SupportedTransportTypesMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "sse",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "http",
		},
		IsTransportTypeFieldSupported: true,
	},
	{
		ClientType:           AmpVSCodeInsider,
		Description:          "VS Code Insiders Sourcegraph Amp extension",
		SettingsFile:         "settings.json",
		MCPServersPathPrefix: "/amp.mcpServers",
		RelPath:              []string{"Code - Insiders", "User"},
		PlatformPrefix: map[string][]string{
			"linux":   {".config"},
			"darwin":  {"Library", "Application Support"},
			"windows": {"AppData", "Roaming"},
		},
		Extension: JSON,
		SupportedTransportTypesMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "sse",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "http",
		},
		IsTransportTypeFieldSupported: true,
	},
	{
		ClientType:           AmpCursor,
		Description:          "Cursor Sourcegraph Amp extension",
		SettingsFile:         "settings.json",
		MCPServersPathPrefix: "/amp.mcpServers",
		RelPath:              []string{"Cursor", "User"},
		PlatformPrefix: map[string][]string{
			"linux":   {".config"},
			"darwin":  {"Library", "Application Support"},
			"windows": {"AppData", "Roaming"},
		},
		Extension: JSON,
		SupportedTransportTypesMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "sse",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "http",
		},
		IsTransportTypeFieldSupported: true,
	},
	{
		ClientType:           AmpWindsurf,
		Description:          "Windsurf Sourcegraph Amp extension",
		SettingsFile:         "settings.json",
		MCPServersPathPrefix: "/amp.mcpServers",
		RelPath:              []string{"Windsurf", "User"},
		PlatformPrefix: map[string][]string{
			"linux":   {".config"},
			"darwin":  {"Library", "Application Support"},
			"windows": {"AppData", "Roaming"},
		},
		Extension: JSON,
		SupportedTransportTypesMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "sse",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "http",
		},
		IsTransportTypeFieldSupported: true,
	},
}

// ConfigFile represents a client configuration file
type ConfigFile struct {
	Path          string
	ClientType    MCPClient
	ConfigUpdater ConfigUpdater
	Extension     Extension
}

// MCPServerConfig represents an MCP server configuration in a client config file
type MCPServerConfig struct {
	URL string `json:"url,omitempty"`
}

// FindClientConfig returns the client configuration file for a given client type.
func FindClientConfig(clientType MCPClient, logger *zap.SugaredLogger) (*ConfigFile, error) {
	// retrieve the metadata of the config files
	configFile, err := retrieveConfigFileMetadata(clientType, logger)
	if err != nil {
		if errors.Is(err, ErrConfigFileNotFound) {
			// Propagate the error if the file is not found
			return nil, fmt.Errorf("%w for client %s", ErrConfigFileNotFound, clientType)
		}
		return nil, err
	}

	// validate the format of the config files
	err = validateConfigFileFormat(configFile)
	if err != nil {
		return nil, fmt.Errorf("failed to validate config file format: %w", err)
	}
	return configFile, nil
}

// FindRegisteredClientConfigs finds all registered client configs and creates them if they don't exist.
func FindRegisteredClientConfigs(logger *zap.SugaredLogger) ([]ConfigFile, error) {
	clientStatuses, err := GetClientStatus(logger)
	if err != nil {
		return nil, fmt.Errorf("failed to get client status: %w", err)
	}

	var configFiles []ConfigFile
	for _, clientStatus := range clientStatuses {
		if !clientStatus.Installed || !clientStatus.Registered {
			continue
		}
		cf, err := FindClientConfig(clientStatus.ClientType, logger)
		if err != nil {
			if errors.Is(err, ErrConfigFileNotFound) {
				logger.Infof("Client config file not found for %s, creating it...", clientStatus.ClientType)
				cf, err = CreateClientConfig(clientStatus.ClientType, logger)
				if err != nil {
					logger.Warnf("Unable to create client config for %s: %v", clientStatus.ClientType, err)
					continue
				}
				logger.Infof("Successfully created client config file for %s", clientStatus.ClientType)
			} else {
				logger.Warnf("Unable to process client config for %s: %v", clientStatus.ClientType, err)
				continue
			}
		}
		configFiles = append(configFiles, *cf)
	}

	return configFiles, nil
}

// CreateClientConfig creates a new client configuration file for a given client type.
func CreateClientConfig(clientType MCPClient, logger *zap.SugaredLogger) (*ConfigFile, error) {
	// Get home directory
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	// Find the configuration for the requested client type
	var clientCfg *mcpClientConfig
	for _, cfg := range supportedClientIntegrations {
		if cfg.ClientType == clientType {
			clientCfg = &cfg
			break
		}
	}

	if clientCfg == nil {
		return nil, fmt.Errorf("unsupported client type: %s", clientType)
	}

	// Build the path to the configuration file
	path := buildConfigFilePath(clientCfg.SettingsFile, clientCfg.RelPath, clientCfg.PlatformPrefix, []string{home})

	// Validate that the file does not already exist
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		return nil, fmt.Errorf("client config file already exists at %s", path)
	}

	// Create the file if it does not exist
	logger.Infof("Creating new client config file at %s", path)
	err = os.WriteFile(path, []byte("{}"), 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to create client config file: %w", err)
	}

	return FindClientConfig(clientType, logger)
}

// Upsert updates/inserts an MCP server in a client configuration file
// It is a wrapper around the ConfigUpdater.Upsert method. Because the
// ConfigUpdater is different for each client type, we need to handle
// the different types of McpServer objects. For example, VSCode and ClaudeCode allows
// for a `type` field, but Cursor and others do not. This allows us to
// build up more complex MCP server configurations for different clients
// without leaking them into the CMD layer.
func Upsert(cf ConfigFile, name string, url string, transportType string) error {
	for i := range supportedClientIntegrations {
		if cf.ClientType != supportedClientIntegrations[i].ClientType {
			continue
		}
		isServerUrl := supportedClientIntegrations[i].MCPServersUrlLabel == "serverUrl"
		mappedTransportType, ok := supportedClientIntegrations[i].SupportedTransportTypesMap[types.TransportType(transportType)]
		if supportedClientIntegrations[i].IsTransportTypeFieldSupported && ok {
			if isServerUrl {
				return cf.ConfigUpdater.Upsert(name, MCPServer{ServerUrl: url, Type: mappedTransportType})
			}
			return cf.ConfigUpdater.Upsert(name, MCPServer{Url: url, Type: mappedTransportType})
		}
		if isServerUrl {
			return cf.ConfigUpdater.Upsert(name, MCPServer{ServerUrl: url})
		}
		return cf.ConfigUpdater.Upsert(name, MCPServer{Url: url})
	}
	return nil
}

// retrieveConfigFileMetadata retrieves the metadata for client configuration files for a given client type.
func retrieveConfigFileMetadata(clientType MCPClient, logger *zap.SugaredLogger) (*ConfigFile, error) {
	// Get home directory
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	// Find the configuration for the requested client type
	var clientCfg *mcpClientConfig
	for _, cfg := range supportedClientIntegrations {
		if cfg.ClientType == clientType {
			clientCfg = &cfg
			break
		}
	}

	if clientCfg == nil {
		return nil, fmt.Errorf("unsupported client type: %s", clientType)
	}

	// Build the path to the configuration file
	path := buildConfigFilePath(clientCfg.SettingsFile, clientCfg.RelPath, clientCfg.PlatformPrefix, []string{home})

	// Validate that the file exists
	if err := validateConfigFileExists(path); err != nil {
		return nil, err
	}

	// Create a config updater for this file
	configUpdater := &JSONConfigUpdater{
		Path:                 path,
		MCPServersPathPrefix: clientCfg.MCPServersPathPrefix,
		logger:               logger,
	}

	// Return the configuration file metadata
	return &ConfigFile{
		Path:          path,
		ConfigUpdater: configUpdater,
		ClientType:    clientCfg.ClientType,
		Extension:     clientCfg.Extension,
	}, nil
}

func buildConfigFilePath(settingsFile string, relPath []string, platformPrefix map[string][]string, path []string) string {
	if prefix, ok := platformPrefix[runtime.GOOS]; ok {
		path = append(path, prefix...)
	}
	path = append(path, relPath...)
	path = append(path, settingsFile)
	return filepath.Clean(filepath.Join(path...))
}

// validateConfigFileExists validates that a client configuration file exists.
func validateConfigFileExists(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return ErrConfigFileNotFound
	}
	return nil
}

func validateConfigFileFormat(cf *ConfigFile) error {
	data, err := os.ReadFile(cf.Path)
	if err != nil {
		return fmt.Errorf("failed to read file %s: %w", cf.Path, err)
	}

	if len(data) == 0 {
		data = []byte("{}") // Default to an empty JSON object if the file is empty
	}

	// Default to JSON
	// we don't care about the contents of the file, we just want to validate that it's valid JSON
	_, err = hujson.Parse(data)
	if err != nil {
		return fmt.Errorf("failed to parse JSON for file %s: %w", cf.Path, err)
	}
	return nil
}

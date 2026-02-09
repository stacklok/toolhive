// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package client provides utilities for managing client configurations
// and interacting with MCP servers.
package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/tailscale/hujson"
	"gopkg.in/yaml.v3"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// lockTimeout is the maximum time to wait for a file lock
const lockTimeout = 1 * time.Second

// defaultURLFieldName is the default URL field name used when no specific mapping exists
const defaultURLFieldName = "url"

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
	// LMStudio represents the LM Studio application.
	LMStudio MCPClient = "lm-studio"
	// Goose represents the Goose AI agent.
	Goose MCPClient = "goose"
	// Trae represents the Trae IDE.
	Trae MCPClient = "trae"
	// Continue represents the Continue.dev IDE plugins.
	Continue MCPClient = "continue"
	// OpenCode represents the OpenCode editor.
	OpenCode MCPClient = "opencode"
	// Kiro represents the Kiro AI IDE.
	Kiro MCPClient = "kiro"
	// Antigravity represents the Google Antigravity IDE.
	Antigravity MCPClient = "antigravity"
	// Zed represents the Zed editor.
	Zed MCPClient = "zed"
	// GeminiCli represents the Google Gemini CLI.
	GeminiCli MCPClient = "gemini-cli"
	// VSCodeServer represents Microsoft's VS Code Server (remote development).
	VSCodeServer MCPClient = "vscode-server"
	// MistralVibe represents the Mistral Vibe IDE.
	MistralVibe MCPClient = "mistral-vibe"
	// Codex represents the OpenAI Codex CLI.
	Codex MCPClient = "codex"
)

// Extension is extension of the client config file.
type Extension string

const (
	// JSON represents a JSON extension.
	JSON Extension = "json"
	// YAML represents a YAML extension.
	YAML Extension = "yaml"
	// TOML represents a TOML extension.
	TOML Extension = "toml"
)

// YAMLStorageType represents how servers are stored in YAML configuration files.
type YAMLStorageType string

const (
	// YAMLStorageTypeMap represents servers stored as a map with server names as keys.
	YAMLStorageTypeMap YAMLStorageType = "map"
	// YAMLStorageTypeArray represents servers stored as an array of objects.
	YAMLStorageTypeArray YAMLStorageType = "array"
)

// TOMLStorageType represents how servers are stored in TOML configuration files.
type TOMLStorageType string

const (
	// TOMLStorageTypeMap represents servers stored as nested tables [section.servername].
	// Example: [mcp_servers.myserver]
	TOMLStorageTypeMap TOMLStorageType = "map"
	// TOMLStorageTypeArray represents servers stored as array of tables [[section]].
	// Example: [[mcp_servers]]
	TOMLStorageTypeArray TOMLStorageType = "array"
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
	SupportedTransportTypesMap    map[types.TransportType]string // stdio mapped to streamable-http (SSE deprecated)
	IsTransportTypeFieldSupported bool
	// MCPServersUrlLabelMap maps transport type to URL field name (e.g., "url", "serverUrl", "httpUrl")
	MCPServersUrlLabelMap map[types.TransportType]string
	// YAML-specific configuration (only used when Extension == YAML)
	YAMLStorageType     YAMLStorageType // How servers are stored in YAML (map or array)
	YAMLIdentifierField string          // For array type: field name that identifies the server
	YAMLDefaults        map[string]any  // Default values to add to entries
	// TOML-specific configuration (only used when Extension == TOML)
	TOMLStorageType TOMLStorageType // How servers are stored in TOML (map or array)
}

// extractServersKeyFromConfig extracts the servers key from MCPServersPathPrefix
// by removing the leading "/" (e.g., "/mcpServers" -> "mcpServers").
func extractServersKeyFromConfig(cfg *mcpClientConfig) string {
	return strings.TrimPrefix(cfg.MCPServersPathPrefix, "/")
}

// extractURLLabelFromConfig extracts the URL field label from MCPServersUrlLabelMap.
// It checks transport types in priority order: StreamableHTTP, then Stdio.
// Returns defaultURLFieldName if no mapping is found.
func extractURLLabelFromConfig(cfg *mcpClientConfig) string {
	if cfg.MCPServersUrlLabelMap != nil {
		if label, ok := cfg.MCPServersUrlLabelMap[types.TransportTypeStreamableHTTP]; ok {
			return label
		}
		if label, ok := cfg.MCPServersUrlLabelMap[types.TransportTypeStdio]; ok {
			return label
		}
	}
	return defaultURLFieldName
}

var (
	// ErrConfigFileNotFound is returned when a client configuration file is not found
	ErrConfigFileNotFound = fmt.Errorf("client config file not found")
	// ErrUnsupportedClientType is returned when an unsupported client type is provided
	ErrUnsupportedClientType = fmt.Errorf("unsupported client type")
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
			types.TransportTypeStdio:          "streamable-http",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "streamable-http",
		},
		IsTransportTypeFieldSupported: true,
		MCPServersUrlLabelMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "url",
			types.TransportTypeSSE:            "url",
			types.TransportTypeStreamableHTTP: "url",
		},
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
			types.TransportTypeStdio:          "streamableHttp",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "streamableHttp",
		},
		IsTransportTypeFieldSupported: true,
		MCPServersUrlLabelMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "url",
			types.TransportTypeSSE:            "url",
			types.TransportTypeStreamableHTTP: "url",
		},
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
			types.TransportTypeStdio:          "http",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "http",
		},
		IsTransportTypeFieldSupported: true,
		MCPServersUrlLabelMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "url",
			types.TransportTypeSSE:            "url",
			types.TransportTypeStreamableHTTP: "url",
		},
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
			types.TransportTypeStdio:          "http",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "http",
		},
		IsTransportTypeFieldSupported: true,
		MCPServersUrlLabelMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "url",
			types.TransportTypeSSE:            "url",
			types.TransportTypeStreamableHTTP: "url",
		},
	},
	{
		ClientType:           Cursor,
		Description:          "Cursor editor",
		SettingsFile:         "mcp.json",
		MCPServersPathPrefix: "/mcpServers",
		RelPath:              []string{".cursor"},
		Extension:            JSON,
		SupportedTransportTypesMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "http",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "http",
		},
		// Adding type field is not explicitly required though, Cursor auto-detects and is able to
		// connect to both sse and streamable-http types
		IsTransportTypeFieldSupported: true,
		MCPServersUrlLabelMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "url",
			types.TransportTypeSSE:            "url",
			types.TransportTypeStreamableHTTP: "url",
		},
	},
	{
		ClientType:           ClaudeCode,
		Description:          "Claude Code CLI",
		SettingsFile:         ".claude.json",
		MCPServersPathPrefix: "/mcpServers",
		RelPath:              []string{},
		Extension:            JSON,
		SupportedTransportTypesMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "http",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "http",
		},
		IsTransportTypeFieldSupported: true,
		MCPServersUrlLabelMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "url",
			types.TransportTypeSSE:            "url",
			types.TransportTypeStreamableHTTP: "url",
		},
	},
	{
		ClientType:           Windsurf,
		Description:          "Windsurf IDE",
		SettingsFile:         "mcp_config.json",
		MCPServersPathPrefix: "/mcpServers",
		RelPath:              []string{".codeium", "windsurf"},
		Extension:            JSON,
		SupportedTransportTypesMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "http",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "http",
		},
		IsTransportTypeFieldSupported: true,
		MCPServersUrlLabelMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "serverUrl",
			types.TransportTypeSSE:            "serverUrl",
			types.TransportTypeStreamableHTTP: "serverUrl",
		},
	},
	{
		ClientType:           WindsurfJetBrains,
		Description:          "Windsurf plugin for JetBrains IDEs",
		SettingsFile:         "mcp_config.json",
		MCPServersPathPrefix: "/mcpServers",
		RelPath:              []string{".codeium"},
		Extension:            JSON,
		SupportedTransportTypesMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "http",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "http",
		},
		IsTransportTypeFieldSupported: true,
		MCPServersUrlLabelMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "serverUrl",
			types.TransportTypeSSE:            "serverUrl",
			types.TransportTypeStreamableHTTP: "serverUrl",
		},
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
			types.TransportTypeStdio:          "http",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "http",
		},
		IsTransportTypeFieldSupported: true,
		MCPServersUrlLabelMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "url",
			types.TransportTypeSSE:            "url",
			types.TransportTypeStreamableHTTP: "url",
		},
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
			types.TransportTypeStdio:          "http",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "http",
		},
		IsTransportTypeFieldSupported: true,
		MCPServersUrlLabelMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "url",
			types.TransportTypeSSE:            "url",
			types.TransportTypeStreamableHTTP: "url",
		},
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
			types.TransportTypeStdio:          "http",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "http",
		},
		IsTransportTypeFieldSupported: true,
		MCPServersUrlLabelMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "url",
			types.TransportTypeSSE:            "url",
			types.TransportTypeStreamableHTTP: "url",
		},
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
			types.TransportTypeStdio:          "http",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "http",
		},
		IsTransportTypeFieldSupported: true,
		MCPServersUrlLabelMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "url",
			types.TransportTypeSSE:            "url",
			types.TransportTypeStreamableHTTP: "url",
		},
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
			types.TransportTypeStdio:          "http",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "http",
		},
		IsTransportTypeFieldSupported: true,
		MCPServersUrlLabelMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "url",
			types.TransportTypeSSE:            "url",
			types.TransportTypeStreamableHTTP: "url",
		},
	},
	{
		ClientType:           LMStudio,
		Description:          "LM Studio application",
		SettingsFile:         "mcp.json",
		MCPServersPathPrefix: "/mcpServers",
		RelPath:              []string{".lmstudio"},
		Extension:            JSON,
		SupportedTransportTypesMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "http",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "http",
		},
		IsTransportTypeFieldSupported: true,
		MCPServersUrlLabelMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "url",
			types.TransportTypeSSE:            "url",
			types.TransportTypeStreamableHTTP: "url",
		},
	},
	{
		ClientType:           Goose,
		Description:          "Goose AI agent",
		SettingsFile:         "config.yaml",
		MCPServersPathPrefix: "/extensions",
		RelPath:              []string{"goose"},
		PlatformPrefix: map[string][]string{
			"linux":   {".config"},
			"darwin":  {".config"},
			"windows": {"AppData", "Block"},
		},
		Extension: YAML,
		SupportedTransportTypesMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "streamable_http",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "streamable_http",
		},
		IsTransportTypeFieldSupported: true,
		MCPServersUrlLabelMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "uri",
			types.TransportTypeSSE:            "uri",
			types.TransportTypeStreamableHTTP: "uri",
		},
		// YAML configuration
		YAMLStorageType: YAMLStorageTypeMap,
		YAMLDefaults: map[string]interface{}{
			"enabled":     true,
			"timeout":     60,
			"description": "",
		},
	},
	{
		ClientType:           Trae,
		Description:          "Trae IDE",
		SettingsFile:         "mcp.json",
		MCPServersPathPrefix: "/mcpServers",
		RelPath:              []string{"Trae", "User"},
		PlatformPrefix: map[string][]string{
			"linux":   {".config"},
			"darwin":  {"Library", "Application Support"},
			"windows": {"AppData", "Roaming"},
		},
		Extension: JSON,
		SupportedTransportTypesMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "http",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "http",
		},
		IsTransportTypeFieldSupported: false,
		MCPServersUrlLabelMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "url",
			types.TransportTypeSSE:            "url",
			types.TransportTypeStreamableHTTP: "url",
		},
	},
	{
		ClientType:           Continue,
		Description:          "Continue.dev IDE plugins",
		SettingsFile:         "config.yaml",
		MCPServersPathPrefix: "/mcpServers",
		RelPath:              []string{".continue"},
		Extension:            YAML,
		SupportedTransportTypesMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "streamable-http",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "streamable-http",
		},
		IsTransportTypeFieldSupported: true,
		MCPServersUrlLabelMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "url",
			types.TransportTypeSSE:            "url",
			types.TransportTypeStreamableHTTP: "url",
		},
		// YAML configuration
		YAMLStorageType:     YAMLStorageTypeArray,
		YAMLIdentifierField: "name",
	},
	{
		ClientType:           OpenCode,
		Description:          "OpenCode editor",
		SettingsFile:         "opencode.json",
		MCPServersPathPrefix: "/mcp",
		RelPath:              []string{".config", "opencode"},
		Extension:            JSON,
		SupportedTransportTypesMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "remote", // OpenCode requires "type": "remote" for URL-based servers
			types.TransportTypeSSE:            "remote",
			types.TransportTypeStreamableHTTP: "remote",
		},
		IsTransportTypeFieldSupported: true, // OpenCode requires "type": "remote" for URL-based servers
		MCPServersUrlLabelMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "url",
			types.TransportTypeSSE:            "url",
			types.TransportTypeStreamableHTTP: "url",
		},
	},
	{
		ClientType:           Kiro,
		Description:          "Kiro AI IDE",
		SettingsFile:         "mcp.json",
		MCPServersPathPrefix: "/mcpServers",
		RelPath:              []string{".kiro", "settings"},
		Extension:            JSON,
		SupportedTransportTypesMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "http",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "http",
		},
		IsTransportTypeFieldSupported: false,
		MCPServersUrlLabelMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "url",
			types.TransportTypeSSE:            "url",
			types.TransportTypeStreamableHTTP: "url",
		},
	},
	{
		ClientType:                    Antigravity,
		Description:                   "Google Antigravity IDE",
		SettingsFile:                  "mcp_config.json",
		MCPServersPathPrefix:          "/mcpServers",
		RelPath:                       []string{".gemini", "antigravity"},
		Extension:                     JSON,
		IsTransportTypeFieldSupported: false,
		MCPServersUrlLabelMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "serverUrl",
			types.TransportTypeSSE:            "serverUrl",
			types.TransportTypeStreamableHTTP: "serverUrl",
		},
	},
	{
		ClientType:           Zed,
		Description:          "Zed editor",
		SettingsFile:         "settings.json",
		MCPServersPathPrefix: "/context_servers",
		RelPath:              []string{"zed"},
		PlatformPrefix: map[string][]string{
			"linux":   {".config"},
			"darwin":  {".config"},
			"windows": {"AppData", "Roaming"},
		},
		Extension:                     JSON,
		IsTransportTypeFieldSupported: false,
		MCPServersUrlLabelMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "url",
			types.TransportTypeSSE:            "url",
			types.TransportTypeStreamableHTTP: "url",
		},
	},
	{
		ClientType:           GeminiCli,
		Description:          "Google Gemini CLI",
		SettingsFile:         "settings.json",
		MCPServersPathPrefix: "/mcpServers",
		RelPath:              []string{".gemini"},
		Extension:            JSON,
		// Gemini CLI uses different URL fields based on transport type:
		// - SSE transport uses "url" field
		// - Streamable HTTP transport uses "httpUrl" field
		IsTransportTypeFieldSupported: false,
		MCPServersUrlLabelMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "httpUrl",
			types.TransportTypeSSE:            "url",
			types.TransportTypeStreamableHTTP: "httpUrl",
		},
	},
	{
		ClientType:   VSCodeServer,
		Description:  "Microsoft's VS Code Server (remote development)",
		SettingsFile: "mcp.json",
		RelPath: []string{
			".vscode-server", "data", "User",
		},
		MCPServersPathPrefix: "/servers",
		Extension:            JSON,
		SupportedTransportTypesMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "http",
			types.TransportTypeSSE:            "sse",
			types.TransportTypeStreamableHTTP: "http",
		},
	},
	{
		ClientType:           MistralVibe,
		Description:          "Mistral Vibe IDE",
		SettingsFile:         "config.toml",
		MCPServersPathPrefix: "/mcp_servers",
		RelPath:              []string{".vibe"},
		Extension:            TOML,
		SupportedTransportTypesMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "streamable-http",
			types.TransportTypeSSE:            "http",
			types.TransportTypeStreamableHTTP: "streamable-http",
		},
		IsTransportTypeFieldSupported: true,
		MCPServersUrlLabelMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "url",
			types.TransportTypeSSE:            "url",
			types.TransportTypeStreamableHTTP: "url",
		},
		// TOML configuration: uses array-of-tables format [[mcp_servers]]
		TOMLStorageType: TOMLStorageTypeArray,
	},
	{
		ClientType:           Codex,
		Description:          "OpenAI Codex CLI",
		SettingsFile:         "config.toml",
		MCPServersPathPrefix: "/mcp_servers",
		RelPath:              []string{".codex"},
		Extension:            TOML,
		// Codex doesn't support a transport type field - it auto-detects
		IsTransportTypeFieldSupported: false,
		MCPServersUrlLabelMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "url",
			types.TransportTypeSSE:            "url",
			types.TransportTypeStreamableHTTP: "url",
		},
		// TOML configuration: uses nested tables format [mcp_servers.servername]
		TOMLStorageType: TOMLStorageTypeMap,
	},
}

// GetAllClients returns a slice of all supported MCP client types, sorted alphabetically.
// This is the single source of truth for valid client types.
func GetAllClients() []MCPClient {
	clients := make([]MCPClient, 0, len(supportedClientIntegrations))
	for _, config := range supportedClientIntegrations {
		clients = append(clients, config.ClientType)
	}
	// Sort alphabetically
	sort.Slice(clients, func(i, j int) bool {
		return clients[i] < clients[j]
	})
	return clients
}

// IsValidClient checks if the provided client type is supported.
func IsValidClient(clientType string) bool {
	for _, config := range supportedClientIntegrations {
		if string(config.ClientType) == clientType {
			return true
		}
	}
	return false
}

// GetClientDescription returns the description for a given client type.
// Returns an empty string if the client type is not found.
func GetClientDescription(clientType MCPClient) string {
	for _, config := range supportedClientIntegrations {
		if config.ClientType == clientType {
			return config.Description
		}
	}
	return ""
}

// GetClientListFormatted returns a formatted multi-line string listing all supported clients
// with their descriptions, sorted alphabetically. This is suitable for use in CLI help text.
func GetClientListFormatted() string {
	// Create a sorted copy of the configurations
	configs := make([]mcpClientConfig, len(supportedClientIntegrations))
	copy(configs, supportedClientIntegrations)
	sort.Slice(configs, func(i, j int) bool {
		return configs[i].ClientType < configs[j].ClientType
	})

	var sb strings.Builder
	for _, config := range configs {
		sb.WriteString(fmt.Sprintf("  - %s: %s\n", config.ClientType, config.Description))
	}
	return strings.TrimSuffix(sb.String(), "\n")
}

// GetClientListCSV returns a comma-separated list of all supported client types, sorted alphabetically.
// This is suitable for use in error messages.
func GetClientListCSV() string {
	clients := GetAllClients() // GetAllClients already returns sorted list
	clientStrs := make([]string, len(clients))
	for i, client := range clients {
		clientStrs[i] = string(client)
	}
	return strings.Join(clientStrs, ", ")
}

// ConfigFile represents a client configuration file
type ConfigFile struct {
	Path          string
	ClientType    MCPClient
	ConfigUpdater ConfigUpdater
	Extension     Extension
}

// FindClientConfig returns the client configuration file for a given client type.
func FindClientConfig(clientType MCPClient) (*ConfigFile, error) {
	manager, err := NewClientManager()
	if err != nil {
		return nil, err
	}
	return manager.FindClientConfig(clientType)
}

// FindClientConfig returns the client configuration file for a given client type using this manager's dependencies.
func (cm *ClientManager) FindClientConfig(clientType MCPClient) (*ConfigFile, error) {
	// retrieve the metadata of the config files
	configFile, err := cm.retrieveConfigFileMetadata(clientType)
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
func FindRegisteredClientConfigs(ctx context.Context) ([]ConfigFile, error) {
	manager, err := NewClientManager()
	if err != nil {
		return nil, err
	}
	return manager.FindRegisteredClientConfigs(ctx)
}

// FindRegisteredClientConfigs finds all registered client configs using this manager's dependencies
func (cm *ClientManager) FindRegisteredClientConfigs(ctx context.Context) ([]ConfigFile, error) {
	clientStatuses, err := cm.GetClientStatus(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get client status: %w", err)
	}

	var configFiles []ConfigFile
	for _, clientStatus := range clientStatuses {
		if !clientStatus.Installed || !clientStatus.Registered {
			continue
		}
		cf, err := cm.FindClientConfig(clientStatus.ClientType)
		if err != nil {
			if errors.Is(err, ErrConfigFileNotFound) {
				logger.Debugf("Client config file not found for %s, creating it...", clientStatus.ClientType)
				cf, err = cm.CreateClientConfig(clientStatus.ClientType)
				if err != nil {
					logger.Warnf("Unable to create client config for %s: %v", clientStatus.ClientType, err)
					continue
				}
				logger.Debugf("Successfully created client config file for %s", clientStatus.ClientType)
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
func CreateClientConfig(clientType MCPClient) (*ConfigFile, error) {
	manager, err := NewClientManager()
	if err != nil {
		return nil, err
	}
	return manager.CreateClientConfig(clientType)
}

// CreateClientConfig creates a new client configuration file for a given client type using this manager's dependencies.
func (cm *ClientManager) CreateClientConfig(clientType MCPClient) (*ConfigFile, error) {
	// Find the configuration for the requested client type
	var clientCfg *mcpClientConfig
	for _, cfg := range cm.clientIntegrations {
		if cfg.ClientType == clientType {
			clientCfg = &cfg
			break
		}
	}

	if clientCfg == nil {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedClientType, clientType)
	}

	// Build the path to the configuration file
	path := buildConfigFilePath(clientCfg.SettingsFile, clientCfg.RelPath, clientCfg.PlatformPrefix, []string{cm.homeDir})

	// Validate that the file does not already exist
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		return nil, fmt.Errorf("client config file already exists at %s", path)
	}

	// Create the file if it does not exist
	logger.Debugf("Creating new client config file at %s", path)

	// Create parent directories if they don't exist
	parentDir := filepath.Dir(path)
	if err := os.MkdirAll(parentDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create parent directories for %s: %w", path, err)
	}

	var initialContent []byte
	switch clientCfg.Extension {
	case YAML, TOML:
		// For YAML and TOML files, create an empty file - the updater will initialize structure as needed
		initialContent = []byte("")
	case JSON:
		// JSON files get empty object
		initialContent = []byte("{}")
	}

	err := os.WriteFile(path, initialContent, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to create client config file: %w", err)
	}

	return cm.FindClientConfig(clientType)
}

// Upsert updates/inserts an MCP server in a client configuration file
// It is a wrapper around the ConfigUpdater.Upsert method. Because the
// ConfigUpdater is different for each client type, we need to handle
// the different types of McpServer objects. For example, VSCode and ClaudeCode allows
// for a `type` field, but Cursor and others do not. This allows us to
// build up more complex MCP server configurations for different clients
// without leaking them into the CMD layer.
func Upsert(cf ConfigFile, name string, url string, transportType string) error {
	manager, err := NewClientManager()
	if err != nil {
		return err
	}
	return manager.Upsert(cf, name, url, transportType)
}

// Upsert updates/inserts an MCP server in a client configuration file using this manager's dependencies.
func (cm *ClientManager) Upsert(cf ConfigFile, name string, url string, transportType string) error {
	for i := range cm.clientIntegrations {
		if cf.ClientType != cm.clientIntegrations[i].ClientType {
			continue
		}
		server := buildMCPServer(url, transportType, &cm.clientIntegrations[i])
		return cf.ConfigUpdater.Upsert(name, server)
	}
	return nil
}

// buildMCPServer constructs an MCPServer struct with the appropriate URL field and optional type field.
// The URL field name is determined by looking up the transport type in MCPServersUrlLabelMap.
// If the map is nil or the transport type is not found, it falls back to "url" as the default.
// For most clients, all transport types map to the same URL field (e.g., "url"), but some clients
// like Gemini CLI use different URL fields per transport type (e.g., "url" for SSE, "httpUrl" for streamable HTTP).
func buildMCPServer(url, transportType string, clientCfg *mcpClientConfig) MCPServer {
	server := MCPServer{}

	// Determine the URL field name from the transport type using MCPServersUrlLabelMap
	urlFieldName := defaultURLFieldName // default fallback
	if clientCfg.MCPServersUrlLabelMap != nil {
		if mappedUrlField, ok := clientCfg.MCPServersUrlLabelMap[types.TransportType(transportType)]; ok {
			urlFieldName = mappedUrlField
		}
	}

	// Set the URL in the appropriate field
	switch urlFieldName {
	case "serverUrl":
		server.ServerUrl = url
	case "httpUrl":
		server.HttpUrl = url
	case "uri":
		server.Uri = url
	default:
		server.Url = url
	}

	// Add transport type field if supported by the client
	if clientCfg.IsTransportTypeFieldSupported {
		if mappedType, ok := clientCfg.SupportedTransportTypesMap[types.TransportType(transportType)]; ok {
			server.Type = mappedType
		}
	}

	return server
}

// retrieveConfigFileMetadata retrieves the metadata for client configuration files using this manager's dependencies.
func (cm *ClientManager) retrieveConfigFileMetadata(clientType MCPClient) (*ConfigFile, error) {
	// Find the configuration for the requested client type
	var clientCfg *mcpClientConfig
	for _, cfg := range cm.clientIntegrations {
		if cfg.ClientType == clientType {
			clientCfg = &cfg
			break
		}
	}

	if clientCfg == nil {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedClientType, clientType)
	}

	// Build the path to the configuration file
	path := buildConfigFilePath(clientCfg.SettingsFile, clientCfg.RelPath, clientCfg.PlatformPrefix, []string{cm.homeDir})

	// Validate that the file exists
	if err := validateConfigFileExists(path); err != nil {
		return nil, err
	}

	// Create a config updater for this file based on the extension
	var configUpdater ConfigUpdater
	switch clientCfg.Extension {
	case YAML:
		// Use the generic YAML converter with configuration from mcpClientConfig
		converter := NewGenericYAMLConverter(clientCfg)
		configUpdater = &YAMLConfigUpdater{
			Path:      path,
			Converter: converter,
		}
	case TOML:
		serversKey := extractServersKeyFromConfig(clientCfg)
		urlLabel := extractURLLabelFromConfig(clientCfg)

		// Choose TOML updater based on storage type
		if clientCfg.TOMLStorageType == TOMLStorageTypeMap {
			// Use map-based format [section.servername] (e.g., Codex)
			configUpdater = &TOMLMapConfigUpdater{
				Path:       path,
				ServersKey: serversKey,
				URLField:   urlLabel,
			}
		} else {
			// Default to array-of-tables format [[section]] (e.g., Mistral Vibe)
			configUpdater = &TOMLConfigUpdater{
				Path:            path,
				ServersKey:      serversKey,
				IdentifierField: "name", // TOML configs use "name" as the identifier
				URLField:        urlLabel,
			}
		}
	case JSON:
		configUpdater = &JSONConfigUpdater{
			Path:                 path,
			MCPServersPathPrefix: clientCfg.MCPServersPathPrefix,
		}
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

	// For YAML and TOML files, empty content is valid
	// For JSON files, default to empty object if the file is empty
	if len(data) == 0 {
		switch cf.Extension {
		case YAML, TOML:
			return nil // Empty YAML/TOML files are valid
		case JSON:
			data = []byte("{}") // Default to an empty JSON object
		}
	}

	switch cf.Extension {
	case YAML:
		var temp any
		err = yaml.Unmarshal(data, &temp)
		if err != nil {
			return fmt.Errorf("failed to parse YAML for file %s: %w", cf.Path, err)
		}
	case TOML:
		var temp any
		err = toml.Unmarshal(data, &temp)
		if err != nil {
			return fmt.Errorf("failed to parse TOML for file %s: %w", cf.Path, err)
		}
	case JSON:
		_, err = hujson.Parse(data)
		if err != nil {
			return fmt.Errorf("failed to parse JSON for file %s: %w", cf.Path, err)
		}
	}
	return nil
}

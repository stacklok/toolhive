// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package client provides utilities for managing client configurations
// and interacting with MCP servers.
package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/tailscale/hujson"
	"gopkg.in/yaml.v3"

	"github.com/stacklok/toolhive/pkg/transport/types"
)

// defaultURLFieldName is the default URL field name used when no specific mapping exists
const defaultURLFieldName = "url"

// ClientApp is an enum of MCP-capable AI clients (IDEs, editors, and coding tools).
// Only clients that support MCP registration appear here; LLM-gateway-only
// tools (e.g. Xcode) are represented by the separate LLMClientApp type so
// that code generators (swag) do not include them in the MCP API enum.
//
//nolint:revive // ClientApp is intentionally named for clarity across packages
type ClientApp string

// LLMClientApp identifies tools that support the LLM gateway but do not
// support MCP client registration (e.g. GitHub Copilot for Xcode).
// Keeping this type separate from ClientApp prevents swag from including
// these tools in the MCP API ClientApp enum.
type LLMClientApp string

const (
	// RooCode represents the Roo Code extension for VS Code.
	RooCode ClientApp = "roo-code"
	// Cline represents the Cline extension for VS Code.
	Cline ClientApp = "cline"
	// Cursor represents the Cursor editor.
	Cursor ClientApp = "cursor"
	// VSCodeInsider represents the VS Code Insiders editor.
	VSCodeInsider ClientApp = "vscode-insider"
	// VSCode represents the standard VS Code editor.
	VSCode ClientApp = "vscode"
	// ClaudeCode represents the Claude Code CLI.
	ClaudeCode ClientApp = "claude-code"
	// Windsurf represents the Windsurf IDE.
	Windsurf ClientApp = "windsurf"
	// WindsurfJetBrains represents the Windsurf plugin for JetBrains.
	WindsurfJetBrains ClientApp = "windsurf-jetbrains"
	// AmpCli represents the Sourcegraph Amp CLI.
	AmpCli ClientApp = "amp-cli"
	// AmpVSCode represents the Sourcegraph Amp extension for VS Code.
	AmpVSCode ClientApp = "amp-vscode"
	// AmpCursor represents the Sourcegraph Amp extension for Cursor.
	AmpCursor ClientApp = "amp-cursor"
	// AmpVSCodeInsider represents the Sourcegraph Amp extension for VS Code Insiders.
	AmpVSCodeInsider ClientApp = "amp-vscode-insider"
	// AmpWindsurf represents the Sourcegraph Amp extension for Windsurf.
	AmpWindsurf ClientApp = "amp-windsurf"
	// LMStudio represents the LM Studio application.
	LMStudio ClientApp = "lm-studio"
	// Goose represents the Goose AI agent.
	Goose ClientApp = "goose"
	// Trae represents the Trae IDE.
	Trae ClientApp = "trae"
	// Continue represents the Continue.dev IDE plugins.
	Continue ClientApp = "continue"
	// OpenCode represents the OpenCode editor.
	OpenCode ClientApp = "opencode"
	// Kiro represents the Kiro AI IDE.
	Kiro ClientApp = "kiro"
	// Antigravity represents the Google Antigravity IDE.
	Antigravity ClientApp = "antigravity"
	// Zed represents the Zed editor.
	Zed ClientApp = "zed"
	// GeminiCli represents the Google Gemini CLI.
	GeminiCli ClientApp = "gemini-cli"
	// VSCodeServer represents Microsoft's VS Code Server (remote development).
	VSCodeServer ClientApp = "vscode-server"
	// MistralVibe represents the Mistral Vibe IDE.
	MistralVibe ClientApp = "mistral-vibe"
	// Codex represents the OpenAI Codex CLI.
	Codex ClientApp = "codex"
	// KimiCli represents the Kimi Code CLI.
	KimiCli ClientApp = "kimi-cli"
	// Factory represents the Factory.ai Droid CLI.
	Factory ClientApp = "factory"
)

const (
	// Xcode represents GitHub Copilot for Xcode.
	// Xcode does not support MCP; it is an LLM-gateway-only tool.
	// It is declared as LLMClientApp (not ClientApp) so that code generators
	// such as swag do not include "xcode" in the MCP API ClientApp enum.
	Xcode LLMClientApp = "xcode"
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

// Platform represents a runtime.GOOS value used as a key in platform-specific path maps.
type Platform string

const (
	// PlatformLinux is the Linux platform.
	PlatformLinux Platform = "linux"
	// PlatformDarwin is the macOS platform.
	PlatformDarwin Platform = "darwin"
	// PlatformWindows is the Windows platform.
	PlatformWindows Platform = "windows"
)

// LLMGatewayKeySpec describes a single JSON key to patch when configuring
// (or reverting) LLM gateway access for a tool. JSONPointer is an RFC 6901
// path (e.g. "/apiKeyHelper" or "/env/ANTHROPIC_BASE_URL"). Dots in flat
// top-level key names (e.g. "/cursor.general.openAIBaseURL") are treated as
// literals by hujson.Patch.
// ValueField names which LLMApplyConfig field to write: "GatewayURL",
// "ProxyBaseURL", "TokenHelperCommand", "PlaceholderAPIKey" (constant "thv-proxy"),
// or "NodeTLSRejectUnauthorized" (writes "0" when TLSSkipVerify is true).
// ClearWhenEmpty: when true and the resolved value is empty, the key is removed
// from the settings file rather than skipped. Use for conditional keys like
// NODE_TLS_REJECT_UNAUTHORIZED that must be cleaned up when the flag is cleared.
type LLMGatewayKeySpec struct {
	JSONPointer    string // RFC 6901 path
	ValueField     string // "GatewayURL" | "ProxyBaseURL" | "TokenHelperCommand" | "PlaceholderAPIKey" | "NodeTLSRejectUnauthorized"
	ClearWhenEmpty bool   // remove the key when the resolved value is empty
}

// clientAppConfig represents a configuration path for a supported MCP client.
type clientAppConfig struct {
	ClientType                    ClientApp
	Description                   string
	RelPath                       []string
	SettingsFile                  string
	PlatformPrefix                map[Platform][]string
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
	// Skill-specific configuration
	SupportsSkills    bool     // Whether this client supports skills
	SkillsGlobalPath  []string // Path segments for global skills dir (from home dir)
	SkillsProjectPath []string // Path segments for project-local skills dir (from project root)
	// SkillsPlatformPrefix maps Platform values to path segments inserted between
	// home dir and SkillsGlobalPath. Needed for clients following platform conventions
	// (e.g., XDG ~/.config/ on Linux/macOS).
	// If nil or missing an entry for the current OS, no prefix is added.
	SkillsPlatformPrefix map[Platform][]string
	// LLM gateway configuration ─────────────────────────────────────────────
	// LLMGatewayMode is "direct" (token-helper) or "proxy" (static key via
	// localhost reverse proxy), or "" when the tool has no LLM gateway support.
	LLMGatewayMode string
	// LLMBinaryName is the executable name looked up via exec.LookPath to
	// confirm the tool is actually installed (not just a leftover config
	// directory). Leave empty for tools that are not on $PATH (e.g. macOS
	// GUI apps).
	LLMBinaryName string
	// LLMGatewayOnly marks tools that support LLM gateway but not MCP (e.g. Xcode).
	// Entries with this flag are excluded from the MCP client list.
	LLMGatewayOnly bool
	// LLMSettingsFile is the filename of the settings file to patch for LLM
	// gateway (may differ from SettingsFile used for MCP).
	LLMSettingsFile string
	// LLMSettingsRelPath is the path segments from home dir + platform prefix
	// to the directory containing LLMSettingsFile.
	LLMSettingsRelPath []string
	// LLMSettingsPlatformPrefix maps Platform to path segments prepended before
	// LLMSettingsRelPath (same semantics as PlatformPrefix).
	LLMSettingsPlatformPrefix map[Platform][]string
	// LLMGatewayKeys lists the JSON Pointer paths and value-field mappings to
	// apply when setting up (or reverting) LLM gateway access.
	LLMGatewayKeys []LLMGatewayKeySpec
}

// extractServersKeyFromConfig extracts the servers key from MCPServersPathPrefix
// by removing the leading "/" (e.g., "/mcpServers" -> "mcpServers").
func extractServersKeyFromConfig(cfg *clientAppConfig) string {
	return strings.TrimPrefix(cfg.MCPServersPathPrefix, "/")
}

// extractURLLabelFromConfig extracts the URL field label from MCPServersUrlLabelMap.
// It checks transport types in priority order: StreamableHTTP, then Stdio.
// Returns defaultURLFieldName if no mapping is found.
func extractURLLabelFromConfig(cfg *clientAppConfig) string {
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

var supportedClientIntegrations = []clientAppConfig{
	{
		ClientType:   RooCode,
		Description:  "VS Code Roo Code extension",
		SettingsFile: "mcp_settings.json",
		RelPath: []string{
			"Code", "User", "globalStorage", "rooveterinaryinc.roo-cline", "settings",
		},
		PlatformPrefix: map[Platform][]string{
			PlatformLinux:   {".config"},
			PlatformDarwin:  {"Library", "Application Support"},
			PlatformWindows: {"AppData", "Roaming"},
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
		SupportsSkills:    true,
		SkillsGlobalPath:  []string{".roo", "skills"},
		SkillsProjectPath: []string{".roo", "skills"},
	},
	{
		ClientType:   Cline,
		Description:  "VS Code Cline extension",
		SettingsFile: "cline_mcp_settings.json",
		RelPath: []string{
			"Code", "User", "globalStorage", "saoudrizwan.claude-dev", "settings",
		},
		PlatformPrefix: map[Platform][]string{
			PlatformLinux:   {".config"},
			PlatformDarwin:  {"Library", "Application Support"},
			PlatformWindows: {"AppData", "Roaming"},
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
		SupportsSkills:    true,
		SkillsGlobalPath:  []string{".cline", "skills"},
		SkillsProjectPath: []string{".cline", "skills"},
	},
	{
		ClientType:   VSCodeInsider,
		Description:  "Visual Studio Code Insiders",
		SettingsFile: "mcp.json",
		RelPath: []string{
			"Code - Insiders", "User",
		},
		PlatformPrefix: map[Platform][]string{
			PlatformLinux:   {".config"},
			PlatformDarwin:  {"Library", "Application Support"},
			PlatformWindows: {"AppData", "Roaming"},
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
		SupportsSkills:    true,
		SkillsGlobalPath:  []string{".copilot", "skills"},
		SkillsProjectPath: []string{".github", "skills"},
		// LLM gateway: patches settings.json (same dir as mcp.json, different file)
		LLMGatewayMode:     "proxy",
		LLMBinaryName:      "code-insiders",
		LLMSettingsFile:    "settings.json",
		LLMSettingsRelPath: []string{"Code - Insiders", "User"},
		LLMSettingsPlatformPrefix: map[Platform][]string{
			PlatformLinux:   {".config"},
			PlatformDarwin:  {"Library", "Application Support"},
			PlatformWindows: {"AppData", "Roaming"},
		},
		LLMGatewayKeys: []LLMGatewayKeySpec{
			{JSONPointer: "/github.copilot.advanced.serverUrl", ValueField: "ProxyBaseURL"},
			{JSONPointer: "/github.copilot.advanced.apiKey", ValueField: "PlaceholderAPIKey"},
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
		PlatformPrefix: map[Platform][]string{
			PlatformLinux:   {".config"},
			PlatformDarwin:  {"Library", "Application Support"},
			PlatformWindows: {"AppData", "Roaming"},
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
		SupportsSkills:    true,
		SkillsGlobalPath:  []string{".copilot", "skills"},
		SkillsProjectPath: []string{".github", "skills"},
		// LLM gateway: patches settings.json (same dir as mcp.json, different file)
		LLMGatewayMode:     "proxy",
		LLMBinaryName:      "code",
		LLMSettingsFile:    "settings.json",
		LLMSettingsRelPath: []string{"Code", "User"},
		LLMSettingsPlatformPrefix: map[Platform][]string{
			PlatformLinux:   {".config"},
			PlatformDarwin:  {"Library", "Application Support"},
			PlatformWindows: {"AppData", "Roaming"},
		},
		LLMGatewayKeys: []LLMGatewayKeySpec{
			{JSONPointer: "/github.copilot.advanced.serverUrl", ValueField: "ProxyBaseURL"},
			{JSONPointer: "/github.copilot.advanced.apiKey", ValueField: "PlaceholderAPIKey"},
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
		SupportsSkills:    true,
		SkillsGlobalPath:  []string{".cursor", "skills"},
		SkillsProjectPath: []string{".cursor", "skills"},
		// LLM gateway: patches the editor settings.json (different from the MCP mcp.json)
		LLMGatewayMode:     "proxy",
		LLMBinaryName:      "cursor",
		LLMSettingsFile:    "settings.json",
		LLMSettingsRelPath: []string{"Cursor", "User"},
		LLMSettingsPlatformPrefix: map[Platform][]string{
			PlatformLinux:   {".config"},
			PlatformDarwin:  {"Library", "Application Support"},
			PlatformWindows: {"AppData", "Roaming"},
		},
		LLMGatewayKeys: []LLMGatewayKeySpec{
			{JSONPointer: "/cursor.general.openAIBaseURL", ValueField: "ProxyBaseURL"},
			{JSONPointer: "/cursor.general.openAIAPIKey", ValueField: "PlaceholderAPIKey"},
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
		SupportsSkills:    true,
		SkillsGlobalPath:  []string{".claude", "skills"},
		SkillsProjectPath: []string{".claude", "skills"},
		// LLM gateway: patches ~/.claude/settings.json (different from the MCP .claude.json)
		LLMGatewayMode:     "direct",
		LLMBinaryName:      "claude",
		LLMSettingsFile:    "settings.json",
		LLMSettingsRelPath: []string{".claude"},
		LLMGatewayKeys: []LLMGatewayKeySpec{
			{JSONPointer: "/apiKeyHelper", ValueField: "TokenHelperCommand"},
			// AnthropicBaseURL appends llm.anthropic_path_prefix to the gateway
			// URL so Envoy AI Gateway (which routes native-Anthropic traffic at
			// /anthropic) works without manual edits.
			{JSONPointer: "/env/ANTHROPIC_BASE_URL", ValueField: "AnthropicBaseURL"},
			// NODE_TLS_REJECT_UNAUTHORIZED is only written when --tls-skip-verify is set.
			// ClearWhenEmpty ensures it is removed when the flag is later cleared.
			{JSONPointer: "/env/NODE_TLS_REJECT_UNAUTHORIZED", ValueField: "NodeTLSRejectUnauthorized", ClearWhenEmpty: true},
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
		SupportsSkills:    true,
		SkillsGlobalPath:  []string{".codeium", "windsurf", "skills"},
		SkillsProjectPath: []string{".windsurf", "skills"},
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
		PlatformPrefix: map[Platform][]string{
			PlatformLinux:   {".config"},
			PlatformDarwin:  {".config"},
			PlatformWindows: {"AppData", "Roaming"},
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
		SupportsSkills:    true,
		SkillsGlobalPath:  []string{".agents", "skills"},
		SkillsProjectPath: []string{".agents", "skills"},
	},
	{
		ClientType:           AmpVSCode,
		Description:          "VS Code Sourcegraph Amp extension",
		SettingsFile:         "settings.json",
		MCPServersPathPrefix: "/amp.mcpServers",
		RelPath:              []string{"Code", "User"},
		PlatformPrefix: map[Platform][]string{
			PlatformLinux:   {".config"},
			PlatformDarwin:  {"Library", "Application Support"},
			PlatformWindows: {"AppData", "Roaming"},
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
		PlatformPrefix: map[Platform][]string{
			PlatformLinux:   {".config"},
			PlatformDarwin:  {"Library", "Application Support"},
			PlatformWindows: {"AppData", "Roaming"},
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
		PlatformPrefix: map[Platform][]string{
			PlatformLinux:   {".config"},
			PlatformDarwin:  {"Library", "Application Support"},
			PlatformWindows: {"AppData", "Roaming"},
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
		PlatformPrefix: map[Platform][]string{
			PlatformLinux:   {".config"},
			PlatformDarwin:  {"Library", "Application Support"},
			PlatformWindows: {"AppData", "Roaming"},
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
		PlatformPrefix: map[Platform][]string{
			PlatformLinux:   {".config"},
			PlatformDarwin:  {".config"},
			PlatformWindows: {"AppData", "Block"},
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
		SupportsSkills:    true,
		SkillsGlobalPath:  []string{".agents", "skills"},
		SkillsProjectPath: []string{".agents", "skills"},
	},
	{
		ClientType:           Trae,
		Description:          "Trae IDE",
		SettingsFile:         "mcp.json",
		MCPServersPathPrefix: "/mcpServers",
		RelPath:              []string{"Trae", "User"},
		PlatformPrefix: map[Platform][]string{
			PlatformLinux:   {".config"},
			PlatformDarwin:  {"Library", "Application Support"},
			PlatformWindows: {"AppData", "Roaming"},
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
		SupportsSkills:    true,
		SkillsGlobalPath:  []string{".agents", "skills"},
		SkillsProjectPath: []string{".agents", "skills"},
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
		SupportsSkills:    true,
		SkillsGlobalPath:  []string{"opencode", "skills"},
		SkillsProjectPath: []string{".opencode", "skills"},
		SkillsPlatformPrefix: map[Platform][]string{
			PlatformLinux:  {".config"},
			PlatformDarwin: {".config"},
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
		SupportsSkills:    true,
		SkillsGlobalPath:  []string{".kiro", "skills"},
		SkillsProjectPath: []string{".kiro", "skills"},
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
		SupportsSkills:    true,
		SkillsGlobalPath:  []string{".agents", "skills"},
		SkillsProjectPath: []string{".agents", "skills"},
	},
	{
		ClientType:           Zed,
		Description:          "Zed editor",
		SettingsFile:         "settings.json",
		MCPServersPathPrefix: "/context_servers",
		RelPath:              []string{"zed"},
		PlatformPrefix: map[Platform][]string{
			PlatformLinux:   {".config"},
			PlatformDarwin:  {".config"},
			PlatformWindows: {"AppData", "Roaming"},
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
		SupportsSkills:    true,
		SkillsGlobalPath:  []string{".agents", "skills"},
		SkillsProjectPath: []string{".agents", "skills"},
		// LLM gateway: patches the same settings.json used for MCP
		LLMGatewayMode:     "direct",
		LLMBinaryName:      "gemini",
		LLMSettingsFile:    "settings.json",
		LLMSettingsRelPath: []string{".gemini"},
		LLMGatewayKeys: []LLMGatewayKeySpec{
			{JSONPointer: "/auth/tokenCommand", ValueField: "TokenHelperCommand"},
			{JSONPointer: "/baseUrl", ValueField: "GatewayURL"},
			// NODE_TLS_REJECT_UNAUTHORIZED is only written when --tls-skip-verify is set.
			// ClearWhenEmpty ensures it is removed when the flag is later cleared.
			{JSONPointer: "/env/NODE_TLS_REJECT_UNAUTHORIZED", ValueField: "NodeTLSRejectUnauthorized", ClearWhenEmpty: true},
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
		TOMLStorageType:   TOMLStorageTypeArray,
		SupportsSkills:    true,
		SkillsGlobalPath:  []string{".vibe", "skills"},
		SkillsProjectPath: []string{".vibe", "skills"},
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
		TOMLStorageType:   TOMLStorageTypeMap,
		SupportsSkills:    true,
		SkillsGlobalPath:  []string{".agents", "skills"},
		SkillsProjectPath: []string{".agents", "skills"},
	},
	{
		ClientType:           KimiCli,
		Description:          "Kimi Code CLI",
		SettingsFile:         "mcp.json",
		MCPServersPathPrefix: "/mcpServers",
		RelPath:              []string{".kimi"},
		Extension:            JSON,
		// Kimi CLI does not use a transport type field in the config file
		IsTransportTypeFieldSupported: false,
		MCPServersUrlLabelMap: map[types.TransportType]string{
			types.TransportTypeStdio:          "url",
			types.TransportTypeSSE:            "url",
			types.TransportTypeStreamableHTTP: "url",
		},
		SupportsSkills:    true,
		SkillsGlobalPath:  []string{".kimi", "skills"},
		SkillsProjectPath: []string{".kimi", "skills"},
	},
	{
		ClientType:           Factory,
		Description:          "Factory.ai Droid CLI",
		SettingsFile:         "mcp.json",
		MCPServersPathPrefix: "/mcpServers",
		RelPath:              []string{".factory"},
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
		SupportsSkills:    true,
		SkillsGlobalPath:  []string{".factory", "skills"},
		SkillsProjectPath: []string{".factory", "skills"},
	},
	{
		// Xcode does not support MCP; it is an LLM-gateway-only entry.
		// Cast LLMClientApp → ClientApp for internal config storage; the type
		// distinction matters only for swag enum generation (see LLMClientApp).
		ClientType:     ClientApp(Xcode),
		Description:    "GitHub Copilot for Xcode",
		LLMGatewayOnly: true,
		LLMGatewayMode: "proxy",
		// Full path is macOS-specific; on Linux/Windows this directory will not
		// exist, so DetectedLLMGatewayClients() naturally returns false there.
		LLMSettingsFile:    "editorSettings.json",
		LLMSettingsRelPath: []string{"Library", "Application Support", "GitHub Copilot for Xcode"},
		LLMGatewayKeys: []LLMGatewayKeySpec{
			{JSONPointer: "/openAIBaseURL", ValueField: "ProxyBaseURL"},
			{JSONPointer: "/apiKey", ValueField: "PlaceholderAPIKey"},
		},
	},
}

// GetAllClients returns a slice of all supported MCP client types, sorted alphabetically.
// This is the single source of truth for valid client types.
func GetAllClients() []ClientApp {
	clients := make([]ClientApp, 0, len(supportedClientIntegrations))
	for _, config := range supportedClientIntegrations {
		if !config.LLMGatewayOnly {
			clients = append(clients, config.ClientType)
		}
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
		if string(config.ClientType) == clientType && !config.LLMGatewayOnly {
			return true
		}
	}
	return false
}

// GetClientDescription returns the description for a given client type.
// Returns an empty string if the client type is not found.
func GetClientDescription(clientType ClientApp) string {
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
	// Create a sorted copy of MCP-capable configurations (exclude LLM-gateway-only entries).
	var configs []clientAppConfig
	for _, cfg := range supportedClientIntegrations {
		if !cfg.LLMGatewayOnly {
			configs = append(configs, cfg)
		}
	}
	sort.Slice(configs, func(i, j int) bool {
		return configs[i].ClientType < configs[j].ClientType
	})

	var sb strings.Builder
	for _, config := range configs {
		fmt.Fprintf(&sb, "  - %s: %s\n", config.ClientType, config.Description)
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
	ClientType    ClientApp
	ConfigUpdater ConfigUpdater
	Extension     Extension
}

// FindClientConfig returns the client configuration file for a given client type.
func FindClientConfig(clientType ClientApp) (*ConfigFile, error) {
	manager, err := NewClientManager()
	if err != nil {
		return nil, err
	}
	return manager.FindClientConfig(clientType)
}

// FindClientConfig returns the client configuration file for a given client type using this manager's dependencies.
func (cm *ClientManager) FindClientConfig(clientType ClientApp) (*ConfigFile, error) {
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
				slog.Debug("client config file not found, creating", "client", clientStatus.ClientType)
				cf, err = cm.CreateClientConfig(clientStatus.ClientType)
				if err != nil {
					slog.Warn("unable to create client config", "client", clientStatus.ClientType, "error", err)
					continue
				}
				slog.Debug("successfully created client config file", "client", clientStatus.ClientType)
			} else {
				slog.Warn("unable to process client config", "client", clientStatus.ClientType, "error", err)
				continue
			}
		}
		configFiles = append(configFiles, *cf)
	}

	return configFiles, nil
}

// CreateClientConfig creates a new client configuration file for a given client type.
func CreateClientConfig(clientType ClientApp) (*ConfigFile, error) {
	manager, err := NewClientManager()
	if err != nil {
		return nil, err
	}
	return manager.CreateClientConfig(clientType)
}

// CreateClientConfig creates a new client configuration file for a given client type using this manager's dependencies.
func (cm *ClientManager) CreateClientConfig(clientType ClientApp) (*ConfigFile, error) {
	// Find the configuration for the requested client type
	clientCfg := cm.lookupClientAppConfig(clientType)
	if clientCfg == nil {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedClientType, clientType)
	}
	if clientCfg.LLMGatewayOnly {
		return nil, fmt.Errorf("%w: %s does not support MCP configuration", ErrUnsupportedClientType, clientType)
	}

	// Build the path to the configuration file
	path := buildConfigFilePath(clientCfg.SettingsFile, clientCfg.RelPath, clientCfg.PlatformPrefix, []string{cm.homeDir})

	// Validate that the file does not already exist
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		return nil, fmt.Errorf("client config file already exists at %s", path)
	}

	// Create the file if it does not exist
	slog.Debug("creating new client config file", "path", path)

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
	cfg := cm.lookupClientAppConfig(cf.ClientType)
	if cfg == nil {
		return nil
	}
	server := buildMCPServer(url, transportType, cfg)
	return cf.ConfigUpdater.Upsert(name, server)
}

// buildMCPServer constructs an MCPServer struct with the appropriate URL field and optional type field.
// The URL field name is determined by looking up the transport type in MCPServersUrlLabelMap.
// If the map is nil or the transport type is not found, it falls back to "url" as the default.
// For most clients, all transport types map to the same URL field (e.g., "url"), but some clients
// like Gemini CLI use different URL fields per transport type (e.g., "url" for SSE, "httpUrl" for streamable HTTP).
func buildMCPServer(url, transportType string, clientCfg *clientAppConfig) MCPServer {
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
func (cm *ClientManager) retrieveConfigFileMetadata(clientType ClientApp) (*ConfigFile, error) {
	// Find the configuration for the requested client type
	clientCfg := cm.lookupClientAppConfig(clientType)
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
		// Use the generic YAML converter with configuration from clientAppConfig
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

func buildConfigFilePath(settingsFile string, relPath []string, platformPrefix map[Platform][]string, path []string) string {
	if prefix, ok := platformPrefix[Platform(runtime.GOOS)]; ok {
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

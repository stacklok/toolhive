// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// config_editor.go provides ConfigUpdater implementations for editing MCP client
// configuration files in JSON, YAML, and TOML formats.
//
// # Error Handling
//
// All ConfigUpdater methods (Upsert/Remove) return errors to their callers rather
// than handling them internally. This design allows callers to decide the appropriate
// action based on context:
//
//   - CLI commands (e.g., "thv client register"): Errors propagate up to Cobra's
//     RunE function, which prints the error to stderr and exits with code 1.
//     This is the correct behavior for explicit user commands.
//
//   - Background operations (e.g., RemoveServerFromClients during workload cleanup):
//     Callers log errors as warnings and continue processing other clients.
//     This allows partial success when some clients fail.
//
//   - Migrations: Errors are logged as warnings and the migration continues,
//     allowing best-effort migration of client configurations.
//
// Write failures are logged at WARN level (not ERROR) because:
//  1. The error is also returned to the caller who decides the severity
//  2. Many callers (RemoveServerFromClients, migrations) treat these as non-fatal
//  3. This avoids misleading ERROR logs for expected failure scenarios
//
// # File Locking
//
// All operations use file-based locking via withFileLock() to ensure safe concurrent
// access. Each config file has a corresponding ".lock" file that is acquired before
// any read-modify-write operation.

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/tailscale/hujson"
	"github.com/tidwall/gjson"
	"gopkg.in/yaml.v3"

	"github.com/stacklok/toolhive/pkg/fileutils"
	"github.com/stacklok/toolhive/pkg/lockfile"
)

// ConfigUpdater defines the interface for types which can edit MCP client config files.
// All methods return errors rather than handling them internally, allowing callers to
// determine the appropriate response (fatal error, warning, or ignore) based on context.
// See the package-level documentation for details on error handling patterns.
type ConfigUpdater interface {
	// Upsert inserts or updates an MCP server configuration.
	// Returns an error if the operation fails (file read/write, parsing, marshaling).
	Upsert(serverName string, data MCPServer) error

	// Remove removes an MCP server configuration.
	// Returns nil if the server doesn't exist (idempotent).
	// Returns an error only for actual failures (file read/write, parsing).
	Remove(serverName string) error
}

// MCPServer represents an MCP server in a MCP client config file
type MCPServer struct {
	Url       string `json:"url,omitempty"`
	ServerUrl string `json:"serverUrl,omitempty"`
	HttpUrl   string `json:"httpUrl,omitempty"`
	Uri       string `json:"uri,omitempty"`
	Type      string `json:"type,omitempty"`
}

// --- Shared helper functions ---

// withFileLock executes the given function while holding a file lock for the specified path.
// This is used by all config updaters (JSON, YAML, TOML) to ensure safe concurrent access.
func withFileLock(path string, fn func() error) error {
	lockPath := path + ".lock"
	fileLock := lockfile.NewTrackedLock(lockPath)

	ctx, cancel := context.WithTimeout(context.Background(), lockTimeout)
	defer cancel()

	locked, err := fileLock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		return fmt.Errorf("failed to acquire lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("failed to acquire lock: timeout after %v", lockTimeout)
	}
	defer lockfile.ReleaseTrackedLock(lockPath, fileLock)

	return fn()
}

// JSONConfigUpdater is a ConfigUpdater that is responsible for updating
// JSON config files.
type JSONConfigUpdater struct {
	Path                 string
	MCPServersPathPrefix string
}

// Upsert inserts or updates an MCP server in the MCP client config file
func (jcu *JSONConfigUpdater) Upsert(serverName string, data MCPServer) error {
	return withFileLock(jcu.Path, func() error {
		content, err := os.ReadFile(jcu.Path)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to read file: %w", err)
		}

		if len(content) == 0 {
			// If the file is empty, we need to initialize it with an empty JSON object
			content = []byte("{}")
		}

		content = ensurePathExists(content, jcu.MCPServersPathPrefix)

		v, err := hujson.Parse(content)
		if err != nil {
			return fmt.Errorf("failed to parse JSON: %w", err)
		}

		dataJSON, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("failed to marshal MCPServer to JSON: %w", err)
		}

		patch := fmt.Sprintf(`[{ "op": "add", "path": "%s/%s", "value": %s } ]`, jcu.MCPServersPathPrefix, serverName, dataJSON)
		if err := v.Patch([]byte(patch)); err != nil {
			return fmt.Errorf("failed to patch JSON: %w", err)
		}

		formatted, err := hujson.Format(v.Pack())
		if err != nil {
			return fmt.Errorf("failed to format JSON: %w", err)
		}

		// Write back to the file atomically
		if err := fileutils.AtomicWriteFile(jcu.Path, formatted, 0600); err != nil {
			slog.Warn("failed to write JSON config file", "error", err)
			return fmt.Errorf("failed to write file: %w", err)
		}

		slog.Debug("successfully updated client config file", "server", serverName)
		return nil
	})
}

// Remove removes an MCP server from the MCP client config file
func (jcu *JSONConfigUpdater) Remove(serverName string) error {
	return withFileLock(jcu.Path, func() error {
		content, err := os.ReadFile(jcu.Path)
		if err != nil {
			if os.IsNotExist(err) {
				// File doesn't exist, nothing to remove
				return nil
			}
			return fmt.Errorf("failed to read file: %w", err)
		}

		if len(content) == 0 {
			// If the file is empty, there is nothing to remove.
			return nil
		}

		v, err := hujson.Parse(content)
		if err != nil {
			return fmt.Errorf("failed to parse JSON: %w", err)
		}

		// Check if the server exists by attempting the patch and handling the error gracefully
		patch := fmt.Sprintf(`[{ "op": "remove", "path": "%s/%s" } ]`, jcu.MCPServersPathPrefix, serverName)
		if err := v.Patch([]byte(patch)); err != nil {
			// If the patch fails because the path doesn't exist, that's fine - nothing to remove
			if strings.Contains(err.Error(), "value not found") || strings.Contains(err.Error(), "path not found") {
				slog.Debug("mcpserver not found in client config file, nothing to remove", "server", serverName)
				return nil
			}
			// For other errors, return the error
			return fmt.Errorf("failed to patch JSON: %w", err)
		}

		formatted, err := hujson.Format(v.Pack())
		if err != nil {
			return fmt.Errorf("failed to format JSON: %w", err)
		}

		// Write back to the file atomically
		if err := fileutils.AtomicWriteFile(jcu.Path, formatted, 0600); err != nil {
			slog.Warn("failed to write JSON config file", "error", err)
			return fmt.Errorf("failed to write file: %w", err)
		}

		slog.Debug("successfully removed mcpserver from client config file", "server", serverName)
		return nil
	})
}

// YAMLConfigUpdater is a ConfigUpdater that is responsible for updating
// YAML config files using a converter interface for flexibility.
type YAMLConfigUpdater struct {
	Path      string
	Converter YAMLConverter
}

// Upsert inserts or updates an MCP server in the config.yaml file using the converter
func (ycu *YAMLConfigUpdater) Upsert(serverName string, data MCPServer) error {
	return withFileLock(ycu.Path, func() error {
		content, err := os.ReadFile(ycu.Path)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to read file: %w", err)
		}

		// Use a generic map to preserve all existing fields, not just extensions
		var config map[string]any

		// If file exists and is not empty, unmarshal existing config into generic map
		if len(content) > 0 {
			if err := yaml.Unmarshal(content, &config); err != nil {
				return fmt.Errorf("failed to parse existing YAML config: %w", err)
			}
		} else {
			// Initialize empty map if file doesn't exist or is empty
			config = make(map[string]any)
		}

		// Convert MCPServer using the converter
		entry, err := ycu.Converter.ConvertFromMCPServer(serverName, data)
		if err != nil {
			return fmt.Errorf("failed to convert MCPServer: %w", err)
		}

		// Upsert the entry using the converter
		if err := ycu.Converter.UpsertEntry(config, serverName, entry); err != nil {
			return fmt.Errorf("failed to upsert entry: %w", err)
		}

		// Marshal back to YAML
		updatedContent, err := yaml.Marshal(config)
		if err != nil {
			return fmt.Errorf("failed to marshal YAML: %w", err)
		}

		// Write back to file atomically
		if err := fileutils.AtomicWriteFile(ycu.Path, updatedContent, 0600); err != nil {
			slog.Warn("failed to write YAML config file", "error", err)
			return fmt.Errorf("failed to write file: %w", err)
		}

		slog.Debug("successfully updated YAML client config file", "server", serverName)
		return nil
	})
}

// Remove removes an entry from the config.yaml file using the converter
func (ycu *YAMLConfigUpdater) Remove(serverName string) error {
	return withFileLock(ycu.Path, func() error {
		// Read existing config
		content, err := os.ReadFile(ycu.Path)
		if err != nil {
			if os.IsNotExist(err) {
				// File doesn't exist, nothing to remove
				return nil
			}
			return fmt.Errorf("failed to read file: %w", err)
		}

		if len(content) == 0 {
			// File is empty, nothing to remove
			return nil
		}

		// Use a generic map to preserve all existing fields, not just extensions
		var config map[string]any
		if err := yaml.Unmarshal(content, &config); err != nil {
			return fmt.Errorf("failed to parse YAML: %w", err)
		}

		if err := ycu.Converter.RemoveEntry(config, serverName); err != nil {
			return fmt.Errorf("failed to remove entry: %w", err)
		}

		updatedContent, err := yaml.Marshal(config)
		if err != nil {
			return fmt.Errorf("failed to marshal YAML: %w", err)
		}

		// Write back to file atomically
		if err := fileutils.AtomicWriteFile(ycu.Path, updatedContent, 0600); err != nil {
			slog.Warn("failed to write YAML config file", "error", err)
			return fmt.Errorf("failed to write file: %w", err)
		}

		slog.Debug("successfully removed server from YAML config file", "server", serverName)
		return nil
	})
}

// --- Shared TOML helper functions ---

// readTOMLConfig reads and parses a TOML config file from the specified path.
func readTOMLConfig(path string) (map[string]any, error) {
	// #nosec G304 -- path is controlled by internal code (TOMLConfigUpdater/TOMLMapConfigUpdater structs)
	content, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	if len(content) == 0 {
		return make(map[string]any), nil
	}

	var config map[string]any
	if err := toml.Unmarshal(content, &config); err != nil {
		return nil, fmt.Errorf("failed to parse existing TOML config: %w", err)
	}
	return config, nil
}

// writeTOMLConfig marshals and writes the config to the specified TOML file path atomically.
func writeTOMLConfig(path string, config map[string]any) error {
	updatedContent, err := toml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal TOML: %w", err)
	}
	if err := fileutils.AtomicWriteFile(path, updatedContent, 0600); err != nil {
		slog.Warn("failed to write TOML config file", "error", err)
		return fmt.Errorf("failed to write file: %w", err)
	}
	return nil
}

// extractURLFromMCPServer extracts the URL value from an MCPServer struct,
// checking fields in priority order based on client specificity:
// Uri (Goose) → ServerUrl (Windsurf) → HttpUrl (Gemini) → Url (default/most common).
func extractURLFromMCPServer(data MCPServer) string {
	switch {
	case data.Uri != "":
		return data.Uri
	case data.ServerUrl != "":
		return data.ServerUrl
	case data.HttpUrl != "":
		return data.HttpUrl
	case data.Url != "":
		return data.Url
	default:
		return ""
	}
}

// convertToAnySlice converts various slice types to []any.
func convertToAnySlice(v any) []any {
	switch s := v.(type) {
	case []any:
		return s
	case []map[string]any:
		result := make([]any, len(s))
		for i, item := range s {
			result[i] = item
		}
		return result
	default:
		return nil
	}
}

// --- TOMLConfigUpdater (array-of-tables format) ---

// TOMLConfigUpdater is a ConfigUpdater that is responsible for updating
// TOML config files with array-of-tables format (used by Mistral Vibe).
type TOMLConfigUpdater struct {
	Path            string
	ServersKey      string // The TOML array key (e.g., "mcp_servers")
	IdentifierField string // The field name used to identify servers (e.g., "name")
	URLField        string // The field name for URL (e.g., "url")
}

// Upsert inserts or updates an MCP server in the TOML config file
func (tcu *TOMLConfigUpdater) Upsert(serverName string, data MCPServer) error {
	return withFileLock(tcu.Path, func() error {
		config, err := readTOMLConfig(tcu.Path)
		if err != nil {
			return err
		}

		servers := tcu.getServersArray(config)
		newEntry := tcu.buildServerEntry(serverName, data)
		servers = tcu.upsertServerEntry(servers, serverName, newEntry)
		config[tcu.ServersKey] = servers

		if err := writeTOMLConfig(tcu.Path, config); err != nil {
			return err
		}

		slog.Debug("successfully updated TOML client config file", "server", serverName)
		return nil
	})
}

// Remove removes an MCP server from the TOML config file
func (tcu *TOMLConfigUpdater) Remove(serverName string) error {
	return withFileLock(tcu.Path, func() error {
		config, err := readTOMLConfig(tcu.Path)
		if err != nil {
			return err
		}

		// If config is empty (file didn't exist or was empty), nothing to remove
		if len(config) == 0 {
			return nil
		}

		existingServers, ok := config[tcu.ServersKey]
		if !ok {
			return nil // No servers section, nothing to remove
		}

		servers := convertToAnySlice(existingServers)
		if servers == nil {
			return nil // Unknown format, nothing to remove
		}

		config[tcu.ServersKey] = tcu.filterOutServer(servers, serverName)

		if err := writeTOMLConfig(tcu.Path, config); err != nil {
			return err
		}

		slog.Debug("successfully removed server from TOML config file", "server", serverName)
		return nil
	})
}

// getServersArray extracts or initializes the servers array from config
func (tcu *TOMLConfigUpdater) getServersArray(config map[string]any) []any {
	existingServers, ok := config[tcu.ServersKey]
	if !ok {
		return []any{}
	}
	servers := convertToAnySlice(existingServers)
	if servers == nil {
		return []any{}
	}
	return servers
}

// upsertServerEntry updates an existing server or appends a new one
func (tcu *TOMLConfigUpdater) upsertServerEntry(servers []any, serverName string, newEntry map[string]any) []any {
	for i, s := range servers {
		if serverEntry, ok := s.(map[string]any); ok {
			if name, exists := serverEntry[tcu.IdentifierField]; exists && name == serverName {
				servers[i] = newEntry
				return servers
			}
		}
	}
	return append(servers, newEntry)
}

// filterOutServer removes the server with the given name from the slice
func (tcu *TOMLConfigUpdater) filterOutServer(servers []any, serverName string) []any {
	filtered := make([]any, 0, len(servers))
	for _, s := range servers {
		serverEntry, ok := s.(map[string]any)
		if !ok {
			filtered = append(filtered, s)
			continue
		}
		name, exists := serverEntry[tcu.IdentifierField]
		if !exists || name != serverName {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

// buildServerEntry creates a server entry map from MCPServer data
func (tcu *TOMLConfigUpdater) buildServerEntry(serverName string, data MCPServer) map[string]any {
	entry := map[string]any{
		tcu.IdentifierField: serverName,
	}

	if url := extractURLFromMCPServer(data); url != "" {
		entry[tcu.URLField] = url
	}

	// Add transport type if specified
	if data.Type != "" {
		entry["transport"] = data.Type
	}

	return entry
}

// --- TOMLMapConfigUpdater (nested tables format) ---

// TOMLMapConfigUpdater is a ConfigUpdater that is responsible for updating
// TOML config files with nested tables format [section.servername] (used by Codex).
type TOMLMapConfigUpdater struct {
	Path       string
	ServersKey string // The TOML section key (e.g., "mcp_servers")
	URLField   string // The field name for URL (e.g., "url")
}

// Upsert inserts or updates an MCP server in the TOML config file using map format
func (tmu *TOMLMapConfigUpdater) Upsert(serverName string, data MCPServer) error {
	return withFileLock(tmu.Path, func() error {
		config, err := readTOMLConfig(tmu.Path)
		if err != nil {
			return err
		}

		// Get or create the servers map
		serversMap := tmu.getServersMap(config)

		// Build the server entry (without the name field since it's the key)
		serverEntry := tmu.buildServerEntry(data)

		// Set the server entry
		serversMap[serverName] = serverEntry
		config[tmu.ServersKey] = serversMap

		if err := writeTOMLConfig(tmu.Path, config); err != nil {
			return err
		}

		slog.Debug("successfully updated TOML client config file", "server", serverName)
		return nil
	})
}

// Remove removes an MCP server from the TOML config file
func (tmu *TOMLMapConfigUpdater) Remove(serverName string) error {
	return withFileLock(tmu.Path, func() error {
		config, err := readTOMLConfig(tmu.Path)
		if err != nil {
			return err
		}

		// If config is empty (file didn't exist or was empty), nothing to remove
		if len(config) == 0 {
			return nil
		}

		serversSection, ok := config[tmu.ServersKey]
		if !ok {
			return nil // No servers section, nothing to remove
		}

		serversMap, ok := serversSection.(map[string]any)
		if !ok {
			return nil // Unknown format, nothing to remove
		}

		// Remove the server if it exists
		delete(serversMap, serverName)
		config[tmu.ServersKey] = serversMap

		if err := writeTOMLConfig(tmu.Path, config); err != nil {
			return err
		}

		slog.Debug("successfully removed server from TOML config file", "server", serverName)
		return nil
	})
}

// getServersMap extracts or initializes the servers map from config
func (tmu *TOMLMapConfigUpdater) getServersMap(config map[string]any) map[string]any {
	existingServers, ok := config[tmu.ServersKey]
	if !ok {
		return make(map[string]any)
	}
	serversMap, ok := existingServers.(map[string]any)
	if !ok {
		return make(map[string]any)
	}
	return serversMap
}

// buildServerEntry creates a server entry map from MCPServer data
func (tmu *TOMLMapConfigUpdater) buildServerEntry(data MCPServer) map[string]any {
	entry := make(map[string]any)

	if url := extractURLFromMCPServer(data); url != "" {
		entry[tmu.URLField] = url
	}

	// Add transport type if specified
	if data.Type != "" {
		entry["transport"] = data.Type
	}

	return entry
}

// ensurePathExists ensures that the path exists in the JSON content
// and returns the updated content.
// For example:
//   - if the path is "/mcp/servers",
//     the function will ensure that the path "/mcp/servers" exists
//     and returns the updated content.
//   - if the path is "/mcpServers",
//     the function will ensure that the path "/mcpServers" exists
//     and returns the updated content.
//
// This is necessary because the MCP client config file is a JSON object,
// and we need to ensure that the path exists before we can add a new key to it.
func ensurePathExists(content []byte, path string) []byte {
	// Special case: if path is root ("/"), just return everything (formatted)
	if path == "/" {
		v, _ := hujson.Parse(content)
		formatted, _ := hujson.Format(v.Pack())
		return formatted
	}

	segments := strings.Split(path, "/")

	// Navigate through the JSON structure
	var pathSoFarForPatch string
	var pathSoFarForRetrieval string
	for i, segment := range segments[:] {
		// we want to skip the first segments because it is the root
		if path[0] == '/' && (i == 0) {
			continue
		}

		// We build the path up to this segment so that we can check if it exists
		// and if it doesn't, we can create it as an empty object.
		// The "/" is added to the path for the patch operation because the path
		// is a JSON pointer, and JSON pointers are prefixed with "/".
		// The "." is added to the path for the retrieval operation.
		// - gjson (used for retrieval) treats `.` as a special (traversal) character,
		// so any json keys which contain `.` must have the `.` "escaped" with a single
		// '\'. In it, key `a.b` would be matched by `a\.b` but not `a.b`.
		// - hujson (used for the patch) treats "." and "\" as ordinary characters in a
		// json key. In it, key `a.b` would be matched by `a.b` but not `a\.b`.
		// So we need to "escape" json keys this way for retrieval, but not for patch.
		if len(pathSoFarForPatch) == 0 {
			pathSoFarForPatch = "/" + segment
			pathSoFarForRetrieval = strings.ReplaceAll(segment, ".", `\.`)
		} else {
			pathSoFarForPatch = pathSoFarForPatch + "/" + segment
			pathSoFarForRetrieval = pathSoFarForRetrieval + "." + strings.ReplaceAll(segment, ".", `\.`)
		}

		// We retrieve the segment from the content so that we can check if it exists
		// and if it doesn't, we can create it as an empty object. If it does exist,
		// we can skip the patch operation onto the next segment.
		segmentPath := gjson.GetBytes(content, pathSoFarForRetrieval).Raw
		if segmentPath != "" {
			continue
		}

		// Create a JSON patch to add an empty object at this path
		patch := fmt.Sprintf(`[{ "op": "add", "path": "%s", "value": {} }]`, pathSoFarForPatch)

		// Parse the current content and apply the patch
		v, _ := hujson.Parse(content)
		err := v.Patch([]byte(patch))
		if err != nil {
			slog.Error("failed to patch file", "error", err)
		}

		// Update the content with the patched version
		content = v.Pack()
	}
	// Parse the updated content with hujson to maintain formatting
	v, _ := hujson.Parse(content)
	formatted, _ := hujson.Format(v.Pack())
	return formatted
}

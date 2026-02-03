// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/tailscale/hujson"
	"github.com/tidwall/gjson"
	"gopkg.in/yaml.v3"

	"github.com/stacklok/toolhive/pkg/lockfile"
	"github.com/stacklok/toolhive/pkg/logger"
)

// ConfigUpdater defines the interface for types which can edit MCP client config files.
type ConfigUpdater interface {
	Upsert(serverName string, data MCPServer) error
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

// JSONConfigUpdater is a ConfigUpdater that is responsible for updating
// JSON config files.
type JSONConfigUpdater struct {
	Path                 string
	MCPServersPathPrefix string
}

// Upsert inserts or updates an MCP server in the MCP client config file
func (jcu *JSONConfigUpdater) Upsert(serverName string, data MCPServer) error {
	// Create a lock file
	lockPath := jcu.Path + ".lock"
	fileLock := lockfile.NewTrackedLock(lockPath)

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
	defer lockfile.ReleaseTrackedLock(lockPath, fileLock)

	content, err := os.ReadFile(jcu.Path)
	if err != nil {
		logger.Errorf("Failed to read file: %v", err)
	}

	if len(content) == 0 {
		// If the file is empty, we need to initialize it with an empty JSON object
		content = []byte("{}")
	}

	content = ensurePathExists(content, jcu.MCPServersPathPrefix)

	v, _ := hujson.Parse(content)

	dataJSON, err := json.Marshal(data)
	if err != nil {
		logger.Errorf("Unable to marshal the MCPServer into JSON: %v", err)
	}

	patch := fmt.Sprintf(`[{ "op": "add", "path": "%s/%s", "value": %s } ]`, jcu.MCPServersPathPrefix, serverName, dataJSON)
	err = v.Patch([]byte(patch))
	if err != nil {
		logger.Errorf("Failed to patch file: %v", err)
	}

	formatted, _ := hujson.Format(v.Pack())
	if err != nil {
		logger.Errorf("Failed to format the patched file: %v", err)
	}

	// Write back to the file
	if err := os.WriteFile(jcu.Path, formatted, 0600); err != nil {
		logger.Errorf("Failed to write file: %v", err)
	}

	logger.Debugf("Successfully updated the client config file for MCPServer %s", serverName)

	return nil
}

// Remove removes an MCP server from the MCP client config file
func (jcu *JSONConfigUpdater) Remove(serverName string) error {
	// Create a lock file
	lockPath := jcu.Path + ".lock"
	fileLock := lockfile.NewTrackedLock(lockPath)

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
	defer lockfile.ReleaseTrackedLock(lockPath, fileLock)

	content, err := os.ReadFile(jcu.Path)
	if err != nil {
		logger.Errorf("Failed to read file: %v", err)
	}

	if len(content) == 0 {
		// If the file is empty, there is nothing to remove.
		return nil
	}

	v, _ := hujson.Parse(content)

	// Check if the server exists by attempting the patch and handling the error gracefully
	patch := fmt.Sprintf(`[{ "op": "remove", "path": "%s/%s" } ]`, jcu.MCPServersPathPrefix, serverName)
	err = v.Patch([]byte(patch))
	if err != nil {
		// If the patch fails because the path doesn't exist, that's fine - nothing to remove
		if strings.Contains(err.Error(), "value not found") || strings.Contains(err.Error(), "path not found") {
			logger.Debugf("MCPServer %s not found in client config file, nothing to remove", serverName)
			return nil
		}
		// For other errors, return the error
		logger.Errorf("Failed to patch file: %v", err)
		return err
	}

	formatted, _ := hujson.Format(v.Pack())

	// Write back to the file
	if err := os.WriteFile(jcu.Path, formatted, 0600); err != nil {
		logger.Errorf("Failed to write file: %v", err)
	}

	logger.Debugf("Successfully removed the MCPServer %s from the client config file", serverName)

	return nil
}

// YAMLConfigUpdater is a ConfigUpdater that is responsible for updating
// YAML config files using a converter interface for flexibility.
type YAMLConfigUpdater struct {
	Path      string
	Converter YAMLConverter
}

// Upsert inserts or updates an MCP server in the config.yaml file using the converter
func (ycu *YAMLConfigUpdater) Upsert(serverName string, data MCPServer) error {
	// Create a lock file
	lockPath := ycu.Path + ".lock"
	fileLock := lockfile.NewTrackedLock(lockPath)

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
	defer lockfile.ReleaseTrackedLock(lockPath, fileLock)

	content, err := os.ReadFile(ycu.Path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read file: %w", err)
	}

	// Use a generic map to preserve all existing fields, not just extensions
	var config map[string]interface{}

	// If file exists and is not empty, unmarshal existing config into generic map
	if len(content) > 0 {
		err = yaml.Unmarshal(content, &config)
		if err != nil {
			return fmt.Errorf("failed to parse existing YAML config: %w", err)
		}
	} else {
		// Initialize empty map if file doesn't exist or is empty
		config = make(map[string]interface{})
	}

	// Convert MCPServer using the converter
	entry, err := ycu.Converter.ConvertFromMCPServer(serverName, data)
	if err != nil {
		return fmt.Errorf("failed to convert MCPServer: %w", err)
	}

	// Upsert the entry using the converter
	err = ycu.Converter.UpsertEntry(config, serverName, entry)
	if err != nil {
		return fmt.Errorf("failed to upsert entry: %w", err)
	}

	// Marshal back to YAML
	updatedContent, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal YAML: %w", err)
	}

	// Write back to file
	if err := os.WriteFile(ycu.Path, updatedContent, 0600); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	logger.Debugf("Successfully updated YAML client config file for server %s", serverName)
	return nil
}

// Remove removes an entry from the config.yaml file using the converter
func (ycu *YAMLConfigUpdater) Remove(serverName string) error {
	// Create a lock file
	lockPath := ycu.Path + ".lock"
	fileLock := lockfile.NewTrackedLock(lockPath)

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
	defer lockfile.ReleaseTrackedLock(lockPath, fileLock)

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
	var config map[string]interface{}
	err = yaml.Unmarshal(content, &config)
	if err != nil {
		return fmt.Errorf("failed to parse YAML: %w", err)
	}

	err = ycu.Converter.RemoveEntry(config, serverName)
	if err != nil {
		return fmt.Errorf("failed to remove entry: %w", err)
	}

	updatedContent, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal YAML: %w", err)
	}

	// Write back to file
	if err := os.WriteFile(ycu.Path, updatedContent, 0600); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	logger.Debugf("Successfully removed server %s from YAML config file", serverName)
	return nil
}

// --- Shared TOML helper functions ---

// tomlWithFileLock executes the given function while holding a file lock for the specified path.
func tomlWithFileLock(path string, fn func() error) error {
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

// writeTOMLConfig marshals and writes the config to the specified TOML file path.
func writeTOMLConfig(path string, config map[string]any) error {
	updatedContent, err := toml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal TOML: %w", err)
	}
	if err := os.WriteFile(path, updatedContent, 0600); err != nil {
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
	return tomlWithFileLock(tcu.Path, func() error {
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

		logger.Debugf("Successfully updated TOML client config file for server %s", serverName)
		return nil
	})
}

// Remove removes an MCP server from the TOML config file
func (tcu *TOMLConfigUpdater) Remove(serverName string) error {
	return tomlWithFileLock(tcu.Path, func() error {
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

		logger.Debugf("Successfully removed server %s from TOML config file", serverName)
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
	return tomlWithFileLock(tmu.Path, func() error {
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

		logger.Debugf("Successfully updated TOML client config file for server %s", serverName)
		return nil
	})
}

// Remove removes an MCP server from the TOML config file
func (tmu *TOMLMapConfigUpdater) Remove(serverName string) error {
	return tomlWithFileLock(tmu.Path, func() error {
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

		logger.Debugf("Successfully removed server %s from TOML config file", serverName)
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
			logger.Errorf("Failed to patch file: %v", err)
		}

		// Update the content with the patched version
		content = v.Pack()
	}
	// Parse the updated content with hujson to maintain formatting
	v, _ := hujson.Parse(content)
	formatted, _ := hujson.Format(v.Pack())
	return formatted
}

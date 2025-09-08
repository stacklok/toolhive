package client

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

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

	logger.Infof("Successfully updated the client config file for MCPServer %s", serverName)

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
			logger.Infof("MCPServer %s not found in client config file, nothing to remove", serverName)
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

	logger.Infof("Successfully removed the MCPServer %s from the client config file", serverName)

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

	logger.Infof("Successfully updated YAML client config file for server %s", serverName)
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

	logger.Infof("Successfully removed server %s from YAML config file", serverName)
	return nil
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

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"fmt"
)

// YAMLConverter defines an interface for converting MCPServer data to different YAML config formats
type YAMLConverter interface {
	ConvertFromMCPServer(serverName string, server MCPServer) (interface{}, error)
	UpsertEntry(config interface{}, serverName string, entry interface{}) error
	RemoveEntry(config interface{}, serverName string) error
}

// GenericYAMLConverter implements YAMLConverter using configuration from clientAppConfig
type GenericYAMLConverter struct {
	storageType     YAMLStorageType        // How servers are stored in YAML (map or array)
	serversPath     string                 // path to servers section (e.g., "extensions" or "mcpServers")
	identifierField string                 // for array type: field that identifies the server
	defaults        map[string]interface{} // default values for fields
	urlLabel        string                 // label for URL field (e.g., "url", "uri", "serverUrl")
}

// NewGenericYAMLConverter creates a converter from clientAppConfig
func NewGenericYAMLConverter(cfg *clientAppConfig) *GenericYAMLConverter {
	return &GenericYAMLConverter{
		storageType:     cfg.YAMLStorageType,
		serversPath:     extractServersKeyFromConfig(cfg),
		identifierField: cfg.YAMLIdentifierField,
		defaults:        cfg.YAMLDefaults,
		urlLabel:        extractURLLabelFromConfig(cfg),
	}
}

// ConvertFromMCPServer converts an MCPServer to the appropriate format based on configuration
func (g *GenericYAMLConverter) ConvertFromMCPServer(serverName string, server MCPServer) (interface{}, error) {
	result := make(map[string]interface{})

	// Add name field
	result["name"] = serverName

	// Handle URL field - extract from whichever MCPServer field has a value
	// and use the configured URL label for the output key.
	if url := extractURLFromMCPServer(server); url != "" {
		// Use the configured URL label (e.g., "uri" for Goose, "url" for Continue)
		if g.urlLabel != "" {
			result[g.urlLabel] = url
		} else {
			result[defaultURLFieldName] = url // Default fallback
		}
	}

	// Add type field
	if server.Type != "" {
		result["type"] = server.Type
	}

	// Apply defaults (e.g., enabled, timeout for Goose)
	for key, value := range g.defaults {
		if _, exists := result[key]; !exists {
			result[key] = value
		}
	}

	return result, nil
}

// UpsertEntry adds or updates an entry based on storage type (map or array)
func (g *GenericYAMLConverter) UpsertEntry(config interface{}, serverName string, entry interface{}) error {
	configMap, ok := config.(map[string]interface{})
	if !ok {
		return fmt.Errorf("invalid config format")
	}

	// Initialize servers section if it doesn't exist
	if configMap[g.serversPath] == nil {
		if g.storageType == YAMLStorageTypeMap {
			configMap[g.serversPath] = make(map[string]interface{})
		} else {
			configMap[g.serversPath] = []interface{}{}
		}
	}

	// Convert entry to map for YAML marshaling
	entryMap, ok := entry.(map[string]interface{})
	if !ok {
		return fmt.Errorf("entry must be a map[string]interface{}")
	}

	if g.storageType == YAMLStorageTypeMap {
		return g.upsertMapEntry(configMap, serverName, entryMap)
	}
	return g.upsertArrayEntry(configMap, serverName, entryMap)
}

// upsertMapEntry handles map-based storage (like Goose)
func (g *GenericYAMLConverter) upsertMapEntry(
	configMap map[string]interface{}, serverName string, entryMap map[string]interface{},
) error {
	servers, ok := configMap[g.serversPath].(map[string]interface{})
	if !ok {
		servers = make(map[string]interface{})
		configMap[g.serversPath] = servers
	}

	servers[serverName] = entryMap
	return nil
}

// upsertArrayEntry handles array-based storage (like Continue)
func (g *GenericYAMLConverter) upsertArrayEntry(
	configMap map[string]interface{}, serverName string, entryMap map[string]interface{},
) error {
	var servers []interface{}

	// Get the servers array, handling different types
	switch v := configMap[g.serversPath].(type) {
	case []interface{}:
		servers = v
	case []map[string]interface{}:
		servers = make([]interface{}, len(v))
		for i, s := range v {
			servers[i] = s
		}
	default:
		servers = []interface{}{}
	}

	// Find and update existing entry or append new one
	found := false
	for i, s := range servers {
		if serverEntry, ok := s.(map[string]interface{}); ok {
			if id, exists := serverEntry[g.identifierField]; exists && id == serverName {
				servers[i] = entryMap
				found = true
				break
			}
		}
	}

	if !found {
		servers = append(servers, entryMap)
	}

	configMap[g.serversPath] = servers
	return nil
}

// RemoveEntry removes an entry based on storage type
func (g *GenericYAMLConverter) RemoveEntry(config interface{}, serverName string) error {
	configMap, ok := config.(map[string]interface{})
	if !ok {
		return fmt.Errorf("invalid config format")
	}

	if configMap[g.serversPath] == nil {
		return nil // Nothing to remove
	}

	if g.storageType == YAMLStorageTypeMap {
		return g.removeMapEntry(configMap, serverName)
	}
	return g.removeArrayEntry(configMap, serverName)
}

// removeMapEntry handles removal from map-based storage
func (g *GenericYAMLConverter) removeMapEntry(configMap map[string]interface{}, serverName string) error {
	servers, ok := configMap[g.serversPath].(map[string]interface{})
	if !ok {
		return fmt.Errorf("invalid servers format")
	}

	delete(servers, serverName)
	return nil
}

// removeArrayEntry handles removal from array-based storage
func (g *GenericYAMLConverter) removeArrayEntry(configMap map[string]interface{}, serverName string) error {
	var servers []interface{}

	// Get the servers array
	switch v := configMap[g.serversPath].(type) {
	case []interface{}:
		servers = v
	case []map[string]interface{}:
		servers = make([]interface{}, len(v))
		for i, s := range v {
			servers[i] = s
		}
	default:
		return nil // Nothing to remove
	}

	// Filter out the server with matching identifier
	filtered := make([]interface{}, 0, len(servers))
	for _, s := range servers {
		if serverEntry, ok := s.(map[string]interface{}); ok {
			if name, exists := serverEntry[g.identifierField]; !exists || name != serverName {
				filtered = append(filtered, s)
			}
		} else {
			filtered = append(filtered, s)
		}
	}

	configMap[g.serversPath] = filtered
	return nil
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xeipuuv/gojsonschema"
)

const (
	// PublisherProvidedKey is the key used in _meta for publisher-provided extensions
	PublisherProvidedKey = "io.modelcontextprotocol.registry/publisher-provided"
)

//go:embed data/toolhive-legacy-registry.schema.json data/upstream-registry.schema.json data/publisher-provided.schema.json
var embeddedSchemaFS embed.FS

// ValidateRegistrySchema validates registry JSON data against the registry schema
// This validates the old ToolHive registry format (flat structure).
func ValidateRegistrySchema(registryData []byte) error {
	// Load the schema from the embedded filesystem
	schemaData, err := embeddedSchemaFS.ReadFile("data/toolhive-legacy-registry.schema.json")
	if err != nil {
		return fmt.Errorf("failed to read embedded registry schema: %w", err)
	}

	// Create schema loader from embedded data
	schemaLoader := gojsonschema.NewBytesLoader(schemaData)

	// Create document loader from registry data
	documentLoader := gojsonschema.NewBytesLoader(registryData)

	// Perform validation
	result, err := gojsonschema.Validate(schemaLoader, documentLoader)
	if err != nil {
		return fmt.Errorf("registry schema validation failed: %w", err)
	}

	// Check if validation passed
	if !result.Valid() {
		var errorMessages []string
		for _, desc := range result.Errors() {
			errorMessages = append(errorMessages, desc.String())
		}

		if len(errorMessages) == 1 {
			return fmt.Errorf("registry schema validation failed: %s", errorMessages[0])
		}

		// Format multiple errors
		resultStr := fmt.Sprintf("registry schema validation failed with %d errors:\n", len(errorMessages))
		for i, msg := range errorMessages {
			resultStr += fmt.Sprintf("  %d. %s\n", i+1, msg)
		}
		return fmt.Errorf("%s", strings.TrimSuffix(resultStr, "\n"))
	}

	return nil
}

// ValidateEmbeddedRegistry validates the embedded registry.json against the schema
func ValidateEmbeddedRegistry() error {
	// Load the embedded registry data
	registryData, err := embeddedRegistryFS.ReadFile("data/registry.json")
	if err != nil {
		return fmt.Errorf("failed to load embedded registry: %w", err)
	}

	return ValidateRegistrySchema(registryData)
}

// ValidatePublisherProvidedExtensions validates publisher-provided extension data
// against the publisher-provided.schema.json schema.
// This validates the structure of ToolHive-specific metadata placed under
// _meta["io.modelcontextprotocol.registry/publisher-provided"] in MCP server definitions.
func ValidatePublisherProvidedExtensions(extensionsData []byte) error {
	// Load the schema from the embedded filesystem
	schemaData, err := embeddedSchemaFS.ReadFile("data/publisher-provided.schema.json")
	if err != nil {
		return fmt.Errorf("failed to read embedded publisher-provided schema: %w", err)
	}

	// Create schema loader from embedded data
	schemaLoader := gojsonschema.NewBytesLoader(schemaData)

	// Create document loader from extensions data
	documentLoader := gojsonschema.NewBytesLoader(extensionsData)

	// Perform validation
	result, err := gojsonschema.Validate(schemaLoader, documentLoader)
	if err != nil {
		return fmt.Errorf("publisher-provided extensions schema validation failed: %w", err)
	}

	// Check if validation passed
	if !result.Valid() {
		var errorMessages []string
		for _, desc := range result.Errors() {
			errorMessages = append(errorMessages, desc.String())
		}

		if len(errorMessages) == 1 {
			return fmt.Errorf("publisher-provided extensions schema validation failed: %s", errorMessages[0])
		}

		// Format multiple errors
		resultStr := fmt.Sprintf("publisher-provided extensions schema validation failed with %d errors:\n", len(errorMessages))
		for i, msg := range errorMessages {
			resultStr += fmt.Sprintf("  %d. %s\n", i+1, msg)
		}
		return fmt.Errorf("%s", strings.TrimSuffix(resultStr, "\n"))
	}

	return nil
}

// ValidateUpstreamRegistry validates UpstreamRegistry JSON data against the upstream-registry.schema.json.
// This validates the complete registry structure including meta, data, servers, and groups.
// It also validates any publisher-provided extensions found in server definitions against
// the publisher-provided.schema.json schema.
func ValidateUpstreamRegistry(registryData []byte) error {
	// Load the schema from the embedded filesystem
	schemaData, err := embeddedSchemaFS.ReadFile("data/upstream-registry.schema.json")
	if err != nil {
		return fmt.Errorf("failed to read embedded registry schema: %w", err)
	}

	// Create schema loader from embedded data
	schemaLoader := gojsonschema.NewBytesLoader(schemaData)

	// Create document loader from registry data
	documentLoader := gojsonschema.NewBytesLoader(registryData)

	// Perform validation - gojsonschema automatically loads HTTP/HTTPS $ref schemas
	result, err := gojsonschema.Validate(schemaLoader, documentLoader)
	if err != nil {
		return fmt.Errorf("registry schema validation failed: %w", err)
	}

	// Check if validation passed
	if !result.Valid() {
		var errorMessages []string
		for _, desc := range result.Errors() {
			errorMessages = append(errorMessages, desc.String())
		}

		if len(errorMessages) == 1 {
			return fmt.Errorf("registry schema validation failed: %s", errorMessages[0])
		}

		// Format multiple errors
		resultStr := fmt.Sprintf("registry schema validation failed with %d errors:\n", len(errorMessages))
		for i, msg := range errorMessages {
			resultStr += fmt.Sprintf("  %d. %s\n", i+1, msg)
		}
		return fmt.Errorf("%s", strings.TrimSuffix(resultStr, "\n"))
	}

	// Also validate publisher-provided extensions in servers
	return validateRegistryExtensions(registryData)
}

// validateRegistryExtensions parses the registry and validates publisher-provided extensions in all servers
func validateRegistryExtensions(registryData []byte) error {
	var registry map[string]any
	if err := json.Unmarshal(registryData, &registry); err != nil {
		return fmt.Errorf("failed to parse registry JSON: %w", err)
	}

	data, ok := registry["data"].(map[string]any)
	if !ok {
		return nil // No data section
	}

	var errors []string

	// Validate extensions in top-level servers
	if servers, ok := data["servers"].([]any); ok {
		errors = append(errors, validateServerList(servers, "")...)
	}

	// Validate extensions in servers within groups
	if groups, ok := data["groups"].([]any); ok {
		errors = append(errors, validateGroupServers(groups)...)
	}

	return formatExtensionErrors(errors)
}

// validateGroupServers validates extensions for servers within groups
func validateGroupServers(groups []any) []string {
	var errors []string
	for _, group := range groups {
		groupMap, ok := group.(map[string]any)
		if !ok {
			continue
		}
		groupName, _ := groupMap["name"].(string)
		if groupServers, ok := groupMap["servers"].([]any); ok {
			errors = append(errors, validateServerList(groupServers, groupName)...)
		}
	}
	return errors
}

// validateServerList validates extensions for a list of servers, with optional group prefix
func validateServerList(servers []any, groupName string) []string {
	var errors []string
	for i, server := range servers {
		serverMap, ok := server.(map[string]any)
		if !ok {
			continue
		}

		serverName := getServerName(serverMap, i)
		if groupName != "" {
			serverName = fmt.Sprintf("group[%s].%s", groupName, serverName)
		}
		if err := validateServerExtensions(serverMap, serverName); err != nil {
			errors = append(errors, err.Error())
		}
	}
	return errors
}

// formatExtensionErrors formats a list of errors into a single error, or returns nil if empty
func formatExtensionErrors(errors []string) error {
	if len(errors) == 0 {
		return nil
	}
	if len(errors) == 1 {
		return fmt.Errorf("publisher-provided extensions validation failed: %s", errors[0])
	}
	resultStr := fmt.Sprintf("publisher-provided extensions validation failed with %d errors:\n", len(errors))
	for i, msg := range errors {
		resultStr += fmt.Sprintf("  %d. %s\n", i+1, msg)
	}
	return fmt.Errorf("%s", strings.TrimSuffix(resultStr, "\n"))
}

// ValidateServerJSON validates a single MCP server JSON object and optionally validates
// any publisher-provided extensions found in its _meta field.
func ValidateServerJSON(serverData []byte, validateExtensions bool) error {
	if !validateExtensions {
		// Just validate the JSON is parseable
		var server map[string]any
		if err := json.Unmarshal(serverData, &server); err != nil {
			return fmt.Errorf("invalid server JSON: %w", err)
		}
		return nil
	}

	// Parse and validate extensions
	var server map[string]any
	if err := json.Unmarshal(serverData, &server); err != nil {
		return fmt.Errorf("invalid server JSON: %w", err)
	}

	serverName := getServerName(server, 0)
	return validateServerExtensions(server, serverName)
}

// validateServerExtensions extracts and validates publisher-provided extensions from a server
func validateServerExtensions(server map[string]any, serverName string) error {
	meta, ok := server["_meta"].(map[string]any)
	if !ok {
		return nil // No _meta field, nothing to validate
	}

	publisherProvided, ok := meta[PublisherProvidedKey].(map[string]any)
	if !ok {
		return nil // No publisher-provided extensions
	}

	// Serialize the extensions and validate
	extensionsData, err := json.Marshal(publisherProvided)
	if err != nil {
		return fmt.Errorf("server %s: failed to serialize extensions: %w", serverName, err)
	}

	if err := ValidatePublisherProvidedExtensions(extensionsData); err != nil {
		return fmt.Errorf("server %s: %w", serverName, err)
	}

	return nil
}

// getServerName extracts a human-readable name for error messages
func getServerName(server map[string]any, index int) string {
	if name, ok := server["name"].(string); ok && name != "" {
		return name
	}
	return fmt.Sprintf("servers[%d]", index)
}

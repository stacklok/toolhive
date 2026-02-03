// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"embed"
	"fmt"
	"strings"

	"github.com/xeipuuv/gojsonschema"
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

// ValidateUpstreamRegistry validates UpstreamRegistry JSON data against the upstream-registry.schema.json
// This validates the complete registry structure including meta, data, servers, and groups.
// It uses gojsonschema which automatically handles HTTP/HTTPS schema references.
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

	return nil
}

package registry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// ValidateRegistrySchema validates registry JSON data against the registry schema
func ValidateRegistrySchema(registryData []byte) error {
	// Load the schema from the docs directory
	// We need to find the schema file relative to the current working directory
	schemaPath := "docs/registry/schema.json"

	// Try to find the schema file
	var schemaData []byte
	var err error

	// First try the direct path
	if _, err := os.Stat(schemaPath); err == nil {
		schemaData, err = os.ReadFile(schemaPath)
		if err != nil {
			return fmt.Errorf("failed to read registry schema: %w", err)
		}
	} else {
		// Try to find it relative to the module root
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get working directory: %w", err)
		}

		// Walk up the directory tree to find the schema
		for {
			testPath := filepath.Join(wd, schemaPath)
			// Clean and validate the path to prevent directory traversal attacks
			cleanPath := filepath.Clean(testPath)
			if !strings.HasSuffix(cleanPath, "docs/registry/schema.json") {
				return fmt.Errorf("invalid schema path detected: %s", cleanPath)
			}

			if _, err := os.Stat(cleanPath); err == nil {
				schemaData, err = os.ReadFile(cleanPath)
				if err != nil {
					return fmt.Errorf("failed to read registry schema: %w", err)
				}
				break
			}

			parent := filepath.Dir(wd)
			if parent == wd {
				return fmt.Errorf("registry schema not found: %s", schemaPath)
			}
			wd = parent
		}
	}

	// Compile the schema
	compiler := jsonschema.NewCompiler()
	schemaID := "file://local/registry-schema.json"
	if err := compiler.AddResource(schemaID, strings.NewReader(string(schemaData))); err != nil {
		return fmt.Errorf("failed to add schema resource: %w", err)
	}
	schema, err := compiler.Compile(schemaID)
	if err != nil {
		return fmt.Errorf("failed to compile registry schema: %w", err)
	}

	// Parse the registry data
	var registryDoc interface{}
	if err := json.Unmarshal(registryData, &registryDoc); err != nil {
		return fmt.Errorf("failed to parse registry data: %w", err)
	}

	// Validate the registry data against the schema
	if err := schema.Validate(registryDoc); err != nil {
		// Extract all validation errors for better user experience
		if validationErr, ok := err.(*jsonschema.ValidationError); ok {
			return formatValidationErrors(validationErr)
		}
		return fmt.Errorf("registry schema validation failed: %w", err)
	}

	return nil
}

// formatValidationErrors formats all validation errors into a comprehensive error message
func formatValidationErrors(validationErr *jsonschema.ValidationError) error {
	var errorMessages []string

	// Collect all validation errors recursively
	collectErrors(validationErr, &errorMessages)

	if len(errorMessages) == 0 {
		return fmt.Errorf("registry schema validation failed: %s", validationErr.Error())
	}

	if len(errorMessages) == 1 {
		return fmt.Errorf("registry schema validation failed: %s", errorMessages[0])
	}

	// Format multiple errors
	result := fmt.Sprintf("registry schema validation failed with %d errors:\n", len(errorMessages))
	for i, msg := range errorMessages {
		result += fmt.Sprintf("  %d. %s\n", i+1, msg)
	}

	return fmt.Errorf("%s", strings.TrimSuffix(result, "\n"))
}

// collectErrors recursively collects all validation error messages
func collectErrors(err *jsonschema.ValidationError, messages *[]string) {
	if err == nil {
		return
	}

	// If this error has causes, recurse into them instead of adding this error
	// This avoids duplicate parent/child error messages
	if len(err.Causes) > 0 {
		for _, cause := range err.Causes {
			collectErrors(cause, messages)
		}
		return
	}

	// This is a leaf error - add it if it has a meaningful message
	if err.Message != "" {
		// Create a descriptive error message with path context
		var pathStr string
		if err.InstanceLocation != "" {
			pathStr = fmt.Sprintf(" at '%s'", err.InstanceLocation)
		}

		errorMsg := fmt.Sprintf("%s%s", err.Message, pathStr)
		*messages = append(*messages, errorMsg)
	}
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

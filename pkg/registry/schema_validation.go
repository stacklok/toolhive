package registry

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

//go:embed data/schema.json
var embeddedSchemaFS embed.FS

// ValidateRegistrySchema validates registry JSON data against the registry schema
func ValidateRegistrySchema(registryData []byte) error {
	// Load the schema from the embedded filesystem
	schemaData, err := embeddedSchemaFS.ReadFile("data/schema.json")
	if err != nil {
		return fmt.Errorf("failed to read embedded registry schema: %w", err)
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

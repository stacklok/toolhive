// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package composer provides composite tool workflow execution for Virtual MCP Server.
package composer

import (
	"fmt"

	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

const (
	// maxOutputPropertyDepth is the maximum nesting depth for output properties.
	// This prevents resource exhaustion from deeply nested configurations.
	maxOutputPropertyDepth = 10
)

// ValidateOutputConfig validates an output configuration at definition load time.
// This performs structural validation of the output schema including:
// - Type validation (valid JSON Schema types)
// - Required field validation (all required fields exist in properties)
// - Mutual exclusivity of Value and Properties fields
// - Maximum nesting depth enforcement
// - Template syntax validation (basic check for valid Go template syntax)
func ValidateOutputConfig(output *config.OutputConfig) error {
	if output == nil {
		return nil // Output is optional
	}

	if len(output.Properties) == 0 {
		return NewValidationError("output.properties", "output properties cannot be empty", nil)
	}

	// Validate that all required fields exist in properties
	for _, requiredField := range output.Required {
		if _, exists := output.Properties[requiredField]; !exists {
			return NewValidationError("output.required",
				fmt.Sprintf("required field %q does not exist in properties", requiredField),
				nil)
		}
	}

	// Validate each property
	for propertyName, property := range output.Properties {
		if err := validateOutputProperty(propertyName, property, 0); err != nil {
			return err
		}
	}

	return nil
}

// validateOutputProperty validates a single output property recursively.
//
//nolint:gocyclo // Validation logic naturally has many branches
func validateOutputProperty(name string, prop config.OutputProperty, depth int) error {
	// Check maximum nesting depth
	if depth > maxOutputPropertyDepth {
		return NewValidationError("output.properties",
			fmt.Sprintf("property %q exceeds maximum nesting depth %d", name, maxOutputPropertyDepth),
			nil)
	}

	// Validate type
	if prop.Type == "" {
		return NewValidationError("output.properties.type",
			fmt.Sprintf("property %q is missing required field 'type'", name),
			nil)
	}

	// Validate type is a valid JSON Schema type
	validTypes := map[string]bool{
		"string":  true,
		"integer": true,
		"number":  true,
		"boolean": true,
		"object":  true,
		"array":   true,
	}
	if !validTypes[prop.Type] {
		return NewValidationError("output.properties.type",
			fmt.Sprintf("property %q has invalid type %q (must be one of: string, integer, number, boolean, object, array)",
				name, prop.Type),
			nil)
	}

	// Validate description
	if prop.Description == "" {
		return NewValidationError("output.properties.description",
			fmt.Sprintf("property %q is missing required field 'description'", name),
			nil)
	}

	// Validate mutual exclusivity of Value and Properties
	hasValue := prop.Value != ""
	hasProperties := len(prop.Properties) > 0

	if hasValue && hasProperties {
		return NewValidationError("output.properties",
			fmt.Sprintf("property %q cannot have both 'value' and 'properties' fields", name),
			nil)
	}

	if !hasValue && !hasProperties {
		return NewValidationError("output.properties",
			fmt.Sprintf("property %q must have either 'value' or 'properties' field", name),
			nil)
	}

	// Type-specific validation
	if prop.Type == "object" {
		// For object types, either Value or Properties is allowed
		if hasValue {
			// Value should be a template that produces JSON
			// We'll validate this at runtime when we expand the template
		} else if hasProperties {
			// Recursively validate nested properties
			for nestedName, nestedProp := range prop.Properties {
				if err := validateOutputProperty(
					fmt.Sprintf("%s.%s", name, nestedName),
					nestedProp,
					depth+1,
				); err != nil {
					return err
				}
			}
		}
	} else {
		// For non-object types, Value is required
		if !hasValue {
			return NewValidationError("output.properties.value",
				fmt.Sprintf("property %q with type %q must have 'value' field", name, prop.Type),
				nil)
		}
		// Properties should not be set for non-object types
		if hasProperties {
			return NewValidationError("output.properties",
				fmt.Sprintf("property %q with type %q cannot have 'properties' field", name, prop.Type),
				nil)
		}
	}

	// Validate template syntax in Value field (basic check)
	if hasValue {
		if err := validateTemplateSyntax(prop.Value); err != nil {
			return NewValidationError("output.properties.value",
				fmt.Sprintf("property %q has invalid template syntax: %v", name, err),
				err)
		}
	}

	// Validate default value type matches declared type
	if !prop.Default.IsEmpty() {
		defaultVal, err := prop.Default.ToAny()
		if err != nil {
			return fmt.Errorf("output property %q: failed to parse default value: %w", name, err)
		}
		if err := validateDefaultValueType(defaultVal, prop.Type, name); err != nil {
			return err
		}
	}

	return nil
}

// validateTemplateSyntax performs basic template syntax validation.
// This doesn't validate template variable references (like .steps.foo.output)
// since those depend on runtime workflow structure. This is validated separately.
func validateTemplateSyntax(tmpl string) error {
	// Basic validation: check for balanced {{ }} braces
	depth := 0
	inTemplate := false
	prevChar := rune(0)

	for _, char := range tmpl {
		if char == '{' && prevChar == '{' {
			if inTemplate {
				return fmt.Errorf("nested template delimiters not allowed")
			}
			inTemplate = true
			depth++
		} else if char == '}' && prevChar == '}' {
			if !inTemplate {
				return fmt.Errorf("unmatched closing template delimiter")
			}
			inTemplate = false
			depth--
		}
		prevChar = char
	}

	if depth != 0 {
		return fmt.Errorf("unbalanced template delimiters")
	}

	return nil
}

// validateDefaultValueType validates that a default value is compatible with the declared type.
// This performs basic type checking to catch configuration errors early.
func validateDefaultValueType(defaultVal any, targetType string, propertyName string) error {
	switch targetType {
	case "string":
		// Strings accept any type (will be converted via fmt.Sprintf)
		return nil

	case "integer":
		// Accept integer types and numeric types that can be converted
		switch defaultVal.(type) {
		case int, int32, int64, float32, float64, string:
			return nil
		default:
			return NewValidationError("output.properties.default",
				fmt.Sprintf("property %q has default value of type %T, expected integer-compatible type", propertyName, defaultVal),
				nil)
		}

	case "number":
		// Accept numeric types
		switch defaultVal.(type) {
		case float32, float64, int, int32, int64, string:
			return nil
		default:
			return NewValidationError("output.properties.default",
				fmt.Sprintf("property %q has default value of type %T, expected number-compatible type", propertyName, defaultVal),
				nil)
		}

	case "boolean":
		// Accept boolean types and convertible types
		switch defaultVal.(type) {
		case bool, int, int32, int64, string:
			return nil
		default:
			return NewValidationError("output.properties.default",
				fmt.Sprintf("property %q has default value of type %T, expected boolean-compatible type", propertyName, defaultVal),
				nil)
		}

	case "object":
		// Accept map or string (JSON)
		switch defaultVal.(type) {
		case map[string]any, string:
			return nil
		default:
			return NewValidationError("output.properties.default",
				fmt.Sprintf("property %q has default value of type %T, expected object or JSON string",
					propertyName, defaultVal),
				nil)
		}

	case "array":
		// Accept slice or string (JSON)
		switch defaultVal.(type) {
		case []any, string:
			return nil
		default:
			return NewValidationError("output.properties.default",
				fmt.Sprintf("property %q has default value of type %T, expected array ([]any) or JSON string", propertyName, defaultVal),
				nil)
		}

	default:
		return NewValidationError("output.properties.type",
			fmt.Sprintf("property %q has unsupported type %q", propertyName, targetType),
			nil)
	}
}

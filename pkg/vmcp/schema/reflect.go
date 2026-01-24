// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// GenerateSchema generates a JSON Schema from a Go struct type using reflection.
//
// The function inspects struct tags to determine:
//   - json: Field name in the schema (uses json tag name)
//   - description: Field description (from `description` tag)
//   - omitempty: Whether the field is optional (not in required array)
//
// Supported types:
//   - string -> {"type": "string"}
//   - int, int64, etc. -> {"type": "integer"}
//   - float64, float32 -> {"type": "number"}
//   - bool -> {"type": "boolean"}
//   - []T -> {"type": "array", "items": {...}}
//   - map[string]any -> {"type": "object"}
//   - struct -> {"type": "object", "properties": {...}}
//
// Example:
//
//	type FindToolInput struct {
//	    ToolDescription string   `json:"tool_description" description:"Natural language description"`
//	    ToolKeywords    []string `json:"tool_keywords,omitempty" description:"Optional keywords"`
//	}
//	schema := GenerateSchema[FindToolInput]()
func GenerateSchema[T any]() (map[string]any, error) {
	var zero T
	t := reflect.TypeOf(zero)
	return generateSchemaForType(t)
}

// generateSchemaForType generates schema for a reflect.Type.
func generateSchemaForType(t reflect.Type) (map[string]any, error) {
	// Handle pointer types
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	switch t.Kind() {
	case reflect.Struct:
		return generateObjectSchema(t)
	case reflect.String:
		return map[string]any{"type": "string"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Uintptr:
		return map[string]any{"type": "integer"}, nil
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}, nil
	case reflect.Bool:
		return map[string]any{"type": "boolean"}, nil
	case reflect.Slice, reflect.Array:
		items, err := generateSchemaForType(t.Elem())
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"type":  "array",
			"items": items,
		}, nil
	case reflect.Map:
		// For map[string]any, just return object type
		return map[string]any{"type": "object"}, nil
	case reflect.Pointer, reflect.Interface,
		reflect.UnsafePointer, reflect.Chan, reflect.Func,
		reflect.Complex64, reflect.Complex128,
		reflect.Invalid:
		return nil, fmt.Errorf("unsupported type: %s", t.Kind())
	default:
		// Should never happen, but appease the linter.
		return nil, fmt.Errorf("unsupported type: %s", t.Kind())
	}
}

// generateObjectSchema generates schema for a struct type.
func generateObjectSchema(t reflect.Type) (map[string]any, error) {
	properties := make(map[string]any)
	var required []string

	for i := range t.NumField() {
		field := t.Field(i)

		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

		// Get JSON tag for field name
		jsonTag := field.Tag.Get("json")
		if jsonTag == "-" {
			continue
		}

		// Parse json tag (name,omitempty)
		jsonName, isOptional := parseJSONTag(jsonTag)
		if jsonName == "" {
			jsonName = field.Name
		}

		// Generate schema for field type
		fieldSchema, err := generateSchemaForType(field.Type)
		if err != nil {
			return nil, err
		}

		// Add description if present
		if desc := field.Tag.Get("description"); desc != "" {
			fieldSchema["description"] = desc
		}

		properties[jsonName] = fieldSchema

		// Add to required if not optional
		if !isOptional {
			required = append(required, jsonName)
		}
	}

	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}

	if len(required) > 0 {
		schema["required"] = required
	}

	return schema, nil
}

// parseJSONTag parses a json struct tag and returns the field name and whether it's optional.
func parseJSONTag(tag string) (name string, optional bool) {
	if tag == "" {
		return "", false
	}

	parts := strings.Split(tag, ",")
	name = parts[0]

	for _, part := range parts[1:] {
		if part == "omitempty" {
			optional = true
		}
	}

	return name, optional
}

// Translate converts an untyped input (typically map[string]any from MCP request arguments)
// to a typed struct using JSON marshalling/unmarshalling.
//
// This provides a simple, reliable way to convert MCP tool arguments to typed Go structs
// without manual field-by-field extraction.
//
// Example:
//
//	args := request.Params.Arguments // map[string]any
//	input, err := Translate[FindToolInput](args)
//	if err != nil {
//	    return nil, fmt.Errorf("invalid arguments: %w", err)
//	}
func Translate[T any](input any) (T, error) {
	var result T

	// Marshal to JSON
	data, err := json.Marshal(input)
	if err != nil {
		return result, fmt.Errorf("failed to marshal input: %w", err)
	}

	// Unmarshal to typed struct
	if err := json.Unmarshal(data, &result); err != nil {
		return result, fmt.Errorf("failed to unmarshal to %T: %w", result, err)
	}

	return result, nil
}

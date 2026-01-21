// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package schema provides typed JSON Schema structures for type coercion and defaults.
//
// This package wraps raw JSON Schema maps (map[string]any) into typed structures
// that provide type-safe operations like coercion and default value application.
package schema

// TypeCoercer coerces values to their expected types.
// Implementations return the coerced value, or the original if coercion fails.
type TypeCoercer interface {
	TryCoerce(value any) any
}

// MakeSchema parses a raw JSON Schema map into typed Schema structures.
// Always returns a valid TypeCoercer; callers do not need to nil-check.
// For nil, empty, or unknown schema types, returns a passthrough coercer
// that returns values unchanged.
func MakeSchema(raw map[string]any) TypeCoercer {
	if len(raw) == 0 {
		return passthroughSchema{}
	}

	schemaType, _ := raw["type"].(string)

	switch schemaType {
	case "object":
		return makeObjectSchema(raw)
	case "array":
		return makeArraySchema(raw)
	case "string", "integer", "number", "boolean":
		return makePrimitiveSchema(schemaType)
	default:
		return passthroughSchema{}
	}
}

// passthroughSchema is a no-op schema that returns values unchanged.
type passthroughSchema struct{}

// TryCoerce returns the value unchanged.
func (passthroughSchema) TryCoerce(value any) any {
	return value
}

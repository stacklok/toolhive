// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"log/slog"
	"strconv"
)

// primitiveSchema represents string/integer/number/boolean types.
type primitiveSchema struct {
	schemaType string
}

// makePrimitiveSchema creates a primitiveSchema from a raw JSON Schema map.
func makePrimitiveSchema(schemaType string) primitiveSchema {
	schema := primitiveSchema{
		schemaType: schemaType,
	}

	return schema
}

// TryCoerce coerces a value to the expected primitive type.
// String values are converted to integer/number/boolean as specified.
// Non-string values pass through unchanged.
// Returns the original value if coercion fails.
func (s primitiveSchema) TryCoerce(value any) any {
	str, ok := value.(string)
	if !ok {
		// Non-string values pass through unchanged
		return value
	}

	switch s.schemaType {
	case "string":
		return value

	case "integer":
		v, err := strconv.ParseInt(str, 10, 64)
		if err != nil {
			slog.Debug("Failed to coerce to integer", "value", str, "error", err)
			return value
		}
		return v

	case "number":
		v, err := strconv.ParseFloat(str, 64)
		if err != nil {
			slog.Debug("Failed to coerce to number", "value", str, "error", err)
			return value
		}
		return v

	case "boolean":
		b, err := strconv.ParseBool(str)
		if err != nil {
			slog.Debug("Failed to coerce to boolean", "value", str, "error", err)
			return value
		}
		return b
	}

	return value
}

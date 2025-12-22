package schema

import (
	"strconv"
)

// primitiveSchema represents string/integer/number/boolean types.
type primitiveSchema struct {
	schemaType   string
	defaultValue any
}

// makePrimitiveSchema creates a primitiveSchema from a raw JSON Schema map.
func makePrimitiveSchema(raw map[string]any, schemaType string) primitiveSchema {
	schema := primitiveSchema{
		schemaType: schemaType,
	}

	if defaultVal, hasDefault := raw["default"]; hasDefault {
		schema.defaultValue = defaultVal
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
		if v, err := strconv.ParseInt(str, 10, 64); err == nil {
			return v
		}

	case "number":
		if v, err := strconv.ParseFloat(str, 64); err == nil {
			return v
		}

	case "boolean":
		switch str {
		case "true", "True", "TRUE", "1":
			return true
		case "false", "False", "FALSE", "0":
			return false
		}
	}

	// Coercion failed, return original value
	return value
}

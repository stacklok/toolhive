package schema

// objectSchema represents a JSON Schema object type.
type objectSchema struct {
	properties map[string]TypeCoercer
	defaults   map[string]any
}

// makeObjectSchema creates an objectSchema from a raw JSON Schema map.
func makeObjectSchema(raw map[string]any) objectSchema {
	schema := objectSchema{
		properties: make(map[string]TypeCoercer),
		defaults:   make(map[string]any),
	}

	properties, ok := raw["properties"].(map[string]any)
	if !ok {
		return schema
	}

	for propName, propSchema := range properties {
		propMap, ok := propSchema.(map[string]any)
		if !ok {
			continue
		}

		// Recursively create schema for this property
		schema.properties[propName] = MakeSchema(propMap)

		// Extract default value if present
		if defaultVal, hasDefault := propMap["default"]; hasDefault {
			schema.defaults[propName] = defaultVal
		}
	}

	return schema
}

// TryCoerce coerces object properties to their expected types.
// Each property is coerced independently according to its schema.
// Properties without a schema pass through unchanged.
//
// Partial coercion behavior: If coercion fails for one property, other
// properties are still coerced. Failed properties retain their original value.
// For example, given {"count": "abc", "enabled": "true"} with integer/boolean
// schema, the result is {"count": "abc", "enabled": true} - count stays as
// string (invalid integer) while enabled is coerced to boolean.
//
// Returns a new map; the original is not modified.
func (s objectSchema) TryCoerce(value any) any {
	obj, ok := value.(map[string]any)
	if !ok {
		return value
	}

	result := make(map[string]any, len(obj))
	for key, val := range obj {
		if propSchema, exists := s.properties[key]; exists {
			result[key] = propSchema.TryCoerce(val)
		} else {
			result[key] = val
		}
	}

	return result
}

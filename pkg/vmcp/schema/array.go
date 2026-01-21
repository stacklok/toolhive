// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package schema

// arraySchema represents an array type with item schema.
type arraySchema struct {
	items TypeCoercer
}

// makeArraySchema creates an arraySchema from a raw JSON Schema map.
func makeArraySchema(raw map[string]any) arraySchema {
	schema := arraySchema{}

	items, ok := raw["items"].(map[string]any)
	if ok {
		schema.items = MakeSchema(items)
	} else {
		schema.items = passthroughSchema{}
	}

	return schema
}

// TryCoerce coerces array elements to their expected types.
// Each element is coerced independently according to the items schema.
//
// Partial coercion behavior: If coercion fails for one element, other
// elements are still coerced. Failed elements retain their original value.
//
// Returns a new slice; the original is not modified.
func (s arraySchema) TryCoerce(value any) any {
	arr, ok := value.([]any)
	if !ok {
		return value
	}

	result := make([]any, len(arr))
	for i, elem := range arr {
		result[i] = s.items.TryCoerce(elem)
	}

	return result
}

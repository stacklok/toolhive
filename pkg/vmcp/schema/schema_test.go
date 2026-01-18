package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMakeSchemaAndTryCoerce(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		schema   map[string]any
		input    any
		expected any
	}{
		// Primitive types - string
		{
			name:     "string stays string",
			schema:   map[string]any{"type": "string"},
			input:    "hello",
			expected: "hello",
		},
		{
			name:     "string non-string passthrough",
			schema:   map[string]any{"type": "string"},
			input:    42,
			expected: 42,
		},

		// Primitive types - integer
		{
			name:     "integer from string",
			schema:   map[string]any{"type": "integer"},
			input:    "123",
			expected: int64(123),
		},
		{
			name:     "negative integer from string",
			schema:   map[string]any{"type": "integer"},
			input:    "-42",
			expected: int64(-42),
		},
		{
			name:     "integer invalid string preserved",
			schema:   map[string]any{"type": "integer"},
			input:    "not_a_number",
			expected: "not_a_number",
		},
		{
			name:     "integer non-string passthrough",
			schema:   map[string]any{"type": "integer"},
			input:    42,
			expected: 42,
		},

		// Primitive types - number
		{
			name:     "number from string float",
			schema:   map[string]any{"type": "number"},
			input:    "3.14",
			expected: 3.14,
		},
		{
			name:     "number from string int",
			schema:   map[string]any{"type": "number"},
			input:    "42",
			expected: float64(42),
		},
		{
			name:     "number invalid string preserved",
			schema:   map[string]any{"type": "number"},
			input:    "invalid",
			expected: "invalid",
		},

		// Primitive types - boolean
		{
			name:     "boolean true lowercase",
			schema:   map[string]any{"type": "boolean"},
			input:    "true",
			expected: true,
		},
		{
			name:     "boolean false lowercase",
			schema:   map[string]any{"type": "boolean"},
			input:    "false",
			expected: false,
		},
		{
			name:     "boolean True mixed case",
			schema:   map[string]any{"type": "boolean"},
			input:    "True",
			expected: true,
		},
		{
			name:     "boolean FALSE uppercase",
			schema:   map[string]any{"type": "boolean"},
			input:    "FALSE",
			expected: false,
		},
		{
			name:     "boolean 1",
			schema:   map[string]any{"type": "boolean"},
			input:    "1",
			expected: true,
		},
		{
			name:     "boolean 0",
			schema:   map[string]any{"type": "boolean"},
			input:    "0",
			expected: false,
		},
		{
			name:     "boolean invalid preserved",
			schema:   map[string]any{"type": "boolean"},
			input:    "maybe",
			expected: "maybe",
		},
		{
			name:     "boolean non-string passthrough",
			schema:   map[string]any{"type": "boolean"},
			input:    true,
			expected: true,
		},

		// Object types
		{
			name: "object coerces properties",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"count":   map[string]any{"type": "integer"},
					"enabled": map[string]any{"type": "boolean"},
					"name":    map[string]any{"type": "string"},
				},
			},
			input: map[string]any{
				"count":   "42",
				"enabled": "true",
				"name":    "test",
			},
			expected: map[string]any{
				"count":   int64(42),
				"enabled": true,
				"name":    "test",
			},
		},
		{
			name: "object unknown properties pass through",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"known": map[string]any{"type": "integer"},
				},
			},
			input: map[string]any{
				"known":   "123",
				"unknown": "value",
			},
			expected: map[string]any{
				"known":   int64(123),
				"unknown": "value",
			},
		},
		{
			name: "object non-object passthrough",
			schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
			input:    "not an object",
			expected: "not an object",
		},

		// Nested object
		{
			name: "nested object coercion",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"config": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"timeout": map[string]any{"type": "integer"},
							"retries": map[string]any{"type": "integer"},
						},
					},
				},
			},
			input: map[string]any{
				"config": map[string]any{
					"timeout": "30",
					"retries": "3",
				},
			},
			expected: map[string]any{
				"config": map[string]any{
					"timeout": int64(30),
					"retries": int64(3),
				},
			},
		},

		// Array types
		{
			name: "array of integers",
			schema: map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "integer"},
			},
			input:    []any{"1", "2", "3"},
			expected: []any{int64(1), int64(2), int64(3)},
		},
		{
			name: "array of booleans",
			schema: map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "boolean"},
			},
			input:    []any{"true", "false", "1", "0"},
			expected: []any{true, false, true, false},
		},
		{
			name: "array nil items passthrough",
			schema: map[string]any{
				"type": "array",
			},
			input:    []any{"1", "2", "3"},
			expected: []any{"1", "2", "3"},
		},
		{
			name: "array non-array passthrough",
			schema: map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "integer"},
			},
			input:    "not an array",
			expected: "not an array",
		},
		{
			name: "array of objects",
			schema: map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":     map[string]any{"type": "integer"},
						"active": map[string]any{"type": "boolean"},
					},
				},
			},
			input: []any{
				map[string]any{"id": "1", "active": "true"},
				map[string]any{"id": "2", "active": "false"},
			},
			expected: []any{
				map[string]any{"id": int64(1), "active": true},
				map[string]any{"id": int64(2), "active": false},
			},
		},

		// Edge cases
		{
			name:     "nil schema returns nil coercer",
			schema:   nil,
			input:    "test",
			expected: "test", // nil coercer means input passes through
		},
		{
			name:     "empty schema returns nil coercer",
			schema:   map[string]any{},
			input:    "test",
			expected: "test",
		},
		{
			name:     "unknown type returns nil coercer",
			schema:   map[string]any{"type": "unknown"},
			input:    "test",
			expected: "test",
		},

		// GitHub issue #3113 example
		{
			name: "GitHub issue 3113 - issue_number coercion",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"method":       map[string]any{"type": "string"},
					"owner":        map[string]any{"type": "string"},
					"repo":         map[string]any{"type": "string"},
					"issue_number": map[string]any{"type": "integer"},
				},
			},
			input: map[string]any{
				"method":       "get",
				"owner":        "stacklok",
				"repo":         "toolhive",
				"issue_number": "3113",
			},
			expected: map[string]any{
				"method":       "get",
				"owner":        "stacklok",
				"repo":         "toolhive",
				"issue_number": int64(3113),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			coercer := MakeSchema(tt.schema)
			got := coercer.TryCoerce(tt.input)

			require.Equal(t, tt.expected, got)
		})
	}
}

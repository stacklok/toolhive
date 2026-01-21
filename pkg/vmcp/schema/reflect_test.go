// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateSchema_AllTypes(t *testing.T) {
	t.Parallel()

	type TestStruct struct {
		StringField string            `json:"string_field,omitempty"`
		IntField    int               `json:"int_field"`
		FloatField  float64           `json:"float_field,omitempty"`
		BoolField   bool              `json:"bool_field"`
		OptionalStr string            `json:"optional_str,omitempty"`
		SliceField  []int             `json:"slice_field"`
		MapField    map[string]string `json:"map_field"`
		StructField struct {
			RequiredField string `json:"field"`
			OptionalField string `json:"optional_field,omitempty"`
		} `json:"struct_field"`
		PointerField *int `json:"pointer_field"`
	}

	expected := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"string_field": map[string]any{"type": "string"},
			"int_field":    map[string]any{"type": "integer"},
			"float_field":  map[string]any{"type": "number"},
			"bool_field":   map[string]any{"type": "boolean"},
			"optional_str": map[string]any{"type": "string"},
			"slice_field": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "integer"},
			},
			"map_field": map[string]any{"type": "object"},
			"struct_field": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"field":          map[string]any{"type": "string"},
					"optional_field": map[string]any{"type": "string"},
				},
				"required": []string{"field"},
			},
			"pointer_field": map[string]any{
				"type": "integer",
			},
		},
		"required": []string{
			"int_field",
			"bool_field",
			"map_field",
			"struct_field",
			"pointer_field",
			"slice_field",
		},
	}

	actual, err := GenerateSchema[TestStruct]()
	require.NoError(t, err)

	require.Equal(t, expected["type"], actual["type"])
	require.Equal(t, expected["properties"], actual["properties"])
	require.ElementsMatch(t, expected["required"], actual["required"])
}

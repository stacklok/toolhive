// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// FindToolInput represents the input schema for optim_find_tool
// This matches the schema defined in pkg/vmcp/optimizer/optimizer.go
type FindToolInput struct {
	ToolDescription string `json:"tool_description" description:"Natural language description of the tool you're looking for"`
	ToolKeywords    string `json:"tool_keywords,omitempty" description:"Optional space-separated keywords for keyword-based search"`
	Limit           int    `json:"limit,omitempty" description:"Maximum number of tools to return (default: 10)"`
}

// CallToolInput represents the input schema for optim_call_tool
// This matches the schema defined in pkg/vmcp/optimizer/optimizer.go
type CallToolInput struct {
	BackendID  string         `json:"backend_id" description:"Backend ID from find_tool results"`
	ToolName   string         `json:"tool_name" description:"Tool name to invoke"`
	Parameters map[string]any `json:"parameters" description:"Parameters to pass to the tool"`
}

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

func TestGenerateSchema_FindToolInput(t *testing.T) {
	t.Parallel()

	expected := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tool_description": map[string]any{
				"type":        "string",
				"description": "Natural language description of the tool you're looking for",
			},
			"tool_keywords": map[string]any{
				"type":        "string",
				"description": "Optional space-separated keywords for keyword-based search",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum number of tools to return (default: 10)",
			},
		},
		"required": []string{"tool_description"},
	}

	actual, err := GenerateSchema[FindToolInput]()
	require.NoError(t, err)

	require.Equal(t, expected, actual)
}

func TestGenerateSchema_CallToolInput(t *testing.T) {
	t.Parallel()

	expected := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"backend_id": map[string]any{
				"type":        "string",
				"description": "Backend ID from find_tool results",
			},
			"tool_name": map[string]any{
				"type":        "string",
				"description": "Tool name to invoke",
			},
			"parameters": map[string]any{
				"type":        "object",
				"description": "Parameters to pass to the tool",
			},
		},
		"required": []string{"backend_id", "tool_name", "parameters"},
	}

	actual, err := GenerateSchema[CallToolInput]()
	require.NoError(t, err)

	require.Equal(t, expected, actual)
}

func TestTranslate_FindToolInput(t *testing.T) {
	t.Parallel()

	input := map[string]any{
		"tool_description": "find a tool to read files",
		"tool_keywords":    "file read",
		"limit":            5,
	}

	result, err := Translate[FindToolInput](input)
	require.NoError(t, err)

	require.Equal(t, FindToolInput{
		ToolDescription: "find a tool to read files",
		ToolKeywords:    "file read",
		Limit:           5,
	}, result)
}

func TestTranslate_CallToolInput(t *testing.T) {
	t.Parallel()

	input := map[string]any{
		"backend_id": "backend-123",
		"tool_name":  "read_file",
		"parameters": map[string]any{
			"path": "/etc/hosts",
		},
	}

	result, err := Translate[CallToolInput](input)
	require.NoError(t, err)

	require.Equal(t, CallToolInput{
		BackendID:  "backend-123",
		ToolName:   "read_file",
		Parameters: map[string]any{"path": "/etc/hosts"},
	}, result)
}

func TestTranslate_PartialInput(t *testing.T) {
	t.Parallel()

	input := map[string]any{
		"tool_description": "find a file reader",
	}

	result, err := Translate[FindToolInput](input)
	require.NoError(t, err)

	require.Equal(t, FindToolInput{
		ToolDescription: "find a file reader",
		ToolKeywords:    "",
		Limit:           0,
	}, result)
}

func TestTranslate_InvalidInput(t *testing.T) {
	t.Parallel()

	input := make(chan int)

	_, err := Translate[FindToolInput](input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to marshal input")
}

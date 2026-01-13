package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
)

func TestGenerateSchema_FindToolInput(t *testing.T) {
	t.Parallel()

	expected := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tool_description": map[string]any{
				"type":        "string",
				"description": "Natural language description of the tool to find",
			},
			"tool_keywords": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Optional keywords to narrow search",
			},
		},
		"required": []string{"tool_description"},
	}

	actual := GenerateSchema[optimizer.FindToolInput]()

	require.Equal(t, expected, actual)
}

func TestGenerateSchema_CallToolInput(t *testing.T) {
	t.Parallel()

	expected := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tool_name": map[string]any{
				"type":        "string",
				"description": "Name of the tool to call",
			},
			"parameters": map[string]any{
				"type":        "object",
				"description": "Parameters to pass to the tool",
			},
		},
		"required": []string{"tool_name", "parameters"},
	}

	actual := GenerateSchema[optimizer.CallToolInput]()

	require.Equal(t, expected, actual)
}

func TestTranslate_FindToolInput(t *testing.T) {
	t.Parallel()

	input := map[string]any{
		"tool_description": "find a tool to read files",
		"tool_keywords":    []any{"file", "read"},
	}

	result, err := Translate[optimizer.FindToolInput](input)
	require.NoError(t, err)

	require.Equal(t, "find a tool to read files", result.ToolDescription)
	require.Equal(t, []string{"file", "read"}, result.ToolKeywords)
}

func TestTranslate_CallToolInput(t *testing.T) {
	t.Parallel()

	input := map[string]any{
		"tool_name": "read_file",
		"parameters": map[string]any{
			"path": "/etc/hosts",
		},
	}

	result, err := Translate[optimizer.CallToolInput](input)
	require.NoError(t, err)

	require.Equal(t, "read_file", result.ToolName)
	require.Equal(t, map[string]any{"path": "/etc/hosts"}, result.Parameters)
}

func TestTranslate_PartialInput(t *testing.T) {
	t.Parallel()

	input := map[string]any{
		"tool_description": "find a file reader",
	}

	result, err := Translate[optimizer.FindToolInput](input)
	require.NoError(t, err)

	require.Equal(t, "find a file reader", result.ToolDescription)
	require.Nil(t, result.ToolKeywords)
}

func TestTranslate_InvalidInput(t *testing.T) {
	t.Parallel()

	input := make(chan int)

	_, err := Translate[optimizer.FindToolInput](input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to marshal input")
}

func TestGenerateSchema_PrimitiveTypes(t *testing.T) {
	t.Parallel()

	type TestStruct struct {
		StringField string  `json:"string_field"`
		IntField    int     `json:"int_field"`
		FloatField  float64 `json:"float_field"`
		BoolField   bool    `json:"bool_field"`
		OptionalStr string  `json:"optional_str,omitempty"`
	}

	expected := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"string_field": map[string]any{"type": "string"},
			"int_field":    map[string]any{"type": "integer"},
			"float_field":  map[string]any{"type": "number"},
			"bool_field":   map[string]any{"type": "boolean"},
			"optional_str": map[string]any{"type": "string"},
		},
		"required": []string{"string_field", "int_field", "float_field", "bool_field"},
	}

	actual := GenerateSchema[TestStruct]()

	require.Equal(t, expected, actual)
}

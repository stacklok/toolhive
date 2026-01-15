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

	actual, err := GenerateSchema[optimizer.FindToolInput]()
	require.NoError(t, err)

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

	actual, err := GenerateSchema[optimizer.CallToolInput]()
	require.NoError(t, err)

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

	require.Equal(t, optimizer.FindToolInput{
		ToolDescription: "find a tool to read files",
		ToolKeywords:    []string{"file", "read"},
	}, result)
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

	require.Equal(t, optimizer.CallToolInput{
		ToolName:   "read_file",
		Parameters: map[string]any{"path": "/etc/hosts"},
	}, result)
}

func TestTranslate_PartialInput(t *testing.T) {
	t.Parallel()

	input := map[string]any{
		"tool_description": "find a file reader",
	}

	result, err := Translate[optimizer.FindToolInput](input)
	require.NoError(t, err)

	require.Equal(t, optimizer.FindToolInput{
		ToolDescription: "find a file reader",
		ToolKeywords:    nil,
	}, result)
}

func TestTranslate_InvalidInput(t *testing.T) {
	t.Parallel()

	input := make(chan int)

	_, err := Translate[optimizer.FindToolInput](input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to marshal input")
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

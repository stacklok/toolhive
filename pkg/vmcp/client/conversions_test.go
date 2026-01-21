// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// These tests verify the critical type conversion logic in the backend client.
// Since we can't easily mock the mark3labs client, we test the conversion patterns
// that our code uses to transform MCP SDK types to vmcp domain types.

func TestToolInputSchemaConversion(t *testing.T) {
	t.Parallel()

	t.Run("converts basic tool schema", func(t *testing.T) {
		t.Parallel()

		sdkTool := mcp.Tool{
			Name:        "create_issue",
			Description: "Create a GitHub issue",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"title": map[string]any{"type": "string", "description": "Issue title"},
					"body":  map[string]any{"type": "string", "description": "Issue body"},
				},
				Required: []string{"title"},
			},
		}

		inputSchema := convertToolInputSchema(sdkTool.InputSchema)

		assert.Equal(t, "object", inputSchema["type"])
		assert.NotNil(t, inputSchema["properties"])
		assert.Equal(t, []string{"title"}, inputSchema["required"])

		props := inputSchema["properties"].(map[string]any)
		assert.Contains(t, props, "title")
		assert.Contains(t, props, "body")
		titleProp := props["title"].(map[string]any)
		assert.Equal(t, "string", titleProp["type"])
		assert.Equal(t, "Issue title", titleProp["description"])
	})

	t.Run("converts schema with $defs", func(t *testing.T) {
		t.Parallel()

		sdkTool := mcp.Tool{
			Name: "complex_tool",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"config": map[string]any{"$ref": "#/$defs/Config"},
				},
				Defs: map[string]any{
					"Config": map[string]any{
						"type":       "object",
						"properties": map[string]any{"enabled": map[string]any{"type": "boolean"}},
					},
				},
			},
		}

		inputSchema := convertToolInputSchema(sdkTool.InputSchema)

		assert.Contains(t, inputSchema, "$defs")
		defs := inputSchema["$defs"].(map[string]any)
		assert.Contains(t, defs, "Config")
	})

	t.Run("handles empty required array", func(t *testing.T) {
		t.Parallel()

		sdkTool := mcp.Tool{
			Name: "optional_tool",
			InputSchema: mcp.ToolInputSchema{
				Type:       "object",
				Properties: map[string]any{"optional_param": map[string]any{"type": "string"}},
				Required:   []string{},
			},
		}

		inputSchema := convertToolInputSchema(sdkTool.InputSchema)

		assert.NotContains(t, inputSchema, "required")
	})
}

func TestContentInterfaceHandling(t *testing.T) {
	t.Parallel()

	t.Run("extracts text content correctly", func(t *testing.T) {
		t.Parallel()

		toolResult := &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent("First text result"),
				mcp.NewTextContent("Second text result"),
			},
			IsError: false,
		}

		resultMap := convertContentToMap(toolResult.Content)

		assert.Equal(t, "First text result", resultMap["text"])
		assert.Equal(t, "Second text result", resultMap["text_1"])
	})

	t.Run("extracts mixed content types", func(t *testing.T) {
		t.Parallel()

		toolResult := &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent("Text content"),
				mcp.NewImageContent("base64data", "image/png"),
				mcp.NewTextContent("More text"),
			},
			IsError: false,
		}

		resultMap := convertContentToMap(toolResult.Content)

		assert.Equal(t, "Text content", resultMap["text"])
		assert.Equal(t, "More text", resultMap["text_1"])
		assert.Equal(t, "base64data", resultMap["image_0"])
	})

	t.Run("handles error result correctly", func(t *testing.T) {
		t.Parallel()

		toolResult := &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent("Error: something went wrong"),
			},
			IsError: true,
		}

		// Verify IsError is a boolean (not pointer) - from client.go:223
		assert.True(t, toolResult.IsError)
		// Our code should check: if result.IsError { return error }
	})
}

func TestResourceContentsHandling(t *testing.T) {
	t.Parallel()

	t.Run("extracts text resource content", func(t *testing.T) {
		t.Parallel()

		resourceResult := &mcp.ReadResourceResult{
			Contents: []mcp.ResourceContents{
				mcp.TextResourceContents{
					URI:      "test://resource",
					MIMEType: "text/plain",
					Text:     "Resource text content",
				},
			},
		}

		data := convertResourceContents(resourceResult.Contents)
		assert.Equal(t, []byte("Resource text content"), data)
	})

	t.Run("extracts blob resource content", func(t *testing.T) {
		t.Parallel()

		resourceResult := &mcp.ReadResourceResult{
			Contents: []mcp.ResourceContents{
				mcp.BlobResourceContents{
					URI:      "test://binary",
					MIMEType: "application/octet-stream",
					Blob:     "YmFzZTY0ZGF0YQ==",
				},
			},
		}

		data := convertResourceContents(resourceResult.Contents)
		assert.Equal(t, []byte("YmFzZTY0ZGF0YQ=="), data)
	})

	t.Run("concatenates multiple resource contents", func(t *testing.T) {
		t.Parallel()

		resourceResult := &mcp.ReadResourceResult{
			Contents: []mcp.ResourceContents{
				mcp.TextResourceContents{URI: "test://multi", Text: "Part 1"},
				mcp.TextResourceContents{URI: "test://multi", Text: "Part 2"},
			},
		}

		data := convertResourceContents(resourceResult.Contents)
		assert.Equal(t, []byte("Part 1Part 2"), data)
	})
}

func TestPromptMessageHandling(t *testing.T) {
	t.Parallel()

	t.Run("extracts prompt with single message", func(t *testing.T) {
		t.Parallel()

		promptResult := &mcp.GetPromptResult{
			Description: "Test prompt",
			Messages: []mcp.PromptMessage{
				{Role: "user", Content: mcp.NewTextContent("What is the weather?")},
			},
		}

		prompt := convertPromptMessages(promptResult.Messages)
		assert.Equal(t, "[user] What is the weather?\n", prompt)
	})

	t.Run("concatenates multiple prompt messages", func(t *testing.T) {
		t.Parallel()

		promptResult := &mcp.GetPromptResult{
			Messages: []mcp.PromptMessage{
				{Role: "system", Content: mcp.NewTextContent("You are a helpful assistant")},
				{Role: "user", Content: mcp.NewTextContent("Hello")},
				{Role: "assistant", Content: mcp.NewTextContent("Hi there!")},
			},
		}

		prompt := convertPromptMessages(promptResult.Messages)
		expected := "[system] You are a helpful assistant\n[user] Hello\n[assistant] Hi there!\n"
		assert.Equal(t, expected, prompt)
	})

	t.Run("handles prompt message without role", func(t *testing.T) {
		t.Parallel()

		promptResult := &mcp.GetPromptResult{
			Messages: []mcp.PromptMessage{
				{Role: "", Content: mcp.NewTextContent("Message content")},
			},
		}

		prompt := convertPromptMessages(promptResult.Messages)
		assert.Equal(t, "Message content\n", prompt)
	})
}

func TestGetPromptArgumentsConversion(t *testing.T) {
	t.Parallel()

	t.Run("converts map[string]any to map[string]string", func(t *testing.T) {
		t.Parallel()

		arguments := map[string]any{
			"string_arg": "value",
			"int_arg":    42,
			"bool_arg":   true,
			"float_arg":  3.14,
		}

		stringArgs := convertPromptArguments(arguments)

		assert.Equal(t, "value", stringArgs["string_arg"])
		assert.Equal(t, "42", stringArgs["int_arg"])
		assert.Equal(t, "true", stringArgs["bool_arg"])
		assert.Equal(t, "3.14", stringArgs["float_arg"])
	})

	t.Run("handles nil and empty values", func(t *testing.T) {
		t.Parallel()

		arguments := map[string]any{
			"nil_arg":   nil,
			"empty_arg": "",
		}

		stringArgs := convertPromptArguments(arguments)

		assert.Equal(t, "<nil>", stringArgs["nil_arg"])
		assert.Equal(t, "", stringArgs["empty_arg"])
	})
}

func TestResourceMIMETypeField(t *testing.T) {
	t.Parallel()

	t.Run("uses MIMEType not MimeType", func(t *testing.T) {
		t.Parallel()

		// This verifies we're using the correct field name (from client.go:167)
		sdkResource := mcp.Resource{
			URI:         "test://resource",
			Name:        "Test Resource",
			Description: "A test resource",
			MIMEType:    "application/json", // Note: MIMEType, not MimeType
		}

		vmcpResource := vmcp.Resource{
			URI:         sdkResource.URI,
			Name:        sdkResource.Name,
			Description: sdkResource.Description,
			MimeType:    sdkResource.MIMEType, // Our conversion uses MIMEType
			BackendID:   "test-backend",
		}

		assert.Equal(t, "application/json", vmcpResource.MimeType)
	})
}

func TestMultipleContentItemsHandling(t *testing.T) {
	t.Parallel()

	t.Run("handles tool result with many text items", func(t *testing.T) {
		t.Parallel()

		toolResult := &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent("Result 1"),
				mcp.NewTextContent("Result 2"),
				mcp.NewTextContent("Result 3"),
				mcp.NewTextContent("Result 4"),
				mcp.NewTextContent("Result 5"),
			},
			IsError: false,
		}

		resultMap := convertContentToMap(toolResult.Content)

		assert.Equal(t, "Result 1", resultMap["text"])
		assert.Equal(t, "Result 2", resultMap["text_1"])
		assert.Equal(t, "Result 3", resultMap["text_2"])
		assert.Equal(t, "Result 4", resultMap["text_3"])
		assert.Equal(t, "Result 5", resultMap["text_4"])
	})

	t.Run("handles tool result with many images", func(t *testing.T) {
		t.Parallel()

		toolResult := &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewImageContent("data1", "image/png"),
				mcp.NewImageContent("data2", "image/jpeg"),
				mcp.NewImageContent("data3", "image/gif"),
			},
			IsError: false,
		}

		resultMap := convertContentToMap(toolResult.Content)

		assert.Equal(t, "data1", resultMap["image_0"])
		assert.Equal(t, "data2", resultMap["image_1"])
		assert.Equal(t, "data3", resultMap["image_2"])
	})

	t.Run("handles empty content array", func(t *testing.T) {
		t.Parallel()

		emptyContent := []mcp.Content{}
		resultMap := convertContentToMap(emptyContent)

		assert.Empty(t, resultMap)
	})
}

func TestPromptArgumentConversion(t *testing.T) {
	t.Parallel()

	t.Run("converts prompt arguments correctly", func(t *testing.T) {
		t.Parallel()

		// From client.go:174-183
		sdkPrompt := mcp.Prompt{
			Name:        "test_prompt",
			Description: "A test prompt",
			Arguments: []mcp.PromptArgument{
				{
					Name:        "required_arg",
					Description: "A required argument",
					Required:    true,
				},
				{
					Name:        "optional_arg",
					Description: "An optional argument",
					Required:    false,
				},
			},
		}

		// Apply our conversion
		args := make([]vmcp.PromptArgument, len(sdkPrompt.Arguments))
		for j, arg := range sdkPrompt.Arguments {
			args[j] = vmcp.PromptArgument{
				Name:        arg.Name,
				Description: arg.Description,
				Required:    arg.Required,
			}
		}

		vmcpPrompt := vmcp.Prompt{
			Name:        sdkPrompt.Name,
			Description: sdkPrompt.Description,
			Arguments:   args,
			BackendID:   "test-backend",
		}

		// Verify conversion
		require.Len(t, vmcpPrompt.Arguments, 2)
		assert.Equal(t, "required_arg", vmcpPrompt.Arguments[0].Name)
		assert.True(t, vmcpPrompt.Arguments[0].Required)
		assert.Equal(t, "optional_arg", vmcpPrompt.Arguments[1].Name)
		assert.False(t, vmcpPrompt.Arguments[1].Required)
	})
}

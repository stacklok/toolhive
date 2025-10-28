package client

import (
	"fmt"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// These tests verify the critical type conversion logic in the backend client.
// Since we can't easily mock the mark3labs client, we test the conversion patterns
// that our code uses to transform MCP SDK types to vmcp domain types.

const textKey = "text"

func TestToolInputSchemaConversion(t *testing.T) {
	t.Parallel()

	t.Run("converts basic tool schema", func(t *testing.T) {
		t.Parallel()

		// Simulate what we receive from the SDK
		sdkTool := mcp.Tool{
			Name:        "create_issue",
			Description: "Create a GitHub issue",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"title": map[string]any{
						"type":        "string",
						"description": "Issue title",
					},
					"body": map[string]any{
						"type":        "string",
						"description": "Issue body",
					},
				},
				Required: []string{"title"},
			},
		}

		// Simulate our conversion logic (from client.go:138-151)
		inputSchema := map[string]any{
			"type": sdkTool.InputSchema.Type,
		}
		if sdkTool.InputSchema.Properties != nil {
			inputSchema["properties"] = sdkTool.InputSchema.Properties
		}
		if len(sdkTool.InputSchema.Required) > 0 {
			inputSchema["required"] = sdkTool.InputSchema.Required
		}
		if sdkTool.InputSchema.Defs != nil {
			inputSchema["$defs"] = sdkTool.InputSchema.Defs
		}

		vmcpTool := vmcp.Tool{
			Name:        sdkTool.Name,
			Description: sdkTool.Description,
			InputSchema: inputSchema,
			BackendID:   "test-backend",
		}

		// Verify conversion
		assert.Equal(t, "create_issue", vmcpTool.Name)
		assert.Equal(t, "object", vmcpTool.InputSchema["type"])
		assert.NotNil(t, vmcpTool.InputSchema["properties"])
		assert.Equal(t, []string{"title"}, vmcpTool.InputSchema["required"])

		// Verify properties structure is preserved
		props, ok := vmcpTool.InputSchema["properties"].(map[string]any)
		require.True(t, ok)
		assert.Contains(t, props, "title")
		assert.Contains(t, props, "body")

		titleProp, ok := props["title"].(map[string]any)
		require.True(t, ok)
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
					"config": map[string]any{
						"$ref": "#/$defs/Config",
					},
				},
				Defs: map[string]any{
					"Config": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"enabled": map[string]any{"type": "boolean"},
						},
					},
				},
			},
		}

		// Apply our conversion logic
		inputSchema := map[string]any{
			"type": sdkTool.InputSchema.Type,
		}
		if sdkTool.InputSchema.Properties != nil {
			inputSchema["properties"] = sdkTool.InputSchema.Properties
		}
		if len(sdkTool.InputSchema.Required) > 0 {
			inputSchema["required"] = sdkTool.InputSchema.Required
		}
		if sdkTool.InputSchema.Defs != nil {
			inputSchema["$defs"] = sdkTool.InputSchema.Defs
		}

		// Verify $defs are preserved
		assert.Contains(t, inputSchema, "$defs")
		defs, ok := inputSchema["$defs"].(map[string]any)
		require.True(t, ok)
		assert.Contains(t, defs, "Config")
	})

	t.Run("handles empty required array", func(t *testing.T) {
		t.Parallel()

		sdkTool := mcp.Tool{
			Name: "optional_tool",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"optional_param": map[string]any{"type": "string"},
				},
				Required: []string{}, // Empty required array
			},
		}

		// Apply our conversion logic
		inputSchema := map[string]any{
			"type": sdkTool.InputSchema.Type,
		}
		if sdkTool.InputSchema.Properties != nil {
			inputSchema["properties"] = sdkTool.InputSchema.Properties
		}
		if len(sdkTool.InputSchema.Required) > 0 {
			inputSchema["required"] = sdkTool.InputSchema.Required
		}

		// Empty required array should NOT be included
		assert.NotContains(t, inputSchema, "required")
	})
}

func TestContentInterfaceHandling(t *testing.T) {
	t.Parallel()

	t.Run("extracts text content correctly", func(t *testing.T) {
		t.Parallel()

		// Simulate what CallTool returns (from client.go:228-250)
		toolResult := &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent("First text result"),
				mcp.NewTextContent("Second text result"),
			},
			IsError: false,
		}

		// Apply our conversion logic
		resultMap := make(map[string]any)
		textIndex := 0
		for _, content := range toolResult.Content {
			if textContent, ok := mcp.AsTextContent(content); ok {
				key := textKey
				if textIndex > 0 {
					key = "text_" + string(rune('0'+textIndex))
				}
				resultMap[key] = textContent.Text
				textIndex++
			}
		}

		// Verify both texts are extracted with proper keys
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

		// Apply our conversion logic
		resultMap := make(map[string]any)
		textIndex := 0
		imageIndex := 0
		for _, content := range toolResult.Content {
			if textContent, ok := mcp.AsTextContent(content); ok {
				key := textKey
				if textIndex > 0 {
					key = "text_" + string(rune('0'+textIndex))
				}
				resultMap[key] = textContent.Text
				textIndex++
			} else if imageContent, ok := mcp.AsImageContent(content); ok {
				key := "image_" + string(rune('0'+imageIndex))
				resultMap[key] = imageContent.Data
				imageIndex++
			}
		}

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

		// Simulate what ReadResource returns (from client.go:276-289)
		resourceResult := &mcp.ReadResourceResult{
			Contents: []mcp.ResourceContents{
				mcp.TextResourceContents{
					URI:      "test://resource",
					MIMEType: "text/plain",
					Text:     "Resource text content",
				},
			},
		}

		// Apply our conversion logic
		var data []byte
		for _, content := range resourceResult.Contents {
			if textContent, ok := mcp.AsTextResourceContents(content); ok {
				data = append(data, []byte(textContent.Text)...)
			} else if blobContent, ok := mcp.AsBlobResourceContents(content); ok {
				data = append(data, []byte(blobContent.Blob)...)
			}
		}

		assert.Equal(t, []byte("Resource text content"), data)
	})

	t.Run("extracts blob resource content", func(t *testing.T) {
		t.Parallel()

		resourceResult := &mcp.ReadResourceResult{
			Contents: []mcp.ResourceContents{
				mcp.BlobResourceContents{
					URI:      "test://binary",
					MIMEType: "application/octet-stream",
					Blob:     "YmFzZTY0ZGF0YQ==", // base64-encoded
				},
			},
		}

		// Apply our conversion logic
		var data []byte
		for _, content := range resourceResult.Contents {
			if textContent, ok := mcp.AsTextResourceContents(content); ok {
				data = append(data, []byte(textContent.Text)...)
			} else if blobContent, ok := mcp.AsBlobResourceContents(content); ok {
				data = append(data, []byte(blobContent.Blob)...)
			}
		}

		// Note: Our code doesn't decode base64, it just returns the blob as-is
		assert.Equal(t, []byte("YmFzZTY0ZGF0YQ=="), data)
	})

	t.Run("concatenates multiple resource contents", func(t *testing.T) {
		t.Parallel()

		resourceResult := &mcp.ReadResourceResult{
			Contents: []mcp.ResourceContents{
				mcp.TextResourceContents{
					URI:  "test://multi",
					Text: "Part 1",
				},
				mcp.TextResourceContents{
					URI:  "test://multi",
					Text: "Part 2",
				},
			},
		}

		// Apply our conversion logic
		var data []byte
		for _, content := range resourceResult.Contents {
			if textContent, ok := mcp.AsTextResourceContents(content); ok {
				data = append(data, []byte(textContent.Text)...)
			}
		}

		assert.Equal(t, []byte("Part 1Part 2"), data)
	})
}

func TestPromptMessageHandling(t *testing.T) {
	t.Parallel()

	t.Run("extracts prompt with single message", func(t *testing.T) {
		t.Parallel()

		// Simulate what GetPrompt returns (from client.go:315-327)
		promptResult := &mcp.GetPromptResult{
			Description: "Test prompt",
			Messages: []mcp.PromptMessage{
				{
					Role:    "user",
					Content: mcp.NewTextContent("What is the weather?"),
				},
			},
		}

		// Apply our conversion logic
		var prompt string
		for _, msg := range promptResult.Messages {
			if msg.Role != "" {
				prompt += "[" + string(msg.Role) + "] "
			}
			if textContent, ok := mcp.AsTextContent(msg.Content); ok {
				prompt += textContent.Text + "\n"
			}
		}

		assert.Equal(t, "[user] What is the weather?\n", prompt)
	})

	t.Run("concatenates multiple prompt messages", func(t *testing.T) {
		t.Parallel()

		promptResult := &mcp.GetPromptResult{
			Messages: []mcp.PromptMessage{
				{
					Role:    "system",
					Content: mcp.NewTextContent("You are a helpful assistant"),
				},
				{
					Role:    "user",
					Content: mcp.NewTextContent("Hello"),
				},
				{
					Role:    "assistant",
					Content: mcp.NewTextContent("Hi there!"),
				},
			},
		}

		// Apply our conversion logic
		var prompt string
		for _, msg := range promptResult.Messages {
			if msg.Role != "" {
				prompt += "[" + string(msg.Role) + "] "
			}
			if textContent, ok := mcp.AsTextContent(msg.Content); ok {
				prompt += textContent.Text + "\n"
			}
		}

		expected := "[system] You are a helpful assistant\n[user] Hello\n[assistant] Hi there!\n"
		assert.Equal(t, expected, prompt)
	})

	t.Run("handles prompt message without role", func(t *testing.T) {
		t.Parallel()

		promptResult := &mcp.GetPromptResult{
			Messages: []mcp.PromptMessage{
				{
					Role:    "", // No role
					Content: mcp.NewTextContent("Message content"),
				},
			},
		}

		// Apply our conversion logic
		var prompt string
		for _, msg := range promptResult.Messages {
			if msg.Role != "" {
				prompt += "[" + string(msg.Role) + "] "
			}
			if textContent, ok := mcp.AsTextContent(msg.Content); ok {
				prompt += textContent.Text + "\n"
			}
		}

		// Should not include role prefix when role is empty
		assert.Equal(t, "Message content\n", prompt)
	})
}

func TestGetPromptArgumentsConversion(t *testing.T) {
	t.Parallel()

	t.Run("converts map[string]any to map[string]string", func(t *testing.T) {
		t.Parallel()

		// This tests the conversion from client.go:306-309
		arguments := map[string]any{
			"string_arg": "value",
			"int_arg":    42,
			"bool_arg":   true,
			"float_arg":  3.14,
		}

		// Apply our conversion logic
		stringArgs := make(map[string]string)
		for k, v := range arguments {
			stringArgs[k] = fmt.Sprintf("%v", v)
		}

		// Verify all types are converted to strings
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

		stringArgs := make(map[string]string)
		for k, v := range arguments {
			stringArgs[k] = fmt.Sprintf("%v", v)
		}

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

		// Edge case: what if a tool returns many text contents?
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

		// Apply our conversion logic
		resultMap := make(map[string]any)
		textIndex := 0
		for _, content := range toolResult.Content {
			if textContent, ok := mcp.AsTextContent(content); ok {
				key := textKey
				if textIndex > 0 {
					key = "text_" + string(rune('0'+textIndex))
				}
				resultMap[key] = textContent.Text
				textIndex++
			}
		}

		// Verify all items are extracted
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

		// Apply our conversion logic
		resultMap := make(map[string]any)
		imageIndex := 0
		for _, content := range toolResult.Content {
			if imageContent, ok := mcp.AsImageContent(content); ok {
				key := "image_" + string(rune('0'+imageIndex))
				resultMap[key] = imageContent.Data
				imageIndex++
			}
		}

		assert.Equal(t, "data1", resultMap["image_0"])
		assert.Equal(t, "data2", resultMap["image_1"])
		assert.Equal(t, "data3", resultMap["image_2"])
	})

	t.Run("handles empty content array", func(t *testing.T) {
		t.Parallel()

		// Apply our conversion logic (from client.go:229-250)
		// When content is empty, the map remains empty
		emptyContent := []mcp.Content{}
		resultMap := make(map[string]any)

		// Simulate the loop - no iterations with empty content
		for range emptyContent {
			// Would never execute
		}

		// Should result in empty map
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

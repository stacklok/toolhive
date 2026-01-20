// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

// Helper functions to encapsulate conversion logic patterns

// convertToolInputSchema simulates the conversion logic from client.go:138-151
func convertToolInputSchema(schema mcp.ToolInputSchema) map[string]any {
	inputSchema := map[string]any{
		"type": schema.Type,
	}
	if schema.Properties != nil {
		inputSchema["properties"] = schema.Properties
	}
	if len(schema.Required) > 0 {
		inputSchema["required"] = schema.Required
	}
	if schema.Defs != nil {
		inputSchema["$defs"] = schema.Defs
	}
	return inputSchema
}

// convertContentToMap simulates the conversion logic from conversion.ContentArrayToMap
// This test helper converts MCP SDK content types to a map for testing.
func convertContentToMap(contents []mcp.Content) map[string]any {
	resultMap := make(map[string]any)
	textIndex := 0
	imageIndex := 0
	for _, content := range contents {
		if textContent, ok := mcp.AsTextContent(content); ok {
			key := "text"
			if textIndex > 0 {
				key = fmt.Sprintf("text_%d", textIndex)
			}
			resultMap[key] = textContent.Text
			textIndex++
		} else if imageContent, ok := mcp.AsImageContent(content); ok {
			key := fmt.Sprintf("image_%d", imageIndex)
			resultMap[key] = imageContent.Data
			imageIndex++
		} else if audioContent, ok := mcp.AsAudioContent(content); ok {
			// Audio content uses the same structure as images (Data + MimeType)
			// For testing purposes, we use the image_ key prefix (matches conversion.ContentArrayToMap behavior)
			key := fmt.Sprintf("image_%d", imageIndex)
			resultMap[key] = audioContent.Data
			imageIndex++
		}
	}
	return resultMap
}

// convertResourceContents simulates the conversion logic from client.go:276-289
func convertResourceContents(contents []mcp.ResourceContents) []byte {
	var data []byte
	for _, content := range contents {
		if textContent, ok := mcp.AsTextResourceContents(content); ok {
			data = append(data, []byte(textContent.Text)...)
		} else if blobContent, ok := mcp.AsBlobResourceContents(content); ok {
			data = append(data, []byte(blobContent.Blob)...)
		}
	}
	return data
}

// convertPromptMessages simulates the conversion logic from client.go:315-327
func convertPromptMessages(messages []mcp.PromptMessage) string {
	var prompt string
	for _, msg := range messages {
		if msg.Role != "" {
			prompt += "[" + string(msg.Role) + "] "
		}
		if textContent, ok := mcp.AsTextContent(msg.Content); ok {
			prompt += textContent.Text + "\n"
		}
	}
	return prompt
}

// convertPromptArguments simulates the conversion logic from client.go:306-309
func convertPromptArguments(arguments map[string]any) map[string]string {
	stringArgs := make(map[string]string)
	for k, v := range arguments {
		stringArgs[k] = fmt.Sprintf("%v", v)
	}
	return stringArgs
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/stacklok/toolhive/pkg/vmcp/conversion"
)

// Helper functions to encapsulate conversion logic patterns

// convertToolInputSchema mirrors the JSON round-trip used in ListCapabilities.
func convertToolInputSchema(schema mcp.ToolInputSchema) map[string]any {
	result := make(map[string]any)
	if b, err := json.Marshal(schema); err == nil {
		if jsonErr := json.Unmarshal(b, &result); jsonErr != nil {
			return map[string]any{"type": schema.Type}
		}
	} else {
		return map[string]any{"type": schema.Type}
	}
	return result
}

// convertContentToMap simulates the conversion logic from conversion.ContentArrayToMap
// This test helper converts MCP SDK content types to a map for testing.
// Audio content is intentionally ignored (not supported for template substitution).
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
		}
		// Audio content is ignored (matches conversion.ContentArrayToMap behavior)
		// Resource content is handled separately, not in this map
	}
	return resultMap
}

// convertResourceContents delegates to conversion.ConcatenateResourceContents,
// returning only the data bytes for backward compatibility with existing tests.
func convertResourceContents(contents []mcp.ResourceContents) []byte {
	data, _ := conversion.ConcatenateResourceContents(contents)
	return data
}

// convertPromptMessages simulates the conversion logic from client.go GetPrompt.
func convertPromptMessages(messages []mcp.PromptMessage) string {
	var sb strings.Builder
	for _, msg := range messages {
		if msg.Role != "" {
			fmt.Fprintf(&sb, "[%s] ", msg.Role)
		}
		if textContent, ok := mcp.AsTextContent(msg.Content); ok {
			sb.WriteString(textContent.Text)
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// convertPromptArguments simulates the conversion logic from client.go:306-309
func convertPromptArguments(arguments map[string]any) map[string]string {
	stringArgs := make(map[string]string)
	for k, v := range arguments {
		stringArgs[k] = fmt.Sprintf("%v", v)
	}
	return stringArgs
}

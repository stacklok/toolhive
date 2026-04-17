// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package conversions

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/mark3labs/mcp-go/mcp"
)

// ParseToolResult converts an mcp.CallToolResult into a Go value suitable
// for Starlark consumption.
//
// It handles multiple response formats:
//   - Structured content with the mcp-go SDK {"result": v} wrapper pattern
//   - Structured content without the wrapper
//   - Text content parsed as JSON
//   - Plain text returned as-is
//   - Multi-item responses (first text item used)
//   - Error results returned as an error
func ParseToolResult(result *mcp.CallToolResult) (interface{}, error) {
	if result.IsError {
		msg := "tool execution error"
		if len(result.Content) > 0 {
			if tc, ok := mcp.AsTextContent(result.Content[0]); ok && tc.Text != "" {
				msg = tc.Text
			}
		}
		return nil, fmt.Errorf("%s", msg)
	}

	// Prefer structured content, but unwrap the common SDK wrapper
	// pattern where the result is {"result": <actual value>}.
	if result.StructuredContent != nil {
		sc, ok := result.StructuredContent.(map[string]interface{})
		if ok && len(sc) == 1 {
			if v, exists := sc["result"]; exists {
				return v, nil
			}
		}
		return result.StructuredContent, nil
	}

	// Fall back to text content
	if len(result.Content) == 0 {
		return nil, nil
	}

	if len(result.Content) > 1 {
		slog.Debug("tool returned multiple content items, using first text item only",
			"count", len(result.Content))
	}

	// Find the first text content item
	for _, content := range result.Content {
		tc, ok := mcp.AsTextContent(content)
		if !ok {
			continue
		}

		// Try to parse as JSON
		var parsed interface{}
		if err := json.Unmarshal([]byte(tc.Text), &parsed); err != nil {
			// Not valid JSON — return as plain string
			return tc.Text, nil
		}
		return parsed, nil
	}

	// No text content found — return nil
	return nil, nil
}

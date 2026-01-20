// Package conversion provides utilities for converting between MCP SDK types and vmcp wrapper types.
// This package centralizes conversion logic to ensure consistency and eliminate duplication.
package conversion

import (
	"fmt"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// ContentArrayToMap converts a vmcp.Content array to a map for template variable substitution.
// This is used by composite tool workflows and backend result handling.
//
// Conversion rules:
//   - First text content: key="text"
//   - Subsequent text content: key="text_1", "text_2", etc.
//   - Image content: key="image_0", "image_1", etc.
//   - Other content types are currently ignored (logged elsewhere)
//
// This ensures consistent behavior between client response handling and workflow step output processing.
func ContentArrayToMap(content []vmcp.Content) map[string]any {
	result := make(map[string]any)
	if len(content) == 0 {
		return result
	}

	textIndex := 0
	imageIndex := 0

	for _, item := range content {
		switch item.Type {
		case "text":
			key := "text"
			if textIndex > 0 {
				key = fmt.Sprintf("text_%d", textIndex)
			}
			result[key] = item.Text
			textIndex++

		case "image":
			key := fmt.Sprintf("image_%d", imageIndex)
			result[key] = item.Data
			imageIndex++

		case "audio":
			// Audio content uses the same structure as images (Data + MimeType)
			// Future enhancement: add dedicated audio_ key prefix
			key := fmt.Sprintf("image_%d", imageIndex)
			result[key] = item.Data
			imageIndex++

			// "resource" type is not converted to map - handled separately
			// Unknown types are ignored - warnings logged at conversion boundaries
		}
	}

	return result
}

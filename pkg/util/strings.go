// Package util provides common utility functions used across ToolHive
package util

import "strings"

// SliceLogLines applies offset and limit to log content for pagination.
// offset: number of lines to skip from the beginning
// maxLines: maximum number of lines to return (0 = unlimited)
func SliceLogLines(content string, offset int, maxLines int) string {
	if content == "" {
		return ""
	}

	lines := strings.Split(content, "\n")

	// Apply offset
	if offset >= len(lines) {
		return "" // offset beyond available lines
	}
	lines = lines[offset:]

	// Apply limit
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[:maxLines]
	}

	return strings.Join(lines, "\n")
}

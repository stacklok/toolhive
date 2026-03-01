// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package conversion provides utilities for converting between MCP SDK types and vmcp wrapper types.
// This package centralizes conversion logic to ensure consistency and eliminate duplication.
package conversion

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// ConvertMCPContent converts a single mcp.Content item to vmcp.Content.
// Unknown content types are returned as vmcp.Content{Type: "unknown"}.
func ConvertMCPContent(content mcp.Content) vmcp.Content {
	if text, ok := mcp.AsTextContent(content); ok {
		return vmcp.Content{Type: "text", Text: text.Text}
	}
	if img, ok := mcp.AsImageContent(content); ok {
		return vmcp.Content{Type: "image", Data: img.Data, MimeType: img.MIMEType}
	}
	if audio, ok := mcp.AsAudioContent(content); ok {
		return vmcp.Content{Type: "audio", Data: audio.Data, MimeType: audio.MIMEType}
	}
	if res, ok := mcp.AsEmbeddedResource(content); ok {
		if textRes, ok := mcp.AsTextResourceContents(res.Resource); ok {
			return vmcp.Content{Type: "resource", Text: textRes.Text, URI: textRes.URI, MimeType: textRes.MIMEType}
		}
		if blobRes, ok := mcp.AsBlobResourceContents(res.Resource); ok {
			return vmcp.Content{Type: "resource", Data: blobRes.Blob, URI: blobRes.URI, MimeType: blobRes.MIMEType}
		}
		slog.Debug("Embedded resource has unknown resource contents type", "type", fmt.Sprintf("%T", res.Resource))
		return vmcp.Content{Type: "resource"}
	}
	slog.Debug("Encountered unknown MCP content type", "type", fmt.Sprintf("%T", content))
	return vmcp.Content{Type: "unknown"}
}

// ConvertMCPContents converts a slice of mcp.Content to []vmcp.Content.
// Returns an empty (non-nil) slice for a nil or empty input.
func ConvertMCPContents(contents []mcp.Content) []vmcp.Content {
	result := make([]vmcp.Content, 0, len(contents))
	for _, c := range contents {
		result = append(result, ConvertMCPContent(c))
	}
	return result
}

// ConcatenateResourceContents concatenates all MCP resource content items into a
// single byte slice and returns the MIME type of the first item.
//
// MCP resources may return multiple content chunks (text or blob). Text chunks
// are appended as UTF-8 bytes; blob chunks are base64-decoded per the MCP spec.
// If base64 decoding fails, the malformed chunk is skipped and a warning is logged
// (appending raw base64 bytes would produce corrupted binary data).
// The MIME type is taken from the first content item; subsequent items are
// expected to share the same type (the MCP spec does not define per-chunk types).
func ConcatenateResourceContents(contents []mcp.ResourceContents) (data []byte, mimeType string) {
	for i, content := range contents {
		if textContent, ok := mcp.AsTextResourceContents(content); ok {
			data = append(data, []byte(textContent.Text)...)
			if i == 0 && textContent.MIMEType != "" {
				mimeType = textContent.MIMEType
			}
		} else if blobContent, ok := mcp.AsBlobResourceContents(content); ok {
			decoded, err := base64.StdEncoding.DecodeString(blobContent.Blob)
			if err != nil {
				slog.Warn("Skipping malformed base64 blob resource chunk; this chunk's data is lost",
					"uri", blobContent.URI, "error", err)
				continue
			}
			data = append(data, decoded...)
			if i == 0 && blobContent.MIMEType != "" {
				mimeType = blobContent.MIMEType
			}
		}
	}
	return data, mimeType
}

// ConvertToolInputSchema converts a mcp.ToolInputSchema to map[string]any via a
// JSON round-trip, capturing all fields (type, properties, required, $defs,
// additionalProperties, etc.) without enumerating them manually. Falls back to
// {type: schema.Type} if marshalling fails.
func ConvertToolInputSchema(schema mcp.ToolInputSchema) map[string]any {
	result := make(map[string]any)
	b, err := json.Marshal(schema)
	if err != nil {
		return map[string]any{"type": schema.Type}
	}
	if err := json.Unmarshal(b, &result); err != nil {
		return map[string]any{"type": schema.Type}
	}
	return result
}

// ConvertPromptMessages flattens MCP prompt messages into a single string with
// the format "[role] text\n". Messages without a role omit the prefix. Only
// text content is included; non-text content is silently discarded (Phase 1
// limitation — vmcp.PromptGetResult carries a flat string, not structured messages).
func ConvertPromptMessages(messages []mcp.PromptMessage) string {
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

// ConvertPromptArguments converts map[string]any to map[string]string by
// formatting each value with fmt.Sprintf("%v", v). Required by the MCP
// GetPrompt API which accepts only string-typed arguments.
func ConvertPromptArguments(arguments map[string]any) map[string]string {
	result := make(map[string]string, len(arguments))
	for k, v := range arguments {
		result[k] = fmt.Sprintf("%v", v)
	}
	return result
}

// ContentArrayToMap converts a vmcp.Content array to a map for template variable substitution.
// This is used by composite tool workflows and backend result handling.
//
// Conversion rules:
//   - First text content: key="text"
//   - Subsequent text content: key="text_1", "text_2", etc.
//   - Image content: key="image_0", "image_1", etc.
//   - Audio content: ignored (not supported for template substitution)
//   - Resource content: ignored (handled separately, not converted to map)
//   - Unknown content types: ignored (warnings logged at conversion boundaries)
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

			// Default case (implicit):
			// - Audio content is ignored (not supported for template substitution)
			// - Resource content is ignored (handled separately, not converted to map)
			// - Unknown content types are ignored (warnings logged at conversion boundaries)
		}
	}

	return result
}

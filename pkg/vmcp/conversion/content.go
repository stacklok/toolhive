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

const (
	contentTypeText     = "text"
	contentTypeImage    = "image"
	contentTypeAudio    = "audio"
	contentTypeResource = "resource"
)

// ConvertMCPContent converts a single mcp.Content item to vmcp.Content.
// Unknown content types are returned as vmcp.Content{Type: "unknown"}.
func ConvertMCPContent(content mcp.Content) vmcp.Content {
	if text, ok := mcp.AsTextContent(content); ok {
		return vmcp.Content{Type: contentTypeText, Text: text.Text}
	}
	if img, ok := mcp.AsImageContent(content); ok {
		return vmcp.Content{Type: contentTypeImage, Data: img.Data, MimeType: img.MIMEType}
	}
	if audio, ok := mcp.AsAudioContent(content); ok {
		return vmcp.Content{Type: contentTypeAudio, Data: audio.Data, MimeType: audio.MIMEType}
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

// ToMCPContent converts a single vmcp.Content item to mcp.Content.
// Unknown content types are converted to empty text with a warning.
func ToMCPContent(content vmcp.Content) mcp.Content {
	switch content.Type {
	case contentTypeText:
		return mcp.NewTextContent(content.Text)
	case contentTypeImage:
		return mcp.NewImageContent(content.Data, content.MimeType)
	case contentTypeAudio:
		return mcp.NewAudioContent(content.Data, content.MimeType)
	case contentTypeResource:
		// Reconstruct embedded resource from vmcp.Content fields.
		// Text content takes precedence over blob content when both are present.
		if content.Text != "" {
			return mcp.NewEmbeddedResource(mcp.TextResourceContents{
				URI:      content.URI,
				MIMEType: content.MimeType,
				Text:     content.Text,
			})
		}
		if content.Data != "" {
			return mcp.NewEmbeddedResource(mcp.BlobResourceContents{
				URI:      content.URI,
				MIMEType: content.MimeType,
				Blob:     content.Data,
			})
		}
		// Empty resource content — preserve resource wrapper and metadata with empty contents.
		slog.Warn("converting empty resource content to empty embedded resource - no Text or Data field present")
		return mcp.NewEmbeddedResource(mcp.TextResourceContents{
			URI:      content.URI,
			MIMEType: content.MimeType,
			Text:     "",
		})
	default:
		slog.Warn("converting unknown content type to empty text - this may cause data loss", "type", content.Type)
		return mcp.NewTextContent("")
	}
}

// ToMCPContents converts a slice of vmcp.Content to []mcp.Content.
func ToMCPContents(contents []vmcp.Content) []mcp.Content {
	result := make([]mcp.Content, 0, len(contents))
	for _, c := range contents {
		result = append(result, ToMCPContent(c))
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

// ConvertToolAnnotations converts mcp.ToolAnnotation to *vmcp.ToolAnnotations.
// Returns nil if all fields are zero-valued (empty Title, all hint pointers nil).
func ConvertToolAnnotations(ann mcp.ToolAnnotation) *vmcp.ToolAnnotations {
	if ann.Title == "" && ann.ReadOnlyHint == nil && ann.DestructiveHint == nil &&
		ann.IdempotentHint == nil && ann.OpenWorldHint == nil {
		return nil
	}
	return &vmcp.ToolAnnotations{
		Title:           ann.Title,
		ReadOnlyHint:    ann.ReadOnlyHint,
		DestructiveHint: ann.DestructiveHint,
		IdempotentHint:  ann.IdempotentHint,
		OpenWorldHint:   ann.OpenWorldHint,
	}
}

// ConvertToolOutputSchema converts a mcp.ToolOutputSchema to map[string]any via a
// JSON round-trip, same pattern as ConvertToolInputSchema.
// Returns nil if the schema has no meaningful type (empty Type field).
func ConvertToolOutputSchema(schema mcp.ToolOutputSchema) map[string]any {
	// A zero-valued ToolOutputSchema has Type="" — this means the backend
	// did not provide an output schema. Return nil to distinguish from a
	// schema that was explicitly set.
	if schema.Type == "" {
		return nil
	}
	b, err := json.Marshal(schema)
	if err != nil {
		return nil
	}
	result := make(map[string]any)
	if err := json.Unmarshal(b, &result); err != nil {
		return nil
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// ToMCPToolAnnotations converts *vmcp.ToolAnnotations back to mcp.ToolAnnotation.
// Returns a zero-valued mcp.ToolAnnotation if annotations is nil.
func ToMCPToolAnnotations(annotations *vmcp.ToolAnnotations) mcp.ToolAnnotation {
	if annotations == nil {
		return mcp.ToolAnnotation{}
	}
	return mcp.ToolAnnotation{
		Title:           annotations.Title,
		ReadOnlyHint:    annotations.ReadOnlyHint,
		DestructiveHint: annotations.DestructiveHint,
		IdempotentHint:  annotations.IdempotentHint,
		OpenWorldHint:   annotations.OpenWorldHint,
	}
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
		case contentTypeText:
			key := contentTypeText
			if textIndex > 0 {
				key = fmt.Sprintf("text_%d", textIndex)
			}
			result[key] = item.Text
			textIndex++

		case contentTypeImage:
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

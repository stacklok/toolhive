// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

func TestConvertToMCPContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    vmcp.Content
		expected mcp.Content
		wantType string // Expected type name for assertion
	}{
		{
			name: "text content",
			input: vmcp.Content{
				Type: "text",
				Text: "Hello, world!",
			},
			expected: mcp.NewTextContent("Hello, world!"),
			wantType: "mcp.TextContent",
		},
		{
			name: "empty text content",
			input: vmcp.Content{
				Type: "text",
				Text: "",
			},
			expected: mcp.NewTextContent(""),
			wantType: "mcp.TextContent",
		},
		{
			name: "image content",
			input: vmcp.Content{
				Type:     "image",
				Data:     "base64-encoded-image-data",
				MimeType: "image/png",
			},
			expected: mcp.NewImageContent("base64-encoded-image-data", "image/png"),
			wantType: "mcp.ImageContent",
		},
		{
			name: "image content with empty mime type",
			input: vmcp.Content{
				Type:     "image",
				Data:     "image-data",
				MimeType: "",
			},
			expected: mcp.NewImageContent("image-data", ""),
			wantType: "mcp.ImageContent",
		},
		{
			name: "audio content",
			input: vmcp.Content{
				Type:     "audio",
				Data:     "base64-encoded-audio-data",
				MimeType: "audio/mpeg",
			},
			expected: mcp.NewAudioContent("base64-encoded-audio-data", "audio/mpeg"),
			wantType: "mcp.AudioContent",
		},
		{
			name: "audio content with empty mime type",
			input: vmcp.Content{
				Type:     "audio",
				Data:     "audio-data",
				MimeType: "",
			},
			expected: mcp.NewAudioContent("audio-data", ""),
			wantType: "mcp.AudioContent",
		},
		{
			name: "resource content converts to empty text",
			input: vmcp.Content{
				Type: "resource",
			},
			expected: mcp.NewTextContent(""),
			wantType: "mcp.TextContent",
		},
		{
			name: "unknown content type converts to empty text",
			input: vmcp.Content{
				Type: "unknown",
			},
			expected: mcp.NewTextContent(""),
			wantType: "mcp.TextContent",
		},
		{
			name: "unrecognized custom type converts to empty text",
			input: vmcp.Content{
				Type: "custom-type",
			},
			expected: mcp.NewTextContent(""),
			wantType: "mcp.TextContent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := convertToMCPContent(tt.input)

			// Type assertion based on expected type
			switch tt.wantType {
			case "mcp.TextContent":
				textResult, ok := result.(mcp.TextContent)
				assert.True(t, ok, "Expected TextContent type")
				expectedText, ok := tt.expected.(mcp.TextContent)
				assert.True(t, ok)
				assert.Equal(t, expectedText.Text, textResult.Text)

			case "mcp.ImageContent":
				imageResult, ok := result.(mcp.ImageContent)
				assert.True(t, ok, "Expected ImageContent type")
				expectedImage, ok := tt.expected.(mcp.ImageContent)
				assert.True(t, ok)
				assert.Equal(t, expectedImage.Data, imageResult.Data)
				assert.Equal(t, expectedImage.MIMEType, imageResult.MIMEType)

			case "mcp.AudioContent":
				audioResult, ok := result.(mcp.AudioContent)
				assert.True(t, ok, "Expected AudioContent type")
				expectedAudio, ok := tt.expected.(mcp.AudioContent)
				assert.True(t, ok)
				assert.Equal(t, expectedAudio.Data, audioResult.Data)
				assert.Equal(t, expectedAudio.MIMEType, audioResult.MIMEType)

			default:
				t.Errorf("Unexpected content type: %s", tt.wantType)
			}
		})
	}
}

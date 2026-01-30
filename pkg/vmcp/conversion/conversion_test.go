// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package conversion_test

import (
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/conversion"
)

func TestContentArrayToMap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		content  []vmcp.Content
		expected map[string]any
	}{
		{
			name:     "empty array returns empty map",
			content:  []vmcp.Content{},
			expected: map[string]any{},
		},
		{
			name: "single text content",
			content: []vmcp.Content{
				{Type: "text", Text: "Hello, world!"},
			},
			expected: map[string]any{
				"text": "Hello, world!",
			},
		},
		{
			name: "multiple text contents",
			content: []vmcp.Content{
				{Type: "text", Text: "First"},
				{Type: "text", Text: "Second"},
				{Type: "text", Text: "Third"},
			},
			expected: map[string]any{
				"text":   "First",
				"text_1": "Second",
				"text_2": "Third",
			},
		},
		{
			name: "single image content",
			content: []vmcp.Content{
				{Type: "image", Data: "base64data", MimeType: "image/png"},
			},
			expected: map[string]any{
				"image_0": "base64data",
			},
		},
		{
			name: "multiple images",
			content: []vmcp.Content{
				{Type: "image", Data: "data1", MimeType: "image/png"},
				{Type: "image", Data: "data2", MimeType: "image/jpeg"},
			},
			expected: map[string]any{
				"image_0": "data1",
				"image_1": "data2",
			},
		},
		{
			name: "mixed content types",
			content: []vmcp.Content{
				{Type: "text", Text: "First text"},
				{Type: "image", Data: "image1", MimeType: "image/png"},
				{Type: "text", Text: "Second text"},
				{Type: "image", Data: "image2", MimeType: "image/jpeg"},
			},
			expected: map[string]any{
				"text":    "First text",
				"text_1":  "Second text",
				"image_0": "image1",
				"image_1": "image2",
			},
		},
		{
			name: "audio content is ignored",
			content: []vmcp.Content{
				{Type: "audio", Data: "audiodata", MimeType: "audio/mpeg"},
			},
			expected: map[string]any{},
		},
		{
			name: "audio mixed with other content is ignored",
			content: []vmcp.Content{
				{Type: "text", Text: "Text content"},
				{Type: "audio", Data: "audiodata", MimeType: "audio/mpeg"},
				{Type: "image", Data: "imagedata", MimeType: "image/png"},
			},
			expected: map[string]any{
				"text":    "Text content",
				"image_0": "imagedata",
			},
		},
		{
			name: "unknown types are ignored",
			content: []vmcp.Content{
				{Type: "text", Text: "Text"},
				{Type: "unknown", Text: "Should be ignored"},
				{Type: "resource", URI: "file://test"},
			},
			expected: map[string]any{
				"text": "Text",
			},
		},
		{
			name: "handles 10+ text items correctly",
			content: []vmcp.Content{
				{Type: "text", Text: "0"},
				{Type: "text", Text: "1"},
				{Type: "text", Text: "2"},
				{Type: "text", Text: "3"},
				{Type: "text", Text: "4"},
				{Type: "text", Text: "5"},
				{Type: "text", Text: "6"},
				{Type: "text", Text: "7"},
				{Type: "text", Text: "8"},
				{Type: "text", Text: "9"},
				{Type: "text", Text: "10"},
				{Type: "text", Text: "11"},
			},
			expected: map[string]any{
				"text":    "0",
				"text_1":  "1",
				"text_2":  "2",
				"text_3":  "3",
				"text_4":  "4",
				"text_5":  "5",
				"text_6":  "6",
				"text_7":  "7",
				"text_8":  "8",
				"text_9":  "9",
				"text_10": "10",
				"text_11": "11",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := conversion.ContentArrayToMap(tt.content)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFromMCPMeta(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    *mcp.Meta
		expected map[string]any
	}{
		{
			name:     "nil meta returns nil",
			input:    nil,
			expected: nil,
		},
		{
			name: "empty meta returns nil",
			input: &mcp.Meta{
				AdditionalFields: map[string]any{},
			},
			expected: nil,
		},
		{
			name: "meta with only progressToken",
			input: &mcp.Meta{
				ProgressToken:    "token-123",
				AdditionalFields: map[string]any{},
			},
			expected: map[string]any{
				"progressToken": "token-123",
			},
		},
		{
			name: "meta with only additional fields",
			input: &mcp.Meta{
				AdditionalFields: map[string]any{
					"traceId": "trace-456",
					"spanId":  "span-789",
				},
			},
			expected: map[string]any{
				"traceId": "trace-456",
				"spanId":  "span-789",
			},
		},
		{
			name: "meta with both progressToken and additional fields",
			input: &mcp.Meta{
				ProgressToken: "token-abc",
				AdditionalFields: map[string]any{
					"traceId": "trace-def",
					"custom":  map[string]any{"nested": "value"},
				},
			},
			expected: map[string]any{
				"progressToken": "token-abc",
				"traceId":       "trace-def",
				"custom":        map[string]any{"nested": "value"},
			},
		},
		{
			name: "progressToken with non-string type is preserved",
			input: &mcp.Meta{
				ProgressToken:    12345,
				AdditionalFields: map[string]any{},
			},
			expected: map[string]any{
				"progressToken": 12345,
			},
		},
		{
			name: "progressToken as nil is not included",
			input: &mcp.Meta{
				ProgressToken: nil,
				AdditionalFields: map[string]any{
					"traceId": "trace-123",
				},
			},
			expected: map[string]any{
				"traceId": "trace-123",
			},
		},
		{
			name: "dedicated progressToken takes precedence over AdditionalFields",
			input: &mcp.Meta{
				ProgressToken: "correct-token",
				AdditionalFields: map[string]any{
					"progressToken": "malicious-token",
					"traceId":       "trace-456",
				},
			},
			expected: map[string]any{
				"progressToken": "correct-token",
				"traceId":       "trace-456",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := conversion.FromMCPMeta(tt.input)

			if tt.expected == nil {
				assert.Nil(t, result)
			} else {
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestToMCPMeta(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    map[string]any
		expected *mcp.Meta
	}{
		{
			name:     "empty map returns nil",
			input:    map[string]any{},
			expected: nil,
		},
		{
			name:     "nil map returns nil",
			input:    nil,
			expected: nil,
		},
		{
			name: "map with only progressToken",
			input: map[string]any{
				"progressToken": "token-123",
			},
			expected: &mcp.Meta{
				ProgressToken:    "token-123",
				AdditionalFields: map[string]any{},
			},
		},
		{
			name: "map with only additional fields",
			input: map[string]any{
				"traceId": "trace-456",
				"spanId":  "span-789",
			},
			expected: &mcp.Meta{
				AdditionalFields: map[string]any{
					"traceId": "trace-456",
					"spanId":  "span-789",
				},
			},
		},
		{
			name: "map with both progressToken and additional fields",
			input: map[string]any{
				"progressToken": "token-abc",
				"traceId":       "trace-def",
				"custom":        map[string]any{"nested": "value"},
			},
			expected: &mcp.Meta{
				ProgressToken: "token-abc",
				AdditionalFields: map[string]any{
					"traceId": "trace-def",
					"custom":  map[string]any{"nested": "value"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := conversion.ToMCPMeta(tt.input)

			if tt.expected == nil {
				assert.Nil(t, result)
			} else {
				assert.NotNil(t, result)
				assert.Equal(t, tt.expected.ProgressToken, result.ProgressToken)
				assert.Equal(t, tt.expected.AdditionalFields, result.AdditionalFields)
			}
		})
	}
}

func TestMetaRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		meta *mcp.Meta
	}{
		{
			name: "full meta with progressToken and additional fields",
			meta: &mcp.Meta{
				ProgressToken: "test-token",
				AdditionalFields: map[string]any{
					"traceId":  "trace-123",
					"spanId":   "span-456",
					"customId": 789,
				},
			},
		},
		{
			name: "meta with only progressToken",
			meta: &mcp.Meta{
				ProgressToken:    "token-only",
				AdditionalFields: map[string]any{},
			},
		},
		{
			name: "meta with only additional fields",
			meta: &mcp.Meta{
				AdditionalFields: map[string]any{
					"field1": "value1",
					"field2": 42,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Convert MCP Meta → map → MCP Meta
			intermediate := conversion.FromMCPMeta(tt.meta)
			result := conversion.ToMCPMeta(intermediate)

			// Verify round-trip preserves all data
			assert.Equal(t, tt.meta.ProgressToken, result.ProgressToken)
			assert.Equal(t, tt.meta.AdditionalFields, result.AdditionalFields)
		})
	}
}

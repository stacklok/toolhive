// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package conversion_test

import (
	"encoding/base64"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/conversion"
)

func TestConvertToolInputSchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		schema mcp.ToolInputSchema
		checks func(t *testing.T, got map[string]any)
	}{
		{
			name: "captures type, properties, required",
			schema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"title": map[string]any{"type": "string"},
				},
				Required: []string{"title"},
			},
			checks: func(t *testing.T, got map[string]any) {
				t.Helper()
				assert.Equal(t, "object", got["type"])
				assert.Contains(t, got, "properties")
				required, ok := got["required"].([]any)
				require.True(t, ok)
				assert.Equal(t, []any{"title"}, required)
			},
		},
		{
			name: "captures $defs",
			schema: mcp.ToolInputSchema{
				Type: "object",
				Defs: map[string]any{"Config": map[string]any{"type": "object"}},
			},
			checks: func(t *testing.T, got map[string]any) {
				t.Helper()
				assert.Contains(t, got, "$defs")
			},
		},
		{
			name:   "nil required emitted as empty array by mcp-go",
			schema: mcp.ToolInputSchema{Type: "object", Required: nil},
			checks: func(t *testing.T, got map[string]any) {
				t.Helper()
				required, ok := got["required"].([]any)
				require.True(t, ok)
				assert.Empty(t, required)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := conversion.ConvertToolInputSchema(tt.schema)
			tt.checks(t, got)
		})
	}
}

func TestConvertPromptMessages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		messages []mcp.PromptMessage
		want     string
	}{
		{
			name:     "empty messages",
			messages: nil,
			want:     "",
		},
		{
			name: "single message with role",
			messages: []mcp.PromptMessage{
				{Role: "user", Content: mcp.NewTextContent("Hello")},
			},
			want: "[user] Hello\n",
		},
		{
			name: "message without role omits prefix",
			messages: []mcp.PromptMessage{
				{Role: "", Content: mcp.NewTextContent("No role")},
			},
			want: "No role\n",
		},
		{
			name: "multiple messages concatenated",
			messages: []mcp.PromptMessage{
				{Role: "system", Content: mcp.NewTextContent("You are helpful")},
				{Role: "user", Content: mcp.NewTextContent("Hi")},
				{Role: "assistant", Content: mcp.NewTextContent("Hello!")},
			},
			want: "[system] You are helpful\n[user] Hi\n[assistant] Hello!\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, conversion.ConvertPromptMessages(tt.messages))
		})
	}
}

func TestConvertPromptArguments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		arguments map[string]any
		want      map[string]string
	}{
		{
			name:      "nil map returns empty map",
			arguments: nil,
			want:      map[string]string{},
		},
		{
			name:      "string values pass through unchanged",
			arguments: map[string]any{"key": "value"},
			want:      map[string]string{"key": "value"},
		},
		{
			name: "non-string values are formatted",
			arguments: map[string]any{
				"int":   42,
				"bool":  true,
				"float": 3.14,
				"nil":   nil,
			},
			want: map[string]string{
				"int":   "42",
				"bool":  "true",
				"float": "3.14",
				"nil":   "<nil>",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, conversion.ConvertPromptArguments(tt.arguments))
		})
	}
}

func TestConvertMCPContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input mcp.Content
		want  vmcp.Content
	}{
		{
			name:  "text content",
			input: mcp.NewTextContent("hello world"),
			want:  vmcp.Content{Type: "text", Text: "hello world"},
		},
		{
			name:  "image content",
			input: mcp.NewImageContent("base64imgdata", "image/png"),
			want:  vmcp.Content{Type: "image", Data: "base64imgdata", MimeType: "image/png"},
		},
		{
			name:  "audio content",
			input: mcp.NewAudioContent("base64audiodata", "audio/mpeg"),
			want:  vmcp.Content{Type: "audio", Data: "base64audiodata", MimeType: "audio/mpeg"},
		},
		{
			name: "embedded resource with text content",
			input: mcp.NewEmbeddedResource(mcp.TextResourceContents{
				URI:      "file://readme.md",
				MIMEType: "text/markdown",
				Text:     "# Hello World",
			}),
			want: vmcp.Content{Type: "resource", Text: "# Hello World", URI: "file://readme.md", MimeType: "text/markdown"},
		},
		{
			name: "embedded resource with blob content",
			input: mcp.NewEmbeddedResource(mcp.BlobResourceContents{
				URI:      "file://image.png",
				MIMEType: "image/png",
				Blob:     "base64blobdata",
			}),
			want: vmcp.Content{Type: "resource", Data: "base64blobdata", URI: "file://image.png", MimeType: "image/png"},
		},
		{
			name: "embedded resource with empty URI and MimeType",
			input: mcp.NewEmbeddedResource(mcp.TextResourceContents{
				Text: "content only",
			}),
			want: vmcp.Content{Type: "resource", Text: "content only"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := conversion.ConvertMCPContent(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestConvertMCPContents(t *testing.T) {
	t.Parallel()

	t.Run("nil slice returns empty slice", func(t *testing.T) {
		t.Parallel()
		got := conversion.ConvertMCPContents(nil)
		assert.Empty(t, got)
	})

	t.Run("empty slice returns empty slice", func(t *testing.T) {
		t.Parallel()
		got := conversion.ConvertMCPContents([]mcp.Content{})
		assert.Empty(t, got)
	})

	t.Run("mixed content types are all converted", func(t *testing.T) {
		t.Parallel()
		input := []mcp.Content{
			mcp.NewTextContent("first"),
			mcp.NewImageContent("imgdata", "image/jpeg"),
			mcp.NewAudioContent("audiodata", "audio/ogg"),
		}
		want := []vmcp.Content{
			{Type: "text", Text: "first"},
			{Type: "image", Data: "imgdata", MimeType: "image/jpeg"},
			{Type: "audio", Data: "audiodata", MimeType: "audio/ogg"},
		}
		got := conversion.ConvertMCPContents(input)
		assert.Equal(t, want, got)
	})

	t.Run("order is preserved", func(t *testing.T) {
		t.Parallel()
		input := []mcp.Content{
			mcp.NewTextContent("a"),
			mcp.NewTextContent("b"),
			mcp.NewTextContent("c"),
		}
		got := conversion.ConvertMCPContents(input)
		require.Len(t, got, 3)
		assert.Equal(t, "a", got[0].Text)
		assert.Equal(t, "b", got[1].Text)
		assert.Equal(t, "c", got[2].Text)
	})
}

func TestConcatenateResourceContents(t *testing.T) {
	t.Parallel()

	rawText := "hello resource"
	blobBytes := []byte("binary data")
	blobEncoded := base64.StdEncoding.EncodeToString(blobBytes)

	tests := []struct {
		name         string
		contents     []mcp.ResourceContents
		wantData     []byte
		wantMimeType string
	}{
		{
			name:     "empty contents",
			contents: nil,
			wantData: nil,
		},
		{
			name: "single text item",
			contents: []mcp.ResourceContents{
				mcp.TextResourceContents{URI: "file://a", MIMEType: "text/plain", Text: rawText},
			},
			wantData:     []byte(rawText),
			wantMimeType: "text/plain",
		},
		{
			name: "single blob item decoded",
			contents: []mcp.ResourceContents{
				mcp.BlobResourceContents{URI: "file://b", MIMEType: "application/octet-stream", Blob: blobEncoded},
			},
			wantData:     blobBytes,
			wantMimeType: "application/octet-stream",
		},
		{
			name: "multiple text chunks concatenated",
			contents: []mcp.ResourceContents{
				mcp.TextResourceContents{URI: "file://c", MIMEType: "text/plain", Text: "part1"},
				mcp.TextResourceContents{URI: "file://c", Text: "part2"},
			},
			wantData:     []byte("part1part2"),
			wantMimeType: "text/plain",
		},
		{
			name: "mime type taken from first item only",
			contents: []mcp.ResourceContents{
				mcp.TextResourceContents{URI: "file://d", MIMEType: "text/html", Text: "a"},
				mcp.TextResourceContents{URI: "file://d", MIMEType: "text/plain", Text: "b"},
			},
			wantData:     []byte("ab"),
			wantMimeType: "text/html",
		},
		{
			name: "invalid base64 blob chunk is skipped",
			contents: []mcp.ResourceContents{
				mcp.BlobResourceContents{URI: "file://e", Blob: "not-valid-base64!!!"},
			},
			// Malformed base64 is skipped entirely; appending raw bytes would produce
			// corrupted binary data, so we prefer an empty result over corrupted data.
			wantData: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			data, mimeType := conversion.ConcatenateResourceContents(tt.contents)
			assert.Equal(t, tt.wantData, data)
			assert.Equal(t, tt.wantMimeType, mimeType)
		})
	}
}

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

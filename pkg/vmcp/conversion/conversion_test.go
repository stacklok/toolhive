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

func TestConvertMCPPromptMessages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		messages []mcp.PromptMessage
		want     []vmcp.PromptMessage
	}{
		{
			name:     "nil messages returns empty slice",
			messages: nil,
			want:     []vmcp.PromptMessage{},
		},
		{
			name:     "empty messages returns empty slice",
			messages: []mcp.PromptMessage{},
			want:     []vmcp.PromptMessage{},
		},
		{
			name: "single text message preserves role and content",
			messages: []mcp.PromptMessage{
				{Role: "user", Content: mcp.NewTextContent("Hello")},
			},
			want: []vmcp.PromptMessage{
				{Role: "user", Content: vmcp.Content{Type: vmcp.ContentTypeText, Text: "Hello"}},
			},
		},
		{
			name: "multiple messages with different roles",
			messages: []mcp.PromptMessage{
				{Role: "system", Content: mcp.NewTextContent("You are helpful")},
				{Role: "user", Content: mcp.NewTextContent("Hi")},
				{Role: "assistant", Content: mcp.NewTextContent("Hello!")},
			},
			want: []vmcp.PromptMessage{
				{Role: "system", Content: vmcp.Content{Type: vmcp.ContentTypeText, Text: "You are helpful"}},
				{Role: "user", Content: vmcp.Content{Type: vmcp.ContentTypeText, Text: "Hi"}},
				{Role: "assistant", Content: vmcp.Content{Type: vmcp.ContentTypeText, Text: "Hello!"}},
			},
		},
		{
			name: "message with image content is preserved",
			messages: []mcp.PromptMessage{
				{Role: "user", Content: mcp.NewImageContent("base64imgdata", "image/png")},
			},
			want: []vmcp.PromptMessage{
				{Role: "user", Content: vmcp.Content{Type: vmcp.ContentTypeImage, Data: "base64imgdata", MimeType: "image/png"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := conversion.ConvertMCPPromptMessages(tt.messages)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestToMCPPromptMessages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		messages []vmcp.PromptMessage
		wantLen  int
		check    func(*testing.T, []mcp.PromptMessage)
	}{
		{
			name:     "nil messages returns empty slice",
			messages: nil,
			wantLen:  0,
		},
		{
			name:     "empty messages returns empty slice",
			messages: []vmcp.PromptMessage{},
			wantLen:  0,
		},
		{
			name: "single text message preserves role and content",
			messages: []vmcp.PromptMessage{
				{Role: "user", Content: vmcp.Content{Type: vmcp.ContentTypeText, Text: "Hello"}},
			},
			wantLen: 1,
			check: func(t *testing.T, result []mcp.PromptMessage) {
				t.Helper()
				assert.Equal(t, mcp.Role("user"), result[0].Role)
				text, ok := mcp.AsTextContent(result[0].Content)
				require.True(t, ok)
				assert.Equal(t, "Hello", text.Text)
			},
		},
		{
			name: "multiple messages with different roles",
			messages: []vmcp.PromptMessage{
				{Role: "system", Content: vmcp.Content{Type: vmcp.ContentTypeText, Text: "Be helpful"}},
				{Role: "user", Content: vmcp.Content{Type: vmcp.ContentTypeText, Text: "Hi"}},
				{Role: "assistant", Content: vmcp.Content{Type: vmcp.ContentTypeText, Text: "Hello!"}},
			},
			wantLen: 3,
			check: func(t *testing.T, result []mcp.PromptMessage) {
				t.Helper()
				assert.Equal(t, mcp.Role("system"), result[0].Role)
				assert.Equal(t, mcp.Role("user"), result[1].Role)
				assert.Equal(t, mcp.Role("assistant"), result[2].Role)
			},
		},
		{
			name: "image content is preserved",
			messages: []vmcp.PromptMessage{
				{Role: "user", Content: vmcp.Content{Type: vmcp.ContentTypeImage, Data: "imgdata", MimeType: "image/png"}},
			},
			wantLen: 1,
			check: func(t *testing.T, result []mcp.PromptMessage) {
				t.Helper()
				img, ok := result[0].Content.(mcp.ImageContent)
				require.True(t, ok)
				assert.Equal(t, "imgdata", img.Data)
				assert.Equal(t, "image/png", img.MIMEType)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := conversion.ToMCPPromptMessages(tt.messages)
			assert.Len(t, got, tt.wantLen)
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

func TestPromptMessagesRoundTrip(t *testing.T) {
	t.Parallel()

	original := []mcp.PromptMessage{
		{Role: "system", Content: mcp.NewTextContent("You are helpful")},
		{Role: "user", Content: mcp.NewImageContent("base64data", "image/png")},
		{Role: "assistant", Content: mcp.NewTextContent("I see an image")},
	}

	// mcp -> vmcp -> mcp
	intermediate := conversion.ConvertMCPPromptMessages(original)
	roundTripped := conversion.ToMCPPromptMessages(intermediate)

	require.Len(t, roundTripped, len(original))
	for i, orig := range original {
		assert.Equal(t, orig.Role, roundTripped[i].Role, "role at index %d", i)
	}

	// Verify text content preserved
	text0, ok := mcp.AsTextContent(roundTripped[0].Content)
	require.True(t, ok)
	assert.Equal(t, "You are helpful", text0.Text)

	// Verify image content preserved
	img1, ok := roundTripped[1].Content.(mcp.ImageContent)
	require.True(t, ok)
	assert.Equal(t, "base64data", img1.Data)
	assert.Equal(t, "image/png", img1.MIMEType)

	// Verify second text content preserved
	text2, ok := mcp.AsTextContent(roundTripped[2].Content)
	require.True(t, ok)
	assert.Equal(t, "I see an image", text2.Text)
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
			want:  vmcp.Content{Type: vmcp.ContentTypeText, Text: "hello world"},
		},
		{
			name:  "image content",
			input: mcp.NewImageContent("base64imgdata", "image/png"),
			want:  vmcp.Content{Type: vmcp.ContentTypeImage, Data: "base64imgdata", MimeType: "image/png"},
		},
		{
			name:  "audio content",
			input: mcp.NewAudioContent("base64audiodata", "audio/mpeg"),
			want:  vmcp.Content{Type: vmcp.ContentTypeAudio, Data: "base64audiodata", MimeType: "audio/mpeg"},
		},
		{
			name: "embedded resource with text content",
			input: mcp.NewEmbeddedResource(mcp.TextResourceContents{
				URI:      "file://readme.md",
				MIMEType: "text/markdown",
				Text:     "# Hello World",
			}),
			want: vmcp.Content{Type: vmcp.ContentTypeResource, Text: "# Hello World", URI: "file://readme.md", MimeType: "text/markdown"},
		},
		{
			name: "embedded resource with blob content",
			input: mcp.NewEmbeddedResource(mcp.BlobResourceContents{
				URI:      "file://image.png",
				MIMEType: "image/png",
				Blob:     "base64blobdata",
			}),
			want: vmcp.Content{Type: vmcp.ContentTypeResource, Data: "base64blobdata", URI: "file://image.png", MimeType: "image/png"},
		},
		{
			name: "embedded resource with empty URI and MimeType",
			input: mcp.NewEmbeddedResource(mcp.TextResourceContents{
				Text: "content only",
			}),
			want: vmcp.Content{Type: vmcp.ContentTypeResource, Text: "content only"},
		},
		{
			name:  "resource_link with all fields",
			input: mcp.NewResourceLink("file://doc.pdf", "My Doc", "A PDF document", "application/pdf"),
			want: vmcp.Content{
				Type:        vmcp.ContentTypeLink,
				URI:         "file://doc.pdf",
				Name:        "My Doc",
				Description: "A PDF document",
				MimeType:    "application/pdf",
			},
		},
		{
			name:  "resource_link with empty optional fields",
			input: mcp.NewResourceLink("file://x", "X", "", ""),
			want:  vmcp.Content{Type: vmcp.ContentTypeLink, URI: "file://x", Name: "X"},
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
			{Type: vmcp.ContentTypeText, Text: "first"},
			{Type: vmcp.ContentTypeImage, Data: "imgdata", MimeType: "image/jpeg"},
			{Type: vmcp.ContentTypeAudio, Data: "audiodata", MimeType: "audio/ogg"},
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
				{Type: vmcp.ContentTypeText, Text: "Hello, world!"},
			},
			expected: map[string]any{
				"text": "Hello, world!",
			},
		},
		{
			name: "multiple text contents",
			content: []vmcp.Content{
				{Type: vmcp.ContentTypeText, Text: "First"},
				{Type: vmcp.ContentTypeText, Text: "Second"},
				{Type: vmcp.ContentTypeText, Text: "Third"},
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
				{Type: vmcp.ContentTypeImage, Data: "base64data", MimeType: "image/png"},
			},
			expected: map[string]any{
				"image_0": "base64data",
			},
		},
		{
			name: "multiple images",
			content: []vmcp.Content{
				{Type: vmcp.ContentTypeImage, Data: "data1", MimeType: "image/png"},
				{Type: vmcp.ContentTypeImage, Data: "data2", MimeType: "image/jpeg"},
			},
			expected: map[string]any{
				"image_0": "data1",
				"image_1": "data2",
			},
		},
		{
			name: "mixed content types",
			content: []vmcp.Content{
				{Type: vmcp.ContentTypeText, Text: "First text"},
				{Type: vmcp.ContentTypeImage, Data: "image1", MimeType: "image/png"},
				{Type: vmcp.ContentTypeText, Text: "Second text"},
				{Type: vmcp.ContentTypeImage, Data: "image2", MimeType: "image/jpeg"},
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
				{Type: vmcp.ContentTypeAudio, Data: "audiodata", MimeType: "audio/mpeg"},
			},
			expected: map[string]any{},
		},
		{
			name: "audio mixed with other content is ignored",
			content: []vmcp.Content{
				{Type: vmcp.ContentTypeText, Text: "Text content"},
				{Type: vmcp.ContentTypeAudio, Data: "audiodata", MimeType: "audio/mpeg"},
				{Type: vmcp.ContentTypeImage, Data: "imagedata", MimeType: "image/png"},
			},
			expected: map[string]any{
				"text":    "Text content",
				"image_0": "imagedata",
			},
		},
		{
			name: "unknown types are ignored",
			content: []vmcp.Content{
				{Type: vmcp.ContentTypeText, Text: "Text"},
				{Type: "unknown", Text: "Should be ignored"},
				{Type: vmcp.ContentTypeResource, URI: "file://test"},
			},
			expected: map[string]any{
				"text": "Text",
			},
		},
		{
			name: "handles 10+ text items correctly",
			content: []vmcp.Content{
				{Type: vmcp.ContentTypeText, Text: "0"},
				{Type: vmcp.ContentTypeText, Text: "1"},
				{Type: vmcp.ContentTypeText, Text: "2"},
				{Type: vmcp.ContentTypeText, Text: "3"},
				{Type: vmcp.ContentTypeText, Text: "4"},
				{Type: vmcp.ContentTypeText, Text: "5"},
				{Type: vmcp.ContentTypeText, Text: "6"},
				{Type: vmcp.ContentTypeText, Text: "7"},
				{Type: vmcp.ContentTypeText, Text: "8"},
				{Type: vmcp.ContentTypeText, Text: "9"},
				{Type: vmcp.ContentTypeText, Text: "10"},
				{Type: vmcp.ContentTypeText, Text: "11"},
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

func TestToMCPContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    vmcp.Content
		wantType string
		wantText string
		wantData string
		wantMime string
		wantURI  string
	}{
		{
			name:     "text content",
			input:    vmcp.Content{Type: vmcp.ContentTypeText, Text: "Hello, world!"},
			wantType: "mcp.TextContent",
			wantText: "Hello, world!",
		},
		{
			name:     "empty text content",
			input:    vmcp.Content{Type: vmcp.ContentTypeText, Text: ""},
			wantType: "mcp.TextContent",
		},
		{
			name:     "image content",
			input:    vmcp.Content{Type: vmcp.ContentTypeImage, Data: "base64data", MimeType: "image/png"},
			wantType: "mcp.ImageContent",
			wantData: "base64data",
			wantMime: "image/png",
		},
		{
			name:     "audio content",
			input:    vmcp.Content{Type: vmcp.ContentTypeAudio, Data: "audiodata", MimeType: "audio/mpeg"},
			wantType: "mcp.AudioContent",
			wantData: "audiodata",
			wantMime: "audio/mpeg",
		},
		{
			name:     "text resource content",
			input:    vmcp.Content{Type: vmcp.ContentTypeResource, Text: "# README", URI: "file://readme.md", MimeType: "text/markdown"},
			wantType: "mcp.EmbeddedResource",
			wantText: "# README",
			wantURI:  "file://readme.md",
			wantMime: "text/markdown",
		},
		{
			name:     "blob resource content",
			input:    vmcp.Content{Type: vmcp.ContentTypeResource, Data: "base64blob", URI: "file://image.png", MimeType: "image/png"},
			wantType: "mcp.EmbeddedResource",
			wantData: "base64blob",
			wantURI:  "file://image.png",
			wantMime: "image/png",
		},
		{
			name:     "empty resource content preserves resource type",
			input:    vmcp.Content{Type: vmcp.ContentTypeResource},
			wantType: "mcp.EmbeddedResource",
			wantText: "", // Empty text but still an EmbeddedResource
		},
		{
			name:     "unknown content type converts to empty text",
			input:    vmcp.Content{Type: "custom-type"},
			wantType: "mcp.TextContent",
		},
		{
			name: "resource_link content all fields",
			input: vmcp.Content{
				Type:        vmcp.ContentTypeLink,
				URI:         "file://doc.pdf",
				Name:        "My Doc",
				Description: "A PDF document",
				MimeType:    "application/pdf",
			},
			wantType: "mcp.ResourceLink",
			wantURI:  "file://doc.pdf",
			wantMime: "application/pdf",
		},
		{
			name:     "resource_link with empty fields",
			input:    vmcp.Content{Type: vmcp.ContentTypeLink},
			wantType: "mcp.ResourceLink",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := conversion.ToMCPContent(tt.input)

			switch tt.wantType {
			case "mcp.TextContent":
				text, ok := result.(mcp.TextContent)
				require.True(t, ok, "expected TextContent")
				assert.Equal(t, tt.wantText, text.Text)
			case "mcp.ImageContent":
				img, ok := result.(mcp.ImageContent)
				require.True(t, ok, "expected ImageContent")
				assert.Equal(t, tt.wantData, img.Data)
				assert.Equal(t, tt.wantMime, img.MIMEType)
			case "mcp.AudioContent":
				audio, ok := result.(mcp.AudioContent)
				require.True(t, ok, "expected AudioContent")
				assert.Equal(t, tt.wantData, audio.Data)
				assert.Equal(t, tt.wantMime, audio.MIMEType)
			case "mcp.EmbeddedResource":
				res, ok := mcp.AsEmbeddedResource(result)
				require.True(t, ok, "expected EmbeddedResource")
				// Check if it's a text resource or blob resource
				if tt.wantText != "" {
					textRes, ok := mcp.AsTextResourceContents(res.Resource)
					require.True(t, ok, "expected TextResourceContents")
					assert.Equal(t, tt.wantText, textRes.Text)
					assert.Equal(t, tt.wantURI, textRes.URI)
					assert.Equal(t, tt.wantMime, textRes.MIMEType)
				} else if tt.wantData != "" {
					blobRes, ok := mcp.AsBlobResourceContents(res.Resource)
					require.True(t, ok, "expected BlobResourceContents")
					assert.Equal(t, tt.wantData, blobRes.Blob)
					assert.Equal(t, tt.wantURI, blobRes.URI)
					assert.Equal(t, tt.wantMime, blobRes.MIMEType)
				}
			case "mcp.ResourceLink":
				link, ok := result.(mcp.ResourceLink)
				require.True(t, ok, "expected ResourceLink")
				assert.Equal(t, tt.wantURI, link.URI)
				assert.Equal(t, tt.wantMime, link.MIMEType)
				assert.Equal(t, tt.input.Name, link.Name)
				assert.Equal(t, tt.input.Description, link.Description)
			default:
				t.Errorf("unexpected wantType: %s", tt.wantType)
			}
		})
	}
}

func TestResourceContentRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		initial mcp.Content
	}{
		{
			name: "text resource round-trip preserves data",
			initial: mcp.NewEmbeddedResource(mcp.TextResourceContents{
				URI:      "file://readme.md",
				MIMEType: "text/markdown",
				Text:     "# README\n\nWelcome!",
			}),
		},
		{
			name: "blob resource round-trip preserves data",
			initial: mcp.NewEmbeddedResource(mcp.BlobResourceContents{
				URI:      "file://image.png",
				MIMEType: "image/png",
				Blob:     "base64imagedata",
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Convert mcp → vmcp
			vmcpContent := conversion.ConvertMCPContent(tt.initial)

			// Convert vmcp → mcp
			mcpContent := conversion.ToMCPContent(vmcpContent)

			// Verify the result matches the original
			initialRes, ok := mcp.AsEmbeddedResource(tt.initial)
			require.True(t, ok, "initial content should be EmbeddedResource")

			finalRes, ok := mcp.AsEmbeddedResource(mcpContent)
			require.True(t, ok, "round-trip result should be EmbeddedResource")

			// Compare text resources
			if initialText, ok := mcp.AsTextResourceContents(initialRes.Resource); ok {
				finalText, ok := mcp.AsTextResourceContents(finalRes.Resource)
				require.True(t, ok, "round-trip should preserve TextResourceContents type")
				assert.Equal(t, initialText.URI, finalText.URI, "URI should be preserved")
				assert.Equal(t, initialText.MIMEType, finalText.MIMEType, "MIMEType should be preserved")
				assert.Equal(t, initialText.Text, finalText.Text, "Text should be preserved")
			}

			// Compare blob resources
			if initialBlob, ok := mcp.AsBlobResourceContents(initialRes.Resource); ok {
				finalBlob, ok := mcp.AsBlobResourceContents(finalRes.Resource)
				require.True(t, ok, "round-trip should preserve BlobResourceContents type")
				assert.Equal(t, initialBlob.URI, finalBlob.URI, "URI should be preserved")
				assert.Equal(t, initialBlob.MIMEType, finalBlob.MIMEType, "MIMEType should be preserved")
				assert.Equal(t, initialBlob.Blob, finalBlob.Blob, "Blob should be preserved")
			}
		})
	}
}

func TestResourceLinkRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		initial mcp.ResourceLink
	}{
		{
			name:    "resource_link with all fields preserved",
			initial: mcp.NewResourceLink("file://doc.pdf", "My Doc", "A PDF document", "application/pdf"),
		},
		{
			name:    "resource_link with empty optional fields preserved",
			initial: mcp.NewResourceLink("file://x", "X", "", ""),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Convert mcp.ResourceLink → vmcp.Content
			vmcpContent := conversion.ConvertMCPContent(tt.initial)

			assert.Equal(t, vmcp.ContentTypeLink, vmcpContent.Type)
			assert.Equal(t, tt.initial.URI, vmcpContent.URI)
			assert.Equal(t, tt.initial.Name, vmcpContent.Name)
			assert.Equal(t, tt.initial.Description, vmcpContent.Description)
			assert.Equal(t, tt.initial.MIMEType, vmcpContent.MimeType)

			// Convert vmcp.Content → mcp.Content
			mcpContent := conversion.ToMCPContent(vmcpContent)

			finalLink, ok := mcpContent.(mcp.ResourceLink)
			require.True(t, ok, "round-trip result should be ResourceLink, got %T", mcpContent)
			assert.Equal(t, tt.initial.URI, finalLink.URI, "URI should be preserved")
			assert.Equal(t, tt.initial.Name, finalLink.Name, "Name should be preserved")
			assert.Equal(t, tt.initial.Description, finalLink.Description, "Description should be preserved")
			assert.Equal(t, tt.initial.MIMEType, finalLink.MIMEType, "MIMEType should be preserved")
		})
	}
}

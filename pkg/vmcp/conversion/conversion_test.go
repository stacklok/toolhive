// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package conversion_test

import (
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/conversion"
)

const (
	testSchemaTypeObject           = "object"
	testSchemaTitle                = "title"
	testRoleUser                   = "user"
	testRoleSystem                 = "system"
	testRoleAssistantExt           = "assistant"
	testMIMEImagePNG               = "image/png"
	testValueString                = "value"
	testMIMEAudioMPEG              = "audio/mpeg"
	testURIReadmeMD                = "file://readme.md"
	testMIMETextMarkdown           = "text/markdown"
	testURIImagePNG                = "file://image.png"
	testDataBase64Blob             = "base64blobdata"
	testURIDocPDF                  = "file://doc.pdf"
	testMIMEAppPDF                 = "application/pdf"
	testMIMEImageJPEG              = "image/jpeg"
	testDataAudio                  = "audiodata"
	testURIFileA                   = "file://a"
	testMIMEAppOctet               = "application/octet-stream"
	testURIFileC                   = "file://c"
	testURIFileD                   = "file://d"
	testURIFileE                   = "file://e"
	testDataPNGBase64              = "cG5nZGF0YQ=="
	testTextHelloWorld             = "Hello, world!"
	testKeyText1                   = "text_1"
	testDataBase64                 = "base64data"
	testKeyImage0                  = "image_0"
	testContentTypeText            = "Text"
	testKeyResource                = "resource"
	testMetaTokenBase              = "token-123"
	testMetaKeyProgressToken       = "progressToken"
	testMetaTraceID                = "trace-456"
	testMetaSpanID                 = "span-789"
	testMetaTokenABC               = "token-abc"
	testMetaTraceDef               = "trace-def"
	testMetaKeyCustom              = "custom"
	testMetaTrace123               = "trace-123"
	testContentTypeMCPText         = "mcp.TextContent"
	testContentTypeMCPEmbedded     = "mcp.EmbeddedResource"
	testContentTypeMCPResourceLink = "mcp.ResourceLink"
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
				Type: testSchemaTypeObject,
				Properties: map[string]any{
					testSchemaTitle: map[string]any{"type": "string"},
				},
				Required: []string{testSchemaTitle},
			},
			checks: func(t *testing.T, got map[string]any) {
				t.Helper()
				assert.Equal(t, testSchemaTypeObject, got["type"])
				assert.Contains(t, got, "properties")
				required, ok := got["required"].([]any)
				require.True(t, ok)
				assert.Equal(t, []any{testSchemaTitle}, required)
			},
		},
		{
			name: "captures $defs",
			schema: mcp.ToolInputSchema{
				Type: testSchemaTypeObject,
				Defs: map[string]any{"Config": map[string]any{"type": testSchemaTypeObject}},
			},
			checks: func(t *testing.T, got map[string]any) {
				t.Helper()
				assert.Contains(t, got, "$defs")
			},
		},
		{
			name:   "nil required emitted as empty array by mcp-go",
			schema: mcp.ToolInputSchema{Type: testSchemaTypeObject, Required: nil},
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
				{Role: testRoleUser, Content: mcp.NewTextContent("Hello")},
			},
			want: []vmcp.PromptMessage{
				{Role: testRoleUser, Content: vmcp.Content{Type: vmcp.ContentTypeText, Text: "Hello"}},
			},
		},
		{
			name: "multiple messages with different roles",
			messages: []mcp.PromptMessage{
				{Role: testRoleSystem, Content: mcp.NewTextContent("You are helpful")},
				{Role: testRoleUser, Content: mcp.NewTextContent("Hi")},
				{Role: testRoleAssistantExt, Content: mcp.NewTextContent("Hello!")},
			},
			want: []vmcp.PromptMessage{
				{Role: testRoleSystem, Content: vmcp.Content{Type: vmcp.ContentTypeText, Text: "You are helpful"}},
				{Role: testRoleUser, Content: vmcp.Content{Type: vmcp.ContentTypeText, Text: "Hi"}},
				{Role: testRoleAssistantExt, Content: vmcp.Content{Type: vmcp.ContentTypeText, Text: "Hello!"}},
			},
		},
		{
			name: "message with image content is preserved",
			messages: []mcp.PromptMessage{
				{Role: testRoleUser, Content: mcp.NewImageContent("base64imgdata", testMIMEImagePNG)},
			},
			want: []vmcp.PromptMessage{
				{Role: testRoleUser, Content: vmcp.Content{Type: vmcp.ContentTypeImage, Data: "base64imgdata", MimeType: testMIMEImagePNG}},
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
				{Role: testRoleUser, Content: vmcp.Content{Type: vmcp.ContentTypeText, Text: "Hello"}},
			},
			wantLen: 1,
			check: func(t *testing.T, result []mcp.PromptMessage) {
				t.Helper()
				assert.Equal(t, mcp.Role(testRoleUser), result[0].Role)
				text, ok := mcp.AsTextContent(result[0].Content)
				require.True(t, ok)
				assert.Equal(t, "Hello", text.Text)
			},
		},
		{
			name: "multiple messages with different roles",
			messages: []vmcp.PromptMessage{
				{Role: testRoleSystem, Content: vmcp.Content{Type: vmcp.ContentTypeText, Text: "Be helpful"}},
				{Role: testRoleUser, Content: vmcp.Content{Type: vmcp.ContentTypeText, Text: "Hi"}},
				{Role: testRoleAssistantExt, Content: vmcp.Content{Type: vmcp.ContentTypeText, Text: "Hello!"}},
			},
			wantLen: 3,
			check: func(t *testing.T, result []mcp.PromptMessage) {
				t.Helper()
				assert.Equal(t, mcp.Role(testRoleSystem), result[0].Role)
				assert.Equal(t, mcp.Role(testRoleUser), result[1].Role)
				assert.Equal(t, mcp.Role(testRoleAssistantExt), result[2].Role)
			},
		},
		{
			name: "image content is preserved",
			messages: []vmcp.PromptMessage{
				{Role: testRoleUser, Content: vmcp.Content{Type: vmcp.ContentTypeImage, Data: "imgdata", MimeType: testMIMEImagePNG}},
			},
			wantLen: 1,
			check: func(t *testing.T, result []mcp.PromptMessage) {
				t.Helper()
				img, ok := result[0].Content.(mcp.ImageContent)
				require.True(t, ok)
				assert.Equal(t, "imgdata", img.Data)
				assert.Equal(t, testMIMEImagePNG, img.MIMEType)
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
		{Role: testRoleSystem, Content: mcp.NewTextContent("You are helpful")},
		{Role: testRoleUser, Content: mcp.NewImageContent(testDataBase64, testMIMEImagePNG)},
		{Role: testRoleAssistantExt, Content: mcp.NewTextContent("I see an image")},
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
	assert.Equal(t, testDataBase64, img1.Data)
	assert.Equal(t, testMIMEImagePNG, img1.MIMEType)

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
			arguments: map[string]any{"key": testValueString},
			want:      map[string]string{"key": testValueString},
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
			input: mcp.NewImageContent("base64imgdata", testMIMEImagePNG),
			want:  vmcp.Content{Type: vmcp.ContentTypeImage, Data: "base64imgdata", MimeType: testMIMEImagePNG},
		},
		{
			name:  "audio content",
			input: mcp.NewAudioContent("base64audiodata", testMIMEAudioMPEG),
			want:  vmcp.Content{Type: vmcp.ContentTypeAudio, Data: "base64audiodata", MimeType: testMIMEAudioMPEG},
		},
		{
			name: "embedded resource with text content",
			input: mcp.NewEmbeddedResource(mcp.TextResourceContents{
				URI:      testURIReadmeMD,
				MIMEType: testMIMETextMarkdown,
				Text:     "# Hello World",
			}),
			want: vmcp.Content{Type: vmcp.ContentTypeResource, Text: "# Hello World", URI: testURIReadmeMD, MimeType: testMIMETextMarkdown},
		},
		{
			name: "embedded resource with blob content",
			input: mcp.NewEmbeddedResource(mcp.BlobResourceContents{
				URI:      testURIImagePNG,
				MIMEType: testMIMEImagePNG,
				Blob:     testDataBase64Blob,
			}),
			want: vmcp.Content{Type: vmcp.ContentTypeResource, Data: testDataBase64Blob, URI: testURIImagePNG, MimeType: testMIMEImagePNG},
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
			input: mcp.NewResourceLink(testURIDocPDF, "My Doc", "A PDF document", testMIMEAppPDF),
			want: vmcp.Content{
				Type:        vmcp.ContentTypeLink,
				URI:         testURIDocPDF,
				Name:        "My Doc",
				Description: "A PDF document",
				MimeType:    testMIMEAppPDF,
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
			mcp.NewImageContent("imgdata", testMIMEImageJPEG),
			mcp.NewAudioContent(testDataAudio, "audio/ogg"),
		}
		want := []vmcp.Content{
			{Type: vmcp.ContentTypeText, Text: "first"},
			{Type: vmcp.ContentTypeImage, Data: "imgdata", MimeType: testMIMEImageJPEG},
			{Type: vmcp.ContentTypeAudio, Data: testDataAudio, MimeType: "audio/ogg"},
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

func TestConvertMCPResourceContents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		contents []mcp.ResourceContents
		want     []vmcp.ResourceContent
	}{
		{
			name:     "nil contents returns empty slice",
			contents: nil,
			want:     []vmcp.ResourceContent{},
		},
		{
			name: "single text item",
			contents: []mcp.ResourceContents{
				mcp.TextResourceContents{URI: testURIFileA, MIMEType: "text/plain", Text: "hello resource"},
			},
			want: []vmcp.ResourceContent{
				{URI: testURIFileA, MimeType: "text/plain", Text: "hello resource"},
			},
		},
		{
			name: "single blob item preserved as base64",
			contents: []mcp.ResourceContents{
				mcp.BlobResourceContents{URI: "file://b", MIMEType: testMIMEAppOctet, Blob: "YmluYXJ5IGRhdGE="},
			},
			want: []vmcp.ResourceContent{
				{URI: "file://b", MimeType: testMIMEAppOctet, Blob: "YmluYXJ5IGRhdGE="},
			},
		},
		{
			name: "multiple items preserve per-item URIs and MIME types",
			contents: []mcp.ResourceContents{
				mcp.TextResourceContents{URI: testURIFileC, MIMEType: "text/plain", Text: "part1"},
				mcp.TextResourceContents{URI: testURIFileD, MIMEType: "text/html", Text: "part2"},
			},
			want: []vmcp.ResourceContent{
				{URI: testURIFileC, MimeType: "text/plain", Text: "part1"},
				{URI: testURIFileD, MimeType: "text/html", Text: "part2"},
			},
		},
		{
			name: "mixed text and blob items",
			contents: []mcp.ResourceContents{
				mcp.TextResourceContents{URI: testURIFileE, MIMEType: "text/plain", Text: "text"},
				mcp.BlobResourceContents{URI: "file://f", MIMEType: testMIMEImagePNG, Blob: testDataPNGBase64},
			},
			want: []vmcp.ResourceContent{
				{URI: testURIFileE, MimeType: "text/plain", Text: "text"},
				{URI: "file://f", MimeType: testMIMEImagePNG, Blob: testDataPNGBase64},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := conversion.ConvertMCPResourceContents(tt.contents)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestToMCPResourceContents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		contents []vmcp.ResourceContent
		check    func(t *testing.T, result []mcp.ResourceContents)
	}{
		{
			name:     "nil contents returns empty slice",
			contents: nil,
			check: func(t *testing.T, result []mcp.ResourceContents) {
				t.Helper()
				assert.Empty(t, result)
			},
		},
		{
			name: "text content produces TextResourceContents",
			contents: []vmcp.ResourceContent{
				{URI: testURIFileA, MimeType: "text/plain", Text: "hello"},
			},
			check: func(t *testing.T, result []mcp.ResourceContents) {
				t.Helper()
				require.Len(t, result, 1)
				textRes, ok := mcp.AsTextResourceContents(result[0])
				require.True(t, ok, "expected TextResourceContents")
				assert.Equal(t, testURIFileA, textRes.URI)
				assert.Equal(t, "text/plain", textRes.MIMEType)
				assert.Equal(t, "hello", textRes.Text)
			},
		},
		{
			name: "blob content produces BlobResourceContents",
			contents: []vmcp.ResourceContent{
				{URI: "file://b", MimeType: testMIMEImagePNG, Blob: testDataPNGBase64},
			},
			check: func(t *testing.T, result []mcp.ResourceContents) {
				t.Helper()
				require.Len(t, result, 1)
				blobRes, ok := mcp.AsBlobResourceContents(result[0])
				require.True(t, ok, "expected BlobResourceContents")
				assert.Equal(t, "file://b", blobRes.URI)
				assert.Equal(t, testMIMEImagePNG, blobRes.MIMEType)
				assert.Equal(t, testDataPNGBase64, blobRes.Blob)
			},
		},
		{
			name: "empty text and blob produces TextResourceContents",
			contents: []vmcp.ResourceContent{
				{URI: testURIFileC, MimeType: "text/plain"},
			},
			check: func(t *testing.T, result []mcp.ResourceContents) {
				t.Helper()
				require.Len(t, result, 1)
				textRes, ok := mcp.AsTextResourceContents(result[0])
				require.True(t, ok, "expected TextResourceContents for empty content")
				assert.Equal(t, testURIFileC, textRes.URI)
				assert.Equal(t, "text/plain", textRes.MIMEType)
				assert.Equal(t, "", textRes.Text)
			},
		},
		{
			name: "mixed items preserve order and types",
			contents: []vmcp.ResourceContent{
				{URI: testURIFileD, MimeType: "text/plain", Text: "text data"},
				{URI: testURIFileE, MimeType: testMIMEImagePNG, Blob: testDataPNGBase64},
			},
			check: func(t *testing.T, result []mcp.ResourceContents) {
				t.Helper()
				require.Len(t, result, 2)
				_, ok := mcp.AsTextResourceContents(result[0])
				assert.True(t, ok, "first item should be TextResourceContents")
				_, ok = mcp.AsBlobResourceContents(result[1])
				assert.True(t, ok, "second item should be BlobResourceContents")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := conversion.ToMCPResourceContents(tt.contents)
			tt.check(t, got)
		})
	}
}

func TestResourceContentsRoundTrip(t *testing.T) {
	t.Parallel()

	t.Run("text resource round-trip", func(t *testing.T) {
		t.Parallel()
		input := []mcp.ResourceContents{
			mcp.TextResourceContents{URI: testURIFileA, MIMEType: "text/plain", Text: "hello"},
		}
		intermediate := conversion.ConvertMCPResourceContents(input)
		output := conversion.ToMCPResourceContents(intermediate)
		require.Len(t, output, 1)
		textRes, ok := mcp.AsTextResourceContents(output[0])
		require.True(t, ok)
		assert.Equal(t, testURIFileA, textRes.URI)
		assert.Equal(t, "text/plain", textRes.MIMEType)
		assert.Equal(t, "hello", textRes.Text)
	})

	t.Run("blob resource round-trip", func(t *testing.T) {
		t.Parallel()
		input := []mcp.ResourceContents{
			mcp.BlobResourceContents{URI: "file://b", MIMEType: testMIMEImagePNG, Blob: testDataPNGBase64},
		}
		intermediate := conversion.ConvertMCPResourceContents(input)
		output := conversion.ToMCPResourceContents(intermediate)
		require.Len(t, output, 1)
		blobRes, ok := mcp.AsBlobResourceContents(output[0])
		require.True(t, ok)
		assert.Equal(t, "file://b", blobRes.URI)
		assert.Equal(t, testMIMEImagePNG, blobRes.MIMEType)
		assert.Equal(t, testDataPNGBase64, blobRes.Blob)
	})
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
				{Type: vmcp.ContentTypeText, Text: testTextHelloWorld},
			},
			expected: map[string]any{
				"text": testTextHelloWorld,
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
				"text":       "First",
				testKeyText1: "Second",
				"text_2":     "Third",
			},
		},
		{
			name: "single image content",
			content: []vmcp.Content{
				{Type: vmcp.ContentTypeImage, Data: testDataBase64, MimeType: testMIMEImagePNG},
			},
			expected: map[string]any{
				testKeyImage0: testDataBase64,
			},
		},
		{
			name: "multiple images",
			content: []vmcp.Content{
				{Type: vmcp.ContentTypeImage, Data: "data1", MimeType: testMIMEImagePNG},
				{Type: vmcp.ContentTypeImage, Data: "data2", MimeType: testMIMEImageJPEG},
			},
			expected: map[string]any{
				testKeyImage0: "data1",
				"image_1":     "data2",
			},
		},
		{
			name: "mixed content types",
			content: []vmcp.Content{
				{Type: vmcp.ContentTypeText, Text: "First text"},
				{Type: vmcp.ContentTypeImage, Data: "image1", MimeType: testMIMEImagePNG},
				{Type: vmcp.ContentTypeText, Text: "Second text"},
				{Type: vmcp.ContentTypeImage, Data: "image2", MimeType: testMIMEImageJPEG},
			},
			expected: map[string]any{
				"text":        "First text",
				testKeyText1:  "Second text",
				testKeyImage0: "image1",
				"image_1":     "image2",
			},
		},
		{
			name: "audio content is ignored",
			content: []vmcp.Content{
				{Type: vmcp.ContentTypeAudio, Data: testDataAudio, MimeType: testMIMEAudioMPEG},
			},
			expected: map[string]any{},
		},
		{
			name: "audio mixed with other content is ignored",
			content: []vmcp.Content{
				{Type: vmcp.ContentTypeText, Text: testContentTypeText},
				{Type: vmcp.ContentTypeAudio, Data: testDataAudio, MimeType: testMIMEAudioMPEG},
				{Type: vmcp.ContentTypeImage, Data: "imagedata", MimeType: testMIMEImagePNG},
			},
			expected: map[string]any{
				"text":        testContentTypeText,
				testKeyImage0: "imagedata",
			},
		},
		{
			name: "unknown types are ignored",
			content: []vmcp.Content{
				{Type: vmcp.ContentTypeText, Text: testContentTypeText},
				{Type: "unknown", Text: "Should be ignored"},
			},
			expected: map[string]any{
				"text": testContentTypeText,
			},
		},
		{
			name: "single text resource content",
			content: []vmcp.Content{
				{Type: vmcp.ContentTypeResource, Text: "SBOM JSON data", URI: "file://sbom.json", MimeType: "application/json"},
			},
			expected: map[string]any{
				testKeyResource: "SBOM JSON data",
			},
		},
		{
			name: "single blob resource content uses Data field",
			content: []vmcp.Content{
				{Type: vmcp.ContentTypeResource, Data: testDataBase64Blob, URI: "file://binary", MimeType: testMIMEAppOctet},
			},
			expected: map[string]any{
				testKeyResource: testDataBase64Blob,
			},
		},
		{
			name: "multiple resource contents",
			content: []vmcp.Content{
				{Type: vmcp.ContentTypeResource, Text: "First resource", URI: testURIFileA},
				{Type: vmcp.ContentTypeResource, Text: "Second resource", URI: "file://b"},
				{Type: vmcp.ContentTypeResource, Data: "Third blob", URI: testURIFileC},
			},
			expected: map[string]any{
				testKeyResource: "First resource",
				"resource_1":    "Second resource",
				"resource_2":    "Third blob",
			},
		},
		{
			name: "mixed text and resource content",
			content: []vmcp.Content{
				{Type: vmcp.ContentTypeText, Text: "summary"},
				{Type: vmcp.ContentTypeResource, Text: "SBOM JSON", URI: "file://sbom.json"},
			},
			expected: map[string]any{
				"text":          "summary",
				testKeyResource: "SBOM JSON",
			},
		},
		{
			name: "resource link content is still ignored",
			content: []vmcp.Content{
				{Type: vmcp.ContentTypeText, Text: testContentTypeText},
				{Type: vmcp.ContentTypeLink, URI: "file://link", Name: "link"},
			},
			expected: map[string]any{
				"text": testContentTypeText,
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
				"text":       "0",
				testKeyText1: "1",
				"text_2":     "2",
				"text_3":     "3",
				"text_4":     "4",
				"text_5":     "5",
				"text_6":     "6",
				"text_7":     "7",
				"text_8":     "8",
				"text_9":     "9",
				"text_10":    "10",
				"text_11":    "11",
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
				ProgressToken:    testMetaTokenBase,
				AdditionalFields: map[string]any{},
			},
			expected: map[string]any{
				testMetaKeyProgressToken: testMetaTokenBase,
			},
		},
		{
			name: "meta with only additional fields",
			input: &mcp.Meta{
				AdditionalFields: map[string]any{
					"traceId": testMetaTraceID,
					"spanId":  testMetaSpanID,
				},
			},
			expected: map[string]any{
				"traceId": testMetaTraceID,
				"spanId":  testMetaSpanID,
			},
		},
		{
			name: "meta with both progressToken and additional fields",
			input: &mcp.Meta{
				ProgressToken: testMetaTokenABC,
				AdditionalFields: map[string]any{
					"traceId":         testMetaTraceDef,
					testMetaKeyCustom: map[string]any{"nested": testValueString},
				},
			},
			expected: map[string]any{
				testMetaKeyProgressToken: testMetaTokenABC,
				"traceId":                testMetaTraceDef,
				testMetaKeyCustom:        map[string]any{"nested": testValueString},
			},
		},
		{
			name: "progressToken with non-string type is preserved",
			input: &mcp.Meta{
				ProgressToken:    12345,
				AdditionalFields: map[string]any{},
			},
			expected: map[string]any{
				testMetaKeyProgressToken: 12345,
			},
		},
		{
			name: "progressToken as nil is not included",
			input: &mcp.Meta{
				ProgressToken: nil,
				AdditionalFields: map[string]any{
					"traceId": testMetaTrace123,
				},
			},
			expected: map[string]any{
				"traceId": testMetaTrace123,
			},
		},
		{
			name: "dedicated progressToken takes precedence over AdditionalFields",
			input: &mcp.Meta{
				ProgressToken: "correct-token",
				AdditionalFields: map[string]any{
					testMetaKeyProgressToken: "malicious-token",
					"traceId":                testMetaTraceID,
				},
			},
			expected: map[string]any{
				testMetaKeyProgressToken: "correct-token",
				"traceId":                testMetaTraceID,
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
				testMetaKeyProgressToken: testMetaTokenBase,
			},
			expected: &mcp.Meta{
				ProgressToken:    testMetaTokenBase,
				AdditionalFields: map[string]any{},
			},
		},
		{
			name: "map with only additional fields",
			input: map[string]any{
				"traceId": testMetaTraceID,
				"spanId":  testMetaSpanID,
			},
			expected: &mcp.Meta{
				AdditionalFields: map[string]any{
					"traceId": testMetaTraceID,
					"spanId":  testMetaSpanID,
				},
			},
		},
		{
			name: "map with both progressToken and additional fields",
			input: map[string]any{
				testMetaKeyProgressToken: testMetaTokenABC,
				"traceId":                testMetaTraceDef,
				testMetaKeyCustom:        map[string]any{"nested": testValueString},
			},
			expected: &mcp.Meta{
				ProgressToken: testMetaTokenABC,
				AdditionalFields: map[string]any{
					"traceId":         testMetaTraceDef,
					testMetaKeyCustom: map[string]any{"nested": testValueString},
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
					"traceId":  testMetaTrace123,
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
			input:    vmcp.Content{Type: vmcp.ContentTypeText, Text: testTextHelloWorld},
			wantType: testContentTypeMCPText,
			wantText: testTextHelloWorld,
		},
		{
			name:     "empty text content",
			input:    vmcp.Content{Type: vmcp.ContentTypeText, Text: ""},
			wantType: testContentTypeMCPText,
		},
		{
			name:     "image content",
			input:    vmcp.Content{Type: vmcp.ContentTypeImage, Data: testDataBase64, MimeType: testMIMEImagePNG},
			wantType: "mcp.ImageContent",
			wantData: testDataBase64,
			wantMime: testMIMEImagePNG,
		},
		{
			name:     "audio content",
			input:    vmcp.Content{Type: vmcp.ContentTypeAudio, Data: testDataAudio, MimeType: testMIMEAudioMPEG},
			wantType: "mcp.AudioContent",
			wantData: testDataAudio,
			wantMime: testMIMEAudioMPEG,
		},
		{
			name:     "text resource content",
			input:    vmcp.Content{Type: vmcp.ContentTypeResource, Text: "# README", URI: testURIReadmeMD, MimeType: testMIMETextMarkdown},
			wantType: testContentTypeMCPEmbedded,
			wantText: "# README",
			wantURI:  testURIReadmeMD,
			wantMime: testMIMETextMarkdown,
		},
		{
			name:     "blob resource content",
			input:    vmcp.Content{Type: vmcp.ContentTypeResource, Data: "base64blob", URI: testURIImagePNG, MimeType: testMIMEImagePNG},
			wantType: testContentTypeMCPEmbedded,
			wantData: "base64blob",
			wantURI:  testURIImagePNG,
			wantMime: testMIMEImagePNG,
		},
		{
			name:     "empty resource content preserves resource type",
			input:    vmcp.Content{Type: vmcp.ContentTypeResource},
			wantType: testContentTypeMCPEmbedded,
			wantText: "", // Empty text but still an EmbeddedResource
		},
		{
			name:     "unknown content type converts to empty text",
			input:    vmcp.Content{Type: "custom-type"},
			wantType: testContentTypeMCPText,
		},
		{
			name: "resource_link content all fields",
			input: vmcp.Content{
				Type:        vmcp.ContentTypeLink,
				URI:         testURIDocPDF,
				Name:        "My Doc",
				Description: "A PDF document",
				MimeType:    testMIMEAppPDF,
			},
			wantType: testContentTypeMCPResourceLink,
			wantURI:  testURIDocPDF,
			wantMime: testMIMEAppPDF,
		},
		{
			name:     "resource_link with empty fields",
			input:    vmcp.Content{Type: vmcp.ContentTypeLink},
			wantType: testContentTypeMCPResourceLink,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := conversion.ToMCPContent(tt.input)

			switch tt.wantType {
			case testContentTypeMCPText:
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
			case testContentTypeMCPEmbedded:
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
			case testContentTypeMCPResourceLink:
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
				URI:      testURIReadmeMD,
				MIMEType: testMIMETextMarkdown,
				Text:     "# README\n\nWelcome!",
			}),
		},
		{
			name: "blob resource round-trip preserves data",
			initial: mcp.NewEmbeddedResource(mcp.BlobResourceContents{
				URI:      testURIImagePNG,
				MIMEType: testMIMEImagePNG,
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
			initial: mcp.NewResourceLink(testURIDocPDF, "My Doc", "A PDF document", testMIMEAppPDF),
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

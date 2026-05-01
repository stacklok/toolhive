// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package conversion

import (
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// testLastModified is a fixed RFC 3339 timestamp used across annotation test cases.
const testLastModified = "2025-01-12T15:00:58Z"

func boolPtr(b bool) *bool          { return &b }
func float64Ptr(f float64) *float64 { return &f }

func TestConvertToolAnnotations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input mcp.ToolAnnotation
		want  *vmcp.ToolAnnotations
	}{
		{
			name: "all fields populated",
			input: mcp.ToolAnnotation{
				Title:           "My Tool",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			},
			want: &vmcp.ToolAnnotations{
				Title:           "My Tool",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			},
		},
		{
			name:  "all zero-valued returns nil",
			input: mcp.ToolAnnotation{},
			want:  nil,
		},
		{
			name: "only Title set",
			input: mcp.ToolAnnotation{
				Title: "Just a Title",
			},
			want: &vmcp.ToolAnnotations{
				Title: "Just a Title",
			},
		},
		{
			name: "only ReadOnlyHint set",
			input: mcp.ToolAnnotation{
				ReadOnlyHint: boolPtr(true),
			},
			want: &vmcp.ToolAnnotations{
				ReadOnlyHint: boolPtr(true),
			},
		},
		{
			name: "mixed hints with some nil",
			input: mcp.ToolAnnotation{
				Title:           "Mixed",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: nil,
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   nil,
			},
			want: &vmcp.ToolAnnotations{
				Title:          "Mixed",
				ReadOnlyHint:   boolPtr(false),
				IdempotentHint: boolPtr(true),
			},
		},
		{
			name: "only DestructiveHint set to false",
			input: mcp.ToolAnnotation{
				DestructiveHint: boolPtr(false),
			},
			want: &vmcp.ToolAnnotations{
				DestructiveHint: boolPtr(false),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ConvertToolAnnotations(tt.input)
			if tt.want == nil {
				assert.Nil(t, got)
			} else {
				require.NotNil(t, got)
				assert.Equal(t, tt.want.Title, got.Title)
				assert.Equal(t, tt.want.ReadOnlyHint, got.ReadOnlyHint)
				assert.Equal(t, tt.want.DestructiveHint, got.DestructiveHint)
				assert.Equal(t, tt.want.IdempotentHint, got.IdempotentHint)
				assert.Equal(t, tt.want.OpenWorldHint, got.OpenWorldHint)
			}
		})
	}
}

func TestConvertToolOutputSchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input mcp.ToolOutputSchema
		want  map[string]any
	}{
		{
			name: "schema with type and properties",
			input: mcp.ToolOutputSchema{
				Type: "object",
				Properties: map[string]any{
					"result": map[string]any{"type": "string"},
					"count":  map[string]any{"type": "integer"},
				},
			},
			want: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"result": map[string]any{"type": "string"},
					"count":  map[string]any{"type": "integer"},
				},
			},
		},
		{
			name:  "empty schema returns nil",
			input: mcp.ToolOutputSchema{},
			want:  nil,
		},
		{
			name:  "schema with only type field",
			input: mcp.ToolOutputSchema{Type: "string"},
			want:  map[string]any{"type": "string"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ConvertToolOutputSchema(tt.input)
			if tt.want == nil {
				assert.Nil(t, got)
			} else {
				require.NotNil(t, got)
				// Check type field
				assert.Equal(t, tt.want["type"], got["type"])
				// Check properties if expected
				if expectedProps, ok := tt.want["properties"]; ok {
					assert.Equal(t, expectedProps, got["properties"])
				}
			}
		})
	}
}

func TestToMCPToolAnnotations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input *vmcp.ToolAnnotations
		check func(t *testing.T, got mcp.ToolAnnotation)
	}{
		{
			name:  "nil input returns zero-valued ToolAnnotation",
			input: nil,
			check: func(t *testing.T, got mcp.ToolAnnotation) {
				t.Helper()
				assert.Empty(t, got.Title)
				assert.Nil(t, got.ReadOnlyHint)
				assert.Nil(t, got.DestructiveHint)
				assert.Nil(t, got.IdempotentHint)
				assert.Nil(t, got.OpenWorldHint)
			},
		},
		{
			name: "fully populated input",
			input: &vmcp.ToolAnnotations{
				Title:           "Full Tool",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			},
			check: func(t *testing.T, got mcp.ToolAnnotation) {
				t.Helper()
				assert.Equal(t, "Full Tool", got.Title)
				require.NotNil(t, got.ReadOnlyHint)
				assert.True(t, *got.ReadOnlyHint)
				require.NotNil(t, got.DestructiveHint)
				assert.False(t, *got.DestructiveHint)
				require.NotNil(t, got.IdempotentHint)
				assert.True(t, *got.IdempotentHint)
				require.NotNil(t, got.OpenWorldHint)
				assert.False(t, *got.OpenWorldHint)
			},
		},
		{
			name: "partial fields",
			input: &vmcp.ToolAnnotations{
				Title:        "Partial",
				ReadOnlyHint: boolPtr(true),
			},
			check: func(t *testing.T, got mcp.ToolAnnotation) {
				t.Helper()
				assert.Equal(t, "Partial", got.Title)
				require.NotNil(t, got.ReadOnlyHint)
				assert.True(t, *got.ReadOnlyHint)
				assert.Nil(t, got.DestructiveHint)
				assert.Nil(t, got.IdempotentHint)
				assert.Nil(t, got.OpenWorldHint)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ToMCPToolAnnotations(tt.input)
			tt.check(t, got)
		})
	}
}

func TestAnnotationsRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input mcp.ToolAnnotation
	}{
		{
			name: "fully populated round-trips",
			input: mcp.ToolAnnotation{
				Title:           "Round Trip Tool",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			},
		},
		{
			name: "partial fields round-trip",
			input: mcp.ToolAnnotation{
				Title:        "Partial Round Trip",
				ReadOnlyHint: boolPtr(false),
			},
		},
		{
			name: "only hints round-trip",
			input: mcp.ToolAnnotation{
				DestructiveHint: boolPtr(true),
				OpenWorldHint:   boolPtr(true),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// mcp.ToolAnnotation -> vmcp.ToolAnnotations -> mcp.ToolAnnotation
			intermediate := ConvertToolAnnotations(tt.input)
			require.NotNil(t, intermediate, "intermediate should not be nil for non-empty input")

			result := ToMCPToolAnnotations(intermediate)

			assert.Equal(t, tt.input.Title, result.Title)
			assert.Equal(t, tt.input.ReadOnlyHint, result.ReadOnlyHint)
			assert.Equal(t, tt.input.DestructiveHint, result.DestructiveHint)
			assert.Equal(t, tt.input.IdempotentHint, result.IdempotentHint)
			assert.Equal(t, tt.input.OpenWorldHint, result.OpenWorldHint)
		})
	}
}

func TestConvertMCPAnnotations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input *mcp.Annotations
		want  *vmcp.ContentAnnotations
	}{
		{
			name:  "nil input returns nil",
			input: nil,
			want:  nil,
		},
		{
			name:  "empty annotations returns nil",
			input: &mcp.Annotations{},
			want:  nil,
		},
		{
			name: "fully populated",
			input: &mcp.Annotations{
				Audience:     []mcp.Role{mcp.RoleUser, mcp.RoleAssistant},
				Priority:     float64Ptr(0.8),
				LastModified: testLastModified,
			},
			want: &vmcp.ContentAnnotations{
				Audience:     []string{"user", "assistant"},
				Priority:     float64Ptr(0.8),
				LastModified: testLastModified,
			},
		},
		{
			name: "only audience",
			input: &mcp.Annotations{
				Audience: []mcp.Role{mcp.RoleUser},
			},
			want: &vmcp.ContentAnnotations{
				Audience: []string{"user"},
			},
		},
		{
			name: "only priority",
			input: &mcp.Annotations{
				Priority: float64Ptr(0.5),
			},
			want: &vmcp.ContentAnnotations{
				Priority: float64Ptr(0.5),
			},
		},
		{
			name: "only lastModified",
			input: &mcp.Annotations{
				LastModified: "2025-06-01T00:00:00Z",
			},
			want: &vmcp.ContentAnnotations{
				LastModified: "2025-06-01T00:00:00Z",
			},
		},
		{
			name: "priority zero is preserved (not collapsed to nil)",
			input: &mcp.Annotations{
				Priority: float64Ptr(0.0),
			},
			want: &vmcp.ContentAnnotations{
				Priority: float64Ptr(0.0),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ConvertMCPAnnotations(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestToMCPAnnotations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input *vmcp.ContentAnnotations
		want  *mcp.Annotations
	}{
		{
			name:  "nil input returns nil",
			input: nil,
			want:  nil,
		},
		{
			name:  "empty non-nil input returns nil",
			input: &vmcp.ContentAnnotations{},
			want:  nil,
		},
		{
			name: "fully populated",
			input: &vmcp.ContentAnnotations{
				Audience:     []string{"user", "assistant"},
				Priority:     float64Ptr(0.8),
				LastModified: testLastModified,
			},
			want: &mcp.Annotations{
				Audience:     []mcp.Role{mcp.RoleUser, mcp.RoleAssistant},
				Priority:     float64Ptr(0.8),
				LastModified: testLastModified,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ToMCPAnnotations(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestContentAnnotationsRoundTrip(t *testing.T) {
	t.Parallel()

	ann := &mcp.Annotations{
		Audience:     []mcp.Role{mcp.RoleUser},
		Priority:     float64Ptr(0.9),
		LastModified: "2025-03-24T10:00:00Z",
	}

	// Create annotated text content
	tc := mcp.NewTextContent("hello")
	tc.Annotated = mcp.Annotated{Annotations: ann}

	// mcp -> vmcp -> mcp round trip
	vmcpContent := ConvertMCPContent(tc)
	require.NotNil(t, vmcpContent.Annotations)
	assert.Equal(t, []string{"user"}, vmcpContent.Annotations.Audience)
	assert.Equal(t, float64Ptr(0.9), vmcpContent.Annotations.Priority)
	assert.Equal(t, "2025-03-24T10:00:00Z", vmcpContent.Annotations.LastModified)

	mcpContent := ToMCPContent(vmcpContent)
	text, ok := mcp.AsTextContent(mcpContent)
	require.True(t, ok)
	assert.Equal(t, "hello", text.Text)
	require.NotNil(t, text.Annotations)
	assert.Equal(t, ann.Audience, text.Annotations.Audience)
	assert.Equal(t, ann.Priority, text.Annotations.Priority)
	assert.Equal(t, ann.LastModified, text.Annotations.LastModified)
}

func TestContentAnnotationsRoundTrip_AllTypes(t *testing.T) {
	t.Parallel()

	ann := &mcp.Annotations{
		Audience: []mcp.Role{mcp.RoleAssistant},
		Priority: float64Ptr(0.5),
	}

	tests := []struct {
		name    string
		content mcp.Content
	}{
		{
			name: "image content",
			content: func() mcp.Content {
				ic := mcp.NewImageContent("base64data", "image/png")
				ic.Annotated = mcp.Annotated{Annotations: ann}
				return ic
			}(),
		},
		{
			name: "audio content",
			content: func() mcp.Content {
				ac := mcp.NewAudioContent("base64audio", "audio/wav")
				ac.Annotated = mcp.Annotated{Annotations: ann}
				return ac
			}(),
		},
		{
			name: "text embedded resource",
			content: func() mcp.Content {
				er := mcp.NewEmbeddedResource(mcp.TextResourceContents{URI: "file://x", Text: "txt"})
				er.Annotated = mcp.Annotated{Annotations: ann}
				return er
			}(),
		},
		{
			name: "blob embedded resource",
			content: func() mcp.Content {
				er := mcp.NewEmbeddedResource(mcp.BlobResourceContents{URI: "file://y", Blob: "YmluYXJ5", MIMEType: "application/octet-stream"})
				er.Annotated = mcp.Annotated{Annotations: ann}
				return er
			}(),
		},
		{
			name: "resource link",
			content: func() mcp.Content {
				rl := mcp.NewResourceLink("file://x", "name", "desc", "text/plain")
				rl.Annotated = mcp.Annotated{Annotations: ann}
				return rl
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			vmcpC := ConvertMCPContent(tt.content)
			require.NotNil(t, vmcpC.Annotations, "annotations should be preserved")
			assert.Equal(t, []string{"assistant"}, vmcpC.Annotations.Audience)
			assert.Equal(t, float64Ptr(0.5), vmcpC.Annotations.Priority)

			mcpC := ToMCPContent(vmcpC)
			// Verify type is preserved (not degraded to unknown/empty text)
			assert.Equal(t, vmcpC.Type, ConvertMCPContent(mcpC).Type)
			// Verify annotations survived the round trip
			roundTripped := ConvertMCPContent(mcpC)
			require.NotNil(t, roundTripped.Annotations)
			assert.Equal(t, vmcpC.Annotations, roundTripped.Annotations)
		})
	}
}

func TestContentWithoutAnnotations(t *testing.T) {
	t.Parallel()

	// Content without annotations should have nil Annotations field
	tc := mcp.NewTextContent("no annotations")
	vmcpC := ConvertMCPContent(tc)
	assert.Nil(t, vmcpC.Annotations)

	// Round-trip should preserve nil
	mcpC := ToMCPContent(vmcpC)
	text, ok := mcp.AsTextContent(mcpC)
	require.True(t, ok)
	assert.Nil(t, text.Annotations)
}

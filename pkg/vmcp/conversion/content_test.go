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

func boolPtr(b bool) *bool { return &b }

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

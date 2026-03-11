// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authorizers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func boolPtr(b bool) *bool { return &b }

func TestToolAnnotationsContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		annotations *ToolAnnotations
		wantNil     bool
	}{
		{
			name: "round-trip with all fields",
			annotations: &ToolAnnotations{
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			},
		},
		{
			name: "round-trip with partial fields",
			annotations: &ToolAnnotations{
				ReadOnlyHint: boolPtr(true),
			},
		},
		{
			name:        "nil annotations stored returns nil",
			annotations: nil,
			wantNil:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := WithToolAnnotations(t.Context(), tc.annotations)
			got := ToolAnnotationsFromContext(ctx)

			if tc.wantNil {
				assert.Nil(t, got)
			} else {
				require.NotNil(t, got)
				assert.Equal(t, tc.annotations, got)
			}
		})
	}
}

func TestToolAnnotationsFromContext_Empty(t *testing.T) {
	t.Parallel()

	// Context that never had annotations stored should return nil.
	got := ToolAnnotationsFromContext(t.Context())
	assert.Nil(t, got)
}

func TestAnnotationsToMap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		annotations *ToolAnnotations
		want        map[string]interface{}
	}{
		{
			name:        "nil input returns nil",
			annotations: nil,
			want:        nil,
		},
		{
			name:        "empty struct returns empty map",
			annotations: &ToolAnnotations{},
			want:        map[string]interface{}{},
		},
		{
			name: "all fields set",
			annotations: &ToolAnnotations{
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			},
			want: map[string]interface{}{
				"readOnlyHint":    true,
				"destructiveHint": false,
				"idempotentHint":  true,
				"openWorldHint":   false,
			},
		},
		{
			name: "partial fields only includes non-nil",
			annotations: &ToolAnnotations{
				ReadOnlyHint:   boolPtr(true),
				IdempotentHint: boolPtr(false),
			},
			want: map[string]interface{}{
				"readOnlyHint":   true,
				"idempotentHint": false,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := AnnotationsToMap(tc.annotations)
			assert.Equal(t, tc.want, got)
		})
	}
}

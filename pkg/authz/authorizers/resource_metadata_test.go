// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authorizers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResourceMetadataContext(t *testing.T) {
	t.Parallel()

	t.Run("round trip", func(t *testing.T) {
		t.Parallel()

		readOnly := true
		want := ResourceMetadata{
			BackendID:   "github-mcp",
			Annotations: &ToolAnnotations{ReadOnlyHint: &readOnly},
		}
		ctx := WithResourceMetadata(t.Context(), want)

		got, ok := ResourceMetadataFromContext(ctx)
		assert.True(t, ok)
		assert.Equal(t, want, got)
	})

	t.Run("missing", func(t *testing.T) {
		t.Parallel()

		got, ok := ResourceMetadataFromContext(t.Context())
		assert.False(t, ok)
		assert.Empty(t, got)
	})

	t.Run("legacy annotation context remains readable", func(t *testing.T) {
		t.Parallel()

		annotations := &ToolAnnotations{ReadOnlyHint: boolPtr(true)}
		ctx := WithToolAnnotations(t.Context(), annotations)

		got, ok := ResourceMetadataFromContext(ctx)
		assert.True(t, ok)
		assert.Equal(t, annotations, got.Annotations)
		assert.Empty(t, got.BackendID)
	})

	t.Run("unified metadata remains readable through annotation helper", func(t *testing.T) {
		t.Parallel()

		annotations := &ToolAnnotations{ReadOnlyHint: boolPtr(true)}
		ctx := WithResourceMetadata(t.Context(), ResourceMetadata{Annotations: annotations})

		assert.Equal(t, annotations, ToolAnnotationsFromContext(ctx))
	})

	t.Run("unified annotations take precedence over legacy context", func(t *testing.T) {
		t.Parallel()

		unified := &ToolAnnotations{ReadOnlyHint: boolPtr(true)}
		legacy := &ToolAnnotations{ReadOnlyHint: boolPtr(false)}
		ctx := WithResourceMetadata(t.Context(), ResourceMetadata{Annotations: unified})
		ctx = WithToolAnnotations(ctx, legacy)

		metadata, ok := ResourceMetadataFromContext(ctx)
		assert.True(t, ok)
		assert.Equal(t, unified, metadata.Annotations)
		assert.Equal(t, unified, ToolAnnotationsFromContext(ctx))
	})
}

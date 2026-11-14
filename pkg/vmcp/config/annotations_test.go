// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToolAnnotationsOverride_ToAnnotations(t *testing.T) {
	t.Parallel()

	t.Run("nil override returns nil", func(t *testing.T) {
		t.Parallel()
		var o *ToolAnnotationsOverride
		assert.Nil(t, o.ToAnnotations())
	})

	t.Run("empty override returns non-nil with all-nil fields", func(t *testing.T) {
		t.Parallel()
		ann := (&ToolAnnotationsOverride{}).ToAnnotations()
		require.NotNil(t, ann)
		assert.Empty(t, ann.Title)
		assert.Nil(t, ann.ReadOnlyHint)
		assert.Nil(t, ann.DestructiveHint)
		assert.Nil(t, ann.IdempotentHint)
		assert.Nil(t, ann.OpenWorldHint)
	})

	t.Run("maps every field", func(t *testing.T) {
		t.Parallel()
		trueVal, falseVal := true, false
		o := &ToolAnnotationsOverride{
			Title:           ptrToStr("My Tool"),
			ReadOnlyHint:    &trueVal,
			DestructiveHint: &falseVal,
			IdempotentHint:  &trueVal,
			OpenWorldHint:   &falseVal,
		}
		ann := o.ToAnnotations()
		require.NotNil(t, ann)
		assert.Equal(t, "My Tool", ann.Title)
		require.NotNil(t, ann.ReadOnlyHint)
		assert.True(t, *ann.ReadOnlyHint)
		require.NotNil(t, ann.DestructiveHint)
		assert.False(t, *ann.DestructiveHint)
		require.NotNil(t, ann.IdempotentHint)
		assert.True(t, *ann.IdempotentHint)
		require.NotNil(t, ann.OpenWorldHint)
		assert.False(t, *ann.OpenWorldHint)
	})
}

func ptrToStr(s string) *string { return &s }

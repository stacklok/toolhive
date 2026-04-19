// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/stretchr/testify/assert"
)

func makeFormFields() []formField {
	const n = 3
	fields := make([]formField, n)
	for i := range fields {
		fields[i] = formField{input: textinput.New(), name: "field"}
	}
	return fields
}

func TestFormNextField(t *testing.T) {
	t.Parallel()

	t.Run("empty slice is safe", func(t *testing.T) {
		t.Parallel()
		idx := 0
		formNextField(nil, &idx) // should not panic
		assert.Equal(t, 0, idx)
	})

	t.Run("advances from -1 to 0", func(t *testing.T) {
		t.Parallel()
		fields := makeFormFields()
		idx := -1
		formNextField(fields, &idx)
		assert.Equal(t, 0, idx)
	})

	t.Run("wraps around from last to first", func(t *testing.T) {
		t.Parallel()
		fields := makeFormFields()
		idx := 2
		// Focus field 2 so Blur can be called
		fields[2].input.Focus()
		formNextField(fields, &idx)
		assert.Equal(t, 0, idx)
	})

	t.Run("advances sequentially", func(t *testing.T) {
		t.Parallel()
		fields := makeFormFields()
		idx := 0
		fields[0].input.Focus()
		formNextField(fields, &idx)
		assert.Equal(t, 1, idx)
	})
}

func TestFormPrevField(t *testing.T) {
	t.Parallel()

	t.Run("empty slice is safe", func(t *testing.T) {
		t.Parallel()
		idx := 0
		formPrevField(nil, &idx) // should not panic
		assert.Equal(t, 0, idx)
	})

	t.Run("wraps from 0 to last", func(t *testing.T) {
		t.Parallel()
		fields := makeFormFields()
		idx := 0
		fields[0].input.Focus()
		formPrevField(fields, &idx)
		assert.Equal(t, 2, idx)
	})

	t.Run("wraps from -1 to last", func(t *testing.T) {
		t.Parallel()
		fields := makeFormFields()
		idx := -1
		formPrevField(fields, &idx)
		assert.Equal(t, 2, idx)
	})

	t.Run("moves backwards sequentially", func(t *testing.T) {
		t.Parallel()
		fields := makeFormFields()
		idx := 2
		fields[2].input.Focus()
		formPrevField(fields, &idx)
		assert.Equal(t, 1, idx)
	})
}

func TestFormBlurAll(t *testing.T) {
	t.Parallel()

	t.Run("resets idx to -1", func(t *testing.T) {
		t.Parallel()
		fields := makeFormFields()
		idx := 1
		fields[1].input.Focus()
		formBlurAll(fields, &idx)
		assert.Equal(t, -1, idx)
	})

	t.Run("empty fields safe", func(t *testing.T) {
		t.Parallel()
		idx := 5
		formBlurAll(nil, &idx)
		assert.Equal(t, -1, idx)
	})
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	tea "charm.land/bubbletea/v2"
)

// formNextField advances focus to the next field in a formField slice (wraps around).
func formNextField(fields []formField, idx *int) {
	if len(fields) == 0 {
		return
	}
	if *idx >= 0 {
		fields[*idx].input.Blur()
	}
	*idx = (*idx + 1) % len(fields)
	fields[*idx].input.Focus()
}

// formPrevField moves focus to the previous field in a formField slice (wraps around).
func formPrevField(fields []formField, idx *int) {
	if len(fields) == 0 {
		return
	}
	if *idx >= 0 {
		fields[*idx].input.Blur()
	}
	if *idx <= 0 {
		*idx = len(fields) - 1
	} else {
		*idx--
	}
	fields[*idx].input.Focus()
}

// formBlurAll blurs every field and resets the focused index to -1.
func formBlurAll(fields []formField, idx *int) {
	for i := range fields {
		fields[i].input.Blur()
	}
	*idx = -1
}

// formForwardMessage forwards a message to the currently focused field.
func formForwardMessage(fields []formField, idx int, msg tea.Msg) tea.Cmd {
	if idx < 0 || idx >= len(fields) {
		return nil
	}
	var cmd tea.Cmd
	fields[idx].input, cmd = fields[idx].input.Update(msg)
	return cmd
}

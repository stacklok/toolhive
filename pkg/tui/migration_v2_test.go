// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"testing"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/cmd/thv/app/ui"
)

func TestViewConfiguresBubbleTeaV2(t *testing.T) {
	t.Parallel()

	view := (Model{}).View()
	assert.Equal(t, "Loading…\n", view.Content)
	assert.True(t, view.AltScreen)
	assert.Equal(t, ui.ColorBg, view.BackgroundColor)

	view = (Model{quitting: true}).View()
	assert.Empty(t, view.Content)
	assert.True(t, view.AltScreen)
	assert.Equal(t, ui.ColorBg, view.BackgroundColor)
}

func TestHandleMsgIgnoresKeyRelease(t *testing.T) {
	t.Parallel()

	model := Model{}
	_, earlyReturn := model.handleMsg(tea.KeyReleaseMsg(tea.Key{Code: 'q', Text: "q"}))

	assert.False(t, earlyReturn)
	assert.False(t, model.quitting)
}

func TestHandlePasteRoutesToActiveInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		model  Model
		assert func(*testing.T, Model)
	}{
		{
			name:  "server filter",
			model: Model{filterActive: true},
			assert: func(t *testing.T, model Model) {
				t.Helper()
				assert.Equal(t, "café", model.filterQuery)
			},
		},
		{
			name:  "log search",
			model: Model{logSearchActive: true, logLines: []string{"café"}},
			assert: func(t *testing.T, model Model) {
				t.Helper()
				assert.Equal(t, "café", model.logSearchQuery)
				assert.Equal(t, []int{0}, model.logSearchMatches)
			},
		},
		{
			name:  "registry filter",
			model: Model{registry: registryState{open: true}},
			assert: func(t *testing.T, model Model) {
				t.Helper()
				assert.Equal(t, "café", model.registry.filter)
			},
		},
		{
			name: "inspector field",
			model: func() Model {
				input := textinput.New()
				input.Focus()
				return Model{
					panel: panelInspector,
					insp:  inspectorState{fieldIdx: 0, fields: []formField{{input: input}}},
				}
			}(),
			assert: func(t *testing.T, model Model) {
				t.Helper()
				assert.Equal(t, "café", model.insp.fields[0].input.Value())
			},
		},
		{
			name: "run form field",
			model: func() Model {
				input := textinput.New()
				input.Focus()
				return Model{
					registry: registryState{open: true},
					runForm:  runFormState{open: true, idx: 0, fields: []formField{{input: input}}},
				}
			}(),
			assert: func(t *testing.T, model Model) {
				t.Helper()
				assert.Equal(t, "café", model.runForm.fields[0].input.Value())
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			model := test.model
			_, earlyReturn := model.handleMsg(tea.PasteMsg{Content: "café"})
			assert.True(t, earlyReturn)
			test.assert(t, model)
		})
	}
}

func TestResizeViewportUsesV2Accessors(t *testing.T) {
	t.Parallel()

	model := Model{}
	_, earlyReturn := model.handleMsg(tea.WindowSizeMsg{Width: 100, Height: 40})

	assert.True(t, earlyReturn)
	assert.Equal(t, 74, model.logView.Width())
	assert.Equal(t, 34, model.logView.Height())
	assert.Equal(t, 74, model.proxyLogView.Width())
	assert.Equal(t, 34, model.proxyLogView.Height())
	assert.Equal(t, 74, model.toolsView.Width())
	assert.Equal(t, 34, model.toolsView.Height())
	assert.Equal(t, 74, model.insp.logView.Width())
	assert.Equal(t, 6, model.insp.logView.Height())
}

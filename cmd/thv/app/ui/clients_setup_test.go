// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/groups"
)

func TestSetupModelUpdate_GroupToClientTransition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		allClients      []client.ClientAppStatus
		grps            []*groups.Group
		selectedGroups  map[int]struct{}
		wantStep        setupStep
		wantQuitting    bool
		wantAllFiltered bool
		wantClientCount int
	}{
		{
			name: "filters already-registered clients on transition",
			allClients: []client.ClientAppStatus{
				{ClientType: client.VSCode, Installed: true},
				{ClientType: client.Cursor, Installed: true},
				{ClientType: client.ClaudeCode, Installed: true},
			},
			grps: []*groups.Group{
				{Name: "group1", RegisteredClients: []string{"vscode"}},
			},
			selectedGroups:  map[int]struct{}{0: {}},
			wantStep:        stepClientSelection,
			wantQuitting:    false,
			wantAllFiltered: false,
			wantClientCount: 2, // cursor and claude-code remain
		},
		{
			name: "sets AllFiltered when all clients are already registered",
			allClients: []client.ClientAppStatus{
				{ClientType: client.VSCode, Installed: true},
				{ClientType: client.Cursor, Installed: true},
			},
			grps: []*groups.Group{
				{Name: "group1", RegisteredClients: []string{"vscode", "cursor"}},
			},
			selectedGroups:  map[int]struct{}{0: {}},
			wantStep:        stepClientSelection,
			wantQuitting:    true,
			wantAllFiltered: true,
			wantClientCount: 0,
		},
		{
			name: "does not transition without group selection",
			allClients: []client.ClientAppStatus{
				{ClientType: client.VSCode, Installed: true},
			},
			grps: []*groups.Group{
				{Name: "group1", RegisteredClients: []string{}},
			},
			selectedGroups:  map[int]struct{}{}, // none selected
			wantStep:        stepGroupSelection, // stays on group step
			wantQuitting:    false,
			wantAllFiltered: false,
			wantClientCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := &setupModel{
				UnfilteredClients: tt.allClients,
				Clients:           tt.allClients,
				Groups:            tt.grps,
				SelectedClients:   make(map[int]struct{}),
				SelectedGroups:    tt.selectedGroups,
				CurrentStep:       stepGroupSelection,
			}

			// Press enter to transition
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			result := updated.(*setupModel)

			assert.Equal(t, tt.wantStep, result.CurrentStep)
			assert.Equal(t, tt.wantQuitting, result.Quitting)
			assert.Equal(t, tt.wantAllFiltered, result.AllFiltered)
			assert.Len(t, result.Clients, tt.wantClientCount)
		})
	}
}

func TestSetupModelUpdate_ClientSelection(t *testing.T) {
	t.Parallel()

	clients := []client.ClientAppStatus{
		{ClientType: client.VSCode, Installed: true},
		{ClientType: client.Cursor, Installed: true},
	}

	m := &setupModel{
		UnfilteredClients: clients,
		Clients:           clients,
		Groups:            []*groups.Group{{Name: "g1"}},
		SelectedClients:   make(map[int]struct{}),
		SelectedGroups:    map[int]struct{}{0: {}},
		CurrentStep:       stepClientSelection,
	}

	// Toggle first client with space
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	result := updated.(*setupModel)
	_, selected := result.SelectedClients[0]
	assert.True(t, selected, "first client should be selected after space")

	// Toggle it off
	updated, _ = result.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	result = updated.(*setupModel)
	_, selected = result.SelectedClients[0]
	assert.False(t, selected, "first client should be deselected after second space")

	// Confirm with enter
	updated, cmd := result.Update(tea.KeyMsg{Type: tea.KeyEnter})
	result = updated.(*setupModel)
	assert.True(t, result.Confirmed)
	assert.True(t, result.Quitting)
	assert.False(t, result.AllFiltered)
	require.NotNil(t, cmd, "should return a quit command")
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/groups"
)

func TestSetupModelUpdate_GroupToClientTransition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                    string
		allClients              []client.ClientAppStatus
		grps                    []*groups.Group
		selectedGroups          map[int]struct{}
		wantStep                setupStep
		wantQuitting            bool
		wantClientCount         int
		wantSelectedIndices     []int
		wantInitiallyRegistered []string
	}{
		{
			name: "pre-selects already-registered clients on transition",
			allClients: []client.ClientAppStatus{
				{ClientType: client.VSCode, Installed: true},
				{ClientType: client.Cursor, Installed: true},
				{ClientType: client.ClaudeCode, Installed: true},
			},
			grps: []*groups.Group{
				{Name: "group1", RegisteredClients: []string{"vscode"}},
			},
			selectedGroups:          map[int]struct{}{0: {}},
			wantStep:                stepClientSelection,
			wantQuitting:            false,
			wantClientCount:         3,
			wantSelectedIndices:     []int{0},
			wantInitiallyRegistered: []string{"vscode"},
		},
		{
			name: "pre-selects all clients when all are registered",
			allClients: []client.ClientAppStatus{
				{ClientType: client.VSCode, Installed: true},
				{ClientType: client.Cursor, Installed: true},
			},
			grps: []*groups.Group{
				{Name: "group1", RegisteredClients: []string{"vscode", "cursor"}},
			},
			selectedGroups:          map[int]struct{}{0: {}},
			wantStep:                stepClientSelection,
			wantQuitting:            false,
			wantClientCount:         2,
			wantSelectedIndices:     []int{0, 1},
			wantInitiallyRegistered: []string{"vscode", "cursor"},
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
			updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			result := updated.(*setupModel)

			assert.Equal(t, tt.wantStep, result.CurrentStep)
			assert.Equal(t, tt.wantQuitting, result.Quitting)
			assert.Len(t, result.Clients, tt.wantClientCount)

			for _, i := range tt.wantSelectedIndices {
				_, selected := result.SelectedClients[i]
				assert.True(t, selected, "client at index %d should be pre-selected", i)
			}
			assert.Equal(t, tt.wantInitiallyRegistered, result.InitiallyRegistered)
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
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	result := updated.(*setupModel)
	_, selected := result.SelectedClients[0]
	assert.True(t, selected, "first client should be selected after space")

	// Toggle it off
	updated, _ = result.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	result = updated.(*setupModel)
	_, selected = result.SelectedClients[0]
	assert.False(t, selected, "first client should be deselected after second space")

	// Confirm with enter
	updated, cmd := result.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	result = updated.(*setupModel)
	assert.True(t, result.Confirmed)
	assert.True(t, result.Quitting)
	require.NotNil(t, cmd, "should return a quit command")
}

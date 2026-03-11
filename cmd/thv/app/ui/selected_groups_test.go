// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/groups"
)

func TestSelectedGroups_BoundsCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		grps           []*groups.Group
		selectedGroups map[int]struct{}
		wantNames      []string
	}{
		{
			name: "all indices out of bounds returns empty",
			grps: []*groups.Group{
				{Name: "only-group"},
			},
			selectedGroups: map[int]struct{}{99: {}, -1: {}},
			wantNames:      nil,
		},
		{
			name: "mix of valid and out-of-bounds indices",
			grps: []*groups.Group{
				{Name: "alpha"},
				{Name: "beta"},
			},
			selectedGroups: map[int]struct{}{0: {}, 50: {}, 1: {}},
			wantNames:      []string{"alpha", "beta"},
		},
		{
			name:           "empty selection returns empty",
			grps:           []*groups.Group{{Name: "g1"}},
			selectedGroups: map[int]struct{}{},
			wantNames:      nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := &setupModel{
				Groups:         tt.grps,
				SelectedGroups: tt.selectedGroups,
			}

			got := m.selectedGroups()

			var gotNames []string
			for _, g := range got {
				gotNames = append(gotNames, g.Name)
			}
			assert.ElementsMatch(t, tt.wantNames, gotNames)
		})
	}
}

func TestFilterClientsBySelectedGroups_OutOfBoundsIndices(t *testing.T) {
	t.Parallel()

	allClients := []client.ClientAppStatus{
		{ClientType: client.VSCode, Installed: true},
		{ClientType: client.Cursor, Installed: true},
	}

	m := &setupModel{
		UnfilteredClients: allClients,
		Clients:           allClients,
		Groups: []*groups.Group{
			{Name: "group1", RegisteredClients: []string{"vscode"}},
		},
		SelectedClients: make(map[int]struct{}),
		SelectedGroups:  map[int]struct{}{0: {}, 99: {}}, // 99 is out of bounds
		CurrentStep:     stepGroupSelection,
	}

	// Press enter to trigger transition which calls filterClientsBySelectedGroups
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	result := updated.(*setupModel)

	assert.Equal(t, stepClientSelection, result.CurrentStep)
	assert.False(t, result.Quitting)
	assert.False(t, result.AllFiltered)
	// Only cursor remains; vscode was filtered by group1, OOB index 99 safely ignored
	assert.Len(t, result.Clients, 1)
	assert.Equal(t, client.Cursor, result.Clients[0].ClientType)
}

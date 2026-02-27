// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package ui provides terminal UI helpers for the ToolHive CLI.
package ui

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/groups"
)

// ErrAllClientsRegistered is returned when all available clients are already
// registered for the selected groups.
var ErrAllClientsRegistered = errors.New("all installed clients are already registered for the selected groups")

var (
	docStyle          = lipgloss.NewStyle().Margin(1, 2)
	selectedItemStyle = lipgloss.NewStyle().PaddingLeft(2).Foreground(lipgloss.Color("170"))
	itemStyle         = lipgloss.NewStyle().PaddingLeft(2)
)

type setupStep int

const (
	stepGroupSelection setupStep = iota
	stepClientSelection
)

type setupModel struct {
	// UnfilteredClients holds all installed clients before group-based filtering.
	UnfilteredClients []client.ClientAppStatus
	// Clients holds the clients displayed in the selection list. After filtering,
	// SelectedClients indices refer to positions in this slice (not UnfilteredClients).
	Clients         []client.ClientAppStatus
	Groups          []*groups.Group
	Cursor          int
	SelectedClients map[int]struct{}
	SelectedGroups  map[int]struct{}
	Quitting        bool
	Confirmed       bool
	AllFiltered     bool
	CurrentStep     setupStep
}

func (*setupModel) Init() tea.Cmd { return nil }

func (m *setupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "ctrl+c", "q":
			m.Confirmed = false
			m.Quitting = true
			return m, tea.Quit
		case "up", "k":
			if m.Cursor > 0 {
				m.Cursor--
			}
		case "down", "j":
			maxItems := m.getMaxCursorPosition()
			if m.Cursor < maxItems-1 {
				m.Cursor++
			}
		case "enter":
			if m.CurrentStep == stepGroupSelection {
				// Require at least one group to be selected before proceeding
				if len(m.SelectedGroups) == 0 {
					return m, nil // Stay on group selection step
				}
				// Filter clients and move to client selection step
				m.filterClientsBySelectedGroups()
				m.CurrentStep = stepClientSelection
				m.Cursor = 0
				if len(m.Clients) == 0 {
					m.AllFiltered = true
					m.Quitting = true
					return m, tea.Quit
				}
				return m, nil
			}
			// Final confirmation
			m.Confirmed = true
			m.Quitting = true
			return m, tea.Quit
		case " ":
			if m.CurrentStep == stepGroupSelection {
				// Toggle group selection
				if _, ok := m.SelectedGroups[m.Cursor]; ok {
					delete(m.SelectedGroups, m.Cursor)
				} else {
					m.SelectedGroups[m.Cursor] = struct{}{}
				}
			} else {
				// Toggle client selection
				if _, ok := m.SelectedClients[m.Cursor]; ok {
					delete(m.SelectedClients, m.Cursor)
				} else {
					m.SelectedClients[m.Cursor] = struct{}{}
				}
			}
		}
	}
	return m, nil
}

func (m *setupModel) getMaxCursorPosition() int {
	if m.CurrentStep == stepGroupSelection {
		return len(m.Groups)
	}
	return len(m.Clients)
}

func (m *setupModel) View() string {
	if m.Quitting {
		return ""
	}
	var b strings.Builder

	if m.CurrentStep == stepGroupSelection {
		b.WriteString("Select groups to register clients with (at least one group needs to be selected):\n\n")
		for i, group := range m.Groups {
			b.WriteString(renderGroupRow(m, i, group))
		}
		b.WriteString("\nUse ↑/↓ (or j/k) to move, 'space' to select, 'enter' to continue, 'q' to quit.\n")
	} else {
		if len(m.SelectedGroups) > 0 {
			fmt.Fprintf(&b, "Selected groups: %s\n\n", strings.Join(m.sortedSelectedGroupNames(), ", "))
		}
		b.WriteString("Select clients to register:\n\n")
		for i, cli := range m.Clients {
			b.WriteString(renderClientRow(m, i, cli))
		}
		b.WriteString("\nUse ↑/↓ (or j/k) to move, 'space' to select, 'enter' to confirm, 'q' to quit.\n")
	}

	return docStyle.Render(b.String())
}

// filterClientsBySelectedGroups replaces Clients with a filtered subset
// that excludes clients already registered in all selected groups, and
// resets SelectedClients since the indices would no longer be valid.
func (m *setupModel) filterClientsBySelectedGroups() {
	if len(m.SelectedGroups) == 0 {
		return
	}

	var selectedGroups []*groups.Group
	for i := range m.SelectedGroups {
		selectedGroups = append(selectedGroups, m.Groups[i])
	}

	m.Clients = client.FilterClientsAlreadyRegistered(m.UnfilteredClients, selectedGroups)
	m.SelectedClients = make(map[int]struct{})
}

// sortedSelectedGroupNames returns selected group names in sorted order.
func (m *setupModel) sortedSelectedGroupNames() []string {
	names := make([]string, 0, len(m.SelectedGroups))
	for i := range m.SelectedGroups {
		names = append(names, m.Groups[i].Name)
	}
	sort.Strings(names)
	return names
}

func renderGroupRow(m *setupModel, i int, group *groups.Group) string {
	cursor := "  "
	if m.Cursor == i {
		cursor = "> "
	}
	checked := " "
	if _, ok := m.SelectedGroups[i]; ok {
		checked = "x"
	}
	row := fmt.Sprintf("%s[%s] %s", cursor, checked, group.Name)
	if m.Cursor == i {
		return selectedItemStyle.Render(row) + "\n"
	}
	return itemStyle.Render(row) + "\n"
}

func renderClientRow(m *setupModel, i int, cli client.ClientAppStatus) string {
	cursor := "  "
	if m.Cursor == i {
		cursor = "> "
	}
	checked := " "
	if _, ok := m.SelectedClients[i]; ok {
		checked = "x"
	}
	row := fmt.Sprintf("%s[%s] %s", cursor, checked, cli.ClientType)
	if m.Cursor == i {
		return selectedItemStyle.Render(row) + "\n"
	}
	return itemStyle.Render(row) + "\n"
}

// RunClientSetup runs the interactive client setup and returns the selected clients, groups, and whether the user confirmed.
func RunClientSetup(
	clients []client.ClientAppStatus,
	availableGroups []*groups.Group,
) ([]client.ClientAppStatus, []string, bool, error) {

	var selectedGroupsMap = make(map[int]struct{})
	var currentStep = stepClientSelection

	// Skip group selection if 0 or 1 groups exist
	if len(availableGroups) == 0 {
		// No groups exist, keep map empty
	} else if len(availableGroups) == 1 {
		// Only one group exists, auto-select it
		selectedGroupsMap[0] = struct{}{}
	} else {
		// Multiple groups exist, show group selection step
		currentStep = stepGroupSelection
	}

	// When skipping group selection, filter out already-registered clients
	displayClients := clients
	if currentStep == stepClientSelection && len(selectedGroupsMap) > 0 {
		var selectedGroups []*groups.Group
		for i := range selectedGroupsMap {
			selectedGroups = append(selectedGroups, availableGroups[i])
		}
		displayClients = client.FilterClientsAlreadyRegistered(clients, selectedGroups)
		if len(displayClients) == 0 {
			groupNames := make([]string, 0, len(selectedGroupsMap))
			for i := range selectedGroupsMap {
				groupNames = append(groupNames, availableGroups[i].Name)
			}
			sort.Strings(groupNames)
			return nil, groupNames, false, ErrAllClientsRegistered
		}
	}

	model := &setupModel{
		UnfilteredClients: clients,
		Clients:           displayClients,
		Groups:            availableGroups,
		SelectedClients:   make(map[int]struct{}),
		SelectedGroups:    selectedGroupsMap,
		CurrentStep:       currentStep,
	}

	p := tea.NewProgram(model)
	finalModel, err := p.Run()
	if err != nil {
		return nil, nil, false, err
	}

	m := finalModel.(*setupModel)

	if m.AllFiltered {
		groupNames := m.sortedSelectedGroupNames()
		return nil, groupNames, false, ErrAllClientsRegistered
	}

	var selectedClients []client.ClientAppStatus
	for i := range m.SelectedClients {
		selectedClients = append(selectedClients, m.Clients[i])
	}

	// Convert selected group indices back to group names
	selectedGroupNames := m.sortedSelectedGroupNames()

	return selectedClients, selectedGroupNames, m.Confirmed, nil
}

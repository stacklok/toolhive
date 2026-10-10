// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package ui provides terminal UI helpers for the ToolHive CLI.
package ui

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/groups"
)

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
	Clients []client.ClientAppStatus
	// InitiallyRegistered holds the client names that were already registered
	// for the selected groups when the client selection step began. Deselecting
	// one of these un-registers it on confirmation.
	InitiallyRegistered []string
	Groups              []*groups.Group
	Cursor              int
	SelectedClients     map[int]struct{}
	SelectedGroups      map[int]struct{}
	Quitting            bool
	Confirmed           bool
	CurrentStep         setupStep
}

func (*setupModel) Init() tea.Cmd { return nil }

func (m *setupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
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
				// List all installed clients with already-registered ones
				// pre-selected, then move to client selection step.
				m.prepareClientSelection()
				m.CurrentStep = stepClientSelection
				m.Cursor = 0
				return m, nil
			}
			// Final confirmation
			m.Confirmed = true
			m.Quitting = true
			return m, tea.Quit
		case "space":
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

func (m *setupModel) View() tea.View {
	if m.Quitting {
		return tea.NewView("")
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
		b.WriteString("Select clients to register (uncheck a pre-selected client to un-register it):\n\n")
		for i, cli := range m.Clients {
			b.WriteString(renderClientRow(m, i, cli))
		}
		b.WriteString("\nUse ↑/↓ (or j/k) to move, 'space' to select, 'enter' to confirm, 'q' to quit.\n")
	}

	return tea.NewView(docStyle.Render(b.String()))
}

// selectedGroups returns the groups corresponding to SelectedGroups indices,
// skipping any index that is out of bounds.
func (m *setupModel) selectedGroups() []*groups.Group {
	selected := make([]*groups.Group, 0, len(m.SelectedGroups))
	for i := range m.SelectedGroups {
		if i < 0 || i >= len(m.Groups) {
			continue
		}
		selected = append(selected, m.Groups[i])
	}
	return selected
}

// prepareClientSelection lists all installed clients and pre-selects those
// already registered in every selected group. Deselecting a pre-selected
// client un-registers it on confirmation; selecting an unregistered one
// registers it.
func (m *setupModel) prepareClientSelection() {
	if len(m.SelectedGroups) == 0 {
		return
	}

	sg := m.selectedGroups()
	m.Clients = m.UnfilteredClients
	m.SelectedClients = make(map[int]struct{})
	m.InitiallyRegistered = nil
	for i, cli := range m.Clients {
		if client.IsClientRegisteredInAllGroups(string(cli.ClientType), sg) {
			m.SelectedClients[i] = struct{}{}
			m.InitiallyRegistered = append(m.InitiallyRegistered, string(cli.ClientType))
		}
	}
}

// sortedSelectedGroupNames returns selected group names in sorted order.
func (m *setupModel) sortedSelectedGroupNames() []string {
	sg := m.selectedGroups()
	names := make([]string, 0, len(sg))
	for _, g := range sg {
		names = append(names, g.Name)
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

// RunClientSetup runs the interactive client setup and returns the selected
// clients, the client names that were already registered for the selected
// groups when the client step began, the selected group names, whether the
// user confirmed, and any error. Pre-selected clients that end up deselected
// should be un-registered by the caller.
func RunClientSetup(
	clients []client.ClientAppStatus,
	availableGroups []*groups.Group,
) ([]client.ClientAppStatus, []string, []string, bool, error) {

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

	model := &setupModel{
		UnfilteredClients: clients,
		Clients:           clients,
		Groups:            availableGroups,
		SelectedClients:   make(map[int]struct{}),
		SelectedGroups:    selectedGroupsMap,
		CurrentStep:       currentStep,
	}

	// When skipping group selection, pre-select clients that are already
	// registered for the auto-selected group instead of hiding them.
	if currentStep == stepClientSelection && len(selectedGroupsMap) > 0 {
		model.prepareClientSelection()
	}

	p := tea.NewProgram(model)
	finalModel, err := p.Run()
	if err != nil {
		return nil, nil, nil, false, err
	}

	m := finalModel.(*setupModel)

	var selectedClients []client.ClientAppStatus
	for i := range m.SelectedClients {
		if i < 0 || i >= len(m.Clients) {
			continue
		}
		selectedClients = append(selectedClients, m.Clients[i])
	}

	// Convert selected group indices back to group names
	selectedGroupNames := m.sortedSelectedGroupNames()

	return selectedClients, m.InitiallyRegistered, selectedGroupNames, m.Confirmed, nil
}

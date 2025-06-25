// Package ui provides terminal UI helpers for the ToolHive CLI.
package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/stacklok/toolhive/pkg/client"
)

var (
	docStyle          = lipgloss.NewStyle().Margin(1, 2)
	selectedItemStyle = lipgloss.NewStyle().PaddingLeft(2).Foreground(lipgloss.Color("170"))
	itemStyle         = lipgloss.NewStyle().PaddingLeft(2)
)

type setupModel struct {
	Clients   []client.MCPClientStatus
	Cursor    int
	Selected  map[int]struct{}
	Quitting  bool
	Confirmed bool
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
			if m.Cursor < len(m.Clients)-1 {
				m.Cursor++
			}
		case "enter":
			m.Confirmed = true
			m.Quitting = true
			return m, tea.Quit
		case " ":
			if _, ok := m.Selected[m.Cursor]; ok {
				delete(m.Selected, m.Cursor)
			} else {
				m.Selected[m.Cursor] = struct{}{}
			}
		}
	}
	return m, nil
}

func (m *setupModel) View() string {
	if m.Quitting {
		return ""
	}
	var b strings.Builder
	b.WriteString("Select clients to register:\n\n")
	for i, cli := range m.Clients {
		b.WriteString(renderClientRow(m, i, cli))
	}
	b.WriteString("\nUse ↑/↓ (or j/k) to move, 'space' to select, 'enter' to confirm, 'q' to quit.\n")
	return docStyle.Render(b.String())
}

func renderClientRow(m *setupModel, i int, cli client.MCPClientStatus) string {
	cursor := "  "
	if m.Cursor == i {
		cursor = "> "
	}
	checked := " "
	if _, ok := m.Selected[i]; ok {
		checked = "x"
	}
	row := fmt.Sprintf("%s[%s] %s", cursor, checked, cli.ClientType)
	if m.Cursor == i {
		return selectedItemStyle.Render(row) + "\n"
	}
	return itemStyle.Render(row) + "\n"
}

// RunClientSetup runs the interactive client setup and returns the selected clients and whether the user confirmed.
func RunClientSetup(clients []client.MCPClientStatus) ([]client.MCPClientStatus, bool, error) {
	model := &setupModel{
		Clients:  clients,
		Selected: make(map[int]struct{}),
	}
	p := tea.NewProgram(model)
	finalModel, err := p.Run()
	if err != nil {
		return nil, false, err
	}
	m := finalModel.(*setupModel)
	var selected []client.MCPClientStatus
	for i := range m.Selected {
		selected = append(selected, clients[i])
	}
	return selected, m.Confirmed, nil
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/stacklok/toolhive/cmd/thv/app/ui"
)

// renderStatusBar renders the bottom 2-line help bar (separator + key hints).
func (m Model) renderStatusBar() string {
	const statusBg = lipgloss.Color("#1e2030")
	const badgeBg = lipgloss.Color("#2a2f45")

	// badge renders a key name with a contrasting background box.
	// We use manual spaces instead of Padding to keep measurement predictable.
	badge := func(k string) string {
		return lipgloss.NewStyle().
			Background(badgeBg).
			Foreground(ui.ColorText).
			Render(" " + k + " ")
	}
	hint := func(k, desc string) string {
		b := badge(k)
		d := lipgloss.NewStyle().Foreground(ui.ColorDim2).Background(statusBg).Render(" " + desc)
		return b + d
	}
	spacer := "   " // plain spaces between hints (carry the statusBg from outer render)

	// Separator line — Width(m.width) ensures background fills the entire row.
	sepLine := lipgloss.NewStyle().
		Width(m.width).
		Background(statusBg).
		Foreground(ui.ColorDim2).
		Render(strings.Repeat("─", m.width))

	// Confirmation prompt takes over the content line.
	if m.confirmDelete {
		if sel := m.selected(); sel != nil {
			warn := lipgloss.NewStyle().Foreground(ui.ColorRed).Bold(true).Render("Delete " + sel.Name + "?")
			info := lipgloss.NewStyle().Foreground(ui.ColorDim2).Render("  Press d to confirm, any other key to cancel")
			contentLine := lipgloss.NewStyle().Width(m.width).Background(statusBg).Render("  " + warn + info)
			return sepLine + "\n" + contentLine
		}
	}

	// When log search prompt is open, show dedicated search hints.
	if m.logSearchActive || m.proxyLogSearchActive {
		parts := []string{
			hint("↵", "confirm"),
			hint("n", "next"),
			hint("N", "prev"),
			hint("esc", "clear"),
		}
		hints := "  " + strings.Join(parts, spacer)
		gap := m.width - ui.VisibleLen(hints)
		if gap < 1 {
			gap = 1
		}
		contentLine := lipgloss.NewStyle().Width(m.width).Background(statusBg).
			Render(hints + strings.Repeat(" ", gap))
		return sepLine + "\n" + contentLine
	}

	// Context-sensitive hints based on active panel.
	var parts []string
	switch m.panel {
	case panelInspector:
		upDownDesc := func() string {
			if m.insp.jsonRoot != nil {
				return "tree"
			}
			if m.insp.result != "" {
				return "scroll"
			}
			return "tool"
		}()
		parts = []string{
			hint("↑↓", upDownDesc),
			hint("tab", "panel"),
			hint("↵", "field/call"),
			hint("y", "copy curl"),
			hint("esc", "back"),
			hint("q", "quit"),
		}
		if m.insp.filterActive {
			parts = []string{
				hint("↑↓", "navigate"),
				hint("↵", "confirm"),
				hint("esc", "clear filter"),
			}
		} else if m.insp.jsonRoot != nil {
			parts = []string{
				hint("↑↓", "tree"),
				hint("space", "fold"),
				hint("c", "copy JSON"),
				hint("y", "copy curl"),
				hint("/", "filter tools"),
				hint("tab", "panel"),
				hint("↵", "field/call"),
				hint("esc", "back"),
				hint("q", "quit"),
			}
		} else {
			parts = append(parts, hint("i", "tool info"))
			parts = append(parts, hint("/", "filter tools"))
		}
	default:
		parts = []string{
			hint("↑↓", "navigate"),
			hint("tab", "panel"),
			hint("s", "stop"),
			hint("r", "restart"),
			hint("d", "delete"),
			hint("u", "copy URL"),
			hint("R", "registry"),
			hint("/", "filter"),
			hint("?", "help"),
			hint("q", "quit"),
		}
		// When log search is active (prompt closed but highlights on), add search navigation hints.
		if m.panel == panelLogs && m.logSearchQuery != "" {
			parts = []string{
				hint("n", "next match"),
				hint("N", "prev match"),
				hint("esc", "clear search"),
				hint("/", "new search"),
				hint("q", "quit"),
			}
		}
		if m.panel == panelProxyLogs && m.proxyLogSearchQuery != "" {
			parts = []string{
				hint("n", "next match"),
				hint("N", "prev match"),
				hint("esc", "clear search"),
				hint("/", "new search"),
				hint("q", "quit"),
			}
		}
	}

	hints := "  " + strings.Join(parts, spacer)

	// Notification — right-aligned, shown only when non-empty.
	notif := ""
	if m.notifMsg != "" {
		notifColor := ui.ColorGreen
		if !m.notifOK {
			notifColor = ui.ColorRed
		}
		notif = lipgloss.NewStyle().
			Foreground(notifColor).
			Background(statusBg).
			Render(m.notifMsg + "  ")
	}

	// Pad hints to fill the gap so notif lands at the far right.
	hintsLen := ui.VisibleLen(hints)
	notifLen := ui.VisibleLen(notif)
	gap := m.width - hintsLen - notifLen
	if gap < 1 {
		gap = 1
	}
	content := hints + strings.Repeat(" ", gap) + notif
	contentLine := lipgloss.NewStyle().Width(m.width).Background(statusBg).Render(content)
	return sepLine + "\n" + contentLine
}

// renderHelpOverlay renders the help modal over the base layout.
func (m Model) renderHelpOverlay(_ string) string {
	helpContent := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ui.ColorPurple).
		Padding(1, 3).
		Width(60).
		Render(helpText())

	return lipgloss.Place(m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		helpContent,
		lipgloss.WithWhitespaceChars(" "),
		lipgloss.WithWhitespaceForeground(ui.ColorDim),
	) + "\n(press any key to close)"
}

func helpText() string {
	bind := func(k, desc string) string {
		key := lipgloss.NewStyle().Foreground(ui.ColorCyan).Render(fmt.Sprintf("%-16s", k))
		d := lipgloss.NewStyle().Foreground(ui.ColorText).Render(desc)
		return key + d
	}
	heading := lipgloss.NewStyle().Foreground(ui.ColorPurple).Bold(true).Render

	lines := []string{
		heading("Navigation"),
		bind("↑/k  ↓/j", "select server"),
		bind("tab", "switch panel (Logs/Info/Tools/Proxy/Inspector)"),
		bind("/", "filter server list"),
		"",
		heading("Actions"),
		bind("s", "stop selected server"),
		bind("r", "restart selected server"),
		bind("d d", "delete (press d twice to confirm)"),
		bind("u", "copy server URL to clipboard"),
		bind("R", "open registry browser"),
		"",
		heading("Logs panel"),
		bind("f", "toggle follow mode"),
		bind("/", "open inline search"),
		bind("n / N", "next / previous search match"),
		bind("esc", "clear search highlights"),
		bind("← →", "scroll horizontally"),
		"",
		heading("Proxy Logs panel"),
		bind("/", "open inline search"),
		bind("n / N", "next / previous search match"),
		bind("esc", "clear search highlights"),
		bind("← →", "scroll horizontally"),
		"",
		heading("Inspector panel"),
		bind("↑/↓", "navigate tools / JSON tree"),
		bind("/", "filter tools by name"),
		bind("↵", "call selected tool"),
		bind("space", "collapse / expand JSON node"),
		bind("c", "copy response to clipboard"),
		bind("y", "copy curl command to clipboard"),
		"",
		heading("Other"),
		bind("?", "toggle this help"),
		bind("q / ctrl+c", "quit"),
	}
	return strings.Join(lines, "\n")
}

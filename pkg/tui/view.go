// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/stacklok/toolhive/cmd/thv/app/ui"
)

// View renders the full TUI to a string.
// We build exactly m.height lines by slotting body lines into a fixed array
// and placing the 2-line statusbar at the last two rows. This avoids any
// off-by-one ambiguity from lipgloss Height padding or trailing-newline
// counting differences between lipgloss and BubbleTea's "\n"-split renderer.
// oscSetBg is the OSC 11 sequence that sets the terminal's own default
// background colour. Every cell that has no explicit background (log text,
// tool descriptions, text-input interiors, etc.) will inherit this colour,
// giving the whole TUI a uniform #1e2030 background without having to style
// every individual element. oscResetBg restores the original colour on exit.
const oscSetBg = "\x1b]11;#1e2030\x07"
const oscResetBg = "\x1b]111;\x07"

func (m Model) View() string {
	if m.quitting {
		// Reset terminal background before handing control back to the shell.
		return oscResetBg
	}
	if m.width == 0 {
		return "Loading…\n"
	}

	sidebar := m.renderSidebar()
	main := m.renderMain()

	// Divider: exactly m.height-2 rows (no trailing \n) to match sidebar/main.
	var dividerStr string
	if m.height > 3 {
		dividerStr = strings.Repeat("│\n", m.height-3) + "│"
	} else {
		dividerStr = "│"
	}
	divider := lipgloss.NewStyle().
		Foreground(ui.ColorDim).
		Render(dividerStr)

	body := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, divider, main)
	statusbar := m.renderStatusBar()

	// Split the statusbar into its two component lines.
	sbParts := strings.SplitN(statusbar, "\n", 2)
	if len(sbParts) < 2 {
		sbParts = append(sbParts, "")
	}

	// bgRow fills any unfilled slots with the main background so no row ever
	// shows the raw terminal background between the content and the statusbar.
	bgRow := lipgloss.NewStyle().Width(m.width).Background(ui.ColorBg).Render("")

	// Build an explicit m.height-line slice so BubbleTea always fills the
	// entire terminal window, regardless of any lipgloss rounding.
	out := make([]string, m.height)
	// Pre-fill every body slot with the background colour.
	for i := range m.height - 2 {
		out[i] = bgRow
	}
	bodyLines := strings.Split(body, "\n")
	// Drop a trailing empty element that lipgloss may append.
	if len(bodyLines) > 0 && bodyLines[len(bodyLines)-1] == "" {
		bodyLines = bodyLines[:len(bodyLines)-1]
	}
	for i, l := range bodyLines {
		if i >= m.height-2 {
			break
		}
		out[i] = l
	}
	out[m.height-2] = sbParts[0]
	out[m.height-1] = sbParts[1]

	// Prepend the OSC 11 sequence so the terminal's default background is
	// #1e2030 for this frame. Every area with no explicit background colour
	// (log lines, tool text, text-input interiors, …) will therefore show
	// the same dark tone as the statusbar with no further changes needed.
	full := oscSetBg + strings.Join(out, "\n")

	if m.showHelp {
		return m.renderHelpOverlay(full)
	}
	if m.registry.open {
		return m.renderRegistryOverlay(full)
	}
	return full
}

// renderSidebar renders the left server list.
func (m Model) renderSidebar() string {
	sw := sidebarW(m.width)

	titleStyle := lipgloss.NewStyle().
		Foreground(ui.ColorPurple).
		Bold(true).
		Width(sw)

	list := m.filteredWorkloads()
	running, stopped := countStatuses(m.workloads)
	summary := lipgloss.NewStyle().Foreground(ui.ColorDim).
		Render(fmt.Sprintf("%dr · %ds", running, stopped))

	header := titleStyle.Render("SERVERS") + "  " + summary + "\n"

	var sb strings.Builder
	sb.WriteString(header)

	for i, w := range list {
		dot := ui.RenderStatusDot(w.Status)
		name := w.Name
		port := fmt.Sprintf(":%d", w.Port)

		nameStyle := lipgloss.NewStyle().Foreground(ui.ColorText)
		portStyle := lipgloss.NewStyle().Foreground(ui.ColorCyan)

		if i == m.selectedIdx {
			nameStyle = nameStyle.Background(lipgloss.Color("#2a2e45")).Bold(true)
			portStyle = portStyle.Background(lipgloss.Color("#2a2e45"))
		}

		line1 := fmt.Sprintf("%s %s%s",
			dot,
			nameStyle.Render(truncateSidebar(name, sw-8)),
			portStyle.Render(port),
		)
		sb.WriteString("  " + line1 + "\n")

		// Show group on a second line if present
		if w.Group != "" {
			groupLine := lipgloss.NewStyle().
				Foreground(ui.ColorDim2).
				Render("    " + w.Group)
			sb.WriteString(groupLine + "\n")
		}
	}

	// Filter prompt
	if m.filterActive {
		prompt := lipgloss.NewStyle().Foreground(ui.ColorYellow).Render("/") +
			lipgloss.NewStyle().Foreground(ui.ColorText).Render(m.filterQuery) +
			lipgloss.NewStyle().Foreground(ui.ColorDim).Render("█")
		sb.WriteString("\n" + prompt + "\n")
	}

	sidebarStyle := lipgloss.NewStyle().
		Width(sw).
		Height(m.height-2).MaxHeight(m.height-2). // body = m.height-2, statusbar = 2, total = m.height
		PaddingRight(1)

	return sidebarStyle.Render(sb.String())
}

// renderMain renders the main content panel (logs or info).
func (m Model) renderMain() string {
	sw := sidebarW(m.width)
	mainW := m.width - sw - 1
	if mainW < 10 {
		mainW = 10
	}

	sel := m.selected()

	// Title bar
	titleStyle := lipgloss.NewStyle().Foreground(ui.ColorBlue).Bold(true)
	var titleText string
	if sel != nil {
		titleText = titleStyle.Render("toolhive") +
			lipgloss.NewStyle().Foreground(ui.ColorDim).Render(" / ") +
			lipgloss.NewStyle().Foreground(ui.ColorText).Bold(true).Render(sel.Name)
	} else {
		titleText = titleStyle.Render("toolhive")
	}

	// Tab bar
	logsTab := m.renderTab("Logs", panelLogs)
	infoTab := m.renderTab("Info", panelInfo)
	toolsTab := m.renderTab("Tools", panelTools)
	proxyTab := m.renderTab("Proxy Logs", panelProxyLogs)
	inspTab := m.renderTab("Inspector", panelInspector)
	tabBar := logsTab + "  " + infoTab + "  " + toolsTab + "  " + proxyTab + "  " + inspTab

	// Separator
	sep := lipgloss.NewStyle().Foreground(ui.ColorDim).
		Render(strings.Repeat("─", mainW))

	// Content
	var content string
	switch m.panel {
	case panelLogs:
		content = m.logView.View()
	case panelInfo:
		if sel != nil {
			content = renderInfo(sel, m.runConfig, mainW)
		} else {
			content = lipgloss.NewStyle().Foreground(ui.ColorDim).Render("No server selected")
		}
	case panelTools:
		content = m.renderTools(mainW)
	case panelProxyLogs:
		content = m.renderProxyLogs(mainW)
	case panelInspector:
		content = m.renderInspector(mainW)
	}

	// Log toolbar (only on logs/proxy logs panels)
	toolbar := ""
	dimToolbar := lipgloss.NewStyle().Foreground(ui.ColorDim)
	if m.panel == panelLogs {
		if m.logSearchActive || m.logSearchQuery != "" {
			// Search toolbar: show prompt or active query with match count.
			queryPart := func() string {
				if m.logSearchActive {
					return lipgloss.NewStyle().Foreground(ui.ColorYellow).Render("/") +
						lipgloss.NewStyle().Foreground(ui.ColorText).Render(m.logSearchQuery) +
						lipgloss.NewStyle().Foreground(ui.ColorDim).Render("█")
				}
				return lipgloss.NewStyle().Foreground(ui.ColorDim2).Render("/") +
					lipgloss.NewStyle().Foreground(ui.ColorText).Render(m.logSearchQuery)
			}()
			matchPart := func() string {
				if len(m.logSearchMatches) == 0 {
					return "  " + lipgloss.NewStyle().Foreground(ui.ColorRed).Render("no matches")
				}
				return "  " + lipgloss.NewStyle().Foreground(ui.ColorGreen).Render(
					fmt.Sprintf("match %d/%d", m.logSearchIdx+1, len(m.logSearchMatches)),
				) + dimToolbar.Render("  (n=next  N=prev  esc=clear)")
			}()
			toolbar = "  " + queryPart + matchPart
		} else {
			followStyle := lipgloss.NewStyle().Foreground(ui.ColorDim2)
			if m.logFollow {
				followStyle = followStyle.Foreground(ui.ColorGreen)
			}
			hScrollHint := dimToolbar.Render("  ←→ scroll")
			if m.logHScrollOff > 0 {
				hScrollHint = dimToolbar.Render(fmt.Sprintf("  ←→ +%d", m.logHScrollOff))
			}
			toolbar = "  " + followStyle.Render("follow") +
				dimToolbar.Render("  (f to toggle)") + hScrollHint
		}
	}
	if m.panel == panelProxyLogs {
		if m.proxyLogSearchActive || m.proxyLogSearchQuery != "" {
			queryPart := func() string {
				if m.proxyLogSearchActive {
					return lipgloss.NewStyle().Foreground(ui.ColorYellow).Render("/") +
						lipgloss.NewStyle().Foreground(ui.ColorText).Render(m.proxyLogSearchQuery) +
						lipgloss.NewStyle().Foreground(ui.ColorDim).Render("█")
				}
				return lipgloss.NewStyle().Foreground(ui.ColorDim2).Render("/") +
					lipgloss.NewStyle().Foreground(ui.ColorText).Render(m.proxyLogSearchQuery)
			}()
			matchPart := func() string {
				if len(m.proxyLogSearchMatches) == 0 {
					return "  " + lipgloss.NewStyle().Foreground(ui.ColorRed).Render("no matches")
				}
				return "  " + lipgloss.NewStyle().Foreground(ui.ColorGreen).Render(
					fmt.Sprintf("match %d/%d", m.proxyLogSearchIdx+1, len(m.proxyLogSearchMatches)),
				) + dimToolbar.Render("  (n=next  N=prev  esc=clear)")
			}()
			toolbar = "  " + queryPart + matchPart
		} else if m.proxyLogFor != "" {
			hScrollHint := dimToolbar.Render("  ←→ scroll")
			if m.proxyLogHScrollOff > 0 {
				hScrollHint = dimToolbar.Render(fmt.Sprintf("  ←→ +%d", m.proxyLogHScrollOff))
			}
			toolbar = "  " + dimToolbar.Render(fmt.Sprintf("source: toolhive/logs/%s.log", m.proxyLogFor)) +
				hScrollHint
		}
	}

	mainStyle := lipgloss.NewStyle().Width(mainW).Height(m.height-2).MaxHeight(m.height-2)

	// Only include toolbar if non-empty to avoid a trailing blank line.
	bodyParts := []string{titleText, tabBar, sep, content}
	if toolbar != "" {
		bodyParts = append(bodyParts, toolbar)
	}
	body := strings.Join(bodyParts, "\n")

	return mainStyle.Render(body)
}

// renderTab renders a single tab, highlighted if active.
func (m Model) renderTab(label string, p activePanel) string {
	if m.panel == p {
		return lipgloss.NewStyle().
			Foreground(ui.ColorBlue).
			Bold(true).
			Underline(true).
			Render("[" + label + "]")
	}
	return lipgloss.NewStyle().
		Foreground(ui.ColorDim2).
		Render("[" + label + "]")
}

// renderProxyLogs renders the proxy log panel.
func (m Model) renderProxyLogs(width int) string {
	_ = width
	if m.selected() == nil {
		return lipgloss.NewStyle().Foreground(ui.ColorDim).Render("No server selected")
	}
	if len(m.proxyLogLines) == 0 {
		return lipgloss.NewStyle().Foreground(ui.ColorDim2).Render("  Waiting for proxy logs…")
	}
	return m.proxyLogView.View()
}

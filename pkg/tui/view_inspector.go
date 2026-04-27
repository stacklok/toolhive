// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/stacklok/toolhive/cmd/thv/app/ui"
)

// renderInspector renders the 3-column tool inspector panel.
func (m Model) renderInspector(mainW int) string {
	toolListW := 22
	remaining := mainW - toolListW - 2 // 2 for separator columns
	if remaining < 20 {
		remaining = 20
	}
	responseW := remaining * 55 / 100
	formW := remaining - responseW - 1

	// mainStyle Height = m.height-2; title(1)+tabBar(1)+sep(1) = 3 overhead → inspH = m.height-5
	inspH := m.height - 5
	if inspH < 5 {
		inspH = 5
	}

	leftCol := m.renderInspToolList(toolListW, inspH)
	middleCol := m.renderInspForm(formW, inspH)
	rightCol := m.renderInspResponse(responseW, inspH)

	// Full-height vertical separators between columns.
	sepStyle := lipgloss.NewStyle().Foreground(ui.ColorDim)
	vline := sepStyle.Render(strings.Repeat("│\n", inspH-1) + "│")

	base := lipgloss.JoinHorizontal(lipgloss.Top, leftCol, vline, middleCol, vline, rightCol)

	// Tool info modal overlaid on top when active.
	if m.insp.showInfo {
		return m.renderToolInfoModal(base, mainW, inspH)
	}
	return base
}

// renderToolInfoModal renders a centered modal with the selected tool's description.
func (m Model) renderToolInfoModal(base string, w, h int) string {
	filtered := m.filteredTools()
	if len(filtered) == 0 || m.insp.toolIdx >= len(filtered) {
		return base
	}
	tool := filtered[m.insp.toolIdx]

	modalW := min(w-8, 64)
	innerW := modalW - 6 // padding 1,3

	titleStyle := lipgloss.NewStyle().Foreground(ui.ColorPurple).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(ui.ColorDim)
	textStyle := lipgloss.NewStyle().Foreground(ui.ColorText)
	hintStyle := lipgloss.NewStyle().Foreground(ui.ColorDim2)

	sep := dimStyle.Render(strings.Repeat("─", innerW))
	desc := tool.Description
	if desc == "" {
		desc = "(no description available)"
	}

	var sb strings.Builder
	sb.WriteString(titleStyle.Render(tool.Name) + "  " + hintStyle.Render("press any key to close") + "\n")
	sb.WriteString(sep + "\n")
	for _, line := range wrapText(desc, innerW, "") {
		sb.WriteString(textStyle.Render(line) + "\n")
	}

	modal := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ui.ColorPurple).
		Padding(1, 3).
		Width(modalW).
		Render(sb.String())

	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center,
		modal,
		lipgloss.WithWhitespaceChars(" "),
		lipgloss.WithWhitespaceForeground(ui.ColorDim),
	)
}

// renderInspToolList renders the left tool-list column of the inspector.
func (m Model) renderInspToolList(width, height int) string {
	dimStyle := lipgloss.NewStyle().Foreground(ui.ColorDim)
	sep := dimStyle.Render(strings.Repeat("─", width))

	filtered := m.filteredTools()
	countStr := fmt.Sprintf("(%d)", len(m.tools))
	if m.insp.filterActive || m.insp.filterQuery != "" {
		countStr = fmt.Sprintf("(%d/%d)", len(filtered), len(m.tools))
	}
	header := lipgloss.NewStyle().Foreground(ui.ColorText).Bold(true).
		Render("TOOLS  " + countStr)

	var sb strings.Builder
	sb.WriteString(header + "\n")
	sb.WriteString(sep + "\n")

	if m.toolsLoading {
		sb.WriteString(lipgloss.NewStyle().Foreground(ui.ColorDim2).Render("  Loading…") + "\n")
		return lipgloss.NewStyle().Width(width).Height(height).Render(sb.String())
	}
	if errors.Is(m.toolsErr, errStdioToolsNotAvailable) {
		for _, line := range wrapText("  "+m.toolsErr.Error(), width, "  ") {
			sb.WriteString(lipgloss.NewStyle().Foreground(ui.ColorDim).Render(line) + "\n")
		}
		return lipgloss.NewStyle().Width(width).Height(height).Render(sb.String())
	}
	if m.toolsErr != nil {
		for _, line := range wrapText("  "+m.toolsErr.Error(), width, "  ") {
			sb.WriteString(lipgloss.NewStyle().Foreground(ui.ColorRed).Render(line) + "\n")
		}
		return lipgloss.NewStyle().Width(width).Height(height).Render(sb.String())
	}

	// Filter prompt.
	if m.insp.filterActive {
		prompt := lipgloss.NewStyle().Foreground(ui.ColorYellow).Render("/") +
			lipgloss.NewStyle().Foreground(ui.ColorText).Render(m.insp.filterQuery) +
			lipgloss.NewStyle().Foreground(ui.ColorDim).Render("█")
		sb.WriteString(prompt + "\n")
	} else if m.insp.filterQuery != "" {
		prompt := lipgloss.NewStyle().Foreground(ui.ColorDim2).Render("/") +
			lipgloss.NewStyle().Foreground(ui.ColorDim2).Render(m.insp.filterQuery)
		sb.WriteString(prompt + "\n")
	}

	selBg := lipgloss.Color("#2a2e45")
	infoIcon := lipgloss.NewStyle().Foreground(ui.ColorDim).Render("ℹ")
	for i, t := range filtered {
		// Reserve 2 chars for the ℹ icon on selected row.
		name := truncateSidebar(t.Name, width-4)
		if i == m.insp.toolIdx {
			namePart := lipgloss.NewStyle().
				Foreground(ui.ColorText).
				Background(selBg).
				Bold(true).
				Render("  " + name)
			iconPart := lipgloss.NewStyle().Background(selBg).Render(" " + infoIcon)
			line := lipgloss.NewStyle().Background(selBg).Width(width).
				Render(namePart + iconPart)
			sb.WriteString(line + "\n")
		} else {
			sb.WriteString(lipgloss.NewStyle().Foreground(ui.ColorDim2).Render("  "+name) + "\n")
		}
	}
	if len(filtered) == 0 && !m.toolsLoading {
		sb.WriteString(lipgloss.NewStyle().Foreground(ui.ColorDim).Render("  no match") + "\n")
	}

	return lipgloss.NewStyle().Width(width).Height(height).Render(sb.String())
}

// renderInspForm renders the middle form column of the inspector.
func (m Model) renderInspForm(width, height int) string {
	filtered := m.filteredTools()
	if len(filtered) == 0 {
		return lipgloss.NewStyle().Width(width).Height(height).
			Foreground(ui.ColorDim).Render("  No tools available")
	}
	if m.insp.toolIdx >= len(filtered) {
		return lipgloss.NewStyle().Width(width).Height(height).
			Foreground(ui.ColorDim).Render("  No tools available")
	}

	tool := filtered[m.insp.toolIdx]
	dimStyle := lipgloss.NewStyle().Foreground(ui.ColorDim)
	sep := dimStyle.Render(strings.Repeat("─", width))

	var sb strings.Builder
	// Tool name and description (capped to 2 lines; press 'i' for full description).
	sb.WriteString(lipgloss.NewStyle().Foreground(ui.ColorCyan).Bold(true).Render(tool.Name) + "\n")
	if tool.Description != "" {
		descLines := wrapText(tool.Description, width-2, "")
		const maxDescLines = 2
		if len(descLines) > maxDescLines {
			descLines = descLines[:maxDescLines]
			descLines[maxDescLines-1] += lipgloss.NewStyle().Foreground(ui.ColorDim).Render("… [i] more")
		}
		for _, line := range descLines {
			sb.WriteString(lipgloss.NewStyle().Foreground(ui.ColorDim2).Render(line) + "\n")
		}
	}
	sb.WriteString(sep + "\n")

	// Form fields
	for i, f := range m.insp.fields {
		for _, line := range renderFormFieldFromStruct(f, i == m.insp.fieldIdx, width) {
			sb.WriteString(line + "\n")
		}
		if i < len(m.insp.fields)-1 {
			sb.WriteString(sep + "\n")
		}
	}

	if len(m.insp.fields) == 0 {
		sb.WriteString(lipgloss.NewStyle().Foreground(ui.ColorDim).Render("  (no parameters)") + "\n")
	}

	sb.WriteString("\n")

	// "↵ Call tool" button — left side.
	callBtn := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ui.ColorBlue).
		Foreground(ui.ColorBlue).
		Padding(0, 2).
		Render("↵  Call tool")

	sb.WriteString(callBtn)

	return lipgloss.NewStyle().Width(width).Height(height).Render(sb.String())
}

// renderInspResponse renders the right response column of the inspector.
//
//nolint:gocyclo // renders all response states; splitting would scatter related view logic
func (m Model) renderInspResponse(width, height int) string {
	sel := m.selected()
	dimStyle := lipgloss.NewStyle().Foreground(ui.ColorDim)

	var sb strings.Builder

	// REQUEST header — title left, [Y] COPY CURL badge at far right.
	reqTitle := lipgloss.NewStyle().Foreground(ui.ColorText).Bold(true).Render("REQUEST")
	copyCurlBadge := inspCopyBadge("y", "COPY CURL")
	reqGap := width - 2 - ui.VisibleLen(reqTitle) - ui.VisibleLen(copyCurlBadge)
	if reqGap < 1 {
		reqGap = 1
	}
	sb.WriteString(reqTitle + strings.Repeat(" ", reqGap) + copyCurlBadge + "\n")
	sb.WriteString(dimStyle.Render(strings.Repeat("─", width-2)) + "\n")

	if ft := m.filteredTools(); sel != nil && len(ft) > 0 && m.insp.toolIdx < len(ft) {
		tool := ft[m.insp.toolIdx]
		args, _, _ := inspFieldValues(m.insp.fields)
		curl := buildCurlStr(sel, tool.Name, args)
		for _, line := range strings.Split(curl, "\n") {
			sb.WriteString(renderCurlLine(line) + "\n")
		}
	} else {
		sb.WriteString(dimStyle.Render("  Type arguments and press ↵ to call") + "\n")
	}

	sb.WriteString(dimStyle.Render(strings.Repeat("─", width-2)) + "\n")
	sb.WriteString("\n")

	// RESPONSE header — title + status left, [C] COPY JSON badge at far right when result available.
	respTitle := lipgloss.NewStyle().Foreground(ui.ColorText).Bold(true).Render("RESPONSE")
	statusBadge := ""
	if !m.insp.loading && m.insp.result != "" {
		if m.insp.resultOK {
			statusBadge = "  " + lipgloss.NewStyle().Foreground(ui.ColorGreen).
				Render(fmt.Sprintf("✓ SUCCESS %dms", m.insp.resultMs))
		} else {
			statusBadge = "  " + lipgloss.NewStyle().Foreground(ui.ColorRed).Render("✗ ERROR")
		}
	}
	copyJSONBadge := ""
	if m.insp.result != "" {
		copyJSONBadge = inspCopyBadge("c", "COPY JSON")
	}
	respLeft := respTitle + statusBadge
	if copyJSONBadge != "" {
		respGap := width - 2 - ui.VisibleLen(respLeft) - ui.VisibleLen(copyJSONBadge)
		if respGap < 1 {
			respGap = 1
		}
		sb.WriteString(respLeft + strings.Repeat(" ", respGap) + copyJSONBadge + "\n")
	} else {
		sb.WriteString(respLeft + "\n")
	}
	sb.WriteString(dimStyle.Render(strings.Repeat("─", width-2)) + "\n")

	switch {
	case m.insp.loading:
		frame := inspSpinFrames[m.insp.spinFrame%len(inspSpinFrames)]
		sb.WriteString(lipgloss.NewStyle().Foreground(ui.ColorCyan).Render(frame+" Calling…") + "\n")
	case m.insp.result != "" && m.insp.jsonRoot != nil:
		// REQUEST header (1) + sep (1) + curl (~3) + sep (1) + RESPONSE header (1) + sep (1) = ~8 overhead.
		// Subtract additional log section height if log lines are present.
		const treeHeaderOverhead = 8
		const logSectionHeight = 9 // blank + sep + LOGS + 6 log lines
		treeH := height - treeHeaderOverhead
		if len(m.insp.logLines) > 0 {
			treeH -= logSectionHeight
		}
		if treeH < 3 {
			treeH = 3
		}
		sb.WriteString(renderJSONTree(m.insp.treeVis, m.insp.treeCursor, m.insp.treeScroll, width, treeH))
	case m.insp.result != "":
		sb.WriteString(m.insp.respView.View())
	default:
		sb.WriteString(dimStyle.Render("  Response will appear here") + "\n")
	}

	// LOGS section — shown below the response whenever there are TUI log messages.
	if len(m.insp.logLines) > 0 {
		sb.WriteString("\n")
		sb.WriteString(dimStyle.Render(strings.Repeat("─", width-2)) + "\n")
		sb.WriteString(lipgloss.NewStyle().Foreground(ui.ColorYellow).Bold(true).Render("LOGS") + "\n")
		sb.WriteString(m.insp.logView.View())
	}

	return lipgloss.NewStyle().Width(width).Height(height).Render(sb.String())
}

// renderTools renders the tools list for the selected workload using the toolsView viewport.
func (m Model) renderTools(_ int) string {
	if m.selected() == nil {
		return lipgloss.NewStyle().Foreground(ui.ColorDim).Render("No server selected")
	}
	if m.toolsLoading {
		return lipgloss.NewStyle().Foreground(ui.ColorDim2).Render("  Loading tools…")
	}
	if errors.Is(m.toolsErr, errStdioToolsNotAvailable) {
		return lipgloss.NewStyle().Foreground(ui.ColorDim).Render("  " + m.toolsErr.Error())
	}
	if m.toolsErr != nil {
		return lipgloss.NewStyle().Foreground(ui.ColorRed).Render("  Error: " + m.toolsErr.Error())
	}
	if len(m.tools) == 0 {
		return lipgloss.NewStyle().Foreground(ui.ColorDim).Render("  No tools available")
	}
	return m.toolsView.View()
}

// buildToolsContent builds the full scrollable content string for the tools viewport.
// selectedIdx highlights the currently selected tool (-1 for none).
func buildToolsContent(tools []mcp.Tool, width, selectedIdx int) string {
	nameW := 28
	descW := width - nameW - 4
	if descW < 20 {
		descW = 20
	}

	nameStyle := lipgloss.NewStyle().Foreground(ui.ColorCyan).Bold(true)
	descStyle := lipgloss.NewStyle().Foreground(ui.ColorDim2)
	countStyle := lipgloss.NewStyle().Foreground(ui.ColorDim)
	selBg := lipgloss.NewStyle().Background(lipgloss.Color("#2a2f45"))
	hintStyle := lipgloss.NewStyle().Foreground(ui.ColorDim)

	var sb strings.Builder
	sb.WriteString("  " + countStyle.Render(fmt.Sprintf("%d tools", len(tools))))
	sb.WriteString("  " + hintStyle.Render("↵ open in inspector"))
	sb.WriteString("\n\n")

	for i, t := range tools {
		name := truncateSidebar(t.Name, nameW)
		namePart := "  " + ui.PadToWidth(nameStyle.Render(name), nameW+2)

		var lines []string
		if t.Description != "" {
			lines = wrapText(t.Description, descW, "")
		}

		selected := i == selectedIdx
		renderLine := func(s string) string {
			if selected {
				return selBg.Width(width - 2).Render(s)
			}
			return s
		}

		if len(lines) == 0 {
			sb.WriteString(renderLine(namePart) + "\n")
			continue
		}
		for j, line := range lines {
			if j == 0 {
				sb.WriteString(renderLine(namePart+descStyle.Render(line)) + "\n")
			} else {
				indent := strings.Repeat(" ", nameW+4)
				sb.WriteString(renderLine(indent+descStyle.Render(line)) + "\n")
			}
		}
	}
	return sb.String()
}

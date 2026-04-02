// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	regtypes "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/cmd/thv/app/ui"
)

// registryBoxDims returns the shared overlay dimensions.
func (m Model) registryBoxDims() (boxW, innerW, visibleRows int) {
	boxW = max(m.width*80/100, 50)
	innerW = boxW - 4 // border (1 each side) + padding (1 each side)
	visibleRows = max(m.height*70/100, 6)
	return
}

// renderRegistryOverlay renders the registry browser overlay.
// It delegates to the run form or detail view as appropriate.
func (m Model) renderRegistryOverlay(_ string) string {
	if m.runForm.open {
		return m.renderRunFormOverlay()
	}
	if m.registry.detail {
		return m.renderRegistryDetailOverlay()
	}
	return m.renderRegistryListOverlay()
}

// renderRegistryListOverlay renders the searchable list of registry items.
func (m Model) renderRegistryListOverlay() string {
	items := m.filteredRegistryItems()
	boxW, innerW, visibleRows := m.registryBoxDims()

	// Layout: header(1) + sep(1) + filter(1) + sep(1) + footer-sep(1) + footer(1) + border/pad(4)
	const fixedLines = 10
	itemRows := max(visibleRows-fixedLines, 2)

	// Column widths: 2-space indent + name(nameW) + 2-space gap + desc + 2-space gap + tag(tagW)
	const tagColW = 10 // "  " + up to 8 chars
	nameW := max(innerW*28/100, 16)
	descW := max(innerW-2-nameW-2-tagColW, 10) // innerW = 2+nameW+2+descW+tagColW

	titleStyle := lipgloss.NewStyle().Foreground(ui.ColorPurple).Bold(true)
	hintStyle := lipgloss.NewStyle().Foreground(ui.ColorDim2)
	dimStyle := lipgloss.NewStyle().Foreground(ui.ColorDim)
	nameStyle := lipgloss.NewStyle().Foreground(ui.ColorText).Bold(true)
	descStyle := lipgloss.NewStyle().Foreground(ui.ColorDim2)
	tagStyle := lipgloss.NewStyle().Foreground(ui.ColorGreen)
	selBg := lipgloss.NewStyle().Background(lipgloss.Color("#2a2e45"))

	var sb strings.Builder

	// Header
	sb.WriteString(titleStyle.Render("REGISTRY") +
		"  " + hintStyle.Render("↑↓ navigate  enter detail  esc close  type to filter") + "\n")
	sb.WriteString(dimStyle.Render(strings.Repeat("─", innerW)) + "\n")

	// Filter line
	sb.WriteString(dimStyle.Render("  ⌕ ") +
		lipgloss.NewStyle().Foreground(ui.ColorText).Render(m.registry.filter) +
		lipgloss.NewStyle().Foreground(ui.ColorDim).Render("█") + "\n")
	sb.WriteString(dimStyle.Render(strings.Repeat("─", innerW)) + "\n")

	// Items
	switch {
	case m.registry.loading:
		sb.WriteString("\n  " + dimStyle.Render("Loading registry…") + "\n")
	case m.registry.err != nil:
		sb.WriteString("\n  " + lipgloss.NewStyle().Foreground(ui.ColorRed).
			Render("Error: "+m.registry.err.Error()) + "\n")
	case len(items) == 0:
		sb.WriteString("\n  " + dimStyle.Render("No servers found") + "\n")
	default:
		end := min(m.registry.scrollOff+itemRows, len(items))
		for i, item := range items[m.registry.scrollOff:end] {
			globalIdx := m.registry.scrollOff + i
			name := truncateSidebar(item.GetName(), nameW)
			desc := runesTruncate(item.GetDescription(), descW)
			tagStr := registryTagStr(item.GetTags(), tagStyle)

			line := "  " + ui.PadToWidth(nameStyle.Render(name), nameW+2) +
				ui.PadToWidth(descStyle.Render(desc), descW+2) + tagStr
			if globalIdx == m.registry.idx {
				line = selBg.Width(innerW).Render(line)
			}
			sb.WriteString(line + "\n")
		}
		if len(items) > itemRows {
			sb.WriteString(dimStyle.Render(fmt.Sprintf("  %d–%d / %d",
				m.registry.scrollOff+1, end, len(items))) + "\n")
		}
	}

	sb.WriteString(dimStyle.Render(strings.Repeat("─", innerW)) + "\n")
	sb.WriteString(dimStyle.Render(fmt.Sprintf("  %d servers available", len(m.registry.items))))

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(ui.ColorPurple).Padding(0, 1).Width(boxW).
			Render(sb.String()),
		lipgloss.WithWhitespaceChars(" "),
		lipgloss.WithWhitespaceForeground(ui.ColorDim),
	)
}

// renderRegistryDetailOverlay renders the full detail view for the selected registry item.
func (m Model) renderRegistryDetailOverlay() string {
	boxW, innerW, visibleRows := m.registryBoxDims()
	items := m.filteredRegistryItems()
	if len(items) == 0 || m.registry.idx >= len(items) {
		return m.renderRegistryListOverlay()
	}
	item := items[m.registry.idx]

	dimStyle := lipgloss.NewStyle().Foreground(ui.ColorDim)
	sep := dimStyle.Render(strings.Repeat("─", innerW))
	lines := buildDetailLines(item, innerW, sep)

	const headerLines = 2
	contentLines := visibleRows - headerLines - 2
	total := len(lines)
	scrollOff := min(m.registry.detailScroll, max(total-contentLines, 0))
	end := min(scrollOff+contentLines, total)

	var sb strings.Builder
	titleStyle := lipgloss.NewStyle().Foreground(ui.ColorPurple).Bold(true)
	hintStyle := lipgloss.NewStyle().Foreground(ui.ColorDim2)
	textStyle := lipgloss.NewStyle().Foreground(ui.ColorText)
	breadcrumb := titleStyle.Render("REGISTRY") + dimStyle.Render("  /  ") +
		textStyle.Bold(true).Render(item.GetName())
	sb.WriteString(breadcrumb + "  " + hintStyle.Render("↑↓ scroll  r=run  y=copy cmd  esc back") + "\n")
	sb.WriteString(sep + "\n")
	for _, l := range lines[scrollOff:end] {
		sb.WriteString(l + "\n")
	}
	if total > contentLines {
		sb.WriteString(dimStyle.Render(fmt.Sprintf("  %d/%d lines", scrollOff+end-scrollOff, total)))
	}

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(ui.ColorPurple).Padding(0, 1).Width(boxW).
			Render(sb.String()),
		lipgloss.WithWhitespaceChars(" "),
		lipgloss.WithWhitespaceForeground(ui.ColorDim),
	)
}

// registryTagStr formats the first tag for the list column.
func registryTagStr(tags []string, style lipgloss.Style) string {
	if len(tags) == 0 {
		return ""
	}
	t := tags[0]
	if len([]rune(t)) > 8 {
		t = string([]rune(t)[:7]) + "…"
	}
	return style.Render(t)
}

// buildDetailLines builds the scrollable content lines for a registry item detail view.
func buildDetailLines(item regtypes.ServerMetadata, innerW int, sep string) []string {
	dimStyle := lipgloss.NewStyle().Foreground(ui.ColorDim)
	labelStyle := lipgloss.NewStyle().Foreground(ui.ColorDim2)
	textStyle := lipgloss.NewStyle().Foreground(ui.ColorText)
	cyanStyle := lipgloss.NewStyle().Foreground(ui.ColorCyan)
	greenStyle := lipgloss.NewStyle().Foreground(ui.ColorGreen)
	yellowStyle := lipgloss.NewStyle().Foreground(ui.ColorYellow)

	detailRow := func(key, val string) string {
		return labelStyle.Render(fmt.Sprintf("  %-14s", key)) + textStyle.Render(val)
	}
	section := func(title string) string {
		return "\n" + sep + "\n" + dimStyle.Render("  "+strings.ToUpper(title))
	}

	var lines []string
	lines = append(lines, buildDetailHeader(item, dimStyle, labelStyle, textStyle, greenStyle, yellowStyle, cyanStyle)...)

	if desc := item.GetDescription(); desc != "" {
		lines = append(lines, section("Description"))
		lines = append(lines, wrapText(desc, innerW-4, "  ")...)
	}
	if repo := item.GetRepositoryURL(); repo != "" {
		lines = append(lines, section("Repository"))
		lines = append(lines, detailRow("URL", repo))
	}
	lines = append(lines, buildDetailServerType(item, section, detailRow)...)
	lines = append(lines, buildDetailTools(item, innerW, section, cyanStyle, labelStyle)...)
	lines = append(lines, "\n"+sep)
	return lines
}

// buildDetailHeader returns the name/tier/status/transport/meta lines.
func buildDetailHeader(
	item regtypes.ServerMetadata,
	dimStyle, labelStyle, textStyle, greenStyle, yellowStyle, cyanStyle lipgloss.Style,
) []string {
	meta := item.GetMetadata()
	starsStr := ""
	if meta != nil && meta.Stars > 0 {
		starsStr = "  " + dimStyle.Render(fmt.Sprintf("★ %d", meta.Stars))
	}
	lastUpdStr := ""
	if meta != nil && meta.LastUpdated != "" {
		lastUpdStr = "  " + dimStyle.Render("updated "+meta.LastUpdated[:min(len(meta.LastUpdated), 10)])
	}

	tierStr := func() string {
		switch item.GetTier() {
		case "Official":
			return greenStyle.Render("Official")
		case "":
			return ""
		default:
			return yellowStyle.Render(item.GetTier())
		}
	}()

	lines := []string{"\n" + textStyle.Bold(true).Render("  "+item.GetName()) + starsStr + lastUpdStr}
	badge := buildBadge(tierStr, item.GetStatus(), item.GetTransport(), dimStyle, labelStyle, cyanStyle)
	if badge != "" {
		lines = append(lines, "  "+badge)
	}
	return lines
}

// buildBadge joins tier/status/transport with "·" separators.
func buildBadge(tier, status, transport string, dimStyle, labelStyle, cyanStyle lipgloss.Style) string {
	dot := dimStyle.Render("  ·  ")
	var parts []string
	if tier != "" {
		parts = append(parts, tier)
	}
	if status != "" {
		parts = append(parts, labelStyle.Render(status))
	}
	if transport != "" {
		parts = append(parts, cyanStyle.Render(transport))
	}
	return strings.Join(parts, dot)
}

// buildDetailServerType appends container image or remote URL section if present.
func buildDetailServerType(
	item regtypes.ServerMetadata,
	section func(string) string,
	detailRow func(string, string) string,
) []string {
	var lines []string
	switch v := item.(type) {
	case interface{ GetImage() string }:
		if img := v.GetImage(); img != "" {
			lines = append(lines, section("Container"))
			lines = append(lines, detailRow("Image", img))
		}
	case interface{ GetURL() string }:
		if u := v.GetURL(); u != "" {
			lines = append(lines, section("Endpoint"))
			lines = append(lines, detailRow("URL", u))
		}
	}
	return lines
}

// buildDetailTools appends the tools section (with descriptions if available).
func buildDetailTools(
	item regtypes.ServerMetadata,
	innerW int,
	section func(string) string,
	cyanStyle, labelStyle lipgloss.Style,
) []string {
	const toolNameColW = 32
	var lines []string
	if toolDefs := item.GetToolDefinitions(); len(toolDefs) > 0 {
		lines = append(lines, section(fmt.Sprintf("Tools  (%d)", len(toolDefs))))
		for _, t := range toolDefs {
			nameRunes := []rune(t.Name)
			if len(nameRunes) <= toolNameColW {
				desc := runesTruncate(t.Description, innerW-toolNameColW-4)
				lines = append(lines, "  "+ui.PadToWidth(cyanStyle.Render(t.Name), toolNameColW+2)+labelStyle.Render(desc))
			} else {
				// Name is long: put description on the next indented line.
				lines = append(lines, "  "+cyanStyle.Render(t.Name))
				if t.Description != "" {
					lines = append(lines, "    "+labelStyle.Render(runesTruncate(t.Description, innerW-6)))
				}
			}
		}
	} else if toolNames := item.GetTools(); len(toolNames) > 0 {
		lines = append(lines, section(fmt.Sprintf("Tools  (%d)", len(toolNames))))
		for _, t := range toolNames {
			lines = append(lines, "  "+cyanStyle.Render(t))
		}
	}
	return lines
}

// renderRunFormOverlay renders the run-from-registry form overlay.
func (m Model) renderRunFormOverlay() string {
	boxW, innerW, visibleRows := m.registryBoxDims()
	item := m.runForm.item

	dimStyle := lipgloss.NewStyle().Foreground(ui.ColorDim)
	titleStyle := lipgloss.NewStyle().Foreground(ui.ColorPurple).Bold(true)
	hintStyle := lipgloss.NewStyle().Foreground(ui.ColorDim2)
	textStyle := lipgloss.NewStyle().Foreground(ui.ColorText)
	greenStyle := lipgloss.NewStyle().Foreground(ui.ColorGreen)
	sep := dimStyle.Render(strings.Repeat("─", innerW))

	var sb strings.Builder

	// Breadcrumb header.
	breadcrumb := titleStyle.Render("REGISTRY") + dimStyle.Render("  /  ") +
		textStyle.Bold(true).Render(item.GetName()) + dimStyle.Render("  /  ") +
		titleStyle.Render("RUN")
	hint := "tab=next  enter=run  esc=cancel"
	if m.runForm.running {
		hint = "starting…"
	}
	sb.WriteString(breadcrumb + "  " + hintStyle.Render(hint) + "\n")
	sb.WriteString(sep + "\n")

	// Form fields (scrollable).
	const linesPerField = 4 // label + desc + input + gap
	const headerFooterLines = 5
	maxFields := max((visibleRows-headerFooterLines)/linesPerField, 2)
	endIdx := min(m.runForm.scroll+maxFields, len(m.runForm.fields))

	for i := m.runForm.scroll; i < endIdx; i++ {
		f := m.runForm.fields[i]
		focused := i == m.runForm.idx
		lines := renderFormFieldFromStruct(f, focused, innerW)
		for _, l := range lines {
			sb.WriteString("  " + l + "\n")
		}
		sb.WriteString("\n")
	}

	if len(m.runForm.fields) > maxFields {
		sb.WriteString(dimStyle.Render(fmt.Sprintf("  field %d/%d", m.runForm.idx+1, len(m.runForm.fields))) + "\n")
	}

	// Run button.
	sb.WriteString(sep + "\n")
	btnLabel := "  ▶ Run " + item.GetName()
	if m.runForm.running {
		btnLabel = "  ⟳ Starting…"
	}
	sb.WriteString(greenStyle.Bold(true).Render(btnLabel))

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(ui.ColorPurple).Padding(0, 1).Width(boxW).
			Render(sb.String()),
		lipgloss.WithWhitespaceChars(" "),
		lipgloss.WithWhitespaceForeground(ui.ColorDim),
	)
}

// buildRunCmd builds a suggested `thv run` command string from registry metadata.
// Required env vars become --secret flags; optional ones are shown as comments.
// Non-default transport and permission profile are included when present.
func buildRunCmd(item regtypes.ServerMetadata) string {
	const defaultTransport = "streamable-http"

	var sb strings.Builder
	sb.WriteString("thv run ")
	sb.WriteString(item.GetName())

	// Transport only when non-default.
	if t := item.GetTransport(); t != "" && t != defaultTransport {
		sb.WriteString(" --transport ")
		sb.WriteString(t)
	}

	// Permission profile from ImageMetadata (Permissions is a direct field, not on the interface).
	if img, ok := item.(*regtypes.ImageMetadata); ok && img != nil && img.Permissions != nil {
		if name := img.Permissions.Name; name != "" && name != "none" {
			sb.WriteString(" --permission-profile ")
			sb.WriteString(name)
		}
	}

	// Required env vars → --secret; optional → comment line.
	var optional []string
	for _, ev := range item.GetEnvVars() {
		if ev == nil {
			continue
		}
		if ev.Required {
			sb.WriteString(" --secret ")
			sb.WriteString(ev.Name)
		} else {
			optional = append(optional, ev.Name)
		}
	}
	for _, name := range optional {
		sb.WriteString("\n# optional: --env ")
		sb.WriteString(name)
		sb.WriteString("=<value>")
	}

	return sb.String()
}

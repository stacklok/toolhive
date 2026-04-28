// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// commandEntry is a single entry in a help section.
type commandEntry struct {
	name string
	desc string
}

// helpSection groups commands under a heading.
type helpSection struct {
	heading  string
	commands []commandEntry
}

// Root help sections — hardcoded for semantic ordering and grouping.
var rootHelpSections = []helpSection{
	{
		heading: "Servers",
		commands: []commandEntry{
			{"run", "Run an MCP server"},
			{"start", "Start (resume) a stopped server"},
			{"stop", "Stop an MCP server"},
			{"restart", "Restart an MCP server"},
			{"rm", "Remove an MCP server"},
			{"list", "List running MCP servers"},
			{"status", "Show detailed server status"},
			{"logs", "View server logs"},
			{"build", "Build a server image without running it"},
			{"tui", "Open the interactive dashboard"},
		},
	},
	{
		heading: "Registry",
		commands: []commandEntry{
			{"registry", "Browse the MCP server registry"},
			{"search", "Search registry for MCP servers"},
		},
	},
	{
		heading: "Clients",
		commands: []commandEntry{
			{"client", "Manage MCP client configurations"},
			{"export", "Export server config for a client"},
			{"mcp", "Interact with MCP servers for debugging"},
			{"inspector", "Open the MCP inspector"},
		},
	},
	{
		heading: "Other",
		commands: []commandEntry{
			{"proxy", "Manage proxy settings"},
			{"secret", "Manage secrets"},
			{"group", "Manage server groups"},
			{"skill", "Manage skills"},
			{"config", "Manage application configuration"},
			{"serve", "Start the ToolHive API server"},
			{"runtime", "Container runtime commands"},
			{"version", "Show version information"},
			{"completion", "Generate shell completion scripts"},
		},
	},
}

// RenderHelp prints the styled help page.
// - Root command: 2-column command grid
// - Parent commands with subcommands: styled subcommand list
// - Non-TTY or leaf commands: falls back to cmd.Usage()
func RenderHelp(cmd *cobra.Command) {
	if !term.IsTerminal(int(os.Stdout.Fd())) { //nolint:gosec // uintptr fits int on all supported platforms
		_ = cmd.Usage()
		return
	}

	// Non-root parent command: show styled subcommand list.
	if cmd.Parent() != nil && cmd.HasSubCommands() {
		renderParentHelp(cmd)
		return
	}

	// Non-root leaf command: fall back to Cobra default.
	if cmd.Parent() != nil {
		_ = cmd.Usage()
		return
	}

	brand := lipgloss.NewStyle().
		Foreground(ColorBlue).
		Bold(true).
		Render("ToolHive")

	descStyle := lipgloss.NewStyle().Foreground(ColorDim2)
	usageLine := lipgloss.NewStyle().
		Foreground(ColorDim).
		Render("Usage:  thv <command> [flags]")

	sectionHeading := lipgloss.NewStyle().
		Foreground(ColorPurple).
		Bold(true)

	cmdName := lipgloss.NewStyle().
		Foreground(ColorCyan).
		Width(14)

	cmdDesc := lipgloss.NewStyle().
		Foreground(ColorDim2)

	footerHint := lipgloss.NewStyle().
		Foreground(ColorDim).
		Render("Run  thv <command> --help  for details on a specific command.")

	var sb strings.Builder

	sb.WriteString("\n")
	fmt.Fprintf(&sb, "  %s\n\n", brand)
	for _, line := range strings.Split(strings.TrimSpace(cmd.Long), "\n") {
		fmt.Fprintf(&sb, "  %s\n", descStyle.Render(line))
	}
	sb.WriteString("\n")
	fmt.Fprintf(&sb, "  %s\n\n", usageLine)

	// Render sections in two columns
	cols := [][]helpSection{
		rootHelpSections[:2],
		rootHelpSections[2:],
	}

	// Build each column as lines
	colLines := make([][]string, 2)
	for ci, sections := range cols {
		for _, sec := range sections {
			colLines[ci] = append(colLines[ci], fmt.Sprintf("  %s", sectionHeading.Render(sec.heading)))
			for _, entry := range sec.commands {
				line := fmt.Sprintf("    %s%s",
					cmdName.Render(entry.name),
					cmdDesc.Render(entry.desc),
				)
				colLines[ci] = append(colLines[ci], line)
			}
			colLines[ci] = append(colLines[ci], "")
		}
	}

	// Interleave: print left column side-by-side with right column
	maxRows := len(colLines[0])
	if len(colLines[1]) > maxRows {
		maxRows = len(colLines[1])
	}

	// Calculate column width from the actual content so nothing overflows.
	colWidth := 0
	for _, line := range colLines[0] {
		if vl := VisibleLen(line); vl > colWidth {
			colWidth = vl
		}
	}
	colWidth += 4 // gap between columns

	for i := range maxRows {
		left := ""
		right := ""
		if i < len(colLines[0]) {
			left = colLines[0][i]
		}
		if i < len(colLines[1]) {
			right = colLines[1][i]
		}
		// Pad left column to colWidth visible chars (strip ANSI for width calc)
		padded := PadToWidth(left, colWidth)
		sb.WriteString(padded + right + "\n")
	}

	fmt.Fprintf(&sb, "  %s\n\n", footerHint)

	fmt.Print(sb.String())
}

// RenderCommandUsage prints a styled usage hint for a command when the user
// omits required arguments. Falls back to cmd.Usage() on non-TTY output.
func RenderCommandUsage(cmd *cobra.Command) {
	if !term.IsTerminal(int(os.Stdout.Fd())) { //nolint:gosec // uintptr fits int on all supported platforms
		_ = cmd.Usage()
		return
	}

	desc := cmd.Long
	if desc == "" {
		desc = cmd.Short
	}

	var sb strings.Builder
	sb.WriteString("\n")

	if desc != "" {
		fmt.Fprintf(&sb, "  %s\n\n", lipgloss.NewStyle().Foreground(ColorDim2).Render(desc))
	}

	fmt.Fprintf(&sb, "  %s\n", lipgloss.NewStyle().Foreground(ColorDim).Render("Usage:"))
	fmt.Fprintf(&sb, "    %s\n", lipgloss.NewStyle().Foreground(ColorCyan).Render(cmd.UseLine()))

	if cmd.Example != "" {
		sb.WriteString("\n")
		fmt.Fprintf(&sb, "  %s\n", lipgloss.NewStyle().Foreground(ColorDim).Render("Examples:"))
		for _, line := range strings.Split(strings.TrimRight(cmd.Example, "\n"), "\n") {
			fmt.Fprintf(&sb, "    %s\n", lipgloss.NewStyle().Foreground(ColorDim2).Render(line))
		}
	}

	sb.WriteString("\n")
	fmt.Fprintf(&sb, "  %s\n\n",
		lipgloss.NewStyle().Foreground(ColorDim).Render(
			"Run  thv "+cmd.Name()+" --help  for more information."))

	fmt.Print(sb.String())
}

// renderParentHelp prints a styled subcommand list for a parent command.
func renderParentHelp(cmd *cobra.Command) {
	var sb strings.Builder
	sb.WriteString("\n")

	desc := cmd.Long
	if desc == "" {
		desc = cmd.Short
	}
	if desc != "" {
		fmt.Fprintf(&sb, "  %s\n\n", lipgloss.NewStyle().Foreground(ColorDim2).Render(desc))
	}

	fmt.Fprintf(&sb, "  %s\n", lipgloss.NewStyle().Foreground(ColorDim).Render("Usage:"))
	fmt.Fprintf(&sb, "    %s\n\n", lipgloss.NewStyle().Foreground(ColorCyan).Render("thv "+cmd.Name()+" <command> [flags]"))

	fmt.Fprintf(&sb, "  %s\n", lipgloss.NewStyle().Foreground(ColorPurple).Bold(true).Render("Commands"))

	nameStyle := lipgloss.NewStyle().Foreground(ColorCyan).Width(14)
	descStyle := lipgloss.NewStyle().Foreground(ColorDim2)

	for _, sub := range cmd.Commands() {
		if sub.Hidden {
			continue
		}
		fmt.Fprintf(&sb, "    %s%s\n", nameStyle.Render(sub.Name()), descStyle.Render(sub.Short))
	}

	sb.WriteString("\n")
	fmt.Fprintf(&sb, "  %s\n\n",
		lipgloss.NewStyle().Foreground(ColorDim).Render(
			"Run  thv "+cmd.Name()+" <command> --help  for details."))

	fmt.Print(sb.String())
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss"

	"github.com/stacklok/toolhive/cmd/thv/app/ui"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
)

// renderFormFieldFromStruct renders a formField with its metadata as a labelled input.
func renderFormFieldFromStruct(f formField, focused bool, width int) []string {
	tag := ""
	if f.typeName != "" {
		tag = "[" + f.typeName + "]"
	} else if f.secret {
		tag = "(secret)"
	}
	return renderFormField(f.name, f.desc, tag, f.required, focused, f.input, width)
}

// renderFormField renders a single labelled form field with optional description,
// required marker, extra tag, and a bordered text input. It returns the rendered
// lines (label, optional description, input) as a slice of strings.
func renderFormField(name, desc, extraTag string, required, focused bool, input textinput.Model, width int) []string {
	var lines []string

	reqMark := ""
	if required {
		reqMark = lipgloss.NewStyle().Foreground(ui.ColorRed).Bold(true).Render(" *")
	}
	tag := ""
	if extraTag != "" {
		tag = "  " + lipgloss.NewStyle().Foreground(ui.ColorDim2).Render(extraTag)
	}
	label := lipgloss.NewStyle().Foreground(ui.ColorText).Bold(true).Render(name) + reqMark + tag
	lines = append(lines, label)

	if desc != "" {
		lines = append(lines, lipgloss.NewStyle().Foreground(ui.ColorDim2).Render("  "+truncateSidebar(desc, width-4)))
	}

	inputStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ui.ColorDim).
		Width(width - 4)
	if focused {
		inputStyle = inputStyle.BorderForeground(ui.ColorCyan)
	}
	lines = append(lines, inputStyle.Render(input.View()))

	return lines
}

// inspCopyBadge renders a small [KEY] LABEL badge for the inspector headers.
func inspCopyBadge(key, label string) string {
	keyPart := lipgloss.NewStyle().
		Background(lipgloss.Color("#2a2f45")).
		Foreground(ui.ColorText).
		Bold(true).
		Render(" " + key + " ")
	labelPart := lipgloss.NewStyle().
		Background(lipgloss.Color("#1a1d2e")).
		Foreground(ui.ColorDim2).
		Render(" " + label + " ")
	return keyPart + labelPart
}

// renderCurlLine applies syntax highlighting to a single line of a curl command.
func renderCurlLine(line string) string {
	trimmed := strings.TrimLeft(line, " ")
	indent := line[:len(line)-len(trimmed)]

	keyword := lipgloss.NewStyle().Foreground(ui.ColorBlue).Bold(true)
	flagStyle := lipgloss.NewStyle().Foreground(ui.ColorPurple)
	methodStyle := lipgloss.NewStyle().Foreground(ui.ColorYellow).Bold(true)
	urlStyle := lipgloss.NewStyle().Foreground(ui.ColorCyan)
	dimStyle := lipgloss.NewStyle().Foreground(ui.ColorDim2)
	strStyle := lipgloss.NewStyle().Foreground(ui.ColorText)

	switch {
	case strings.HasPrefix(trimmed, "curl "):
		// "curl -X POST \"
		rest := trimmed[5:]
		// rest should be "-X POST \"
		parts := strings.Fields(rest)
		out := keyword.Render("curl") + " "
		if len(parts) >= 2 && parts[0] == "-X" {
			out += flagStyle.Render("-X") + " " + methodStyle.Render(parts[1])
			if len(parts) > 2 {
				out += " " + dimStyle.Render(strings.Join(parts[2:], " "))
			}
		} else {
			out += dimStyle.Render(rest)
		}
		return indent + out
	case strings.HasPrefix(trimmed, "'http"):
		// URL line: 'http://...' \
		idx := strings.LastIndex(trimmed, "'")
		if idx > 0 {
			url := trimmed[:idx+1]
			suffix := strings.TrimSpace(trimmed[idx+1:])
			out := urlStyle.Render(url)
			if suffix != "" {
				out += " " + dimStyle.Render(suffix)
			}
			return indent + out
		}
		return indent + urlStyle.Render(trimmed)
	case strings.HasPrefix(trimmed, "-H "):
		// -H 'Header: value' \
		rest := trimmed[3:]
		return indent + flagStyle.Render("-H") + " " + strStyle.Render(rest)
	case strings.HasPrefix(trimmed, "-d "):
		// -d '...'
		rest := trimmed[3:]
		return indent + flagStyle.Render("-d") + " " + dimStyle.Render(rest)
	default:
		return indent + dimStyle.Render(trimmed)
	}
}

// wrapText wraps text to fit within maxW runes per line, with a given indent prefix.
func wrapText(text string, maxW int, indent string) []string {
	words := strings.Fields(text)
	var lines []string
	line := indent
	for _, w := range words {
		candidate := line + w
		if line != indent {
			candidate = line + " " + w
		}
		if len([]rune(candidate)) > maxW && line != indent {
			lines = append(lines, line)
			line = indent + w
		} else {
			line = candidate
		}
	}
	if line != indent {
		lines = append(lines, line)
	}
	return lines
}

// runesTruncate truncates s to at most n runes, appending "..." if truncated.
func runesTruncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// truncateSidebar shortens s to n runes.
func truncateSidebar(s string, n int) string {
	if n <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}

// countStatuses counts running vs stopped workloads.
func countStatuses(list []core.Workload) (running, stopped int) {
	for _, w := range list {
		switch w.Status {
		case rt.WorkloadStatusRunning, rt.WorkloadStatusUnauthenticated, rt.WorkloadStatusUnhealthy:
			running++
		case rt.WorkloadStatusStopped, rt.WorkloadStatusError, rt.WorkloadStatusStarting,
			rt.WorkloadStatusStopping, rt.WorkloadStatusRemoving, rt.WorkloadStatusUnknown:
			stopped++
		}
	}
	return
}

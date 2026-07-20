// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/stacklok/toolhive/cmd/thv/app/ui"
)

// formatLogLine parses a structured JSON log line (slog format) and returns a
// human-readable, colorized string. Non-JSON lines are returned unchanged.
func formatLogLine(raw string) string {
	raw = strings.TrimRight(raw, "\r\n")
	if len(raw) == 0 || raw[0] != '{' {
		return raw
	}

	var entry map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		return raw
	}

	ts := extractStr(entry, "time")
	if len(ts) >= 19 {
		ts = ts[11:19] // HH:MM:SS
	}
	level := strings.ToUpper(extractStr(entry, "level"))
	msg := extractStr(entry, "msg")

	// Collect remaining fields sorted for stable output.
	skip := map[string]bool{"time": true, "level": true, "msg": true}
	var extras []string
	for k, v := range entry {
		if skip[k] {
			continue
		}
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			extras = append(extras, fmt.Sprintf("%s=%s", k, s))
		} else {
			extras = append(extras, fmt.Sprintf("%s=%s", k, string(v)))
		}
	}
	sort.Strings(extras)

	dim := lipgloss.NewStyle().Foreground(ui.ColorDim)

	// Message and extras color depend on log level.
	msgColor := ui.ColorText
	extrasColor := ui.ColorDim2
	switch level {
	case "ERROR":
		msgColor = ui.ColorRed
		extrasColor = ui.ColorRed
	case "WARN":
		msgColor = ui.ColorYellow
		extrasColor = ui.ColorYellow
	}

	var sb strings.Builder
	sb.WriteString(dim.Render(ts))
	sb.WriteString(" ")
	sb.WriteString(levelStyle(level))
	sb.WriteString("  ")
	sb.WriteString(lipgloss.NewStyle().Foreground(msgColor).Render(msg))
	if len(extras) > 0 {
		sb.WriteString("  ")
		sb.WriteString(lipgloss.NewStyle().Foreground(extrasColor).Render(strings.Join(extras, "  ")))
	}
	return sb.String()
}

func extractStr(m map[string]json.RawMessage, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return string(v)
	}
	return s
}

func levelStyle(level string) string {
	label := fmt.Sprintf("%-5s", level)
	switch level {
	case "ERROR":
		return lipgloss.NewStyle().Foreground(ui.ColorRed).Bold(true).Render(label)
	case "WARN":
		return lipgloss.NewStyle().Foreground(ui.ColorYellow).Render(label)
	case "INFO":
		return lipgloss.NewStyle().Foreground(ui.ColorBlue).Render(label)
	default:
		return lipgloss.NewStyle().Foreground(ui.ColorDim2).Render(label)
	}
}

// buildHScrollContent builds viewport content applying horizontal scroll.
// Each line is ANSI-cut to [hOff, hOff+viewW] so no wrapping occurs.
func buildHScrollContent(lines []string, viewW, hOff int) string {
	if len(lines) == 0 {
		return ""
	}
	if viewW <= 0 || (hOff == 0 && viewW >= 512) {
		return strings.Join(lines, "\n")
	}
	var sb strings.Builder
	for i, line := range lines {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(xansi.Cut(line, hOff, hOff+viewW))
	}
	return sb.String()
}

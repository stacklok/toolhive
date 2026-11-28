// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package ui provides shared styling helpers for the ToolHive CLI.
package ui

import (
	"strings"

	"charm.land/lipgloss/v2"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
)

// Tokyo Night palette
var (
	ColorGreen  = lipgloss.Color("#9ece6a")
	ColorRed    = lipgloss.Color("#f7768e")
	ColorYellow = lipgloss.Color("#e0af68")
	ColorBlue   = lipgloss.Color("#7aa2f7")
	ColorPurple = lipgloss.Color("#bb9af7")
	ColorCyan   = lipgloss.Color("#7dcfff")
	ColorDim    = lipgloss.Color("#4a5070")
	ColorDim2   = lipgloss.Color("#6272a4")
	ColorText   = lipgloss.Color("#c0caf5")
	// ColorBg is the main TUI background — the same dark tone used by the statusbar.
	ColorBg = lipgloss.Color("#1e2030")
)

var (
	dotRunning  = lipgloss.NewStyle().Foreground(ColorGreen).Render("●")
	dotStopped  = lipgloss.NewStyle().Foreground(ColorDim).Render("○")
	dotError    = lipgloss.NewStyle().Foreground(ColorRed).Render("●")
	dotWarning  = lipgloss.NewStyle().Foreground(ColorYellow).Render("●")
	dotStarting = lipgloss.NewStyle().Foreground(ColorBlue).Render("◌")
)

// RenderStatusDot returns a colored bullet for the given WorkloadStatus.
func RenderStatusDot(status rt.WorkloadStatus) string {
	switch status {
	case rt.WorkloadStatusRunning:
		return dotRunning
	case rt.WorkloadStatusStopped:
		return dotStopped
	case rt.WorkloadStatusError:
		return dotError
	case rt.WorkloadStatusStarting:
		return dotStarting
	case rt.WorkloadStatusStopping:
		return dotWarning
	case rt.WorkloadStatusUnhealthy:
		return dotWarning
	case rt.WorkloadStatusUnauthenticated:
		return dotWarning
	case rt.WorkloadStatusAuthRetrying:
		return dotWarning
	case rt.WorkloadStatusRemoving:
		return dotWarning
	case rt.WorkloadStatusPolicyStopped:
		return dotStopped
	case rt.WorkloadStatusUnknown:
		return dotStopped
	}
	return dotStopped
}

// VisibleLen returns the number of visible characters in s, stripping ANSI
// escape sequences and counting multi-byte UTF-8 codepoints as one character.
func VisibleLen(s string) int {
	inEscape := false
	count := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inEscape {
			if c == 'm' {
				inEscape = false
			}
			continue
		}
		if c == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			inEscape = true
			i++ // skip '['
			continue
		}
		// Skip UTF-8 continuation bytes (0x80–0xBF); count only leading bytes.
		if c >= 0x80 && c <= 0xBF {
			continue
		}
		count++
	}
	return count
}

// PadToWidth pads s (which may contain ANSI escapes) so its visible width equals w.
// If s is already wider, it is returned unchanged.
func PadToWidth(s string, w int) string {
	visible := VisibleLen(s)
	if visible >= w {
		return s
	}
	return s + strings.Repeat(" ", w-visible)
}

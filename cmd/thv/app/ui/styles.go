// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package ui provides shared styling helpers for the ToolHive CLI.
package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

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

// Background shades for status pills.
var (
	bgRunning  = lipgloss.Color("#1a3320")
	bgStopped  = lipgloss.Color("#1e2030")
	bgError    = lipgloss.Color("#3d1a1e")
	bgStarting = lipgloss.Color("#2e2400")
	bgWarning  = lipgloss.Color("#2e2400")
)

var (
	dotRunning  = lipgloss.NewStyle().Foreground(ColorGreen).Render("●")
	dotStopped  = lipgloss.NewStyle().Foreground(ColorDim).Render("○")
	dotError    = lipgloss.NewStyle().Foreground(ColorRed).Render("●")
	dotWarning  = lipgloss.NewStyle().Foreground(ColorYellow).Render("●")
	dotStarting = lipgloss.NewStyle().Foreground(ColorBlue).Render("◌")

	pillRunning = lipgloss.NewStyle().
			Background(bgRunning).Foreground(ColorGreen).
			Padding(0, 1).Render("● running")
	pillStopped = lipgloss.NewStyle().
			Background(bgStopped).Foreground(ColorDim2).
			Padding(0, 1).Render("● stopped")
	pillError = lipgloss.NewStyle().
			Background(bgError).Foreground(ColorRed).
			Padding(0, 1).Render("● error")
	pillStarting = lipgloss.NewStyle().
			Background(bgStarting).Foreground(ColorYellow).
			Padding(0, 1).Render("◌ starting")
	pillStopping = lipgloss.NewStyle().
			Background(bgWarning).Foreground(ColorYellow).
			Padding(0, 1).Render("◌ stopping")
	pillUnhealthy = lipgloss.NewStyle().
			Background(bgWarning).Foreground(ColorYellow).
			Padding(0, 1).Render("● unhealthy")
	pillRemoving = lipgloss.NewStyle().
			Background(bgWarning).Foreground(ColorYellow).
			Padding(0, 1).Render("◌ removing")
	pillUnknown = lipgloss.NewStyle().
			Background(bgStopped).Foreground(ColorDim).
			Padding(0, 1).Render("○ unknown")
	pillUnauthed = lipgloss.NewStyle().
			Background(bgWarning).Foreground(ColorYellow).
			Padding(0, 1).Render("⚠ unauthed")

	keyStyle  = lipgloss.NewStyle().Foreground(ColorDim2)
	portStyle = lipgloss.NewStyle().Foreground(ColorCyan).Bold(true)
	dimStyle  = lipgloss.NewStyle().Foreground(ColorDim)
)

// PillWidth is the fixed visible width of a status pill (for column alignment).
const PillWidth = 13 // "● unhealthy" + 2 padding = longest

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
	case rt.WorkloadStatusRemoving:
		return dotWarning
	case rt.WorkloadStatusPolicyStopped:
		return dotStopped
	case rt.WorkloadStatusUnknown:
		return dotStopped
	}
	return dotStopped
}

// RenderStatusPill returns a badge with background color for the given status.
func RenderStatusPill(status rt.WorkloadStatus) string {
	switch status {
	case rt.WorkloadStatusRunning:
		return pillRunning
	case rt.WorkloadStatusStopped:
		return pillStopped
	case rt.WorkloadStatusError:
		return pillError
	case rt.WorkloadStatusStarting:
		return pillStarting
	case rt.WorkloadStatusStopping:
		return pillStopping
	case rt.WorkloadStatusUnhealthy:
		return pillUnhealthy
	case rt.WorkloadStatusRemoving:
		return pillRemoving
	case rt.WorkloadStatusUnknown:
		return pillUnknown
	case rt.WorkloadStatusUnauthenticated:
		return pillUnauthed
	case rt.WorkloadStatusPolicyStopped:
		return pillStopped
	default:
		return pillUnknown
	}
}

// RenderGroupChip returns a bordered group name tag.
func RenderGroupChip(group string) string {
	if group == "" {
		return dimStyle.Render("—")
	}
	text := lipgloss.NewStyle().Foreground(ColorDim2).Render(group)
	lbracket := lipgloss.NewStyle().Foreground(ColorDim).Render("[")
	rbracket := lipgloss.NewStyle().Foreground(ColorDim).Render("]")
	return lbracket + text + rbracket
}

// RenderKey returns a dim-styled label for key-value displays.
func RenderKey(key string) string {
	return keyStyle.Render(key)
}

// RenderPort returns a bold cyan port number string.
func RenderPort(port string) string {
	return portStyle.Render(port)
}

// RenderDim returns a dim-styled string.
func RenderDim(s string) string {
	return dimStyle.Render(s)
}

// RenderText returns a text-colored string.
func RenderText(s string) string {
	return lipgloss.NewStyle().Foreground(ColorText).Render(s)
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

// RenderServerTypeBadge returns a styled badge for container vs remote server type.
func RenderServerTypeBadge(isRemote bool) string {
	if isRemote {
		return lipgloss.NewStyle().
			Background(lipgloss.Color("#1a1040")).
			Foreground(ColorPurple).
			Padding(0, 1).
			Render("remote")
	}
	return lipgloss.NewStyle().
		Background(lipgloss.Color("#0d1a3a")).
		Foreground(ColorBlue).
		Padding(0, 1).
		Render("container")
}

// RenderTierBadge returns a styled badge for the registry tier.
func RenderTierBadge(tier string) string {
	switch strings.ToLower(tier) {
	case "official":
		return lipgloss.NewStyle().
			Background(lipgloss.Color("#2e2400")).
			Foreground(ColorYellow).
			Padding(0, 1).
			Render("official")
	case "community":
		return lipgloss.NewStyle().
			Background(lipgloss.Color("#1e2030")).
			Foreground(ColorDim2).
			Padding(0, 1).
			Render("community")
	case "deprecated":
		return lipgloss.NewStyle().
			Background(bgError).
			Foreground(ColorRed).
			Padding(0, 1).
			Render("deprecated")
	default:
		return lipgloss.NewStyle().
			Foreground(ColorDim).
			Render(tier)
	}
}

// RenderStars returns a yellow star count string.
func RenderStars(n int) string {
	if n == 0 {
		return lipgloss.NewStyle().Foreground(ColorDim).Render("—")
	}
	return lipgloss.NewStyle().Foreground(ColorYellow).Render(fmt.Sprintf("★ %d", n))
}

// RenderLogLine colorizes a log line based on detected severity level.
func RenderLogLine(line string) string {
	upper := strings.ToUpper(line)
	switch {
	case containsLevel(upper, "ERROR", "FATAL", "CRIT"):
		return lipgloss.NewStyle().Foreground(ColorRed).Render(line)
	case containsLevel(upper, "WARN", "WARNING"):
		return lipgloss.NewStyle().Foreground(ColorYellow).Render(line)
	case containsLevel(upper, "DEBUG", "TRACE"):
		return lipgloss.NewStyle().Foreground(ColorDim2).Render(line)
	case containsLevel(upper, "INFO"):
		return lipgloss.NewStyle().Foreground(ColorText).Render(line)
	default:
		return lipgloss.NewStyle().Foreground(ColorDim2).Render(line)
	}
}

// containsLevel checks whether the line contains one of the given level tokens.
func containsLevel(upper string, levels ...string) bool {
	for _, lvl := range levels {
		// Match common patterns: level=INFO, [INFO], INFO:, INFO space
		if strings.Contains(upper, "LEVEL="+lvl) ||
			strings.Contains(upper, "["+lvl+"]") ||
			strings.Contains(upper, lvl+":") ||
			strings.Contains(upper, " "+lvl+" ") ||
			strings.HasPrefix(upper, lvl+" ") {
			return true
		}
	}
	return false
}

// RenderSection renders a section heading (e.g. "Permissions").
func RenderSection(title string) string {
	return "\n" + lipgloss.NewStyle().Foreground(ColorPurple).Bold(true).Render(title)
}

// PadLeftToWidth right-aligns s within width w by prepending spaces.
// If s is already wider, it is returned unchanged.
func PadLeftToWidth(s string, w int) string {
	visible := VisibleLen(s)
	if visible >= w {
		return s
	}
	return strings.Repeat(" ", w-visible) + s
}

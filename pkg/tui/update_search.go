// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/stacklok/toolhive/cmd/thv/app/ui"
)

// searchParams groups the mutable search state and associated viewport data
// needed by the shared search helpers. Callers construct this with pointers to
// the relevant Model fields so the helpers can read and write them in place.
type searchParams struct {
	active  *bool
	query   *string
	matches *[]int
	idx     *int
	lines   []string
	vp      *viewport.Model
	hOff    int
}

// handleSearchKey is the shared key handler for both log and proxy-log search.
func handleSearchKey(msg tea.KeyMsg, p searchParams) tea.Cmd {
	switch {
	case key.Matches(msg, keys.Escape):
		// Esc clears the search entirely and restores normal log content.
		*p.active = false
		*p.query = ""
		*p.matches = nil
		*p.idx = 0
		p.vp.SetContent(buildHScrollContent(p.lines, p.vp.Width, p.hOff))
	case key.Matches(msg, keys.Enter):
		// Enter closes the prompt but keeps highlights and the current match.
		*p.active = false
	case key.Matches(msg, keys.SearchNext):
		if len(*p.matches) > 0 {
			*p.idx = (*p.idx + 1) % len(*p.matches)
			scrollToSearchMatch(p)
		}
	case key.Matches(msg, keys.SearchPrev):
		if len(*p.matches) > 0 {
			*p.idx = (*p.idx - 1 + len(*p.matches)) % len(*p.matches)
			scrollToSearchMatch(p)
		}
	case msg.Type == tea.KeyBackspace:
		if len(*p.query) > 0 {
			// Remove last rune (not last byte) to handle multi-byte UTF-8.
			r := []rune(*p.query)
			*p.query = string(r[:len(r)-1])
			rebuildSearch(p)
		}
	default:
		if msg.Type == tea.KeyRunes {
			*p.query += msg.String()
			rebuildSearch(p)
		}
	}
	return nil
}

// rebuildSearch recalculates which lines match the current query and
// refreshes the viewport content with highlights.
func rebuildSearch(p searchParams) {
	*p.matches = nil
	*p.idx = 0
	if *p.query == "" {
		p.vp.SetContent(buildHScrollContent(p.lines, p.vp.Width, p.hOff))
		return
	}
	lq := strings.ToLower(*p.query)
	for i, line := range p.lines {
		if strings.Contains(strings.ToLower(line), lq) {
			*p.matches = append(*p.matches, i)
		}
	}
	// Clamp current index.
	if *p.idx >= len(*p.matches) {
		*p.idx = 0
	}
	scrollToSearchMatch(p)
}

// scrollToSearchMatch updates the viewport content with highlights and scrolls
// to the current match.
func scrollToSearchMatch(p searchParams) {
	if len(*p.matches) == 0 {
		// Re-render without highlights when there are no matches.
		if *p.query != "" {
			p.vp.SetContent(buildHighlightedLogContent(p.lines, *p.query, nil, 0, p.vp.Width, p.hOff))
		}
		return
	}
	p.vp.SetContent(buildHighlightedLogContent(p.lines, *p.query, *p.matches, *p.idx, p.vp.Width, p.hOff))
	// Scroll the viewport so the current match line is visible.
	matchLine := (*p.matches)[*p.idx]
	p.vp.SetYOffset(matchLine)
}

// logSearchParams builds searchParams for the main log panel.
func (m *Model) logSearchParams() searchParams {
	return searchParams{
		active:  &m.logSearchActive,
		query:   &m.logSearchQuery,
		matches: &m.logSearchMatches,
		idx:     &m.logSearchIdx,
		lines:   m.logLines,
		vp:      &m.logView,
		hOff:    m.logHScrollOff,
	}
}

// proxyLogSearchParams builds searchParams for the proxy log panel.
func (m *Model) proxyLogSearchParams() searchParams {
	return searchParams{
		active:  &m.proxyLogSearchActive,
		query:   &m.proxyLogSearchQuery,
		matches: &m.proxyLogSearchMatches,
		idx:     &m.proxyLogSearchIdx,
		lines:   m.proxyLogLines,
		vp:      &m.proxyLogView,
		hOff:    m.proxyLogHScrollOff,
	}
}

// handleLogSearchKey handles key input while the log search prompt is open.
func (m *Model) handleLogSearchKey(msg tea.KeyMsg) tea.Cmd {
	return handleSearchKey(msg, m.logSearchParams())
}

// rebuildLogSearch recalculates which log lines match the current query
// and refreshes the viewport content with highlights.
func (m *Model) rebuildLogSearch() {
	rebuildSearch(m.logSearchParams())
}

// scrollToMatch updates the viewport content with highlights and scrolls to the current match.
func (m *Model) scrollToMatch() {
	scrollToSearchMatch(m.logSearchParams())
}

// handleProxyLogSearchKey processes key events when proxy log search is active.
func (m *Model) handleProxyLogSearchKey(msg tea.KeyMsg) tea.Cmd {
	return handleSearchKey(msg, m.proxyLogSearchParams())
}

// rebuildProxyLogSearch recomputes proxyLogSearchMatches for the current query and refreshes the viewport.
func (m *Model) rebuildProxyLogSearch() {
	rebuildSearch(m.proxyLogSearchParams())
}

// scrollToProxyMatch updates the proxy log viewport with highlights and scrolls to the current match.
func (m *Model) scrollToProxyMatch() {
	scrollToSearchMatch(m.proxyLogSearchParams())
}

// buildHighlightedLogContent builds viewport content like buildHScrollContent but also
// highlights the search query within matching lines. The current focused match
// is highlighted with green; other matches with yellow.
func buildHighlightedLogContent(lines []string, query string, matches []int, currentMatchIdx int, viewW, hOff int) string {
	if len(lines) == 0 {
		return ""
	}
	if query == "" {
		return buildHScrollContent(lines, viewW, hOff)
	}

	// Build a set for fast match lookup.
	matchSet := make(map[int]bool, len(matches))
	for _, idx := range matches {
		matchSet[idx] = true
	}
	var currentMatchLine int
	if len(matches) > 0 && currentMatchIdx < len(matches) {
		currentMatchLine = matches[currentMatchIdx]
	}

	lowerQuery := strings.ToLower(query)

	var sb strings.Builder
	for i, line := range lines {
		if i > 0 {
			sb.WriteByte('\n')
		}

		if !matchSet[i] {
			// Non-matching line: apply only h-scroll.
			if viewW > 0 {
				xansiLine := xansiCutLine(line, hOff, viewW)
				sb.WriteString(xansiLine)
			} else {
				sb.WriteString(line)
			}
			continue
		}

		// Matching line: inject highlights then h-scroll.
		highlightBg := ui.ColorYellow
		if i == currentMatchLine {
			highlightBg = ui.ColorGreen
		}
		highlighted := highlightSubstring(line, query, lowerQuery, highlightBg)

		if viewW > 0 {
			sb.WriteString(xansiCutLine(highlighted, hOff, viewW))
		} else {
			sb.WriteString(highlighted)
		}
	}
	return sb.String()
}

// highlightSubstring wraps all case-insensitive occurrences of query within line
// with a lipgloss background color. It operates on rune indices so that
// multi-byte UTF-8 characters and Unicode case mappings are handled correctly.
func highlightSubstring(line, query, lowerQuery string, bg lipgloss.Color) string {
	if query == "" {
		return line
	}
	lineRunes := []rune(line)
	lowerLineRunes := []rune(strings.ToLower(line))
	queryRunes := []rune(lowerQuery)
	qLen := len(queryRunes)
	hlStyle := lipgloss.NewStyle().Background(bg).Foreground(ui.ColorBg)

	var sb strings.Builder
	pos := 0
	for pos <= len(lowerLineRunes)-qLen {
		idx := runesIndex(lowerLineRunes[pos:], queryRunes)
		if idx < 0 {
			break
		}
		abs := pos + idx
		sb.WriteString(string(lineRunes[pos:abs]))
		sb.WriteString(hlStyle.Render(string(lineRunes[abs : abs+qLen])))
		pos = abs + qLen
	}
	sb.WriteString(string(lineRunes[pos:]))
	return sb.String()
}

// runesIndex returns the rune index of the first occurrence of sub in s, or -1.
func runesIndex(s, sub []rune) int {
	if len(sub) == 0 {
		return 0
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		match := true
		for j := range sub {
			if s[i+j] != sub[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// xansiCutLine applies ANSI-aware horizontal slicing to a single line.
// It is a thin wrapper around buildHScrollContent for a single line.
func xansiCutLine(line string, hOff, viewW int) string {
	result := buildHScrollContent([]string{line}, viewW, hOff)
	return result
}

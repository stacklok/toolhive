// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	mcpclient "github.com/mark3labs/mcp-go/client"

	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/runner"
	types "github.com/stacklok/toolhive/pkg/transport/types"
)

// handleConfirmDeleteKey handles key input while waiting for delete confirmation.
func (m *Model) handleConfirmDeleteKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, keys.Delete):
		m.confirmDelete = false
		return m.doDelete()
	default:
		m.confirmDelete = false
	}
	return nil
}

// handleFilterKey handles key input while the filter prompt is active.
func (m *Model) handleFilterKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, keys.Escape) || key.Matches(msg, keys.Quit):
		m.filterActive = false
		m.filterQuery = ""
		m.selectedIdx = 0
	case key.Matches(msg, keys.Enter):
		m.filterActive = false
	case msg.Type == tea.KeyBackspace:
		if len(m.filterQuery) > 0 {
			r := []rune(m.filterQuery)
			m.filterQuery = string(r[:len(r)-1])
		}
	default:
		if msg.Type == tea.KeyRunes {
			m.filterQuery += msg.String()
		}
	}
	return nil
}

// handleNormalKey handles key input in normal (non-filter) mode.
//
//nolint:gocyclo // key-handler switch; complexity is inherent to dispatching over all normal-mode key bindings
func (m *Model) handleNormalKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, keys.Quit):
		if m.mcpClient != nil {
			_ = m.mcpClient.Close()
			m.mcpClient = nil
		}
		if m.logCtxCancel != nil {
			m.logCtxCancel()
			m.logCtxCancel = nil
		}
		if m.proxyLogCancel != nil {
			m.proxyLogCancel()
			m.proxyLogCancel = nil
		}
		m.quitting = true
		return tea.Quit

	case key.Matches(msg, keys.Up):
		if m.panel == panelTools && len(m.tools) > 0 {
			return m.toolsNavigateUp()
		}
		return m.navigateUp()

	case key.Matches(msg, keys.Down):
		if m.panel == panelTools && len(m.tools) > 0 {
			return m.toolsNavigateDown()
		}
		return m.navigateDown()

	case key.Matches(msg, keys.Enter):
		if m.panel == panelTools && len(m.tools) > 0 {
			return m.toolsJumpToInspector()
		}

	case key.Matches(msg, keys.Tab):
		return m.togglePanel()

	case key.Matches(msg, keys.Follow):
		m.toggleFollow()

	case key.Matches(msg, keys.Stop):
		return m.doStop()

	case key.Matches(msg, keys.Restart):
		return m.doRestart()

	case key.Matches(msg, keys.Delete):
		if sel := m.selected(); sel != nil {
			m.confirmDelete = true
		}

	case key.Matches(msg, keys.Filter):
		if m.panel == panelLogs {
			m.logSearchActive = true
			return nil
		}
		if m.panel == panelProxyLogs {
			m.proxyLogSearchActive = true
			return nil
		}
		m.filterActive = true
		m.filterQuery = ""

	case key.Matches(msg, keys.Help):
		m.showHelp = true

	case key.Matches(msg, keys.Registry):
		return m.openRegistry()

	case key.Matches(msg, keys.Escape):
		if m.panel == panelLogs && m.logSearchQuery != "" {
			m.logSearchQuery = ""
			m.logSearchMatches = nil
			m.logSearchIdx = 0
			m.logView.SetContent(buildHScrollContent(m.logLines, m.logView.Width, m.logHScrollOff))
		}
		if m.panel == panelProxyLogs && m.proxyLogSearchQuery != "" {
			m.proxyLogSearchQuery = ""
			m.proxyLogSearchMatches = nil
			m.proxyLogSearchIdx = 0
			m.proxyLogView.SetContent(buildHScrollContent(m.proxyLogLines, m.proxyLogView.Width, m.proxyLogHScrollOff))
		}

	case key.Matches(msg, keys.SearchNext):
		if m.panel == panelLogs && len(m.logSearchMatches) > 0 {
			m.logSearchIdx = (m.logSearchIdx + 1) % len(m.logSearchMatches)
			m.scrollToMatch()
		}
		if m.panel == panelProxyLogs && len(m.proxyLogSearchMatches) > 0 {
			m.proxyLogSearchIdx = (m.proxyLogSearchIdx + 1) % len(m.proxyLogSearchMatches)
			m.scrollToProxyMatch()
		}

	case key.Matches(msg, keys.SearchPrev):
		if m.panel == panelLogs && len(m.logSearchMatches) > 0 {
			m.logSearchIdx = (m.logSearchIdx - 1 + len(m.logSearchMatches)) % len(m.logSearchMatches)
			m.scrollToMatch()
		}
		if m.panel == panelProxyLogs && len(m.proxyLogSearchMatches) > 0 {
			m.proxyLogSearchIdx = (m.proxyLogSearchIdx - 1 + len(m.proxyLogSearchMatches)) % len(m.proxyLogSearchMatches)
			m.scrollToProxyMatch()
		}

	case key.Matches(msg, keys.ScrollLeft):
		m.hScrollLeft()

	case key.Matches(msg, keys.ScrollRight):
		m.hScrollRight()

	case key.Matches(msg, keys.CopyURL):
		if sel := m.selected(); sel != nil && sel.URL != "" {
			if err := clipboard.WriteAll(sel.URL); err != nil {
				return m.showNotif("clipboard: "+err.Error(), false)
			}
			return m.showNotif("✓ URL copied", true)
		}
	}

	return nil
}

// toolsNavigateUp moves the tool selection up and refreshes the viewport.
func (m *Model) toolsNavigateUp() tea.Cmd {
	if m.toolsSelectedIdx > 0 {
		m.toolsSelectedIdx--
		m.toolsView.SetContent(buildToolsContent(m.tools, m.toolsView.Width, m.toolsSelectedIdx))
		m.toolsScrollToSelected()
	}
	return nil
}

// toolsNavigateDown moves the tool selection down and refreshes the viewport.
func (m *Model) toolsNavigateDown() tea.Cmd {
	if m.toolsSelectedIdx < len(m.tools)-1 {
		m.toolsSelectedIdx++
		m.toolsView.SetContent(buildToolsContent(m.tools, m.toolsView.Width, m.toolsSelectedIdx))
		m.toolsScrollToSelected()
	}
	return nil
}

// toolsScrollToSelected adjusts the viewport so the selected tool stays visible.
func (m *Model) toolsScrollToSelected() {
	// Each tool occupies approximately 1-3 lines; use a rough line-per-tool estimate.
	// The header is 2 lines (count + blank line).
	const headerLines = 2
	line := headerLines + m.toolsSelectedIdx
	if line < m.toolsView.YOffset {
		m.toolsView.SetYOffset(line)
	} else if line >= m.toolsView.YOffset+m.toolsView.Height {
		m.toolsView.SetYOffset(line - m.toolsView.Height + 1)
	}
}

// toolsJumpToInspector switches to the Inspector panel with the currently
// selected tool pre-selected and the form ready to fill.
func (m *Model) toolsJumpToInspector() tea.Cmd {
	// Find the matching index in the inspector tool list (same m.tools slice).
	m.insp.toolIdx = m.toolsSelectedIdx
	if m.insp.toolIdx >= len(m.tools) {
		m.insp.toolIdx = 0
	}
	m.panel = panelInspector
	m.inspRebuildForm()
	// Focus the first field if available.
	if len(m.insp.fields) > 0 {
		m.insp.fieldIdx = 0
		m.insp.fields[0].input.Focus()
	}
	return m.maybeStartToolsFetch()
}

func (m *Model) navigateUp() tea.Cmd {
	if m.selectedIdx > 0 {
		m.selectedIdx--
		return m.onSelectionChanged()
	}
	return nil
}

func (m *Model) navigateDown() tea.Cmd {
	list := m.filteredWorkloads()
	if m.selectedIdx < len(list)-1 {
		m.selectedIdx++
		return m.onSelectionChanged()
	}
	return nil
}

// hScrollLeft scrolls the active log panel left by 8 columns.
func (m *Model) hScrollLeft() {
	const step = 8
	switch m.panel {
	case panelLogs:
		if m.logHScrollOff > 0 {
			m.logHScrollOff -= step
			if m.logHScrollOff < 0 {
				m.logHScrollOff = 0
			}
			m.logView.SetContent(buildHScrollContent(m.logLines, m.logView.Width, m.logHScrollOff))
		}
	case panelProxyLogs:
		if m.proxyLogHScrollOff > 0 {
			m.proxyLogHScrollOff -= step
			if m.proxyLogHScrollOff < 0 {
				m.proxyLogHScrollOff = 0
			}
			m.proxyLogView.SetContent(buildHScrollContent(m.proxyLogLines, m.proxyLogView.Width, m.proxyLogHScrollOff))
		}
	case panelInfo, panelTools, panelInspector:
		// h-scroll not applicable to these panels
	}
}

// hScrollRight scrolls the active log panel right by 8 columns.
func (m *Model) hScrollRight() {
	const step = 8
	switch m.panel {
	case panelLogs:
		maxOff := maxLineLen(m.logLines)
		if m.logHScrollOff+step <= maxOff {
			m.logHScrollOff += step
			m.logView.SetContent(buildHScrollContent(m.logLines, m.logView.Width, m.logHScrollOff))
		}
	case panelProxyLogs:
		maxOff := maxLineLen(m.proxyLogLines)
		if m.proxyLogHScrollOff+step <= maxOff {
			m.proxyLogHScrollOff += step
			m.proxyLogView.SetContent(buildHScrollContent(m.proxyLogLines, m.proxyLogView.Width, m.proxyLogHScrollOff))
		}
	case panelInfo, panelTools, panelInspector:
		// h-scroll not applicable to these panels
	}
}

// maxLineLen returns the length (in runes) of the longest line in the slice.
func maxLineLen(lines []string) int {
	m := 0
	for _, l := range lines {
		if n := len([]rune(l)); n > m {
			m = n
		}
	}
	return m
}

// onSelectionChanged resets panel state and starts any needed background fetches.
func (m *Model) onSelectionChanged() tea.Cmd {
	// Close the previous workload's MCP client.
	if m.mcpClient != nil {
		_ = m.mcpClient.Close()
		m.mcpClient = nil
	}

	m.toolsFor = ""        // invalidate tools cache
	m.toolsSelectedIdx = 0 // reset tool selection
	m.runConfigFor = ""    // invalidate runConfig cache
	m.runConfig = nil
	m.logHScrollOff = 0
	m.proxyLogHScrollOff = 0

	// Reset inspector state on selection change.
	m.insp.toolIdx = 0
	m.insp.fields = nil
	m.insp.fieldIdx = -1
	m.insp.result = ""

	// Cancel proxy log stream for old selection.
	if m.proxyLogCancel != nil {
		m.proxyLogCancel()
		m.proxyLogCancel = nil
		m.proxyLogLines = nil
		m.proxyLogView.SetContent("")
		m.proxyLogFor = ""
	}

	cmds := []tea.Cmd{m.startLogStream()}
	switch m.panel {
	case panelTools:
		cmds = append(cmds, m.maybeStartToolsFetch())
	case panelInfo:
		cmds = append(cmds, m.maybeLoadRunConfig())
	case panelProxyLogs:
		cmds = append(cmds, m.startProxyLogStream())
	case panelInspector:
		cmds = append(cmds, m.maybeStartToolsFetch())
	case panelLogs:
		// log stream already started above
	}
	return tea.Batch(cmds...)
}

func (m *Model) togglePanel() tea.Cmd {
	switch m.panel {
	case panelLogs:
		m.panel = panelInfo
		return m.maybeLoadRunConfig()
	case panelInfo:
		m.panel = panelTools
		return m.maybeStartToolsFetch()
	case panelTools:
		m.panel = panelProxyLogs
		return m.startProxyLogStream()
	case panelProxyLogs:
		// Stop proxy log stream when leaving the panel.
		if m.proxyLogCancel != nil {
			m.proxyLogCancel()
			m.proxyLogCancel = nil
		}
		m.panel = panelInspector
		m.inspRebuildForm()
		return m.maybeStartToolsFetch()
	case panelInspector:
		m.blurAllInspFields()
		m.panel = panelLogs
	}
	return nil
}

// maybeStartToolsFetch fetches tools for the selected workload if not already loaded.
func (m *Model) maybeStartToolsFetch() tea.Cmd {
	sel := m.selected()
	if sel == nil {
		return nil
	}
	// STDIO servers only support a single initialize handshake; calling it again
	// from the TUI would interfere with the real client connection.
	if sel.TransportType == types.TransportTypeStdio {
		m.toolsFor = sel.Name
		m.toolsLoading = false
		m.tools = nil
		m.toolsErr = errStdioToolsNotAvailable
		return nil
	}
	// Retry if previously failed; skip only when successfully loaded.
	if m.toolsFor == sel.Name && !m.toolsLoading && m.toolsErr == nil {
		return nil // already loaded successfully
	}
	m.toolsFor = sel.Name
	m.toolsLoading = true
	m.tools = nil
	m.toolsErr = nil

	// Connect the MCP client asynchronously if not already present.
	if m.mcpClient == nil {
		return startMCPClientConnect(m.ctx, sel)
	}
	return startToolsFetch(m.ctx, m.mcpClient, sel)
}

// maybeLoadRunConfig loads the RunConfig for the selected workload if not already loaded.
func (m *Model) maybeLoadRunConfig() tea.Cmd {
	sel := m.selected()
	if sel == nil {
		return nil
	}
	if m.runConfigFor == sel.Name && m.runConfig != nil {
		return nil // already loaded
	}
	m.runConfigFor = sel.Name
	m.runConfig = nil
	name := sel.Name
	ctx := m.ctx
	return func() tea.Msg {
		cfg, err := runner.LoadState(ctx, name)
		if err != nil {
			return runConfigLoadedMsg{workloadName: name, cfg: nil, err: err}
		}
		return runConfigLoadedMsg{workloadName: name, cfg: cfg}
	}
}

// startToolsFetch returns a tea.Cmd that fetches tools for a workload via an MCP client.
func startToolsFetch(ctx context.Context, c *mcpclient.Client, w *core.Workload) tea.Cmd {
	name := w.Name
	return func() tea.Msg {
		tools, err := fetchTools(ctx, c)
		return toolsFetchedMsg{workloadName: name, tools: tools, err: err}
	}
}

func (m *Model) toggleFollow() {
	m.logFollow = !m.logFollow
	if m.logFollow {
		m.logView.GotoBottom()
	}
}

func (m *Model) doStop() tea.Cmd {
	if sel := m.selected(); sel != nil {
		return stopWorkload(m.ctx, m.manager, sel.Name)
	}
	return nil
}

func (m *Model) doRestart() tea.Cmd {
	if sel := m.selected(); sel != nil {
		return restartWorkload(m.ctx, m.manager, sel.Name)
	}
	return nil
}

func (m *Model) doDelete() tea.Cmd {
	if sel := m.selected(); sel != nil {
		return deleteWorkload(m.ctx, m.manager, sel.Name)
	}
	return nil
}

// openRegistry opens the registry overlay and triggers a fetch if needed.
func (m *Model) openRegistry() tea.Cmd {
	m.registry.open = true
	m.registry.filter = ""
	m.registry.idx = 0
	if len(m.registry.items) > 0 {
		return nil // already loaded
	}
	m.registry.loading = true
	m.registry.err = nil
	return fetchRegistryItems(m.ctx)
}

// refreshWorkloads returns a tea.Cmd that fetches the workload list.
func (m *Model) refreshWorkloads() tea.Cmd {
	return func() tea.Msg {
		list, err := m.manager.ListWorkloads(m.ctx, true)
		if err != nil {
			return nil
		}
		return workloadsRefreshMsg{workloads: list}
	}
}

// resizeViewport recalculates the viewport dimensions based on the terminal size.
func (m *Model) resizeViewport() {
	sidebarWidth := sidebarW(m.width)
	mainWidth := m.width - sidebarWidth - 1 // 1 for the divider
	// mainStyle Height = m.height-2; title(1)+tabBar(1)+sep(1)+toolbar(1) = 4 overhead
	logHeight := max(m.height-6, 1)
	m.logView.Width = mainWidth
	m.logView.Height = logHeight
	m.proxyLogView.Width = mainWidth
	m.proxyLogView.Height = logHeight
	// Tools viewport: same height as logs, rebuild content to reflect new width.
	if m.toolsView.Width != mainWidth || m.toolsView.Height != logHeight {
		m.toolsView.Width = mainWidth
		m.toolsView.Height = logHeight
		if len(m.tools) > 0 {
			m.toolsView.SetContent(buildToolsContent(m.tools, mainWidth, m.toolsSelectedIdx))
		}
	}
	// Inspector response viewport: right-column minus headers (~8 lines) and log section (6 lines).
	const inspLogHeight = 6
	// inspH = m.height - 5 (from renderInspector); 8 lines of REQUEST/RESPONSE headers overhead.
	const inspHeaderOverhead = 8
	m.insp.logView.Width = mainWidth
	m.insp.logView.Height = inspLogHeight
	m.insp.respView.Width = mainWidth
	m.insp.respView.Height = max(m.height-10-inspLogHeight, 3)
	m.insp.treeVisH = max(m.height-5-inspHeaderOverhead, 3)
}

// sidebarW returns the sidebar width given total terminal width.
func sidebarW(totalWidth int) int {
	w := totalWidth / 4
	if w < 24 {
		return 24
	}
	if w > 40 {
		return 40
	}
	return w
}

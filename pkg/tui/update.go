// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// workloadsRefreshMsg is sent when the workload list is refreshed.
type workloadsRefreshMsg struct {
	workloads []core.Workload
}

// notifClearMsg is sent after the notification auto-dismiss timer fires.
type notifClearMsg struct{}

// showNotif sets a transient notification and schedules its auto-clear after 3s.
func (m *Model) showNotif(msg string, ok bool) tea.Cmd {
	m.notifMsg = msg
	m.notifOK = ok
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return notifClearMsg{} })
}

// tuiLogMsg carries a captured slog message for display in the inspector.
type tuiLogMsg string

// logLineMsg carries a single new log line from the streaming goroutine.
type logLineMsg string

// logStreamDoneMsg is sent when the log stream channel is closed.
type logStreamDoneMsg struct{}

// tickMsg is sent by the periodic workload refresh ticker.
type tickMsg time.Time

// toolsFetchedMsg is sent when the tools list is loaded from an MCP server.
type toolsFetchedMsg struct {
	workloadName string
	tools        []vmcp.Tool
	err          error
}

// runConfigLoadedMsg is sent when the RunConfig is loaded for a workload.
type runConfigLoadedMsg struct {
	workloadName string
	cfg          *runner.RunConfig
	err          error
}

const refreshInterval = 5 * time.Second

// Init starts background ticks for workload refresh.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		scheduleRefresh(),
		m.startLogStream(),
		m.watchTUILog(),
	)
}

// watchTUILog returns a command that waits for the next slog message on tuiLogCh.
func (m *Model) watchTUILog() tea.Cmd {
	if m.tuiLogCh == nil {
		return nil
	}
	ch := m.tuiLogCh
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return tuiLogMsg(msg)
	}
}

func scheduleRefresh() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// startLogStream begins streaming logs for the currently selected workload.
func (m *Model) startLogStream() tea.Cmd {
	sel := m.selected()
	if sel == nil {
		return nil
	}

	// Cancel any existing stream.
	if m.logCtxCancel != nil {
		m.logCtxCancel()
	}
	m.logLines = nil
	m.logView.SetContent("")

	ctx, cancel := context.WithCancel(m.ctx)
	m.logCtxCancel = cancel
	m.streamingFor = sel.Name
	m.logCh = StreamWorkloadLogs(ctx, m.manager, sel.Name)

	return readLogLine(m.logCh)
}

// readLogLine returns a tea.Cmd that waits for the next log line.
func readLogLine(ch <-chan string) tea.Cmd {
	return func() tea.Msg {
		line, ok := <-ch
		if !ok {
			return logStreamDoneMsg{}
		}
		return logLineMsg(line)
	}
}

// startProxyLogStream begins streaming proxy logs for the currently selected workload.
func (m *Model) startProxyLogStream() tea.Cmd {
	sel := m.selected()
	if sel == nil {
		return nil
	}

	if m.proxyLogCancel != nil {
		m.proxyLogCancel()
	}
	m.proxyLogLines = nil
	m.proxyLogView.SetContent("")

	ctx, cancel := context.WithCancel(m.ctx)
	m.proxyLogCancel = cancel
	m.proxyLogFor = sel.Name
	m.proxyLogCh = StreamProxyLogs(ctx, m.manager, sel.Name)

	return readProxyLogLine(m.proxyLogCh)
}

// Update handles all incoming messages and key events.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	if cmd, done := m.handleMsg(msg); done {
		return m, cmd
	} else if cmd != nil {
		cmds = append(cmds, cmd)
	}

	// Forward scroll events to the active viewport.
	switch m.panel {
	case panelLogs:
		var vpCmd tea.Cmd
		m.logView, vpCmd = m.logView.Update(msg)
		if vpCmd != nil {
			cmds = append(cmds, vpCmd)
		}
	case panelProxyLogs:
		var vpCmd tea.Cmd
		m.proxyLogView, vpCmd = m.proxyLogView.Update(msg)
		if vpCmd != nil {
			cmds = append(cmds, vpCmd)
		}
	case panelInspector:
		var vpCmd tea.Cmd
		m.insp.respView, vpCmd = m.insp.respView.Update(msg)
		if vpCmd != nil {
			cmds = append(cmds, vpCmd)
		}
	case panelTools:
		var vpCmd tea.Cmd
		m.toolsView, vpCmd = m.toolsView.Update(msg)
		if vpCmd != nil {
			cmds = append(cmds, vpCmd)
		}
	case panelInfo:
		// no viewport to forward scroll to
	}

	return m, tea.Batch(cmds...)
}

// handleMsg dispatches a message and returns (cmd, earlyReturn).
// earlyReturn=true means Update should return immediately with cmd.
func (m *Model) handleMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeViewport()
		return nil, true
	case tea.KeyMsg:
		return m.handleKey(msg), false
	case tickMsg:
		return tea.Batch(m.refreshWorkloads(), scheduleRefresh()), false
	case workloadsRefreshMsg:
		return m.handleWorkloadsRefresh(msg)
	case logLineMsg:
		return m.handleLogLine(msg)
	case logStreamDoneMsg:
		// Stream ended; do nothing — a future selection change will restart it.
	case proxyLogLineMsg:
		return m.handleProxyLogLine(msg)
	case proxyLogStreamDoneMsg:
		// Stream ended.
	case actionDoneMsg:
		var notifCmd tea.Cmd
		if msg.err != nil {
			notifCmd = m.showNotif("✗ "+msg.name+": "+msg.err.Error(), false)
		} else {
			notifCmd = m.showNotif("✓ "+msg.name+" "+msg.action, true)
		}
		return tea.Batch(m.refreshWorkloads(), notifCmd), false
	case notifClearMsg:
		m.notifMsg = ""
		return nil, false
	case toolsFetchedMsg:
		m.handleToolsFetched(msg)
	case registryLoadedMsg:
		m.handleRegistryLoaded(msg)
	case runConfigLoadedMsg:
		m.handleRunConfigLoaded(msg)
	case inspCallResultMsg:
		m.handleInspCallResult(msg)
	case tuiLogMsg:
		return m.handleTUILog(msg), false
	case inspSpinTickMsg:
		if m.insp.loading {
			m.insp.spinFrame = (m.insp.spinFrame + 1) % len(inspSpinFrames)
			return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return inspSpinTickMsg{} }), false
		}
	}
	return nil, false
}

func (m *Model) handleWorkloadsRefresh(msg workloadsRefreshMsg) (tea.Cmd, bool) {
	core.SortWorkloadsByName(msg.workloads)
	m.workloads = msg.workloads
	if m.selectedIdx >= len(m.filteredWorkloads()) && m.selectedIdx > 0 {
		m.selectedIdx = len(m.filteredWorkloads()) - 1
	}
	return nil, false
}

func (m *Model) handleLogLine(msg logLineMsg) (tea.Cmd, bool) {
	m.logLines = append(m.logLines, formatLogLine(string(msg)))
	m.logView.SetContent(buildHScrollContent(m.logLines, m.logView.Width, m.logHScrollOff))
	if m.logFollow {
		m.logView.GotoBottom()
	}
	if m.logCh != nil {
		return readLogLine(m.logCh), false
	}
	return nil, false
}

func (m *Model) handleProxyLogLine(msg proxyLogLineMsg) (tea.Cmd, bool) {
	m.proxyLogLines = append(m.proxyLogLines, formatLogLine(string(msg)))
	m.proxyLogView.SetContent(buildHScrollContent(m.proxyLogLines, m.proxyLogView.Width, m.proxyLogHScrollOff))
	m.proxyLogView.GotoBottom()
	if m.proxyLogCh != nil {
		return readProxyLogLine(m.proxyLogCh), false
	}
	return nil, false
}

func (m *Model) handleToolsFetched(msg toolsFetchedMsg) {
	if msg.workloadName == m.toolsFor {
		m.tools = msg.tools
		m.toolsErr = msg.err
		m.toolsLoading = false
		m.toolsSelectedIdx = 0
		m.toolsView.SetContent(buildToolsContent(m.tools, m.toolsView.Width, m.toolsSelectedIdx))
		m.toolsView.GotoTop()
	}
}

func (m *Model) handleRegistryLoaded(msg registryLoadedMsg) {
	m.registry.loading = false
	m.registry.err = msg.err
	m.registry.items = msg.items
	m.registry.idx = 0
}

func (m *Model) handleRunConfigLoaded(msg runConfigLoadedMsg) {
	if msg.workloadName == m.runConfigFor {
		m.runConfig = msg.cfg
	}
}

// handleTUILog appends a captured slog message to the inspector log view.
func (m *Model) handleTUILog(msg tuiLogMsg) tea.Cmd {
	m.insp.logLines = append(m.insp.logLines, string(msg))
	content := strings.Join(m.insp.logLines, "\n")
	m.insp.logView.SetContent(content)
	m.insp.logView.GotoBottom()
	return m.watchTUILog()
}

// handleInspCallResult processes the result of a tool call from the inspector.
func (m *Model) handleInspCallResult(msg inspCallResultMsg) {
	m.insp.loading = false
	m.insp.resultMs = msg.elapsedMs
	if msg.err != nil {
		m.insp.result = "Error: " + msg.err.Error()
		m.insp.resultOK = false
	} else {
		m.insp.result = formatInspResult(msg.result)
		m.insp.resultOK = msg.result == nil || !msg.result.IsError
	}
	m.insp.respView.SetContent(m.insp.result)
	m.insp.respView.GotoTop()

	// Attempt to parse the result as a JSON tree for interactive display.
	m.insp.jsonRoot = parseJSONTree(m.insp.result)
	if m.insp.jsonRoot != nil {
		m.insp.treeVis = flattenVisible(m.insp.jsonRoot)
		m.insp.treeCursor = 0
		m.insp.treeScroll = 0
	}
}

// handleKey dispatches key events and returns a follow-up tea.Cmd if any.
func (m *Model) handleKey(msg tea.KeyMsg) tea.Cmd {
	// Registry overlay has its own key handling.
	if m.registry.open {
		return m.handleRegistryKey(msg)
	}
	if m.filterActive {
		return m.handleFilterKey(msg)
	}
	if m.showHelp {
		m.showHelp = false
		return nil
	}
	if m.confirmDelete {
		return m.handleConfirmDeleteKey(msg)
	}
	if m.panel == panelInspector {
		return m.handleInspectorKey(msg)
	}
	// Log search prompt captures all input while active.
	if m.panel == panelLogs && m.logSearchActive {
		return m.handleLogSearchKey(msg)
	}
	if m.panel == panelProxyLogs && m.proxyLogSearchActive {
		return m.handleProxyLogSearchKey(msg)
	}
	return m.handleNormalKey(msg)
}

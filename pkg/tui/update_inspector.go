// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/mark3labs/mcp-go/mcp"
)

// handleInspectorKey handles key input when the inspector panel is active.
//
//nolint:gocyclo // key-handler switch; complexity is inherent to dispatching over all inspector key bindings
func (m *Model) handleInspectorKey(msg tea.KeyMsg) tea.Cmd {
	// Info modal captures all input — any key closes it.
	if m.insp.showInfo {
		m.insp.showInfo = false
		return nil
	}

	// Filter prompt captures all input until Enter or Esc.
	if m.insp.filterActive {
		return m.handleInspFilterKey(msg)
	}

	// When a text field is focused, forward everything except Tab/ShiftTab/Escape/Enter/arrows.
	if m.insp.fieldIdx >= 0 {
		switch {
		case key.Matches(msg, keys.Escape):
			m.blurAllInspFields()
			return nil
		case key.Matches(msg, keys.Tab):
			return m.inspNextField()
		case key.Matches(msg, keys.ShiftTab):
			return m.inspPrevField()
		case key.Matches(msg, keys.Enter):
			// Enter calls the tool even while a field is focused.
			return m.inspDoCall()
		case key.Matches(msg, keys.Up):
			// Arrow keys move the JSON tree cursor; single-line textinputs ignore them anyway.
			if m.insp.jsonRoot != nil {
				return m.inspTreeMove(-1)
			}
			if m.insp.result != "" {
				m.insp.respView.ScrollUp(1)
				return nil
			}
			return m.inspForwardToField(msg)
		case key.Matches(msg, keys.Down):
			if m.insp.jsonRoot != nil {
				return m.inspTreeMove(1)
			}
			if m.insp.result != "" {
				m.insp.respView.ScrollDown(1)
				return nil
			}
			return m.inspForwardToField(msg)
		case key.Matches(msg, keys.Space):
			// Intercept Space for JSON tree collapse even when a field is focused.
			if m.insp.jsonRoot != nil {
				toggleCollapse(m.insp.treeVis, m.insp.treeCursor)
				m.insp.treeVis = flattenVisible(m.insp.jsonRoot)
				if m.insp.treeCursor >= len(m.insp.treeVis) {
					m.insp.treeCursor = len(m.insp.treeVis) - 1
				}
				m.treeClampScroll()
				return nil
			}
			return m.inspForwardToField(msg)
		default:
			return m.inspForwardToField(msg)
		}
	}

	// No field focused — navigation mode.
	switch {
	case key.Matches(msg, keys.Escape):
		// Esc goes back to the tools panel; response is preserved until
		// the user changes tool or leaves the inspector panel.
		m.panel = panelTools
		m.blurAllInspFields()
		return nil
	case key.Matches(msg, keys.Up):
		if m.insp.jsonRoot != nil {
			return m.inspTreeMove(-1)
		}
		if m.insp.result != "" {
			m.insp.respView.ScrollUp(1)
			return nil
		}
		return m.inspNavigateUp()
	case key.Matches(msg, keys.Down):
		if m.insp.jsonRoot != nil {
			return m.inspTreeMove(1)
		}
		if m.insp.result != "" {
			m.insp.respView.ScrollDown(1)
			return nil
		}
		return m.inspNavigateDown()
	case key.Matches(msg, keys.Space):
		// Toggle collapse on the selected JSON node.
		if m.insp.jsonRoot != nil {
			toggleCollapse(m.insp.treeVis, m.insp.treeCursor)
			m.insp.treeVis = flattenVisible(m.insp.jsonRoot)
			// Clamp cursor in case collapsed nodes removed items below cursor.
			if m.insp.treeCursor >= len(m.insp.treeVis) {
				m.insp.treeCursor = len(m.insp.treeVis) - 1
			}
			m.treeClampScroll()
		}
		return nil
	case key.Matches(msg, keys.CopyCurl):
		// y copies the curl command for the current tool call to clipboard.
		if sel := m.selected(); sel != nil {
			if ft := m.filteredTools(); len(ft) > 0 && m.insp.toolIdx < len(ft) {
				tool := ft[m.insp.toolIdx]
				curl := buildCurlStr(sel, tool.Name, inspFieldValues(m.insp.fields))
				_ = clipboard.WriteAll(curl)
				return m.showNotif("✓ curl copied", true)
			}
		}
		return nil
	case key.Matches(msg, keys.CopyNode):
		// c copies the full response JSON to clipboard.
		if m.insp.result != "" {
			m.inspCopyNode()
			return m.showNotif("✓ copied to clipboard", true)
		}
		return nil
	case key.Matches(msg, keys.Filter):
		// / opens the tool filter prompt.
		m.insp.filterActive = true
		m.insp.filterQuery = ""
		m.insp.toolIdx = 0
		m.inspRebuildForm()
		return nil
	case key.Matches(msg, keys.ToolInfo):
		// i opens the tool description modal.
		if ft := m.filteredTools(); len(ft) > 0 && m.insp.toolIdx < len(ft) {
			m.insp.showInfo = true
		}
		return nil
	case key.Matches(msg, keys.Tab):
		return m.togglePanel()
	case key.Matches(msg, keys.Enter):
		if len(m.insp.fields) > 0 {
			return m.inspNextField()
		}
		return m.inspDoCall()
	default:
		return nil
	}
}

// handleInspFilterKey handles key input while the inspector tool filter is active.
func (m *Model) handleInspFilterKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, keys.Escape):
		m.insp.filterActive = false
		m.insp.filterQuery = ""
		m.insp.toolIdx = 0
		m.inspRebuildForm()
	case key.Matches(msg, keys.Enter):
		// Find the currently selected tool in the filtered list, then clear the
		// filter so the full list is shown with that tool still highlighted.
		filtered := m.filteredTools()
		var selectedTool *mcp.Tool
		if len(filtered) > 0 && m.insp.toolIdx < len(filtered) {
			t := filtered[m.insp.toolIdx]
			selectedTool = &t
		}
		m.insp.filterActive = false
		m.insp.filterQuery = ""
		// Restore toolIdx to the tool's position in the full list.
		if selectedTool != nil {
			for i, t := range m.tools {
				if t.Name == selectedTool.Name {
					m.insp.toolIdx = i
					break
				}
			}
		}
		if len(m.insp.fields) > 0 {
			m.insp.fieldIdx = 0
			m.insp.fields[0].input.Focus()
		}
	case key.Matches(msg, keys.Up):
		return m.inspNavigateUp()
	case key.Matches(msg, keys.Down):
		return m.inspNavigateDown()
	case msg.Type == tea.KeyBackspace:
		if len(m.insp.filterQuery) > 0 {
			r := []rune(m.insp.filterQuery)
			m.insp.filterQuery = string(r[:len(r)-1])
			m.insp.toolIdx = 0
			m.inspRebuildForm()
		}
	default:
		if msg.Type == tea.KeyRunes {
			m.insp.filterQuery += msg.String()
			m.insp.toolIdx = 0
			m.inspRebuildForm()
		}
	}
	return nil
}

// inspNavigateUp moves to the previous tool in the filtered list.
func (m *Model) inspNavigateUp() tea.Cmd {
	if m.insp.toolIdx > 0 {
		m.insp.toolIdx--
		m.inspRebuildForm()
	}
	return nil
}

// inspNavigateDown moves to the next tool in the filtered list.
func (m *Model) inspNavigateDown() tea.Cmd {
	if m.insp.toolIdx < len(m.filteredTools())-1 {
		m.insp.toolIdx++
		m.inspRebuildForm()
	}
	return nil
}

// inspNextField advances focus to the next inspector form field.
func (m *Model) inspNextField() tea.Cmd {
	formNextField(m.insp.fields, &m.insp.fieldIdx)
	return nil
}

// inspPrevField moves focus to the previous inspector form field.
func (m *Model) inspPrevField() tea.Cmd {
	formPrevField(m.insp.fields, &m.insp.fieldIdx)
	return nil
}

// blurAllInspFields blurs all inspector text inputs and resets the focused index.
func (m *Model) blurAllInspFields() {
	formBlurAll(m.insp.fields, &m.insp.fieldIdx)
}

// inspRebuildForm rebuilds the form fields for the currently selected tool.
func (m *Model) inspRebuildForm() {
	filtered := m.filteredTools()
	if len(filtered) == 0 || m.insp.toolIdx >= len(filtered) {
		m.insp.fields = nil
		m.insp.fieldIdx = -1
		m.insp.result = ""
		m.insp.resultOK = false
		m.insp.resultMs = 0
		m.insp.respView.SetContent("")
		m.insp.logLines = nil
		m.insp.logView.SetContent("")
		return
	}
	tool := filtered[m.insp.toolIdx]
	// Preserve the result if we're rebuilding for the same tool (e.g. re-entering inspector).
	if m.insp.resultTool != tool.Name {
		m.insp.result = ""
		m.insp.resultOK = false
		m.insp.resultMs = 0
		m.insp.respView.SetContent("")
		m.insp.logLines = nil
		m.insp.logView.SetContent("")
		m.insp.jsonRoot = nil
		m.insp.treeVis = nil
		m.insp.treeCursor = 0
		m.insp.treeScroll = 0
	}
	m.insp.fields = buildInspFields(tool)
	m.insp.fieldIdx = -1
}

// inspTreeMove moves the JSON tree cursor by delta (+1 down, -1 up) and adjusts scroll.
func (m *Model) inspTreeMove(delta int) tea.Cmd {
	if len(m.insp.treeVis) == 0 {
		return nil
	}
	m.insp.treeCursor += delta
	if m.insp.treeCursor < 0 {
		m.insp.treeCursor = 0
	}
	if m.insp.treeCursor >= len(m.insp.treeVis) {
		m.insp.treeCursor = len(m.insp.treeVis) - 1
	}
	m.treeClampScroll()
	return nil
}

// treeClampScroll adjusts treeScroll so that treeCursor stays in the visible window.
func (m *Model) treeClampScroll() {
	if m.insp.treeVisH <= 0 {
		return
	}
	if m.insp.treeCursor < m.insp.treeScroll {
		m.insp.treeScroll = m.insp.treeCursor
	}
	if m.insp.treeCursor >= m.insp.treeScroll+m.insp.treeVisH {
		m.insp.treeScroll = m.insp.treeCursor - m.insp.treeVisH + 1
	}
}

// inspCopyNode copies the full response JSON to the clipboard.
func (m *Model) inspCopyNode() {
	if m.insp.result == "" {
		return
	}
	_ = clipboard.WriteAll(m.insp.result)
}

// inspForwardToField forwards a key message to the currently focused field.
func (m *Model) inspForwardToField(msg tea.KeyMsg) tea.Cmd {
	return formForwardKey(m.insp.fields, m.insp.fieldIdx, msg)
}

// inspDoCall starts an async tool call with the current field values.
func (m *Model) inspDoCall() tea.Cmd {
	if m.insp.loading {
		return nil
	}
	if m.mcpClient == nil {
		return nil
	}
	filtered := m.filteredTools()
	if len(filtered) == 0 || m.insp.toolIdx >= len(filtered) {
		return nil
	}
	tool := filtered[m.insp.toolIdx]
	args := inspFieldValues(m.insp.fields)
	m.blurAllInspFields()
	m.insp.loading = true
	m.insp.spinFrame = 0
	m.insp.resultTool = tool.Name // track which tool the result belongs to
	spinCmd := tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return inspSpinTickMsg{} })
	callCmd := startInspCallTool(m.ctx, m.mcpClient, tool.Name, args)
	return tea.Batch(spinCmd, callCmd)
}

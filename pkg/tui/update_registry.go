// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// handleRegistryKey handles key input while the registry overlay is open.
func (m *Model) handleRegistryKey(msg tea.KeyMsg) tea.Cmd {
	// Detail view has its own key handling.
	if m.registry.detail {
		return m.handleRegistryDetailKey(msg)
	}
	switch {
	case key.Matches(msg, keys.Escape), key.Matches(msg, keys.Registry):
		m.registry.open = false
		m.registry.detail = false
		m.registry.filter = ""
		m.registry.idx = 0
		m.registry.scrollOff = 0
	case key.Matches(msg, keys.Enter):
		items := m.filteredRegistryItems()
		if len(items) > 0 && m.registry.idx < len(items) {
			m.registry.detail = true
			m.registry.detailScroll = 0
		}
	case key.Matches(msg, keys.Up):
		if m.registry.idx > 0 {
			m.registry.idx--
			m.clampRegistryScroll()
		}
	case key.Matches(msg, keys.Down):
		items := m.filteredRegistryItems()
		if m.registry.idx < len(items)-1 {
			m.registry.idx++
			m.clampRegistryScroll()
		}
	case msg.Type == tea.KeyBackspace:
		if len(m.registry.filter) > 0 {
			m.registry.filter = m.registry.filter[:len(m.registry.filter)-1]
			m.registry.idx = 0
			m.registry.scrollOff = 0
		}
	default:
		if msg.Type == tea.KeyRunes {
			m.registry.filter += msg.String()
			m.registry.idx = 0
			m.registry.scrollOff = 0
		}
	}
	return nil
}

// handleRegistryDetailKey handles key input in the detail view.
func (m *Model) handleRegistryDetailKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, keys.Escape):
		m.registry.detail = false
		m.registry.detailScroll = 0
	case key.Matches(msg, keys.Up):
		if m.registry.detailScroll > 0 {
			m.registry.detailScroll--
		}
	case key.Matches(msg, keys.Down):
		m.registry.detailScroll++
	case key.Matches(msg, keys.CopyCurl):
		// y copies the suggested `thv run` command for the selected registry item.
		items := m.filteredRegistryItems()
		if len(items) > 0 && m.registry.idx < len(items) {
			cmd := buildRunCmd(items[m.registry.idx])
			_ = clipboard.WriteAll(cmd)
			return m.showNotif("✓ run command copied", true)
		}
	}
	return nil
}

// clampRegistryScroll adjusts the scroll offset so the selected item is visible.
func (m *Model) clampRegistryScroll() {
	visible := m.registryVisibleRows()
	if visible < 1 {
		return
	}
	if m.registry.idx < m.registry.scrollOff {
		m.registry.scrollOff = m.registry.idx
	}
	if m.registry.idx >= m.registry.scrollOff+visible {
		m.registry.scrollOff = m.registry.idx - visible + 1
	}
}

// registryVisibleRows returns how many item rows fit in the current overlay.
func (m *Model) registryVisibleRows() int {
	// overlay height is ~70% of terminal, minus borders/header/search/footer (~8 lines)
	h := m.height*70/100 - 8
	if h < 3 {
		return 3
	}
	return h
}

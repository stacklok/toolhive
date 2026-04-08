// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"strings"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	regtypes "github.com/stacklok/toolhive-core/registry/types"
)

// handleRegistryKey handles key input while the registry overlay is open.
func (m *Model) handleRegistryKey(msg tea.KeyMsg) tea.Cmd {
	// Run form captures all input while open.
	if m.runForm.open {
		return m.handleRunFormKey(msg)
	}
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
			r := []rune(m.registry.filter)
			m.registry.filter = string(r[:len(r)-1])
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
	case key.Matches(msg, keys.Restart):
		items := m.filteredRegistryItems()
		if len(items) > 0 && m.registry.idx < len(items) {
			return m.openRunForm(items[m.registry.idx])
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

// openRunForm initialises the run-from-registry form for the given item.
func (m *Model) openRunForm(item regtypes.ServerMetadata) tea.Cmd {
	m.runForm = runFormState{
		open:   true,
		item:   item,
		fields: buildRunFormFields(item),
		idx:    0,
		scroll: 0,
	}
	if len(m.runForm.fields) > 0 {
		m.runForm.fields[0].input.Focus()
	}
	return nil
}

// handleRunFormKey handles key input while the run form is open.
func (m *Model) handleRunFormKey(msg tea.KeyMsg) tea.Cmd {
	if m.runForm.running {
		return nil
	}
	switch {
	case key.Matches(msg, keys.Escape):
		m.runForm.open = false
		return nil
	case key.Matches(msg, keys.Tab):
		m.runFormNextField()
		m.clampRunFormScroll()
		return nil
	case key.Matches(msg, keys.ShiftTab):
		m.runFormPrevField()
		m.clampRunFormScroll()
		return nil
	case key.Matches(msg, keys.Enter):
		return m.runFormSubmit()
	default:
		return m.runFormForwardToField(msg)
	}
}

func (m *Model) runFormNextField() {
	formNextField(m.runForm.fields, &m.runForm.idx)
}

func (m *Model) runFormPrevField() {
	formPrevField(m.runForm.fields, &m.runForm.idx)
}

func (m *Model) blurAllRunFormFields() {
	formBlurAll(m.runForm.fields, &m.runForm.idx)
}

func (m *Model) runFormForwardToField(msg tea.KeyMsg) tea.Cmd {
	return formForwardKey(m.runForm.fields, m.runForm.idx, msg)
}

// runFormSubmit validates required fields and launches the run command.
func (m *Model) runFormSubmit() tea.Cmd {
	if len(m.runForm.fields) == 0 {
		return m.showNotif("✗ no form fields", false)
	}

	// Validate required fields.
	for _, f := range m.runForm.fields {
		if f.required && strings.TrimSpace(f.input.Value()) == "" {
			return m.showNotif("✗ "+f.name+" is required", false)
		}
	}

	workloadName := strings.TrimSpace(m.runForm.fields[0].input.Value())

	secrets := make(map[string]string)
	envs := make(map[string]string)
	for _, f := range m.runForm.fields[1:] {
		val := strings.TrimSpace(f.input.Value())
		if val == "" {
			continue
		}
		if f.secret {
			secrets[f.name] = val
		} else {
			envs[f.name] = val
		}
	}

	m.runForm.running = true
	m.blurAllRunFormFields()
	return runFromRegistry(m.ctx, m.runForm.item, workloadName, secrets, envs)
}

// clampRunFormScroll ensures the focused field is visible in the form overlay.
func (m *Model) clampRunFormScroll() {
	// Each field takes ~3 lines (label + optional desc + input).
	// Visible area is roughly 70% of height minus header/footer.
	visibleFields := max((m.height*70/100-8)/3, 2)
	if m.runForm.idx < m.runForm.scroll {
		m.runForm.scroll = m.runForm.idx
	}
	if m.runForm.idx >= m.runForm.scroll+visibleFields {
		m.runForm.scroll = m.runForm.idx - visibleFields + 1
	}
}

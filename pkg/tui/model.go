// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package tui provides an interactive terminal dashboard for ToolHive.
package tui

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"

	regtypes "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/workloads"
)

// activePanel identifies which tab is currently visible in the main area.
type activePanel int

const (
	panelLogs activePanel = iota
	panelInfo
	panelTools
	panelProxyLogs
	panelInspector
)

// formField bundles a text input with its metadata, replacing parallel slices.
type formField struct {
	input    textinput.Model
	name     string
	required bool
	desc     string
	typeName string // inspector: type hint like "string", "integer"
	secret   bool   // run form: whether this is a secret field
}

// inspectorState holds all state for the tool inspector panel.
type inspectorState struct {
	toolIdx      int
	filterActive bool
	filterQuery  string
	fields       []formField
	fieldIdx     int // -1 = no field focused; 0..n-1 = field focused
	result       string
	resultOK     bool
	resultMs     int64
	resultTool   string // tool name the current result belongs to
	loading      bool
	showInfo     bool // showing tool description modal
	spinFrame    int
	respView     viewport.Model
	logLines     []string
	logView      viewport.Model
	jsonRoot     *jsonNode // nil when response is not valid JSON
	treeVis      []visItem // flattened visible-node list (rebuilt on collapse/expand)
	treeCursor   int       // cursor position in treeVis
	treeScroll   int       // index of first visible item
	treeVisH     int       // available render height (set by resizeViewport)
}

// runFormState holds state for the "run from registry" form overlay.
type runFormState struct {
	open    bool
	item    regtypes.ServerMetadata
	fields  []formField
	idx     int
	running bool
	scroll  int
}

// registryState holds state for the registry browser overlay.
type registryState struct {
	open         bool
	items        []regtypes.ServerMetadata
	filter       string
	idx          int
	scrollOff    int // first visible item index in list
	loading      bool
	err          error
	detail       bool // showing detail view for selected item
	detailScroll int  // scroll offset in detail view
}

// Model is the top-level BubbleTea model for the TUI dashboard.
type Model struct {
	ctx     context.Context
	manager workloads.Manager

	// Dimensions
	width  int
	height int

	// Sidebar state
	workloads    []core.Workload
	selectedIdx  int
	filterQuery  string
	filterActive bool

	// Main panel
	panel         activePanel
	logView       viewport.Model
	logLines      []string
	logFollow     bool
	logHScrollOff int

	// Log search state
	logSearchActive  bool
	logSearchQuery   string
	logSearchMatches []int // indices into logLines that match
	logSearchIdx     int   // current focused match index

	// Log streaming
	logCh        <-chan string
	logCtxCancel context.CancelFunc
	streamingFor string // workload name currently being streamed

	// Tools panel state
	tools            []vmcp.Tool
	toolsLoading     bool
	toolsFor         string // workload name whose tools are loaded
	toolsErr         error
	toolsView        viewport.Model
	toolsSelectedIdx int // currently highlighted tool in Tools panel

	// Proxy logs panel state
	proxyLogView       viewport.Model
	proxyLogLines      []string
	proxyLogCh         <-chan string
	proxyLogCancel     context.CancelFunc
	proxyLogFor        string // workload name currently being streamed for proxy logs
	proxyLogHScrollOff int

	// Proxy log search state
	proxyLogSearchActive  bool
	proxyLogSearchQuery   string
	proxyLogSearchMatches []int
	proxyLogSearchIdx     int

	// RunConfig (enhanced info panel)
	runConfig    *runner.RunConfig
	runConfigFor string // workload name whose runConfig is loaded

	// Registry overlay state
	registry registryState

	// Run-from-registry form state
	runForm runFormState

	// Inspector panel state
	insp inspectorState

	// TUI-level log capture: slog WARN/ERROR messages sent here while TUI runs.
	tuiLogCh <-chan string

	// Transient status bar notification (right-aligned, auto-clears after 3s).
	notifMsg string
	notifOK  bool

	// After a run-from-registry completes, select the new workload by name.
	pendingSelect string

	// UI flags
	showHelp      bool
	confirmDelete bool // waiting for second 'd' to confirm deletion
	quitting      bool
}

// selected returns the currently selected workload, or nil if none.
func (m *Model) selected() *core.Workload {
	list := m.filteredWorkloads()
	if len(list) == 0 {
		return nil
	}
	if m.selectedIdx >= len(list) {
		return nil
	}
	w := list[m.selectedIdx]
	return &w
}

// filteredWorkloads returns workloads matching the current filter query.
func (m *Model) filteredWorkloads() []core.Workload {
	if !m.filterActive || m.filterQuery == "" {
		return m.workloads
	}
	var out []core.Workload
	for _, w := range m.workloads {
		if strings.Contains(w.Name, m.filterQuery) {
			out = append(out, w)
		}
	}
	return out
}

// filteredRegistryItems returns registry items matching the current registry filter.
func (m *Model) filteredRegistryItems() []regtypes.ServerMetadata {
	return filterRegistryItems(m.registry.items, m.registry.filter)
}

// filteredTools returns tools matching the current inspector filter query.
func (m *Model) filteredTools() []vmcp.Tool {
	if !m.insp.filterActive || m.insp.filterQuery == "" {
		return m.tools
	}
	var out []vmcp.Tool
	for _, t := range m.tools {
		if strings.Contains(t.Name, m.insp.filterQuery) {
			out = append(out, t)
		}
	}
	return out
}
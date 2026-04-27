// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package tui provides an interactive terminal dashboard for ToolHive.
package tui

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	regtypes "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/runner"
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
	provider     registry.Provider // cached provider for SearchServers filtering
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

	// MCP client (persistent per selected workload)
	mcpClient *mcpclient.Client

	// Tools panel state
	tools            []mcp.Tool
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
// Filtering applies whenever the query is non-empty, even after the prompt
// is dismissed with Enter, so the user can navigate the filtered list.
func (m *Model) filteredWorkloads() []core.Workload {
	if m.filterQuery == "" {
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
// When the filter starts with "/" it matches only the short name (the part after
// the last "/" in the server name), so "/github" finds "io.github.stacklok/github"
// without matching the "io.github" prefix. Otherwise it delegates to SearchServers
// for full-text matching across name, description, and tags.
func (m *Model) filteredRegistryItems() []regtypes.ServerMetadata {
	if m.registry.filter == "" {
		return m.registry.items
	}
	// "/" prefix: match only the short name (after the last "/").
	if strings.HasPrefix(m.registry.filter, "/") {
		q := strings.ToLower(strings.TrimPrefix(m.registry.filter, "/"))
		var out []regtypes.ServerMetadata
		for _, item := range m.registry.items {
			shortName := item.GetName()
			if idx := strings.LastIndex(shortName, "/"); idx >= 0 {
				shortName = shortName[idx+1:]
			}
			if strings.Contains(strings.ToLower(shortName), q) {
				out = append(out, item)
			}
		}
		return out
	}
	if m.registry.provider != nil {
		results, err := m.registry.provider.SearchServers(m.registry.filter)
		if err == nil {
			return results
		}
		// On error fall through to unfiltered list so the UI stays responsive.
	}
	return m.registry.items
}

// filteredTools returns tools matching the current inspector filter query.
func (m *Model) filteredTools() []mcp.Tool {
	if !m.insp.filterActive || m.insp.filterQuery == "" {
		return m.tools
	}
	var out []mcp.Tool
	for _, t := range m.tools {
		if strings.Contains(t.Name, m.insp.filterQuery) {
			out = append(out, t)
		}
	}
	return out
}

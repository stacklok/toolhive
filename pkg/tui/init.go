// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"fmt"

	"github.com/charmbracelet/bubbles/viewport"

	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/workloads"
)

// New creates a new TUI model.
// logCh (optional) receives slog WARN/ERROR messages captured while the TUI runs.
func New(ctx context.Context, manager workloads.Manager, logCh <-chan string) (Model, error) {
	// Fetch initial workload list
	list, err := manager.ListWorkloads(ctx, true)
	if err != nil {
		return Model{}, fmt.Errorf("failed to list workloads: %w", err)
	}
	core.SortWorkloadsByName(list)

	vp := viewport.New(80, 20)
	vp.SetContent("")

	pvp := viewport.New(80, 20)
	pvp.SetContent("")

	tvp := viewport.New(80, 20)
	tvp.SetContent("")

	ivp := viewport.New(60, 20)
	ivp.SetContent("")

	lvp := viewport.New(60, 6)
	lvp.SetContent("")

	m := Model{
		ctx:          ctx,
		manager:      manager,
		workloads:    list,
		panel:        panelLogs,
		logView:      vp,
		logFollow:    true,
		proxyLogView: pvp,
		toolsView:    tvp,
		insp: inspectorState{
			respView: ivp,
			logView:  lvp,
			fieldIdx: -1,
		},
		tuiLogCh: logCh,
	}

	return m, nil
}

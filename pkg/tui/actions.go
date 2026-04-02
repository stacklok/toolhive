// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/stacklok/toolhive/pkg/workloads"
)

// actionDoneMsg is sent when a stop/restart/delete action completes.
type actionDoneMsg struct {
	action string // "stopped", "restarted", "deleted"
	name   string // workload name
	err    error
}

// stopWorkload returns a tea.Cmd that stops the named workload.
func stopWorkload(ctx context.Context, manager workloads.Manager, name string) tea.Cmd {
	return func() tea.Msg {
		fn, err := manager.StopWorkloads(ctx, []string{name})
		if err != nil {
			return actionDoneMsg{action: "stopped", name: name, err: err}
		}
		return actionDoneMsg{action: "stopped", name: name, err: fn()}
	}
}

// deleteWorkload returns a tea.Cmd that removes the named workload.
func deleteWorkload(ctx context.Context, manager workloads.Manager, name string) tea.Cmd {
	return func() tea.Msg {
		fn, err := manager.DeleteWorkloads(ctx, []string{name})
		if err != nil {
			return actionDoneMsg{action: "deleted", name: name, err: err}
		}
		return actionDoneMsg{action: "deleted", name: name, err: fn()}
	}
}

// restartWorkload returns a tea.Cmd that restarts the named workload.
func restartWorkload(ctx context.Context, manager workloads.Manager, name string) tea.Cmd {
	return func() tea.Msg {
		fn, err := manager.RestartWorkloads(ctx, []string{name}, false)
		if err != nil {
			return actionDoneMsg{action: "restarted", name: name, err: err}
		}
		return actionDoneMsg{action: "restarted", name: name, err: fn()}
	}
}


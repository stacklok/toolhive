// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	regtypes "github.com/stacklok/toolhive-core/registry/types"
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

// runFormResultMsg is sent when a "run from registry" command completes.
type runFormResultMsg struct {
	name   string
	server string
	err    error
}

// runFromRegistry returns a tea.Cmd that runs a registry server via the thv CLI.
func runFromRegistry(ctx context.Context, item regtypes.ServerMetadata, workloadName string, secrets, envs map[string]string) tea.Cmd {
	return func() tea.Msg {
		exe, err := os.Executable()
		if err != nil {
			return runFormResultMsg{name: workloadName, server: item.GetName(), err: fmt.Errorf("find executable: %w", err)}
		}

		serverName := item.GetName()
		args := []string{"run", serverName, "--name", workloadName}

		// Transport only when non-default.
		const defaultTransport = "streamable-http"
		if t := item.GetTransport(); t != "" && t != defaultTransport {
			args = append(args, "--transport", t)
		}

		// Permission profile from ImageMetadata.
		if img, ok := item.(*regtypes.ImageMetadata); ok && img != nil && img.Permissions != nil {
			if name := img.Permissions.Name; name != "" && name != "none" {
				args = append(args, "--permission-profile", name)
			}
		}

		// Secrets.
		for k, v := range secrets {
			args = append(args, "--secret", k+"="+v)
		}

		// Env vars.
		for k, v := range envs {
			args = append(args, "--env", k+"="+v)
		}

		cmd := exec.CommandContext(ctx, exe, args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return runFormResultMsg{
				name:   workloadName,
				server: serverName,
				err:    fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out))),
			}
		}
		return runFormResultMsg{name: workloadName, server: serverName}
	}
}


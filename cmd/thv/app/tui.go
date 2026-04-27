// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/cmd/thv/app/ui"
	"github.com/stacklok/toolhive/pkg/tui"
	"github.com/stacklok/toolhive/pkg/workloads"
)

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Open the interactive TUI dashboard (experimental)",
	Long: `Launch the interactive terminal dashboard for managing MCP servers.

The dashboard shows a real-time list of servers with live log streaming,
tool inspection, and registry browsing — all from a single terminal window.

Key bindings:
  ↑/↓/j/k   navigate servers or tools
  tab        cycle panels: Logs → Info → Tools → Proxy Logs → Inspector
  s          stop selected server
  r          restart selected server
  d d        delete selected server (press d twice)
  /          filter server list, or search logs (on Logs/Proxy Logs panel)
  n/N        next/previous search match
  f          toggle log follow mode
  ←/→        horizontal scroll in log panels
  R          open registry browser
  enter      open tool in inspector (from Tools panel)
  space      toggle JSON node collapse (in inspector response)
  c          copy response JSON to clipboard
  y          copy curl command to clipboard
  u          copy server URL to clipboard
  i          show tool description (in inspector)
  ?          show full help overlay
  q/ctrl+c   quit`,
	RunE: tuiCmdFunc,
}

func tuiCmdFunc(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	// Redirect slog WARN/ERROR to a channel so messages don't leak to stderr
	// while the TUI is rendering in alt-screen mode.
	tuiLogCh := make(chan string, 256)
	origLogger := slog.Default()
	slog.SetDefault(slog.New(ui.NewTUILogHandler(tuiLogCh, slog.LevelWarn)))
	defer slog.SetDefault(origLogger)

	// Ensure the terminal background colour set by the TUI's OSC 11 sequence is
	// always reset, even if the program exits via a panic or signal rather than
	// a clean quit. On a normal quit, View() emits the reset; this defer covers
	// panic paths. The signal handler covers SIGTERM/SIGINT when the defer
	// cannot run (e.g. terminal multiplexers sending signals directly).
	// "\x1b]111;\x07" is the OSC 111 sequence that restores the terminal's
	// default background colour.
	const oscReset = "\x1b]111;\x07"
	defer func() { _, _ = fmt.Fprint(os.Stdout, oscReset) }()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		_, _ = fmt.Fprint(os.Stdout, oscReset)
		signal.Stop(sigCh)
		// Re-raise so the default handler terminates the process.
		self, _ := os.FindProcess(os.Getpid())
		_ = self.Signal(syscall.SIGTERM)
	}()

	manager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %w", err)
	}

	model, err := tui.New(ctx, manager, tuiLogCh)
	if err != nil {
		return fmt.Errorf("failed to initialize TUI: %w", err)
	}

	p := tea.NewProgram(model, tea.WithAltScreen())
	_, runErr := p.Run()

	// BubbleTea puts the terminal in raw mode (OPOST/ONLCR disabled) and
	// may not fully restore it before the shell regains control.
	// Running "stty sane" is the most reliable way to reset all terminal
	// flags (OPOST, ONLCR, ECHO, ICANON, …) back to safe defaults.
	if stty := exec.Command("stty", "sane"); stty != nil {
		stty.Stdin = os.Stdin
		_ = stty.Run()
	}

	if runErr != nil {
		return fmt.Errorf("TUI error: %w", runErr)
	}

	return nil
}

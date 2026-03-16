// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

// thv-vm-init is the PID 1 init process for ToolHive go-microvm guest VMs.
// It boots the guest (mounts, DHCP, SSH), reads the original OCI entrypoint
// from /etc/thv-entrypoint.json, starts the MCP server as a child process,
// and forwards termination signals.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/stacklok/go-microvm/guest/boot"
	"github.com/stacklok/go-microvm/guest/reaper"
)

const shutdownTimeout = 5 * time.Second

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// PID 1 must reap orphaned children.
	stopReaper := reaper.Start(logger)
	defer stopReaper()

	// Boot: essential mounts, DHCP networking, SSH server.
	shutdown, err := boot.Run(logger,
		boot.WithSSHKeysPath("/root/.ssh/authorized_keys"),
		boot.WithEnvFilePath("/etc/environment"),
	)
	if err != nil {
		logger.Error("boot failed", "error", err)
		halt()
		return
	}

	// Load the original OCI entrypoint that was captured by the
	// InjectEntrypoint rootfs hook before WithInitOverride replaced it.
	ep, err := loadEntrypoint(DefaultEntrypointPath)
	if err != nil {
		logger.Error("failed to load entrypoint", "error", err)
		gracefulHalt(logger, shutdown)
		return
	}

	// Start the MCP server as a child process.
	cmd := exec.Command(ep.Cmd[0], ep.Cmd[1:]...) //nolint:gosec // cmd comes from trusted OCI config
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), ep.Env...)
	if ep.WorkingDir != "" {
		cmd.Dir = ep.WorkingDir
	}

	if err := cmd.Start(); err != nil {
		logger.Error("failed to start MCP server", "error", err, "cmd", ep.Cmd)
		gracefulHalt(logger, shutdown)
		return
	}
	logger.Info("MCP server started", "pid", cmd.Process.Pid, "cmd", ep.Cmd)

	// Forward SIGTERM/SIGINT to the child process.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		for received := range sig {
			logger.Info("received signal, forwarding to child", "signal", received)
			_ = cmd.Process.Signal(received)
		}
	}()

	// Wait for the child to exit.
	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			logger.Info("MCP server exited", "code", exitErr.ExitCode())
		} else {
			logger.Error("error waiting for MCP server", "error", err)
		}
	}

	gracefulHalt(logger, shutdown)
}

// gracefulHalt shuts down the boot services with a timeout and then halts the VM.
func gracefulHalt(logger *slog.Logger, shutdown func()) {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	done := make(chan struct{})
	go func() {
		shutdown()
		close(done)
	}()

	select {
	case <-done:
		logger.Info("shutdown complete")
	case <-ctx.Done():
		logger.Warn("shutdown timed out")
	}

	halt()
}

// halt powers off the VM. As PID 1 inside a VM, Reboot with POWER_OFF
// is the clean way to stop — calling os.Exit() would cause a kernel panic.
func halt() {
	_ = syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF)
}

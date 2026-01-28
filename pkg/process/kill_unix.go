// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package process

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// KillProcess kills a process by its ID.
// It first sends SIGTERM for graceful shutdown, waits briefly, then sends SIGKILL
// if the process is still running. This handles zombie processes that may survive
// laptop sleep/wake cycles and don't respond to SIGTERM.
func KillProcess(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("failed to find process: %w", err)
	}

	// First try graceful termination with SIGTERM
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		// Process might already be dead
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return fmt.Errorf("failed to send SIGTERM to process: %w", err)
	}

	// Wait briefly for graceful shutdown
	time.Sleep(500 * time.Millisecond)

	// Check if process is still alive
	alive, err := FindProcess(pid)
	if err != nil || !alive {
		// Process terminated gracefully
		return nil
	}

	// Process didn't respond to SIGTERM - force kill with SIGKILL
	// This handles zombie processes that may survive laptop sleep
	if err := proc.Signal(syscall.SIGKILL); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return fmt.Errorf("failed to send SIGKILL to process: %w", err)
	}

	return nil
}

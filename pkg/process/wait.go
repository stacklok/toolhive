// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package process

import (
	"context"
	"time"
)

// WaitForExit waits for the process with the given PID to exit.
// It polls FindProcess every 50ms until the process is no longer running
// or the context is cancelled or the timeout is reached.
// Returns nil when the process has exited, or an error on timeout/context cancellation.
func WaitForExit(ctx context.Context, pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		alive, err := FindProcess(pid)
		if err != nil {
			return err
		}
		if !alive {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return context.DeadlineExceeded
			}
		}
	}
}

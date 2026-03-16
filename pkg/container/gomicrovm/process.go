// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package gomicrovm

import "syscall"

// processChecker abstracts process liveness checks for testing.
type processChecker struct {
	isAlive func(pid int) bool
	kill    func(pid int, sig syscall.Signal) error
}

func defaultProcessChecker() *processChecker {
	return &processChecker{
		isAlive: isProcessAlive,
		kill:    syscall.Kill,
	}
}

// isProcessAlive checks if a process with the given PID exists by sending
// signal 0. Returns false if the process doesn't exist or is not accessible.
func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

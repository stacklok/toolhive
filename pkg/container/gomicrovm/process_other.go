// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package gomicrovm

import "syscall"

// processChecker abstracts process liveness checks for testing.
// On non-Linux platforms, all checks return false since go-microvm requires KVM.
type processChecker struct {
	isAlive func(pid int) bool
	kill    func(pid int, sig syscall.Signal) error
}

func defaultProcessChecker() *processChecker {
	return &processChecker{
		isAlive: func(_ int) bool { return false },
		kill:    func(_ int, _ syscall.Signal) error { return nil },
	}
}

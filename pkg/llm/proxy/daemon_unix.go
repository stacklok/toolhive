// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package proxy

import (
	"syscall"
)

// getSysProcAttr returns Unix-specific SysProcAttr that creates a new session,
// detaching the child process from the terminal.
func getSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

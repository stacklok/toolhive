// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package workloads

import (
	"syscall"
)

// getSysProcAttr returns the platform-specific SysProcAttr for detaching processes
func getSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setsid: true, // Create a new session (Unix only)
	}
}

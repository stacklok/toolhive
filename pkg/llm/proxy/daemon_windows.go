// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package proxy

import (
	"os"
	"syscall"
)

func processAlive(pid int) bool {
	_, err := os.FindProcess(pid)
	return err == nil
}

func getSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x00000008, // 0x00000008 = DETACHED_PROCESS (Win32 API)
	}
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package process

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

// Windows API constants
const (
	processQueryInformation               = 0x0400
	stillActive                           = 259
	errorInvalidParameter   syscall.Errno = 87
)

// Windows API functions
var (
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	openProcess        = kernel32.NewProc("OpenProcess")
	getExitCodeProcess = kernel32.NewProc("GetExitCodeProcess")
	closeHandle        = kernel32.NewProc("CloseHandle")
)

// FindProcess finds a process by its ID and checks if it's running.
// This function works on Windows.
func FindProcess(pid int) (bool, error) {
	if pid <= 0 {
		return false, fmt.Errorf("invalid PID: %d", pid)
	}

	// On Windows, we need to use Windows API to check if a process is running

	// Open the process with PROCESS_QUERY_INFORMATION access right
	handle, _, err := openProcess.Call(
		uintptr(processQueryInformation),
		uintptr(0),
		uintptr(pid),
	)

	if handle == 0 {
		// With these valid access/inheritance arguments, ERROR_INVALID_PARAMETER
		// means the PID does not exist. Access denial leaves its state unknown.
		if errors.Is(err, errorInvalidParameter) {
			return false, nil
		}
		return false, fmt.Errorf("failed to open process: %w", err)
	}

	// Don't forget to close the handle when we're done
	defer closeHandle.Call(handle)

	// Check if the process is still running by getting its exit code
	var exitCode uint32
	ret, _, err := getExitCodeProcess.Call(
		handle,
		uintptr(unsafe.Pointer(&exitCode)),
	)

	if ret == 0 {
		// Failed to get exit code
		return false, fmt.Errorf("failed to get process exit code: %w", err)
	}

	// If the exit code is STILL_ACTIVE, the process is running
	return exitCode == stillActive, nil
}

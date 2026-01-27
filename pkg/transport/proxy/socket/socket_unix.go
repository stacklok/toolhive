// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

// Package socket provides platform-specific socket configuration.
package socket

import (
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// ListenConfig returns a net.ListenConfig with SO_REUSEADDR enabled
// This allows the server to restart immediately even if the port is in TIME_WAIT state
// or held by a zombie process (common after laptop sleep/wake cycles)
func ListenConfig() net.ListenConfig {
	return net.ListenConfig{
		Control: func(_, _ string, c syscall.RawConn) error {
			var opErr error
			if err := c.Control(func(fd uintptr) {
				opErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
			}); err != nil {
				return err
			}
			return opErr
		},
	}
}

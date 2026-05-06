// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package api

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/Microsoft/go-winio"
)

// namedPipeBufferSize is the size of the input/output buffers winio allocates
// per pipe instance. 64 KiB matches what go-winio uses in similar consumers
// (Docker, containerd, Podman) and is well above any single HTTP header chunk.
const namedPipeBufferSize = 64 * 1024

// setupUnixSocket creates either a Windows named-pipe listener (when address
// has the \\.\pipe\ prefix) or an AF_UNIX listener at a filesystem path.
//
// Named pipes are kernel objects rather than files, so the os.Stat / os.Remove
// precheck, os.MkdirAll, and os.Chmod steps are skipped: the pipe namespace
// has no parent directory, and access control is governed by the security
// descriptor on the listener (winio's default restricts access to the
// creating user, which matches the toolhive-studio same-user use case).
//
// AF_UNIX is supported on Windows 10 1803+. The chmod step is dropped on this
// path because POSIX file modes do not apply on Windows.
func setupUnixSocket(address string) (net.Listener, error) {
	if strings.HasPrefix(address, namedPipePrefix) {
		// MessageMode is left at false (byte stream) explicitly because HTTP
		// requires byte-oriented framing.
		listener, err := winio.ListenPipe(address, &winio.PipeConfig{
			MessageMode:      false,
			InputBufferSize:  namedPipeBufferSize,
			OutputBufferSize: namedPipeBufferSize,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create named pipe listener: %w", err)
		}
		return listener, nil
	}

	if _, err := os.Stat(address); err == nil {
		if err := os.Remove(address); err != nil {
			return nil, fmt.Errorf("failed to remove existing socket: %w", err)
		}
	}

	if err := os.MkdirAll(filepath.Dir(address), 0750); err != nil {
		return nil, fmt.Errorf("failed to create socket directory: %w", err)
	}

	listener, err := net.Listen("unix", address)
	if err != nil {
		return nil, fmt.Errorf("failed to create UNIX socket listener: %w", err)
	}

	return listener, nil
}

// cleanupUnixSocket removes the AF_UNIX socket file at address, or no-ops for
// named pipes (the pipe is destroyed when the listener closes).
func cleanupUnixSocket(address string) {
	if strings.HasPrefix(address, namedPipePrefix) {
		return
	}
	if err := os.Remove(address); err != nil && !os.IsNotExist(err) {
		slog.Warn("failed to remove socket file", "error", err)
	}
}

// socketURL returns the URL form of a Unix-socket or named-pipe address for
// the discovery file. Named pipes are emitted as npipe://<name> where <name>
// is everything after the \\.\pipe\ prefix.
func socketURL(address string) string {
	if strings.HasPrefix(address, namedPipePrefix) {
		return "npipe://" + strings.TrimPrefix(address, namedPipePrefix)
	}
	return "unix://" + address
}

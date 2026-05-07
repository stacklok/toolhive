// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package api

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
)

// supportsNamedPipe reports whether the current build target can host a
// Windows named-pipe listener. Used by createListener to reject pipe addresses
// before reaching the per-platform setupUnixSocket implementation.
func supportsNamedPipe() bool { return false }

// setupUnixSocket creates a UNIX domain socket listener at the given path.
// On non-Windows platforms named-pipe addresses are not supported; callers
// guard against that in createListener.
func setupUnixSocket(address string) (net.Listener, error) {
	if err := os.Remove(address); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("failed to remove existing socket: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(address), 0750); err != nil {
		return nil, fmt.Errorf("failed to create socket directory: %w", err)
	}

	listener, err := net.Listen("unix", address)
	if err != nil {
		return nil, fmt.Errorf("failed to create UNIX socket listener: %w", err)
	}

	if err := os.Chmod(address, socketPermissions); err != nil {
		// Roll back the bound listener and the socket file rather than leaking
		// either: the listener owns the AF_UNIX file and net.Listen will not
		// rebind the same path while the file exists.
		_ = listener.Close()
		_ = os.Remove(address)
		return nil, fmt.Errorf("failed to set socket permissions: %w", err)
	}

	return listener, nil
}

// cleanupUnixSocket removes the socket file at address. Missing files are not
// an error since cleanup may run after a partial startup.
func cleanupUnixSocket(address string) {
	if err := os.Remove(address); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("failed to remove socket file", "error", err)
	}
}

// socketURL returns the URL form of a Unix-socket address for the discovery
// file. Non-Windows platforms only ever produce unix:// URLs.
func socketURL(address string) string {
	return "unix://" + address
}

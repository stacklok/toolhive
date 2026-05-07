// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package api

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"

	"github.com/Microsoft/go-winio"
)

// namedPipeBufferSize is the size of the input/output buffers winio allocates
// per pipe instance. 64 KiB matches what go-winio uses in similar consumers
// (Docker, containerd, Podman) and is well above any single HTTP header chunk.
const namedPipeBufferSize = 64 * 1024

// namedPipeSDDL restricts the named-pipe DACL to the creating user (OW) and
// SYSTEM (SY), each granted GenericAll. The Protected flag (P) blocks ACE
// inheritance from any container object. Without an explicit DACL, winio
// inherits CreateNamedPipeW's default which permits other interactive users
// to open the pipe and exercise the entire REST API; on shared, RDP, or
// Citrix hosts that is reachable by anyone with a desktop session, since
// OIDC is opt-in (WithOIDCConfig) rather than always-on.
const namedPipeSDDL = `D:P(A;;GA;;;OW)(A;;GA;;;SY)`

// supportsNamedPipe reports whether the current build target can host a
// Windows named-pipe listener. Used by createListener to choose between the
// pipe and AF_UNIX paths without dragging the runtime package into server.go.
func supportsNamedPipe() bool { return true }

// setupUnixSocket creates either a Windows named-pipe listener (when address
// has the \\.\pipe\ prefix) or an AF_UNIX listener at a filesystem path.
//
// Named pipes are kernel objects rather than files, so the os.Remove,
// os.MkdirAll, and os.Chmod steps are skipped: the pipe namespace has no
// parent directory, and access control is governed by the SDDL passed to
// winio.ListenPipe (see namedPipeSDDL).
//
// AF_UNIX is supported on Windows 10 1803+. The chmod step is dropped on this
// path because POSIX file modes do not apply on Windows; the resulting
// .sock file inherits the parent directory's NTFS ACL, which is best-effort.
// Prefer the named-pipe path when listener access control matters.
func setupUnixSocket(address string) (net.Listener, error) {
	if isNamedPipeAddress(address) {
		// MessageMode is left at false (byte stream) explicitly because HTTP
		// requires byte-oriented framing. SecurityDescriptor restricts the
		// pipe DACL to the creating user and SYSTEM; without it the winio
		// default permits other interactive users to dial the pipe.
		listener, err := winio.ListenPipe(address, &winio.PipeConfig{
			MessageMode:        false,
			InputBufferSize:    namedPipeBufferSize,
			OutputBufferSize:   namedPipeBufferSize,
			SecurityDescriptor: namedPipeSDDL,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create named pipe listener: %w", err)
		}
		return listener, nil
	}

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

	return listener, nil
}

// cleanupUnixSocket removes the AF_UNIX socket file at address, or no-ops for
// named pipes (the pipe is destroyed when the listener closes).
func cleanupUnixSocket(address string) {
	if isNamedPipeAddress(address) {
		return
	}
	if err := os.Remove(address); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("failed to remove socket file", "error", err)
	}
}

// socketURL returns the URL form of a Unix-socket or named-pipe address for
// the discovery file. Named pipes are emitted as npipe://<name> where <name>
// is everything after the \\.\pipe\ prefix.
func socketURL(address string) string {
	if isNamedPipeAddress(address) {
		return "npipe://" + address[len(namedPipePrefix):]
	}
	return "unix://" + address
}

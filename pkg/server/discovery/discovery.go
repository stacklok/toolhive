// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package discovery provides server discovery file management for ToolHive.
// It writes, reads, and removes a JSON file that advertises a running server
// so clients (CLI, Studio) can find it without configuration.
package discovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/adrg/xdg"

	"github.com/stacklok/toolhive/pkg/fileutils"
)

const (
	// dirPermissions is the permission mode for the discovery directory.
	dirPermissions = 0700
	// filePermissions is the permission mode for the discovery file.
	filePermissions = 0600
)

// ServerInfo contains the information advertised by a running ToolHive server.
type ServerInfo struct {
	// URL is the address where the server is listening.
	// For TCP: "http://127.0.0.1:52341"
	// For Unix sockets: "unix:///path/to/thv.sock"
	URL string `json:"url"`

	// PID is the process ID of the running server.
	PID int `json:"pid"`

	// Nonce is a unique identifier generated at server startup.
	// It solves PID reuse: clients verify the nonce via /health to confirm
	// the discovery file refers to the expected server instance.
	Nonce string `json:"nonce"`

	// StartedAt is the UTC timestamp when the server started.
	StartedAt time.Time `json:"started_at"`
}

// defaultDiscoveryDir returns the default directory for the discovery file
// based on the XDG Base Directory Specification.
func defaultDiscoveryDir() string {
	return filepath.Join(xdg.StateHome, "toolhive", "server")
}

// FilePath returns the full path to the server discovery file
// using the default XDG-based directory.
func FilePath() string {
	return filepath.Join(defaultDiscoveryDir(), "server.json")
}

// WriteServerInfo atomically writes the server discovery file.
// It creates the directory if needed, rejects symlinks at the target path,
// and writes with restricted permissions (0600).
func WriteServerInfo(info *ServerInfo) error {
	return writeServerInfoTo(defaultDiscoveryDir(), info)
}

// ReadServerInfo reads and parses the server discovery file.
// Returns os.ErrNotExist if the file does not exist.
func ReadServerInfo() (*ServerInfo, error) {
	return readServerInfoFrom(defaultDiscoveryDir())
}

// RemoveServerInfo removes the server discovery file.
// It is a no-op if the file does not exist.
func RemoveServerInfo() error {
	return removeServerInfoFrom(defaultDiscoveryDir())
}

// writeServerInfoTo writes the discovery file into the given directory.
func writeServerInfoTo(dir string, info *ServerInfo) error {
	if err := os.MkdirAll(dir, dirPermissions); err != nil {
		return fmt.Errorf("failed to create discovery directory: %w", err)
	}

	// Tighten permissions on the directory in case it already existed with
	// looser permissions. MkdirAll only applies mode to newly-created dirs.
	if err := os.Chmod(dir, dirPermissions); err != nil {
		return fmt.Errorf("failed to set discovery directory permissions: %w", err)
	}

	path := filepath.Join(dir, "server.json")

	// Reject symlinks at the target path to prevent symlink attacks
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to write discovery file: %s is a symlink", path)
		}
	}

	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal server info: %w", err)
	}

	if err := fileutils.AtomicWriteFile(path, data, filePermissions); err != nil {
		return fmt.Errorf("failed to write discovery file: %w", err)
	}

	return nil
}

// readServerInfoFrom reads the discovery file from the given directory.
func readServerInfoFrom(dir string) (*ServerInfo, error) {
	path := filepath.Join(dir, "server.json")

	// Reject symlinks on the read path, consistent with the write path.
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("refusing to read discovery file: %s is a symlink", path)
		}
	}

	data, err := os.ReadFile(path) // #nosec G304 -- path is constructed from a trusted XDG directory, not user input
	if err != nil {
		return nil, err
	}

	var info ServerInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("failed to parse discovery file: %w", err)
	}

	return &info, nil
}

// removeServerInfoFrom removes the discovery file from the given directory.
func removeServerInfoFrom(dir string) error {
	err := os.Remove(filepath.Join(dir, "server.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to remove discovery file: %w", err)
	}
	return nil
}

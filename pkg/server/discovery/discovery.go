// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package discovery provides server discovery file management for ToolHive.
// It writes, reads, and removes a JSON file that advertises a running server
// so clients (CLI, Studio) can find it without configuration.
package discovery

import (
	"context"
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

// discoveryDirChain returns the directories ToolHive creates for the discovery
// file under base, outermost first. The last element is the discovery
// directory itself.
//
// The intermediate toolhive directory is part of the chain on purpose. A user
// who can delete or rename it can substitute a directory of their own, and
// every restriction applied to the leaf would then apply to a directory nobody
// reads.
func discoveryDirChain(base string) []string {
	toolhiveDir := filepath.Join(base, "toolhive")
	return []string{toolhiveDir, filepath.Join(toolhiveDir, "server")}
}

// discoveryServerDir returns the leaf directory that holds server.json.
func discoveryServerDir(base string) string {
	chain := discoveryDirChain(base)
	return chain[len(chain)-1]
}

// defaultDiscoveryDir returns the default directory for the discovery file
// based on the XDG Base Directory Specification.
func defaultDiscoveryDir() string {
	chain := discoveryDirChain(xdg.StateHome)
	return chain[len(chain)-1]
}

// FilePath returns the full path to the server discovery file
// using the default XDG-based directory.
func FilePath() string {
	return filepath.Join(defaultDiscoveryDir(), "server.json")
}

// SecureDirResult reports whether ensureSecureDirIn repaired a previously
// insecure discovery directory chain.
type SecureDirResult struct {
	RepairedInsecureChain bool
}

// EnsureSecureDir creates the discovery directory chain and locks it down.
// Callers must invoke it before they read, lock, or otherwise trust anything
// under the discovery directory, not just before they write.
//
// Startup is the case that matters: pkg/api creates the directory, takes
// server.json.lock inside it, and runs Discover before it ever calls
// WriteServerInfo. If a directory left loose by an earlier run only got
// restricted by the write, a pre-planted server.json would be trusted for a
// whole startup cycle, and a Discover result of StateRunning returns before
// the write is reached at all.
func EnsureSecureDir() error {
	_, err := EnsureSecureDirEx()
	return err
}

// EnsureSecureDirEx is like EnsureSecureDir but reports whether the chain was
// repaired from an insecure state. pkg/api uses this under the discovery
// file lock to fail closed on an existing record instead of deleting it.
func EnsureSecureDirEx() (SecureDirResult, error) {
	return ensureSecureDirIn(xdg.StateHome)
}

// ensureSecureDirIn creates the discovery directory chain under base and
// restricts the directories ToolHive owns in it. Windows hardens the full
// chain; other platforms chmod only the server leaf so shared toolhive state
// (runconfigs, toolhive.db) keeps its existing group permissions.
//
// A discovery file that existed while the chain was still loose is not removed
// here: it cannot be classified as legitimate or forged without the startup
// lock and a health check. pkg/api calls ReconcileDiscoveryAfterInsecureUpgrade
// under that lock instead.
func ensureSecureDirIn(base string) (SecureDirResult, error) {
	secureDirs := discoveryDirsToSecure(base)

	hadInsecureChain, err := discoveryChainWasInsecure(secureDirs)
	if err != nil {
		return SecureDirResult{}, err
	}

	fullChain := discoveryDirChain(base)
	if err := mkdirDiscoveryChain(fullChain); err != nil {
		return SecureDirResult{}, err
	}

	for _, dir := range secureDirs {
		if err := restrictDiscoveryDirPermissions(dir); err != nil {
			return SecureDirResult{}, err
		}
	}

	return SecureDirResult{RepairedInsecureChain: hadInsecureChain}, nil
}

func discoveryChainWasInsecure(chain []string) (bool, error) {
	for _, dir := range chain {
		loose, err := discoveryDirPermissionsLoose(dir)
		if err != nil {
			return false, err
		}
		if loose {
			return true, nil
		}
	}
	return false, nil
}

// ReconcileDiscoveryAfterInsecureUpgrade runs under the discovery file lock
// after EnsureSecureDirEx repaired an insecure chain. An existing record cannot
// be classified as legitimate or forged from directory permissions alone, so
// startup fails closed when a healthy server answers, removes stale records, or
// leaves an unhealthy record for the caller to overwrite. It never deletes a
// record that might belong to a still-running peer.
func ReconcileDiscoveryAfterInsecureUpgrade(ctx context.Context, repaired bool) error {
	if !repaired {
		return nil
	}

	path := FilePath()
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	result, err := Discover(ctx)
	if err != nil {
		return err
	}

	switch result.State {
	case StateRunning:
		return fmt.Errorf("another ToolHive server is already running at %s (PID %d)", result.Info.URL, result.Info.PID)
	case StateStale:
		return CleanupStale()
	case StateUnhealthy:
		return nil
	case StateNotFound:
		return fmt.Errorf(
			"refusing to start: discovery record %s exists after security upgrade but could not be validated; "+
				"remove it manually or stop the other server",
			path,
		)
	default:
		return fmt.Errorf("refusing to start: unexpected discovery state %q after security upgrade", result.State)
	}
}

// WriteServerInfo atomically writes the server discovery file.
// It creates and restricts the directory chain if needed, rejects symlinks at
// the target path, and writes with restricted permissions (0600).
func WriteServerInfo(info *ServerInfo) error {
	if err := EnsureSecureDir(); err != nil {
		return err
	}
	return writeServerInfoTo(defaultDiscoveryDir(), info)
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
	// On Windows this sets an explicit protected DACL (POSIX modes are
	// advisory on NTFS); elsewhere it is os.Chmod(dirPermissions).
	if err := restrictDiscoveryDirPermissions(dir); err != nil {
		return err
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

	// Reject a file another account owns for the same reason we reject a
	// symlink: the URL and nonce inside it decide where clients send their
	// API traffic. Tightening the directory stops new writes, but a file
	// planted while the directory was still loose keeps its old ACL.
	if err := validateDiscoveryFileOwner(path); err != nil {
		return nil, err
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

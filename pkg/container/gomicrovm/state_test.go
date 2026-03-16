// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package gomicrovm

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stacklok/go-microvm/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/container/runtime"
)

func TestPersistAndLoadVMState(t *testing.T) {
	t.Parallel()

	stateDir := resolvedTempDir(t)
	createdAt := time.Date(2025, 6, 15, 10, 30, 0, 0, time.UTC)

	entry := &vmEntry{
		name:          "test-vm",
		image:         "ghcr.io/example/server:latest",
		labels:        map[string]string{"toolhive": "true", "custom": "val"},
		ports:         []runtime.PortMapping{{ContainerPort: 8080, HostPort: 19000, Protocol: "tcp"}},
		transportType: "streamable-http",
		createdAt:     createdAt,
		dataDir:       stateDir,
	}

	err := persistVMState(stateDir, entry)
	require.NoError(t, err)

	statePath := filepath.Join(stateDir, stateFileName)
	loaded, err := loadVMState(statePath)
	require.NoError(t, err)

	assert.Equal(t, entry.name, loaded.Name)
	assert.Equal(t, entry.image, loaded.Image)
	assert.Equal(t, entry.labels, loaded.Labels)
	assert.Equal(t, entry.ports, loaded.Ports)
	assert.Equal(t, entry.transportType, loaded.TransportType)
	assert.True(t, entry.createdAt.Equal(loaded.CreatedAt))
	assert.Equal(t, entry.dataDir, loaded.DataDir)
}

func TestLoadVMState_InvalidJSON(t *testing.T) {
	t.Parallel()

	dir := resolvedTempDir(t)
	statePath := filepath.Join(dir, stateFileName)
	require.NoError(t, os.WriteFile(statePath, []byte("not json"), 0o600))

	_, err := loadVMState(statePath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshaling state")
}

func TestLoadVMState_MissingFile(t *testing.T) {
	t.Parallel()

	_, err := loadVMState("/nonexistent/path/state.json")
	require.Error(t, err)
}

func TestRecoverState(t *testing.T) {
	t.Parallel()

	baseDir := resolvedTempDir(t)
	vmsDir := filepath.Join(baseDir, "vms")

	// Create two VM state directories.
	for _, name := range []string{"vm-alpha", "vm-beta"} {
		vmDir := filepath.Join(vmsDir, name)
		require.NoError(t, os.MkdirAll(vmDir, 0o700))
		entry := &vmEntry{
			name:      name,
			image:     "img:" + name,
			labels:    map[string]string{"toolhive": "true"},
			createdAt: time.Now(),
			dataDir:   vmDir,
		}
		require.NoError(t, persistVMState(vmDir, entry))
	}

	// Add a directory with invalid state (should be skipped).
	badDir := filepath.Join(vmsDir, "vm-bad")
	require.NoError(t, os.MkdirAll(badDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(badDir, stateFileName), []byte("{invalid"), 0o600))

	// Add a non-directory entry (should be skipped).
	require.NoError(t, os.WriteFile(filepath.Join(vmsDir, "not-a-dir"), []byte("x"), 0o600))

	c := &Client{
		vms: make(map[string]*vmEntry),
		opts: clientOptions{
			dataDir: baseDir,
			procCheck: &processChecker{
				isAlive: func(_ int) bool { return false },
				kill:    func(_ int, _ syscall.Signal) error { return nil },
			},
		},
	}
	c.recoverState()

	assert.Len(t, c.vms, 2)
	for _, name := range []string{"vm-alpha", "vm-beta"} {
		entry, ok := c.vms[name]
		require.True(t, ok, "expected VM %s to be recovered", name)
		assert.Equal(t, runtime.WorkloadStatusStopped, entry.state)
		assert.Nil(t, entry.vm, "recovered VMs should have nil vm handle")
	}
}

func TestRecoverState_NoDirectory(t *testing.T) {
	t.Parallel()

	c := &Client{
		vms: make(map[string]*vmEntry),
		opts: clientOptions{
			dataDir:   "/nonexistent/path",
			procCheck: defaultProcessChecker(),
		},
	}
	c.recoverState()

	assert.Empty(t, c.vms)
}

func TestRecoverState_AliveProcess(t *testing.T) {
	t.Parallel()

	baseDir := resolvedTempDir(t)
	vmsDir := filepath.Join(baseDir, "vms")
	vmDir := filepath.Join(vmsDir, "alive-vm")
	require.NoError(t, os.MkdirAll(vmDir, 0o700))

	entry := &vmEntry{
		name:      "alive-vm",
		image:     "img:alive",
		labels:    map[string]string{"toolhive": "true"},
		createdAt: time.Now(),
		dataDir:   vmDir,
	}
	require.NoError(t, persistVMState(vmDir, entry))

	// Write gomicrovm-state.json with a PID.
	writeStateFile(t, vmDir, 12345, true)

	c := &Client{
		vms: make(map[string]*vmEntry),
		opts: clientOptions{
			dataDir: baseDir,
			procCheck: &processChecker{
				isAlive: func(_ int) bool { return true },
				kill:    func(_ int, _ syscall.Signal) error { return nil },
			},
		},
	}
	c.recoverState()

	require.Contains(t, c.vms, "alive-vm")
	assert.Equal(t, runtime.WorkloadStatusRunning, c.vms["alive-vm"].state)
}

func TestReadRunnerPID(t *testing.T) {
	t.Parallel()

	t.Run("valid state file", func(t *testing.T) {
		t.Parallel()
		dir := resolvedTempDir(t)
		writeStateFile(t, dir, 42, true)
		assert.Equal(t, 42, readRunnerPID(dir))
	})

	t.Run("missing file", func(t *testing.T) {
		t.Parallel()
		dir := resolvedTempDir(t)
		assert.Equal(t, 0, readRunnerPID(dir))
	})

	t.Run("corrupt file", func(t *testing.T) {
		t.Parallel()
		dir := resolvedTempDir(t)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "go-microvm-state.json"), []byte("{bad"), 0o600))
		assert.Equal(t, 0, readRunnerPID(dir))
	})
}

func TestCleanupOrphanedRunner(t *testing.T) {
	t.Parallel()

	t.Run("alive process gets killed", func(t *testing.T) {
		t.Parallel()
		dir := resolvedTempDir(t)
		writeStateFile(t, dir, 12345, true)

		var killed []int
		pc := &processChecker{
			isAlive: func(_ int) bool {
				// First call returns true (alive), subsequent calls false (killed).
				if len(killed) == 0 {
					return true
				}
				return false
			},
			kill: func(pid int, _ syscall.Signal) error {
				killed = append(killed, pid)
				return nil
			},
		}
		cleanupOrphanedRunner(dir, pc)
		assert.NotEmpty(t, killed, "expected kill to be called")
	})

	t.Run("dead process is no-op", func(t *testing.T) {
		t.Parallel()
		dir := resolvedTempDir(t)
		writeStateFile(t, dir, 99999, false)

		var killed []int
		pc := &processChecker{
			isAlive: func(_ int) bool { return false },
			kill: func(pid int, _ syscall.Signal) error {
				killed = append(killed, pid)
				return nil
			},
		}
		cleanupOrphanedRunner(dir, pc)
		assert.Empty(t, killed, "kill should not be called for dead process")
	})

	t.Run("missing state file is no-op", func(t *testing.T) {
		t.Parallel()
		dir := resolvedTempDir(t)

		var killed []int
		pc := &processChecker{
			isAlive: func(_ int) bool { return true },
			kill: func(pid int, _ syscall.Signal) error {
				killed = append(killed, pid)
				return nil
			},
		}
		cleanupOrphanedRunner(dir, pc)
		assert.Empty(t, killed, "kill should not be called when state file is missing")
	})
}

// writeStateFile writes a go-microvm state file using the go-microvm state
// package's own Manager, ensuring the format stays in sync with the library.
func writeStateFile(t *testing.T, dir string, pid int, active bool) {
	t.Helper()
	mgr := state.NewManager(dir)
	ls, err := mgr.LoadAndLock(context.Background())
	require.NoError(t, err)
	defer ls.Release()

	ls.State.Active = active
	ls.State.Name = "test"
	ls.State.PID = pid
	ls.State.Image = "test:latest"
	ls.State.CPUs = 1
	ls.State.MemoryMB = 512
	ls.State.CreatedAt = time.Now()

	require.NoError(t, ls.Save())
}

// resolvedTempDir creates a temp directory with symlinks resolved.
// This avoids issues on systems where t.TempDir() returns a symlinked path.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	return resolved
}

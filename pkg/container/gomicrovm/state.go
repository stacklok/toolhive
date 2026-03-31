// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package gomicrovm

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/stacklok/go-microvm/state"

	"github.com/stacklok/toolhive/pkg/container/runtime"
)

const stateFileName = "vm-state.json"

// vmStatePersisted is the on-disk representation of a vmEntry.
type vmStatePersisted struct {
	Name               string                `json:"name"`
	Image              string                `json:"image"`
	Labels             map[string]string     `json:"labels"`
	Ports              []runtime.PortMapping `json:"ports"`
	TransportType      string                `json:"transport_type"`
	StdioRelayHostPort int                   `json:"stdio_relay_host_port,omitempty"`
	CreatedAt          time.Time             `json:"created_at"`
	DataDir            string                `json:"data_dir"`
}

// persistVMState writes the vmEntry state to a JSON file in the given directory.
func persistVMState(dir string, entry *vmEntry) error {
	persisted := vmStatePersisted{
		Name:               entry.name,
		Image:              entry.image,
		Labels:             entry.labels,
		Ports:              entry.ports,
		TransportType:      entry.transportType,
		StdioRelayHostPort: entry.stdioRelayHostPort,
		CreatedAt:          entry.createdAt,
		DataDir:            entry.dataDir,
	}
	data, err := json.Marshal(persisted)
	if err != nil {
		return fmt.Errorf("marshaling state: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, stateFileName), data, 0o600)
}

// loadVMState reads and parses a persisted VM state file.
func loadVMState(path string) (*vmStatePersisted, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is constructed from trusted data dir
	if err != nil {
		return nil, fmt.Errorf("reading state file: %w", err)
	}
	var persisted vmStatePersisted
	if err := json.Unmarshal(data, &persisted); err != nil {
		return nil, fmt.Errorf("unmarshaling state: %w", err)
	}
	return &persisted, nil
}

// readRunnerPID reads the gomicrovm-state.json file from a VM data directory
// and returns the runner PID. Returns 0 if the file is missing, unreadable,
// or does not contain a valid PID.
func readRunnerPID(dataDir string) int {
	mgr := state.NewManager(dataDir)
	s, err := mgr.Load()
	if err != nil {
		return 0
	}
	return s.PID
}

// cleanupOrphanedRunner terminates a go-microvm-runner process if it is still
// alive. It sends SIGTERM to the process group (-pid) first, waits up to 5
// seconds, then sends SIGKILL if the process is still running.
func cleanupOrphanedRunner(dataDir string, pc *processChecker) {
	pid := readRunnerPID(dataDir)
	if pid <= 0 || !pc.isAlive(pid) {
		return
	}

	slog.Info("terminating orphaned go-microvm-runner", "pid", pid, "dataDir", dataDir)

	// Send SIGTERM to the process group so child processes are also signaled.
	if err := pc.kill(-pid, syscall.SIGTERM); err != nil {
		// Fall back to signaling the process directly.
		_ = pc.kill(pid, syscall.SIGTERM)
	}

	// Wait up to 5 seconds for the process to exit.
	const (
		waitTimeout  = 5 * time.Second
		pollInterval = 200 * time.Millisecond
	)
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		if !pc.isAlive(pid) {
			return
		}
		time.Sleep(pollInterval)
	}

	// Process still alive — force kill.
	slog.Warn("force-killing orphaned go-microvm-runner", "pid", pid)
	_ = pc.kill(-pid, syscall.SIGKILL)
	_ = pc.kill(pid, syscall.SIGKILL)
}

// recoverState scans the VMs directory for persisted state files,
// cleans up any orphaned runner processes, and re-registers VMs.
// VMs whose runner is still alive are marked as running; otherwise stopped.
func (c *Client) recoverState() {
	vmsDir := filepath.Join(c.opts.dataDir, "vms")
	entries, err := os.ReadDir(vmsDir)
	if err != nil {
		return
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		vmDir := filepath.Join(vmsDir, e.Name())
		statePath := filepath.Join(vmDir, stateFileName)
		vmState, err := loadVMState(statePath)
		if err != nil {
			slog.Debug("skipping VM state recovery", "dir", e.Name(), "error", err)
			continue
		}

		// Check if the runner process is still alive.
		pid := readRunnerPID(vmState.DataDir)
		status := func() runtime.WorkloadStatus {
			if pid > 0 && c.opts.procCheck.isAlive(pid) {
				return runtime.WorkloadStatusRunning
			}
			return runtime.WorkloadStatusStopped
		}()

		// Clean up orphaned runners that are still alive but can't be managed.
		if status == runtime.WorkloadStatusStopped {
			cleanupOrphanedRunner(vmState.DataDir, c.opts.procCheck)
		}

		c.vms[vmState.Name] = &vmEntry{
			name:               vmState.Name,
			image:              vmState.Image,
			labels:             vmState.Labels,
			state:              status,
			vm:                 nil,
			createdAt:          vmState.CreatedAt,
			dataDir:            vmState.DataDir,
			ports:              vmState.Ports,
			transportType:      vmState.TransportType,
			stdioRelayHostPort: vmState.StdioRelayHostPort,
		}
	}
}

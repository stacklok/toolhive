// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package gomicrovm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/labels"
)

// StopWorkload stops a running microVM workload.
func (c *Client) StopWorkload(ctx context.Context, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.vms[name]
	if !ok {
		return nil
	}

	if entry.vm == nil {
		// Recovered VM without a handle — kill the runner process directly.
		if entry.dataDir != "" {
			cleanupOrphanedRunner(entry.dataDir, c.opts.procCheck)
		}
		entry.state = runtime.WorkloadStatusStopped
		return nil
	}

	if err := entry.vm.Stop(ctx); err != nil {
		entry.state = runtime.WorkloadStatusError
		return fmt.Errorf("stopping VM %s: %w", name, err)
	}
	entry.state = runtime.WorkloadStatusStopped
	return nil
}

// RemoveWorkload removes a go-microvm VM workload and cleans up its resources.
func (c *Client) RemoveWorkload(ctx context.Context, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.vms[name]
	if !ok {
		return nil
	}

	if entry.vm != nil {
		if err := entry.vm.Remove(ctx); err != nil {
			return fmt.Errorf("removing VM %s: %w", name, err)
		}
	} else if entry.dataDir != "" {
		// Recovered VM without a handle — kill the runner process before cleanup.
		cleanupOrphanedRunner(entry.dataDir, c.opts.procCheck)
	}

	delete(c.vms, name)

	if entry.dataDir != "" {
		_ = os.RemoveAll(entry.dataDir)
	}
	return nil
}

// IsWorkloadRunning checks if a go-microvm VM workload is running.
// It first checks in-memory state, then verifies the actual runner process
// is alive via its PID from go-microvm state. If the process is dead
// but in-memory state says running, the state is corrected to stopped.
func (c *Client) IsWorkloadRunning(_ context.Context, name string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.vms[name]
	if !ok {
		return false, nil
	}

	// For entries without a vm handle (recovered from disk), check the
	// runner process directly via PID.
	if entry.vm == nil && entry.dataDir != "" {
		pid := readRunnerPID(entry.dataDir)
		alive := pid > 0 && c.opts.procCheck.isAlive(pid)
		if entry.state == runtime.WorkloadStatusRunning && !alive {
			entry.state = runtime.WorkloadStatusStopped
		}
		return alive, nil
	}

	// For entries with a vm handle, also verify the process is alive.
	if entry.state == runtime.WorkloadStatusRunning && entry.dataDir != "" {
		pid := readRunnerPID(entry.dataDir)
		if pid > 0 && !c.opts.procCheck.isAlive(pid) {
			entry.state = runtime.WorkloadStatusStopped
			return false, nil
		}
	}

	return entry.state == runtime.WorkloadStatusRunning, nil
}

// ListWorkloads lists all go-microvm VM workloads managed by ToolHive.
func (c *Client) ListWorkloads(_ context.Context) ([]runtime.ContainerInfo, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var result []runtime.ContainerInfo
	for _, entry := range c.vms {
		if !labels.IsToolHiveContainer(entry.labels) {
			continue
		}
		result = append(result, entryToContainerInfo(entry))
	}
	return result, nil
}

// GetWorkloadInfo retrieves detailed information about a go-microvm VM workload.
func (c *Client) GetWorkloadInfo(_ context.Context, name string) (runtime.ContainerInfo, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.vms[name]
	if !ok {
		return runtime.ContainerInfo{}, runtime.ErrWorkloadNotFound
	}
	return entryToContainerInfo(entry), nil
}

// GetWorkloadLogs retrieves logs from a go-microvm VM workload.
func (c *Client) GetWorkloadLogs(_ context.Context, name string, _ bool, lines int) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.vms[name]
	if !ok {
		return "", runtime.ErrWorkloadNotFound
	}

	logPath := filepath.Join(entry.dataDir, "console.log")
	return readLogFile(logPath, lines)
}

// readLogFile reads a log file, optionally returning only the last N lines.
// If the file does not exist, an empty string is returned with no error.
// If lines is 0, the entire file is returned.
func readLogFile(path string, lines int) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is constructed from trusted data dir
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}

	content := string(data)
	if lines == 0 {
		return content, nil
	}

	allLines := strings.Split(content, "\n")
	// Remove trailing empty element from a final newline
	if len(allLines) > 0 && allLines[len(allLines)-1] == "" {
		allLines = allLines[:len(allLines)-1]
	}

	if lines >= len(allLines) {
		return strings.Join(allLines, "\n"), nil
	}

	return strings.Join(allLines[len(allLines)-lines:], "\n"), nil
}

// entryToContainerInfo converts a vmEntry to runtime.ContainerInfo.
func entryToContainerInfo(entry *vmEntry) runtime.ContainerInfo {
	return runtime.ContainerInfo{
		Name:      entry.name,
		Image:     entry.image,
		Status:    string(entry.state),
		State:     entry.state,
		Created:   entry.createdAt,
		StartedAt: entry.createdAt,
		Labels:    entry.labels,
		Ports:     entry.ports,
	}
}

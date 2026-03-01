// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// WorkloadMonitor watches a workload's state and reports when it exits
type WorkloadMonitor struct {
	runtime          Runtime
	containerName    string
	stopCh           chan struct{}
	errorCh          chan error
	wg               sync.WaitGroup
	running          bool
	mutex            sync.Mutex
	initialStartTime time.Time // Track container start time to detect restarts
}

// NewMonitor creates a new workload monitor
func NewMonitor(rt Runtime, containerName string) Monitor {
	return &WorkloadMonitor{
		runtime:       rt,
		containerName: containerName,
		stopCh:        make(chan struct{}),
		errorCh:       make(chan error, 1), // Buffered to prevent blocking
	}
}

// StartMonitoring starts monitoring the workload
func (m *WorkloadMonitor) StartMonitoring(ctx context.Context) (<-chan error, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.running {
		return m.errorCh, nil // Already monitoring
	}

	// Check if the workload exists and is running
	running, err := m.runtime.IsWorkloadRunning(ctx, m.containerName)
	if err != nil {
		return nil, err
	}
	if !running {
		return nil, NewContainerError(ErrContainerNotRunning, m.containerName, "container is not running")
	}

	// Get initial container info to track start time
	info, err := m.runtime.GetWorkloadInfo(ctx, m.containerName)
	if err != nil {
		return nil, NewContainerError(err, m.containerName, fmt.Sprintf("failed to get container info: %v", err))
	}
	m.initialStartTime = info.StartedAt

	m.running = true
	m.wg.Add(1)

	// Start monitoring in a goroutine
	go m.monitor(ctx)

	return m.errorCh, nil
}

// StopMonitoring stops monitoring the workload
func (m *WorkloadMonitor) StopMonitoring() {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if !m.running {
		return // Not monitoring
	}

	close(m.stopCh)
	m.wg.Wait()
	m.running = false
}

// monitor checks the workload status periodically
func (m *WorkloadMonitor) monitor(ctx context.Context) {
	defer m.wg.Done()

	// Check interval
	checkInterval := 5 * time.Second

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Context canceled
			return
		case <-m.stopCh:
			// Monitoring stopped
			return
		case <-ticker.C:
			// Check if the container is still running
			// Create a short timeout context for this check, derived from parent
			// to respect parent cancellation while having its own timeout
			checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			running, err := m.runtime.IsWorkloadRunning(checkCtx, m.containerName)
			cancel() // Always cancel the context to avoid leaks
			if err != nil {
				// If the container is not found, it has been removed
				if IsContainerNotFound(err) {
					removeErr := NewContainerError(
						ErrContainerRemoved,
						m.containerName,
						fmt.Sprintf("Container %s not found, it has been removed", m.containerName),
					)
					m.errorCh <- removeErr
					return
				}

				// For other errors, log and continue
				continue
			}

			if !running {
				// Container has exited, get logs and info
				// Create a short timeout context for these operations, derived from parent
				infoCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				// Get last 50 lines of logs for error reporting
				logs, _ := m.runtime.GetWorkloadLogs(infoCtx, m.containerName, false, 50)
				info, _ := m.runtime.GetWorkloadInfo(infoCtx, m.containerName)
				cancel() // Always cancel the context to avoid leaks

				exitErr := NewContainerError(
					ErrContainerExited,
					m.containerName,
					fmt.Sprintf("Container %s (%s) exited unexpectedly. Status: %s. Last logs:\n%s",
						m.containerName, m.containerName, info.Status, logs),
				)
				m.errorCh <- exitErr
				return
			}

			// Container is running - check if it was restarted (different start time)
			// Derive timeout from parent context to respect cancellation
			infoCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			info, err := m.runtime.GetWorkloadInfo(infoCtx, m.containerName)
			cancel()
			if err == nil && !info.StartedAt.IsZero() && !info.StartedAt.Equal(m.initialStartTime) {
				// Container was restarted (has a different start time)
				restartErr := NewContainerError(
					ErrContainerRestarted,
					m.containerName,
					fmt.Sprintf("Container %s was restarted (start time changed from %s to %s)",
						m.containerName, m.initialStartTime.Format(time.RFC3339), info.StartedAt.Format(time.RFC3339)),
				)
				m.errorCh <- restartErr
				return
			}
		}
	}
}

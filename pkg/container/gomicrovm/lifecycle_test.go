// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package gomicrovm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stacklok/go-microvm/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	lb "github.com/stacklok/toolhive/pkg/labels"
)

// fakeVM implements vmHandle for testing lifecycle methods.
type fakeVM struct {
	stopErr   error
	removeErr error
	stopped   bool
	removed   bool
	dataDir   string
	name      string
}

func (f *fakeVM) Stop(_ context.Context) error {
	f.stopped = true
	return f.stopErr
}

func (f *fakeVM) Remove(_ context.Context) error {
	f.removed = true
	return f.removeErr
}

func (f *fakeVM) DataDir() string { return f.dataDir }
func (f *fakeVM) Name() string    { return f.name }

func newTestClient(t *testing.T) *Client {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	return &Client{
		vms: make(map[string]*vmEntry),
		opts: clientOptions{
			dataDir: resolved,
			procCheck: &processChecker{
				isAlive: func(_ int) bool { return false },
				kill:    func(_ int, _ syscall.Signal) error { return nil },
			},
		},
	}
}

func newTestClientWithProcCheck(t *testing.T, alive func(int) bool) *Client {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	return &Client{
		vms: make(map[string]*vmEntry),
		opts: clientOptions{
			dataDir: resolved,
			procCheck: &processChecker{
				isAlive: alive,
				kill:    func(_ int, _ syscall.Signal) error { return nil },
			},
		},
	}
}

func TestStopWorkload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		workloadName  string
		setupEntry    *vmEntry
		expectState   runtime.WorkloadStatus
		expectStopped bool
		expectError   bool
	}{
		{
			name:         "unknown workload is a no-op",
			workloadName: "nonexistent",
		},
		{
			name:         "stops running VM",
			workloadName: "my-vm",
			setupEntry: &vmEntry{
				name:  "my-vm",
				state: runtime.WorkloadStatusRunning,
				vm:    &fakeVM{},
			},
			expectState:   runtime.WorkloadStatusStopped,
			expectStopped: true,
		},
		{
			name:         "recovered VM with nil handle",
			workloadName: "recovered",
			setupEntry: &vmEntry{
				name:  "recovered",
				state: runtime.WorkloadStatusStopped,
				vm:    nil,
			},
			expectState: runtime.WorkloadStatusStopped,
		},
		{
			name:         "stop error sets error state",
			workloadName: "failing",
			setupEntry: &vmEntry{
				name:  "failing",
				state: runtime.WorkloadStatusRunning,
				vm:    &fakeVM{stopErr: fmt.Errorf("shutdown failed")},
			},
			expectState:   runtime.WorkloadStatusError,
			expectStopped: true,
			expectError:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newTestClient(t)
			if tc.setupEntry != nil {
				c.vms[tc.workloadName] = tc.setupEntry
			}

			err := c.StopWorkload(context.Background(), tc.workloadName)
			if tc.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			if tc.setupEntry != nil {
				assert.Equal(t, tc.expectState, tc.setupEntry.state)
			}
			if tc.expectStopped {
				fake, ok := tc.setupEntry.vm.(*fakeVM)
				require.True(t, ok)
				assert.True(t, fake.stopped)
			}
		})
	}
}

func TestRemoveWorkload(t *testing.T) {
	t.Parallel()

	t.Run("unknown workload is a no-op", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		require.NoError(t, c.RemoveWorkload(context.Background(), "nonexistent"))
	})

	t.Run("removes VM and cleans up state", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		vmDir := filepath.Join(c.opts.dataDir, "vms", "my-vm")
		require.NoError(t, os.MkdirAll(vmDir, 0o700))

		fake := &fakeVM{}
		c.vms["my-vm"] = &vmEntry{name: "my-vm", vm: fake, dataDir: vmDir}

		require.NoError(t, c.RemoveWorkload(context.Background(), "my-vm"))
		assert.True(t, fake.removed)
		_, ok := c.vms["my-vm"]
		assert.False(t, ok, "VM should be removed from registry")
	})
}

// writeMicrovmState writes a go-microvm state file using the go-microvm state
// package's own Manager, ensuring the format stays in sync with the library.
func writeMicrovmState(t *testing.T, dir string, pid int, active bool) {
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

func TestIsWorkloadRunning(t *testing.T) {
	t.Parallel()

	t.Run("unknown workload", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		got, err := c.IsWorkloadRunning(context.Background(), "missing")
		require.NoError(t, err)
		assert.False(t, got)
	})

	t.Run("running workload with vm handle", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		c.vms["active"] = &vmEntry{name: "active", state: runtime.WorkloadStatusRunning, vm: &fakeVM{}}
		got, err := c.IsWorkloadRunning(context.Background(), "active")
		require.NoError(t, err)
		assert.True(t, got)
	})

	t.Run("stopped workload", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		c.vms["dead"] = &vmEntry{name: "dead", state: runtime.WorkloadStatusStopped}
		got, err := c.IsWorkloadRunning(context.Background(), "dead")
		require.NoError(t, err)
		assert.False(t, got)
	})

	t.Run("recovered VM with alive process", func(t *testing.T) {
		t.Parallel()
		c := newTestClientWithProcCheck(t, func(_ int) bool { return true })
		vmDir := filepath.Join(c.opts.dataDir, "vms", "recovered")
		require.NoError(t, os.MkdirAll(vmDir, 0o700))
		writeMicrovmState(t, vmDir, 12345, true)

		c.vms["recovered"] = &vmEntry{
			name:    "recovered",
			state:   runtime.WorkloadStatusRunning,
			vm:      nil,
			dataDir: vmDir,
		}
		got, err := c.IsWorkloadRunning(context.Background(), "recovered")
		require.NoError(t, err)
		assert.True(t, got)
	})

	t.Run("recovered VM with dead process corrects state", func(t *testing.T) {
		t.Parallel()
		c := newTestClientWithProcCheck(t, func(_ int) bool { return false })
		vmDir := filepath.Join(c.opts.dataDir, "vms", "dead-process")
		require.NoError(t, os.MkdirAll(vmDir, 0o700))
		writeMicrovmState(t, vmDir, 99999, true)

		c.vms["dead-process"] = &vmEntry{
			name:    "dead-process",
			state:   runtime.WorkloadStatusRunning,
			vm:      nil,
			dataDir: vmDir,
		}
		got, err := c.IsWorkloadRunning(context.Background(), "dead-process")
		require.NoError(t, err)
		assert.False(t, got)
		assert.Equal(t, runtime.WorkloadStatusStopped, c.vms["dead-process"].state)
	})

	t.Run("VM with handle detects dead process", func(t *testing.T) {
		t.Parallel()
		c := newTestClientWithProcCheck(t, func(_ int) bool { return false })
		vmDir := filepath.Join(c.opts.dataDir, "vms", "handle-dead")
		require.NoError(t, os.MkdirAll(vmDir, 0o700))
		writeMicrovmState(t, vmDir, 54321, true)

		c.vms["handle-dead"] = &vmEntry{
			name:    "handle-dead",
			state:   runtime.WorkloadStatusRunning,
			vm:      &fakeVM{dataDir: vmDir},
			dataDir: vmDir,
		}
		got, err := c.IsWorkloadRunning(context.Background(), "handle-dead")
		require.NoError(t, err)
		assert.False(t, got)
		assert.Equal(t, runtime.WorkloadStatusStopped, c.vms["handle-dead"].state)
	})
}

func TestListWorkloads(t *testing.T) {
	t.Parallel()

	c := newTestClient(t)
	c.vms["toolhive-vm"] = &vmEntry{
		name:      "toolhive-vm",
		image:     "ghcr.io/example/server:latest",
		labels:    map[string]string{lb.LabelToolHive: lb.LabelToolHiveValue},
		state:     runtime.WorkloadStatusRunning,
		createdAt: time.Now(),
	}
	c.vms["non-toolhive"] = &vmEntry{
		name:   "non-toolhive",
		labels: map[string]string{"other": "label"},
	}

	items, err := c.ListWorkloads(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "toolhive-vm", items[0].Name)
}

func TestGetWorkloadInfo(t *testing.T) {
	t.Parallel()

	t.Run("found", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		c.vms["my-vm"] = &vmEntry{
			name:  "my-vm",
			image: "img:latest",
			state: runtime.WorkloadStatusRunning,
		}
		info, err := c.GetWorkloadInfo(context.Background(), "my-vm")
		require.NoError(t, err)
		assert.Equal(t, "my-vm", info.Name)
		assert.Equal(t, runtime.WorkloadStatusRunning, info.State)
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		_, err := c.GetWorkloadInfo(context.Background(), "missing")
		require.ErrorIs(t, err, runtime.ErrWorkloadNotFound)
	})
}

func TestGetWorkloadLogs(t *testing.T) {
	t.Parallel()

	t.Run("workload not found", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		_, err := c.GetWorkloadLogs(context.Background(), "missing", false, 0)
		require.ErrorIs(t, err, runtime.ErrWorkloadNotFound)
	})

	t.Run("reads log file", func(t *testing.T) {
		t.Parallel()
		c := newTestClient(t)
		vmDir := filepath.Join(c.opts.dataDir, "vms", "logged")
		require.NoError(t, os.MkdirAll(vmDir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(vmDir, "console.log"), []byte("line1\nline2\nline3\n"), 0o600))

		c.vms["logged"] = &vmEntry{name: "logged", dataDir: vmDir}
		logs, err := c.GetWorkloadLogs(context.Background(), "logged", false, 0)
		require.NoError(t, err)
		assert.Contains(t, logs, "line1")
		assert.Contains(t, logs, "line3")
	})
}

func TestAttachToWorkload(t *testing.T) {
	t.Parallel()
	c := newTestClient(t)
	_, _, err := c.AttachToWorkload(context.Background(), "any")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stdio transport is not supported")
}

func TestReadLogFile(t *testing.T) {
	t.Parallel()

	t.Run("missing file returns empty", func(t *testing.T) {
		t.Parallel()
		got, err := readLogFile("/nonexistent/console.log", 0)
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("full read", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		logPath := filepath.Join(dir, "console.log")
		require.NoError(t, os.WriteFile(logPath, []byte("a\nb\nc\nd\n"), 0o600))

		got, err := readLogFile(logPath, 0)
		require.NoError(t, err)
		assert.Equal(t, "a\nb\nc\nd\n", got)
	})

	t.Run("tail last 2 lines", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		logPath := filepath.Join(dir, "console.log")
		require.NoError(t, os.WriteFile(logPath, []byte("a\nb\nc\nd"), 0o600))

		got, err := readLogFile(logPath, 2)
		require.NoError(t, err)
		assert.Equal(t, "c\nd", got)
	})

	t.Run("tail more than available lines", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		logPath := filepath.Join(dir, "console.log")
		require.NoError(t, os.WriteFile(logPath, []byte("only\ntwo"), 0o600))

		got, err := readLogFile(logPath, 10)
		require.NoError(t, err)
		assert.Equal(t, "only\ntwo", got)
	})
}

func TestEntryToContainerInfo(t *testing.T) {
	t.Parallel()

	now := time.Now()
	entry := &vmEntry{
		name:      "test",
		image:     "img:v1",
		state:     runtime.WorkloadStatusRunning,
		createdAt: now,
		labels:    map[string]string{"k": "v"},
		ports:     []runtime.PortMapping{{ContainerPort: 8080, HostPort: 19000, Protocol: "tcp"}},
	}

	info := entryToContainerInfo(entry)
	assert.Equal(t, "test", info.Name)
	assert.Equal(t, "img:v1", info.Image)
	assert.Equal(t, string(runtime.WorkloadStatusRunning), info.Status)
	assert.Equal(t, runtime.WorkloadStatusRunning, info.State)
	assert.Equal(t, now, info.Created)
	assert.Equal(t, now, info.StartedAt, "StartedAt should be set to createdAt")
	assert.Equal(t, map[string]string{"k": "v"}, info.Labels)
	assert.Len(t, info.Ports, 1)
}

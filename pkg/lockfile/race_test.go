// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package lockfile

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestAcquireTrackedLock_NoLostUpdates reproduces the unlock/unlink release
// race: many independent lockers (goroutines standing in for separate
// processes) repeatedly acquire the same lock path, read-increment-write a
// shared counter file, and release. Before AcquireTrackedLock's stale-inode
// check, a locker could acquire a lock on an inode already orphaned by a
// concurrent unlink+recreate, letting two "exclusive" critical sections
// overlap and lose an update.
//
//nolint:paralleltest // exercises the shared globalRegistry directly, like the other tests in this package
func TestAcquireTrackedLock_NoLostUpdates(t *testing.T) {
	tempDir := t.TempDir()
	counterPath := filepath.Join(tempDir, "counter")
	lockPath := counterPath + ".lock"
	require.NoError(t, os.WriteFile(counterPath, []byte("0"), 0o600))

	const goroutines = 16
	const iterations = 300

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range iterations {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				lock, locked, err := AcquireTrackedLock(ctx, lockPath, time.Millisecond)
				cancel()
				require.NoError(t, err)
				require.True(t, locked, "failed to acquire lock within timeout")

				data, err := os.ReadFile(counterPath)
				require.NoError(t, err)
				n, err := strconv.Atoi(string(data))
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(counterPath, []byte(strconv.Itoa(n+1)), 0o600))

				ReleaseTrackedLock(lockPath, lock)
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("timeout waiting for lock goroutines to finish")
	}

	data, err := os.ReadFile(counterPath)
	require.NoError(t, err)
	got, err := strconv.Atoi(string(data))
	require.NoError(t, err)
	require.Equal(t, goroutines*iterations, got, "lost updates detected — lock did not provide mutual exclusion")
}

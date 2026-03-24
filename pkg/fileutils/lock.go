// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package fileutils

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/stacklok/toolhive/pkg/lockfile"
)

const (
	// DefaultLockTimeout is the maximum time to wait for a file lock.
	DefaultLockTimeout = 5 * time.Second

	// defaultLockRetryInterval is the interval between lock acquisition attempts.
	defaultLockRetryInterval = 100 * time.Millisecond
)

// processLocks provides per-path in-process mutual exclusion.
// flock(2) does NOT provide mutual exclusion between different file descriptors
// within the same process — only between different processes. Since each call to
// WithFileLock opens a new file descriptor, concurrent goroutines can all acquire
// the flock simultaneously. This in-process mutex ensures serialization within a
// single process, while the flock continues to protect cross-process access.
var processLocks sync.Map

// getProcessLock returns the in-process mutex for the given path,
// creating one if it does not already exist.
func getProcessLock(path string) *sync.Mutex {
	val, _ := processLocks.LoadOrStore(path, &sync.Mutex{})
	return val.(*sync.Mutex)
}

// WithFileLock executes fn while holding both an in-process mutex and an
// OS-level advisory file lock on path + ".lock".
// The in-process mutex serializes goroutines within the same process (where
// flock is ineffective), and the file lock serializes across processes.
func WithFileLock(path string, fn func() error) error {
	// Acquire in-process lock first to serialize goroutines.
	mu := getProcessLock(path)
	mu.Lock()
	defer mu.Unlock()

	lockPath := path + ".lock"
	fileLock := lockfile.NewTrackedLock(lockPath)

	ctx, cancel := context.WithTimeout(context.Background(), DefaultLockTimeout)
	defer cancel()

	locked, err := fileLock.TryLockContext(ctx, defaultLockRetryInterval)
	if err != nil {
		return fmt.Errorf("failed to acquire lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("failed to acquire lock: timeout after %v", DefaultLockTimeout)
	}
	defer lockfile.ReleaseTrackedLock(lockPath, fileLock)

	return fn()
}

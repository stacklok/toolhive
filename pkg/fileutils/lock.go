// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package fileutils

import (
	"context"
	"fmt"
	"time"

	"github.com/stacklok/toolhive/pkg/lockfile"
)

const (
	// DefaultLockTimeout is the maximum time to wait for a file lock.
	DefaultLockTimeout = 5 * time.Second

	// defaultLockRetryInterval is the interval between lock acquisition attempts.
	defaultLockRetryInterval = 100 * time.Millisecond
)

// WithFileLock executes fn while holding an OS-level advisory file lock on path + ".lock".
// It uses a 1-second timeout with 100ms retry interval.
func WithFileLock(path string, fn func() error) error {
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

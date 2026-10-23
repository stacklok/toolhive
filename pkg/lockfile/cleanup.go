// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package lockfile provides utilities for managing file locks and cleanup.
package lockfile

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

var (
	// globalRegistry holds all active lock files for cleanup
	globalRegistry = &lockRegistry{
		locks: make(map[string]*flock.Flock),
	}
)

// lockRegistry manages active file locks for cleanup purposes
type lockRegistry struct {
	mu    sync.RWMutex
	locks map[string]*flock.Flock
}

// RegisterLock adds a lock to the global registry for cleanup
func (lr *lockRegistry) RegisterLock(lockPath string, lock *flock.Flock) {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	lr.locks[lockPath] = lock
}

// UnregisterLock removes a lock from the global registry
func (lr *lockRegistry) UnregisterLock(lockPath string) {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	delete(lr.locks, lockPath)
}

// CleanupAll unlocks and removes all registered lock files
func (lr *lockRegistry) CleanupAll() {
	lr.mu.Lock()
	defer lr.mu.Unlock()

	for lockPath, lock := range lr.locks {
		if err := lock.Unlock(); err != nil && !os.IsNotExist(err) {
			slog.Warn("failed to unlock file", "path", lockPath, "error", err)
		}

		if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
			slog.Warn("failed to remove lock file", "path", lockPath, "error", err)
		}
	}

	// Clear the registry
	lr.locks = make(map[string]*flock.Flock)
}

// NewTrackedLock creates a new file lock and registers it for cleanup
func NewTrackedLock(lockPath string) *flock.Flock {
	lock := flock.New(lockPath)
	globalRegistry.RegisterLock(lockPath, lock)
	return lock
}

// ReleaseTrackedLock unlocks, removes, and unregisters a lock file.
//
// The path is unlinked before the flock is released. flock(2) locks attach to
// an inode, not a path, so releasing in the other order leaves a window where
// a waiter can flock this soon-to-be-removed inode and then have the path
// pulled out from under it by this call's Remove, while a third locker
// creates a fresh inode at the same path — two lockers now believe they hold
// "the" lock. Unlinking first means anyone who opens lockPath from here on
// necessarily gets a new inode, which is what AcquireTrackedLock's identity
// check depends on to notice and retry past a stale lock.
func ReleaseTrackedLock(lockPath string, lock *flock.Flock) {
	if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		slog.Warn("failed to remove lock file", "path", lockPath, "error", err)
	}

	if err := lock.Unlock(); err != nil && !os.IsNotExist(err) {
		slog.Warn("failed to unlock file", "path", lockPath, "error", err)
	}

	globalRegistry.UnregisterLock(lockPath)
}

// AcquireTrackedLock acquires an exclusive lock on lockPath and registers it
// for cleanup, the way NewTrackedLock+TryLockContext do, but additionally
// guards against acquiring a lock on a stale, already-orphaned inode.
//
// Because flock(2) locks an open file description on an inode rather than a
// path, and acquisition (open, then flock) and release (unlink, then unlock —
// see ReleaseTrackedLock) are separate steps, a caller can open lockPath and
// still be blocked in flock() at the moment another holder unlinks and
// releases: that other holder's Remove can land between this caller's open()
// and its successful flock(), or a concurrent third party can recreate
// lockPath under a new inode while this caller's flock succeeds on the old,
// now-unreachable one. Either way this caller would believe it holds "the"
// lock for lockPath while actually holding a lock nothing else will ever
// contend for again. After acquiring, this function checks that the locked
// file's inode is still the one lockPath resolves to (os.SameFile); on
// mismatch it discards the stale lock and retries with a fresh open, bounded
// by ctx.
func AcquireTrackedLock(ctx context.Context, lockPath string, retryDelay time.Duration) (*flock.Flock, bool, error) {
	for {
		lock := flock.New(lockPath)

		locked, err := lock.TryLockContext(ctx, retryDelay)
		if err != nil {
			return nil, false, err
		}
		if !locked {
			return nil, false, nil
		}

		if lockMatchesPath(lock, lockPath) {
			globalRegistry.RegisterLock(lockPath, lock)
			return lock, true, nil
		}

		if err := lock.Unlock(); err != nil && !os.IsNotExist(err) {
			slog.Warn("failed to unlock stale lock file", "path", lockPath, "error", err)
		}
		if ctx.Err() != nil {
			return nil, false, nil
		}
	}
}

// lockMatchesPath reports whether the inode lock currently holds is still the
// one lockPath resolves to, i.e. that the lock has not been orphaned by a
// concurrent unlink (and possibly recreation) of lockPath.
func lockMatchesPath(lock *flock.Flock, lockPath string) bool {
	heldInfo, err := lock.Stat()
	if err != nil {
		return false
	}

	pathInfo, err := os.Stat(lockPath)
	if err != nil {
		return false
	}

	return os.SameFile(heldInfo, pathInfo)
}

// CleanupAllLocks provides global cleanup of all registered lock files
func CleanupAllLocks() {
	globalRegistry.CleanupAll()
}

// CleanupStaleLocks removes stale lock files from the specified directories
// A lock file is considered stale if it's older than the maxAge duration
func CleanupStaleLocks(directories []string, maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)

	for _, dir := range directories {
		matches, err := filepath.Glob(filepath.Join(dir, "*.lock"))
		if err != nil {
			slog.Warn("failed to glob lock files", "dir", dir, "error", err)
			continue
		}

		for _, lockFile := range matches {
			info, err := os.Stat(lockFile)
			if err != nil {
				continue // File may have been removed already
			}

			if info.ModTime().Before(cutoff) {
				// Try to acquire the lock to check if it's really stale
				lock := flock.New(lockFile)
				if locked, err := lock.TryLock(); err == nil && locked {
					// Lock was acquired, so it was stale
					if err := lock.Unlock(); err != nil && !os.IsNotExist(err) {
						slog.Warn("failed to unlock stale lock file", "path", lockFile, "error", err)
					}
					if err := os.Remove(lockFile); err != nil && !os.IsNotExist(err) {
						slog.Warn("failed to remove stale lock file", "path", lockFile, "error", err)
					} else {
						slog.Debug("removed stale lock file", "path", lockFile)
					}
				}
			}
		}
	}
}

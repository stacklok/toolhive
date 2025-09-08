// Package lockfile provides utilities for managing file locks and cleanup.
package lockfile

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"

	"github.com/stacklok/toolhive/pkg/logger"
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
			logger.Warnf("failed to unlock file %s: %v", lockPath, err)
		}

		if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
			logger.Warnf("failed to remove lock file %s: %v", lockPath, err)
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

// ReleaseTrackedLock unlocks, removes, and unregisters a lock file
func ReleaseTrackedLock(lockPath string, lock *flock.Flock) {
	if err := lock.Unlock(); err != nil && !os.IsNotExist(err) {
		logger.Warnf("failed to unlock file %s: %v", lockPath, err)
	}

	if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		logger.Warnf("failed to remove lock file %s: %v", lockPath, err)
	}

	globalRegistry.UnregisterLock(lockPath)
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
			logger.Warnf("failed to glob lock files in %s: %v", dir, err)
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
						logger.Warnf("failed to unlock stale lock file %s: %v", lockFile, err)
					}
					if err := os.Remove(lockFile); err != nil && !os.IsNotExist(err) {
						logger.Warnf("failed to remove stale lock file %s: %v", lockFile, err)
					} else {
						logger.Debugf("removed stale lock file: %s", lockFile)
					}
				}
			}
		}
	}
}

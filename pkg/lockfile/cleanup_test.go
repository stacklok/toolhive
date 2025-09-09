package lockfile

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/logger"
)

func init() {
	// Initialize logger for all tests
	logger.Initialize()
}

func TestLockRegistry_RegisterLock(t *testing.T) {
	t.Parallel()

	registry := &lockRegistry{
		locks: make(map[string]*flock.Flock),
	}

	lockPath := "/test/path/file.lock"
	lock := flock.New(lockPath)

	registry.RegisterLock(lockPath, lock)

	registry.mu.RLock()
	defer registry.mu.RUnlock()

	assert.Contains(t, registry.locks, lockPath)
	assert.Equal(t, lock, registry.locks[lockPath])
}

func TestLockRegistry_UnregisterLock(t *testing.T) {
	t.Parallel()

	registry := &lockRegistry{
		locks: make(map[string]*flock.Flock),
	}

	lockPath := "/test/path/file.lock"
	lock := flock.New(lockPath)

	// First register the lock
	registry.RegisterLock(lockPath, lock)

	// Verify it's registered
	registry.mu.RLock()
	assert.Contains(t, registry.locks, lockPath)
	registry.mu.RUnlock()

	// Unregister the lock
	registry.UnregisterLock(lockPath)

	// Verify it's unregistered
	registry.mu.RLock()
	assert.NotContains(t, registry.locks, lockPath)
	registry.mu.RUnlock()
}

func TestLockRegistry_CleanupAll(t *testing.T) {
	t.Parallel()

	// Create temporary directory for test lock files
	tempDir, err := os.MkdirTemp("", "lockfile-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	registry := &lockRegistry{
		locks: make(map[string]*flock.Flock),
	}

	// Create multiple test lock files
	lockPaths := make([]string, 3)
	locks := make([]*flock.Flock, 3)

	for i := 0; i < 3; i++ {
		lockPaths[i] = filepath.Join(tempDir, "test"+string(rune('1'+i))+".lock")
		locks[i] = flock.New(lockPaths[i])

		// Create and lock the file
		require.NoError(t, locks[i].Lock())
		registry.RegisterLock(lockPaths[i], locks[i])
	}

	// Verify all locks are registered
	registry.mu.RLock()
	assert.Len(t, registry.locks, 3)
	registry.mu.RUnlock()

	// Cleanup all locks
	registry.CleanupAll()

	// Verify registry is empty
	registry.mu.RLock()
	assert.Len(t, registry.locks, 0)
	registry.mu.RUnlock()

	// Verify lock files are removed (best effort, may not always succeed in tests)
	for _, lockPath := range lockPaths {
		_, err := os.Stat(lockPath)
		assert.True(t, os.IsNotExist(err), "Lock file should be removed: %s", lockPath)
	}
}

//nolint:paralleltest // Modifies global state, cannot run in parallel
func TestNewTrackedLock(t *testing.T) {
	// Don't run this test in parallel since it modifies global state
	// t.Parallel()

	// Save original registry and restore after test
	origRegistry := globalRegistry
	defer func() { globalRegistry = origRegistry }()

	// Create a new registry for this test
	globalRegistry = &lockRegistry{
		locks: make(map[string]*flock.Flock),
	}

	lockPath := "/test/path/tracked.lock"
	lock := NewTrackedLock(lockPath)

	assert.NotNil(t, lock)

	// Verify lock is registered
	globalRegistry.mu.RLock()
	assert.Contains(t, globalRegistry.locks, lockPath)
	assert.Equal(t, lock, globalRegistry.locks[lockPath])
	globalRegistry.mu.RUnlock()
}

//nolint:paralleltest // Modifies global state, cannot run in parallel
func TestReleaseTrackedLock(t *testing.T) {
	// Don't run this test in parallel since it modifies global state
	// t.Parallel()

	// Create temporary directory for test lock files
	tempDir, err := os.MkdirTemp("", "lockfile-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Save original registry and restore after test
	origRegistry := globalRegistry
	defer func() { globalRegistry = origRegistry }()

	// Create a new registry for this test
	globalRegistry = &lockRegistry{
		locks: make(map[string]*flock.Flock),
	}

	lockPath := filepath.Join(tempDir, "tracked.lock")
	lock := NewTrackedLock(lockPath)

	// Lock the file to create it
	require.NoError(t, lock.Lock())

	// Verify lock file exists
	_, err = os.Stat(lockPath)
	require.NoError(t, err)

	// Release the tracked lock
	ReleaseTrackedLock(lockPath, lock)

	// Verify lock is unregistered
	globalRegistry.mu.RLock()
	assert.NotContains(t, globalRegistry.locks, lockPath)
	globalRegistry.mu.RUnlock()

	// Verify lock file is removed
	_, err = os.Stat(lockPath)
	assert.True(t, os.IsNotExist(err), "Lock file should be removed")
}

//nolint:paralleltest // Modifies global state, cannot run in parallel
func TestCleanupAllLocks(t *testing.T) {
	// Don't run this test in parallel since it modifies global state
	// t.Parallel()

	// Create temporary directory for test lock files
	tempDir, err := os.MkdirTemp("", "lockfile-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Save original registry and restore after test
	origRegistry := globalRegistry
	defer func() { globalRegistry = origRegistry }()

	// Create a new registry for this test
	globalRegistry = &lockRegistry{
		locks: make(map[string]*flock.Flock),
	}

	// Create multiple tracked locks
	lockPaths := make([]string, 3)
	locks := make([]*flock.Flock, 3)

	for i := 0; i < 3; i++ {
		lockPaths[i] = filepath.Join(tempDir, "global"+string(rune('1'+i))+".lock")
		locks[i] = NewTrackedLock(lockPaths[i])
		require.NoError(t, locks[i].Lock())
	}

	// Verify all locks are registered
	globalRegistry.mu.RLock()
	assert.Len(t, globalRegistry.locks, 3)
	globalRegistry.mu.RUnlock()

	// Cleanup all locks
	CleanupAllLocks()

	// Verify registry is empty
	globalRegistry.mu.RLock()
	assert.Len(t, globalRegistry.locks, 0)
	globalRegistry.mu.RUnlock()

	// Verify lock files are removed
	for _, lockPath := range lockPaths {
		_, err := os.Stat(lockPath)
		assert.True(t, os.IsNotExist(err), "Lock file should be removed: %s", lockPath)
	}
}

func TestCleanupStaleLocks(t *testing.T) {
	t.Parallel()

	// Create temporary directory for test lock files
	tempDir, err := os.MkdirTemp("", "lockfile-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Create stale lock files (older than maxAge)
	staleLockPath := filepath.Join(tempDir, "stale.lock")
	staleLock := flock.New(staleLockPath)
	require.NoError(t, staleLock.Lock())
	require.NoError(t, staleLock.Unlock()) // Unlock immediately to make it stale

	// Modify the file's timestamp to make it appear old
	oldTime := time.Now().Add(-10 * time.Minute)
	require.NoError(t, os.Chtimes(staleLockPath, oldTime, oldTime))

	// Create a fresh lock file (newer than maxAge)
	freshLockPath := filepath.Join(tempDir, "fresh.lock")
	freshLock := flock.New(freshLockPath)
	require.NoError(t, freshLock.Lock())
	defer freshLock.Unlock()

	// Create an active lock file that's old but currently locked
	activeLockPath := filepath.Join(tempDir, "active.lock")
	activeLock := flock.New(activeLockPath)
	require.NoError(t, activeLock.Lock())
	defer activeLock.Unlock()

	// Make it appear old
	require.NoError(t, os.Chtimes(activeLockPath, oldTime, oldTime))

	// Run cleanup with 5-minute max age
	CleanupStaleLocks([]string{tempDir}, 5*time.Minute)

	// Verify stale lock is removed
	_, err = os.Stat(staleLockPath)
	assert.True(t, os.IsNotExist(err), "Stale lock file should be removed")

	// Verify fresh lock still exists
	_, err = os.Stat(freshLockPath)
	assert.NoError(t, err, "Fresh lock file should still exist")

	// Verify active lock still exists (even though it's old, it's locked)
	_, err = os.Stat(activeLockPath)
	assert.NoError(t, err, "Active lock file should still exist")
}

func TestCleanupStaleLocks_NonexistentDirectory(t *testing.T) {
	t.Parallel()

	nonexistentDir := "/this/directory/does/not/exist"

	// Should not panic or error when given a nonexistent directory
	assert.NotPanics(t, func() {
		CleanupStaleLocks([]string{nonexistentDir}, 5*time.Minute)
	})
}

func TestCleanupStaleLocks_EmptyDirectoryList(t *testing.T) {
	t.Parallel()

	// Should handle empty directory list gracefully
	assert.NotPanics(t, func() {
		CleanupStaleLocks([]string{}, 5*time.Minute)
	})
}

func TestLockRegistry_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	registry := &lockRegistry{
		locks: make(map[string]*flock.Flock),
	}

	const numGoroutines = 10
	const numOperations = 50

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// Launch multiple goroutines that register and unregister locks concurrently
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()

			for j := 0; j < numOperations; j++ {
				lockPath := filepath.Join("/test", "concurrent", "lock_"+string(rune(id))+"_"+string(rune(j))+".lock")
				lock := flock.New(lockPath)

				// Register lock
				registry.RegisterLock(lockPath, lock)

				// Brief pause to allow other goroutines to interleave
				time.Sleep(time.Microsecond)

				// Unregister lock
				registry.UnregisterLock(lockPath)
			}
		}(i)
	}

	wg.Wait()

	// Verify registry is empty after all operations
	registry.mu.RLock()
	assert.Len(t, registry.locks, 0)
	registry.mu.RUnlock()
}

func TestCleanupStaleLocks_WithActiveFiles(t *testing.T) {
	t.Parallel()

	// Create temporary directory for test lock files
	tempDir, err := os.MkdirTemp("", "lockfile-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Create a subdirectory to test nested directory handling
	subDir := filepath.Join(tempDir, "subdir")
	require.NoError(t, os.MkdirAll(subDir, 0755))

	// Create various lock files
	testCases := []struct {
		name     string
		path     string
		age      time.Duration
		locked   bool
		expected bool // true if should be removed
	}{
		{"old_unlocked_root", filepath.Join(tempDir, "old_unlocked.lock"), 10 * time.Minute, false, true},
		{"old_locked_root", filepath.Join(tempDir, "old_locked.lock"), 10 * time.Minute, true, false},
		{"new_unlocked_root", filepath.Join(tempDir, "new_unlocked.lock"), 1 * time.Minute, false, false},
		{"new_locked_root", filepath.Join(tempDir, "new_locked.lock"), 1 * time.Minute, true, false},
		{"old_unlocked_sub", filepath.Join(subDir, "old_unlocked.lock"), 10 * time.Minute, false, true},
	}

	var locks []*flock.Flock
	defer func() {
		// Cleanup any remaining locks
		for _, lock := range locks {
			lock.Unlock()
		}
	}()

	for _, tc := range testCases {
		lock := flock.New(tc.path)
		require.NoError(t, lock.Lock(), "Failed to create lock for %s", tc.name)

		if !tc.locked {
			require.NoError(t, lock.Unlock(), "Failed to unlock %s", tc.name)
		} else {
			locks = append(locks, lock) // Keep locked for cleanup
		}

		// Set file age
		fileTime := time.Now().Add(-tc.age)
		require.NoError(t, os.Chtimes(tc.path, fileTime, fileTime), "Failed to set time for %s", tc.name)
	}

	// Run cleanup
	CleanupStaleLocks([]string{tempDir, subDir}, 5*time.Minute)

	// Verify results
	for _, tc := range testCases {
		_, err := os.Stat(tc.path)
		if tc.expected {
			assert.True(t, os.IsNotExist(err), "File %s should be removed", tc.name)
		} else {
			assert.NoError(t, err, "File %s should still exist", tc.name)
		}
	}
}

//nolint:paralleltest // Modifies global state, cannot run in parallel
func TestReleaseTrackedLock_AlreadyUnlocked(t *testing.T) {
	// Don't run this test in parallel since it modifies global state
	// t.Parallel()

	// Create temporary directory for test lock files
	tempDir, err := os.MkdirTemp("", "lockfile-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Save original registry and restore after test
	origRegistry := globalRegistry
	defer func() { globalRegistry = origRegistry }()

	// Create a new registry for this test
	globalRegistry = &lockRegistry{
		locks: make(map[string]*flock.Flock),
	}

	lockPath := filepath.Join(tempDir, "already_unlocked.lock")
	lock := NewTrackedLock(lockPath)

	// Lock and then immediately unlock to simulate already unlocked scenario
	require.NoError(t, lock.Lock())
	require.NoError(t, lock.Unlock())

	// ReleaseTrackedLock should handle already unlocked files gracefully
	assert.NotPanics(t, func() {
		ReleaseTrackedLock(lockPath, lock)
	})

	// Verify lock is unregistered
	globalRegistry.mu.RLock()
	assert.NotContains(t, globalRegistry.locks, lockPath)
	globalRegistry.mu.RUnlock()
}

//nolint:paralleltest // Modifies global state, cannot run in parallel
func TestCleanupAllLocks_EmptyRegistry(t *testing.T) {
	// Don't run this test in parallel since it modifies global state
	// t.Parallel()

	// Save original registry and restore after test
	origRegistry := globalRegistry
	defer func() { globalRegistry = origRegistry }()

	// Create an empty registry for this test
	globalRegistry = &lockRegistry{
		locks: make(map[string]*flock.Flock),
	}

	// Should handle empty registry gracefully
	assert.NotPanics(t, func() {
		CleanupAllLocks()
	})

	// Verify registry remains empty
	globalRegistry.mu.RLock()
	assert.Len(t, globalRegistry.locks, 0)
	globalRegistry.mu.RUnlock()
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package keyring

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testValue = "test-value"

func TestKeyctlProvider_DeleteAllNoDeadlock(t *testing.T) {
	t.Parallel()

	provider, err := NewKeyctlProvider()
	if err != nil {
		t.Skip("keyctl not available:", err)
	}

	keyctlProv, ok := provider.(*keyctlProvider)
	require.True(t, ok, "expected keyctlProvider type")

	service := "toolhive-deadlock-test"
	key := "test-key"

	// Ensure cleanup even if test fails
	t.Cleanup(func() {
		_ = keyctlProv.DeleteAll(service)
	})

	err = keyctlProv.Set(service, key, testValue)
	require.NoError(t, err, "failed to set test key")

	// DeleteAll should complete without deadlocking
	// Use a timeout to detect deadlock
	done := make(chan struct{})
	go func() {
		defer close(done)
		err = keyctlProv.DeleteAll(service)
	}()

	select {
	case <-done:
		// Success - no deadlock
		assert.NoError(t, err, "DeleteAll should succeed")
	case <-time.After(5 * time.Second):
		t.Fatal("DeleteAll deadlocked - timeout after 5 seconds")
	}

	// Verify the key was deleted
	_, err = keyctlProv.Get(service, key)
	assert.ErrorIs(t, err, ErrNotFound, "key should be deleted")
}

func TestKeyctlProvider_DeleteAllCrossProcess(t *testing.T) {
	t.Parallel()

	// This test verifies that DeleteAll works even when the key was set by
	// a different process (simulated by creating a new provider instance
	// which has an empty in-memory map)

	provider1, err := NewKeyctlProvider()
	if err != nil {
		t.Skip("keyctl not available:", err)
	}

	keyctlProv1, ok := provider1.(*keyctlProvider)
	require.True(t, ok)

	service := "toolhive-crossprocess-test"
	key := service // Using service:service pattern

	// Ensure cleanup even if test fails
	t.Cleanup(func() {
		_ = keyctlProv1.DeleteAll(service)
	})

	err = keyctlProv1.Set(service, key, testValue)
	require.NoError(t, err, "failed to set test key")

	// Create a new provider instance (simulates Process B with empty in-memory map)
	provider2, err := NewKeyctlProvider()
	require.NoError(t, err)

	keyctlProv2, ok := provider2.(*keyctlProvider)
	require.True(t, ok)

	// Verify the in-memory map is empty for provider2
	assert.Empty(t, keyctlProv2.keys, "new provider should have empty in-memory map")

	// DeleteAll should still work because it searches the kernel keyring directly
	err = keyctlProv2.DeleteAll(service)
	assert.NoError(t, err, "DeleteAll should succeed even with empty in-memory map")

	// Verify the key was deleted from kernel keyring (check via any provider)
	_, err = keyctlProv1.Get(service, key)
	assert.ErrorIs(t, err, ErrNotFound, "key should be deleted from kernel keyring")
}

func TestKeyctlProvider_ConcurrentDeleteAll(t *testing.T) {
	t.Parallel()

	provider, err := NewKeyctlProvider()
	if err != nil {
		t.Skip("keyctl not available:", err)
	}

	keyctlProv, ok := provider.(*keyctlProvider)
	require.True(t, ok)

	service := "toolhive-concurrent-test"

	// Ensure cleanup even if test fails
	t.Cleanup(func() {
		_ = keyctlProv.DeleteAll(service)
	})

	// Set up multiple keys
	for i := 0; i < 5; i++ {
		err := keyctlProv.Set(service, GenerateUniqueTestKey(), "value")
		require.NoError(t, err)
	}

	// Run multiple concurrent DeleteAll calls
	var wg sync.WaitGroup
	errChan := make(chan error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := keyctlProv.DeleteAll(service); err != nil {
				errChan <- err
			}
		}()
	}

	wg.Wait()
	close(errChan)

	// Check for any errors
	for err := range errChan {
		t.Errorf("concurrent DeleteAll failed: %v", err)
	}
}

func TestKeyctlProvider_DeleteAllWithMultipleKeys(t *testing.T) {
	t.Parallel()

	provider, err := NewKeyctlProvider()
	if err != nil {
		t.Skip("keyctl not available:", err)
	}

	keyctlProv, ok := provider.(*keyctlProvider)
	require.True(t, ok)

	service := "toolhive-multikey-test"
	keys := []string{"key1", "key2", "key3"}

	// Ensure cleanup even if test fails
	t.Cleanup(func() {
		_ = keyctlProv.DeleteAll(service)
	})

	for _, key := range keys {
		err := keyctlProv.Set(service, key, "value-"+key)
		require.NoError(t, err, "failed to set key: "+key)
	}

	// DeleteAll should remove all keys
	err = keyctlProv.DeleteAll(service)
	assert.NoError(t, err, "DeleteAll should succeed")

	// Verify all keys were deleted
	for _, key := range keys {
		_, err := keyctlProv.Get(service, key)
		assert.ErrorIs(t, err, ErrNotFound, "key should be deleted: "+key)
	}
}

func TestKeyctlProvider_DeleteUnlocked(t *testing.T) {
	t.Parallel()

	provider, err := NewKeyctlProvider()
	if err != nil {
		t.Skip("keyctl not available:", err)
	}

	keyctlProv, ok := provider.(*keyctlProvider)
	require.True(t, ok)

	service := "toolhive-unlocked-test"
	key := "test-key"

	// Ensure cleanup even if test fails
	t.Cleanup(func() {
		_ = keyctlProv.DeleteAll(service)
	})

	err = keyctlProv.Set(service, key, testValue)
	require.NoError(t, err)

	// Test deleteKeyUnlocked directly (need to acquire lock manually for test)
	keyctlProv.mu.Lock()
	err = keyctlProv.deleteKeyUnlocked(service, key)
	keyctlProv.mu.Unlock()

	assert.NoError(t, err, "deleteKeyUnlocked should succeed")

	// Verify the key was deleted
	_, err = keyctlProv.Get(service, key)
	assert.ErrorIs(t, err, ErrNotFound, "key should be deleted")
}

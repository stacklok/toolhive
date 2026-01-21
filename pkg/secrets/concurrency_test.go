// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package secrets_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/secrets/keyring"
)

// TestConcurrentProviderCreation tests that multiple goroutines can safely
// create a secrets provider simultaneously without conflicts.
func TestConcurrentKeyringAvailability(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping concurrency test in short mode")
	}

	const numGoroutines = 10
	const numIterations = 5

	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines*numIterations)

	// Start multiple goroutines that concurrently check keyring availability
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()

			for j := 0; j < numIterations; j++ {
				_, err := secrets.CreateSecretProvider(secrets.EnvironmentType)
				if err != nil {
					errors <- fmt.Errorf("goroutine %d, iteration %d: CreateSecretProvider failed: %w", goroutineID, j, err)
					continue
				}
			}
		}(i)
	}

	// Wait for all goroutines to complete
	wg.Wait()
	close(errors)

	// Check for errors
	var errorList []error
	for err := range errors {
		errorList = append(errorList, err)
	}
	// We expect all operations to succeed - no errors should occur
	assert.Empty(t, errorList, "Expected no errors during concurrent keyring availability checks, but got %d errors", len(errorList))

	// All provider creation operations should succeed
	assert.Empty(t, errorList)
}

// TestConcurrentUniqueKeyGeneration tests that concurrent calls to
// generateUniqueTestKey produce unique keys.
func TestConcurrentUniqueKeyGeneration(t *testing.T) {
	t.Parallel()
	const numGoroutines = 20
	const keysPerGoroutine = 10

	var wg sync.WaitGroup
	allKeys := make(chan string, numGoroutines*keysPerGoroutine)

	// Start multiple goroutines generating keys concurrently
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for j := 0; j < keysPerGoroutine; j++ {
				key := keyring.GenerateUniqueTestKey()
				allKeys <- key
			}
		}()
	}

	// Wait for all goroutines to complete
	wg.Wait()
	close(allKeys)

	// Collect all keys and check for duplicates
	keys := make(map[string]bool)
	duplicateCount := 0

	for key := range allKeys {
		if keys[key] {
			duplicateCount++
			t.Errorf("Found duplicate key: %s", key)
		}
		keys[key] = true
	}

	expectedTotal := numGoroutines * keysPerGoroutine
	assert.Equal(t, expectedTotal, len(keys), "Expected %d unique keys, got %d", expectedTotal, len(keys))
	assert.Equal(t, 0, duplicateCount, "Found %d duplicate keys", duplicateCount)
}

// TestSequentialConcurrency tests that rapid sequential calls to keyring functions
// don't cause race conditions or resource conflicts.
func TestSequentialConcurrency(t *testing.T) {
	t.Parallel()
	const numOperations = 20
	var errorList []error
	successCount := 0

	// Perform rapid sequential operations that could cause race conditions
	for i := 0; i < numOperations; i++ {
		provider, err := secrets.CreateSecretProvider(secrets.EnvironmentType)
		if err != nil {
			errorList = append(errorList, fmt.Errorf("operation %d: CreateSecretProvider failed: %w", i, err))
			continue
		}

		// Test provider operations
		if provider != nil {
			_, _ = provider.GetSecret(context.Background(), "non-existent-key")
			successCount++
		}
	}

	// We expect all operations to succeed - no errors should occur
	assert.Empty(t, errorList, "Expected no errors during sequential operations, but got %d errors", len(errorList))

	// All operations should succeed
	assert.Equal(t, numOperations, successCount,
		"Expected %d successful sequential operations, got %d", numOperations, successCount)
}

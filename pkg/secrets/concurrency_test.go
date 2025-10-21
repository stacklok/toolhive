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

// TestConcurrentKeyringAvailability tests that multiple goroutines can safely
// check keyring availability simultaneously without conflicts.
func TestConcurrentKeyringAvailability(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping concurrency test in short mode")
	}

	const numGoroutines = 20
	const numIterations = 5

	var wg sync.WaitGroup
	results := make(chan bool, numGoroutines*numIterations)
	errors := make(chan error, numGoroutines*numIterations)

	// Start multiple goroutines that concurrently check keyring availability
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()

			for j := 0; j < numIterations; j++ {
				// Test IsKeyringAvailable
				available := secrets.IsKeyringAvailable()
				results <- available

				// Test CreateSecretProvider (which internally calls IsKeyringAvailable)
				provider, err := secrets.CreateSecretProvider(secrets.NoneType)
				if err != nil {
					errors <- fmt.Errorf("goroutine %d, iteration %d: CreateSecretProvider failed: %w", goroutineID, j, err)
					continue
				}

				// Test provider operations
				if provider != nil {
					// Test a simple operation to ensure provider is working
					_, _ = provider.GetSecret(context.Background(), "non-existent-key")
				}
			}
		}(i)
	}

	// Wait for all goroutines to complete
	wg.Wait()
	close(results)
	close(errors)

	// Check results
	successCount := 0
	for result := range results {
		if result {
			successCount++
		}
	}

	// Check for errors
	var errorList []error
	for err := range errors {
		errorList = append(errorList, err)
	}
	// We expect all operations to succeed - no errors should occur
	assert.Empty(t, errorList, "Expected no errors during concurrent keyring availability checks, but got %d errors", len(errorList))

	// All keyring availability checks should succeed
	expectedSuccess := numGoroutines * numIterations
	assert.Equal(t, expectedSuccess, successCount,
		"Expected %d successful keyring availability checks, got %d", expectedSuccess, successCount)
}

// TestConcurrentKeyringOperations tests that multiple goroutines can safely
// perform keyring operations simultaneously using the same provider.
func TestConcurrentKeyringOperations(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping concurrency test in short mode")
	}

	// Create a keyring provider directly
	keyringProvider := keyring.NewCompositeProvider()
	if !keyringProvider.IsAvailable() {
		t.Skip("Skipping test: keyring not available")
	}

	const numGoroutines = 10
	const numOperations = 3

	var wg sync.WaitGroup
	var mu sync.Mutex
	var errorList []error
	successCount := 0

	// Start multiple goroutines that perform concurrent keyring operations
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()

			for j := 0; j < numOperations; j++ {
				// Use unique keys for each operation to avoid conflicts
				service := fmt.Sprintf("test-service-%d", goroutineID)
				key := fmt.Sprintf("test-key-%d-%d", goroutineID, j)
				value := fmt.Sprintf("test-value-%d-%d", goroutineID, j)

				// Test Set operation
				err := keyringProvider.Set(service, key, value)
				if err != nil {
					mu.Lock()
					errorList = append(errorList, fmt.Errorf("goroutine %d, iteration %d, Set: %w", goroutineID, j, err))
					mu.Unlock()
					continue
				}

				// Test Get operation
				retrievedValue, err := keyringProvider.Get(service, key)
				if err != nil {
					mu.Lock()
					errorList = append(errorList, fmt.Errorf("goroutine %d, iteration %d, Get: %w", goroutineID, j, err))
					mu.Unlock()
					continue
				}

				if retrievedValue == value {
					mu.Lock()
					successCount++
					mu.Unlock()
				}

				// Test Delete operation
				err = keyringProvider.Delete(service, key)
				if err != nil {
					mu.Lock()
					errorList = append(errorList, fmt.Errorf("goroutine %d, iteration %d, Delete: %w", goroutineID, j, err))
					mu.Unlock()
				}
			}
		}(i)
	}

	// Wait for all goroutines to complete
	wg.Wait()

	// We expect all operations to succeed - no errors should occur with our fix
	assert.Empty(t, errorList, "Expected no errors during concurrent keyring operations, but got %d errors", len(errorList))

	// All keyring operations should succeed
	expectedSuccess := numGoroutines * numOperations
	assert.Equal(t, expectedSuccess, successCount,
		"Expected %d successful keyring operations, got %d", expectedSuccess, successCount)
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
		// Test IsKeyringAvailable
		available := secrets.IsKeyringAvailable()
		if !available {
			errorList = append(errorList, fmt.Errorf("operation %d: keyring not available", i))
			continue
		}

		// Test CreateSecretProvider
		provider, err := secrets.CreateSecretProvider(secrets.NoneType)
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

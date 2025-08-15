//go:build linux
// +build linux

package keyring

import (
	"testing"
)

func TestKeyctlProvider_Creation(t *testing.T) {
	t.Parallel()
	provider, err := NewKeyctlProvider()
	if err != nil {
		t.Skipf("Keyctl provider creation failed (may be expected in test environments): %v", err)
		return
	}

	if provider == nil {
		t.Fatal("NewKeyctlProvider should not return nil when no error")
	}

	// Should be the correct type
	_, ok := provider.(*keyctlProvider)
	if !ok {
		t.Fatal("NewKeyctlProvider should return *keyctlProvider")
	}
}

func TestKeyctlProvider_Name(t *testing.T) {
	t.Parallel()
	provider, err := NewKeyctlProvider()
	if err != nil {
		t.Skipf("Keyctl provider creation failed: %v", err)
		return
	}

	name := provider.Name()
	if name != "Linux Keyctl" {
		t.Errorf("expected 'Linux Keyctl', got '%s'", name)
	}
}

func TestKeyctlProvider_BasicOperations(t *testing.T) {
	t.Parallel()
	provider, err := NewKeyctlProvider()
	if err != nil {
		t.Skipf("Keyctl provider creation failed: %v", err)
		return
	}

	if !provider.IsAvailable() {
		t.Skip("Keyctl provider not available in test environment")
		return
	}

	testService := "toolhive-test"
	testKey := "test-key"
	testValue := "test-value"

	t.Run("Set and Get", func(t *testing.T) {
		t.Parallel()
		// Set a value
		err := provider.Set(testService, testKey, testValue)
		if err != nil {
			t.Fatalf("Set failed: %v", err)
		}

		// Get the value back
		value, err := provider.Get(testService, testKey)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}

		if value != testValue {
			t.Errorf("expected %s, got %s", testValue, value)
		}
	})

	t.Run("Get_NotFound", func(t *testing.T) {
		t.Parallel()
		_, err := provider.Get("nonexistent-service", "nonexistent-key")
		if err == nil {
			t.Error("expected error for nonexistent key")
		}

		if err != ErrNotFound {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("Delete", func(t *testing.T) {
		t.Parallel()
		// Delete the test key
		err := provider.Delete(testService, testKey)
		if err != nil {
			t.Fatalf("Delete failed: %v", err)
		}

		// Verify it's gone
		_, err = provider.Get(testService, testKey)
		if err != ErrNotFound {
			t.Error("key should not exist after deletion")
		}
	})

	t.Run("DeleteAll", func(t *testing.T) {
		t.Parallel()
		// Set multiple keys
		keys := []string{"key1", "key2", "key3"}
		for _, key := range keys {
			err := provider.Set(testService, key, "value-"+key)
			if err != nil {
				t.Fatalf("failed to set key %s: %v", key, err)
			}
		}

		// Delete all keys for the service
		err := provider.DeleteAll(testService)
		if err != nil {
			t.Fatalf("DeleteAll failed: %v", err)
		}

		// Verify all keys are gone
		for _, key := range keys {
			_, err := provider.Get(testService, key)
			if err != ErrNotFound {
				t.Errorf("key %s should not exist after DeleteAll", key)
			}
		}
	})
}

func TestKeyctlProvider_MultipleServices(t *testing.T) {
	t.Parallel()
	provider, err := NewKeyctlProvider()
	if err != nil {
		t.Skipf("Keyctl provider creation failed: %v", err)
		return
	}

	if !provider.IsAvailable() {
		t.Skip("Keyctl provider not available in test environment")
		return
	}

	services := []string{"service1", "service2", "service3"}
	testKey := "shared-key"

	// Set the same key name in different services
	for i, service := range services {
		value := "value-" + string(rune('A'+i))
		err := provider.Set(service, testKey, value)
		if err != nil {
			t.Fatalf("failed to set key in service %s: %v", service, err)
		}
	}

	// Verify each service has its own value
	for i, service := range services {
		expectedValue := "value-" + string(rune('A'+i))
		value, err := provider.Get(service, testKey)
		if err != nil {
			t.Fatalf("failed to get key from service %s: %v", service, err)
		}
		if value != expectedValue {
			t.Errorf("service %s: expected %s, got %s", service, expectedValue, value)
		}
	}

	// Clean up
	for _, service := range services {
		_ = provider.DeleteAll(service)
	}
}

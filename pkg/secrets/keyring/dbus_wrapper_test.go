package keyring

import (
	"runtime"
	"strings"
	"testing"
)

func TestDbusWrapperProvider_Creation(t *testing.T) {
	t.Parallel()
	provider := NewZalandoKeyringProvider()
	if provider == nil {
		t.Fatal("NewZalandoKeyringProvider should not return nil")
	}

	// Should be the correct type
	_, ok := provider.(*dbusWrapperProvider)
	if !ok {
		t.Fatal("NewZalandoKeyringProvider should return *dbusWrapperProvider")
	}
}

func TestDbusWrapperProvider_Name(t *testing.T) {
	t.Parallel()
	provider := NewZalandoKeyringProvider()
	name := provider.Name()

	if name == "" {
		t.Fatal("provider name should not be empty")
	}

	// Check platform-specific naming
	switch runtime.GOOS {
	case "darwin":
		if !strings.Contains(strings.ToLower(name), "keychain") {
			t.Errorf("expected macOS name to contain 'keychain', got: %s", name)
		}
	case "windows":
		if !strings.Contains(strings.ToLower(name), "credential") {
			t.Errorf("expected Windows name to contain 'credential', got: %s", name)
		}
	case linuxOS:
		if !strings.Contains(strings.ToLower(name), "d-bus") {
			t.Errorf("expected Linux name to contain 'd-bus', got: %s", name)
		}
	default:
		if !strings.Contains(strings.ToLower(name), "keyring") {
			t.Errorf("expected default name to contain 'keyring', got: %s", name)
		}
	}

	t.Logf("Provider name on %s: %s", runtime.GOOS, name)
}

func TestDbusWrapperProvider_Interface(t *testing.T) {
	t.Parallel()
	provider := NewZalandoKeyringProvider()

	// Test that all interface methods exist and handle errors gracefully
	// Note: We can't test actual keyring functionality in CI environments,
	// but we can ensure the interface is properly implemented

	t.Run("IsAvailable", func(t *testing.T) {
		t.Parallel()
		available := provider.IsAvailable()
		t.Logf("IsAvailable: %v", available)
		// Result depends on environment, but should not panic
	})

	t.Run("Set and Get", func(t *testing.T) {
		t.Parallel()
		testService := "toolhive-test"
		testKey := "test-key"
		testValue := "test-value"

		// Try to set a value
		err := provider.Set(testService, testKey, testValue)
		if err != nil {
			t.Logf("Set failed (expected if keyring unavailable): %v", err)
			return // Skip further tests if keyring unavailable
		}

		// Try to get the value back
		value, err := provider.Get(testService, testKey)
		if err != nil {
			t.Errorf("Get failed after successful Set: %v", err)
			return
		}

		if value != testValue {
			t.Errorf("expected %s, got %s", testValue, value)
		}

		// Clean up
		_ = provider.Delete(testService, testKey)
	})

	t.Run("Get_NotFound", func(t *testing.T) {
		t.Parallel()
		_, err := provider.Get("nonexistent-service", "nonexistent-key")
		if err == nil {
			t.Error("expected error for nonexistent key")
		}

		// Should return our custom ErrNotFound for missing keys
		// (though this depends on keyring availability)
		t.Logf("Get nonexistent key error: %v", err)
	})

	t.Run("Delete", func(t *testing.T) {
		t.Parallel()
		// Delete should not error even for nonexistent keys
		err := provider.Delete("test-service", "nonexistent-key")
		if err != nil {
			t.Logf("Delete failed (may be expected): %v", err)
		}
	})

	t.Run("DeleteAll", func(t *testing.T) {
		t.Parallel()
		// DeleteAll should not error even for nonexistent services
		err := provider.DeleteAll("test-service")
		if err != nil {
			t.Logf("DeleteAll failed (may be expected): %v", err)
		}
	})
}

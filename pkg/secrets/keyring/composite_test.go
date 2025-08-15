package keyring

import (
	"runtime"
	"testing"
)

func TestCompositeProvider_Creation(t *testing.T) {
	t.Parallel()
	provider := NewCompositeProvider()
	if provider == nil {
		t.Fatal("NewCompositeProvider should not return nil")
	}

	// Should have a valid name
	name := provider.Name()
	if name == "" {
		t.Fatal("provider name should not be empty")
	}

	// Test that it's actually a composite provider
	composite, ok := provider.(*compositeProvider)
	if !ok {
		t.Fatal("NewCompositeProvider should return a *compositeProvider")
	}

	// Should have at least one provider (the zalando/go-keyring wrapper)
	if len(composite.providers) == 0 {
		t.Fatal("composite provider should have at least one underlying provider")
	}

	// On Linux, should have two providers (zalando + keyctl), elsewhere just one
	expectedProviders := 1
	if runtime.GOOS == linuxOS {
		expectedProviders = 2
	}

	if len(composite.providers) != expectedProviders {
		t.Errorf("expected %d providers on %s, got %d", expectedProviders, runtime.GOOS, len(composite.providers))
	}
}

func TestCompositeProvider_ActiveProviderSelection(t *testing.T) {
	t.Parallel()
	provider := NewCompositeProvider()
	composite := provider.(*compositeProvider)

	// Initially, no active provider should be set
	if composite.active != nil {
		t.Error("active provider should initially be nil")
	}

	// Call getActiveProvider to trigger selection
	active := composite.getActiveProvider()

	// Should now have an active provider (or nil if no providers are available)
	// We can't guarantee which one will be available in test environments,
	// but the selection logic should work
	if active != nil && composite.active != active {
		t.Error("active provider should be set after getActiveProvider call")
	}
}

func TestCompositeProvider_MethodDelegation(t *testing.T) {
	t.Parallel()
	provider := NewCompositeProvider()

	// Test that all methods delegate properly and don't panic
	// Note: We can't test actual functionality without a working keyring,
	// but we can ensure methods don't panic and handle errors gracefully

	t.Run("Set", func(t *testing.T) {
		t.Parallel()
		err := provider.Set("test-service", "test-key", "test-value")
		// Error is expected if no keyring is available
		t.Logf("Set result: %v", err)
	})

	t.Run("Get", func(t *testing.T) {
		t.Parallel()
		_, err := provider.Get("test-service", "test-key")
		// Error is expected if no keyring is available or key not found
		t.Logf("Get result: %v", err)
	})

	t.Run("Delete", func(t *testing.T) {
		t.Parallel()
		err := provider.Delete("test-service", "test-key")
		// Error is expected if no keyring is available
		t.Logf("Delete result: %v", err)
	})

	t.Run("DeleteAll", func(t *testing.T) {
		t.Parallel()
		err := provider.DeleteAll("test-service")
		// Error is expected if no keyring is available
		t.Logf("DeleteAll result: %v", err)
	})

	t.Run("IsAvailable", func(t *testing.T) {
		t.Parallel()
		available := provider.IsAvailable()
		t.Logf("IsAvailable result: %v", available)
	})

	t.Run("Name", func(t *testing.T) {
		t.Parallel()
		name := provider.Name()
		if name == "" {
			t.Error("Name should not return empty string")
		}
		t.Logf("Name result: %s", name)
	})
}

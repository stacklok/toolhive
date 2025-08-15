package keyring

import (
	"testing"
)

// TestProviderInterface ensures all implementations fulfill the Provider interface
func TestProviderInterface(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		fn   func() Provider
	}{
		{
			name: "ZalandoKeyringProvider",
			fn:   func() Provider { return NewZalandoKeyringProvider() },
		},
		{
			name: "CompositeProvider",
			fn:   func() Provider { return NewCompositeProvider() },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider := tt.fn()
			if provider == nil {
				t.Fatal("provider should not be nil")
			}

			// Test that provider implements all interface methods
			// Note: We won't test functionality here, just that methods exist and return appropriate types
			_ = provider.Name()
			_ = provider.IsAvailable()

			// These methods should exist and not panic when called with valid arguments
			err := provider.Set("test-service", "test-key", "test-value")
			if err != nil {
				t.Logf("Set failed (expected if keyring unavailable): %v", err)
			}

			_, err = provider.Get("test-service", "test-key")
			if err != nil {
				t.Logf("Get failed (expected if keyring unavailable or key not found): %v", err)
			}

			err = provider.Delete("test-service", "test-key")
			if err != nil {
				t.Logf("Delete failed (expected if keyring unavailable): %v", err)
			}

			err = provider.DeleteAll("test-service")
			if err != nil {
				t.Logf("DeleteAll failed (expected if keyring unavailable): %v", err)
			}
		})
	}
}

// TestErrNotFound tests that our custom error is properly defined
func TestErrNotFound(t *testing.T) {
	t.Parallel()
	if ErrNotFound == nil {
		t.Fatal("ErrNotFound should not be nil")
	}

	if ErrNotFound.Error() == "" {
		t.Fatal("ErrNotFound should have a non-empty error message")
	}
}

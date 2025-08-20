package keyring

import (
	"os"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isRunningInCI detects if we're running in a CI environment
// by checking for common CI environment variables
func isRunningInCI() bool {
	ciEnvVars := []string{
		"GITHUB_ACTIONS",
		"CI",
		"GITLAB_CI",
		"CIRCLECI",
		"TRAVIS",
		"BUILDKITE",
		"DRONE",
		"CONTINUOUS_INTEGRATION",
	}

	for _, envVar := range ciEnvVars {
		if os.Getenv(envVar) != "" {
			return true
		}
	}
	return false
}

// mockProvider is a test implementation of the Provider interface
type mockProvider struct {
	name      string
	available bool
	setErr    error
	getErr    error
	deleteErr error
	storage   map[string]map[string]string // service -> key -> value
}

func newMockProvider(name string, available bool) *mockProvider {
	return &mockProvider{
		name:      name,
		available: available,
		storage:   make(map[string]map[string]string),
	}
}

func (m *mockProvider) Set(service, key, value string) error {
	if m.setErr != nil {
		return m.setErr
	}
	if m.storage[service] == nil {
		m.storage[service] = make(map[string]string)
	}
	m.storage[service][key] = value
	return nil
}

func (m *mockProvider) Get(service, key string) (string, error) {
	if m.getErr != nil {
		return "", m.getErr
	}
	if serviceMap, exists := m.storage[service]; exists {
		if value, exists := serviceMap[key]; exists {
			return value, nil
		}
	}
	return "", ErrNotFound
}

func (m *mockProvider) Delete(service, key string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	if serviceMap, exists := m.storage[service]; exists {
		delete(serviceMap, key)
		if len(serviceMap) == 0 {
			delete(m.storage, service)
		}
	}
	return nil
}

func (m *mockProvider) DeleteAll(service string) error {
	delete(m.storage, service)
	return nil
}

func (m *mockProvider) IsAvailable() bool {
	return m.available
}

func (m *mockProvider) Name() string {
	return m.name
}

func TestNewCompositeProvider(t *testing.T) {
	t.Parallel()
	provider := NewCompositeProvider()
	require.NotNil(t, provider)

	composite, ok := provider.(*compositeProvider)
	require.True(t, ok, "provider should be a compositeProvider")

	// Should always have at least one provider (zalando wrapper)
	assert.GreaterOrEqual(t, len(composite.providers), 1)

	// First provider should always be zalando wrapper
	firstProvider := composite.providers[0]
	require.NotNil(t, firstProvider)

	// Test platform-specific behavior
	switch runtime.GOOS {
	case "linux":
		// On Linux, first provider should be D-Bus Secret Service
		assert.Equal(t, "D-Bus Secret Service", firstProvider.Name())
		// On Linux, we might have keyctl as fallback (if available)
		// Length could be 1 (only zalando) or 2 (zalando + keyctl)
		assert.GreaterOrEqual(t, len(composite.providers), 1)
		assert.LessOrEqual(t, len(composite.providers), 2)

		if len(composite.providers) == 2 {
			// If keyctl is available, it should be second
			assert.Equal(t, "Linux Keyctl", composite.providers[1].Name())
		}
	case "darwin":
		// On macOS, should have macOS Keychain
		assert.Equal(t, "macOS Keychain", firstProvider.Name())
		// Should have exactly one provider on macOS
		assert.Equal(t, 1, len(composite.providers))
	case "windows":
		// On Windows, should have Windows Credential Manager
		assert.Equal(t, "Windows Credential Manager", firstProvider.Name())
		// Should have exactly one provider on Windows
		assert.Equal(t, 1, len(composite.providers))
	default:
		// On other platforms, should have generic name
		assert.Equal(t, "Platform Keyring", firstProvider.Name())
	}

	// Verify the composite provider implements all interface methods
	assert.NotNil(t, composite.IsAvailable)
	assert.NotNil(t, composite.Name)
	assert.NotNil(t, composite.Get)
	assert.NotNil(t, composite.Set)
	assert.NotNil(t, composite.Delete)
	assert.NotNil(t, composite.DeleteAll)
}

func TestCompositeProvider_GetActiveProvider(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name               string
		primaryAvailable   bool
		secondaryAvailable bool
		expectedProvider   string
		expectedNil        bool
	}{
		{
			name:               "primary available, use primary",
			primaryAvailable:   true,
			secondaryAvailable: true,
			expectedProvider:   "primary",
			expectedNil:        false,
		},
		{
			name:               "primary unavailable, use secondary",
			primaryAvailable:   false,
			secondaryAvailable: true,
			expectedProvider:   "secondary",
			expectedNil:        false,
		},
		{
			name:               "both unavailable, return nil",
			primaryAvailable:   false,
			secondaryAvailable: false,
			expectedProvider:   "",
			expectedNil:        true,
		},
		{
			name:               "only primary available",
			primaryAvailable:   true,
			secondaryAvailable: false,
			expectedProvider:   "primary",
			expectedNil:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			primary := newMockProvider("primary", tt.primaryAvailable)
			secondary := newMockProvider("secondary", tt.secondaryAvailable)

			composite := &compositeProvider{
				providers: []Provider{primary, secondary},
			}

			activeProvider := composite.getActiveProvider()

			if tt.expectedNil {
				assert.Nil(t, activeProvider)
			} else {
				require.NotNil(t, activeProvider)
				assert.Equal(t, tt.expectedProvider, activeProvider.Name())
				// Verify the active provider is cached
				assert.Equal(t, activeProvider, composite.active)
			}
		})
	}
}

func TestCompositeProvider_Operations_WithAvailableProvider(t *testing.T) {
	t.Parallel()
	mockProv := newMockProvider("test-provider", true)
	composite := &compositeProvider{
		providers: []Provider{mockProv},
	}

	// Test Set
	err := composite.Set("test-service", "test-key", "test-value")
	assert.NoError(t, err)

	// Test Get
	value, err := composite.Get("test-service", "test-key")
	assert.NoError(t, err)
	assert.Equal(t, "test-value", value)

	// Test Delete
	err = composite.Delete("test-service", "test-key")
	assert.NoError(t, err)

	// Verify deletion
	_, err = composite.Get("test-service", "test-key")
	assert.ErrorIs(t, err, ErrNotFound)

	// Test DeleteAll
	_ = composite.Set("test-service", "key1", "value1")
	_ = composite.Set("test-service", "key2", "value2")
	err = composite.DeleteAll("test-service")
	assert.NoError(t, err)
}

func TestCompositeProvider_Operations_NoProviderAvailable(t *testing.T) {
	t.Parallel()
	mockProv := newMockProvider("test-provider", false)
	composite := &compositeProvider{
		providers: []Provider{mockProv},
	}

	// Test Set
	err := composite.Set("test-service", "test-key", "test-value")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no keyring provider available")

	// Test Get
	_, err = composite.Get("test-service", "test-key")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no keyring provider available")

	// Test Delete
	err = composite.Delete("test-service", "test-key")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no keyring provider available")

	// Test DeleteAll
	err = composite.DeleteAll("test-service")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no keyring provider available")
}

// Integration test with actual runtime behavior
func TestCompositeProvider_RealProviders(t *testing.T) {
	t.Parallel()
	provider := NewCompositeProvider()
	require.NotNil(t, provider)

	// Should always have at least one provider (zalando wrapper)
	composite, ok := provider.(*compositeProvider)
	require.True(t, ok)
	assert.GreaterOrEqual(t, len(composite.providers), 1)

	// Test that the provider selection works
	_ = composite.getActiveProvider()

	// On any platform, we should get some kind of provider name
	name := provider.Name()
	assert.NotEmpty(t, name)

	// In CI environments, keyring services may not be available, so we skip this assertion
	if !isRunningInCI() {
		assert.NotEqual(t, "None Available", name, "should have at least one working provider")
	} else {
		t.Logf("Skipping keyring availability assertion in CI environment (provider: %s)", name)
	}

	// Test basic availability
	available := provider.IsAvailable()
	if available {
		// If available, basic operations should work
		err := provider.Set("toolhive-test", "integration-test", "test-value")
		if err == nil {
			// Only test Get/Delete if Set worked
			value, err := provider.Get("toolhive-test", "integration-test")
			if err == nil {
				assert.Equal(t, "test-value", value)
			}
			_ = provider.Delete("toolhive-test", "integration-test")
		}
	}
}

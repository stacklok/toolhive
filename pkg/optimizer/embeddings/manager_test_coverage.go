package embeddings

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestManager_GetCacheStats tests cache statistics
func TestManager_GetCacheStats(t *testing.T) {
	t.Parallel()

	config := &Config{
		BackendType:  "ollama",
		BaseURL:      "http://localhost:11434",
		Model:        "all-minilm",
		Dimension:    384,
		EnableCache:  true,
		MaxCacheSize: 100,
	}

	manager, err := NewManager(config)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v", err)
		return
	}
	defer func() { _ = manager.Close() }()

	stats := manager.GetCacheStats()
	require.NotNil(t, stats)
	assert.True(t, stats["enabled"].(bool))
	assert.Contains(t, stats, "hits")
	assert.Contains(t, stats, "misses")
	assert.Contains(t, stats, "size")
	assert.Contains(t, stats, "maxsize")
}

// TestManager_GetCacheStats_Disabled tests cache statistics when cache is disabled
func TestManager_GetCacheStats_Disabled(t *testing.T) {
	t.Parallel()

	config := &Config{
		BackendType: "ollama",
		BaseURL:     "http://localhost:11434",
		Model:       "all-minilm",
		Dimension:   384,
		EnableCache: false,
	}

	manager, err := NewManager(config)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v", err)
		return
	}
	defer func() { _ = manager.Close() }()

	stats := manager.GetCacheStats()
	require.NotNil(t, stats)
	assert.False(t, stats["enabled"].(bool))
}

// TestManager_ClearCache tests cache clearing
func TestManager_ClearCache(t *testing.T) {
	t.Parallel()

	config := &Config{
		BackendType:  "ollama",
		BaseURL:      "http://localhost:11434",
		Model:        "all-minilm",
		Dimension:    384,
		EnableCache:  true,
		MaxCacheSize: 100,
	}

	manager, err := NewManager(config)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v", err)
		return
	}
	defer func() { _ = manager.Close() }()

	// Clear cache should not panic
	manager.ClearCache()

	// Multiple clears should be safe
	manager.ClearCache()
}

// TestManager_ClearCache_Disabled tests cache clearing when cache is disabled
func TestManager_ClearCache_Disabled(t *testing.T) {
	t.Parallel()

	config := &Config{
		BackendType: "ollama",
		BaseURL:     "http://localhost:11434",
		Model:       "all-minilm",
		Dimension:   384,
		EnableCache: false,
	}

	manager, err := NewManager(config)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v", err)
		return
	}
	defer func() { _ = manager.Close() }()

	// Clear cache should not panic even when disabled
	manager.ClearCache()
}

// TestManager_Dimension tests dimension accessor
func TestManager_Dimension(t *testing.T) {
	t.Parallel()

	config := &Config{
		BackendType: "ollama",
		BaseURL:     "http://localhost:11434",
		Model:       "all-minilm",
		Dimension:   384,
	}

	manager, err := NewManager(config)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v", err)
		return
	}
	defer func() { _ = manager.Close() }()

	dimension := manager.Dimension()
	assert.Equal(t, 384, dimension)
}

// TestManager_Dimension_Default tests default dimension
func TestManager_Dimension_Default(t *testing.T) {
	t.Parallel()

	config := &Config{
		BackendType: "ollama",
		BaseURL:     "http://localhost:11434",
		Model:       "all-minilm",
		// Dimension not set, should default to 384
	}

	manager, err := NewManager(config)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v", err)
		return
	}
	defer func() { _ = manager.Close() }()

	dimension := manager.Dimension()
	assert.Equal(t, 384, dimension)
}

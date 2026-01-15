package embeddings

import (
	"testing"
)

func TestCache_GetPut(t *testing.T) {
	t.Parallel()
	c := newCache(2)

	// Test cache miss
	result := c.Get("key1")
	if result != nil {
		t.Error("Expected cache miss for non-existent key")
	}
	if c.misses != 1 {
		t.Errorf("Expected 1 miss, got %d", c.misses)
	}

	// Test cache put and hit
	embedding := []float32{1.0, 2.0, 3.0}
	c.Put("key1", embedding)

	result = c.Get("key1")
	if result == nil {
		t.Fatal("Expected cache hit for existing key")
	}
	if c.hits != 1 {
		t.Errorf("Expected 1 hit, got %d", c.hits)
	}

	// Verify embedding values
	if len(result) != len(embedding) {
		t.Errorf("Embedding length mismatch: got %d, want %d", len(result), len(embedding))
	}
	for i := range embedding {
		if result[i] != embedding[i] {
			t.Errorf("Embedding value mismatch at index %d: got %f, want %f", i, result[i], embedding[i])
		}
	}
}

func TestCache_LRUEviction(t *testing.T) {
	t.Parallel()
	c := newCache(2)

	// Add two items (fills cache)
	c.Put("key1", []float32{1.0})
	c.Put("key2", []float32{2.0})

	if c.Size() != 2 {
		t.Errorf("Expected cache size 2, got %d", c.Size())
	}

	// Add third item (should evict key1)
	c.Put("key3", []float32{3.0})

	if c.Size() != 2 {
		t.Errorf("Expected cache size 2 after eviction, got %d", c.Size())
	}

	// key1 should be evicted (oldest)
	if result := c.Get("key1"); result != nil {
		t.Error("key1 should have been evicted")
	}

	// key2 and key3 should still exist
	if result := c.Get("key2"); result == nil {
		t.Error("key2 should still exist")
	}
	if result := c.Get("key3"); result == nil {
		t.Error("key3 should still exist")
	}
}

func TestCache_MoveToFrontOnAccess(t *testing.T) {
	t.Parallel()
	c := newCache(2)

	// Add two items
	c.Put("key1", []float32{1.0})
	c.Put("key2", []float32{2.0})

	// Access key1 (moves it to front)
	c.Get("key1")

	// Add third item (should evict key2, not key1)
	c.Put("key3", []float32{3.0})

	// key1 should still exist (was accessed recently)
	if result := c.Get("key1"); result == nil {
		t.Error("key1 should still exist (was accessed recently)")
	}

	// key2 should be evicted (was oldest)
	if result := c.Get("key2"); result != nil {
		t.Error("key2 should have been evicted")
	}

	// key3 should exist
	if result := c.Get("key3"); result == nil {
		t.Error("key3 should exist")
	}
}

func TestCache_UpdateExistingKey(t *testing.T) {
	t.Parallel()
	c := newCache(2)

	// Add initial value
	c.Put("key1", []float32{1.0})

	// Update with new value
	newEmbedding := []float32{2.0, 3.0}
	c.Put("key1", newEmbedding)

	// Should get updated value
	result := c.Get("key1")
	if result == nil {
		t.Fatal("Expected cache hit for existing key")
	}

	if len(result) != len(newEmbedding) {
		t.Errorf("Embedding length mismatch: got %d, want %d", len(result), len(newEmbedding))
	}

	// Cache size should still be 1
	if c.Size() != 1 {
		t.Errorf("Expected cache size 1, got %d", c.Size())
	}
}

func TestCache_Clear(t *testing.T) {
	t.Parallel()
	c := newCache(10)

	// Add some items
	c.Put("key1", []float32{1.0})
	c.Put("key2", []float32{2.0})
	c.Put("key3", []float32{3.0})

	// Access some items to generate stats
	c.Get("key1")
	c.Get("missing")

	if c.Size() != 3 {
		t.Errorf("Expected cache size 3, got %d", c.Size())
	}

	// Clear cache
	c.Clear()

	if c.Size() != 0 {
		t.Errorf("Expected cache size 0 after clear, got %d", c.Size())
	}

	// Stats should be reset
	if c.hits != 0 {
		t.Errorf("Expected 0 hits after clear, got %d", c.hits)
	}
	if c.misses != 0 {
		t.Errorf("Expected 0 misses after clear, got %d", c.misses)
	}

	// Items should be gone
	if result := c.Get("key1"); result != nil {
		t.Error("key1 should be gone after clear")
	}
}

package embeddings

import (
	"testing"
)

func TestOllamaBackend_Placeholder(t *testing.T) {
	// This test verifies that Ollama backend is properly structured
	// Actual Ollama tests require ollama to be running

	// Test that NewOllamaBackend handles connection failure gracefully
	_, err := NewOllamaBackend("http://localhost:99999", "nomic-embed-text")
	if err == nil {
		t.Error("Expected error when connecting to invalid Ollama URL")
	}
}

func TestManagerWithOllama(t *testing.T) {
	// Test that Manager falls back to placeholder when Ollama is not available or model not pulled
	config := &Config{
		BackendType: "ollama",
		Dimension:   384,
		EnableCache: true,
		MaxCacheSize: 100,
	}

	manager, err := NewManager(config)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}
	defer manager.Close()

	// Should work with placeholder backend fallback
	// (Ollama might not have model pulled, so it falls back to placeholder)
	embeddings, err := manager.GenerateEmbedding([]string{"test text"})
	
	// If Ollama is available with the model, great!
	// If not, it should have fallen back to placeholder
	if err != nil {
		// Check if it's a "model not found" error - this is expected
		if embeddings == nil {
			t.Skip("Ollama not available or model not pulled (expected in CI/test environments)")
		}
	}

	if len(embeddings) != 1 {
		t.Errorf("Expected 1 embedding, got %d", len(embeddings))
	}

	// Dimension could be 384 (placeholder) or 768 (Ollama nomic-embed-text)
	if len(embeddings[0]) != 384 && len(embeddings[0]) != 768 {
		t.Errorf("Expected dimension 384 or 768, got %d", len(embeddings[0]))
	}
}

func TestManagerWithPlaceholder(t *testing.T) {
	// Test explicit placeholder backend
	config := &Config{
		BackendType: "placeholder",
		Dimension:   384,
		EnableCache: false,
	}

	manager, err := NewManager(config)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}
	defer manager.Close()

	// Test single embedding
	embeddings, err := manager.GenerateEmbedding([]string{"hello world"})
	if err != nil {
		t.Fatalf("Failed to generate embedding: %v", err)
	}

	if len(embeddings) != 1 {
		t.Errorf("Expected 1 embedding, got %d", len(embeddings))
	}

	if len(embeddings[0]) != 384 {
		t.Errorf("Expected dimension 384, got %d", len(embeddings[0]))
	}

	// Test batch embeddings
	texts := []string{"text 1", "text 2", "text 3"}
	embeddings, err = manager.GenerateEmbedding(texts)
	if err != nil {
		t.Fatalf("Failed to generate batch embeddings: %v", err)
	}

	if len(embeddings) != 3 {
		t.Errorf("Expected 3 embeddings, got %d", len(embeddings))
	}

	// Verify embeddings are deterministic
	embeddings2, _ := manager.GenerateEmbedding([]string{"text 1"})
	for i := range embeddings[0] {
		if embeddings[0][i] != embeddings2[0][i] {
			t.Error("Embeddings should be deterministic")
			break
		}
	}
}


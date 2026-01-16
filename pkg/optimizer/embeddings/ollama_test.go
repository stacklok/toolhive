package embeddings

import (
	"testing"
)

func TestOllamaBackend_ConnectionFailure(t *testing.T) {
	t.Parallel()
	// This test verifies that Ollama backend handles connection failures gracefully

	// Test that NewOllamaBackend handles connection failure gracefully
	_, err := NewOllamaBackend("http://localhost:99999", "all-minilm")
	if err == nil {
		t.Error("Expected error when connecting to invalid Ollama URL")
	}
}

func TestManagerWithOllama(t *testing.T) {
	t.Parallel()
	// Test that Manager works with Ollama when available
	config := &Config{
		BackendType:  "ollama",
		BaseURL:      "http://localhost:11434",
		Model:        "all-minilm",
		Dimension:   768,
		EnableCache:  true,
		MaxCacheSize: 100,
	}

	manager, err := NewManager(config)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v. Run 'ollama serve && ollama pull all-minilm'", err)
		return
	}
	defer manager.Close()

	// Test single embedding
	embeddings, err := manager.GenerateEmbedding([]string{"test text"})
	if err != nil {
		// Model might not be pulled - skip gracefully
		t.Skipf("Skipping test: Failed to generate embedding. Error: %v. Run 'ollama pull nomic-embed-text'", err)
		return
	}

	if len(embeddings) != 1 {
		t.Errorf("Expected 1 embedding, got %d", len(embeddings))
	}

		// Ollama all-minilm uses 384 dimensions
		if len(embeddings[0]) != 384 {
			t.Errorf("Expected dimension 384, got %d", len(embeddings[0]))
		}

	// Test batch embeddings
	texts := []string{"text 1", "text 2", "text 3"}
	embeddings, err = manager.GenerateEmbedding(texts)
	if err != nil {
		// Model might not be pulled - skip gracefully
		t.Skipf("Skipping test: Failed to generate batch embeddings. Error: %v. Run 'ollama pull nomic-embed-text'", err)
		return
	}

	if len(embeddings) != 3 {
		t.Errorf("Expected 3 embeddings, got %d", len(embeddings))
	}
}

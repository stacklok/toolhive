// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package embeddings

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

const testEmbeddingsEndpoint = "/v1/embeddings"

func TestOpenAICompatibleBackend(t *testing.T) {
	t.Parallel()
	// Create a test server that mimics OpenAI-compatible API
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == testEmbeddingsEndpoint {
			var req openaiEmbedRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("Failed to decode request: %v", err)
			}

			// Return a mock embedding response
			resp := openaiEmbedResponse{
				Object: "list",
				Data: []struct {
					Object    string    `json:"object"`
					Embedding []float32 `json:"embedding"`
					Index     int       `json:"index"`
				}{
					{
						Object:    "embedding",
						Embedding: make([]float32, 384),
						Index:     0,
					},
				},
				Model: req.Model,
			}

			// Fill with test data
			for i := range resp.Data[0].Embedding {
				resp.Data[0].Embedding[i] = float32(i) / 384.0
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}

		// Health check endpoint
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Test backend creation
	backend, err := NewOpenAICompatibleBackend(server.URL, "test-model", 384)
	if err != nil {
		t.Fatalf("Failed to create backend: %v", err)
	}
	defer backend.Close()

	// Test embedding generation
	embedding, err := backend.Embed("test text")
	if err != nil {
		t.Fatalf("Failed to generate embedding: %v", err)
	}

	if len(embedding) != 384 {
		t.Errorf("Expected embedding dimension 384, got %d", len(embedding))
	}

	// Test batch embedding
	texts := []string{"text1", "text2", "text3"}
	embeddings, err := backend.EmbedBatch(texts)
	if err != nil {
		t.Fatalf("Failed to generate batch embeddings: %v", err)
	}

	if len(embeddings) != len(texts) {
		t.Errorf("Expected %d embeddings, got %d", len(texts), len(embeddings))
	}
}

func TestOpenAICompatibleBackendErrors(t *testing.T) {
	t.Parallel()
	// Test missing baseURL
	_, err := NewOpenAICompatibleBackend("", "model", 384)
	if err == nil {
		t.Error("Expected error for missing baseURL")
	}

	// Test missing model
	_, err = NewOpenAICompatibleBackend("http://localhost:8000", "", 384)
	if err == nil {
		t.Error("Expected error for missing model")
	}
}

func TestManagerWithVLLM(t *testing.T) {
	t.Parallel()
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == testEmbeddingsEndpoint {
			resp := openaiEmbedResponse{
				Object: "list",
				Data: []struct {
					Object    string    `json:"object"`
					Embedding []float32 `json:"embedding"`
					Index     int       `json:"index"`
				}{
					{
						Object:    "embedding",
						Embedding: make([]float32, 384),
						Index:     0,
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Test manager with vLLM backend
	config := &Config{
		BackendType: "vllm",
		BaseURL:     server.URL,
		Model:       "sentence-transformers/all-MiniLM-L6-v2",
		Dimension:   384,
		EnableCache: true,
	}

	manager, err := NewManager(config)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}
	defer manager.Close()

	// Test embedding generation
	embeddings, err := manager.GenerateEmbedding([]string{"test"})
	if err != nil {
		t.Fatalf("Failed to generate embeddings: %v", err)
	}

	if len(embeddings) != 1 {
		t.Errorf("Expected 1 embedding, got %d", len(embeddings))
	}
	if len(embeddings[0]) != 384 {
		t.Errorf("Expected dimension 384, got %d", len(embeddings[0]))
	}
}

func TestManagerWithUnified(t *testing.T) {
	t.Parallel()
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == testEmbeddingsEndpoint {
			resp := openaiEmbedResponse{
				Object: "list",
				Data: []struct {
					Object    string    `json:"object"`
					Embedding []float32 `json:"embedding"`
					Index     int       `json:"index"`
				}{
					{
						Object:    "embedding",
						Embedding: make([]float32, 768),
						Index:     0,
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Test manager with unified backend
	config := &Config{
		BackendType: "unified",
		BaseURL:     server.URL,
		Model:       "nomic-embed-text",
		Dimension:   768,
		EnableCache: false,
	}

	manager, err := NewManager(config)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}
	defer manager.Close()

	// Test embedding generation
	embeddings, err := manager.GenerateEmbedding([]string{"test"})
	if err != nil {
		t.Fatalf("Failed to generate embeddings: %v", err)
	}

	if len(embeddings) != 1 {
		t.Errorf("Expected 1 embedding, got %d", len(embeddings))
	}
}

func TestManagerFallbackBehavior(t *testing.T) {
	t.Parallel()
	// Test that invalid vLLM backend fails gracefully during initialization
	// (No fallback behavior is currently implemented)
	config := &Config{
		BackendType: "vllm",
		BaseURL:     "http://invalid-host-that-does-not-exist:9999",
		Model:       "test-model",
		Dimension:   384,
	}

	_, err := NewManager(config)
	if err == nil {
		t.Error("Expected error when creating manager with invalid backend URL")
	}
	// Test passes if error is returned (no fallback behavior)
}

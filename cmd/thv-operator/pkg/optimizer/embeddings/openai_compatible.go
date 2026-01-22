// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package embeddings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/stacklok/toolhive/pkg/logger"
)

// OpenAICompatibleBackend implements the Backend interface for OpenAI-compatible APIs.
//
// Supported Services:
//   - vLLM: Recommended for production Kubernetes deployments
//   - High-throughput GPU-accelerated inference
//   - PagedAttention for efficient GPU memory utilization
//   - Superior scalability for multi-user environments
//   - Ollama: Good for local development (via /v1/embeddings endpoint)
//   - OpenAI: For cloud-based embeddings
//   - Any OpenAI-compatible embedding service
//
// For production deployments, vLLM is strongly recommended due to its performance
// characteristics and Kubernetes-native design.
type OpenAICompatibleBackend struct {
	baseURL   string
	model     string
	dimension int
	client    *http.Client
}

type openaiEmbedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"` // OpenAI standard uses "input"
}

type openaiEmbedResponse struct {
	Object string `json:"object"`
	Data   []struct {
		Object    string    `json:"object"`
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Model string `json:"model"`
}

// NewOpenAICompatibleBackend creates a new OpenAI-compatible backend.
//
// Examples:
//   - vLLM: NewOpenAICompatibleBackend("http://vllm-service:8000", "sentence-transformers/all-MiniLM-L6-v2", 384)
//   - Ollama: NewOpenAICompatibleBackend("http://localhost:11434", "nomic-embed-text", 768)
//   - OpenAI: NewOpenAICompatibleBackend("https://api.openai.com", "text-embedding-3-small", 1536)
func NewOpenAICompatibleBackend(baseURL, model string, dimension int) (*OpenAICompatibleBackend, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("baseURL is required for OpenAI-compatible backend")
	}
	if model == "" {
		return nil, fmt.Errorf("model is required for OpenAI-compatible backend")
	}
	if dimension == 0 {
		dimension = 384 // Default dimension
	}

	logger.Infof("Initializing OpenAI-compatible backend (model: %s, url: %s)", model, baseURL)

	backend := &OpenAICompatibleBackend{
		baseURL:   baseURL,
		model:     model,
		dimension: dimension,
		client:    &http.Client{},
	}

	// Test connection
	resp, err := backend.client.Get(baseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", baseURL, err)
	}
	_ = resp.Body.Close()

	logger.Info("Successfully connected to OpenAI-compatible service")
	return backend, nil
}

// Embed generates an embedding for a single text using OpenAI-compatible API
func (o *OpenAICompatibleBackend) Embed(text string) ([]float32, error) {
	reqBody := openaiEmbedRequest{
		Model: o.model,
		Input: text,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Use standard OpenAI v1 endpoint
	resp, err := o.client.Post(
		o.baseURL+"/v1/embeddings",
		"application/json",
		bytes.NewBuffer(jsonData),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to call embeddings API: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var embedResp openaiEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&embedResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(embedResp.Data) == 0 {
		return nil, fmt.Errorf("no embeddings in response")
	}

	return embedResp.Data[0].Embedding, nil
}

// EmbedBatch generates embeddings for multiple texts
func (o *OpenAICompatibleBackend) EmbedBatch(texts []string) ([][]float32, error) {
	embeddings := make([][]float32, len(texts))

	for i, text := range texts {
		emb, err := o.Embed(text)
		if err != nil {
			return nil, fmt.Errorf("failed to embed text %d: %w", i, err)
		}
		embeddings[i] = emb
	}

	return embeddings, nil
}

// Dimension returns the embedding dimension
func (o *OpenAICompatibleBackend) Dimension() int {
	return o.dimension
}

// Close releases any resources
func (*OpenAICompatibleBackend) Close() error {
	// HTTP client doesn't need explicit cleanup
	return nil
}

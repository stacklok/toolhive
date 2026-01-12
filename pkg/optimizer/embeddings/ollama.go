package embeddings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/stacklok/toolhive/pkg/logger"
)

// OllamaBackend implements the Backend interface using Ollama
// This provides local embeddings without remote API calls
// Ollama must be running locally (ollama serve)
type OllamaBackend struct {
	baseURL   string
	model     string
	dimension int
	client    *http.Client
}

type ollamaEmbedRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type ollamaEmbedResponse struct {
	Embedding []float64 `json:"embedding"`
}

// NewOllamaBackend creates a new Ollama backend
// Requires Ollama to be running locally: ollama serve
// Default model: nomic-embed-text (768 dimensions)
func NewOllamaBackend(baseURL, model string) (*OllamaBackend, error) {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	if model == "" {
		model = "nomic-embed-text" // Default embedding model
	}

	logger.Infof("Initializing Ollama backend (model: %s, url: %s)", model, baseURL)

	backend := &OllamaBackend{
		baseURL:   baseURL,
		model:     model,
		dimension: 768, // nomic-embed-text dimension
		client:    &http.Client{},
	}

	// Test connection
	resp, err := backend.client.Get(baseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Ollama at %s: %w (is 'ollama serve' running?)", baseURL, err)
	}
	resp.Body.Close()

	logger.Info("Successfully connected to Ollama")
	return backend, nil
}

// Embed generates an embedding for a single text
func (o *OllamaBackend) Embed(text string) ([]float32, error) {
	reqBody := ollamaEmbedRequest{
		Model:  o.model,
		Prompt: text,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := o.client.Post(
		o.baseURL+"/api/embeddings",
		"application/json",
		bytes.NewBuffer(jsonData),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to call Ollama API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Ollama API returned status %d: %s", resp.StatusCode, string(body))
	}

	var embedResp ollamaEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&embedResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Convert []float64 to []float32
	embedding := make([]float32, len(embedResp.Embedding))
	for i, v := range embedResp.Embedding {
		embedding[i] = float32(v)
	}

	return embedding, nil
}

// EmbedBatch generates embeddings for multiple texts
func (o *OllamaBackend) EmbedBatch(texts []string) ([][]float32, error) {
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
func (o *OllamaBackend) Dimension() int {
	return o.dimension
}

// Close releases any resources
func (o *OllamaBackend) Close() error {
	// HTTP client doesn't need explicit cleanup
	return nil
}


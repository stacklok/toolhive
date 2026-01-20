package embeddings

import (
	"fmt"
	"strings"
	"sync"

	"github.com/stacklok/toolhive/pkg/logger"
)

const (
	// DefaultModelAllMiniLM is the default Ollama model name
	DefaultModelAllMiniLM = "all-minilm"
	// BackendTypeOllama is the Ollama backend type
	BackendTypeOllama = "ollama"
)

// Config holds configuration for the embedding manager
type Config struct {
	// BackendType specifies which backend to use:
	// - "ollama": Ollama native API (default)
	// - "vllm": vLLM OpenAI-compatible API
	// - "unified": Generic OpenAI-compatible API (works with both)
	// - "openai": OpenAI-compatible API
	BackendType string

	// BaseURL is the base URL for the embedding service
	// - Ollama: http://127.0.0.1:11434 (or http://localhost:11434, will be normalized to 127.0.0.1)
	// - vLLM: http://localhost:8000
	BaseURL string

	// Model is the model name to use
	// - Ollama: "all-minilm" (default), "nomic-embed-text"
	// - vLLM: "sentence-transformers/all-MiniLM-L6-v2", "intfloat/e5-mistral-7b-instruct"
	Model string

	// Dimension is the embedding dimension (default 384 for all-MiniLM-L6-v2)
	Dimension int

	// EnableCache enables caching of embeddings
	EnableCache bool

	// MaxCacheSize is the maximum number of embeddings to cache (default 1000)
	MaxCacheSize int
}

// Backend interface for different embedding implementations
type Backend interface {
	Embed(text string) ([]float32, error)
	EmbedBatch(texts []string) ([][]float32, error)
	Dimension() int
	Close() error
}

// Manager manages embedding generation using pluggable backends
// Default backend is all-MiniLM-L6-v2 (same model as codegate)
type Manager struct {
	config  *Config
	backend Backend
	cache   *cache
	mu      sync.RWMutex
}

// NewManager creates a new embedding manager
func NewManager(config *Config) (*Manager, error) {
	if config.Dimension == 0 {
		config.Dimension = 384 // Default dimension for all-MiniLM-L6-v2
	}

	if config.MaxCacheSize == 0 {
		config.MaxCacheSize = 1000
	}

	// Default to Ollama
	if config.BackendType == "" {
		config.BackendType = BackendTypeOllama
	}

	// Initialize backend based on configuration
	var backend Backend
	var err error

	switch config.BackendType {
	case BackendTypeOllama:
		// Use Ollama native API (requires ollama serve)
		baseURL := config.BaseURL
		if baseURL == "" {
			baseURL = "http://127.0.0.1:11434"
		} else {
			// Normalize localhost to 127.0.0.1 to avoid IPv6 resolution issues
			baseURL = strings.ReplaceAll(baseURL, "localhost", "127.0.0.1")
		}
		model := config.Model
		if model == "" {
			model = DefaultModelAllMiniLM // Default: all-MiniLM-L6-v2
		}
		// Update dimension if not set and using default model
		if config.Dimension == 0 && model == DefaultModelAllMiniLM {
			config.Dimension = 384
		}
		backend, err = NewOllamaBackend(baseURL, model)
		if err != nil {
			return nil, fmt.Errorf(
				"failed to initialize Ollama backend: %w (ensure 'ollama serve' is running and 'ollama pull %s' has been executed)",
				err, DefaultModelAllMiniLM)
		}

	case "vllm", "unified", "openai":
		// Use OpenAI-compatible API
		// vLLM is recommended for production Kubernetes deployments (GPU-accelerated, high-throughput)
		// Also supports: Ollama v1 API, OpenAI, or any OpenAI-compatible service
		if config.BaseURL == "" {
			return nil, fmt.Errorf("BaseURL is required for %s backend", config.BackendType)
		}
		if config.Model == "" {
			return nil, fmt.Errorf("model is required for %s backend", config.BackendType)
		}
		backend, err = NewOpenAICompatibleBackend(config.BaseURL, config.Model, config.Dimension)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize %s backend: %w", config.BackendType, err)
		}

	default:
		return nil, fmt.Errorf("unknown backend type: %s (supported: ollama, vllm, unified, openai)", config.BackendType)
	}

	m := &Manager{
		config:  config,
		backend: backend,
	}

	if config.EnableCache {
		m.cache = newCache(config.MaxCacheSize)
	}

	return m, nil
}

// GenerateEmbedding generates embeddings for the given texts
// Returns a 2D slice where each row is an embedding for the corresponding text
// Uses all-MiniLM-L6-v2 by default (same model as codegate)
func (m *Manager) GenerateEmbedding(texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, fmt.Errorf("no texts provided")
	}

	// Check cache for single text requests
	if len(texts) == 1 && m.config.EnableCache && m.cache != nil {
		if cached := m.cache.Get(texts[0]); cached != nil {
			logger.Debugf("Cache hit for embedding")
			return [][]float32{cached}, nil
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Use backend to generate embeddings
	embeddings, err := m.backend.EmbedBatch(texts)
	if err != nil {
		return nil, fmt.Errorf("failed to generate embeddings: %w", err)
	}

	// Cache single embeddings
	if len(texts) == 1 && m.config.EnableCache && m.cache != nil {
		m.cache.Put(texts[0], embeddings[0])
	}

	logger.Debugf("Generated %d embeddings (dimension: %d)", len(embeddings), m.backend.Dimension())
	return embeddings, nil
}

// GetCacheStats returns cache statistics
func (m *Manager) GetCacheStats() map[string]interface{} {
	if !m.config.EnableCache || m.cache == nil {
		return map[string]interface{}{
			"enabled": false,
		}
	}

	return map[string]interface{}{
		"enabled": true,
		"hits":    m.cache.hits,
		"misses":  m.cache.misses,
		"size":    m.cache.Size(),
		"maxsize": m.config.MaxCacheSize,
	}
}

// ClearCache clears the embedding cache
func (m *Manager) ClearCache() {
	if m.config.EnableCache && m.cache != nil {
		m.cache.Clear()
		logger.Info("Embedding cache cleared")
	}
}

// Close releases resources
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.backend != nil {
		return m.backend.Close()
	}

	return nil
}

// Dimension returns the embedding dimension
func (m *Manager) Dimension() int {
	if m.backend != nil {
		return m.backend.Dimension()
	}
	return m.config.Dimension
}

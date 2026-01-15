package embeddings

import (
	"fmt"
	"sync"

	"github.com/stacklok/toolhive/pkg/logger"
)

const (
	// BackendTypePlaceholder is the placeholder backend type
	BackendTypePlaceholder = "placeholder"
)

// Config holds configuration for the embedding manager
type Config struct {
	// BackendType specifies which backend to use:
	// - "ollama": Ollama native API
	// - "vllm": vLLM OpenAI-compatible API
	// - "unified": Generic OpenAI-compatible API (works with both)
	// - "placeholder": Hash-based embeddings for testing
	BackendType string

	// BaseURL is the base URL for the embedding service
	// - Ollama: http://localhost:11434
	// - vLLM: http://localhost:8000
	BaseURL string

	// Model is the model name to use
	// - Ollama: "nomic-embed-text", "all-minilm"
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

	// Default to placeholder (zero dependencies)
	if config.BackendType == "" {
		config.BackendType = "placeholder"
	}

	// Initialize backend based on configuration
	var backend Backend
	var err error

	switch config.BackendType {
	case "ollama":
		// Use Ollama native API (requires ollama serve)
		baseURL := config.BaseURL
		if baseURL == "" {
			baseURL = "http://localhost:11434"
		}
		model := config.Model
		if model == "" {
			model = "nomic-embed-text"
		}
		backend, err = NewOllamaBackend(baseURL, model)
		if err != nil {
			logger.Warnf("Failed to initialize Ollama backend: %v", err)
			logger.Info("Falling back to placeholder embeddings. To use Ollama: ollama serve && ollama pull nomic-embed-text")
			backend = &PlaceholderBackend{dimension: config.Dimension}
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
			logger.Warnf("Failed to initialize %s backend: %v", config.BackendType, err)
			logger.Infof("Falling back to placeholder embeddings")
			backend = &PlaceholderBackend{dimension: config.Dimension}
		}

	case BackendTypePlaceholder:
		// Use placeholder for testing
		backend = &PlaceholderBackend{dimension: config.Dimension}

	default:
		return nil, fmt.Errorf("unknown backend type: %s (supported: ollama, vllm, unified, placeholder)", config.BackendType)
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
		// If backend fails, fall back to placeholder for non-placeholder backends
		if m.config.BackendType != "placeholder" {
			logger.Warnf("%s backend failed: %v, falling back to placeholder", m.config.BackendType, err)
			placeholder := &PlaceholderBackend{dimension: m.config.Dimension}
			embeddings, err = placeholder.EmbedBatch(texts)
			if err != nil {
				return nil, fmt.Errorf("failed to generate embeddings: %w", err)
			}
		} else {
			return nil, fmt.Errorf("failed to generate embeddings: %w", err)
		}
	}

	// Cache single embeddings
	if len(texts) == 1 && m.config.EnableCache && m.cache != nil {
		m.cache.Put(texts[0], embeddings[0])
	}

	logger.Debugf("Generated %d embeddings (dimension: %d)", len(embeddings), m.backend.Dimension())
	return embeddings, nil
}

// PlaceholderBackend is a simple backend for testing
type PlaceholderBackend struct {
	dimension int
}

// Embed generates a deterministic hash-based embedding for the given text.
func (p *PlaceholderBackend) Embed(text string) ([]float32, error) {
	return p.generatePlaceholderEmbedding(text), nil
}

// EmbedBatch generates embeddings for multiple texts.
func (p *PlaceholderBackend) EmbedBatch(texts []string) ([][]float32, error) {
	embeddings := make([][]float32, len(texts))
	for i, text := range texts {
		embeddings[i] = p.generatePlaceholderEmbedding(text)
	}
	return embeddings, nil
}

// Dimension returns the embedding dimension.
func (p *PlaceholderBackend) Dimension() int {
	return p.dimension
}

// Close closes the backend (no-op for placeholder).
func (*PlaceholderBackend) Close() error {
	return nil
}

func (p *PlaceholderBackend) generatePlaceholderEmbedding(text string) []float32 {
	embedding := make([]float32, p.dimension)

	// Simple hash-based generation for testing
	hash := 0
	for _, c := range text {
		hash = (hash*31 + int(c)) % 1000000
	}

	// Generate deterministic values
	for i := range embedding {
		hash = (hash*1103515245 + 12345) % 1000000
		embedding[i] = float32(hash) / 1000000.0
	}

	// Normalize the embedding (L2 normalization)
	var norm float32
	for _, v := range embedding {
		norm += v * v
	}
	if norm > 0 {
		norm = float32(1.0 / float64(norm))
		for i := range embedding {
			embedding[i] *= norm
		}
	}

	return embedding
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

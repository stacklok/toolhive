# ToolHive Embeddings - Design and Implementation

## Overview

The ToolHive optimizer embeddings package provides semantic text embeddings for tool discovery and similarity matching. This document explains the design decisions, architecture, and trade-offs that led to the current implementation.

## Problem Statement

We needed a way to generate embeddings in Go that:
1. **Works locally** - No mandatory remote API calls
2. **Pure Go preferred** - Minimal native dependencies for easy deployment
3. **Production-ready** - Performance and reliability for production use
4. **Flexible** - Support multiple backends and deployment scenarios
5. **Compatible with codegate** - Ideally use the same model (all-MiniLM-L6-v2, 384 dimensions)

## Design Decisions

### 1. Pluggable Backend Architecture

**Decision**: Implement a `Backend` interface with multiple implementations.

```go
type Backend interface {
    Embed(text string) ([]float32, error)
    EmbedBatch(texts []string) ([][]float32, error)
    Dimension() int
    Close() error
}
```

**Rationale**:
- Different deployment scenarios need different solutions
- Easy to test (placeholder backend)
- Can add new backends without changing core logic
- Graceful degradation (automatic fallback)

### 2. Three Backend Implementations

#### A. Placeholder Backend (Default)
**What**: Hash-based deterministic embeddings  
**When**: Testing, CI/CD, development, fallback  
**Dependencies**: Zero  
**Performance**: Excellent (<1ms)

**Rationale**: We needed a **zero-dependency** option that works immediately. While not semantically meaningful, it provides:
- Deterministic behavior for testing
- Always-working fallback
- Fast iteration during development

#### B. Ollama Backend
**What**: HTTP client for Ollama's native API  
**When**: Local development, CPU-only deployments  
**Dependencies**: Ollama service (no Go dependencies)  
**Performance**: Good (50-100ms)

**Rationale**: Ollama provides the best **local development experience**:
- Simple setup (`ollama serve`)
- Works without GPUs
- Can use all-minilm model (same as codegate)
- Pure Go HTTP client (no CGO)

#### C. OpenAI-Compatible Backend (Unified)
**What**: HTTP client for OpenAI-compatible `/v1/embeddings` API  
**When**: Production with vLLM, Ollama in OpenAI mode, or any compatible service  
**Dependencies**: Compatible service (no Go dependencies)  
**Performance**: Excellent with vLLM (10-30ms with GPU)

**Rationale**: This provides **production-grade performance** and **maximum compatibility**:
- Works with vLLM (GPU-accelerated, high throughput)
- Works with Ollama (via `/v1/embeddings`)
- Standard OpenAI protocol
- Pure Go HTTP client (no CGO)

### 3. HTTP-Based, Not Native Inference

**Decision**: Use HTTP APIs to external services instead of embedding models directly in Go.

**Why we rejected native inference**:

| Approach | Why Rejected |
|----------|-------------|
| **all-minilm-l6-v2-go** | Requires ONNX Runtime C++ library (not pure Go) |
| **ONNX + tokenizer** | Complex setup, requires C++ ONNX Runtime, 2+ hour setup |
| **llama.cpp bindings** | Requires CGO, complex build process, cross-compilation issues |
| **fastembed-go** | Still requires ONNX Runtime |

**Conclusion**: No production-ready, pure Go, zero-dependency embedding library exists that matches ML model quality.

**Our solution**: HTTP-based backends that:
- ✅ Are pure Go (standard HTTP library)
- ✅ Have zero native dependencies
- ✅ Work across platforms (no CGO, no cross-compilation issues)
- ✅ Deploy as single binary
- ✅ Separate concerns (embedding service vs. application)

### 4. OpenAI Protocol as Standard

**Decision**: Support OpenAI-compatible `/v1/embeddings` API.

**Rationale**:
- **Industry standard**: Most embedding services support it
- **Protocol compatibility**: vLLM and Ollama both support it
- **Future-proof**: Can swap services without code changes
- **Consistent interface**: Same API across backends

**API Format**:
```json
POST /v1/embeddings
{
  "model": "sentence-transformers/all-MiniLM-L6-v2",
  "input": "text to embed"
}

Response:
{
  "data": [
    {
      "embedding": [0.123, 0.456, ...],
      "index": 0
    }
  ]
}
```

### 5. Automatic Fallback to Placeholder

**Decision**: If a backend fails, automatically fall back to placeholder.

**Rationale**:
- **Resilience**: Application never fails due to embeddings
- **Development**: Works immediately without setup
- **Graceful degradation**: Better than hard failure
- **Testing**: Always works in CI/CD

**Trade-off**: Placeholder embeddings are not semantically meaningful, but this is acceptable because:
- It's only a fallback
- Logs clearly indicate when fallback is used
- Still works for basic functionality
- Better than crashing

## Architecture

```
┌─────────────────────────────────────────┐
│         Embedding Manager               │
│  ┌────────────────────────────────┐    │
│  │   Config & Cache               │    │
│  └────────────────────────────────┘    │
│              ↓                          │
│  ┌────────────────────────────────┐    │
│  │   Backend Interface            │    │
│  └────────────────────────────────┘    │
│              ↓                          │
│  ┌──────────┬──────────┬──────────┐    │
│  │Placeholder│ Ollama  │ Unified  │    │
│  │ Backend  │ Backend │ Backend  │    │
│  └──────────┴──────────┴──────────┘    │
└─────────────────────────────────────────┘
         ↓           ↓           ↓
    (no service) Ollama    vLLM/Ollama
                 (native)  (OpenAI API)
```

## Deployment Scenarios

### Local Development
```go
config := &Config{
    BackendType: "ollama",
    BaseURL:     "http://localhost:11434",
    Model:       "all-minilm",
    Dimension:   384,
}
```

**Setup**: `ollama serve && ollama pull all-minilm`

### Production (CPU)
```go
config := &Config{
    BackendType: "ollama",
    BaseURL:     "http://ollama.ollama.svc.cluster.local:11434",
    Model:       "nomic-embed-text",
    Dimension:   768,
}
```

**Deploy**: Ollama in Kubernetes (no GPU required)

### Production (GPU)
```go
config := &Config{
    BackendType: "vllm",
    BaseURL:     "http://vllm-embeddings.embeddings.svc.cluster.local:8000",
    Model:       "intfloat/e5-mistral-7b-instruct",
    Dimension:   4096,
}
```

**Deploy**: vLLM in Kubernetes with GPU

### Testing / CI/CD
```go
config := &Config{
    BackendType: "placeholder",
    Dimension:   384,
}
```

**Setup**: Zero - works immediately

## Comparison with Codegate

| Aspect | ToolHive (Go) | Codegate (Python) |
|--------|---------------|-------------------|
| **Model** | all-minilm (configurable) | all-MiniLM-L6-v2 |
| **Dimensions** | 384 (configurable) | 384 |
| **Inference** | HTTP to service | llama.cpp (native) |
| **Language** | Pure Go | Python + C++ |
| **Dependencies** | Service (optional) | llama.cpp (required) |
| **Setup** | `ollama serve` or none | Build llama.cpp |
| **Deployment** | Single binary | Python + model files |
| **Performance** | Good (50-100ms) | Excellent (10-20ms) |
| **Fallback** | Automatic | None |
| **GPU Support** | Via vLLM | Via llama.cpp |

## Why vLLM for Production

**vLLM** is recommended for production GPU deployments because:

1. **High Performance**: PagedAttention, dynamic batching, optimized GPU usage
2. **Production-Ready**: Used widely in industry, battle-tested
3. **Scalable**: Handles high concurrency efficiently
4. **Compatible**: OpenAI-compatible API
5. **Kubernetes-Friendly**: Easy to deploy with Helm

**Comparison**:

| Feature | vLLM (GPU) | Ollama (CPU) | Placeholder |
|---------|-----------|--------------|-------------|
| Latency | 10-30ms | 50-100ms | <1ms |
| Throughput | Very High | Medium | Very High |
| GPU Required | Yes | No | No |
| Batch Efficiency | Excellent | Good | N/A |
| Setup Complexity | Medium | Low | None |
| Production Grade | ✅ | ✅ | ❌ (testing only) |

## Protocol Compatibility

Both Ollama and vLLM support the **OpenAI-compatible `/v1/embeddings` endpoint**, allowing our `OpenAICompatibleBackend` to work with both:

**Ollama**:
- Native API: `POST /api/embeddings` (used by `OllamaBackend`)
- OpenAI API: `POST /v1/embeddings` (used by `OpenAICompatibleBackend`)

**vLLM**:
- OpenAI API: `POST /v1/embeddings` (used by `OpenAICompatibleBackend`)

This means you can:
- Use `OllamaBackend` for Ollama-specific features
- Use `OpenAICompatibleBackend` with both Ollama and vLLM
- Switch between services without code changes

## Usage Examples

### Quick Start (Zero Setup)
```go
// Works immediately, no dependencies
manager, _ := NewManager(&Config{
    BackendType: "placeholder",
    Dimension:   384,
})

embeddings, _ := manager.GenerateEmbedding([]string{"hello world"})
```

### Development (Ollama)
```bash
# Terminal 1: Start Ollama
ollama serve
ollama pull all-minilm

# Terminal 2: Use in Go
```

```go
manager, _ := NewManager(&Config{
    BackendType: "ollama",
    Model:       "all-minilm",
    Dimension:   384,
    EnableCache: true,
})

embeddings, _ := manager.GenerateEmbedding([]string{
    "What is this function?",
    "How do I use the optimizer?",
})
```

### Production (vLLM in Kubernetes)
```go
manager, _ := NewManager(&Config{
    BackendType: "vllm",
    BaseURL:     os.Getenv("VLLM_URL"), // http://vllm-service:8000
    Model:       "sentence-transformers/all-MiniLM-L6-v2",
    Dimension:   384,
    EnableCache: true,
})

embeddings, _ := manager.GenerateEmbedding(texts)
```

### Unified (Works with Both)
```go
// Single config that works with Ollama or vLLM
manager, _ := NewManager(&Config{
    BackendType: "unified",
    BaseURL:     os.Getenv("EMBEDDINGS_URL"),
    Model:       os.Getenv("EMBEDDINGS_MODEL"),
    Dimension:   384,
    EnableCache: true,
})
```

## Key Takeaways

1. **No Pure Go Solution**: There's no production-ready, pure Go, zero-dependency embedding library that matches ML model quality.

2. **HTTP is Best**: Using HTTP APIs to embedding services (Ollama, vLLM) provides the best balance of:
   - Pure Go code (no CGO)
   - Production performance
   - Easy deployment
   - Flexibility

3. **Pluggable Architecture**: Multiple backends support different scenarios:
   - **Placeholder**: Testing, development, fallback
   - **Ollama**: Local development, CPU deployments
   - **vLLM**: Production with GPU

4. **Graceful Degradation**: Automatic fallback ensures the application always works, even if embedding services are unavailable.

5. **OpenAI Protocol**: Using the industry-standard OpenAI API format ensures compatibility and future-proofing.

## Future Enhancements

- **Batch API support**: Use batch endpoints when available for efficiency
- **Connection pooling**: Reuse HTTP connections for better performance
- **Retry logic**: Automatic retries with exponential backoff
- **Circuit breaker**: Temporarily disable backends that are consistently failing
- **Model auto-detection**: Automatically detect embedding dimensions from model metadata
- **Pure Go backend**: If/when a production-ready pure Go library emerges

## Conclusion

The current architecture provides:
- ✅ Zero-dependency option (placeholder)
- ✅ Easy local development (Ollama)
- ✅ Production-grade performance (vLLM)
- ✅ Pure Go implementation (no CGO)
- ✅ Single binary deployment
- ✅ Automatic fallback (graceful degradation)
- ✅ Protocol compatibility (OpenAI standard)
- ✅ Flexibility (pluggable backends)

This design balances **practicality** (works now), **performance** (production-ready), and **simplicity** (pure Go, easy deployment).


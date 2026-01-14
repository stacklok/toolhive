# Optimizer Package

The optimizer package provides semantic tool discovery and ingestion for MCP servers in ToolHive's vMCP. It enables intelligent, context-aware tool selection to reduce token usage and improve LLM performance.

## Features

- **Pure Go**: No CGO dependencies - uses [chromem-go](https://github.com/philippgille/chromem-go) for vector search
- **In-Memory by Default**: Fast ephemeral database with optional persistence
- **Pluggable Embeddings**: Supports vLLM, Ollama, and placeholder backends
- **Event-Driven**: Integrates with vMCP's `OnRegisterSession` hook for automatic ingestion
- **Semantic Search**: Cosine similarity search for intelligent tool discovery
- **Token Counting**: Tracks token usage for LLM consumption metrics

## Architecture

```
pkg/optimizer/
├── models/           # Domain models (Server, Tool, etc.)
├── db/               # chromem-go database layer (pure Go)
├── embeddings/       # Embedding backends (vLLM, Ollama, placeholder)
├── ingestion/        # Event-driven ingestion service
└── tokens/           # Token counting for LLM metrics
```

## Embedding Backends

The optimizer supports multiple embedding backends:

| Backend | Use Case | Performance | Setup |
|---------|----------|-------------|-------|
| **vLLM** | **Production/Kubernetes (recommended)** | Excellent (GPU) | Deploy vLLM service |
| Ollama | Local development, CPU-only | Good | `ollama serve` |
| Placeholder | Testing, CI/CD | Fast (hash-based) | Zero setup |

**For production Kubernetes deployments, vLLM is recommended** due to its high-throughput performance, GPU efficiency (PagedAttention), and scalability for multi-user environments.

## Quick Start

### vMCP Integration (Recommended)

The optimizer is designed to work as part of vMCP, not standalone:

```yaml
# examples/vmcp-config-optimizer.yaml
optimizer:
  enabled: true
  embeddingBackend: placeholder  # or "ollama", "openai-compatible"
  embeddingDimension: 384
  # persistPath: /data/optimizer  # Optional: for persistence
```

Start vMCP with optimizer:

```bash
thv vmcp serve --config examples/vmcp-config-optimizer.yaml
```

When optimizer is enabled, vMCP exposes:
- `optim.find_tool`: Semantic search for tools
- `optim.call_tool`: Dynamic tool invocation

### Programmatic Usage

```go
import (
    "context"
    
    "github.com/stacklok/toolhive/pkg/optimizer/db"
    "github.com/stacklok/toolhive/pkg/optimizer/embeddings"
    "github.com/stacklok/toolhive/pkg/optimizer/ingestion"
)

func main() {
    ctx := context.Background()

    // Initialize database (in-memory)
    database, err := db.NewDB(&db.Config{
        PersistPath: "", // Empty = in-memory only
    })
    if err != nil {
        panic(err)
    }

    // Initialize embedding manager with placeholder (no external dependencies)
    embeddingMgr, err := embeddings.NewManager(&embeddings.Config{
        BackendType: "placeholder",
        Dimension:   384,
    })
    if err != nil {
        panic(err)
    }

    // Create ingestion service
    svc, err := ingestion.NewService(&ingestion.Config{
        DBConfig:        &db.Config{PersistPath: ""},
        EmbeddingConfig: embeddingMgr.Config(),
    })
    if err != nil {
        panic(err)
    }
    defer svc.Close()

    // Ingest a server (called by vMCP on session registration)
    err = svc.IngestServer(ctx, "server-id", "MyServer", nil, []mcp.Tool{...})
    if err != nil {
        panic(err)
    }
}
```

### Production Deployment with vLLM (Kubernetes)

```yaml
optimizer:
  enabled: true
  embeddingBackend: openai-compatible
  embeddingURL: http://vllm-service:8000/v1
  embeddingModel: BAAI/bge-small-en-v1.5
  embeddingDimension: 768
  persistPath: /data/optimizer  # Persistent storage for faster restarts
```

Deploy vLLM alongside vMCP:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: vllm-embeddings
spec:
  template:
    spec:
      containers:
      - name: vllm
        image: vllm/vllm-openai:latest
        args:
          - --model
          - BAAI/bge-small-en-v1.5
          - --port
          - "8000"
        resources:
          limits:
            nvidia.com/gpu: 1
```

### Local Development with Ollama

```bash
# Start Ollama
ollama serve

# Pull an embedding model
ollama pull nomic-embed-text
```

Configure vMCP:

```yaml
optimizer:
  enabled: true
  embeddingBackend: ollama
  embeddingURL: http://localhost:11434
  embeddingModel: nomic-embed-text
  embeddingDimension: 384
```

## Configuration

### Database

- **Storage**: chromem-go (pure Go, no CGO)
- **Default**: In-memory (ephemeral)
- **Persistence**: Optional via `persistPath`
- **Format**: Binary (gob encoding)

### Embedding Models

Common embedding dimensions:
- **384**: all-MiniLM-L6-v2, nomic-embed-text (default)
- **768**: BAAI/bge-small-en-v1.5
- **1536**: OpenAI text-embedding-3-small

### Performance

From chromem-go benchmarks (mid-range 2020 Intel laptop):
- **1,000 tools**: ~0.5ms query time
- **5,000 tools**: ~2.2ms query time
- **25,000 tools**: ~9.9ms query time
- **100,000 tools**: ~39.6ms query time

Perfect for typical vMCP deployments (hundreds to thousands of tools).

## Testing

Run the unit tests:

```bash
# Test all packages
go test ./pkg/optimizer/...

# Test with coverage
go test -cover ./pkg/optimizer/...

# Test specific package
go test ./pkg/optimizer/models
```

## Development

### Adding New Features

1. **Models**: Add new domain models in `pkg/optimizer/models/`
2. **Database**: Add new operations in `pkg/optimizer/db/`
3. **Tests**: Add unit tests following existing patterns

### chromem-go Integration

The optimizer uses chromem-go collections:
- **backend_servers**: Server metadata and embeddings
- **backend_tools**: Tool metadata and embeddings

Each collection stores:
- **ID**: Unique identifier
- **Content**: Text for embedding (name + description)
- **Metadata**: JSON-serialized data (server/tool details)
- **Embedding**: Generated automatically by chromem-go

## Comparison with sqlite-vec Version

| Feature | sqlite-vec (CGO) | chromem-go (Pure Go) |
|---------|------------------|----------------------|
| Database | mattn/go-sqlite3 | chromem-go |
| Dependencies | CGO required | Zero CGO |
| Vector Search | sqlite-vec extension | Built-in cosine similarity |
| Performance | Good | Excellent for <100K docs |
| Cross-compilation | Complex | Simple |
| Container Size | Larger | Smaller |
| Setup | Extension loading | Zero setup |

## Known Limitations

1. **Scale**: Optimized for <100,000 tools (more than sufficient for typical vMCP deployments)
2. **Approximate Search**: chromem-go uses exhaustive search (not HNSW), but this is fine for our scale
3. **Persistence Format**: Binary gob format (not human-readable)

## Future Enhancements

- [ ] Implement `optim.find_tool` semantic search handler
- [ ] Implement `optim.call_tool` dynamic invocation handler
- [ ] Add batch embedding optimization
- [ ] Add prometheus metrics for query performance
- [ ] Add query result caching

## License

This package is part of ToolHive and follows the same license.

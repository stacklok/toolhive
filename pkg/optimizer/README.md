# Optimizer Package

The optimizer package provides semantic tool discovery and ingestion for MCP servers in ToolHive. It's a Go port of the [mcp-optimizer](https://github.com/stacklok/mcp-optimizer) Python implementation.

## Features

- **Backend Discovery**: Automatically discovers MCP backends from Docker or Kubernetes
- **Semantic Embeddings**: Pluggable embedding backends (vLLM, Ollama, or placeholder)
- **vLLM Support**: Production-ready with vLLM for high-throughput GPU-accelerated embeddings
- **Vector Search**: SQLite-based semantic search with cosine similarity
- **Token Counting**: Tracks token usage for LLM consumption metrics
- **Pure Go**: No CGO required, works with `CGO_ENABLED=0`

## Architecture

```
pkg/optimizer/
├── models/           # Domain models (Server, Tool, etc.)
├── db/               # Database layer with pure Go SQLite
├── embeddings/       # Embedding backends (vLLM, Ollama, placeholder)
├── ingestion/        # Core ingestion service
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

### Standalone Command

```bash
# Initialize and ingest backends (one-time)
thv optimizer ingest \
  --model-path /path/to/model.onnx \
  --runtime-mode docker

# Continuous polling mode
thv optimizer ingest \
  --model-path /path/to/model.onnx \
  --poll-interval 30 \
  --runtime-mode docker

# Query tools semantically
thv optimizer query "get current time" \
  --model-path /path/to/model.onnx \
  --limit 10

# Check status
thv optimizer status
```

### Production Deployment with vLLM (Kubernetes)

**vLLM is the recommended backend for production** due to high-throughput performance, GPU efficiency (PagedAttention), and scalability:

```go
import (
    "context"
    "os"
    "time"
    
    "github.com/stacklok/toolhive/pkg/optimizer/db"
    "github.com/stacklok/toolhive/pkg/optimizer/embeddings"
    "github.com/stacklok/toolhive/pkg/optimizer/ingestion"
)

func main() {
    // Initialize database
    database, err := db.NewDB(&db.Config{
        Path: "/data/optimizer.db",
    })
    if err != nil {
        panic(err)
    }
    defer database.Close()

    // Initialize embedding manager with vLLM (recommended for production)
    embeddingMgr, err := embeddings.NewManager(&embeddings.Config{
        BackendType:  "vllm",  // Use vLLM for production Kubernetes deployments
        BaseURL:      os.Getenv("VLLM_URL"), // e.g., http://vllm-service:8000
        Model:        "sentence-transformers/all-MiniLM-L6-v2",
        Dimension:    384,
        EnableCache:  true,
        MaxCacheSize: 1000,
    })
    if err != nil {
        panic(err)
    }
    defer embeddingMgr.Close()

    // Start ingestion service as background goroutine
    svc, err := ingestion.NewService(&ingestion.Config{
        DB:               database,
        EmbeddingManager: embeddingMgr,
        PollInterval:     30 * time.Second,
    })
    if err != nil {
        panic(err)
    }

    go func() {
        if err := svc.Run(context.Background()); err != nil {
            log.Printf("Ingestion service error: %v", err)
        }
    }()
    
    // ... rest of vMCP initialization
}
```

### Local Development with Ollama

For local development without GPU requirements:

## Configuration

### Database

The optimizer uses SQLite with the sqlite-vec extension for vector similarity search:

- **Default Location**: `~/.toolhive/optimizer.db`
- **Schema**: Separates registry servers (from catalog) and backend servers (running instances)
- **Vector Tables**: Uses `vec0` virtual tables with cosine distance for embeddings

### Embedding Model

The optimizer requires an ONNX embedding model:

- **Default Model**: BAAI/bge-small-en-v1.5 (384 dimensions)
- **Format**: ONNX model file (.onnx)
- **Environment**: Set `SQLITE_VEC_PATH` for sqlite-vec extension location

### Runtime Modes

- **docker**: Discovers backends from Docker daemon (default)
- **k8s**: Discovers backends from Kubernetes API

## Database Schema

### Tables

- `mcpservers_registry`: Registry servers from catalog
- `mcpservers_backend`: Running backend servers
- `tools_registry`: Tools from registry servers
- `tools_backend`: Tools from backend servers (with token counts)

### Vector Tables

- `registry_server_vector`: Server embeddings (registry)
- `registry_tool_vectors`: Tool embeddings (registry)
- `backend_server_vector`: Server embeddings (backend)
- `backend_tool_vectors`: Tool embeddings (backend)

### FTS Tables

- `registry_tool_fts`: Full-text search for registry tools
- `backend_tool_fts`: Full-text search for backend tools

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

### Using Real Embeddings

The current implementation uses placeholder embeddings for testing. To use real embeddings:

**Option 1: Ollama (Recommended for local development)**
```bash
# Start Ollama
ollama serve

# Pull an embedding model
ollama pull nomic-embed-text

# Use in code
config := &embeddings.Config{
    BackendType: "ollama",
    BaseURL:     "http://localhost:11434",
    Model:       "nomic-embed-text",
}
```

**Option 2: vLLM (Recommended for production)**
```bash
# Start vLLM with an embedding model
vllm serve sentence-transformers/all-MiniLM-L6-v2

# Use in code
config := &embeddings.Config{
    BackendType: "vllm",
    BaseURL:     "http://localhost:8000",
    Model:       "sentence-transformers/all-MiniLM-L6-v2",
}
```

## Comparison with Python Version

This Go implementation follows the same architecture as the Python mcp-optimizer:

| Feature | Python | Go |
|---------|--------|-----|
| Database | SQLAlchemy + aiosqlite | database/sql + modernc.org/sqlite |
| Embeddings | FastEmbed | Ollama/vLLM (OpenAI-compatible) |
| Vector Search | sqlite-vec | sqlite-vec |
| CLI | Click | Cobra |
| Testing | pytest | go test |
| Async | asyncio | goroutines |

## Known Limitations

1. **ONNX Integration**: Currently uses placeholder embeddings (requires tokenizer)
2. **Backend Discovery**: Docker/K8s discovery not fully implemented
3. **MCP Client**: Tool listing from MCP servers is simplified
4. **Query Command**: Semantic search not yet implemented

## Future Enhancements

- [ ] Complete ONNX Runtime integration with tokenizer
- [ ] Implement Docker backend discovery
- [ ] Implement Kubernetes backend discovery
- [ ] Add MCP client for tool listing
- [ ] Implement semantic query search
- [ ] Add registry server ingestion
- [ ] Add batch ingestion optimization
- [ ] Add connection pooling for MCP clients

## License

This package is part of ToolHive and follows the same license.



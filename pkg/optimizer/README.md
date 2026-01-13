# Optimizer Package

The optimizer package provides semantic tool discovery and ingestion for MCP servers in ToolHive. It's a Go port of the [mcp-optimizer](https://github.com/stacklok/mcp-optimizer) Python implementation.

## Features

- **Backend Discovery**: Automatically discovers MCP backends from Docker or Kubernetes
- **Semantic Embeddings**: Generates embeddings using ONNX Runtime for semantic tool search
- **Vector Search**: Uses sqlite-vec for efficient similarity search
- **Token Counting**: Tracks token usage for LLM consumption metrics
- **Dual Operation Modes**: Can run as a standalone command or integrated as a goroutine

## Architecture

```
pkg/optimizer/
├── models/           # Domain models (Server, Tool, etc.)
├── db/               # Database layer with SQLite + sqlite-vec
├── embeddings/       # ONNX Runtime embedding manager
├── ingestion/        # Core ingestion service
└── tokens/           # Token counting for LLM metrics
```

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

### Integrated with vMCP

```go
import (
    "context"
    "time"
    
    "github.com/stacklok/toolhive/pkg/optimizer/db"
    "github.com/stacklok/toolhive/pkg/optimizer/embeddings"
    "github.com/stacklok/toolhive/pkg/optimizer/ingestion"
)

func main() {
    // Create ingestion service
    config := &ingestion.Config{
        DBConfig: &db.Config{
            DBPath: "/path/to/optimizer.db",
        },
        EmbeddingConfig: &embeddings.Config{
            ModelPath:    "/path/to/model.onnx",
            Dimension:    384,
            EnableCache:  true,
            MaxCacheSize: 1000,
        },
        RuntimeMode: "docker",
    }
    
    svc, err := ingestion.NewService(config)
    if err != nil {
        log.Fatal(err)
    }
    defer svc.Close()
    
    // Start polling as goroutine
    ctx := context.Background()
    go svc.StartPolling(ctx, 30*time.Second)
    
    // Your vMCP server logic here...
}
```

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



# Optimizer Package

The optimizer package provides semantic tool discovery and ingestion for MCP servers in ToolHive. It's a Go port of the [mcp-optimizer](https://github.com/stacklok/mcp-optimizer) Python implementation.

## Features

- **Workload Discovery**: Automatically discovers MCP workloads from Docker or Kubernetes
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
# Initialize and ingest workloads (one-time)
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
- **Schema**: Separates registry servers (from catalog) and workload servers (running instances)
- **Vector Tables**: Uses `vec0` virtual tables with cosine distance for embeddings

### Embedding Model

The optimizer requires an ONNX embedding model:

- **Default Model**: BAAI/bge-small-en-v1.5 (384 dimensions)
- **Format**: ONNX model file (.onnx)
- **Environment**: Set `SQLITE_VEC_PATH` for sqlite-vec extension location

### Runtime Modes

- **docker**: Discovers workloads from Docker daemon (default)
- **k8s**: Discovers workloads from Kubernetes API

## Database Schema

### Tables

- `mcpservers_registry`: Registry servers from catalog
- `mcpservers_workload`: Running workload servers
- `tools_registry`: Tools from registry servers
- `tools_workload`: Tools from workload servers (with token counts)

### Vector Tables

- `registry_server_vector`: Server embeddings (registry)
- `registry_tool_vectors`: Tool embeddings (registry)
- `workload_server_vector`: Server embeddings (workload)
- `workload_tool_vectors`: Tool embeddings (workload)

### FTS Tables

- `registry_tool_fts`: Full-text search for registry tools
- `workload_tool_fts`: Full-text search for workload tools

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
3. **Migrations**: Create new SQL files in `pkg/optimizer/db/migrations/`
4. **Tests**: Add unit tests following existing patterns

### ONNX Model Integration

The current implementation includes a placeholder for ONNX Runtime inference. To complete the integration:

1. Install ONNX Runtime C library
2. Implement tokenization (BPE tokenizer)
3. Replace `generatePlaceholderEmbedding()` with actual ONNX inference
4. Handle input_ids and attention_mask tensors

## Comparison with Python Version

This Go implementation follows the same architecture as the Python mcp-optimizer:

| Feature | Python | Go |
|---------|--------|-----|
| Database | SQLAlchemy + aiosqlite | database/sql + go-sqlite3 |
| Embeddings | FastEmbed | ONNX Runtime |
| Vector Search | sqlite-vec | sqlite-vec |
| CLI | Click | Cobra |
| Testing | pytest | go test |
| Async | asyncio | goroutines |

## Known Limitations

1. **ONNX Integration**: Currently uses placeholder embeddings (requires tokenizer)
2. **Workload Discovery**: Docker/K8s discovery not fully implemented
3. **MCP Client**: Tool listing from MCP servers is simplified
4. **Query Command**: Semantic search not yet implemented

## Future Enhancements

- [ ] Complete ONNX Runtime integration with tokenizer
- [ ] Implement Docker workload discovery
- [ ] Implement Kubernetes workload discovery
- [ ] Add MCP client for tool listing
- [ ] Implement semantic query search
- [ ] Add registry server ingestion
- [ ] Add batch ingestion optimization
- [ ] Add connection pooling for MCP clients

## License

This package is part of ToolHive and follows the same license.



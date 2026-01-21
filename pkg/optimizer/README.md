# Optimizer Package

The optimizer package provides semantic tool discovery and ingestion for MCP servers in ToolHive's vMCP. It enables intelligent, context-aware tool selection to reduce token usage and improve LLM performance.

## Features

- **Pure Go**: No CGO dependencies - uses [chromem-go](https://github.com/philippgille/chromem-go) for vector search and `modernc.org/sqlite` for FTS5
- **Hybrid Search**: Combines semantic search (chromem-go) with BM25 full-text search (SQLite FTS5)
- **In-Memory by Default**: Fast ephemeral database with optional persistence
- **Pluggable Embeddings**: Supports vLLM, Ollama, and placeholder backends
- **Event-Driven**: Integrates with vMCP's `OnRegisterSession` hook for automatic ingestion
- **Semantic + Keyword Search**: Configurable ratio between semantic and BM25 search
- **Token Counting**: Tracks token usage for LLM consumption metrics

## Architecture

```
pkg/optimizer/
├── models/           # Domain models (Server, Tool, etc.)
├── db/               # Hybrid database layer (chromem-go + SQLite FTS5)
│   ├── db.go         # Database coordinator
│   ├── fts.go        # SQLite FTS5 for BM25 search (pure Go)
│   ├── hybrid.go     # Hybrid search combining semantic + BM25
│   ├── backend_server.go  # Server operations
│   └── backend_tool.go    # Tool operations
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

## Hybrid Search

The optimizer **always uses hybrid search** combining:

1. **Semantic Search** (chromem-go): Understands meaning and context via embeddings
2. **BM25 Full-Text Search** (SQLite FTS5): Keyword matching with Porter stemming

This dual approach ensures the best of both worlds: semantic understanding for intent-based queries and keyword precision for technical terms and acronyms.

### Configuration

```yaml
optimizer:
  enabled: true
  embeddingBackend: placeholder
  embeddingDimension: 384
  # persistPath: /data/optimizer  # Optional: for persistence
  # ftsDBPath: /data/optimizer-fts.db  # Optional: defaults to :memory: or {persistPath}/fts.db
  hybridSearchRatio: 70  # 70% semantic, 30% BM25 (default, 0-100 percentage)
```

| Ratio | Semantic | BM25 | Best For |
|-------|----------|------|----------|
| 1.0 | 100% | 0% | Pure semantic (intent-heavy queries) |
| 0.7 | 70% | 30% | **Default**: Balanced hybrid |
| 0.5 | 50% | 50% | Equal weight |
| 0.0 | 0% | 100% | Pure keyword (exact term matching) |

### How It Works

1. **Parallel Execution**: Semantic and BM25 searches run concurrently
2. **Result Merging**: Combines results and removes duplicates
3. **Ranking**: Sorts by similarity/relevance score
4. **Limit Enforcement**: Returns top N results

### Example Queries

| Query | Semantic Match | BM25 Match | Winner |
|-------|----------------|------------|--------|
| "What's the weather?" | ✅ `get_current_weather` | ✅ `weather_forecast` | Both (deduped) |
| "SQL database query" | ❌ (no embeddings) | ✅ `execute_sql` | BM25 |
| "Make it rain outside" | ✅ `weather_control` | ❌ (no keyword) | Semantic |

## Quick Start

### vMCP Integration (Recommended)

The optimizer is designed to work as part of vMCP, not standalone:

```yaml
# examples/vmcp-config-optimizer.yaml
optimizer:
  enabled: true
  embeddingBackend: placeholder  # or "ollama", "openai-compatible"
  embeddingDimension: 384
  # persistPath: /data/optimizer  # Optional: for chromem-go persistence
  # ftsDBPath: /data/fts.db  # Optional: auto-defaults to :memory: or {persistPath}/fts.db
  # hybridSearchRatio: 70  # Optional: 70% semantic, 30% BM25 (default, 0-100 percentage)
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

    // Initialize embedding manager with Ollama (default)
    embeddingMgr, err := embeddings.NewManager(&embeddings.Config{
        BackendType: "ollama",
        BaseURL:     "http://localhost:11434",
        Model:       "all-minilm",
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
ollama pull all-minilm
```

Configure vMCP:

```yaml
optimizer:
  enabled: true
  embeddingBackend: ollama
  embeddingURL: http://localhost:11434
  embeddingModel: all-minilm
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

## Inspecting the Database

The optimizer uses a hybrid database (chromem-go + SQLite FTS5). Here's how to inspect each:

### Inspecting SQLite FTS5 (Easiest)

The FTS5 database is standard SQLite and can be opened with any SQLite tool:

```bash
# Use sqlite3 CLI
sqlite3 /tmp/vmcp-optimizer-fts.db

# Count documents
SELECT COUNT(*) FROM backend_servers_fts;
SELECT COUNT(*) FROM backend_tools_fts;

# View tool names and descriptions
SELECT tool_name, tool_description FROM backend_tools_fts LIMIT 10;

# Full-text search with BM25 ranking
SELECT tool_name, rank 
FROM backend_tool_fts_index 
WHERE backend_tool_fts_index MATCH 'github repository' 
ORDER BY rank 
LIMIT 5;

# Join servers and tools
SELECT s.name, t.tool_name, t.tool_description
FROM backend_tools_fts t
JOIN backend_servers_fts s ON t.mcpserver_id = s.id
LIMIT 10;
```

**VSCode Extension**: Install `alexcvzz.vscode-sqlite` to view `.db` files directly in VSCode.

### Inspecting chromem-go (Vector Database)

chromem-go uses `.gob` binary files. Use the provided inspection scripts:

```bash
# Quick summary (shows collection sizes and first few documents)
go run scripts/inspect-chromem-raw.go /tmp/vmcp-optimizer-debug.db

# View specific tool with full metadata and embeddings
go run scripts/view-chromem-tool.go /tmp/vmcp-optimizer-debug.db get_file_contents

# View all documents (warning: lots of output)
go run scripts/view-chromem-tool.go /tmp/vmcp-optimizer-debug.db

# Search by content
go run scripts/view-chromem-tool.go /tmp/vmcp-optimizer-debug.db "search"
```

### chromem-go Schema

Each document in chromem-go contains:

```go
Document {
  ID:        string              // "github" or UUID for tools
  Content:   string              // "tool_name. description..."
  Embedding: []float32           // 384-dimensional vector
  Metadata:  map[string]string   // {"type": "backend_tool", "server_id": "github", "data": "...JSON..."}
}
```

**Collections**:
- `backend_servers`: Server metadata (3 documents in typical setup)
- `backend_tools`: Tool metadata and embeddings (40+ documents)

## Known Limitations

1. **Scale**: Optimized for <100,000 tools (more than sufficient for typical vMCP deployments)
2. **Approximate Search**: chromem-go uses exhaustive search (not HNSW), but this is fine for our scale
3. **Persistence Format**: Binary gob format (not human-readable)

## License

This package is part of ToolHive and follows the same license.

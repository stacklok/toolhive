# Optimizer Ingestion Tests

## Running the Full Integration Test

The optimizer ingestion tests demonstrate the complete workflow of ingesting MCP tools into a searchable database with vector embeddings.

### Quick Start

```bash
# Using task (recommended)
task test-optimizer

# Or run the script directly
./scripts/test-optimizer-with-sqlite-vec.sh

# Run specific test
./scripts/test-optimizer-with-sqlite-vec.sh -run TestServiceCreationAndIngestion
```

### What the Script Does

The `test-optimizer-with-sqlite-vec.sh` script:

1. **Detects your OS and architecture** (macOS/Linux, x86_64/ARM64)
2. **Downloads sqlite-vec** if not already present (cached in `/tmp/sqlite-vec/`)
3. **Sets up the environment** with proper CGO flags and FTS5 support
4. **Runs the integration tests** with real vector similarity search

### Requirements

- **Go 1.21+** with CGO enabled
- **curl** for downloading sqlite-vec
- **Internet connection** (first run only, to download sqlite-vec)

### What Gets Tested

The main test (`TestServiceCreationAndIngestion`) verifies:

- ✅ **Database Creation** - SQLite database with migrations
- ✅ **sqlite-vec Integration** - Vector tables for embeddings
- ✅ **FTS5 Integration** - Full-text search tables
- ✅ **Tool Ingestion** - MCP tools stored with metadata
- ✅ **Embedding Generation** - 384-dimensional embeddings
- ✅ **Token Counting** - LLM token usage metrics
- ✅ **Semantic Search** - Vector similarity search (cosine distance)
- ✅ **Tool Updates** - Delete and re-sync operations

### Example Output

```
🔍 ToolHive Optimizer Integration Tests
==========================================

✓ sqlite-vec available: /tmp/sqlite-vec/vec0.dylib

🧪 Running optimizer tests with sqlite-vec...

=== RUN   TestServiceCreationAndIngestion
    Tool 0: name=get_weather, tokens=54, embedding_dim=384
    Tool 1: name=get_time, tokens=47, embedding_dim=384
    Search results for 'what's the weather':
      1. test-server - get_weather (distance: 0.2509, tokens: 54)
      2. test-server - get_time (distance: 0.2523, tokens: 47)
    ✓ Complete workflow tested:
      - Database creation
      - Service initialization
      - Tool ingestion with embeddings
      - Token counting
      - Semantic search
      - Tool updates
--- PASS: TestServiceCreationAndIngestion (0.04s)

✅ All tests passed!
```

### Manual Setup (Optional)

If you prefer to set up sqlite-vec manually:

```bash
# Download sqlite-vec for your platform
cd /tmp
curl -L https://github.com/asg017/sqlite-vec/releases/download/v0.1.1/sqlite-vec-0.1.1-loadable-macos-aarch64.tar.gz -o sqlite-vec.tar.gz
tar xzf sqlite-vec.tar.gz

# Run tests with explicit path
export SQLITE_VEC_PATH=/tmp/vec0.dylib
export CGO_ENABLED=1
go test -tags="fts5" ./pkg/optimizer/ingestion/... -v
```

### Troubleshooting

#### sqlite-vec not found

The script automatically downloads sqlite-vec. If download fails:
- Check internet connection
- Visit https://github.com/asg017/sqlite-vec/releases manually
- Download the appropriate version for your OS/architecture
- Extract to `/tmp/sqlite-vec/` or set `SQLITE_VEC_PATH`

#### CGO errors

Ensure CGO is enabled:
```bash
export CGO_ENABLED=1
```

#### FTS5 errors

The script automatically builds with FTS5 support using `-tags="fts5"`. If you see FTS5 errors, ensure you're using the script or including the build tag.

#### Ollama test failures

The Ollama integration test (`TestServiceWithOllama`) requires:
- Ollama running locally (`ollama serve`)
- The `all-minilm` model installed (`ollama pull all-minilm`)
- This test is skipped in short mode (`-short` flag)

### CI/CD Integration

For CI environments, the script will:
- Download sqlite-vec automatically
- Cache it in `/tmp/sqlite-vec/`
- Work on both Linux and macOS runners
- Support both x86_64 and ARM64 architectures

Example GitHub Actions:

```yaml
- name: Run Optimizer Tests
  run: task test-optimizer
```

### Architecture

The ingestion service demonstrates:

```
┌─────────────────────────────────────────┐
│         Ingestion Service               │
│  ┌────────────────────────────────┐    │
│  │   MCP Tool Discovery           │    │
│  └────────────────────────────────┘    │
│              ↓                          │
│  ┌────────────────────────────────┐    │
│  │   Embedding Generation         │    │
│  │   (384-dim vectors)            │    │
│  └────────────────────────────────┘    │
│              ↓                          │
│  ┌────────────────────────────────┐    │
│  │   Token Counting               │    │
│  └────────────────────────────────┘    │
│              ↓                          │
│  ┌────────────────────────────────┐    │
│  │   Database Storage             │    │
│  │   - Tools table                │    │
│  │   - Vector table (sqlite-vec)  │    │
│  │   - FTS table (FTS5)           │    │
│  └────────────────────────────────┘    │
└─────────────────────────────────────────┘
```

### Related Documentation

- [Embeddings Package](../embeddings/README.md) - Embedding generation backends
- [Database Schema](../db/migrations/001_initial.sql) - Database structure
- [Models](../models/models.go) - Data models


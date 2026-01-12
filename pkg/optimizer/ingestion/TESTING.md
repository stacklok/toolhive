# Quick Testing Reference

## Run the Full Integration Test

```bash
# Easiest way - using task
task test-optimizer

# Or directly
./scripts/test-optimizer-with-sqlite-vec.sh

# Run specific test only
./scripts/test-optimizer-with-sqlite-vec.sh -run TestServiceCreationAndIngestion
```

## What It Tests

✅ Complete ingestion workflow:
- Database creation with migrations
- sqlite-vec vector search
- FTS5 full-text search  
- Tool ingestion with embeddings (384-dim)
- Token counting for LLM metrics
- Semantic search by embedding similarity
- Tool updates (delete + re-sync)

## Requirements

- Go 1.21+ with CGO
- curl (for downloading sqlite-vec)
- Internet (first run only)

The script automatically:
- Detects your OS/architecture
- Downloads sqlite-vec if needed
- Sets up environment
- Runs tests

## Output Example

```
🔍 ToolHive Optimizer Integration Tests
==========================================

✓ sqlite-vec available: /tmp/sqlite-vec/vec0.dylib

🧪 Running optimizer tests with sqlite-vec...

Tool 0: name=get_weather, tokens=54, embedding_dim=384
Tool 1: name=get_time, tokens=47, embedding_dim=384

Search results for 'what's the weather':
  1. test-server - get_weather (distance: 0.2509, tokens: 54)
  2. test-server - get_time (distance: 0.2523, tokens: 47)

✓ Complete workflow tested

✅ All tests passed!
```

## See Also

- [Full Documentation](README.md) - Detailed testing guide
- [Embeddings](../embeddings/README.md) - Embedding backends
- [Database Schema](../db/migrations/001_initial.sql) - DB structure


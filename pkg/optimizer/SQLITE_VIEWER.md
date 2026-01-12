# Viewing the Optimizer Database in Cursor

## Problem: "no such module: vec0"

Cursor's built-in SQLite viewer doesn't have the `sqlite-vec` extension loaded, so it can't display the vector tables (`workload_tool_vectors`, etc.).

## Solutions

### Option 1: Use the Query Script (Recommended)

```bash
# Query with sqlite-vec support
./scripts/query-optimizer-db.sh /tmp/optimizer-test.db
```

This opens an interactive SQLite shell with the vec0 extension loaded.

### Option 2: Use SQLite CLI Directly

```bash
# Load extension and query
sqlite3 /tmp/optimizer-test.db -cmd ".load /tmp/sqlite-vec/vec0.dylib"
```

### Option 3: View Non-Vector Tables Only in Cursor

The following tables work fine in Cursor's viewer (no vec0 dependency):

✅ **Safe to view in Cursor:**
- `mcpservers_registry` - Registry MCP servers
- `mcpservers_workload` - Running workload servers
- `tools_registry` - Registry tools
- `tools_workload` - **Workload tools (main tool data)**
- `workload_tool_fts` - Full-text search index
- `workload_tool_fts_*` - FTS internal tables
- `migrations` - Schema version

❌ **Require vec0 extension:**
- `workload_tool_vectors` - Tool embeddings
- `workload_server_vector` - Server embeddings
- `registry_tool_vectors` - Registry tool embeddings
- `registry_server_vector` - Registry server embeddings
- `*_chunks`, `*_rowids`, `*_vector_chunks*` - sqlite-vec internal tables

### Option 4: Query Without Opening Tables

In Cursor, you can use the SQLite viewer to **run queries** without opening the vector tables:

```sql
-- View all tools (works fine)
SELECT 
    json_extract(details, '$.name') as tool_name,
    json_extract(details, '$.description') as description,
    token_count
FROM tools_workload;

-- View servers (works fine)
SELECT name, status, url 
FROM mcpservers_workload;

-- Full-text search (works fine)
SELECT tool_name, tool_description
FROM workload_tool_fts
WHERE workload_tool_fts MATCH 'weather';
```

Just **avoid querying the vector tables** in Cursor's viewer.

## Why This Happens

- `sqlite-vec` is a **runtime-loaded extension** (`.dylib`/`.so` file)
- Cursor's SQLite viewer uses a built-in SQLite without custom extensions
- The vec0 tables are **virtual tables** that require the extension to function

## What You CAN See in Cursor

All the **actual data** is in these tables:
- **Tools**: `tools_workload` (all tool details in JSON)
- **Servers**: `mcpservers_workload` (server metadata)
- **Search**: `workload_tool_fts` (full-text search index)

The **vector tables** are just for similarity search - the embeddings are binary data anyway, so you wouldn't gain much from viewing them directly.

## Alternative: Export Data

```bash
# Export tools to JSON
sqlite3 /tmp/optimizer-test.db "SELECT details FROM tools_workload;" | jq .

# Export to CSV
sqlite3 /tmp/optimizer-test.db -header -csv \
  "SELECT 
     json_extract(details, '$.name') as name,
     json_extract(details, '$.description') as description,
     token_count 
   FROM tools_workload;" > tools.csv
```

## Quick Reference

```bash
# Inspect database (no extension needed)
./scripts/inspect-optimizer-db.sh

# Query with vec0 support
./scripts/query-optimizer-db.sh /tmp/optimizer-test.db

# View in Cursor
# ✅ Open: tools_workload, mcpservers_workload, workload_tool_fts
# ❌ Avoid: *_vectors, *_vector, *_chunks, *_rowids tables
```


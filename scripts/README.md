# ToolHive Scripts

Utility scripts for development, testing, and debugging.

## Optimizer Database Inspection

Tools to inspect the vMCP optimizer's hybrid database (chromem-go + SQLite FTS5).

### SQLite FTS5 Database

```bash
# Quick shell script wrapper
./scripts/inspect-optimizer-db.sh /tmp/vmcp-optimizer-fts.db

# Or use sqlite3 directly
sqlite3 /tmp/vmcp-optimizer-fts.db "SELECT COUNT(*) FROM backend_tools_fts;"
```

### chromem-go Vector Database

chromem-go stores data in binary `.gob` format. Use these Go scripts:

#### Quick Summary
```bash
go run scripts/inspect-chromem-raw/inspect-chromem-raw.go /tmp/vmcp-optimizer-debug.db
```
Shows collection sizes and first few documents from each collection.

**Example output:**
```
📁 Collection ID: 5ff43c0b
   Documents: 4
   - Document ID: github
     Content: github
     Embedding: 384 dimensions
     Type: backend_server
```

#### Detailed View
```bash
# View specific tool
go run scripts/view-chromem-tool/view-chromem-tool.go /tmp/vmcp-optimizer-debug.db get_file_contents

# View all documents
go run scripts/view-chromem-tool/view-chromem-tool.go /tmp/vmcp-optimizer-debug.db

# Search by name/content
go run scripts/view-chromem-tool/view-chromem-tool.go /tmp/vmcp-optimizer-debug.db "search"
```

**Example output:**
```
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
Document ID: 4da1128d-7800-4d4a-a28e-9d1ad8fcb989
Content: get_file_contents. Get the contents of a file...
Embedding Dimensions: 384

Metadata:
  data: {
    "id": "4da1128d-7800-4d4a-a28e-9d1ad8fcb989",
    "mcpserver_id": "github",
    "tool_name": "get_file_contents",
    "description": "Get the contents of a file or directory...",
    "token_count": 38,
    ...
  }
  server_id: github
  type: backend_tool

Embedding (first 10): [0.000, 0.003, 0.001, 0.005, ...]
```

#### VSCode Integration

For SQLite files, install the VSCode extension:
```bash
code --install-extension alexcvzz.vscode-sqlite
```

Then open any `.db` file in VSCode to browse tables visually.

## Testing Scripts

### Optimizer Tests
```bash
# Test with sqlite-vec extension
./scripts/test-optimizer-with-sqlite-vec.sh
```

## Contributing

When adding new scripts:
1. Make shell scripts executable: `chmod +x scripts/your-script.sh`
2. Add error handling and usage instructions
3. Document the script in this README
4. Test on both macOS and Linux if possible

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

### Optimizer Tool Finding Tests

These scripts test the `optim.find_tool` functionality in different scenarios:

#### Test via vMCP Server Connection
```bash
# Test optim.find_tool through a running vMCP server
go run scripts/test-vmcp-find-tool/main.go "read pull requests from GitHub" [server_url]

# Default server URL: http://localhost:4483/mcp
# Example:
go run scripts/test-vmcp-find-tool/main.go "search the web" http://localhost:4483/mcp
```
Connects to a running vMCP server and calls `optim.find_tool` via the MCP protocol. Useful for integration testing with a live server.

#### Call Optimizer Tool Directly
```bash
# Call optim.find_tool via MCP client
go run scripts/call-optim-find-tool/main.go <tool_description> [tool_keywords] [limit] [server_url]

# Examples:
go run scripts/call-optim-find-tool/main.go "search the web" "web search" 20
go run scripts/call-optim-find-tool/main.go "read files" "" 10 http://localhost:4483/mcp
```
A more flexible client for calling `optim.find_tool` with various parameters. Useful for manual testing and debugging.

#### Test Optimizer Handler Directly
```bash
# Test the optimizer handler directly (unit test style)
go run scripts/test-optim-find-tool/main.go "read pull requests from GitHub"
```
Tests the optimizer's `find_tool` handler directly without requiring a full vMCP server. Creates a mock environment with test tools and embeddings. Useful for development and debugging the optimizer logic.

### Other Optimizer Tests
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

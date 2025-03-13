#!/bin/bash

# Send a simple JSON-RPC request to the MCP server
curl -v --max-time 10 -X POST http://localhost:8080/servers/basic-mcp-server/connect \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "initialize",
    "params": {
      "clientInfo": {
        "name": "test-client",
        "version": "1.0.0"
      },
      "protocolVersion": "0.1.0",
      "capabilities": {}
    }
  }'
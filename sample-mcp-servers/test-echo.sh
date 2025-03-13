#!/bin/bash

# Send a JSON-RPC request to the MCP server to call the echo tool
curl --max-time 5 -X POST http://localhost:8080/servers/basic-mcp-server/connect \
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

echo -e "\n\nSending echo request...\n"

curl --max-time 5 -X POST http://localhost:8080/servers/basic-mcp-server/connect \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 2,
    "method": "tools/list",
    "params": {}
  }'

echo -e "\n\nCalling echo tool...\n"

curl --max-time 5 -X POST http://localhost:8080/servers/basic-mcp-server/connect \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 3,
    "method": "tools/call",
    "params": {
      "name": "echo",
      "arguments": {
        "text": "Hello, MCP server!"
      }
    }
  }'
#!/bin/bash

# Set colors for better readability
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

echo -e "${BLUE}=== MCP Server Echo Test Script ===${NC}"
echo -e "${BLUE}This script demonstrates communication with an MCP server using HTTP/SSE transport${NC}"
echo -e "${BLUE}=================================================================${NC}\n"

echo -e "${YELLOW}Step 1: Getting the port from the list command...${NC}"
echo -e "Running: ${GREEN}cargo run -- list --format json | jq -r '.[0].port'${NC}"
PORT=$(cargo run -- list --format json | jq -r '.[0].port')

if [ -z "$PORT" ]; then
  echo -e "${RED}Error: Could not get port from list command${NC}"
  echo -e "${RED}Make sure an MCP server is running and visible to vibetool${NC}"
  exit 1
fi

echo -e "${GREEN}Successfully retrieved port: $PORT${NC}\n"

echo -e "${YELLOW}Step 2: Establishing connection to the MCP server${NC}"
echo -e "${BLUE}According to the MCP specification for HTTP with SSE transport:${NC}"
echo -e "1. The client should first establish an SSE connection to receive messages"
echo -e "2. The server sends an endpoint event with the URI for sending messages"
echo -e "3. All subsequent client messages are sent as HTTP POST requests to this endpoint\n"

echo -e "${YELLOW}In a real implementation, we would:${NC}"
echo -e "1. Connect to ${GREEN}http://localhost:$PORT/sse${NC} for server-sent events"
echo -e "2. Receive the endpoint URI from the server"
echo -e "3. Use that URI for all subsequent requests\n"

echo -e "${YELLOW}For this test script, we'll simulate the process and use the standard endpoint${NC}\n"

echo -e "${YELLOW}Step 3: Sending initialize request to the server...${NC}"
echo -e "Sending JSON-RPC request to: ${GREEN}http://localhost:$PORT/messages${NC}"
echo -e "Method: ${GREEN}initialize${NC}"
echo -e "Request ID: ${GREEN}1${NC}\n"

curl -v --max-time 5 -X POST http://localhost:$PORT/messages \
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

echo -e "\n\n${YELLOW}Step 4: Listing available tools...${NC}"
echo -e "Sending JSON-RPC request to: ${GREEN}http://localhost:$PORT/messages${NC}"
echo -e "Method: ${GREEN}tools/list${NC}"
echo -e "Request ID: ${GREEN}2${NC}\n"

curl -v --max-time 5 -X POST http://localhost:$PORT/messages \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 2,
    "method": "tools/list",
    "params": {}
  }'

echo -e "\n\n${YELLOW}Step 5: Calling the echo tool...${NC}"
echo -e "Sending JSON-RPC request to: ${GREEN}http://localhost:$PORT/messages${NC}"
echo -e "Method: ${GREEN}tools/call${NC}"
echo -e "Tool: ${GREEN}echo${NC}"
echo -e "Request ID: ${GREEN}3${NC}\n"

curl -v --max-time 5 -X POST http://localhost:$PORT/messages \
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

echo -e "\n\n${BLUE}=== Test Complete ===${NC}"
echo -e "${BLUE}If all requests were successful, the MCP server is working correctly${NC}"
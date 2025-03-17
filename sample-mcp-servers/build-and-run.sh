#!/bin/bash
# Script to build and run sample MCP servers with mcp-lok

set -e

# Function to display usage information
usage() {
  echo "Usage: $0 [options]"
  echo "Options:"
  echo "  --build-all         Build all sample MCP servers"
  echo "  --build basic       Build only the basic MCP server"
  echo "  --build weather     Build only the weather MCP server"
  echo "  --build sse         Build only the SSE MCP server"
  echo "  --run basic         Run the basic MCP server with mcp-lok"
  echo "  --run weather       Run the weather MCP server with mcp-lok"
  echo "  --run sse           Run the SSE MCP server with mcp-lok"
  echo "  --help              Display this help message"
  exit 1
}

# Check if no arguments were provided
if [ $# -eq 0 ]; then
  usage
fi

# Function to build a server
build_server() {
  local server_name=$1
  echo "Building $server_name MCP server..."
  pushd "$(dirname "$0")/$server_name-mcp-server"
  podman build -t "$server_name-mcp-server:latest" .
  echo "$server_name MCP server built successfully."
  popd
}

# Function to run a server
run_server() {
  local server_name=$1
  echo "Running $server_name MCP server with mcp-lok..."
  
  # Check if the server is the weather server and if an API key is provided
  if [ "$server_name" == "weather" ]; then
    # Check if OPENWEATHER_API_KEY is set
    if [ -z "$OPENWEATHER_API_KEY" ]; then
      echo "Error: OPENWEATHER_API_KEY environment variable is not set."
      echo "Please set it before running the weather MCP server:"
      echo "export OPENWEATHER_API_KEY=your-api-key-here"
      exit 1
    fi
    
    # Update the server config with the API key
    sed -i "s/your-api-key-here/$OPENWEATHER_API_KEY/g" "$(dirname "$0")/$server_name-mcp-server/server-config.json"
  fi
  
  # Run the server with mcp-lok or cargo run if mcp-lok is not available
  if command -v mcp-lok &> /dev/null; then
    echo "Using mcp-lok command..."
    pushd "$(dirname "$0")"
    mcp-lok run --server-config "$server_name-mcp-server/server-config.json"
    popd
  else
    echo "mcp-lok command not found, using cargo run instead..."
    cargo run -- run --server-config "sample-mcp-servers/$server_name-mcp-server/server-config.json"
  fi
}

# Parse command line arguments
while [ $# -gt 0 ]; do
  case "$1" in
    --build-all)
      build_server "basic"
      build_server "weather"
      build_server "sse"
      shift
      ;;
    --build)
      if [ "$2" == "basic" ] || [ "$2" == "weather" ] || [ "$2" == "sse" ]; then
        build_server "$2"
        shift 2
      else
        echo "Error: Unknown server '$2'"
        usage
      fi
      ;;
    --run)
      if [ "$2" == "basic" ] || [ "$2" == "weather" ] || [ "$2" == "sse" ]; then
        run_server "$2"
        shift 2
      else
        echo "Error: Unknown server '$2'"
        usage
      fi
      ;;
    --help)
      usage
      ;;
    *)
      echo "Error: Unknown option '$1'"
      usage
      ;;
  esac
done
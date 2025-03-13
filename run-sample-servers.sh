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
  echo "  --run basic         Run the basic MCP server with mcp-lok"
  echo "  --run weather       Run the weather MCP server with mcp-lok"
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
  
  pushd sample-mcp-servers/$server_name-mcp-server
  podman build -t $server_name-mcp-server:latest .
  popd
  
  echo "$server_name MCP server built successfully."
}

# Function to run a server
run_server() {
  local server_name=$1
  echo "Running $server_name MCP server with mcp-lok..."
  
  # Load configuration from server-config.json
  local config_file="sample-mcp-servers/$server_name-mcp-server/server-config.json"
  
  # Extract values from the config file
  local image=$(jq -r '.image' "$config_file")
  local transport="stdio"  # Default to stdio transport
  local port=""
  local env_vars=""
  
  # Check if network access is allowed
  local network_allowed=$(jq -r '.network_access.allow_outbound' "$config_file")
  if [ "$network_allowed" == "true" ]; then
    transport="sse"
    port="--port 8080"
    
    # For weather server, check if API key is set
    if [ "$server_name" == "weather" ]; then
      # Check if OPENWEATHER_API_KEY is set
      if [ -z "$OPENWEATHER_API_KEY" ]; then
        echo "Error: OPENWEATHER_API_KEY environment variable is not set."
        echo "Please set it before running the weather MCP server:"
        echo "export OPENWEATHER_API_KEY=your-api-key-here"
        exit 1
      fi
      
      # Update the server config with the API key
      env_vars="OPENWEATHER_API_KEY=$OPENWEATHER_API_KEY"
    fi
  fi
  
  # Create a permission profile based on the server config
  local permission_profile="sample-mcp-servers/$server_name-permission-profile.json"
  
  echo '{
    "read": [],
    "write": [],
    "network": {
      "outbound": {
        "insecure_allow_all": false,
        "allow_transport": ["tcp"],
        "allow_host": [],
        "allow_port": [80, 443]
      }
    }
  }' > "$permission_profile"
  
  # Add allowed domains to the permission profile if network is allowed
  if [ "$network_allowed" == "true" ]; then
    local domains=$(jq -r '.network_access.allowed_domains[]' "$config_file")
    for domain in $domains; do
      jq --arg domain "$domain" '.network.outbound.allow_host += [$domain]' "$permission_profile" > "$permission_profile.tmp" && mv "$permission_profile.tmp" "$permission_profile"
    done
    
    # Set insecure_allow_all to true if no specific domains are provided
    if [ -z "$domains" ]; then
      jq '.network.outbound.insecure_allow_all = true' "$permission_profile" > "$permission_profile.tmp" && mv "$permission_profile.tmp" "$permission_profile"
    fi
  fi
  
  # Get command and args from config
  local cmd=$(jq -r '.command[0]' "$config_file")
  local args=$(jq -r '.args[]' "$config_file")
  
  # Format the args properly for mcp-lok
  local args_str="-- $cmd $args"
  
  # Run the server with mcp-lok
  echo "Running with: mcp-lok run --name $server_name --transport $transport $port --permission-profile $permission_profile $image $args_str"
  
  if [ -n "$env_vars" ]; then
    cargo run -- run --name $server_name --transport $transport $port --permission-profile $permission_profile $image $args_str
  else
    cargo run -- run --name $server_name --transport $transport $port --permission-profile $permission_profile $image $args_str
  fi
}

# Parse command line arguments
while [ $# -gt 0 ]; do
  case "$1" in
    --build-all)
      build_server "basic"
      build_server "weather"
      shift
      ;;
    --build)
      if [ "$2" == "basic" ] || [ "$2" == "weather" ]; then
        build_server "$2"
        shift 2
      else
        echo "Error: Unknown server '$2'"
        usage
      fi
      ;;
    --run)
      if [ "$2" == "basic" ] || [ "$2" == "weather" ]; then
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

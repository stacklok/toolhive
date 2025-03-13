# Running Sample MCP Servers with mcp-lok

This document explains how to run the sample MCP servers using the mcp-lok tool.

## Prerequisites

1. Make sure you have Podman installed and running.
2. For the weather MCP server, you need an OpenWeather API key.

## Building the Sample Servers

To build all sample servers:

```bash
./run-sample-servers.sh --build-all
```

To build a specific server:

```bash
./run-sample-servers.sh --build basic
# or
./run-sample-servers.sh --build weather
```

## Running the Sample Servers

### Basic MCP Server

To run the basic MCP server:

```bash
./run-sample-servers.sh --run basic
```

This server provides:
- A resource that returns information about the server
- An `echo` tool that echoes back the input text
- A `get_timestamp` tool that returns the current timestamp

### Weather MCP Server

To run the weather MCP server, you need to set your OpenWeather API key:

```bash
export OPENWEATHER_API_KEY=your-api-key-here
./run-sample-servers.sh --run weather
```

This server provides:
- Resources for getting current weather data for specific cities
- A `get_forecast` tool for getting weather forecasts
- A `get_current_weather` tool for getting current weather data

## Testing the Servers

After running a server, you can use the mcp-lok list command to see the running servers:

```bash
podman exec -u jaosorior -w /var/home/jaosorior/Development/stacklok/mcp-lok-1 dev cargo run -- list
```

To stop a server:

```bash
podman exec -u jaosorior -w /var/home/jaosorior/Development/stacklok/mcp-lok-1 dev cargo run -- stop basic-mcp-server
# or
podman exec -u jaosorior -w /var/home/jaosorior/Development/stacklok/mcp-lok-1 dev cargo run -- stop weather-mcp-server
```

To remove a server:

```bash
podman exec -u jaosorior -w /var/home/jaosorior/Development/stacklok/mcp-lok-1 dev cargo run -- rm basic-mcp-server
# or
podman exec -u jaosorior -w /var/home/jaosorior/Development/stacklok/mcp-lok-1 dev cargo run -- rm weather-mcp-server
```

## How It Works

The `run-sample-servers.sh` script:

1. Reads the server configuration from the server-config.json file
2. Creates a permission profile based on the server's network access requirements
3. Runs the server using mcp-lok with the appropriate parameters

The script adapts the server configuration to work with the current mcp-lok CLI, which doesn't directly support the `--server-config` option mentioned in the original README.
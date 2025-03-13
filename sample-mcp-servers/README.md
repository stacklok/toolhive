# Sample MCP Servers for mcp-lok

This directory contains sample MCP (Model Context Protocol) servers that can be used to test mcp-lok. These servers implement the Model Context Protocol and can be run securely using mcp-lok.

## Available Servers

### 1. Basic MCP Server

A simple MCP server that implements basic MCP functionality. It provides:

- A resource that returns information about the server
- An `echo` tool that echoes back the input text
- A `get_timestamp` tool that returns the current timestamp

### 2. Weather MCP Server

A more complex MCP server that provides weather data using the OpenWeather API. It provides:

- Resources for getting current weather data for specific cities
- A `get_forecast` tool for getting weather forecasts
- A `get_current_weather` tool for getting current weather data

## Building the Servers

To build the server container images, run the following commands:

```bash
# Build the basic MCP server
cd basic-mcp-server
podman build -t basic-mcp-server:latest .

# Build the weather MCP server
cd ../weather-mcp-server
podman build -t weather-mcp-server:latest .
```

## Running the Servers with mcp-lok

To run the servers with mcp-lok, use the following commands:

```bash
# Run the basic MCP server
mcp-lok run --server-config basic-mcp-server/server-config.json

# Run the weather MCP server
# Note: You need to replace "your-api-key-here" in the server-config.json file
# with your actual OpenWeather API key
mcp-lok run --server-config weather-mcp-server/server-config.json
```

## Testing the Servers

You can test the servers by connecting to them via the mcp-lok HTTP API:

```bash
# Connect to the basic MCP server
curl -N http://localhost:8080/servers/basic-mcp-server/connect

# Connect to the weather MCP server
curl -N http://localhost:8080/servers/weather-mcp-server/connect
```

## Server Configuration

Each server has a `server-config.json` file that configures how mcp-lok runs the server. The configuration includes:

- Server ID and name
- Container image to use
- Command and arguments to run
- Environment variables
- Resource limits
- Network access controls

For example, the weather MCP server needs outbound network access to the OpenWeather API, so its configuration includes:

```json
"network_access": {
  "allow_outbound": true,
  "allowed_domains": ["api.openweathermap.org"]
}
```

## Security Considerations

These sample servers demonstrate different security profiles:

- The basic MCP server has no network access and minimal resource requirements
- The weather MCP server requires outbound network access to the OpenWeather API

When creating your own MCP servers, consider the principle of least privilege and only grant the permissions that are necessary for the server to function.
# Virtual MCP Server (vmcp)

The Virtual MCP Server (vmcp) is a standalone binary that aggregates multiple MCP (Model Context Protocol) servers into a single unified interface. It provides a secure, observable, and controlled way to expose multiple MCP servers through a single endpoint.

## Features

- **Tool Aggregation**: Combine tools from multiple MCP servers into a single interface
- **Resource Aggregation**: Aggregate resources from different sources
- **Prompt Aggregation**: Unified access to prompts from multiple backends
- **Middleware Integration**: Reuses ToolHive's security and middleware infrastructure
  - Authentication (GitHub, local, anonymous)
  - Authorization (Cedar policies)
  - Audit logging
  - Telemetry (OpenTelemetry)
- **Per-Backend Configuration**: Apply different middleware settings to individual backends
- **Namespace Management**: Optional prefixes for tools and resources to avoid naming conflicts

## Installation

### From Source

```bash
# Build the binary
task build-vmcp

# Or install to GOPATH/bin
task install-vmcp
```

### Using Container Image

```bash
# Build the container image
task build-vmcp-image

# Or pull from GitHub Container Registry
docker pull ghcr.io/stacklok/toolhive/vmcp:latest
```

## Usage

### CLI Commands

#### Start the Server

```bash
vmcp serve --config /path/to/vmcp-config.yaml
```

#### Validate Configuration

```bash
vmcp validate --config /path/to/vmcp-config.yaml
```

#### Show Version

```bash
vmcp version
```

### Configuration

vmcp uses a YAML configuration file to define:

1. **Server Settings**: Address, transport type, TLS configuration
2. **Middleware Chain**: Authentication, authorization, audit, telemetry
3. **Backend Servers**: List of MCP servers to aggregate

See [examples/vmcp-config.yaml](../../examples/vmcp-config.yaml) for a complete example.

## Backend Types

vmcp supports three types of MCP server backends:

1. **Container**: ToolHive-managed containerized MCP servers
   ```yaml
   type: "container"
   config:
     image: "mcp/server:latest"
     env:
       VAR: "value"
   ```

2. **Stdio**: Direct stdio communication with MCP servers
   ```yaml
   type: "stdio"
   config:
     command: "node"
     args: ["server.js"]
     working_dir: "/app"
   ```

3. **SSE**: HTTP-based Server-Sent Events connection
   ```yaml
   type: "sse"
   config:
     url: "http://localhost:9000/sse"
     headers:
       Authorization: "Bearer token"
   ```

## Middleware

vmcp reuses ToolHive's middleware components:

### Authentication

Supports multiple authentication providers:
- **GitHub**: OAuth-based authentication with GitHub
- **Local**: File-based user authentication
- **Anonymous**: No authentication (for development)

### Authorization

Uses Cedar policy language for fine-grained access control:
```yaml
authz:
  enabled: true
  policy_file: "/path/to/policies.cedar"
```

### Audit Logging

Logs all MCP operations for compliance and security:
```yaml
audit:
  enabled: true
  log_file: "/var/log/vmcp/audit.log"
  format: "json"
```

### Telemetry

OpenTelemetry integration for metrics and traces:
```yaml
telemetry:
  enabled: true
  otlp_endpoint: "http://localhost:4318"
  metrics: true
  traces: true
```

## Per-Backend Middleware

Override global middleware settings for specific backends:

```yaml
backends:
  - name: "public-api"
    type: "sse"
    config:
      url: "http://api.example.com/sse"
    middleware:
      auth:
        enabled: false  # Public API, no auth needed
      audit:
        enabled: true   # But still audit all requests
```

## Architecture

```
┌─────────────┐
│  MCP Client │
└──────┬──────┘
       │
       ▼
┌─────────────────────────────────┐
│     Virtual MCP Server (vmcp)   │
│  ┌───────────────────────────┐  │
│  │   Middleware Chain        │  │
│  │  - Auth                   │  │
│  │  - Authz                  │  │
│  │  - Audit                  │  │
│  │  - Telemetry              │  │
│  └───────────────────────────┘  │
│  ┌───────────────────────────┐  │
│  │   Router / Aggregator     │  │
│  └───────────────────────────┘  │
└────┬─────────┬─────────┬────────┘
     │         │         │
     ▼         ▼         ▼
┌─────────┐ ┌─────────┐ ┌─────────┐
│ Backend │ │ Backend │ │ Backend │
│ MCP 1   │ │ MCP 2   │ │ MCP 3   │
└─────────┘ └─────────┘ └─────────┘
```

## Development

### Building

```bash
# Build binary
task build-vmcp

# Build container image
task build-vmcp-image

# Build everything
task build-all-images
```

### Testing

```bash
# Run tests
go test ./pkg/vmcp/...

# Run with coverage
go test -cover ./pkg/vmcp/...
```

## Differences from ToolHive (thv)

| Feature | thv | vmcp |
|---------|-----|------|
| Purpose | Run individual MCP servers | Aggregate multiple MCP servers |
| Architecture | Single server per instance | Multiple backends per instance |
| Configuration | RunConfig format | vMCP config format |
| Use Case | Development, testing | Production, multi-server deployments |
| Middleware | Per-server | Global + per-backend overrides |

## Contributing

vmcp is part of the ToolHive project. Please see the main [CONTRIBUTING.md](../../CONTRIBUTING.md) for contribution guidelines.

## License

Apache 2.0 - See [LICENSE](../../LICENSE) for details.

# Virtual MCP Server (vmcp)

The Virtual MCP Server (vmcp) is a standalone binary that aggregates multiple MCP (Model Context Protocol) servers from a ToolHive group into a single unified interface. It acts as an aggregation proxy that consolidates tools, resources, and prompts from all workloads in the group.

**Reference**: See [THV-2106 Virtual MCP Server Proposal](/docs/proposals/THV-2106-virtual-mcp-server.md) for complete design details.

## Features

- **Group-Based Backend Management**: References an existing ToolHive group for automatic workload discovery
- **Tool Aggregation**: Combines tools from multiple MCP servers with conflict resolution (prefix, priority, or manual)
- **Resource & Prompt Aggregation**: Unified access to resources and prompts from all backends
- **Two Authentication Boundaries**:
  - **Incoming Auth** (Client → Virtual MCP): OIDC, local, or anonymous authentication
  - **Outgoing Auth** (Virtual MCP → Backend APIs): RFC 8693 token exchange for backend API access
- **Per-Backend Token Exchange**: Different authentication strategies per backend (pass_through, token_exchange, service_account)
- **Authorization**: Cedar policy-based access control
- **Operational Features**: Circuit breakers, health checks, timeout management, failure handling
- **Future**: Composite tools with elicitation support for multi-step workflows

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

1. **Group Reference**: ToolHive group containing MCP server workloads
2. **Incoming Authentication**: Client → Virtual MCP authentication boundary
3. **Outgoing Authentication**: Virtual MCP → Backend API token exchange
4. **Tool Aggregation**: Conflict resolution and filtering strategies
5. **Operational Settings**: Timeouts, health checks, circuit breakers

See [examples/vmcp-config.yaml](../../examples/vmcp-config.yaml) for a complete example.

## Authentication Model

Virtual MCP implements **two independent authentication boundaries**:

### 1. Incoming Authentication (Client → Virtual MCP)

Validates client requests to Virtual MCP using tokens with `aud=vmcp`:

```yaml
incoming_auth:
  type: oidc
  oidc:
    issuer: "https://keycloak.example.com/realms/myrealm"
    client_id: "vmcp-client"
    audience: "vmcp"  # Token must have aud=vmcp
```

### 2. Outgoing Authentication (Virtual MCP → Backend APIs)

Performs **RFC 8693 token exchange** to obtain backend API-specific tokens. These tokens are NOT for authenticating to backend MCP servers, but for the backend MCP servers to use when calling upstream APIs (GitHub API, Jira API, etc.):

```yaml
outgoing_auth:
  backends:
    github:
      type: token_exchange
      token_exchange:
        audience: "github-api"  # Token for GitHub API
        scopes: ["repo", "read:org"]  # GitHub API scopes
```

**Key Point**: Backend MCP servers receive pre-validated tokens and use them directly to call external APIs. They don't validate tokens themselves—security relies on network isolation and properly scoped API tokens.

## Tool Aggregation & Conflict Resolution

Virtual MCP aggregates tools from all workloads in the group and provides three strategies for handling naming conflicts:

### 1. Prefix Strategy (Default)

Automatically prefixes all tool names with the workload identifier:

```yaml
aggregation:
  conflict_resolution: prefix
  conflict_resolution_config:
    prefix_format: "{workload}_"  # github_create_pr, jira_create_pr
```

### 2. Priority Strategy

First workload in priority order wins; conflicting tools from others are dropped:

```yaml
aggregation:
  conflict_resolution: priority
  conflict_resolution_config:
    priority_order: ["github", "jira", "slack"]
```

### 3. Manual Strategy

Explicitly define overrides for all tools:

```yaml
aggregation:
  conflict_resolution: manual
  tools:
    - workload: "github"
      overrides:
        create_pr:
          name: "gh_create_pr"
          description: "Create a GitHub pull request"
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

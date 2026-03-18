# Virtual MCP Server (vmcp)

The Virtual MCP Server (vmcp) is a standalone binary that aggregates multiple MCP (Model Context Protocol) servers from a ToolHive group into a single unified interface. It acts as an aggregation proxy that consolidates tools, resources, and prompts from all workloads in the group.

**Reference**: See [THV-2106 Virtual MCP Server Proposal](/docs/proposals/THV-2106-virtual-mcp-server.md) for complete design details.

## Features

### Implemented (Phase 1)
- ✅ **Group-Based Backend Management**: Automatic workload discovery from ToolHive groups
- ✅ **Tool Aggregation**: Combines tools from multiple MCP servers with conflict resolution (prefix, priority, manual)
- ✅ **Resource & Prompt Aggregation**: Unified access to resources and prompts from all backends
- ✅ **Request Routing**: Intelligent routing of tool/resource/prompt requests to correct backends
- ✅ **Metadata Preservation**: Forwards `_meta` fields from client requests to backends and preserves `_meta` from backend responses (including `progressToken` for progress notifications)
- ✅ **Session Management**: MCP protocol session tracking with TTL-based cleanup
- ✅ **Health Endpoints**: `/health` and `/ping` for service monitoring
- ✅ **Configuration Validation**: `vmcp validate` command for config verification
- ✅ **Observability**: OpenTelemetry metrics and traces for backend operations and workflow executions

### In Progress
- 🚧 **Incoming Authentication** (Issue #165): OIDC, local, anonymous authentication
- 🚧 **Outgoing Authentication** (Issue #160): RFC 8693 token exchange for backend API access
- 🚧 **Token Caching**: Memory and Redis cache providers
- 🚧 **Health Monitoring** (Issue #166): Circuit breakers, backend health checks

### Future (Phase 2+)
- 📋 **Authorization**: Cedar policy-based access control
- 📋 **Composite Tools**: Multi-step workflows with elicitation support
- 📋 **Advanced Routing**: Load balancing, failover strategies

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

## Quick Start

```bash
# 1. Create a ToolHive group
thv group create my-team

# 2. Run some MCP servers in the group
thv run github --name github-mcp --group my-team
thv run fetch --name fetch-mcp --group my-team

# 3. Create a vmcp configuration file (see examples/vmcp-config.yaml)
cat > vmcp-config.yaml <<EOF
name: "my-vmcp"
groupRef: "my-team"
incomingAuth:
  type: anonymous
outgoingAuth:
  source: inline
  default:
    type: unauthenticated
aggregation:
  conflictResolution: prefix
  conflictResolutionConfig:
    prefixFormat: "{workload}_"
EOF

# 4. Validate the configuration
vmcp validate --config vmcp-config.yaml

# 5. Start the Virtual MCP Server
vmcp serve --config vmcp-config.yaml

# 6. Test the health endpoint
curl http://127.0.0.1:4483/health
# {"status":"ok"}

# 7. Connect your MCP client to http://127.0.0.1:4483/mcp
# The client will see aggregated tools from all backends:
#   - github-mcp_create_issue, github-mcp_list_repos, ...
#   - fetch-mcp_fetch, ...
```

## Usage

### CLI Commands

#### Start the Server

```bash
# Basic usage
vmcp serve --config /path/to/vmcp-config.yaml

# With audit logging enabled (uses default configuration)
vmcp serve --config /path/to/vmcp-config.yaml --enable-audit

# Customize host and port
vmcp serve --config /path/to/vmcp-config.yaml --host 0.0.0.0 --port 8080
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
6. **Telemetry**: OpenTelemetry metrics/tracing and Prometheus endpoint
7. **Audit Logging**: MCP operation audit logs (optional, can be enabled via `--enable-audit` flag for quick setup)

See [examples/vmcp-config.yaml](../../examples/vmcp-config.yaml) for a complete example.

## Authentication Model

Virtual MCP implements **two independent authentication boundaries**:

### 1. Incoming Authentication (Client → Virtual MCP)

Validates client requests to Virtual MCP using tokens with `aud=vmcp`:

```yaml
incomingAuth:
  type: oidc
  oidc:
    issuer: "https://keycloak.example.com/realms/myrealm"
    clientId: "vmcp-client"
    audience: "vmcp"  # Token must have aud=vmcp
```

### 2. Outgoing Authentication (Virtual MCP → Backend APIs)

Performs **RFC 8693 token exchange** to obtain backend API-specific tokens. These tokens are NOT for authenticating to backend MCP servers, but for the backend MCP servers to use when calling upstream APIs (GitHub API, Jira API, etc.):

```yaml
outgoingAuth:
  backends:
    github:
      type: token_exchange
      tokenExchange:
        audience: "github-api"  # Token for GitHub API
        scopes: ["repo", "read:org"]  # GitHub API scopes
```

**Key Point**: Backend MCP servers receive pre-validated tokens and use them directly to call external APIs. They don't validate tokens themselves—security relies on network isolation and properly scoped API tokens.

## Session Security

### Token Binding (Session Management V2)

When Session Management V2 is enabled, vmcp implements **token binding** to prevent session hijacking attacks. Each session is cryptographically bound to the authentication token used to create it.

**Security Features:**

- **HMAC-SHA256 Hashing**: Token hashes use HMAC with a server-managed secret
- **Per-Session Salt**: Each session has a unique random salt
- **Constant-Time Comparison**: Prevents timing attacks
- **Request-Level Validation**: Each request independently validates the caller token; failed validation terminates the session immediately to prevent session hijacking attacks

**Configuration:**

Set the HMAC secret via environment variable (required for production):

```bash
export VMCP_SESSION_HMAC_SECRET="your-32-plus-byte-secret-here"
vmcp serve --config vmcp-config.yaml
```

**Security Best Practices:**

- ✅ Generate a secure random secret (32+ bytes recommended)
- ✅ Store the secret in a secure configuration system (HashiCorp Vault, AWS Secrets Manager, etc.)
- ✅ Rotate the secret periodically (requires session recreation)
- ❌ Never commit secrets to version control
- ❌ Never use the default secret in production

**Generating a Secure Secret:**

```bash
# Generate a 32-byte secret using OpenSSL
openssl rand -base64 32

# Or using head and base64
head -c 32 /dev/urandom | base64
```

**Example Kubernetes Deployment:**

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: vmcp-secrets
type: Opaque
stringData:
  hmac-secret: "<your-generated-secret>"
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: vmcp
spec:
  template:
    spec:
      containers:
      - name: vmcp
        image: ghcr.io/stacklok/toolhive/vmcp:latest
        env:
        - name: VMCP_SESSION_HMAC_SECRET
          valueFrom:
            secretKeyRef:
              name: vmcp-secrets
              key: hmac-secret
```

**Note**: When **Session Management V2 is enabled**, Kubernetes deployments **require** `VMCP_SESSION_HMAC_SECRET` to be set (the server will fail to start without it). For non-Kubernetes environments (local development/testing), a default insecure secret is used as a fallback, but this is **NOT recommended for production**. If Session Management V2 is disabled, this environment variable is not required.

### Automatic Secret Management (ToolHive Operator)

When deploying vMCP via the **ToolHive operator** with Session Management V2 enabled, the HMAC secret is **automatically generated and managed** for you:

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: VirtualMCPServer
metadata:
  name: my-vmcp
spec:
  config:
    operational:
      sessionManagementV2: true  # Enables automatic HMAC secret creation
    group: my-group
```

The operator will:

- ✅ Automatically generate a cryptographically secure 32-byte HMAC secret
- ✅ Store it in a Kubernetes Secret named `{vmcp-name}-hmac-secret`
- ✅ Inject it into the vMCP deployment as `VMCP_SESSION_HMAC_SECRET`
- ✅ Validate existing secrets (ownership, structure, and content)
- ✅ Automatically delete the secret when the VirtualMCPServer is removed

**No manual secret generation or management required!** The operator handles all of this automatically when you enable Session Management V2.

> **Note**: The secret is generated once at creation time and persists for the lifetime of the VirtualMCPServer. Secret rotation is not currently supported but may be added in a future release.

## Tool Aggregation & Conflict Resolution

Virtual MCP aggregates tools from all workloads in the group and provides three strategies for handling naming conflicts:

### 1. Prefix Strategy (Default)

Automatically prefixes all tool names with the workload identifier:

```yaml
aggregation:
  conflictResolution: prefix
  conflictResolutionConfig:
    prefixFormat: "{workload}_"  # github_create_pr, jira_create_pr
```

### 2. Priority Strategy

First workload in priority order wins; conflicting tools from others are dropped:

```yaml
aggregation:
  conflictResolution: priority
  conflictResolutionConfig:
    priorityOrder: ["github", "jira", "slack"]
```

### 3. Manual Strategy

Explicitly define overrides for all tools:

```yaml
aggregation:
  conflictResolution: manual
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

## Known Limitations

### Audio Content Not Supported

Audio content type from MCP responses is not currently supported and will be silently ignored in template variable substitution.

**Impact**: Minimal - audio content in MCP tools is rare. Audio data in tool responses will not be available for composite tool workflows.

**Code Reference**: `pkg/vmcp/conversion/content.go` (ContentArrayToMap function)

**Future Enhancement**: Add support for audio content with dedicated `audio_N` key prefix.

## Contributing

vmcp is part of the ToolHive project. Please see the main [CONTRIBUTING.md](../../CONTRIBUTING.md) for contribution guidelines.

## License

Apache 2.0 - See [LICENSE](../../LICENSE) for details.

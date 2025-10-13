# Core Concepts

This document defines the key concepts, terminology, and abstractions used throughout ToolHive. Understanding these concepts is essential for working with the platform.

## Platform Philosophy

ToolHive is not just a container runner - it's a **platform** that provides:
- Proxy infrastructure with middleware
- Security and isolation
- Configuration management
- Registry and distribution
- Aggregation and composition

## Nouns (Things)

### Workload

A **workload** is the fundamental deployment unit in ToolHive. It represents everything needed to run an MCP server:

**Components:**
- Primary MCP server (container or remote endpoint)
- Proxy process (for non-stdio transports or detached mode)
- Network configuration and port mappings
- Permission profile and security policies
- Middleware configuration
- State and metadata

**Types:**
1. **Container Workload**: MCP server running in a container
2. **Remote Workload**: MCP server running on a remote host

**Lifecycle States:**
- `starting` - Workload is being created
- `running` - Workload is active and serving requests
- `stopping` - Workload is being stopped
- `stopped` - Workload is stopped but can be restarted
- `removing` - Workload is being deleted
- `error` - Workload encountered an error
- `unhealthy` - Workload is running but unhealthy

**Implementation:**
- Interface: `pkg/workloads/manager.go:38`
- Status: `pkg/container/runtime/types.go:17`
- Core type: `pkg/core/workload.go`

**Related concepts:** Transport, Permission Profile, RunConfig

### Transport

A **transport** defines how MCP clients communicate with MCP servers. It encapsulates the protocol and proxy implementation.

**Three types:**

1. **stdio**: Standard input/output communication
   - Container speaks stdin/stdout
   - Proxy translates HTTP ↔ stdio
   - Two proxy modes: SSE or Streamable HTTP

2. **sse**: Server-Sent Events over HTTP
   - Container speaks HTTP with SSE
   - Transparent HTTP proxy
   - Server-initiated messages supported

3. **streamable-http**: Bidirectional HTTP streaming
   - Container speaks HTTP with `/mcp` endpoint
   - Transparent HTTP proxy (same as SSE)
   - Session management via headers

**Implementation:**
- Interface: `pkg/transport/types/transport.go:81`
- Types: `pkg/transport/types/transport.go:109`
- Factory: `pkg/transport/factory.go:19`

**Related concepts:** Proxy, Middleware, Session

### Proxy

A **proxy** is the component that sits between MCP clients and MCP servers, forwarding traffic while applying middleware.

**Two proxy types:**

1. **Transparent Proxy**: Used by SSE and Streamable HTTP transports
   - Location: `pkg/transport/proxy/transparent/transparent_proxy.go`
   - Uses `httputil.ReverseProxy`
   - No protocol-specific logic
   - Forwards HTTP directly

2. **Protocol-Specific Proxy**: Used by stdio transport
   - SSE mode: `pkg/transport/proxy/httpsse/http_proxy.go`
   - Streamable mode: `pkg/transport/proxy/streamable/streamable_proxy.go`
   - Parses JSON-RPC messages
   - Implements MCP transport protocol

**Proxy responsibilities:**
- Apply middleware chain
- Handle sessions
- Forward requests/responses
- Health checking (for containers)
- Metrics and auth info endpoints

**Implementation:**
- Interface: `pkg/transport/types/transport.go:149`

**Related concepts:** Transport, Middleware, Session

### Middleware

**Middleware** is a composable layer in the request processing chain. Each middleware can inspect, modify, or reject requests.

**Eight middleware types:**

1. **Authentication** (`auth`) - JWT token validation
2. **Token Exchange** (`tokenexchange`) - OAuth token exchange
3. **MCP Parser** (`mcp-parser`) - JSON-RPC parsing
4. **Tool Filter** (`tool-filter`) - Filter available tools
5. **Tool Call Filter** (`tool-call-filter`) - Filter tool calls
6. **Telemetry** (`telemetry`) - OpenTelemetry instrumentation
7. **Authorization** (`authz`) - Cedar policy evaluation
8. **Audit** (`audit`) - Request logging

**Execution order:**
Middleware applied in reverse order: Auth → Parser → Authz → Audit

**Implementation:**
- Interface: `pkg/transport/types/transport.go:23`
- Factory: `pkg/runner/middleware.go:15`
- Documentation: `docs/middleware.md`

**Related concepts:** Proxy, Authentication, Authorization

### RunConfig

**RunConfig** is ToolHive's standard configuration format for running MCP servers. It's a JSON/YAML structure that contains everything needed to deploy a workload.

**Key fields:**
- `image` or `remoteURL` - What to run
- `transport` - How to communicate
- `port`, `targetPort` - Network configuration
- `permissionProfile` - Security settings
- `middlewareConfigs` - Middleware chain
- `envVars` - Environment variables
- `volumes` - File mounts

**Schema version:** `v0.1.0` (current)

**Portability:**
- Export: `thv export <workload>` → JSON file
- Import: `thv run --from-config <file>`
- API contract: Format is versioned and stable

**Implementation:**
- Definition: `pkg/runner/config.go:34`
- Schema version: `pkg/runner/config.go:30`

**Related concepts:** Workload, Permission Profile, Middleware

### Permission Profile

A **permission profile** defines security boundaries for MCP servers:

**Three permission types:**

1. **File System Access**:
   - `read` - Mount paths as read-only
   - `write` - Mount paths as read-write
   - Mount declaration formats: `path`, `host:container`, `uri://resource:container`

2. **Network Access**:
   - `outbound.insecureAllowAll` - Allow all outbound connections
   - `outbound.allowHost` - Whitelist specific hosts
   - `outbound.allowPort` - Whitelist specific ports
   - `inbound.allowHost` - Whitelist inbound connections

3. **Privileged Mode**:
   - `privileged` - Run with host device access (dangerous!)

**Built-in profiles:**
- `none` - No permissions (default)
- `network` - Full network access

**Implementation:**
- Definition: `pkg/permissions/profile.go:22`
- Network: `pkg/permissions/profile.go:48`
- Mount declarations: `pkg/permissions/profile.go:152`

**Related concepts:** RunConfig, Workload, Security

### Group

A **group** is a logical collection of MCP servers that share a common purpose or use case.

**Use cases:**
- Organizational structure (e.g., "data-analysis" group)
- Virtual MCP servers (aggregate multiple MCPs into one)
- Access control (apply policies at group level)
- Client configuration (configure clients to use groups)

**Operations:**
- Create group by adding workloads with `--group` flag
- List workloads in group: `thv group list <name>`
- Move workloads: `thv group move`
- Delete group: `thv group delete <name>`

**Implementation:**
- Group management: `pkg/groups/`
- Workload group field: `pkg/runner/config.go:148`

**Related concepts:** Virtual MCP, Workload, Client

### Registry

A **registry** is a catalog of MCP server definitions with metadata, configuration, and provenance information.

**Registry types:**

1. **Built-in Registry**: Curated by Stacklok
   - Source: https://github.com/stacklok/toolhive-registry
   - Updated regularly via `thv registry update`
   - Trusted and verified servers

2. **Custom Registry**: User-provided
   - JSON file: `thv registry add <path>`
   - Remote URL: `thv registry add <url>`
   - Organization-specific servers

3. **Registry API**: Self-hosted registry server
   - Binary: `thv-registry-api`
   - Storage: File or Kubernetes ConfigMap
   - API endpoints for management

**Registry entry types:**
- `servers` - Container-based MCP servers
- `remoteServers` - Remote MCP servers (HTTPS endpoints)
- `groups` - Predefined groups of servers

**Implementation:**
- Registry types: `pkg/registry/types.go`
- Registry manager: `pkg/registry/registry.go`
- API server: `cmd/thv-registry-api/`

**Related concepts:** Image Metadata, Remote Server Metadata

### Session

A **session** tracks state for MCP client connections, particularly for transports that require session management.

**Session types:**

1. **SSE Session**: For stdio transport with SSE proxy mode
   - Tracks connected SSE clients
   - Message queue per client
   - Endpoint URL generation

2. **Streamable Session**: For stdio transport with streamable proxy mode
   - Tracks `Mcp-Session-Id` header
   - Request/response correlation
   - Ephemeral sessions for sessionless requests

3. **Proxy Session**: For transparent proxy (SSE/Streamable transports)
   - Session ID detection from headers
   - Session ID detection from SSE body
   - Minimal state tracking

**Session lifecycle:**
- Created on first request or explicit initialize
- Tracked via session manager with TTL
- Cleaned up after inactivity or explicit deletion

**Implementation:**
- Manager: `pkg/transport/session/manager.go`
- SSE: `pkg/transport/session/sse_session.go`
- Streamable: `pkg/transport/session/streamable_session.go`
- Proxy: `pkg/transport/session/proxy_session.go`

**Related concepts:** Transport, Proxy

### Runtime

A **runtime** is an abstraction over container orchestration systems. It provides a unified interface for container operations.

**Runtime types:**

1. **Docker Runtime**: Docker Engine API
2. **Podman Runtime**: Podman socket API
3. **Colima Runtime**: Docker-compatible (uses Docker runtime)
4. **Kubernetes Runtime**: Kubernetes API (StatefulSets)

**Runtime interface:**
- `DeployWorkload` - Create and start workload
- `StopWorkload` - Stop workload
- `RemoveWorkload` - Delete workload
- `ListWorkloads` - List all workloads
- `GetWorkloadInfo` - Get workload details
- `GetWorkloadLogs` - Retrieve logs
- `AttachToWorkload` - Attach to stdin/stdout (stdio only)
- `IsWorkloadRunning` - Check if running

**Runtime detection:**
Order: Podman → Colima → Docker → Kubernetes (via env)

**Implementation:**
- Interface: `pkg/container/runtime/types.go:125`
- Factory: `pkg/container/factory.go`
- Detection: `pkg/container/runtime/types.go:291`

**Related concepts:** Deployer, Workload, Container

### Client

An **MCP client** is an application that uses MCP servers (e.g., Claude Desktop, IDEs, AI tools).

**Client types:**
- `claude-code` - Claude Code
- `cursor` - Cursor editor
- `vscode` - VS Code
- `cline` - Cline extension
- `windsurf` - Windsurf editor
- Many more...

**Client configuration:**
ToolHive can automatically configure clients to use MCP servers:
- Reads client config files
- Adds server URLs
- Updates on workload start/stop
- Supports multiple config formats

**Client operations:**
- `thv client list` - Show available clients
- `thv client setup <client>` - Configure client
- `thv client status` - Show client status

**Implementation:**
- Client types: `pkg/client/types.go`
- Manager: `pkg/client/manager.go`

**Related concepts:** Workload, Group

## Verbs (Actions)

### Deploy

**Deploy** creates and starts a workload with all its components.

**For containers:**
1. Create container with image
2. Configure networking and ports
3. Apply permission profile
4. Start container
5. Attach streams (if stdio)
6. Start proxy
7. Apply middleware
8. Update state

**For remote servers:**
1. Validate remote URL
2. Start proxy
3. Configure authentication (if needed)
4. Apply middleware
4. Update state

**Commands:**
- `thv run <image|url>` - Deploy and start
- `thv run --from-config <file>` - Deploy from config

**Implementation:**
- CLI: `cmd/thv/app/run.go`
- Workloads: `pkg/workloads/manager.go:316`
- Runtime: `pkg/container/runtime/types.go:98`

**Related concepts:** Workload, Runtime, Transport

### Proxy

**Proxy** forwards MCP traffic between clients and servers while applying middleware.

**Proxy types:**
- **Transparent**: Forwards HTTP without parsing
- **Protocol-specific**: Parses and translates messages

**Proxy operations:**
1. Start HTTP server on proxy port
2. Apply middleware chain to requests
3. Forward to destination (container or remote)
4. Return responses to clients
5. Track sessions
6. Expose metrics and health endpoints

**Implementation:**
- Transparent: `pkg/transport/proxy/transparent/transparent_proxy.go`
- SSE: `pkg/transport/proxy/httpsse/http_proxy.go`
- Streamable: `pkg/transport/proxy/streamable/streamable_proxy.go`

**Related concepts:** Transport, Middleware, Session

### Attach

**Attach** connects to a container's stdin/stdout streams for stdio transport.

**Attach process:**
1. Container must be running
2. Request attach from runtime
3. Receive stdin (WriteCloser) and stdout (ReadCloser)
4. Start message processing goroutines
5. Read JSON-RPC from stdout
6. Write JSON-RPC to stdin

**Framing:**
- Newline-delimited JSON-RPC messages
- Each message ends with `\n`

**Implementation:**
- Transport: `pkg/transport/stdio.go:162`
- Runtime interface: `pkg/container/runtime/types.go:115`

**Related concepts:** Stdio Transport, Runtime

### Parse

**Parse** extracts structured information from JSON-RPC MCP messages for middleware processing.

**Parsing includes:**
- Message type (request, response, notification)
- Method name (e.g., `tools/call`, `resources/read`)
- Request ID
- Parameters
- Resource ID (for resource operations)
- Arguments (for tool calls)

**Parsed data stored in context:**
- Available to downstream middleware
- Used by authorization for policy evaluation
- Used by audit for event logging

**Implementation:**
- Parser middleware: `pkg/mcp/parser.go`
- Parsed type: `pkg/mcp/types.go`

**Related concepts:** Middleware, Authorization, Audit

### Filter

**Filter** controls which tools or operations are available to MCP clients.

**Two filter types:**

1. **Tool Filter**: Controls `tools/list` responses
   - Whitelist specific tools
   - Override tool names/descriptions
   - Applied to list responses

2. **Tool Call Filter**: Controls `tools/call` requests
   - Validate tool calls against whitelist
   - Apply name overrides to calls
   - Reject unauthorized calls

**Configuration:**
- `toolsFilter` - List of allowed tools
- `toolsOverride` - Map of name/description overrides

**Implementation:**
- Filter middleware: `pkg/mcp/middleware.go`
- Override type: `pkg/runner/config.go:464`

**Related concepts:** Middleware, Authorization

### Authorize

**Authorize** evaluates Cedar policies to determine if requests are permitted.

**Authorization process:**
1. Get parsed MCP data from context
2. Get JWT claims from auth middleware
3. Create Cedar entities (Principal, Action, Resource)
4. Evaluate Cedar policies
5. Allow or deny request

**Policy language:**
Cedar policies use:
- `principal` - Who is making the request (from JWT)
- `action` - What operation (from MCP method)
- `resource` - What is being accessed (from MCP resource ID)

**Example policy:**
```cedar
permit(
  principal == User::"user@example.com",
  action == Action::"tools/call",
  resource == Tool::"web-search"
);
```

**Implementation:**
- Authz middleware: `pkg/authz/middleware.go`
- Policy engine: Cedar (external library)

**Related concepts:** Middleware, Authentication, Parse

### Audit

**Audit** logs MCP operations for compliance, monitoring, and debugging.

**Audit events:**
- `mcp_tool_call` - Tool executions
- `mcp_resource_read` - Resource access
- `mcp_prompt_get` - Prompt retrieval
- `mcp_list_operation` - List operations
- `http_request` - Generic HTTP requests

**Event data:**
- Timestamp, source, outcome
- Subjects (user, session)
- Target (endpoint, method, resource)
- Request/response data (configurable)
- Duration and metadata

**Implementation:**
- Audit middleware: `pkg/audit/middleware.go`
- Event types: `pkg/audit/events.go`
- Config: `pkg/audit/config.go`

**Related concepts:** Middleware, Authorization, Parse

### Export

**Export** serializes a workload's RunConfig to a portable JSON file.

**Export process:**
1. Load workload state from disk
2. Read RunConfig
3. Serialize to JSON with formatting
4. Write to file or stdout

**Exported format:**
- JSON with schema version
- All configuration fields
- Permission profile included
- Middleware configuration included

**Commands:**
- `thv export <workload>` - Export to stdout
- `thv export <workload> > config.json` - Export to file

**Implementation:**
- CLI: `cmd/thv/app/export.go`
- Serialization: `pkg/runner/config.go:164`

**Related concepts:** RunConfig, Import, State

### Import

**Import** creates a workload from an exported RunConfig file.

**Import process:**
1. Read JSON file
2. Deserialize to RunConfig
3. Validate schema version
4. Deploy workload with configuration

**Commands:**
- `thv run --from-config <file>` - Import and run

**Implementation:**
- CLI: `cmd/thv/app/run.go`
- Deserialization: `pkg/runner/config.go:175`

**Related concepts:** RunConfig, Export, Deploy

### Monitor

**Monitor** watches container health and lifecycle events.

**Monitoring includes:**
- Container exit detection
- Health checks (via MCP ping)
- Automatic proxy shutdown on container exit

**Health checking:**
- Send MCP `ping` request periodically
- Check for valid response
- Shutdown if unhealthy

**Implementation:**
- Monitor: `pkg/container/monitor.go`
- Health checker: `pkg/healthcheck/checker.go`

**Related concepts:** Workload, Transport, Proxy

## Relationships

### Workload Composition

```mermaid
graph TB
    Workload[Workload]
    Workload --> RunConfig[RunConfig]
    Workload --> Runtime[Runtime]
    Workload --> Transport[Transport]
    Workload --> State[State]

    RunConfig --> Profile[Permission Profile]
    RunConfig --> Middleware[Middleware Configs]
    RunConfig --> EnvVars[Environment Variables]

    Transport --> Proxy[Proxy]
    Proxy --> Sessions[Sessions]

    style Workload fill:#90caf9
    style RunConfig fill:#e3f2fd
    style Transport fill:#81c784
```

### Request Flow

```mermaid
graph LR
    Client[Client] --> Proxy[Proxy]
    Proxy --> MW1[Auth]
    MW1 --> MW2[Parser]
    MW2 --> MW3[Authz]
    MW3 --> MW4[Audit]
    MW4 --> Container[MCP Server]

    style Proxy fill:#81c784
    style Container fill:#ffb74d
```

### Data Hierarchy

```
Registry
├── Servers (Container-based)
│   └── ImageMetadata
│       ├── image
│       ├── transport
│       ├── envVars
│       └── permissionProfile
├── RemoteServers (Remote)
│   └── RemoteServerMetadata
│       ├── url
│       ├── transport
│       ├── headers
│       └── oauthConfig
└── Groups
    ├── servers (map)
    └── remoteServers (map)
```

## Terminology Quick Reference

| Term | One-line Definition |
|------|---------------------|
| **Workload** | A deployed MCP server with all its components |
| **Transport** | Protocol for MCP client-server communication |
| **Proxy** | Component that forwards traffic + applies middleware |
| **Middleware** | Composable request processing layer |
| **RunConfig** | Portable JSON configuration for workloads |
| **Permission Profile** | Security policy (filesystem, network, privileges) |
| **Group** | Logical collection of related MCP servers |
| **Registry** | Catalog of MCP server definitions |
| **Session** | State tracking for MCP connections |
| **Runtime** | Abstraction over container systems |
| **Client** | Application that uses MCP servers |
| **Deploy** | Create and start a workload |
| **Proxy** (verb) | Forward traffic with middleware |
| **Attach** | Connect to container stdin/stdout |
| **Parse** | Extract structured info from JSON-RPC |
| **Filter** | Control available tools/operations |
| **Authorize** | Evaluate Cedar policies |
| **Audit** | Log operations for compliance |
| **Export** | Serialize RunConfig to JSON |
| **Import** | Create workload from JSON |
| **Monitor** | Watch container health |

## Related Documentation

- [Architecture Overview](00-overview.md) - Platform overview
- [Deployment Modes](01-deployment-modes.md) - How concepts work in each mode
- [Transport Architecture](03-transport-architecture.md) - Transport and proxy details
- [RunConfig and Permissions](05-runconfig-and-permissions.md) - Configuration schema
- [Middleware](../middleware.md) - Middleware system

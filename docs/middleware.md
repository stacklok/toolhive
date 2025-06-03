# Middleware Architecture

This document describes the middleware architecture used in ToolHive for processing MCP (Model Context Protocol) requests. The middleware chain provides authentication, parsing, authorization, and auditing capabilities in a modular and extensible way.

## Overview

ToolHive uses a layered middleware architecture to process incoming MCP requests. Each middleware component has a specific responsibility and operates in a well-defined order to ensure proper request handling, security, and observability.

The middleware chain consists of the following components:

1. **Authentication Middleware**: Validates JWT tokens and extracts client identity
2. **MCP Parsing Middleware**: Parses JSON-RPC MCP requests and extracts structured data
3. **Authorization Middleware**: Evaluates Cedar policies to authorize requests
4. **Audit Middleware**: Logs request events for compliance and monitoring

## Architecture Diagram

```mermaid
graph TD
    A[Incoming MCP Request] --> B[Authentication Middleware]
    B --> C[MCP Parsing Middleware]
    C --> D[Authorization Middleware]
    D --> E[Audit Middleware]
    E --> F[MCP Server Handler]
    
    B --> B1[JWT Validation]
    B1 --> B2[Extract Claims]
    B2 --> B3[Add to Context]
    
    C --> C1[JSON-RPC Parsing]
    C1 --> C2[Extract Method & Params]
    C2 --> C3[Extract Resource ID & Args]
    C3 --> C4[Store Parsed Data]
    
    D --> D1[Get Parsed MCP Data]
    D1 --> D2[Create Cedar Entities]
    D2 --> D3[Evaluate Policies]
    D3 --> D4{Authorized?}
    D4 -->|Yes| D5[Continue]
    D4 -->|No| D6[403 Forbidden]
    
    E --> E1[Determine Event Type]
    E1 --> E2[Extract Audit Data]
    E2 --> E3[Log Event]
    
    style A fill:#e1f5fe
    style F fill:#e8f5e8
    style D6 fill:#ffebee
```

## Middleware Flow

```mermaid
sequenceDiagram
    participant Client
    participant Auth as Authentication
    participant Parser as MCP Parser
    participant Authz as Authorization
    participant Audit as Audit
    participant Server as MCP Server
    
    Client->>Auth: HTTP Request with JWT
    Auth->>Auth: Validate JWT Token
    Auth->>Auth: Extract Claims
    Note over Auth: Add claims to context
    
    Auth->>Parser: Request + JWT Claims
    Parser->>Parser: Parse JSON-RPC
    Parser->>Parser: Extract MCP Method
    Parser->>Parser: Extract Resource ID & Arguments
    Note over Parser: Add parsed data to context
    
    Parser->>Authz: Request + Parsed MCP Data
    Authz->>Authz: Get Parsed Data from Context
    Authz->>Authz: Create Cedar Entities
    Authz->>Authz: Evaluate Policies
    
    alt Authorized
        Authz->>Audit: Authorized Request
        Audit->>Audit: Extract Event Data
        Audit->>Audit: Log Audit Event
        Audit->>Server: Process Request
        Server->>Client: Response
    else Unauthorized
        Authz->>Client: 403 Forbidden
    end
```

## Middleware Components

### 1. Authentication Middleware

**Purpose**: Validates JWT tokens and extracts client identity information.

**Location**: `pkg/auth/middleware.go`

**Responsibilities**:
- Validate JWT token signature and expiration
- Extract JWT claims (sub, name, roles, etc.)
- Add claims to request context for downstream middleware

**Context Data Added**:
- JWT claims with `claim_` prefix (e.g., `claim_sub`, `claim_name`)

### 2. MCP Parsing Middleware

**Purpose**: Parses JSON-RPC MCP requests and extracts structured information.

**Location**: `pkg/mcp/parser.go`

**Responsibilities**:
- Parse JSON-RPC 2.0 messages
- Extract MCP method names (e.g., `tools/call`, `resources/read`)
- Extract resource IDs and arguments based on method type
- Store parsed data in request context

**Context Data Added**:
- `ParsedMCPRequest` containing:
  - Method name
  - Request ID
  - Raw parameters
  - Extracted resource ID
  - Extracted arguments

**Supported MCP Methods**:
- `initialize` - Client initialization
- `tools/call`, `tools/list` - Tool operations
- `prompts/get`, `prompts/list` - Prompt operations
- `resources/read`, `resources/list` - Resource operations
- `notifications/*` - Notification messages
- `ping`, `logging/setLevel` - System operations

### 3. Authorization Middleware

**Purpose**: Evaluates Cedar policies to determine if requests are authorized.

**Location**: `pkg/authz/middleware.go`

**Responsibilities**:
- Retrieve parsed MCP data from context
- Create Cedar entities (Principal, Action, Resource)
- Evaluate Cedar policies against the request
- Allow or deny the request based on policy evaluation
- Filter list responses based on user permissions

**Dependencies**:
- Requires JWT claims from Authentication middleware
- Requires parsed MCP data from MCP Parsing middleware

### 4. Audit Middleware

**Purpose**: Logs request events for compliance, monitoring, and debugging.

**Location**: `pkg/audit/auditor.go`

**Responsibilities**:
- Determine event type based on request characteristics
- Extract audit-relevant data from request and response
- Log structured audit events
- Track request duration and outcome

**Event Types**:
- `mcp_tool_call` - Tool execution events
- `mcp_resource_read` - Resource access events
- `mcp_prompt_get` - Prompt retrieval events
- `mcp_list_operation` - List operation events
- `http_request` - General HTTP request events

## Data Flow Through Context

The middleware chain uses Go's `context.Context` to pass data between components:

```mermaid
graph LR
    A[Request Context] --> B[+ JWT Claims]
    B --> C[+ Parsed MCP Data]
    C --> D[+ Authorization Result]
    D --> E[+ Audit Metadata]
    
    subgraph "Authentication"
        B
    end
    
    subgraph "MCP Parser"
        C
    end
    
    subgraph "Authorization"
        D
    end
    
    subgraph "Audit"
        E
    end
```

## Configuration

### Enabling Middleware

The middleware chain is automatically configured when starting an MCP server with ToolHive:

```bash
# Basic MCP server (Authentication + Parsing + Audit)
thv run --transport sse --name my-server my-image:latest

# With authorization enabled
thv run --transport sse --name my-server --authz-config authz.yaml my-image:latest

# With custom audit configuration
thv run --transport sse --name my-server --audit-config audit.yaml my-image:latest
```

### Middleware Order

The middleware order is critical and enforced by the system:

1. **Authentication** - Must be first to establish client identity
2. **MCP Parsing** - Must come after authentication to access JWT context
3. **Authorization** - Must come after parsing to access structured MCP data
4. **Audit** - Must be last to capture the complete request lifecycle

## Error Handling

Each middleware component handles errors gracefully:

```mermaid
graph TD
    A[Request] --> B{Auth Valid?}
    B -->|No| C[401 Unauthorized]
    B -->|Yes| D{MCP Parseable?}
    D -->|No| E[Continue without parsing]
    D -->|Yes| F{Authorized?}
    F -->|No| G[403 Forbidden]
    F -->|Yes| H[Process Request]
    
    style C fill:#ffebee
    style G fill:#ffebee
    style H fill:#e8f5e8
```

**Error Responses**:
- `401 Unauthorized` - Invalid or missing JWT token
- `403 Forbidden` - Valid token but insufficient permissions
- `400 Bad Request` - Malformed MCP request (when parsing is required)

## Performance Considerations

### Parsing Optimization

The MCP parsing middleware uses efficient strategies:

- **Map-based method handlers** instead of large switch statements
- **Single-pass parsing** of JSON-RPC messages
- **Lazy evaluation** - only parses MCP-specific endpoints
- **Context reuse** - parsed data shared across middleware

### Authorization Caching

The authorization middleware optimizes policy evaluation:

- **Policy compilation** happens once at startup
- **Entity creation** is optimized for common patterns
- **Result caching** for repeated identical requests (when enabled)

## Monitoring and Observability

### Audit Events

All middleware components contribute to audit events:

```json
{
  "type": "mcp_tool_call",
  "loggedAt": "2025-06-03T13:02:28Z",
  "source": {"type": "network", "value": "192.0.2.1"},
  "outcome": "success",
  "subjects": {"user": "user123"},
  "component": "toolhive-api",
  "target": {
    "endpoint": "/messages",
    "method": "POST",
    "type": "tool",
    "resource_id": "weather"
  },
  "data": {
    "request": {"location": "New York"},
    "response": {"temperature": "22°C"}
  },
  "metadata": {
    "auditId": "uuid",
    "duration_ms": 150,
    "transport": "http"
  }
}
```

### Metrics

Key metrics tracked by the middleware:

- **Request duration** - Time spent in each middleware component
- **Authorization decisions** - Permit/deny rates and reasons
- **Parsing success rates** - MCP message parsing statistics
- **Error rates** - Authentication and authorization failures

## Extending the Middleware

### Adding New Middleware

To add new middleware to the chain:

1. Implement the `func(http.Handler) http.Handler` interface
2. Add configuration options to the runner
3. Insert at the appropriate position in the chain
4. Update tests to include the new middleware

### Custom Authorization Policies

See the [Authorization Framework](authz.md) documentation for details on writing Cedar policies.

### Custom Audit Events

The audit middleware can be extended to capture additional event types and data fields based on your requirements.

## Troubleshooting

### Common Issues

**Middleware Order Problems**:
- Ensure authentication runs before authorization
- Ensure MCP parsing runs before authorization
- Check that all required middleware is included in tests

**Context Data Missing**:
- Verify middleware order is correct
- Check that upstream middleware completed successfully
- Ensure context keys are correctly defined and used

**Performance Issues**:
- Monitor middleware execution time
- Check for inefficient policy evaluation
- Consider enabling authorization result caching

### Debug Information

Enable debug logging to see middleware execution:

```bash
export LOG_LEVEL=debug
thv run --transport sse --name my-server my-image:latest
```

This will show detailed information about each middleware component's execution and data flow.
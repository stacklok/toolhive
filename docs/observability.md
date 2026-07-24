# Observability and Telemetry

This document describes the observability architecture implemented in ToolHive
for monitoring MCP (Model Context Protocol) server interactions. ToolHive
provides OpenTelemetry-based instrumentation with support for distributed
tracing, metrics collection, and structured logging.

This document is intended for developers working on ToolHive. For user guides on
setting up and using these features, see the ToolHive documentation:

- [Observability overview](https://docs.stacklok.com/toolhive/concepts/observability),
  including trace structure and example metrics
- [CLI guide](https://docs.stacklok.com/toolhive/guides-cli/telemetry-and-metrics),
  including how to enable and configure telemetry and send to common backends

To run a complete local observability stack (Prometheus, Grafana, and the
OpenTelemetry Collector) for testing this instrumentation, see the
[OpenTelemetry example stack](../examples/otel/README.md).

For migrating from legacy attribute names to the new OTEL MCP semantic
conventions, see the [Telemetry Migration Guide](./telemetry-migration-guide.md).

## Overview

ToolHive's observability stack provides complete visibility into MCP proxy
operations through:

1. **Distributed tracing**: Track requests across the proxy-container boundary
   with OpenTelemetry traces
2. **Metrics collection**: Monitor performance, usage patterns, and error rates
   with Prometheus and OTLP metrics
3. **Structured logging**: Capture detailed audit events for compliance and
   debugging
4. **Protocol-aware instrumentation**: MCP-specific insights beyond generic HTTP
   metrics

See [the original design document](https://github.com/stacklok/toolhive-rfcs/blob/main/rfcs/THV-0001-otel-integration-proposal.md) for
more details on the design and goals of this observability architecture.

## Architecture

```mermaid
graph TD
    A[MCP Client] --> B[ToolHive Proxy Runner]
    B --> C[Container MCP Server]

    B --> D[OpenTelemetry Middleware]
    D --> E[Trace Exporter]
    D --> F[Metrics Exporter]

    E --> G[OTLP Endpoint]
    E --> H[Jaeger]
    E --> I[DataDog]

    F --> J[Prometheus /metrics]
    F --> K[OTLP Metrics]

    G --> L[Observability Backend]
    K --> L
    J --> M[Prometheus Server]

    classDef toolhive fill:#EDD9A3,color:#000;
    classDef external fill:#7AB7FF,color:#000;
    class B,D toolhive;
    class L,M external;
```

## Integration with Existing Middleware

The OpenTelemetry middleware integrates seamlessly with ToolHive's
[existing middleware stack](./middleware.md):

```mermaid
graph TD
    A[HTTP Request] --> B[Authentication Middleware]
    B --> C[MCP Parsing Middleware]
    C --> D[OpenTelemetry Middleware]
    D --> E[Authorization Middleware]
    E --> F[Audit Middleware]
    F --> G[MCP Server Handler]

    style D fill:#EDD9A3,color:#000;
```

The telemetry middleware:

- **Leverages parsed MCP data** from the parsing middleware
- **Includes authentication context** from JWT claims
- **Captures authorization decisions** for compliance
- **Correlates with audit events** for complete observability

This provides end-to-end visibility across the entire request lifecycle while
maintaining the modular architecture of ToolHive's middleware system.

## Configuration

### CLI Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--otel-endpoint` | string | `""` | OTLP endpoint URL (e.g., `localhost:4317`). Telemetry is disabled when empty and Prometheus is not enabled. |
| `--otel-tracing-enabled` | bool | `true` | Enable distributed tracing (requires endpoint) |
| `--otel-metrics-enabled` | bool | `true` | Enable OTLP metrics export (requires endpoint) |
| `--otel-sampling-rate` | float | `0.1` | Trace sampling rate (0.0–1.0). The CLI default is `0.1` (10%); the Kubernetes CRD default is `0.05` (5%). Config file values override the CLI default when the flag is not explicitly set. |
| `--otel-service-name` | string | `"toolhive-mcp-proxy"` | Service name for telemetry resource |
| `--otel-headers` | string[] | `nil` | OTLP authentication headers (`key=value` format) |
| `--otel-insecure` | bool | `false` | Use HTTP instead of HTTPS for the OTLP endpoint |
| `--otel-enable-prometheus-metrics-path` | bool | `false` | Expose Prometheus `/metrics` endpoint on the transport port |
| `--otel-env-vars` | string[] | `nil` | Environment variables to include in spans (comma-separated) |
| `--otel-custom-attributes` | string | `""` | Custom resource attributes (`key1=value1,key2=value2`) |
| `--otel-use-legacy-attributes` | bool | `true` | Emit legacy attribute names alongside new OTEL semantic convention names |

### Configuration File

Telemetry can also be configured via `~/.toolhive/config.yaml`:

```yaml
otel:
  endpoint: "localhost:4317"
  sampling-rate: 0.1
  env-vars:
    - NODE_ENV
    - DEPLOYMENT_ENV
  insecure: true
  use-legacy-attributes: false
```

CLI flags take precedence over configuration file values when explicitly set.

### Kubernetes CRD

**MCPTelemetryConfig (preferred)**: Define telemetry settings in a shared
`MCPTelemetryConfig` resource and reference it via `spec.telemetryConfigRef`
in MCPServer, MCPRemoteProxy, or VirtualMCPServer. This eliminates duplication
when managing multiple servers. Each server provides a unique `serviceName`
override. Sensitive headers (API keys, bearer tokens) are stored in Kubernetes
Secrets via `sensitiveHeaders[].secretKeyRef`.

```yaml
apiVersion: toolhive.stacklok.dev/v1beta1
kind: MCPTelemetryConfig
metadata:
  name: shared-otel
spec:
  openTelemetry:
    enabled: true
    endpoint: otel-collector:4318
    insecure: true
    tracing:
      enabled: true
      samplingRate: "0.1"
    metrics:
      enabled: true
---
apiVersion: toolhive.stacklok.dev/v1beta1
kind: MCPServer
metadata:
  name: my-server
spec:
  # ... other fields ...
  telemetryConfigRef:
    name: shared-otel
    serviceName: my-server    # unique per server
```

See [`examples/operator/mcp-servers/mcpserver_fetch_otel.yaml`](../examples/operator/mcp-servers/mcpserver_fetch_otel.yaml)
for a complete example.

**Inline (deprecated)**: The inline `spec.telemetry` (MCPServer, MCPRemoteProxy)
and `spec.config.telemetry` (VirtualMCPServer) fields still work but are
deprecated and will be removed in a future API version. They are mutually exclusive with
`telemetryConfigRef` (CEL enforced). All three resource types now support
`spec.telemetryConfigRef`.

For VirtualMCPServer telemetry, see the
[vMCP observability docs](./operator/virtualmcpserver-observability.md).

### Validation Rules

- If an OTLP endpoint is configured but both `tracingEnabled` and
  `metricsEnabled` are `false`, configuration validation fails.
- If only `enablePrometheusMetricsPath` is enabled (no OTLP endpoint),
  Prometheus metrics are served without OTLP export.
- If nothing is configured (no endpoint, no Prometheus), telemetry is disabled.

## Metrics Reference

### MCP Proxy Metrics

These metrics are emitted by the telemetry middleware (`pkg/telemetry/middleware.go`)
for each MCP server proxy. Metric and label names follow the shared
`stacklok.*`/OTel semantic-convention vocabulary (Metrics Standardization RFC);
see the [Telemetry Migration Guide](./telemetry-migration-guide.md) for the
mapping from the legacy `toolhive_mcp_*` names these replace.

#### `stacklok.toolhive.proxy.active_connections` (UpDownCounter)

Number of currently active MCP connections. Prometheus exposes this as
`stacklok_toolhive_proxy_active_connections`.

| Attribute | Type | Description |
|-----------|------|-------------|
| `mcp_server` | string | MCP server name |
| `transport` | string | Backend transport type |
| `connection_type` | string | `"sse"` (only present for SSE connections) |

#### `mcp.server.operation.duration` (Histogram, seconds)

Duration of MCP server operations per the
[OTEL MCP semantic conventions](https://github.com/open-telemetry/semantic-conventions/blob/main/docs/gen-ai/mcp.md).
Recorded only for requests with a resolvable MCP method (`tools/call`,
`resources/read`, etc.) — GET (SSE stream open) and DELETE (session
termination) requests carry no MCP method and are not recorded here.

**Bucket boundaries** (`coremetrics.BucketsMCPProxy()`): `[0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300]`

| Attribute | Type | Condition | Description |
|-----------|------|-----------|-------------|
| `mcp.method.name` | string | Always | MCP method (`tools/call`, `resources/read`, etc.) |
| `jsonrpc.protocol.version` | string | Always | Always `"2.0"` |
| `network.transport` | string | Always | `"tcp"` or `"pipe"` |
| `network.protocol.name` | string | If applicable | `"http"` for SSE/streamable-http |
| `network.protocol.version` | string | If available | HTTP protocol version (`1.1`, `2`) |
| `error.type` | string | On HTTP 5xx | HTTP status code as string |
| `gen_ai.operation.name` | string | For `tools/call` | Always `"execute_tool"` |
| `gen_ai.tool.name` | string | For `tools/call` | Tool name |
| `gen_ai.prompt.name` | string | For `prompts/get` | Prompt name |

> **Note**: `mcp_resource_id` (tool name, resource URI, or prompt name) is
> deliberately omitted from this metric's attributes to bound cardinality; it
> feeds `gen_ai.tool.name`/`gen_ai.prompt.name` instead, which are themselves
> only present for the method kinds that have a name to report.

#### `http.server.request.duration` (Histogram, seconds)

Duration of every HTTP request the middleware handles, per the
[OTEL HTTP semantic conventions](https://opentelemetry.io/docs/specs/semconv/http/).
Recorded for every request — including SSE-open GETs and session-delete
DELETEs that carry no MCP method — so transport-level coverage doesn't depend
on a resolvable MCP method the way `mcp.server.operation.duration` does. For
SSE connections, recorded once the connection closes (not per-chunk).

**Bucket boundaries** (`coremetrics.BucketsFastHTTP()`): `[0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10]`

| Attribute | Type | Condition | Description |
|-----------|------|-----------|-------------|
| `http.request.method` | string | Always | HTTP method (`GET`, `POST`, `DELETE`) |
| `http.response.status_code` | int | Always | HTTP status code |
| `error.type` | string | On HTTP 5xx | HTTP status code as string |

#### `stacklok.build_info` (Gauge)

Registered once per process via the shared `coremetrics.RegisterBuildInfo`
helper (`pkg/telemetry/middleware.go`). Always reports `1`, carrying version
metadata as attributes.

| Attribute | Type | Description |
|-----------|------|-------------|
| `stacklok_component` | string | Always `"toolhive"` |
| `stacklok_product` | string | Always `"stacklok-platform"` |
| `version` | string | ToolHive version |
| `commit` | string | Build commit SHA |

### D8 Ownership Labels

Every metric emitted by ToolHive carries `stacklok_component="toolhive"` and
`stacklok_product="stacklok-platform"` as OTel resource attributes (not
per-series labels), applied as the last OTel resource detector so they cannot
be overridden by `--otel-custom-attributes` or `OTEL_RESOURCE_ATTRIBUTES`
(OTel resource merge is last-detector-wins). See
`pkg/telemetry/providers/providers.go`.

### Rate Limit Metrics

These metrics are emitted for Redis-backed rate limit checks used by MCPServer
and VirtualMCPServer. Prometheus appends `_total` to counter names. The latency
histogram is exported with the `_seconds` unit suffix and the standard
`_bucket`, `_sum`, and `_count` series suffixes.

#### `toolhive_rate_limit_decisions` (Counter)

Total number of rate limit bucket decisions. An allowed request increments once
for every applicable bucket. A rejected request increments only for the first
bucket rejected by the atomic Redis check. Requests with no applicable bucket
do not increment this counter.

| Attribute | Type | Description |
|-----------|------|-------------|
| `namespace` | string | Kubernetes namespace associated with the server |
| `mcp_server` | string | MCPServer or VirtualMCPServer name |
| `decision` | string | `"allowed"` or `"rejected"` |
| `scope` | string | `"shared"` or `"per_user"` |
| `operation_type` | string | `"server"` or `"tool"` |

#### `toolhive_rate_limit_redis_errors` (Counter)

Total number of Redis errors encountered while checking rate limits.

| Attribute | Type | Description |
|-----------|------|-------------|
| `namespace` | string | Kubernetes namespace associated with the server |
| `mcp_server` | string | MCPServer or VirtualMCPServer name |
| `error_type` | string | `"timeout"`, `"connection"`, `"auth"`, or `"other"` |

#### `toolhive_rate_limit_check_latency` (Histogram, seconds)

Duration of each attempted atomic Redis Lua rate limit check, including failed
checks.

| Attribute | Type | Description |
|-----------|------|-------------|
| `namespace` | string | Kubernetes namespace associated with the server |
| `mcp_server` | string | MCPServer or VirtualMCPServer name |

## Span Attributes

### HTTP Attributes

These follow the [OTEL HTTP semantic conventions](https://opentelemetry.io/docs/specs/semconv/http/).
They are always emitted.

**Request attributes:**

| Attribute | Type | Description |
|-----------|------|-------------|
| `http.request.method` | string | HTTP request method |
| `url.full` | string | Full request URL |
| `url.scheme` | string | URL scheme (`http`, `https`) |
| `url.path` | string | URL path |
| `url.query` | string | URL query string (if present) |
| `server.address` | string | Server host |
| `user_agent.original` | string | User agent string |
| `http.request.body.size` | int64 | Request body size (if > 0) |

**Response attributes:**

| Attribute | Type | Description |
|-----------|------|-------------|
| `http.response.status_code` | int | Response HTTP status code |
| `http.response.body.size` | int64 | Response body size |

### MCP Protocol Attributes

These are set when an MCP JSON-RPC request is parsed by the MCP parsing
middleware (`pkg/mcp/parser.go`).

| Attribute | Type | Condition | Description |
|-----------|------|-----------|-------------|
| `mcp.method.name` | string | Always | MCP JSON-RPC method name |
| `rpc.system.name` | string | Always | Always `"jsonrpc"` |
| `jsonrpc.protocol.version` | string | Always | Always `"2.0"` |
| `jsonrpc.request.id` | string | If request has ID | JSON-RPC request ID |
| `mcp.resource.uri` | string | Resource methods only | Resource URI |
| `mcp.server.name` | string | Always | MCP server name |
| `mcp.is_batch` | bool | If batch request | Batch request indicator |

The `mcp.resource.uri` attribute is set only for the following methods:
`resources/read`, `resources/subscribe`, `resources/unsubscribe`,
`notifications/resources/updated`.

### Tool, Prompt, and Resource Attributes

**For `tools/call`:**

| Attribute | Type | Description |
|-----------|------|-------------|
| `gen_ai.tool.name` | string | Tool name |
| `gen_ai.operation.name` | string | Always `"execute_tool"` |
| `gen_ai.tool.call.arguments` | string | Sanitized tool arguments (max 200 chars) |

**For `prompts/get`:**

| Attribute | Type | Description |
|-----------|------|-------------|
| `gen_ai.prompt.name` | string | Prompt name |

**For `initialize`:**

| Attribute | Type | Description |
|-----------|------|-------------|
| `mcp.client.name` | string | Client name from `clientInfo` |

### Network and Transport Attributes

| Attribute | Type | Description | Values |
|-----------|------|-------------|--------|
| `network.transport` | string | Network transport protocol | `"tcp"` (SSE, streamable-http), `"pipe"` (stdio) |
| `network.protocol.name` | string | Application protocol | `"http"` (SSE, streamable-http), empty (stdio) |
| `network.protocol.version` | string | HTTP protocol version | `"1.1"`, `"2"` |
| `mcp.backend.protocol.version` | string | Backend MCP protocol version | SSE: `"1.1"` |

### Session and Client Attributes

| Attribute | Type | Condition | Description |
|-----------|------|-----------|-------------|
| `mcp.session.id` | string | `Mcp-Session-Id` header present | Session identifier |
| `mcp.protocol.version` | string | `MCP-Protocol-Version` header present | MCP protocol version |
| `client.address` | string | Remote address available | Client IP address |
| `client.port` | int | Port parseable from remote address | Client port |

### Error Attributes

| Attribute | Type | Condition | Description |
|-----------|------|-----------|-------------|
| `error.type` | string | HTTP 5xx errors | HTTP status code as string (e.g., `"500"`) |

**Span status behavior:**
- HTTP 5xx: Span status set to `Error` with message `"HTTP {code}"`
- HTTP 4xx: Span status left as `Unset` (client errors per OTEL semconv)
- HTTP 2xx/3xx: Span status set to `Ok`

### Environment and Custom Attributes

**Environment variables** (`--otel-env-vars`): Specified host environment
variables are read and added to spans as `environment.{VAR_NAME}` attributes.
Only variables explicitly listed in the configuration are captured.

**Custom resource attributes** (`--otel-custom-attributes` or
`OTEL_RESOURCE_ATTRIBUTES`): Key-value pairs added as OTEL resource attributes
to all telemetry signals.

### SSE Connection Attributes

SSE connections get a dedicated short-lived span (`sse.connection_established`)
with:

| Attribute | Type | Description |
|-----------|------|-------------|
| `sse.event_type` | string | Always `"connection_established"` |
| `mcp.server.name` | string | MCP server name |

Plus the standard HTTP, network, and transport attributes.

## Span Naming Conventions

Span names follow the OTEL MCP semantic conventions:

| Pattern | When | Example |
|---------|------|---------|
| `{mcp.method.name} {target}` | MCP request with resource ID | `"tools/call fetch"` |
| `{mcp.method.name}` | MCP request without resource ID | `"initialize"` |
| `{HTTP_METHOD} {url.path}` | Non-MCP requests (fallback) | `"GET /health"` |
| `sse.connection_established` | SSE connection setup | — |

All proxy spans use `SpanKindServer`.

## Distributed Tracing

### Trace Context Propagation

ToolHive supports W3C Trace Context propagation through two mechanisms:

1. **HTTP headers** — Standard `traceparent` and `tracestate` headers
2. **MCP `_meta` field** — Trace context embedded in the JSON-RPC
   `params._meta` object, as recommended by the MCP OpenTelemetry specification

**Priority**: When both are present, `_meta` trace context takes precedence
over HTTP headers, since `_meta` is the MCP-specified propagation mechanism.

### How It Works

**Inbound (client → ToolHive proxy):**

The telemetry middleware first extracts trace context from HTTP headers, then
checks for `_meta` in the parsed MCP request. If `_meta` contains `traceparent`
(and optionally `tracestate`), the middleware extracts the trace context from it,
which overrides the HTTP header context. A child span is then created with the
extracted trace as parent.

```json
{
  "method": "tools/call",
  "params": {
    "name": "fetch",
    "arguments": {"url": "https://example.com"},
    "_meta": {
      "traceparent": "00-abcdef1234567890abcdef1234567890-1234567890abcdef-01",
      "tracestate": "vendor=value"
    }
  }
}
```

**Outbound (vMCP → backend):**

The `InjectMetaTraceContext` function (`pkg/telemetry/propagation.go`) can
inject the current trace context into the `_meta` field when forwarding requests
to backends, enabling end-to-end distributed tracing across the vMCP
aggregation layer.

### Propagators

ToolHive configures the following OTEL propagators globally:
- `propagation.TraceContext{}` — W3C Trace Context
- `propagation.Baggage{}` — W3C Baggage

### Implementation

The trace context propagation is implemented in `pkg/telemetry/propagation.go`
using a `MetaCarrier` that implements `propagation.TextMapCarrier` for MCP
`_meta` maps. The MCP `_meta` field is extracted by the MCP parsing middleware
(`pkg/mcp/parser.go`) and stored in the request context.

## Legacy Attribute Compatibility

ToolHive supports dual emission of span attributes controlled by the
`useLegacyAttributes` configuration option. When set to `true` (the current
default), both legacy and new OTEL semantic convention attribute names are
emitted on every span, allowing existing dashboards to continue working during
migration.

For a complete mapping of legacy to new attribute names and migration
instructions, see the [Telemetry Migration Guide](./telemetry-migration-guide.md).

## Virtual MCP Server Telemetry

For observability in the Virtual MCP Server (vMCP), including backend request
metrics, workflow execution telemetry, and distributed tracing, see the
dedicated [Virtual MCP Server Observability](./operator/virtualmcpserver-observability.md)
documentation.

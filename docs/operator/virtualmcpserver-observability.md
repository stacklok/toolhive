# Virtual MCP Server Observability

This document describes the observability for the Virtual MCP
Server (vMCP), which aggregates multiple backend MCP servers into a unified
interface. The vMCP provides OpenTelemetry-based instrumentation for monitoring
backend operations and composite tool workflow executions.

For general ToolHive observability concepts and proxy runner telemetry, see the
main [Observability and Telemetry](../observability.md) documentation.

For migrating from legacy attribute names to the new OTEL MCP semantic
conventions, see the [Telemetry Migration Guide](../telemetry-migration-guide.md).

## Overview

The vMCP telemetry provides visibility into:

1. **Backend operations**: Track requests to individual backend MCP servers
   including tool calls, resource reads, prompt retrieval, and capability listing
2. **Workflow executions**: Monitor composite tool workflow performance and errors
3. **Distributed tracing**: Correlate requests across the vMCP and its backends

The vMCP uses a decorator pattern to wrap backend clients and workflow executors
with telemetry instrumentation. This approach provides consistent metrics and
tracing without modifying the core business logic.

The implementation of backend metrics and traces can be found in
`pkg/vmcp/internal/backendtelemetry/backendtelemetry.go`; composite tool
(workflow) telemetry is in `pkg/vmcp/core/core_telemetry.go` and
`pkg/vmcp/server/sessionmanager/factory.go`.

## Metrics

Metric and label names follow the shared `stacklok.*`/OTel semantic-convention
vocabulary (Metrics Standardization RFC); see the
[Telemetry Migration Guide](../telemetry-migration-guide.md) for the mapping
from the legacy `toolhive_vmcp_*` names these replace.

### Backend Metrics

Backend metrics track requests to individual backend MCP servers
(`pkg/vmcp/internal/backendtelemetry/backendtelemetry.go`).

#### `stacklok.vmcp.mcp_server.health` (Gauge)

Per-backend health, re-derived from the live backend registry on every
collection so a backend removed at runtime (e.g. via `list_changed`) stops
being reported instead of leaving a stale series behind. Emits one point per
`(mcp_server, state)` pair — the observed state reports `1`, the other `0`.

| Attribute | Type | Description |
|-----------|------|-------------|
| `mcp_server` | string | Backend workload name |
| `state` | string | `"healthy"` or `"unhealthy"` |

#### `mcp.client.operation.duration` (Histogram, seconds)

Duration of MCP client operations per the
[OTEL MCP semantic conventions](https://github.com/open-telemetry/semantic-conventions/blob/main/docs/gen-ai/mcp.md).

**Bucket boundaries** (`coremetrics.BucketsMCPProxy()`): `[0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300]`

| Attribute | Type | Condition | Description |
|-----------|------|-----------|-------------|
| `mcp.method.name` | string | Always | MCP method name |
| `network.transport` | string | Always | `"tcp"` or `"pipe"` |
| `error.type` | string | On error | Go error type (e.g., `*url.Error`) |

Backend operation spans carry additional ToolHive-specific and OTEL MCP
semconv attributes (`target.workload_id`, `target.workload_name`,
`target.base_url`, `target.transport_type`, `action`, `gen_ai.tool.name`,
`mcp.resource.uri`, `gen_ai.prompt.name`) — see
[Backend Operation Spans](#backend-operation-spans) below.

### Composite Tool (Workflow) Metrics

Composite tool metrics track workflow executions
(`pkg/vmcp/core/core_telemetry.go`, `pkg/vmcp/server/sessionmanager/factory.go`).

#### `stacklok.vmcp.composite_tool.executions` (Counter)

Total number of composite tool workflow executions, split by outcome.

| Attribute | Type | Description |
|-----------|------|-------------|
| `composite_tool` | string | Composite tool (workflow) name |
| `outcome` | string | `"success"` or `"error"` |

#### `stacklok.vmcp.composite_tool.duration` (Histogram, seconds)

Duration of composite tool workflow executions, recorded regardless of outcome.

**Bucket boundaries** (`coremetrics.BucketsMCPProxy()`): `[0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300]`

| Attribute | Type | Description |
|-----------|------|-------------|
| `composite_tool` | string | Composite tool (workflow) name |

### Token Cache Metrics

#### `stacklok.vmcp.token_cache.requests` (Counter)

Total number of vMCP token cache lookups, split by hit/miss result
(`pkg/vmcp/cache/telemetry.go`). Not yet emitted: no production `TokenCache`
implementation is currently wired through `NewMeteredTokenCache`.

| Attribute | Type | Description |
|-----------|------|-------------|
| `result` | string | `"hit"` or `"miss"` |

## Distributed Tracing

### Backend Operation Spans

The vMCP creates a span for each backend operation with `SpanKindClient`.

**Span naming convention**: `{mcp.method.name} {target}` where target is the
tool name or prompt name. For methods without a bounded target (e.g.,
`resources/read`, `list_capabilities`), only the method name is used to avoid
unbounded cardinality in span names. The resource URI is captured in span
attributes instead.

Examples:
- `"tools/call fetch"` — tool call to the "fetch" tool
- `"resources/read"` — resource read (URI in `mcp.resource.uri` attribute)
- `"prompts/get summarize"` — prompt retrieval for "summarize"
- `"list_capabilities"` — capability listing

**Span attributes** include both ToolHive-specific backward-compatible attributes
(`target.workload_id`, `target.workload_name`, `target.base_url`,
`target.transport_type`, `action`) and OTEL MCP spec attributes
(`mcp.method.name`, `gen_ai.tool.name`, `mcp.resource.uri`,
`gen_ai.prompt.name`).

**Error handling**: On error, the span records the error via `span.RecordError()`
and sets status to `codes.Error`.

### Workflow Execution Spans

Workflow executor spans use the name `telemetryWorkflowExecutor.ExecuteWorkflow`
with the `workflow.name` attribute. These spans nest the individual backend
operation spans, enabling attribution of workflow errors or latency to specific
tool calls.

### Trace Context Propagation

The vMCP client passes the current context through to backend calls, preserving
trace context across the vMCP aggregation layer. The
`InjectMetaTraceContext` function (`pkg/telemetry/propagation.go`) can inject
W3C Trace Context (`traceparent`, `tracestate`) into the MCP `_meta` field for
backends that support it.

## Configuration

**MCPTelemetryConfig (preferred)**: Define telemetry settings in a shared
`MCPTelemetryConfig` resource and reference it via `spec.telemetryConfigRef`
in VirtualMCPServer. This eliminates duplication when managing multiple servers
and keeps telemetry configuration consistent across MCPServer, MCPRemoteProxy,
and VirtualMCPServer resources.

```yaml
# Shared telemetry configuration
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
  prometheus:
    enabled: true
---
# VirtualMCPServer referencing shared telemetry config
apiVersion: toolhive.stacklok.dev/v1beta1
kind: VirtualMCPServer
metadata:
  name: my-vmcp
spec:
  telemetryConfigRef:
    name: shared-otel
    serviceName: my-vmcp
  groupRef:
    name: my-group
  incomingAuth:
    type: anonymous
```

See [`examples/operator/virtual-mcps/vmcp_with_telemetry_ref.yaml`](../../examples/operator/virtual-mcps/vmcp_with_telemetry_ref.yaml)
for a complete example with an MCPGroup and backend MCPServer.

**Inline (deprecated)**: The inline `spec.config.telemetry` field still works
but is deprecated and will be removed in a future API version. It is mutually exclusive with
`telemetryConfigRef` (CEL enforced). Migrate to `telemetryConfigRef` to use the
shared MCPTelemetryConfig pattern.

```yaml
# Deprecated — use telemetryConfigRef instead
apiVersion: toolhive.stacklok.dev/v1beta1
kind: VirtualMCPServer
metadata:
  name: my-vmcp
spec:
  groupRef:
    name: my-group
  config:
    telemetry:
      endpoint: "otel-collector:4317"
      serviceName: "my-vmcp"
      insecure: true
      tracingEnabled: true
      samplingRate: "0.1"
      metricsEnabled: true
      enablePrometheusMetricsPath: true
      useLegacyAttributes: true
  incomingAuth:
    type: anonymous
```

See the [VirtualMCPServer API reference](./virtualmcpserver-api.md) for complete
CRD documentation.

## Related Documentation

- [Observability and Telemetry](../observability.md) - Main ToolHive observability documentation
- [Telemetry Migration Guide](../telemetry-migration-guide.md) - Legacy to new attribute migration
- [VirtualMCPServer API Reference](./virtualmcpserver-api.md) - Complete CRD specification

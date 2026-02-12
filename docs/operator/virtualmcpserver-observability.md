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

The implementation of both metrics and traces can be found in `pkg/vmcp/server/telemetry.go`.

## Metrics

### Backend Metrics

Backend metrics track requests to individual backend MCP servers.

#### `toolhive_vmcp_backends_discovered` (Gauge)

Number of backends discovered. Recorded once at startup.

#### `toolhive_vmcp_backend_requests` (Counter)

Total number of requests sent to backend MCP servers.

| Attribute | Type | Description |
|-----------|------|-------------|
| `target.workload_id` | string | Backend workload ID |
| `target.workload_name` | string | Backend workload name |
| `target.base_url` | string | Backend base URL |
| `target.transport_type` | string | Backend transport type (`stdio`, `sse`, `streamable-http`) |
| `action` | string | Internal action name (`call_tool`, `read_resource`, `get_prompt`, `list_capabilities`) |
| `mcp.method.name` | string | MCP method name (`tools/call`, `resources/read`, `prompts/get`, `list_capabilities`) |

Method-specific attributes (added in addition to the above):

| Attribute | Method | Description |
|-----------|--------|-------------|
| `tool_name` | `call_tool` | Tool name (ToolHive-specific) |
| `gen_ai.tool.name` | `call_tool` | Tool name (OTEL MCP semconv) |
| `resource_uri` | `read_resource` | Resource URI (ToolHive-specific) |
| `mcp.resource.uri` | `read_resource` | Resource URI (OTEL MCP semconv) |
| `prompt_name` | `get_prompt` | Prompt name (ToolHive-specific) |
| `gen_ai.prompt.name` | `get_prompt` | Prompt name (OTEL MCP semconv) |

#### `toolhive_vmcp_backend_errors` (Counter)

Total number of errors from backend MCP servers.

**Attributes**: Same as `toolhive_vmcp_backend_requests`.

#### `toolhive_vmcp_backend_requests_duration` (Histogram, seconds)

Duration of requests to backend MCP servers. Uses default histogram bucket
boundaries.

**Attributes**: Same as `toolhive_vmcp_backend_requests`.

#### `mcp.client.operation.duration` (Histogram, seconds)

Duration of MCP client operations per the
[OTEL MCP semantic conventions](https://github.com/open-telemetry/semantic-conventions/blob/main/docs/gen-ai/mcp.md).

**Bucket boundaries**: `[0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10, 30, 60, 120, 300]`

| Attribute | Type | Condition | Description |
|-----------|------|-----------|-------------|
| `mcp.method.name` | string | Always | MCP method name |
| `network.transport` | string | Always | `"tcp"` or `"pipe"` |
| `error.type` | string | On error | Go error type (e.g., `*url.Error`) |

### Workflow Metrics

Workflow metrics track composite tool workflow executions.

#### `toolhive_vmcp_workflow_executions` (Counter)

Total number of workflow executions.

| Attribute | Type | Description |
|-----------|------|-------------|
| `workflow.name` | string | Workflow name |

#### `toolhive_vmcp_workflow_errors` (Counter)

Total number of workflow execution errors.

**Attributes**: Same as `toolhive_vmcp_workflow_executions`.

#### `toolhive_vmcp_workflow_duration` (Histogram, seconds)

Duration of workflow executions.

**Attributes**: Same as `toolhive_vmcp_workflow_executions`.

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

Configure telemetry in the `VirtualMCPServer` resource using the `spec.config.telemetry`
field. The telemetry configuration uses the `telemetry.Config` type which provides
flat configuration for OpenTelemetry and Prometheus settings.

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: VirtualMCPServer
metadata:
  name: my-vmcp
spec:
  config:
    groupRef: my-group
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

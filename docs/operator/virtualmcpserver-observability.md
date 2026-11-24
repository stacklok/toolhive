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
3. **Code mode**: Track `execute_tool_script` executions, their outcome, duration,
   inner tool calls and `parallel()` fan-out
4. **Distributed tracing**: Correlate requests across the vMCP and its backends

The vMCP uses a decorator pattern to wrap backend clients and workflow executors
with telemetry instrumentation. This approach provides consistent metrics and
tracing without modifying the core business logic.

The implementation of both metrics and traces can be found in `pkg/vmcp/internal/backendtelemetry/backendtelemetry.go`.

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

### Code Mode Metrics

Code mode metrics are recorded when code mode is enabled and telemetry is
configured. They track calls to the `execute_tool_script` virtual tool. The
Prometheus exporter adds the `_total` and `_seconds` suffixes.

The `status` attribute holds how the script ended:

| `status` | Meaning |
|----------|---------|
| `success` | The script ran and returned a result |
| `error` | Invalid arguments, a runtime error in the script, or a failed tool call |
| `timeout` | The script or one of its tool calls hit its deadline |
| `step_limit` | The script, or a callable it ran under `parallel()`, used up its step budget |

#### `toolhive_vmcp_code_mode_enabled` (Gauge)

`1` when code mode is enabled on the vMCP server, `0` when it is not. It is
reported whenever telemetry is configured, so disabled servers count too. A code
mode change restarts the vMCP pod, so the value always matches the running
configuration.

| Attribute | Type | Description |
|-----------|------|-------------|
| `server` | string | vMCP server name (same label as `toolhive_mcp_*`) |

#### `toolhive_vmcp_script_executions` (Counter)

Total number of `execute_tool_script` calls. **Attributes**: `status`.

#### `toolhive_vmcp_script_duration` (Histogram, seconds)

Wall-clock duration of each `execute_tool_script` call. **Attributes**: `status`.

**Bucket boundaries**: `[0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10, 30, 60, 120, 300]`

#### `toolhive_vmcp_script_tool_calls` (Histogram, `{call}`)

Number of tool calls each script started, including calls that failed or were
denied. Calls made under `parallel()` count individually. **Attributes**: `status`.

**Bucket boundaries**: `[0, 1, 2, 3, 5, 10, 20, 50, 100, 250]`

#### `toolhive_vmcp_script_parallel_tasks` (Counter, `{task}`)

Total number of callables started by `parallel()`, summed over every
`parallel()` call in every script. Divide by
`toolhive_vmcp_script_executions` for the average fan-out per script.
**Attributes**: `status`.

#### Code mode vs. regular tool calls

The vMCP request metrics already label each `tools/call` with the tool name, so
no separate counter is needed to split code mode from regular calls:
`toolhive_mcp_tool_calls` (attribute `tool`) counts `execute_tool_script` as
one call. A script's inner tool calls do not pass the request middleware, so
they are counted only by `toolhive_vmcp_script_tool_calls` and, per backend,
by `toolhive_vmcp_backend_requests`.

The `status` label of `toolhive_mcp_tool_calls` is the HTTP outcome. A script
that fails still answers with HTTP 200 and `isError: true`, so use
`toolhive_vmcp_script_executions{status}` for script outcomes.

#### Example PromQL

```promql
# Share of tool calls that are code mode scripts (last 5 minutes)
sum(rate(toolhive_mcp_tool_calls_total{tool="execute_tool_script"}[5m]))
/
sum(rate(toolhive_mcp_tool_calls_total[5m]))

# Code mode call rate vs. regular call rate
sum(rate(toolhive_mcp_tool_calls_total{tool="execute_tool_script"}[5m]))
sum(rate(toolhive_mcp_tool_calls_total{tool!="execute_tool_script"}[5m]))

# vMCP servers with code mode enabled, and the share of all servers
sum(toolhive_vmcp_code_mode_enabled)
sum(toolhive_vmcp_code_mode_enabled) / count(toolhive_vmcp_code_mode_enabled)

# Script error rate by status
sum by (status) (rate(toolhive_vmcp_script_executions_total{status!="success"}[5m]))
/ ignoring(status) group_left
sum(rate(toolhive_vmcp_script_executions_total[5m]))

# p95 script duration
histogram_quantile(0.95, sum by (le) (rate(toolhive_vmcp_script_duration_seconds_bucket[5m])))

# Average tool calls per script, and average parallel() fan-out per script
sum(rate(toolhive_vmcp_script_tool_calls_sum[5m])) / sum(rate(toolhive_vmcp_script_tool_calls_count[5m]))
sum(rate(toolhive_vmcp_script_parallel_tasks_total[5m])) / sum(rate(toolhive_vmcp_script_executions_total[5m]))
```

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

### Code Mode Spans

Each `execute_tool_script` call gets a `codemode.ExecuteToolScript` span. Each
tool call the script makes gets a `codemode.CallTool` child span, including
calls made under `parallel()`, and the backend operation span for that call
nests under it.

| Span | Attribute | Description |
|------|-----------|-------------|
| `codemode.ExecuteToolScript` | `script.status` | `success`, `error`, `timeout` or `step_limit` |
| | `script.tool_count` | Tool calls the script started, including failed and denied ones |
| | `script.parallel_used` | Whether the script called `parallel()` |
| | `script.parallel_calls` | Number of `parallel()` calls |
| | `script.parallel_tasks` | Callables started by `parallel()` |
| | `script.step_count` | Starlark execution steps, including `parallel()` children |
| | `script.hash` | First 16 hex characters of the SHA-256 of the script body |
| `codemode.CallTool` | `tool_name`, `gen_ai.tool.name` | The tool called |

A failed script or tool call sets the span status to `codes.Error` with the
outcome as its description. The error text is not recorded on spans, because it
can quote script data; it is in the debug log instead.

### Trace Context Propagation

The vMCP client passes the current context through to backend calls, preserving
trace context across the vMCP aggregation layer. The
`InjectMetaTraceContext` function (`pkg/telemetry/propagation.go`) can inject
W3C Trace Context (`traceparent`, `tracestate`) into the MCP `_meta` field for
backends that support it.

## Code Mode Logs

Code mode logs each script at DEBUG, so they are off by default. A failed
script is the caller's error and can happen at request rate, so it stays at
DEBUG as well; alert on `toolhive_vmcp_script_executions{status!="success"}`
instead.

| Message | Fields |
|---------|--------|
| `codemode: script started` | `script_hash`, `session_id`, `principal`, `script_len` |
| `codemode: script finished` | `script_hash`, `session_id`, `status`, `duration`, `tool_calls`, `steps`, `parallel_calls`, `parallel_tasks`, `error` (on failure) |

The script body is never logged; `script_hash` correlates runs of the same
script. `error` is the message the caller received. `session_id` is empty for
requests without an MCP session (stateless Modern requests).

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

**Inline (deprecated, operator no-op)**: `spec.config.telemetry` is ignored by
the operator. It remains valid for standalone CLI deployments only. If it is set
without `spec.telemetryConfigRef`, the operator emits a Warning event
(`InlineTelemetryIgnored`) and does not register `/metrics` — Prometheus scrapes
then receive HTTP 406 from the MCP handler. Use `telemetryConfigRef`. Setting
both fields is not CEL-rejected; `telemetryConfigRef` is the operator source of
truth and inline telemetry is ignored.

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

### Where `/metrics` is served

With `enablePrometheusMetricsPath` enabled, Virtual MCP serves `/metrics` on a
**dedicated diagnostics port** (default `9464`). Point scrapers at that port, and keep
it out of any Service or Ingress that faces the internet.

During the migration window `/metrics` is *also* still served on the port carrying MCP
traffic, so an existing scrape configuration keeps working until you move it. See
[Migration window](../observability.md#migration-window) and
[issue #6384](https://github.com/stacklok/toolhive/issues/6384) for the timeline.

**`prometheusPort` and `metricsOnTransportPort` are only settable through the inline
`spec.config.telemetry` path** — both examples above sit above this section, but only
the inline (deprecated) one can set them: they live on `telemetry.Config`, which
`spec.config.telemetry` embeds directly, and the shared `MCPTelemetryConfig` used by
`telemetryConfigRef` does not carry them. A `telemetryConfigRef` deployment still gets
the default diagnostics port and the migration-window transport-port copy — it just
cannot change the port or opt out of the window early. Once you have moved a scraper to
the diagnostics port, set `metricsOnTransportPort: false` (inline config only) to
confirm nothing else depended on the old location.

The endpoint is unauthenticated, so restricting who can reach that port is what
protects it. See [Metrics endpoint exposure](../observability.md#metrics-endpoint-exposure)
for the rationale, a `NetworkPolicy` example, and guidance on scoping external routes.

See the [VirtualMCPServer API reference](./virtualmcpserver-api.md) for complete
CRD documentation.

## Related Documentation

- [Observability and Telemetry](../observability.md) - Main ToolHive observability documentation
- [Telemetry Migration Guide](../telemetry-migration-guide.md) - Legacy to new attribute migration
- [VirtualMCPServer API Reference](./virtualmcpserver-api.md) - Complete CRD specification

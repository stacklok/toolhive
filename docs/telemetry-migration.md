# Telemetry Migration Guide: MCP OTEL Semantic Conventions

This document describes the breaking changes to OpenTelemetry span attributes,
metrics, and span naming introduced by the alignment with the
[MCP OpenTelemetry Semantic Conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/mcp/)
specification.

If you have existing Grafana dashboards, Prometheus alerts, or trace queries
that reference the old attribute names, use this guide to migrate them to the
new names.

## Span Attribute Renames

The following span attributes have been renamed to match the MCP OTEL semantic
conventions. The old attribute names are no longer emitted.

| Old Attribute | New Attribute | Notes |
|---|---|---|
| `mcp.method` | `mcp.method.name` | MCP method name (e.g., `tools/call`, `resources/read`) |
| `mcp.request.id` | `jsonrpc.request.id` | JSON-RPC request identifier |
| `mcp.tool.name` | `gen_ai.tool.name` | Tool name for `tools/call` requests |
| `mcp.tool.arguments` | `gen_ai.tool.call.arguments` | Sanitized tool arguments (sensitive keys redacted) |
| `mcp.prompt.name` | `gen_ai.prompt.name` | Prompt name for `prompts/get` requests |
| `mcp.resource.id` | `mcp.resource.uri` | Resource identifier for `resources/read` requests |
| `mcp.transport` | `network.transport` | Transport type, mapped to standard values (see below) |
| `http.method` | `http.request.method` | HTTP request method |
| `http.url` | `url.full` | Full request URL |
| `http.scheme` | `url.scheme` | URL scheme (e.g., `http`, `https`) |
| `http.host` | `server.address` | Server host address |
| `http.target` | `url.path` | URL path |
| `http.user_agent` | `user_agent.original` | User-Agent header value |
| `http.request_content_length` | `http.request.body.size` | Request body size (now `Int64`, only set when > 0) |
| `http.query` | `url.query` | URL query string |
| `http.status_code` | `http.response.status_code` | HTTP response status code |
| `http.response_content_length` | `http.response.body.size` | Response body size in bytes |

### Removed HTTP Attributes

| Removed Attribute | Reason |
|---|---|
| `http.duration_ms` | Non-standard; span start/end timestamps already capture duration |

### Transport Value Mapping

The `network.transport` attribute now uses standard OTEL network transport
values instead of ToolHive-specific transport names:

| ToolHive Transport | `network.transport` Value |
|---|---|
| `stdio` | `pipe` |
| `sse` | `tcp` |
| `streamable-http` | `tcp` |

## Removed Attributes

The following attributes have been removed entirely. The MCP OTEL specification
does not use RPC semantic conventions.

| Removed Attribute | Reason |
|---|---|
| `rpc.system` | MCP spec does not use RPC conventions |
| `rpc.service` | MCP spec does not use RPC conventions |

If you have queries or alerts that filter on these attributes, remove those
filters or replace them with `mcp.method.name` and `mcp.protocol.version` as
appropriate.

## New Attributes

The following attributes are newly emitted and were not present before the
alignment.

| Attribute | Description |
|---|---|
| `mcp.protocol.version` | MCP protocol version, currently `"2025-06-18"` |
| `mcp.session.id` | Session ID extracted from the `Mcp-Session-Id` HTTP header |
| `network.protocol.name` | Protocol name (e.g., `"http"`); set for SSE and streamable-http transports |
| `network.protocol.version` | Protocol version derived from the HTTP request (e.g., `"1.1"` for HTTP/1.1, `"2"` for HTTP/2) |
| `client.address` | Client IP address extracted from `RemoteAddr` |
| `client.port` | Client port number extracted from `RemoteAddr` |
| `gen_ai.operation.name` | Set to `"execute_tool"` for `tools/call` requests |
| `error.type` | HTTP status code as a string (e.g., `"500"`) on server error responses (status >= 500) |

## New Standard Metrics

Two new histogram metrics are emitted alongside the existing legacy metrics.

| Metric Name | Type | Unit | Description |
|---|---|---|---|
| `mcp.server.operation.duration` | Histogram | seconds | Duration of individual MCP operations |
| `mcp.server.session.duration` | Histogram | seconds | Duration of MCP sessions (from `initialize` to session end) |

Both metrics use the following bucket boundaries (in seconds):

```
[0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10, 30, 60, 120, 300]
```

### Operation Duration Attributes

The `mcp.server.operation.duration` metric includes the following attributes:

- `mcp.method.name` -- the MCP method
- `network.transport` -- the mapped transport value
- `mcp.protocol.version` -- the MCP protocol version
- `error.type` -- present only on server error responses (5xx)
- `gen_ai.tool.name` -- present only for `tools/call`
- `gen_ai.operation.name` -- present only for `tools/call` (value: `"execute_tool"`)
- `gen_ai.prompt.name` -- present only for `prompts/get`

### Session Duration Attributes

The `mcp.server.session.duration` metric includes:

- `mcp.protocol.version` -- the MCP protocol version
- `network.transport` -- the mapped transport value
- `network.protocol.name` -- present for HTTP-based transports

## Span Name Changes

Span names now follow the MCP OTEL specification format.

**Old format:** `mcp.{method}` (e.g., `mcp.tools/call`)

**New format:** `{method}` or `{method} {target}`

| MCP Method | Old Span Name | New Span Name | Notes |
|---|---|---|---|
| `tools/call` | `mcp.tools/call` | `tools/call my_tool` | Includes tool name as target |
| `prompts/get` | `mcp.prompts/get` | `prompts/get my_prompt` | Includes prompt name as target |
| `resources/read` | `mcp.resources/read` | `resources/read` | Method name only |
| `tools/list` | `mcp.tools/list` | `tools/list` | Method name only |
| `initialize` | `mcp.initialize` | `initialize` | Method name only |

Non-MCP requests (such as health checks) continue to use the
`{HTTP_METHOD} {path}` format (e.g., `GET /healthz`).

## Legacy Metrics (Unchanged)

The following legacy metrics continue to be emitted and are unchanged. They are
emitted alongside the new standard metrics, so existing dashboards and alerts
that use these metrics will continue to work without modification.

| Metric Name | Type | Description |
|---|---|---|
| `toolhive_mcp_requests_total` | Counter | Total number of MCP requests |
| `toolhive_mcp_request_duration_seconds` | Histogram | Duration of MCP requests in seconds |
| `toolhive_mcp_active_connections` | Gauge | Number of active MCP connections |
| `toolhive_mcp_tool_calls_total` | Counter | Total number of MCP tool calls |

These metrics retain their existing attribute names (`method`, `status_code`,
`status`, `mcp_method`, `mcp_resource_id`, `server`, `transport`) and are not
affected by the semantic convention alignment.

## Migration Examples

### Grafana / PromQL Queries

**Request rate by MCP method**

Old:
```promql
rate(toolhive_mcp_requests_total{mcp_method!="unknown"}[5m])
```

New (using standard metric):
```promql
rate(mcp_server_operation_duration_seconds_count[5m])
```

Note: The legacy query above continues to work. The standard metric provides
the same information with spec-compliant attribute names.

**P99 latency for tool calls**

Old:
```promql
histogram_quantile(0.99,
  rate(toolhive_mcp_request_duration_seconds_bucket{mcp_method="tools/call"}[5m])
)
```

New (using standard metric):
```promql
histogram_quantile(0.99,
  rate(mcp_server_operation_duration_seconds_bucket{
    mcp_method_name="tools/call"
  }[5m])
)
```

**Error rate by method**

Old:
```promql
rate(toolhive_mcp_requests_total{status="error"}[5m])
  / rate(toolhive_mcp_requests_total[5m])
```

New (using standard metric with error.type):
```promql
rate(mcp_server_operation_duration_seconds_count{error_type!=""}[5m])
  / rate(mcp_server_operation_duration_seconds_count[5m])
```

**Average session duration**

This metric is new and has no legacy equivalent:
```promql
rate(mcp_server_session_duration_seconds_sum[5m])
  / rate(mcp_server_session_duration_seconds_count[5m])
```

### Trace Queries

**Jaeger / Tempo: Find tool call spans**

Old:
```
service="toolhive" mcp.tool.name="github_search"
```

New:
```
service="toolhive" gen_ai.tool.name="github_search"
```

**Jaeger / Tempo: Find spans by MCP method**

Old:
```
service="toolhive" mcp.method="tools/call"
```

New:
```
service="toolhive" mcp.method.name="tools/call"
```

**Jaeger / Tempo: Find error spans**

New (no equivalent before):
```
service="toolhive" error.type="500"
```

**Jaeger / Tempo: Find spans by session**

New (no equivalent before):
```
service="toolhive" mcp.session.id="session-abc-123"
```

### Grafana Dashboard Variable Updates

If your Grafana dashboards use template variables that reference old attribute
names, update them as follows:

Old variable query:
```promql
label_values(toolhive_mcp_requests_total, mcp_method)
```

New variable query (standard metric):
```promql
label_values(mcp_server_operation_duration_seconds_count, mcp_method_name)
```

## Migration Checklist

Use this checklist to ensure a complete migration:

- [ ] Update PromQL queries that reference renamed span attributes in metric labels
- [ ] Update Grafana dashboard panels and variables
- [ ] Update alerting rules that filter on old attribute names
- [ ] Update trace search queries in Jaeger, Tempo, or other trace backends
- [ ] Update any custom span processors or exporters that filter by attribute name
- [ ] Verify that dashboards using legacy `toolhive_mcp_*` metrics still function
      (they should -- these metrics are unchanged)
- [ ] Consider adopting the new `mcp.server.operation.duration` and
      `mcp.server.session.duration` metrics for new dashboards

## Prometheus Metric Name Mapping

When ToolHive metrics are scraped by Prometheus (via the `/metrics` endpoint or
OTLP export), the metric names are transformed according to Prometheus naming
conventions:

| OTEL Metric Name | Prometheus Metric Name |
|---|---|
| `mcp.server.operation.duration` | `mcp_server_operation_duration_seconds` |
| `mcp.server.session.duration` | `mcp_server_session_duration_seconds` |

Attribute names are similarly transformed: dots become underscores (e.g.,
`mcp.method.name` becomes `mcp_method_name` in PromQL).

## Timeline

- **Current release**: Both legacy and standard metrics are emitted. Legacy
  metrics and their attribute names are unchanged.
- **Future release**: Legacy `toolhive_mcp_*` metrics may be deprecated. Watch
  the release notes for deprecation notices.

Migrate to the standard metrics at your earliest convenience to avoid disruption
when legacy metrics are eventually removed.

## References

- [MCP OpenTelemetry Semantic Conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/mcp/)
- [ToolHive Observability Documentation](./observability.md)
- [ToolHive Middleware Documentation](./middleware.md)

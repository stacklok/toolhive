# Telemetry Migration Guide

This guide covers the migration from ToolHive's legacy telemetry attribute names
to the new names that align with the
[OTEL MCP semantic conventions](https://github.com/open-telemetry/semantic-conventions/blob/main/docs/gen-ai/mcp.md)
and the [OTEL HTTP semantic conventions](https://opentelemetry.io/docs/specs/semconv/http/).

For the complete metrics and attributes reference, see the
[Observability and Telemetry](./observability.md) documentation and the
[Virtual MCP Server Observability](./operator/virtualmcpserver-observability.md)
documentation.

---

## What Changed

ToolHive's telemetry has been updated across three areas:

1. **Span attribute names** — Renamed to follow OTEL semantic conventions
   (HTTP, RPC, MCP/gen_ai namespaces).
2. **New metrics** — Two new histogram metrics following the OTEL MCP spec:
   `mcp.server.operation.duration` and `mcp.client.operation.duration`.
3. **Metric name and label standardization** — The legacy `toolhive_mcp_*` and
   `toolhive_vmcp_*` metric names and their label vocabulary (`server`,
   `mcp_method`, `tool`, `workflow.name`, …) have been replaced by the shared
   `stacklok.*`/OTel-semconv vocabulary (`mcp_server`, `mcp_method_name`,
   `tool_name`, `composite_tool`, …), and six legacy metric twins that
   duplicated an OTel-semconv equivalent have been deleted outright (see
   [Deleted Legacy Metrics](#deleted-legacy-metrics) below). This is a breaking
   change for any dashboard or alert querying the old metric/label names.

### What Is New

| Addition | Description |
|----------|-------------|
| `mcp.server.operation.duration` metric | OTEL MCP spec histogram for server-side operation latency |
| `mcp.client.operation.duration` metric | OTEL MCP spec histogram for vMCP-to-backend latency |
| MCP `_meta` trace context propagation | Extract/inject `traceparent`/`tracestate` from MCP `params._meta` |
| MCP request parsing middleware | Dedicated middleware extracts method, resource ID, arguments, and `_meta` |
| `--otel-custom-attributes` flag | Add custom resource attributes to all telemetry signals |
| `--otel-env-vars` flag | Include host environment variables in spans |
| `--otel-use-legacy-attributes` flag | Control legacy attribute dual emission |
| OTLP header credential redaction | `Config.String()` / `Config.GoString()` redact header values |
| `stacklok.vmcp.token_cache.requests` metric | Hit/miss counter for the vMCP token cache; not yet emitted (no production `TokenCache` implementation wired in) |

---

## Backward Compatibility

### The `useLegacyAttributes` Flag

To avoid breaking existing dashboards and alerts, ToolHive uses a **dual
emission** strategy:

| Setting | Behavior |
|---------|----------|
| `useLegacyAttributes: true` **(current default)** | Emits **both** legacy and new attribute names on every span |
| `useLegacyAttributes: false` | Emits **only** new OTEL semantic convention attribute names |

**Deprecation timeline:**
- **Current release**: Default is `true`. Both old and new attributes emitted.
- **Future release**: Default will change to `false`. Legacy attributes still
  available but opt-in.
- **Later release**: Legacy attributes removed entirely.

### How to Set the Flag

**CLI:**

```bash
thv run --otel-use-legacy-attributes=false ...
```

**Configuration file** (`~/.toolhive/config.yaml`):

```yaml
otel:
  use-legacy-attributes: false
```

**Kubernetes CRD** (MCPServer):

```yaml
spec:
  openTelemetry:
    useLegacyAttributes: false
```

**Kubernetes CRD** (VirtualMCPServer):

```yaml
spec:
  config:
    telemetry:
      useLegacyAttributes: false
```

---

## Attribute Name Mapping

### HTTP Request Attributes

| Legacy Name | New Name | Notes |
|-------------|----------|-------|
| `http.method` | `http.request.method` | Renamed for clarity |
| `http.url` | `url.full` | Moved to `url.*` namespace |
| `http.scheme` | `url.scheme` | Moved to `url.*` namespace |
| `http.host` | `server.address` | Renamed per OTEL spec |
| `http.target` | `url.path` | Moved to `url.*` namespace |
| `http.user_agent` | `user_agent.original` | Renamed per OTEL spec |
| `http.request_content_length` | `http.request.body.size` | Renamed; type changed string → int64 |
| `http.query` | `url.query` | Moved to `url.*` namespace |

### HTTP Response Attributes

| Legacy Name | New Name | Notes |
|-------------|----------|-------|
| `http.status_code` | `http.response.status_code` | Namespaced under `http.response.*` |
| `http.response_content_length` | `http.response.body.size` | Renamed |
| `http.duration_ms` | *(removed)* | Duration is captured in histogram metrics; no span attribute replacement |

### MCP Protocol Attributes

| Legacy Name | New Name | Notes |
|-------------|----------|-------|
| `mcp.method` | `mcp.method.name` | Added `.name` suffix per OTEL convention |
| `rpc.system` | `rpc.system.name` | OTEL deprecated `rpc.system` |
| `rpc.service` | *(removed)* | Value was always `"mcp"`; redundant |
| `mcp.request.id` | `jsonrpc.request.id` | Moved to `jsonrpc.*` namespace |
| `mcp.resource.id` | `mcp.resource.uri` | Renamed to reflect URI semantics; now only set for resource methods |

### Tool and Prompt Attributes

| Legacy Name | New Name | Notes |
|-------------|----------|-------|
| `mcp.tool.name` | `gen_ai.tool.name` | Moved to `gen_ai.*` namespace per OTEL MCP semconv |
| `mcp.tool.arguments` | `gen_ai.tool.call.arguments` | Moved to `gen_ai.*` namespace |
| `mcp.prompt.name` | `gen_ai.prompt.name` | Moved to `gen_ai.*` namespace |

### Transport Attributes

| Legacy Name | New Name | Notes |
|-------------|----------|-------|
| `mcp.transport` | `network.transport` + `network.protocol.name` | Split into standard OTEL network attributes |

**Mapping of `mcp.transport` values to new attributes:**

| `mcp.transport` value | `network.transport` | `network.protocol.name` |
|----------------------|---------------------|------------------------|
| `"stdio"` | `"pipe"` | *(empty)* |
| `"sse"` | `"tcp"` | `"http"` |
| `"streamable-http"` | `"tcp"` | `"http"` |

### Attributes With No Legacy Equivalent (New Only)

These attributes are new and have no legacy predecessor:

| Attribute | When Set | Description |
|-----------|----------|-------------|
| `jsonrpc.protocol.version` | MCP requests | Always `"2.0"` |
| `gen_ai.operation.name` | `tools/call` | Always `"execute_tool"` |
| `mcp.backend.protocol.version` | SSE transport | Backend protocol version |
| `network.protocol.version` | HTTP requests | HTTP protocol version (`1.1`, `2`) |
| `error.type` | HTTP 5xx errors | HTTP status code as string |
| `mcp.session.id` | Streamable HTTP | From `Mcp-Session-Id` header |
| `mcp.protocol.version` | Streamable HTTP | From `MCP-Protocol-Version` header |
| `mcp.client.name` | `initialize` | Client name from `clientInfo` |
| `mcp.is_batch` | Batch requests | Batch request indicator |
| `client.address` | All requests | Client IP address |
| `client.port` | All requests | Client port |
| `sse.event_type` | SSE connections | Always `"connection_established"` |
| `environment.{VAR}` | If configured | Host environment variable values |

---

## Migration Steps

### Step 1: Upgrade with Defaults (No Action Required)

When upgrading to this release, dual emission is enabled by default. Both old
and new attribute names appear on spans. Your existing dashboards and alerts
continue to work without changes.

### Step 2: Update Dashboards and Alerts for Renamed/Deleted Metrics

Update any PromQL, alert rule, or dashboard panel that references a legacy
metric or label name (see [Metric Name and Label Mapping](#metric-name-and-label-mapping)
and [Deleted Legacy Metrics](#deleted-legacy-metrics) below):

```promql
# Before (deleted)
rate(toolhive_mcp_requests_total{mcp_method="tools/call"}[5m])

# After: OTEL MCP spec-compliant metric for operation duration
histogram_quantile(0.95,
  rate(mcp_server_operation_duration_seconds_bucket{
    mcp_method_name="tools/call"
  }[5m])
)
```

### Step 3: Update Trace Queries

Update any trace queries (Jaeger, Tempo, Datadog, etc.) that filter on legacy
attribute names:

```
# Before
http.method = "POST" AND mcp.method = "tools/call" AND mcp.tool.name = "fetch"

# After
http.request.method = "POST" AND mcp.method.name = "tools/call" AND gen_ai.tool.name = "fetch"
```

### Step 4: Update Dashboard Panels

For Grafana dashboards that visualize span attributes, update the attribute
references using the mapping tables above. You can run both old and new queries
side-by-side during migration to verify equivalence.

### Step 5: Disable Legacy Attributes

Once all dashboards, alerts, and queries have been migrated:

```bash
thv run --otel-use-legacy-attributes=false ...
```

Or in `config.yaml`:

```yaml
otel:
  use-legacy-attributes: false
```

This reduces span size and improves performance by eliminating duplicate
attributes.

---

## Metric Name and Label Mapping

**Important**: Unlike span attributes, metric name and label changes are
**not** gated by `useLegacyAttributes` — the rename is unconditional. A
dashboard or alert querying an old metric or label name must be updated
regardless of that flag's setting.

| Legacy Metric/Label | New Metric/Label | Notes |
|----------------------|-------------------|-------|
| `server` (label, proxy + rate limit metrics) | `mcp_server` | |
| `mcp_method` (label) | `mcp_method_name` | |
| `tool` (label, `toolhive_mcp_tool_calls`) | `tool_name` | |
| `workflow.name` (label, vMCP workflow metrics) | `composite_tool` | |
| `toolhive_mcp_active_connections` | `stacklok.toolhive.proxy.active_connections` | Renamed, not deleted |
| `toolhive_vmcp_workflow_executions` | `stacklok.vmcp.composite_tool.executions` | Now split by `outcome` label instead of a separate errors counter |
| `toolhive_vmcp_workflow_errors` | `stacklok.vmcp.composite_tool.executions` (filtered to `outcome="error"`) | Merged into the executions counter above, not a standalone metric |
| `toolhive_vmcp_workflow_duration` | `stacklok.vmcp.composite_tool.duration` | |
| `toolhive_vmcp_backend_requests_duration` | `mcp.client.operation.duration` | OTEL MCP semconv histogram |
| `toolhive_vmcp_backends_discovered` | `stacklok.vmcp.mcp_server.health` | Semantic change, not a plain rename: a live per-`(mcp_server, state)` health gauge, not a fire-once discovery count |
| `toolhive_vmcp_optimizer_find_tool_requests` / `_find_tool_errors` | `stacklok.vmcp.optimizer.find_tool.requests` | Merged into one counter split by `outcome` label |
| `toolhive_vmcp_optimizer_find_tool_duration` | `stacklok.vmcp.optimizer.find_tool.duration` | |
| `toolhive_vmcp_optimizer_find_tool_results` | `stacklok.vmcp.optimizer.find_tool.results` | |
| `toolhive_vmcp_optimizer_token_savings_percent` | `stacklok.vmcp.optimizer.token_savings` | |
| `toolhive_vmcp_optimizer_call_tool_requests` / `_call_tool_errors` / `_call_tool_not_found` | `stacklok.vmcp.optimizer.call_tool.requests` | Merged into one counter split by `outcome` label (`success`, `error`, or `not_found`) |
| `toolhive_vmcp_optimizer_call_tool_duration` | `stacklok.vmcp.optimizer.call_tool.duration` | |

The new `mcp.server.operation.duration` and `mcp.client.operation.duration`
metrics use OTEL MCP semantic convention attribute names exclusively (e.g.,
`mcp.method.name` instead of `mcp_method`).

### Deleted Legacy Metrics

The following metrics duplicated an OTel-semconv equivalent and have been
deleted outright — there is no renamed successor to redirect a query to,
only the semconv metric that already covered the same signal:

| Deleted Metric | Semconv Replacement |
|-----------------|----------------------|
| `toolhive_mcp_requests` | `mcp.server.operation.duration` (request count is derivable via `_count`) |
| `toolhive_mcp_request_duration` | `mcp.server.operation.duration` |
| `toolhive_mcp_tool_calls` | `mcp.server.operation.duration` filtered to `mcp.method.name="tools/call"` |
| `toolhive_vmcp_backend_requests` | `mcp.client.operation.duration` |
| `toolhive_vmcp_backend_errors` | `mcp.client.operation.duration` filtered to `error.type != ""` |
| `toolhive_vmcp_backend_requests_duration` | `mcp.client.operation.duration` |

A dashboard or alert built on any of these six metrics has no direct
successor query — it must be rebuilt against the semconv histogram.

Full request-volume parity for `toolhive_mcp_requests` requires summing both
`mcp_server_operation_duration_seconds_count` and
`http_server_request_duration_seconds_count`: GET (SSE-open) and DELETE
(session-terminate) requests carry no MCP method and so only appear in the
latter.

---

## vMCP Backend Client Attributes

The vMCP backend client (`pkg/vmcp/internal/backendtelemetry/backendtelemetry.go`) emits both
ToolHive-specific and OTEL spec attributes on spans. These are always emitted
regardless of `useLegacyAttributes` since they serve different purposes:

| ToolHive-Specific (always emitted) | OTEL Spec (always emitted) | Description |
|------------------------------------|---------------------------|-------------|
| `target.workload_id` | — | Backend workload ID |
| `target.workload_name` | — | Backend workload name |
| `target.base_url` | — | Backend base URL |
| `target.transport_type` | — | Backend transport type |
| `action` | `mcp.method.name` | Action / MCP method |
| `tool_name` | `gen_ai.tool.name` | Tool name (for `call_tool`) |
| `resource_uri` | `mcp.resource.uri` | Resource URI (for `read_resource`) |
| `prompt_name` | `gen_ai.prompt.name` | Prompt name (for `get_prompt`) |

The `mcp.client.operation.duration` metric uses only `mcp.method.name` and
`network.transport` as labels (plus `error.type` on error), following the OTEL
MCP semantic conventions.

---

## Known Limitations

- **`error.type` is HTTP-only**: Currently set only for HTTP 5xx errors.
  JSON-RPC error codes (e.g., `-32601`) returned in HTTP 200 responses are not
  yet captured. Tracked in [#3765](https://github.com/stacklok/toolhive/issues/3765).
- **`mcp.server.session.duration` not implemented**: The OTEL MCP spec
  recommends this metric. Tracked in [#3764](https://github.com/stacklok/toolhive/issues/3764).
- **`rpc.response.status_code` not implemented**: Requires response body
  parsing. Tracked in [#3765](https://github.com/stacklok/toolhive/issues/3765).

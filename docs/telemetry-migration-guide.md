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

ToolHive's telemetry has been updated across two areas:

1. **Span attribute names** — Renamed to follow OTEL semantic conventions
   (HTTP, RPC, MCP/gen_ai namespaces).
2. **New metrics** — Two new histogram metrics following the OTEL MCP spec:
   `mcp.server.operation.duration` and `mcp.client.operation.duration`.

Existing metrics (`toolhive_mcp_requests`, `toolhive_mcp_request_duration`,
`toolhive_mcp_tool_calls`, `toolhive_mcp_active_connections`, and all
`toolhive_vmcp_*` metrics) are **unchanged** — their names and label names
remain the same.

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

### Step 2: Adopt New Metrics (Optional)

Consider adopting the new spec-compliant metrics alongside your existing ones:

```promql
# Existing metric (unchanged)
rate(toolhive_mcp_requests_total{mcp_method="tools/call"}[5m])

# New spec-compliant metric for operation duration
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

## Metric Label Changes

**Important**: The metric *label names* on existing `toolhive_mcp_*` and
`toolhive_vmcp_*` metrics have **not** changed. The `useLegacyAttributes` flag
only affects **span attributes** (trace data), not metric labels.

The new `mcp.server.operation.duration` and `mcp.client.operation.duration`
metrics use OTEL MCP semantic convention attribute names exclusively (e.g.,
`mcp.method.name` instead of `mcp_method`).

---

## vMCP Backend Client Attributes

The vMCP backend client (`pkg/vmcp/server/telemetry.go`) emits both
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

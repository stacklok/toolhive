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

ToolHive's telemetry has been updated across four areas:

1. **Span attribute names** — Renamed to follow OTEL semantic conventions
   (HTTP, RPC, MCP/gen_ai namespaces).
2. **New metrics** — Three new histogram metrics following OTEL semantic
   conventions: `mcp.server.operation.duration` and `mcp.client.operation.duration`
   (OTEL MCP spec), and `http.server.request.duration` (OTEL HTTP spec, covering
   transport-level requests that don't carry an MCP method — SSE connection opens,
   session-terminate DELETEs, etc.).
3. **Metric name and label standardization** — The legacy `toolhive_mcp_*` and
   `toolhive_vmcp_*` metric names and their label vocabulary (`server`,
   `mcp_method`, `tool`, `workflow.name`, …) have been replaced by the shared
   `stacklok.*`/OTel-semconv vocabulary (`mcp_server`, `mcp_method_name`,
   `gen_ai_tool_name`, `composite_tool`, …), and six legacy metric twins that
   duplicated an OTel-semconv equivalent have been deleted outright (see
   [Deleted Legacy Metrics](#deleted-legacy-metrics) below). Both the renamed and
   the retired names are re-emitted while `useLegacyMetrics` is on (the current
   default), so this is a scheduled deprecation rather than an immediate break —
   but every dashboard or alert querying an old metric or label name must be
   migrated before that default flips. See
   [Backward Compatibility](#backward-compatibility).

   One narrowing to be aware of: the deleted `toolhive_mcp_requests`/
   `toolhive_mcp_request_duration` twins classified any HTTP status ≥400 as an
   error. The new `http.server.request.duration` metric's `error.type` attribute
   follows OTEL HTTP semconv and is only set for status ≥500 (see
   [Known Limitations](#known-limitations)). Any dashboard or alert computing
   "error rate" from `error.type` presence will stop counting 4xx client errors
   (e.g. auth denials) after upgrade. Query `http_response_status_code=~"[45].."`
   directly instead if 4xx should still count toward error rate.
4. **D8 ownership labels hardened** — `stacklok_component`/`stacklok_product`
   are now reserved and cannot be overridden via `--otel-custom-attributes` or
   `OTEL_RESOURCE_ATTRIBUTES` (see [D8 Ownership Labels](./observability.md#d8-ownership-labels)).
   Any deployment previously setting a custom value for either key will see
   that value silently replaced by the frozen ToolHive default.

### What Is New

| Addition | Description |
|----------|-------------|
| `mcp.server.operation.duration` metric | OTEL MCP spec histogram for server-side operation latency |
| `mcp.client.operation.duration` metric | OTEL MCP spec histogram for vMCP-to-backend latency |
| `http.server.request.duration` metric | OTEL HTTP semconv histogram recorded for every request/response-cycle request, including those carrying no MCP method (session-terminate `DELETE`s). Total RPS is derivable from its `_count` series |
| `stacklok.toolhive.proxy.sse_connection.duration` metric | Duration of SSE connections, recorded once the connection closes. Separate from `http.server.request.duration` because SSE connections live for minutes to hours and would pin every observation to that histogram's 10s top bucket |
| `stacklok.build_info` metric | Build identity gauge: always observes 1, carrying `component`, `version`, and `commit` labels. Exported as `stacklok_build_info_ratio` (the `WithUnit("1")` → `_ratio` translation rule) |
| `stacklok.vmcp.mcp_server.health` metric | Live per-backend health gauge, emitting one point per `(mcp_server, state)` pair across all five health states. Semantically new — not a rename of the fire-once `toolhive_vmcp_backends_discovered` |
| `outcome` label vocabulary | `success`/`error`/`not_found` now splits counters that were previously separate metrics (workflow executions, optimizer `find_tool`/`call_tool`) |
| MCP `_meta` trace context propagation | Extract/inject `traceparent`/`tracestate` from MCP `params._meta` |
| MCP request parsing middleware | Dedicated middleware extracts method, resource ID, arguments, and `_meta` |
| `--otel-custom-attributes` flag | Add custom resource attributes to all telemetry signals |
| `--otel-env-vars` flag | Include host environment variables in spans |
| `--otel-use-legacy-attributes` flag | Control legacy attribute dual emission |
| OTLP header credential redaction | `Config.String()` / `Config.GoString()` redact header values |

---

## Backward Compatibility

Both span attributes and metrics are dual-emitted during the migration, each
behind its own flag:

| Signal | Policy |
|--------|--------|
| Span (trace) attributes | Dual-emitted behind `useLegacyAttributes`, see below |
| Metric names and labels | Dual-emitted behind `useLegacyMetrics`, see below |

A rename empties a dashboard panel exactly as a deletion does, so the renamed
metrics get the same overlap window the deleted twins had by accident — their
semconv replacements were already emitting alongside them before this release.
The window is what makes migration verifiable: an operator can run the old and
new queries side by side, confirm they agree, and only then cut over.

Dual emission is not free — each legacy alias is a second instrument with its
own series and cardinality — which is why it is a flag with a stated removal
release rather than a permanent second vocabulary.

### The `useLegacyMetrics` Flag

| Setting | Behavior |
|---------|----------|
| `useLegacyMetrics: true` **(current default)** | Emits **both** legacy and current metric names |
| `useLegacyMetrics: false` | Emits **only** the current `stacklok.*`/semconv metric names |

Set it via `--otel-use-legacy-metrics` on `thv run`, `use-legacy-metrics` in the
config file, or `openTelemetry.useLegacyMetrics` on `MCPTelemetryConfig` /
`VirtualMCPServer`.

Legacy aliases keep their original label vocabulary, not the new one — a
dashboard grouping by `server` or `workflow.name` keeps working, and one
grouping by `mcp_server` reads the current series. Counters that were merged
into an `outcome` label (workflow errors, optimizer `find_tool`/`call_tool`) are
re-emitted as the separate counters they were, so existing alerts on them still
fire.

**Deprecation timeline:**
- **Current release**: Default is `true`. Both old and new metric names emitted.
- **Future release**: Default changes to `false`. Legacy names opt-in.
- **Later release**: Legacy metric names removed entirely.

### The `useLegacyAttributes` Flag

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

### Step 1: Upgrade with Defaults

Dual emission is enabled by default for both signals: `useLegacyAttributes` puts
old and new names on every span, and `useLegacyMetrics` emits each renamed or
deleted metric under its original name alongside the current one.

So an upgrade with defaults does not break existing dashboards and alerts — but
this is a **migration window, not a permanent state**. Both flags default to
`false` in a future release and the legacy names are removed after that, so the
work in Steps 2–4 has to happen before then. Verify each rewritten query against
the still-emitted legacy series while both are live; that overlap is the whole
point of the window.

One exception to plan for: bucket boundaries are not dual-emitted. Metrics whose
names survive but whose histogram buckets changed will mix two layouts across
the upgrade, so `histogram_quantile()` over a range spanning it returns
misleading values until the range clears the boundary.

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

Update attribute and metric references using the mapping tables above. While both
flags are on, run the old and new queries side by side and confirm they agree
before deleting the old panel — that is what the migration window is for.

Two things the comparison will not catch, so check them by hand:

- **Bucket boundaries.** Not dual-emitted; a quantile panel spanning the upgrade
  mixes layouts regardless of which metric name it queries.
- **Merged counters.** Where a counter was folded into an `outcome` label, the
  legacy alias reproduces the original counter's semantics — including that it
  counts attempts rather than completions where it originally did. The current
  metric may therefore differ from its alias by design.

### Step 5: Disable Legacy Emission

Once all dashboards, alerts, and queries have been migrated, turn both flags off:

```bash
thv run --otel-use-legacy-attributes=false --otel-use-legacy-metrics=false ...
```

Or in `config.yaml`:

```yaml
otel:
  use-legacy-attributes: false
  use-legacy-metrics: false
```

The two are independent, so they can be retired separately — disable the metric
aliases once the PromQL is migrated, even if trace queries still need the legacy
attribute keys.

Disabling legacy attributes reduces span size; disabling legacy metrics drops a
whole parallel set of series, which is the larger saving of the two.

---

## Metric Name and Label Mapping

**Important**: metric names and labels are gated by `useLegacyMetrics`, not by
`useLegacyAttributes` — the two flags are independent. While `useLegacyMetrics`
is on (the current default) a dashboard querying an old metric or label name
keeps working, but it must still be migrated before that default flips.

The "New Metric/Label" column below gives the OTel instrument/attribute name
(dotted form, matching what appears in code and in OTLP export). **Query
against the "Prometheus Name" column instead** — that's what actually reaches
PromQL: dots become underscores, and Prometheus appends `_total` to counters
and `_seconds_bucket`/`_seconds_sum`/`_seconds_count` (or the instrument's
declared unit, e.g. `_percent`) to histograms. A value pasted from the "New
Metric/Label" column directly into PromQL returns zero results.

| Legacy Metric/Label | New Metric/Label | Prometheus Name | Notes |
|----------------------|-------------------|------------------|-------|
| `server` (label, proxy + rate limit metrics) | `mcp_server` | `mcp_server` | |
| `mcp_method` (label, deleted `toolhive_mcp_*` metrics) | `mcp.method.name` | `mcp_method_name` | New attribute on `mcp.server.operation.duration`, not a same-metric label rename |
| `tool` (label, deleted `toolhive_mcp_tool_calls`) | `gen_ai.tool.name` | `gen_ai_tool_name` | New attribute on `mcp.server.operation.duration`; **not** `tool_name` — that label is used only by the unrelated vMCP optimizer `call_tool` metrics (see [vMCP Backend Client Attributes](#vmcp-backend-client-attributes)) |
| `workflow.name` (label, vMCP workflow metrics) | `composite_tool` | `composite_tool` | |
| `toolhive_mcp_active_connections` | `stacklok.toolhive.proxy.active_connections` | `stacklok_toolhive_proxy_active_connections` | Renamed, not deleted. No `_total` suffix — it's an UpDownCounter, which Prometheus exposes as a gauge |
| `toolhive_rate_limit_decisions` | `stacklok.toolhive.ratelimit.decisions` | `stacklok_toolhive_ratelimit_decisions_total` | Renamed, not deleted |
| `toolhive_rate_limit_redis_errors` | `stacklok.toolhive.ratelimit.redis_errors` | `stacklok_toolhive_ratelimit_redis_errors_total` | Renamed, not deleted |
| `toolhive_rate_limit_check_latency` | `stacklok.toolhive.ratelimit.check_latency` | `stacklok_toolhive_ratelimit_check_latency_seconds_bucket` / `_sum` / `_count` | Renamed, not deleted |
| `toolhive_vmcp_workflow_executions` | `stacklok.vmcp.composite_tool.executions` | `stacklok_vmcp_composite_tool_executions_total` | Now split by `outcome` label instead of a separate errors counter |
| `toolhive_vmcp_workflow_errors` | `stacklok.vmcp.composite_tool.executions` (filtered to `outcome="error"`) | `stacklok_vmcp_composite_tool_executions_total{outcome="error"}` | Merged into the executions counter above, not a standalone metric |
| `toolhive_vmcp_workflow_duration` | `stacklok.vmcp.composite_tool.duration` | `stacklok_vmcp_composite_tool_duration_seconds_bucket` / `_sum` / `_count` | |
| `toolhive_vmcp_backends_discovered` | `stacklok.vmcp.mcp_server.health` | `stacklok_vmcp_mcp_server_health` | Semantic change, not a plain rename: a live per-`(mcp_server, state)` health gauge, not a fire-once discovery count. No suffix — it's an ObservableGauge |
| `toolhive_vmcp_optimizer_find_tool_requests` / `_find_tool_errors` | `stacklok.vmcp.optimizer.find_tool.requests` | `stacklok_vmcp_optimizer_find_tool_requests_total` | Merged into one counter split by `outcome` label |
| `toolhive_vmcp_optimizer_find_tool_duration` | `stacklok.vmcp.optimizer.find_tool.duration` | `stacklok_vmcp_optimizer_find_tool_duration_seconds_bucket` / `_sum` / `_count` | |
| `toolhive_vmcp_optimizer_find_tool_results` | `stacklok.vmcp.optimizer.find_tool.results` | `stacklok_vmcp_optimizer_find_tool_results_bucket` / `_sum` / `_count` | Unit is `{tools}` (a count), not seconds — no `_seconds` infix |
| `toolhive_vmcp_optimizer_token_savings_percent` | `stacklok.vmcp.optimizer.token_savings` | `stacklok_vmcp_optimizer_token_savings_percent_bucket` / `_sum` / `_count` | Unit `%` becomes the `_percent` infix, which happens to match the old Prometheus name exactly |
| `toolhive_vmcp_optimizer_call_tool_requests` / `_call_tool_errors` / `_call_tool_not_found` | `stacklok.vmcp.optimizer.call_tool.requests` | `stacklok_vmcp_optimizer_call_tool_requests_total` | Merged into one counter split by `outcome` label (`success`, `error`, or `not_found`) |
| `toolhive_vmcp_optimizer_call_tool_duration` | `stacklok.vmcp.optimizer.call_tool.duration` | `stacklok_vmcp_optimizer_call_tool_duration_seconds_bucket` / `_sum` / `_count` | |
| `toolhive_vmcp_backend_revision_reclassifications` | `stacklok.vmcp.backend.revision_reclassifications` | `stacklok_vmcp_backend_revision_reclassifications_total` | Renamed, not deleted |

The new `mcp.server.operation.duration` and `mcp.client.operation.duration`
metrics use OTEL MCP semantic convention attribute names exclusively (e.g.,
`mcp.method.name` instead of `mcp_method`), and expose in PromQL as
`mcp_server_operation_duration_seconds_{bucket,sum,count}` and
`mcp_client_operation_duration_seconds_{bucket,sum,count}` respectively —
see the [Deleted Legacy Metrics](#deleted-legacy-metrics) table below for
their old→new mapping.

### Deleted Legacy Metrics

The following metrics duplicated an OTel-semconv equivalent and are being
retired: there is no renamed successor to redirect a query to, only the semconv
metric that already covered the same signal. They are still emitted while
`useLegacyMetrics` is on, so a dashboard built on them keeps working through the
migration window, but they have no long-term replacement under their own name:

| Deleted Metric | Semconv Replacement |
|-----------------|----------------------|
| `toolhive_mcp_requests` | `http.server.request.duration` (total request count is derivable via `_count`; see note below) |
| `toolhive_mcp_request_duration` | `mcp.server.operation.duration` |
| `toolhive_mcp_tool_calls` | `mcp.server.operation.duration` filtered to `mcp.method.name="tools/call"` |
| `toolhive_vmcp_backend_requests` | `mcp.client.operation.duration` |
| `toolhive_vmcp_backend_errors` | `mcp.client.operation.duration` filtered to `error.type != ""` |
| `toolhive_vmcp_backend_requests_duration` | `mcp.client.operation.duration` |

A dashboard or alert built on any of these six metrics has no direct
successor query — it must be rebuilt against the semconv histogram before
`useLegacyMetrics` defaults to `false`.

For total HTTP request volume, use `http_server_request_duration_seconds_count`
alone — it is recorded for every request the middleware handles, including
GET (SSE-open) and DELETE (session-terminate) requests that carry no MCP
method. Do not also sum `mcp_server_operation_duration_seconds_count`: every
MCP-method-bearing request increments both metrics, so summing them
double-counts those requests. Use `mcp_server_operation_duration_seconds_count`
only for a per-`mcp_method_name` breakdown of the MCP-bearing subset.

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

The `mcp.client.operation.duration` metric uses `mcp.method.name`,
`network.transport`, and `mcp_server` (the backend workload name) as labels
(plus `error.type` on error), following the OTEL MCP semantic conventions.
`mcp_server` preserves the per-backend breakdown the deleted
`toolhive_vmcp_backend_requests_duration` twin had.

---

## Known Limitations

- **`error.type` is HTTP-only**: Currently set only for HTTP 5xx errors.
  JSON-RPC error codes (e.g., `-32601`) returned in HTTP 200 responses are not
  yet captured. Tracked in [#3765](https://github.com/stacklok/toolhive/issues/3765).
- **`mcp.server.session.duration` not implemented**: The OTEL MCP spec
  recommends this metric. Tracked in [#3764](https://github.com/stacklok/toolhive/issues/3764).
- **`rpc.response.status_code` not implemented**: Requires response body
  parsing. Tracked in [#3765](https://github.com/stacklok/toolhive/issues/3765).

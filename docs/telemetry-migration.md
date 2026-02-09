# Telemetry Attribute Migration Guide

This guide covers the migration from ToolHive's original telemetry attribute
names to the standardized MCP OpenTelemetry semantic conventions.

## Overview

ToolHive has updated its telemetry span attributes to align with:

- [OpenTelemetry HTTP Semantic Conventions](https://opentelemetry.io/docs/specs/semconv/http/)
- [OpenTelemetry General Conventions](https://opentelemetry.io/docs/specs/semconv/general/)
- MCP-specific conventions following the gen_ai namespace pattern

The new attribute names are always emitted. For backward compatibility, dual
emission of both old and new names is **enabled by default** in this release.
This default will change to `false` in a future release, at which point only
the new standard names will be emitted unless you explicitly opt in.

## Backward Compatibility

### Current Default

Dual emission is **enabled by default** (`--otel-use-legacy-attributes=true`) in
this release. Every span includes both the new standard names and the old legacy
names simultaneously, giving you time to migrate dashboards and alerts.

> **Deprecation notice:** In a future release, the default will change to `false`.
> Plan to update your queries to use the new attribute names.

### Disabling Dual Emission

To emit only the new standard attribute names (opt out of legacy):

**CLI flag:**

```bash
thv run --otel-use-legacy-attributes=false ...
```

**Config file:**

```yaml
otel:
  use-legacy-attributes: false
```

### Migration Steps

1. Verify dual emission is active (default in this release)
2. Update dashboards and alerts to use the new attribute names
3. Verify new queries work correctly alongside old ones
4. Disable the legacy flag with `--otel-use-legacy-attributes=false` once migration is complete

### Kubernetes Operator Deployments

The `UseLegacyAttributes` setting also applies to MCP servers managed by the
ToolHive Kubernetes operator.

**VirtualMCPServer**: Set the field in the telemetry config:

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: VirtualMCPServer
metadata:
  name: my-vmcp
spec:
  config:
    telemetry:
      useLegacyAttributes: false  # Set to false to disable dual emission
```

**MCPServer**: The operator defaults to `true` (dual emission enabled) for this
release. CRD-level configuration for this field will be added in a future release.

**Recommended upgrade path for K8s clusters:**

1. Update CRDs first (includes the new `useLegacyAttributes` field)
2. Upgrade the operator — dual emission is enabled by default
3. Migrate dashboards and alerts to the new attribute names
4. Set `useLegacyAttributes: false` on VirtualMCPServer resources

### Scope

The `--otel-use-legacy-attributes` flag only affects **trace span attributes**.
Metric label names (e.g., `mcp_method`, `status_code`, `server`) are not
affected and remain unchanged.

## Attribute Name Changes

### HTTP Attributes

| Old Name | New Name | Notes |
|----------|----------|-------|
| `http.method` | `http.request.method` | OTEL HTTP semconv |
| `http.url` | `url.full` | OTEL URL semconv |
| `http.scheme` | `url.scheme` | OTEL URL semconv |
| `http.host` | `server.address` | OTEL server semconv |
| `http.target` | `url.path` | OTEL URL semconv |
| `http.user_agent` | `user_agent.original` | OTEL user agent semconv |
| `http.request_content_length` | `http.request.body.size` | Now Int64, was string |
| `http.query` | `url.query` | OTEL URL semconv |
| `http.status_code` | `http.response.status_code` | OTEL HTTP semconv |
| `http.response_content_length` | `http.response.body.size` | OTEL HTTP semconv |
| `http.duration_ms` | *(legacy only)* | Removed from default spans; duration is captured by span timestamps and the `toolhive_mcp_request_duration` histogram metric |

### MCP Attributes

| Old Name | New Name | Notes |
|----------|----------|-------|
| `mcp.method` | `mcp.method.name` | Namespaced method name |
| `mcp.request.id` | `jsonrpc.request.id` | JSON-RPC standard |
| `mcp.resource.id` | `mcp.resource.uri` | URI semantics |
| `mcp.transport` | `network.transport` | OTEL network semconv (`pipe`, `tcp`) |
| `rpc.system` | `rpc.system` | Always emitted (value: `jsonrpc`), required by OTEL JSON-RPC semconv |
| `rpc.service` | *(legacy only)* | Available via `--otel-use-legacy-attributes`; replaced by `mcp.method.name` |

### Tool/Prompt Attributes

| Old Name | New Name | Notes |
|----------|----------|-------|
| `mcp.tool.name` | `gen_ai.tool.name` | gen_ai namespace |
| `mcp.tool.arguments` | `gen_ai.tool.call.arguments` | gen_ai namespace |
| `mcp.prompt.name` | `gen_ai.prompt.name` | gen_ai namespace |

### New Attributes (no legacy equivalent)

| Attribute | Description |
|-----------|-------------|
| `mcp.protocol.version` | MCP protocol version (e.g., `2025-06-18`) |
| `jsonrpc.protocol.version` | JSON-RPC protocol version (always `2.0`) |
| `gen_ai.operation.name` | Operation type (e.g., `execute_tool`) |
| `network.protocol.name` | Protocol name (e.g., `http`) |
| `network.protocol.version` | HTTP protocol version (e.g., `1.1`, `2`) |
| `client.address` | Client IP address |
| `client.port` | Client port number |
| `mcp.session.id` | MCP session ID from `Mcp-Session-Id` header |
| `error.type` | HTTP status code string on 5xx errors |

## Span Name Changes

Span names have been updated to be more concise:

| Old Format | New Format | Example |
|------------|------------|---------|
| `mcp.tools/call` | `tools/call {tool_name}` | `tools/call github_search` |
| `mcp.prompts/get` | `prompts/get {prompt_name}` | `prompts/get code_review` |
| `mcp.resources/read` | `resources/read` | `resources/read` |
| `mcp.tools/list` | `tools/list` | `tools/list` |

## Span Status Changes

The span status behavior has been updated to follow OTEL semantic conventions:

| HTTP Status | Old Behavior | New Behavior |
|-------------|-------------|-------------|
| 2xx/3xx | `Ok` | `Ok` |
| 4xx | `Error` | `Unset` (not a server error) |
| 5xx | `Error` | `Error` with `error.type` attribute |

Per the OTEL spec, 4xx responses are client errors and should not set the span
status to Error, as they are not indicative of server-side issues.

## PromQL Migration Examples

### Request Rate

```promql
# Old
sum(rate(toolhive_mcp_requests_total{mcp_method="tools/call"}[5m]))

# New (unchanged - metric labels not affected)
sum(rate(toolhive_mcp_requests_total{mcp_method="tools/call"}[5m]))
```

### Trace Queries

If using a trace backend with query support, update attribute filters:

```
# Old
mcp.method = "tools/call" AND mcp.tool.name = "github_search"

# New
mcp.method.name = "tools/call" AND gen_ai.tool.name = "github_search"
```

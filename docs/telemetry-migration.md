# Telemetry Attribute Migration Guide

This guide covers the migration from ToolHive's original telemetry attribute
names to the standardized MCP OpenTelemetry semantic conventions.

## Overview

ToolHive has updated its telemetry span attributes to align with:

- [OpenTelemetry HTTP Semantic Conventions](https://opentelemetry.io/docs/specs/semconv/http/)
- [OpenTelemetry General Conventions](https://opentelemetry.io/docs/specs/semconv/general/)
- MCP-specific conventions following the gen_ai namespace pattern

The new attribute names are always emitted. For backward compatibility, you can
enable dual emission of both old and new names using the `--otel-use-legacy-attributes`
flag.

## Backward Compatibility

### Enabling Dual Emission

To emit both old and new attribute names during migration:

**CLI flag:**

```bash
thv run --otel-use-legacy-attributes ...
```

**Config file:**

```yaml
otel:
  use-legacy-attributes: true
```

When enabled, every span includes both the new standard names and the old legacy
names simultaneously, allowing you to migrate dashboards and alerts gradually.

### Migration Steps

1. Enable `--otel-use-legacy-attributes` in your deployment
2. Update dashboards and alerts to use the new attribute names
3. Verify new queries work correctly alongside old ones
4. Disable the legacy flag once migration is complete

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

### MCP Attributes

| Old Name | New Name | Notes |
|----------|----------|-------|
| `mcp.method` | `mcp.method.name` | Namespaced method name |
| `mcp.request.id` | `jsonrpc.request.id` | JSON-RPC standard |
| `mcp.resource.id` | `mcp.resource.uri` | URI semantics |
| `mcp.transport` | `network.transport` | OTEL network semconv (`pipe`, `tcp`) |
| `rpc.system` | *(removed by default)* | Available via `--otel-use-legacy-attributes`; replaced by `mcp.protocol.version` |
| `rpc.service` | *(removed by default)* | Available via `--otel-use-legacy-attributes`; replaced by `mcp.method.name` |

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

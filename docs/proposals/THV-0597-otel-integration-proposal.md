# OpenTelemetry Integration Design for ToolHive MCP Server Proxies

## Problem Statement

ToolHive currently lacks observability into MCP server interactions, making it difficult to:
- Debug MCP protocol issues
- Monitor performance and reliability
- Track usage patterns and errors
- Correlate issues across the proxy-container boundary

## Goals

- Add comprehensive OpenTelemetry instrumentation to MCP server proxies
- Provide traces, metrics, and structured logging for all MCP interactions
- Maintain backward compatibility and minimal performance impact
- Support standard OTEL backends (Jaeger, Honeycomb, DataDog, etc.)

## Non-Goals

- Instrumenting MCP servers themselves (only the proxy layer)
- Custom telemetry formats or proprietary backends
- Breaking changes to existing APIs

## Architecture Overview

ToolHive uses HTTP proxies to front MCP servers running in containers:

```
Client → HTTP Proxy → Container (MCP Server)
         ↑
    OTEL Middleware
```

Two transport modes exist:
1. **SSE Transport**: `TransparentProxy` forwards HTTP directly to containers
2. **Stdio Transport**: `HTTPSSEProxy` bridges HTTP/SSE to container stdio

Both use the `types.Middleware` interface, providing a clean integration point.

## Detailed Design

### 1. Telemetry Provider (`pkg/telemetry`)

Create a new telemetry package that provides:

```go
type Config struct {
    Enabled         bool
    Endpoint        string
    ServiceName     string
    ServiceVersion  string
    SamplingRate    float64
    Headers         map[string]string
    Insecure        bool
}

type Provider struct {
    tracerProvider trace.TracerProvider
    meterProvider  metric.MeterProvider
}

func NewProvider(ctx context.Context, config Config) (*Provider, error)
func (p *Provider) Middleware() types.Middleware
func (p *Provider) Shutdown(ctx context.Context) error
```

The provider initializes OpenTelemetry with proper resource attribution and configures exporters for OTLP endpoints. It handles graceful shutdown and provides HTTP middleware for instrumentation.

### 2. HTTP Middleware Implementation

The middleware wraps HTTP handlers to provide comprehensive instrumentation:

**Request Processing:**
- Extract HTTP metadata (method, URL, headers)
- Start trace spans with semantic conventions
- Parse request bodies for MCP protocol information
- Record request metrics and active connections

**Response Processing:**
- Capture response metadata (status, size, duration)
- Record completion metrics
- Finalize spans with response attributes

**Error Handling:**
The middleware never fails the underlying request, even if telemetry operations encounter errors. All operations use timeouts and circuit breakers.

### 3. MCP Protocol Instrumentation

Enhanced instrumentation for JSON-RPC calls:

```go
func extractMCPMethod(body []byte) (method, id string, err error)
func addMCPAttributes(span trace.Span, method string, serverName string)
```

This extracts MCP-specific information like method names (tools/list, resources/read), request IDs, and error codes to provide protocol-level observability beyond HTTP metrics.

### 4. Configuration Integration

Add CLI flags to existing commands:

```bash
--otel-enabled                 # Enable OpenTelemetry
--otel-endpoint               # OTLP endpoint URL
--otel-service-name           # Service name (default: toolhive-mcp-proxy)
--otel-sampling-rate          # Trace sampling rate (0.0-1.0)
--otel-headers               # Authentication headers
--otel-insecure              # Disable TLS verification
```

Environment variable support:
```bash
TOOLHIVE_OTEL_ENABLED=true
TOOLHIVE_OTEL_ENDPOINT=https://api.honeycomb.io
TOOLHIVE_OTEL_HEADERS="x-honeycomb-team=your-api-key"
```

### 5. Integration Points

**Run Command Integration:**
The `thv run` command creates telemetry providers when OTEL is enabled and adds the middleware to the chain alongside authentication middleware.

**Proxy Command Integration:**
The standalone `thv proxy` command receives similar integration for proxy-only deployments.

**Transport Integration:**
Both SSE and stdio transports automatically inherit telemetry through the middleware system.

## Data Model

### Trace Attributes

**HTTP Layer:**
```
service.name: toolhive-mcp-proxy
service.version: 1.0.0
http.method: POST
http.url: http://localhost:8080/sse
http.status_code: 200
```

**MCP Layer:**
```
mcp.server.name: github
mcp.server.image: ghcr.io/example/github-mcp:latest
mcp.transport: sse
mcp.method: tools/list
mcp.request.id: 123
rpc.system: jsonrpc
container.id: abc123def456
```

### Metrics

```
# Request count
toolhive_mcp_requests_total{method="POST",status_code="200",mcp_method="tools/list",server="github"}

# Request duration
toolhive_mcp_request_duration_seconds{method="POST",mcp_method="tools/list",server="github"}

# Active connections
toolhive_mcp_active_connections{server="github",transport="sse"}
```

## Implementation Plan

### Phase 1: Core Infrastructure
- Create `pkg/telemetry` package
- Implement basic HTTP middleware
- Add CLI flags and configuration
- Integration with `run` and `proxy` commands

### Phase 2: MCP Protocol Support
- JSON-RPC message parsing
- MCP-specific span attributes
- Enhanced metrics with MCP context

### Phase 3: Production Readiness
- Performance optimization
- Error handling and graceful degradation
- Documentation and examples

### Phase 4: Advanced Features
- Custom dashboards and alerts
- Sampling strategies
- Advanced correlation features

## Security Considerations

- **Data Sanitization**: Exclude sensitive headers and request bodies from traces
- **Sampling**: Default to 10% sampling to control costs and overhead
- **Authentication**: Support standard OTLP authentication headers
- **Graceful Degradation**: Continue normal operation if telemetry endpoint is unavailable

## Implementation Benefits

1. **Non-Intrusive**: Leverages existing middleware system without architectural changes
2. **Comprehensive Coverage**: Captures all MCP traffic through proxy instrumentation
3. **Flexible Configuration**: Supports various OTEL backends
4. **Production Ready**: Includes sampling, authentication, and graceful degradation
5. **MCP-Aware**: Provides protocol-specific insights beyond generic HTTP metrics

## Success Metrics

- Zero performance regression in proxy throughput
- Complete trace coverage for all MCP interactions
- Successful integration with major OTEL backends
- Positive feedback from operators on debugging capabilities

## Alternatives Considered

1. **Container-level instrumentation**: Rejected due to complexity and MCP server diversity
2. **Custom telemetry format**: Rejected in favor of OTEL standards
3. **Sidecar approach**: Rejected due to deployment complexity

The middleware-based approach is elegant because it leverages existing infrastructure while providing comprehensive observability. OpenTelemetry packages are already available as indirect dependencies, so no major dependency changes are required.

## Prometheus Integration

Prometheus can be integrated with this OpenTelemetry design through multiple pathways:

### 1. OTEL Collector → Prometheus (Recommended)

The most robust approach uses the OpenTelemetry Collector as an intermediary:

```
ToolHive Proxy → OTEL Collector → Prometheus
```

**How it works:**
- ToolHive sends metrics via OTLP to an OTEL Collector
- The collector exports metrics to Prometheus using the `prometheusexporter`
- Prometheus scrapes the collector's `/metrics` endpoint

**Benefits:**
- Centralized metric processing and transformation
- Can aggregate metrics from multiple ToolHive instances
- Supports metric filtering, renaming, and enrichment
- Provides a buffer if Prometheus is temporarily unavailable

### 2. Direct Prometheus Exporter

ToolHive could expose metrics directly via a Prometheus endpoint:

```
ToolHive Proxy → Prometheus (direct scrape)
```

**Implementation:**
- Add a Prometheus exporter alongside the OTLP exporter
- Expose `/metrics` endpoint on each proxy instance
- Configure Prometheus to scrape ToolHive instances directly

**Configuration Addition:**
```bash
--prometheus-enabled          # Enable Prometheus metrics endpoint
--prometheus-port 9090       # Port for /metrics endpoint
--prometheus-path /metrics   # Metrics endpoint path
```

### 3. Prometheus Metric Examples

Metrics would follow Prometheus conventions:

```prometheus
# Counter metrics
toolhive_mcp_requests_total{server="github",method="tools_list",status="success",transport="sse"} 42

# Histogram metrics  
toolhive_mcp_request_duration_seconds_bucket{server="github",method="tools_list",le="0.1"} 10
toolhive_mcp_request_duration_seconds_sum{server="github",method="tools_list"} 12.5
toolhive_mcp_request_duration_seconds_count{server="github",method="tools_list"} 42

# Gauge metrics
toolhive_mcp_active_connections{server="github",transport="sse"} 5
```

### 4. Example: tools/call Trace and Metrics

Here's how a `tools/call` MCP method would appear in traces and metrics:

**Distributed Trace:**
```
Span: mcp.proxy.request
├── service.name: toolhive-mcp-proxy
├── service.version: 1.0.0
├── http.method: POST
├── http.url: http://localhost:8080/messages?session_id=abc123
├── http.status_code: 200
├── mcp.server.name: github
├── mcp.server.image: ghcr.io/example/github-mcp:latest
├── mcp.transport: sse
├── container.id: container_abc123
└── Child Span: mcp.tools/call
    ├── mcp.method: tools/call
    ├── mcp.request.id: req_456
    ├── rpc.system: jsonrpc
    ├── rpc.service: mcp
    ├── mcp.tool.name: create_issue
    ├── mcp.tool.arguments: {"title":"Bug report","body":"..."}
    ├── span.kind: client
    └── duration: 1.2s
```

**Prometheus Metrics:**
```prometheus
# Request count for tools/call
toolhive_mcp_requests_total{server="github",method="tools_call",status="success",transport="sse"} 15

# Duration histogram for tools/call
toolhive_mcp_request_duration_seconds_bucket{server="github",method="tools_call",le="0.5"} 8
toolhive_mcp_request_duration_seconds_bucket{server="github",method="tools_call",le="1.0"} 12
toolhive_mcp_request_duration_seconds_bucket{server="github",method="tools_call",le="2.0"} 15
toolhive_mcp_request_duration_seconds_sum{server="github",method="tools_call"} 18.5
toolhive_mcp_request_duration_seconds_count{server="github",method="tools_call"} 15

# Tool-specific metrics
toolhive_mcp_tool_calls_total{server="github",tool="create_issue",status="success"} 8
toolhive_mcp_tool_calls_total{server="github",tool="create_issue",status="error"} 2
```

**JSON-RPC Request Body (parsed for instrumentation):**
```json
{
  "jsonrpc": "2.0",
  "id": "req_456",
  "method": "tools/call",
  "params": {
    "name": "create_issue",
    "arguments": {
      "title": "Bug report",
      "body": "Found an issue with the API"
    }
  }
}
```

This provides rich observability showing:
- HTTP-level metrics (request count, duration, status)
- MCP protocol details (method, tool name, request ID)
- Tool-specific usage patterns
- Error rates per tool
- Performance characteristics of different tools

### 4. Recommended Approach

Start with **OTEL Collector integration** because:
- Future-proof: Works with any observability backend
- Scalable: Centralized metric processing
- Flexible: Can add Prometheus direct export later if needed
- Standard: Follows OpenTelemetry best practices

The implementation would add a configuration option:

```bash
--otel-exporter otlp          # Send to OTEL Collector (default)
--otel-exporter prometheus    # Direct Prometheus export
--otel-exporter both          # Both OTLP and Prometheus
```

# Virtual MCP Server Observability

This document describes the observability for the Virtual MCP
Server (vMCP), which aggregates multiple backend MCP servers into a unified
interface. The vMCP provides OpenTelemetry-based instrumentation for monitoring
backend operations and composite tool workflow executions.

For general ToolHive observability concepts and proxy runner telemetry, see the
main [Observability and Telemetry](../observability.md) documentation.

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

The vMCP emits metrics for backend operations and workflow executions. All
metrics use the `toolhive_vmcp_` prefix.

**Backend metrics** track requests to individual backend MCP servers, including
request counts, error counts, and request duration histograms. These metrics
include attributes identifying the target backend (workload ID, name, URL,
transport type) and the action being performed (tool call, resource read, etc.).

**Workflow metrics** track composite tool workflow executions, including
execution counts, error counts, and duration histograms. These metrics include
the workflow name as an attribute.

## Distributed Tracing

The vMCP creates spans for each individual backend operation as well as workflow executions, enabling the attribution of workflow execution errors or latency to specific tool calls.


## Configuration

Configure telemetry in the `VirtualMCPServer` resource using the `spec.telemetry`
field. The telemetry configuration uses the same `TelemetryConfig` type as
`MCPServer`, providing a consistent configuration experience across resources.

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: VirtualMCPServer
metadata:
  name: my-vmcp
spec:
  config:
    groupRef: my-group
  incomingAuth:
    type: anonymous
  telemetry:
    openTelemetry:
      enabled: true
      endpoint: "otel-collector:4317"
      serviceName: "my-vmcp"
      insecure: true
      tracing:
        enabled: true
        samplingRate: "0.1"
      metrics:
        enabled: true
    prometheus:
      enabled: true
```

See the [VirtualMCPServer API reference](./virtualmcpserver-api.md) for complete
CRD documentation.

## Related Documentation

- [Observability and Telemetry](../observability.md) - Main ToolHive observability documentation
- [VirtualMCPServer API Reference](./virtualmcpserver-api.md) - Complete CRD specification

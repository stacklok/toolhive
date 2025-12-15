# Observability and Telemetry

This document describes the observability architecture implemented in ToolHive
for monitoring MCP (Model Context Protocol) server interactions. ToolHive
provides OpenTelemetry-based instrumentation with support for distributed
tracing, metrics collection, and structured logging.

This document is intended for developers working on ToolHive. For user guides on
setting up and using these features, see the ToolHive documentation:

- [Observability overview](https://docs.stacklok.com/toolhive/concepts/observability),
  including trace structure and example metrics
- [CLI guide](https://docs.stacklok.com/toolhive/guides-cli/telemetry-and-metrics),
  including how to enable and configure telemetry and send to common backends

## Overview

ToolHive's observability stack provides complete visibility into MCP proxy
operations through:

1. **Distributed tracing**: Track requests across the proxy-container boundary
   with OpenTelemetry traces
2. **Metrics collection**: Monitor performance, usage patterns, and error rates
   with Prometheus and OTLP metrics
3. **Structured logging**: Capture detailed audit events for compliance and
   debugging
4. **Protocol-aware instrumentation**: MCP-specific insights beyond generic HTTP
   metrics

See [the original design document](./proposals/otel-integration-proposal.md) for
more details on the design and goals of this observability architecture.

## Architecture

```mermaid
graph TD
    A[MCP Client] --> B[ToolHive Proxy Runner]
    B --> C[Container MCP Server]

    B --> D[OpenTelemetry Middleware]
    D --> E[Trace Exporter]
    D --> F[Metrics Exporter]

    E --> G[OTLP Endpoint]
    E --> H[Jaeger]
    E --> I[DataDog]

    F --> J[Prometheus /metrics]
    F --> K[OTLP Metrics]

    G --> L[Observability Backend]
    K --> L
    J --> M[Prometheus Server]

    classDef toolhive fill:#EDD9A3,color:#000;
    classDef external fill:#7AB7FF,color:#000;
    class B,D toolhive;
    class L,M external;
```

## Integration with Existing Middleware

The OpenTelemetry middleware integrates seamlessly with ToolHive's
[existing middleware stack](./middleware.md):

```mermaid
graph TD
    A[HTTP Request] --> B[Authentication Middleware]
    B --> C[MCP Parsing Middleware]
    C --> D[OpenTelemetry Middleware]
    D --> E[Authorization Middleware]
    E --> F[Audit Middleware]
    F --> G[MCP Server Handler]

    style D fill:#EDD9A3,color:#000;
```

The telemetry middleware:

- **Leverages parsed MCP data** from the parsing middleware
- **Includes authentication context** from JWT claims
- **Captures authorization decisions** for compliance
- **Correlates with audit events** for complete observability

This provides end-to-end visibility across the entire request lifecycle while
maintaining the modular architecture of ToolHive's middleware system.

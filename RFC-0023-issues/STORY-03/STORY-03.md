# MCPTelemetryConfig CRD — Types, Controller, and Tests

**As a** platform engineer managing observability infrastructure,
**I want** to define telemetry configuration once in an `MCPTelemetryConfig` resource,
**so that** all MCP servers share consistent collector endpoints and sampling settings without duplication.

**Size**: L
**Dependencies**: None
**Labels**: `operator`, `api`, `telemetry`

## Context

Telemetry configurations (collector endpoint, sampling rate, headers) are duplicated across MCPServer resources. The CRD type `OpenTelemetryConfig` and the application type `telemetry.Config` have different shapes — nested vs flat struct, `[]string` vs `map[string]string` for headers — requiring manual conversion code that has historically produced silent bugs (PR #3118).

This story creates a dedicated `MCPTelemetryConfig` CRD that embeds `telemetry.Config` from `pkg/telemetry/config.go` directly, eliminating the conversion layer.

## Acceptance Criteria

- [ ] `MCPTelemetryConfig` CRD is defined, embedding `telemetry.Config` from `pkg/telemetry/config.go`
- [ ] CLI-only fields (`EnvironmentVariables`, `CustomAttributes`) are excluded from the CRD spec
- [ ] `Headers` uses `map[string]string` (matching the application type, not `[]string`)
- [ ] `sensitiveHeaders` field supports `SecretKeyRef` for credential headers (no inline secrets)
- [ ] Status tracks `observedGeneration`, `configHash`, `referencingServers`, and `conditions`
- [ ] Controller follows same pattern as MCPOIDCConfig (finalizer, ref tracking, cascade, deletion protection)
- [ ] CRD manifest included in `operator-crds` Helm chart
- [ ] Unit tests cover type embedding, controller reconciliation, and sensitive header handling
- [ ] Integration tests in `cmd/thv-operator/test-integration/` cover cross-CRD reference lifecycle, cascade, and deletion protection

## Sub-Issues

| ID | Title |
|---|---|
| [03-A](03-A.md) | Define `MCPTelemetryConfig` API types with `telemetry.Config` embedding (with type tests) |
| [03-B](03-B.md) | Define `TelemetryConfigWithRef` struct for workload references |
| [03-C](03-C.md) | Implement `MCPTelemetryConfig` controller (with unit and integration tests) |
| [03-E](03-E.md) | Helm chart: MCPTelemetryConfig CRD template |

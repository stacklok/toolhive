# Workload CRD Config Reference Updates

**As a** platform engineer deploying MCP servers,
**I want** MCPServer, MCPRemoteEndpoint, and VirtualMCPServer to reference shared `MCPOIDCConfig` and `MCPTelemetryConfig` resources,
**so that** workloads use centralized configuration without duplication.

**Size**: XL
**Dependencies**: STORY-02 (MCPOIDCConfig), STORY-03 (MCPTelemetryConfig)
**Labels**: `operator`, `api`, `controller`

## Context

With MCPOIDCConfig and MCPTelemetryConfig CRDs available (STORY-02, STORY-03), workload CRDs need to be updated to reference them. Each workload can either reference a shared config CRD or specify inline configuration. Workload controllers must resolve references, merge per-server overrides, and fail closed if a referenced config is missing or not ready.

## Acceptance Criteria

- [ ] MCPServer spec accepts `oidcConfig.ref` pointing to an MCPOIDCConfig with per-server `audience` and `scopes` overrides
- [ ] MCPServer spec accepts `telemetryConfig.ref` pointing to an MCPTelemetryConfig with per-server `serviceName`
- [ ] Inline config remains supported — CEL enforces `ref` and `inline` are mutually exclusive
- [ ] MCPRemoteEndpoint spec accepts the same config ref patterns
- [ ] VirtualMCPServer spec accepts `oidcConfigRef` for incoming auth
- [ ] MCPServer controller resolves config refs, merges per-server overrides, and passes resolved config to workload
- [ ] MCPRemoteEndpoint controller resolves config refs
- [ ] VirtualMCPServer controller resolves OIDC config ref
- [ ] All workload controllers fail closed: if a referenced config CRD does not exist or is not `Ready`, the workload enters `Failed` phase with a descriptive condition
- [ ] Config hash annotations are tracked in workload status for change detection
- [ ] Default audience generation (`{kind}/{namespace}/{name}`) works when audience is not specified
- [ ] Unit tests cover ref resolution, inline fallback, fail-closed behavior, and override merging
- [ ] Integration tests in `cmd/thv-operator/test-integration/` cover cross-CRD reference resolution for all workload types
- [ ] Architecture documentation (`docs/arch/09-operator-architecture.md`) updated with new reference patterns
- [ ] Helm chart RBAC examples updated for platform team / app team separation
- [ ] Example manifests and migration guide provided

## Sub-Issues

| ID | Title |
|---|---|
| [07-A](07-A.md) | Update MCPServer types with config refs |
| [07-B](07-B.md) | Update MCPServer controller for config ref resolution (with unit and integration tests) |
| [07-C](07-C.md) | Update MCPRemoteEndpoint types and controller (with tests) |
| [07-D](07-D.md) | Update VirtualMCPServer types and controller (with tests) |
| [07-F](07-F.md) | Documentation, Helm, and migration guide |

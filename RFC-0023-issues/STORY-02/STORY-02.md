# MCPOIDCConfig CRD — Types, Controller, and Tests

**As a** platform engineer managing multiple MCP servers with the same identity provider,
**I want** to define OIDC configuration once in an `MCPOIDCConfig` resource and reference it from multiple workloads,
**so that** I can update issuer settings in one place instead of duplicating config across every MCPServer.

**Size**: L
**Dependencies**: None
**Labels**: `operator`, `api`, `oidc`

## Context

OIDC configurations are defined inline in multiple CRDs (MCPServer, MCPRemoteProxy, VirtualMCPServer). Platform teams managing 10+ MCPServer resources with the same OIDC provider must duplicate the full config in each resource. A single issuer URL change requires updating every resource.

This story extracts shared OIDC configuration into a dedicated reusable CRD following the pattern established by `MCPExternalAuthConfig`.

## Acceptance Criteria

- [ ] `MCPOIDCConfig` CRD is defined with `inline`, `configMapRef`, and `kubernetesServiceAccount` variants
- [ ] CEL validation enforces exactly one variant is set (no `type` discriminator field)
- [ ] Status tracks `observedGeneration`, `configHash`, `referencingServers`, and standard `conditions`
- [ ] Controller adds finalizer on first reconciliation
- [ ] Controller tracks referencing workloads in `status.referencingServers`
- [ ] Controller blocks deletion while references exist (finalizer pattern)
- [ ] Controller cascades config changes to referencing workloads via annotation (`toolhive.stacklok.dev/oidcconfig-hash`)
- [ ] Controller sets `Ready` condition based on config validation
- [ ] Controller emits Warning event when duplicate audiences are detected across referencing workloads
- [ ] CRD manifest is generated and included in `operator-crds` Helm chart
- [ ] Unit tests cover type validation, controller reconciliation, finalizer logic, cascade, and audience uniqueness
- [ ] Integration tests in `cmd/thv-operator/test-integration/` cover cross-CRD reference lifecycle, cascade reconciliation, and deletion protection

## Sub-Issues

| ID | Title |
|---|---|
| [02-A](02-A.md) | Define `MCPOIDCConfig` API types (with CEL validation tests) |
| [02-B](02-B.md) | Define `OIDCConfigWithRef` struct for workload references |
| [02-C](02-C.md) | Implement `MCPOIDCConfig` controller (with unit and integration tests) |
| [02-D](02-D.md) | Audience uniqueness warning (with tests) |
| [02-F](02-F.md) | Helm chart: MCPOIDCConfig CRD template |
